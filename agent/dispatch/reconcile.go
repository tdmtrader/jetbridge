package dispatch

import (
	"context"
	"errors"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/atc/db"
)

// reconcileCompletedRuns walks RUNNING tickets whose pipeline run is complete
// and applies the board's human-attention transition through the single
// writer. It projects generic execution completion onto the ticket adapter;
// the linked workflow run and snapshots remain canonical. Stale or missing
// transitions are benign races. A run still waiting on a human keeps
// completed_at NULL, so it can never be a candidate here.
func (d *Dispatcher) reconcileCompletedRuns(ctx context.Context, logger lager.Logger) error {
	if d.cfg.RunReader == nil {
		return nil
	}
	running, err := d.deps.Tickets.List(tickets.ListFilter{State: tickets.StateRunning})
	if err != nil {
		logger.Error("failed-to-list-running-tickets", err)
		return err
	}

	for _, t := range running {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if t.PipelineRunID == nil {
			// Moved to running outside dispatch (manual transition without
			// a run id). Nothing to reconcile against; humans own it.
			continue
		}
		run, found, err := d.cfg.RunReader.GetRunByID(*t.PipelineRunID)
		if err != nil {
			logger.Error("failed-to-get-run", err, lager.Data{"ticket": t.ID, "run": *t.PipelineRunID})
			continue
		}
		status := db.PipelineRunErrored // vanished run row = errored
		if found {
			if _, complete := run.CompletedAt(); !complete {
				continue // still running; the lifecycler owns completion
			}
			status = run.Status()
		}
		d.reconcileOne(logger, t, status)
	}
	return nil
}

func (d *Dispatcher) reconcileOne(logger lager.Logger, t tickets.Ticket, status db.PipelineRunStatus) {
	log := logger.Session("reconcile", lager.Data{"ticket": t.ID, "run-status": string(status)})

	transition := func(to tickets.State, meta tickets.TransitionMeta) {
		err := d.deps.Tickets.Transition(t.ID, tickets.StateRunning, to, meta)
		switch {
		case err == nil:
			log.Info("reconciled", lager.Data{"to": string(to)})
		case errors.Is(err, tickets.ErrStaleTransition), errors.Is(err, tickets.ErrTicketNotFound):
			log.Debug("reconcile-raced", lager.Data{"to": string(to)}) // another writer won; benign
		default:
			log.Error("failed-to-transition", err, lager.Data{"to": string(to)})
		}
	}

	// (a) Run succeeded but the ticket never left running: the API
	// transition caller should have moved it — safety net.
	if status == db.PipelineRunSucceeded {
		transition(tickets.StateNeedsReview, tickets.TransitionMeta{})
		return
	}

	// (b) failed/errored/aborted: terminal subordinate runs are always
	// projected to human review. Generic workflow outcomes and waits remain
	// the canonical v3 mechanism.
	transition(tickets.StateNeedsReview, tickets.TransitionMeta{})
}
