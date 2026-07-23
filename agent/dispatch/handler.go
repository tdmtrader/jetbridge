package dispatch

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/concourse/concourse/agent/api/tickets"

	"encoding/json"
)

// NewHTTPHandler serves POST /api/v1/agent/tickets/:ticket_id/dispatch —
// the manual dispatch trigger. Auth is the wrappa's member tier
// (human-only, deliberately no principal tier: the human trigger is the
// budget gate while budget admission is deferred). userName resolves the
// authenticated username for run attribution (created_by).
func NewHTTPHandler(deps Deps, userName func(*http.Request) string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.FormValue(":ticket_id"))
		if err != nil || id <= 0 {
			http.Error(w, "invalid ticket_id", http.StatusBadRequest)
			return
		}

		res, err := DispatchOne(r.Context(), deps, id, userName(r))
		switch {
		case errors.Is(err, tickets.ErrTicketNotFound):
			http.Error(w, "ticket not found", http.StatusNotFound)
			return
		case errors.Is(err, ErrNotQueued), errors.Is(err, tickets.ErrStaleTransition), errors.Is(err, tickets.ErrDispatchConflict):
			http.Error(w, err.Error(), http.StatusConflict)
			return
		case errors.Is(err, ErrInputsPending):
			http.Error(w, "workflow inputs pending", http.StatusConflict)
			return
		case errors.Is(err, ErrNoWorkflow), errors.Is(err, ErrWorkflowNotFound), errors.Is(err, ErrRenderRefused):
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		case errors.Is(err, ErrBudgetExhausted):
			http.Error(w, err.Error(), http.StatusConflict)
			return
		case err != nil:
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(tickets.DispatchResponse{
			RunID:         res.RunID,
			PipelineName:  res.PipelineName,
			WorkflowRunID: res.WorkflowRunID,
			Warnings:      res.Warnings,
		})
	})
}
