package credentials

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

// ClaimsFunc extracts the caller's identity from the request. Wired in
// atc/api/handler.go from accessor claims (sub + preferred username +
// admin); injected as a function to keep this package free of atc imports.
type ClaimsFunc func(r *http.Request) (sub string, userName string, isAdmin bool, ok bool)

// Handler serves the self-scoped credential vault API.
type Handler struct {
	backend Backend
	claims  ClaimsFunc
}

func NewHandler(backend Backend, claims ClaimsFunc) *Handler {
	return &Handler{backend: backend, claims: claims}
}

// resolveTarget resolves the users row a request operates on: the caller's
// own row, or — for admins that requested PlatformUserName — the
// service user's row (the only permitted non-self target).
func (h *Handler) resolveTarget(w http.ResponseWriter, r *http.Request, requested string) (int, string, bool) {
	sub, claimName, isAdmin, ok := h.claims(r)
	if !ok || sub == "" {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return 0, "", false
	}
	switch requested {
	case "":
		// self-scoped
	case PlatformUserName:
		if !isAdmin {
			http.Error(w, "only admins may manage the platform credential", http.StatusForbidden)
			return 0, "", false
		}
		sub, claimName = PlatformUserSub, PlatformUserName
	default:
		http.Error(w, `user must be omitted or "platform"`, http.StatusBadRequest)
		return 0, "", false
	}
	userID, userName, found, err := h.backend.UserBySub(sub)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return 0, "", false
	}
	if !found {
		http.Error(w, "no user record for this token; log in to this Concourse first", http.StatusNotFound)
		return 0, "", false
	}
	if userName == "" {
		userName = claimName
	}
	return userID, userName, true
}

// Set handles PUT /api/v1/agent/user-credentials (self only; admins may
// pass "user":"platform" to vault the platform service user's credential).
func (h *Handler) Set(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "request body exceeds 1MB", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	var req PutRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := req.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	userID, userName, ok := h.resolveTarget(w, r, req.User)
	if !ok {
		return
	}

	var expiresAt time.Time
	if req.ExpiresAt > 0 {
		expiresAt = time.Unix(req.ExpiresAt, 0)
	}
	if err := h.backend.Put(userID, userName, req.Kind, req.Token, expiresAt); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":     "saved",
		"kind":       req.Kind,
		"expires_at": req.ExpiresAt,
	})
}

// Status handles GET /api/v1/agent/user-credentials (self only; no
// tokens; admins may pass ?user=platform).
func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	userID, _, ok := h.resolveTarget(w, r, r.URL.Query().Get("user"))
	if !ok {
		return
	}
	creds, err := h.backend.Status(userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if creds == nil {
		creds = []Credential{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(creds)
}

// Delete handles DELETE /api/v1/agent/user-credentials/:kind (self only;
// admins may pass ?user=platform).
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	userID, _, ok := h.resolveTarget(w, r, r.URL.Query().Get("user"))
	if !ok {
		return
	}
	kind := r.FormValue(":kind")
	if !ValidKind(kind) {
		http.Error(w, "unknown credential kind", http.StatusBadRequest)
		return
	}
	if err := h.backend.Delete(userID, kind); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
