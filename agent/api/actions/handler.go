// Package actions serves the cluster-wide action-suppression API:
// GET  /api/v1/agent/actions (GetAgentActions — any authenticated user)
// PUT  /api/v1/agent/actions (SetAgentActions — admin only).
//
// The switch stops every EXTERNAL side effect (publisher writes) without a
// redeploy. It does not gate dispatch, agent execution, or sealing.
// Authentication and admin authorization are enforced by the wrappa layer
// (atc/wrappa/api_auth_wrappa.go); the handler trusts the request.
package actions

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/concourse/concourse/agent/publisher"
)

// Store is the persistence seam. db.AgentSettingsFactory satisfies it; the
// tests use a memory store. found=false means no admin has engaged the switch.
type Store interface {
	GetActionsSetting() (mode string, updatedAt time.Time, updatedBy string, found bool, err error)
	SetActionsMode(mode, updatedBy string) error
}

// UserNameFunc derives the requesting user's username (injected from the
// accessor by atc/api; this package never imports atc/api).
type UserNameFunc func(*http.Request) string

type Handler struct {
	store    Store
	userName UserNameFunc
}

func NewHandler(store Store, userName UserNameFunc) *Handler {
	return &Handler{store: store, userName: userName}
}

// Response is the GET/PUT body. mode is the EFFECTIVE mode every publisher
// honors now; source is "setting" when an admin set it and "default" when the
// switch has never been engaged. Note there is no boot flag: unlike the
// dispatcher, the switch has exactly one unset meaning — active.
type Response struct {
	Mode      string  `json:"mode"`
	Source    string  `json:"source"`
	UpdatedAt *string `json:"updated_at"`
	UpdatedBy *string `json:"updated_by"`
}

func (h *Handler) currentState() (Response, error) {
	mode, updatedAt, updatedBy, found, err := h.store.GetActionsSetting()
	if err != nil {
		// Deliberately NOT publisher.EffectiveActionsMode(…, err): the
		// fail-safe "suppressed" answer is for ENFORCEMENT. A reader asking
		// what the switch says must get an error, not a guess in either
		// direction.
		return Response{}, err
	}
	resp := Response{
		Mode:   publisher.EffectiveActionsMode(mode, found, nil),
		Source: "default",
	}
	if found {
		resp.Source = "setting"
		at := updatedAt.UTC().Format(time.RFC3339)
		resp.UpdatedAt = &at
		by := updatedBy
		resp.UpdatedBy = &by
	}
	return resp, nil
}

// Get handles GET /api/v1/agent/actions.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	resp, err := h.currentState()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// Set handles PUT /api/v1/agent/actions.
func (h *Handler) Set(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if !publisher.ValidActionsMode(body.Mode) {
		http.Error(w, "mode must be one of active|suppressed", http.StatusBadRequest)
		return
	}

	identity := h.userName(r)
	if identity == "" {
		// Record an honest sentinel rather than fabricating an actor for a
		// security-relevant control.
		identity = "unknown"
	}
	if err := h.store.SetActionsMode(body.Mode, identity); err != nil {
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
