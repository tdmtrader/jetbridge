package dispatch

import (
	"context"
	"errors"
	"fmt"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflowrun"
)

// TicketProjector is THE terminalizer for the ticket adapter: when the
// always-on workflow-run reconciler finalizes a run, this projects that one
// fact onto the ticket that owns the run. There is no second loop watching
// tickets — the workflow run is canonical, the ticket board is a projection of
// it, and every terminal outcome (succeeded, failed, errored, aborted) means
// the same thing to a ticket: a human should look at it.
//
// It lives in agent/dispatch because ticket semantics do; agent/workflowrun
// only declares the seam (workflowrun.TicketProjector) and cannot import
// tickets without inverting the dependency.
type TicketProjector struct {
	tickets tickets.Store
}

var _ workflowrun.TicketProjector = (*TicketProjector)(nil)

func NewTicketProjector(store tickets.Store) (*TicketProjector, error) {
	if store == nil {
		return nil, fmt.Errorf("ticket projector: ticket store is required")
	}
	return &TicketProjector{tickets: store}, nil
}

// ProjectFinalizedRun moves the ticket that owns runID from running to
// needs_review through the single writer. It is a no-op — never an error —
// when the ticket has moved on, was never linked to this exact run, or another
// writer won the race: this is a safety net projecting a fact, not an
// authority over the board.
func (projector *TicketProjector) ProjectFinalizedRun(
	_ context.Context,
	ticketID int,
	runID snapshot.WorkflowRunID,
) error {
	ticket, found, err := projector.tickets.Get(ticketID)
	if err != nil {
		return fmt.Errorf("read ticket %d for terminal workflow run %s: %w", ticketID, runID, err)
	}
	if !found || ticket.State != tickets.StateRunning {
		return nil
	}
	if ticket.WorkflowRunID == nil || *ticket.WorkflowRunID != runID {
		// The ticket is running some OTHER run (or was moved to running by
		// hand). Only the run a ticket is durably linked to may terminalize it.
		return nil
	}
	err = projector.tickets.Transition(
		ticketID, tickets.StateRunning, tickets.StateNeedsReview, tickets.TransitionMeta{},
	)
	if err == nil || errors.Is(err, tickets.ErrStaleTransition) || errors.Is(err, tickets.ErrTicketNotFound) {
		return nil
	}
	return fmt.Errorf("project terminal workflow run %s onto ticket %d: %w", runID, ticketID, err)
}
