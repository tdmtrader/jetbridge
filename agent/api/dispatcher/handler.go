// Package dispatcher serves the dispatcher runtime-control API:
// GET  /api/v1/agent/dispatcher (GetAgentDispatcher — any authenticated user)
// PUT  /api/v1/agent/dispatcher (SetAgentDispatcher — admin only).
//
// The mode the dispatcher loop honors is the singleton agent_settings row and
// nothing else — there is no boot flag behind it. Migration 1773106137 seeds
// that row, so the mode always has an author and a timestamp. Authentication
// and admin authorization are enforced by the wrappa layer
// (atc/wrappa/api_auth_wrappa.go); the handler trusts the request.
package dispatcher

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/concourse/concourse/agent/dispatch"
)

// Store is the persistence seam. db.AgentSettingsFactory satisfies it; the
// tests use a memory store. GetDispatcherSetting returns found=false only if
// the seeded singleton row was deleted, which every reader treats as off.
type Store interface {
	GetDispatcherSetting() (mode string, updatedAt time.Time, updatedBy string, found bool, err error)
	SetDispatcherMode(mode, updatedBy string) error
}

// UserNameFunc derives the requesting user's username (injected from the
// accessor by atc/api, mirroring principals.UserNameFunc — this package never
// imports atc).
type UserNameFunc func(*http.Request) string

// Handler serves the dispatcher status/control routes.
type Handler struct {
	store    Store
	userName UserNameFunc
}

// NewHandler wires the settings store and the requester-identity func.
func NewHandler(store Store, userName UserNameFunc) *Handler {
	return &Handler{store: store, userName: userName}
}

// Response is the GET/PUT body: the mode the loop honors now, plus who set it
// and when. updated_at/updated_by are null only if the seeded row was deleted.
type Response struct {
	Mode      string  `json:"mode"`
	UpdatedAt *string `json:"updated_at"`
	UpdatedBy *string `json:"updated_by"`
}

func (h *Handler) currentState() (Response, error) {
	mode, updatedAt, updatedBy, found, err := h.store.GetDispatcherSetting()
	if err != nil {
		return Response{}, err
	}
	resp := Response{Mode: dispatch.ResolveEffectiveMode(found, mode)}
	if found {
		at := updatedAt.UTC().Format(time.RFC3339)
		resp.UpdatedAt = &at
		by := updatedBy
		resp.UpdatedBy = &by
	}
	return resp, nil
}

// Get handles GET /api/v1/agent/dispatcher.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	resp, err := h.currentState()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// Set handles PUT /api/v1/agent/dispatcher.
func (h *Handler) Set(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if !dispatch.ValidMode(body.Mode) {
		http.Error(w, "mode must be one of off|paused|active", http.StatusBadRequest)
		return
	}

	identity := h.userName(r)
	if identity == "" {
		// Record an honest sentinel rather than masquerading as a real
		// "admin" — updated_by is display/audit provenance for a
		// security-relevant control, so don't fabricate an actor.
		identity = "unknown"
	}
	if err := h.store.SetDispatcherMode(body.Mode, identity); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp, err := h.currentState()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
