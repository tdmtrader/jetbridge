package dispatch

import (
	"errors"
	"io"
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
		case errors.Is(err, ErrNotQueued), errors.Is(err, tickets.ErrStaleTransition):
			http.Error(w, err.Error(), http.StatusConflict)
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
			RunID:        res.RunID,
			PipelineName: res.PipelineName,
			Warnings:     res.Warnings,
		})
	})
}

// NewMergeHTTPHandler serves POST /api/v1/agent/tickets/:ticket_id/merge —
// the platform-owned merge trigger (design 2026-07-20 §2). Human-only, on
// the same member tier as dispatch and for the same reason: an agent
// principal must never be able to land its own work.
//
// Body is an optional MergeRequest; the zero value is a pushing
// merge-commit integration gated by the ticket's own workflow.
func NewMergeHTTPHandler(deps Deps, userName func(*http.Request) string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.FormValue(":ticket_id"))
		if err != nil || id <= 0 {
			http.Error(w, "invalid ticket_id", http.StatusBadRequest)
			return
		}

		req := tickets.MergeRequest{Push: true}
		if r.Body != nil {
			// An empty body keeps the defaults; malformed JSON is a 400.
			var decoded tickets.MergeRequest
			switch err := json.NewDecoder(r.Body).Decode(&decoded); {
			case err == nil:
				req = decoded
			case errors.Is(err, io.EOF):
			default:
				http.Error(w, "malformed merge request: "+err.Error(), http.StatusBadRequest)
				return
			}
		}

		res, err := MergeOne(r.Context(), deps, id, userName(r), MergeOptions{
			Method:  req.Method,
			Message: req.Message,
			Push:    req.Push,
		})
		switch {
		case errors.Is(err, tickets.ErrTicketNotFound):
			http.Error(w, "ticket not found", http.StatusNotFound)
			return
		case errors.Is(err, ErrNotReviewable):
			http.Error(w, err.Error(), http.StatusConflict)
			return
		case errors.Is(err, ErrRenderRefused):
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		case err != nil:
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(tickets.MergeResponse{
			RunID:        res.RunID,
			PipelineName: res.PipelineName,
		})
	})
}
