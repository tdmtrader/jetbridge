package principals

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// UserNameFunc derives the requesting admin's username (injected from
// the accessor by atc/api, mirroring reviews.BuildLookupFunc — this
// package never imports atc).
type UserNameFunc func(*http.Request) string

// Handler serves the admin principal-management API. Authentication and
// admin authorization are enforced by the wrappa layer (CheckAdminHandler,
// see atc/wrappa/api_auth_wrappa.go); the handler trusts the request.
type Handler struct {
	store    Store
	userName UserNameFunc
}

func NewHandler(store Store, userName UserNameFunc) *Handler {
	return &Handler{store: store, userName: userName}
}

// CreatedResponse is the POST response: the principal plus the full
// token, surfaced exactly once.
type CreatedResponse struct {
	Principal
	Token string `json:"token"`
}

// CreatePrincipal handles POST /api/v1/agent/principals.
func (h *Handler) CreatePrincipal(w http.ResponseWriter, r *http.Request) {
	var spec CreateSpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if spec.TeamName == "" {
		spec.TeamName = "main"
	}
	spec.CreatedBy = h.userName(r)
	if err := spec.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	p, token, err := h.store.Create(spec)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(CreatedResponse{Principal: p, Token: token})
}

// ListPrincipals handles GET /api/v1/agent/principals. Kind is derived
// read-side (ticket #44) rather than stored, so this is the only place
// principal rows are classified as operator vs. run.
func (h *Handler) ListPrincipals(w http.ResponseWriter, r *http.Request) {
	list, err := h.store.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for i := range list {
		list[i].Kind = DeriveKind(list[i].Name)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

// RevokePrincipal handles DELETE /api/v1/agent/principals/:principal_id.
func (h *Handler) RevokePrincipal(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.FormValue(":principal_id"))
	if err != nil {
		http.Error(w, "invalid principal_id", http.StatusBadRequest)
		return
	}

	found, err := h.store.Revoke(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "principal not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
