package tickets

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
)

// UserNameFunc resolves the authenticated human username for a request
// ("" when anonymous). Injected because this package must not import
// atc/api/accessor (accessor imports atc/db, which imports this
// package via AgentTicketsFactory — a cycle). atc/api/handler.go wires
// accessor.GetAccessor(r).Claims().UserName.
type UserNameFunc func(r *http.Request) string

// Handler serves the /api/v1/agent/tickets* routes. Auth is
// enforced by the wrappa tiers (00-shared-contracts.md §4.2); this
// handler only reads WHO the verified writer is.
type Handler struct {
	store    Store
	userName UserNameFunc
}

func NewHandler(store Store, userName UserNameFunc) *Handler {
	return &Handler{store: store, userName: userName}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func readJSON(w http.ResponseWriter, r *http.Request, into any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return false
	}
	if err := json.Unmarshal(body, into); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func ticketIDParam(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.FormValue(":ticket_id"))
	if err != nil || id <= 0 {
		http.Error(w, "invalid ticket_id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// CreateTicket handles POST /api/v1/agent/tickets.
//
// Ticket creation is an ordinary human main-team action. Human-created tickets
// use web or fly attribution only.
func (h *Handler) CreateTicket(w http.ResponseWriter, r *http.Request) {
	var req CreateRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Title == "" {
		http.Error(w, "title is required", http.StatusBadRequest)
		return
	}
	if req.Repo == "" {
		http.Error(w, "repo is required", http.StatusBadRequest)
		return
	}
	origin := req.Origin
	if origin == "" {
		origin = "web"
	}
	if !ValidOrigin(origin) {
		http.Error(w, "invalid origin", http.StatusBadRequest)
		return
	}
	name := h.userName(r)

	t := &Ticket{
		Title:                req.Title,
		Body:                 req.Body,
		Origin:               origin,
		Repo:                 req.Repo,
		TargetBranch:         req.TargetBranch,
		WorkflowName:         req.WorkflowName,
		WorkflowVersion:      req.WorkflowVersion,
		RepositorySnapshotID: req.RepositorySnapshotID,
		CreatedBy:            name,
		ExternalRef:          req.ExternalRef,
	}
	// Triggering user: credential attachment + cost attribution (dispatch
	// resolves user_id from users.username in wave 4).
	t.UserName = name

	id, err := h.store.Create(t)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	created, _, err := h.store.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// ListTickets handles GET /api/v1/agent/tickets (?state=&repo=&origin=&limit=).
func (h *Handler) ListTickets(w http.ResponseWriter, r *http.Request) {
	filter := ListFilter{Limit: 100}
	if s := r.URL.Query().Get("state"); s != "" {
		if !ValidState(State(s)) {
			http.Error(w, "invalid state filter", http.StatusBadRequest)
			return
		}
		filter.State = State(s)
	}
	filter.Repo = r.URL.Query().Get("repo")
	if o := r.URL.Query().Get("origin"); o != "" {
		if !ValidOrigin(o) {
			http.Error(w, "invalid origin filter", http.StatusBadRequest)
			return
		}
		filter.Origin = o
	}
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l <= 500 {
		filter.Limit = l
	}

	list, err := h.store.List(filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// GetTicket handles GET /api/v1/agent/tickets/:ticket_id. The response
// uses the TicketDetail contract.
func (h *Handler) GetTicket(w http.ResponseWriter, r *http.Request) {
	id, ok := ticketIDParam(w, r)
	if !ok {
		return
	}
	t, found, err := h.store.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "ticket not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, TicketDetail{Ticket: *t})
}

// UpdateTicket handles PUT /api/v1/agent/tickets/:ticket_id —
// title/body/workflow ref/target branch. NEVER state.
func (h *Handler) UpdateTicket(w http.ResponseWriter, r *http.Request) {
	id, ok := ticketIDParam(w, r)
	if !ok {
		return
	}
	var req UpdateRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Title == nil && req.Body == nil &&
		req.WorkflowName == nil && req.WorkflowVersion == nil && req.TargetBranch == nil &&
		req.RepositorySnapshotID == nil {
		http.Error(w, "no fields to update", http.StatusBadRequest)
		return
	}
	err := h.store.Update(id, Update{
		Title: req.Title, Body: req.Body,
		WorkflowName: req.WorkflowName, WorkflowVersion: req.WorkflowVersion,
		TargetBranch: req.TargetBranch, RepositorySnapshotID: req.RepositorySnapshotID,
	})
	if errors.Is(err, ErrTicketNotFound) {
		http.Error(w, "ticket not found", http.StatusNotFound)
		return
	}
	if errors.Is(err, ErrDispatchConflict) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	updated, _, err := h.store.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// TransitionTicket handles PUT /api/v1/agent/tickets/:ticket_id/state —
// the ONLY HTTP path that changes ticket state, delegating to the
// single-writer Store.Transition. 409 on illegal/stale transitions.
func (h *Handler) TransitionTicket(w http.ResponseWriter, r *http.Request) {
	id, ok := ticketIDParam(w, r)
	if !ok {
		return
	}
	var req TransitionRequest
	if !readJSON(w, r, &req) {
		return
	}
	if !ValidState(req.From) || !ValidState(req.To) {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}
	// pipeline_run_id is server-owned: the durable workflow/pipeline link is
	// written only by in-process dispatch (Store.Transition, which never
	// passes through this HTTP handler), never accepted from a caller (F30 id
	// class). TransitionRequest's decoder rejects the retired key outright, so
	// no pipeline identity flows from HTTP into TransitionMeta here.
	err := h.store.Transition(id, req.From, req.To, TransitionMeta{})
	switch {
	case errors.Is(err, ErrTicketNotFound):
		http.Error(w, "ticket not found", http.StatusNotFound)
		return
	case errors.Is(err, ErrInvalidTransition), errors.Is(err, ErrStaleTransition):
		http.Error(w, err.Error(), http.StatusConflict)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	updated, _, err := h.store.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}
