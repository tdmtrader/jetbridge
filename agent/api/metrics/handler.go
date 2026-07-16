package metrics

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	schema "github.com/concourse/concourse/agent/schema"
)

// Handler serves the agent run-metrics routes. Auth is enforced by the
// wrappa layer (principal(metrics:write) for submit; authorized viewer for
// list) — the handler trusts the request.
type Handler struct {
	store Store
}

func NewHandler(store Store) *Handler {
	return &Handler{store: store}
}

// SubmitMetrics handles POST /api/v1/agent/metrics.
func (h *Handler) SubmitMetrics(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 5<<20))
	if err != nil {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	rm, err := ParseSubmission(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.store.Upsert(rm); err != nil {
		http.Error(w, "failed to store metrics", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// ListRecent handles GET /api/v1/agent/metrics?limit=N — the most-recent agent
// run metrics across all tickets/builds, newest-first (operator dashboard).
func (h *Handler) ListRecent(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, _ = strconv.Atoi(v) // invalid/absent → 0 → store applies its default
	}
	rows, err := h.store.ListRecent(limit)
	if err != nil {
		http.Error(w, "failed to list metrics", http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []schema.RunMetrics{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rows)
}

// ListByTicket handles GET /api/v1/agent/tickets/:ticket_id/metrics.
func (h *Handler) ListByTicket(w http.ResponseWriter, r *http.Request) {
	ticketID, err := strconv.Atoi(r.FormValue(":ticket_id"))
	if err != nil || ticketID <= 0 {
		http.Error(w, "invalid ticket_id", http.StatusBadRequest)
		return
	}
	rows, err := h.store.ListByTicket(ticketID)
	if err != nil {
		http.Error(w, "failed to list metrics", http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []schema.RunMetrics{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rows)
}
