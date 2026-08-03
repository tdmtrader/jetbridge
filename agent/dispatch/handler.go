package dispatch

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/concourse/concourse/agent/api/tickets"
)

// NewHTTPHandler serves POST /api/v1/agent/tickets/:ticket_id/dispatch — the
// manual dispatch trigger, on the SAME Deps the dispatcher component runs.
// Auth is the wrappa's member tier (human-only, deliberately no principal
// tier: an agent must not be able to spend the cluster's budget by calling
// this). IdentityFunc resolves the canonical authenticated display identity
// used for run attribution (created_by).
type IdentityFunc func(*http.Request) (string, error)

func NewHTTPHandler(deps Deps, identity IdentityFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.FormValue(":ticket_id"))
		if err != nil || id <= 0 {
			http.Error(w, "invalid ticket_id", http.StatusBadRequest)
			return
		}

		dispatchedBy, err := identity(r)
		if err != nil || strings.TrimSpace(dispatchedBy) == "" {
			http.Error(w, "authenticated ticket dispatcher is unavailable", http.StatusInternalServerError)
			return
		}

		res, err := DispatchOne(r.Context(), deps, id, dispatchedBy)
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
		case errors.Is(err, ErrNoWorkflow), errors.Is(err, ErrWorkflowNotFound),
			errors.Is(err, ErrNotTicketDispatchable):
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
			WorkflowRunID: res.WorkflowRunID,
			PipelineRunID: res.PipelineRunID,
			Warnings:      res.Warnings,
		})
	})
}
