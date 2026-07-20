// Package dispatcher serves the dispatcher runtime-control API:
// GET  /api/v1/agent/dispatcher (GetAgentDispatcher — any authenticated user)
// PUT  /api/v1/agent/dispatcher (SetAgentDispatcher — admin only).
//
// The effective mode the dispatcher loop honors is resolved from the singleton
// agent_settings row, falling back to the --agent-dispatcher-enabled boot flag
// when no row exists. Authentication and admin authorization are enforced by
// the wrappa layer (atc/wrappa/api_auth_wrappa.go); the handler trusts the
// request.
package dispatcher

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/concourse/concourse/agent/dispatch"
)

// errInvalidMode is returned by the memory store for a mode outside
// {off,paused,active} (the db factory returns its own ErrInvalidDispatcherMode).
var errInvalidMode = errors.New("dispatcher mode must be one of off|paused|active")

// Store is the persistence seam. db.AgentSettingsFactory satisfies it; the
// tests use a memory store. GetDispatcherSetting returns found=false when no
// row exists yet (fall back to the boot default).
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
	bootFlag bool
	userName UserNameFunc
}

// NewHandler wires the store, the --agent-dispatcher-enabled boot flag (the
// fallback seed shown as boot_default and used when no setting row exists), and
// the requester-identity func.
func NewHandler(store Store, bootFlag bool, userName UserNameFunc) *Handler {
	return &Handler{store: store, bootFlag: bootFlag, userName: userName}
}

// Response is the GET/PUT body. mode is the EFFECTIVE mode the loop honors now;
// source is "setting" when it came from the agent_settings row, "boot-default"
// when it fell back to the boot flag; boot_default is what the flag resolves to
// (shown so the UI can explain the fallback). updated_at/updated_by are null
// until a mode is set at runtime.
type Response struct {
	Mode        string  `json:"mode"`
	Source      string  `json:"source"`
	UpdatedAt   *string `json:"updated_at"`
	UpdatedBy   *string `json:"updated_by"`
	BootDefault string  `json:"boot_default"`
}

func (h *Handler) bootDefaultMode() string {
	if h.bootFlag {
		return dispatch.ModeActive
	}
	return dispatch.ModeOff
}

func (h *Handler) currentState() (Response, error) {
	mode, updatedAt, updatedBy, found, err := h.store.GetDispatcherSetting()
	if err != nil {
		return Response{}, err
	}
	resp := Response{
		Mode:        dispatch.ResolveEffectiveMode(found, mode, h.bootFlag),
		Source:      "boot-default",
		BootDefault: h.bootDefaultMode(),
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
