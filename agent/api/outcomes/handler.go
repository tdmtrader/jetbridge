package outcomes

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/concourse/concourse/agent/api/tickets"
)

// UserNameFunc resolves the authenticated human username ("" when
// anonymous). Injected because this package must not import
// atc/api/accessor (which imports atc/db, which imports this package via
// AgentOutcomesFactory — a cycle). atc/api/handler.go wires
// accessor.GetAccessor(r).Claims().UserName.
type UserNameFunc func(r *http.Request) string

// Handler serves GetAgentTicketOutcome + SetAgentTicketDisposition. Auth
// is enforced by the wrappa tier (authorized member/viewer, §4.2); this
// handler only reads WHO the verified writer is.
type Handler struct {
	store    Store
	tickets  tickets.Store
	userName UserNameFunc
}

func NewHandler(store Store, ticketStore tickets.Store, userName UserNameFunc) *Handler {
	return &Handler{store: store, tickets: ticketStore, userName: userName}
}

// DispositionRequest is the PUT body for SetAgentTicketDisposition.
type DispositionRequest struct {
	Disposition string `json:"disposition"` // sent_back | abandoned | concluded
	Reason      string `json:"reason"`      // §1.11 taxonomy (required)
	Notes       string `json:"notes,omitempty"`
}

// DispositionRequestFor builds a DispositionRequest — shared by the
// go-concourse client and its tests so neither duplicates field names.
func DispositionRequestFor(disposition, reason, notes string) DispositionRequest {
	return DispositionRequest{Disposition: disposition, Reason: reason, Notes: notes}
}

// dispositionToState maps a disposition to its ticket lifecycle state.
// All three are needs_review edges; concluded is the neutral terminal
// ("run finished, human reviewed, no merge intended" — FLOWS.md §4).
func dispositionToState(d string) tickets.State {
	switch d {
	case DispositionAbandoned:
		return tickets.StateAbandoned
	case DispositionConcluded:
		return tickets.StateConcluded
	}
	return tickets.StateSentBack
}

// GetOutcome handles GET /api/v1/agent/tickets/:ticket_id/outcome.
func (h *Handler) GetOutcome(w http.ResponseWriter, r *http.Request) {
	id, ok := ticketID(w, r)
	if !ok {
		return
	}
	o, found, err := h.store.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "no outcome for ticket", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

// SetDisposition handles PUT /api/v1/agent/tickets/:ticket_id/disposition.
func (h *Handler) SetDisposition(w http.ResponseWriter, r *http.Request) {
	id, ok := ticketID(w, r)
	if !ok {
		return
	}
	var req DispositionRequest
	if !readJSON(w, r, &req) {
		return
	}
	if !ValidDisposition(req.Disposition) {
		http.Error(w, "disposition must be sent_back, abandoned, or concluded", http.StatusBadRequest)
		return
	}
	if !ValidDispositionReason(req.Reason) {
		http.Error(w, "reason must be one of the disposition taxonomy values", http.StatusBadRequest)
		return
	}

	tk, found, err := h.tickets.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "ticket not found", http.StatusNotFound)
		return
	}

	// Walk the ticket lifecycle FIRST, through the single writer. If the
	// current state does not allow the edge (e.g. already merged), do not
	// touch agent_outcomes — the disposition and the ticket state stay
	// consistent.
	target := dispositionToState(req.Disposition)
	if err := h.tickets.Transition(id, tk.State, target, tickets.TransitionMeta{}); err != nil {
		switch {
		case errors.Is(err, tickets.ErrInvalidTransition), errors.Is(err, tickets.ErrStaleTransition):
			http.Error(w, err.Error(), http.StatusConflict)
		case errors.Is(err, tickets.ErrTicketNotFound):
			http.Error(w, err.Error(), http.StatusNotFound)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	if err := h.store.SetDisposition(id, DispositionInput{
		Disposition: req.Disposition, Reason: req.Reason, Notes: req.Notes,
		By: h.userName(r),
	}); err != nil {
		if errors.Is(err, ErrOutcomeNotFound) {
			// No outcome row yet (never pushed / dispositioned pre-harvest):
			// create one so the disposition is durable.
			_ = h.store.Ensure(&Outcome{TicketID: id, Repo: tk.Repo, Branch: tk.Branch})
			if err2 := h.store.SetDisposition(id, DispositionInput{
				Disposition: req.Disposition, Reason: req.Reason, Notes: req.Notes, By: h.userName(r),
			}); err2 != nil {
				http.Error(w, err2.Error(), http.StatusInternalServerError)
				return
			}
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	o, _, _ := h.store.Get(id)
	writeJSON(w, http.StatusOK, o)
}

func ticketID(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.FormValue(":ticket_id"))
	if err != nil || id <= 0 {
		http.Error(w, "invalid ticket_id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
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
