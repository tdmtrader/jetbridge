package workflowrun

import (
	"context"
	"errors"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
)

type ticketProjectionCall struct {
	ticketID int
	runID    snapshot.WorkflowRunID
}

type ticketProjectorFake struct {
	calls []ticketProjectionCall
	err   error
}

func (fake *ticketProjectorFake) ProjectFinalizedRun(
	_ context.Context,
	ticketID int,
	runID snapshot.WorkflowRunID,
) error {
	fake.calls = append(fake.calls, ticketProjectionCall{ticketID: ticketID, runID: runID})
	return fake.err
}

// ticketOwned stamps the ONE link a run has back to a ticket.
func ticketOwned(run db.AgentWorkflowRun, reference string) db.AgentWorkflowRun {
	run.OriginKind = OriginKindTicket
	run.OriginReference = reference
	return run
}

func failedTicketRun(t *testing.T, reference string) db.AgentWorkflowRun {
	t.Helper()
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	failed := db.AgentWorkflowRunExecutionStatusFailed
	return ticketOwned(runningRun(snapshot.WorkflowRunID(7), now, &failed, true), reference)
}

func runReconcilerOver(
	t *testing.T,
	run db.AgentWorkflowRun,
	projector TicketProjector,
	finalize func(context.Context, db.AgentWorkflowRunFinalization) (db.AgentWorkflowRunFinalizationResult, bool, error),
) (*reconciliationStoreFake, *reconciliationReporterFake) {
	t.Helper()
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	store := storeForSingleRun(run)
	store.finalizeFn = finalize
	reporter := &reconciliationReporterFake{}
	options := []ReconcilerOption{}
	if projector != nil {
		options = append(options, WithTicketProjector(projector))
	}
	reconciler := mustReconciler(t, store, now, 15*time.Minute, time.Minute, reporter, nil, options...)
	if err := reconciler.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return store, reporter
}

// The reconciler is THE terminalizer: finalizing a ticket-owned run is what
// moves the ticket, and there is no second loop that would do it otherwise.
func TestReconcilerProjectsFinalizedRunOntoItsTicket(t *testing.T) {
	projector := &ticketProjectorFake{}
	runReconcilerOver(t, failedTicketRun(t, "42"), projector, nil)

	if len(projector.calls) != 1 {
		t.Fatalf("projections = %+v, want exactly one", projector.calls)
	}
	if projector.calls[0].ticketID != 42 || projector.calls[0].runID != snapshot.WorkflowRunID(7) {
		t.Errorf("projection = %+v, want ticket 42 / run 7", projector.calls[0])
	}
}

func TestReconcilerDoesNotProjectRunsNoTicketOwns(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	failed := db.AgentWorkflowRunExecutionStatusFailed
	run := runningRun(snapshot.WorkflowRunID(7), now, &failed, true)
	run.OriginKind = "experiment"
	run.OriginReference = "experiment:1:cell:2"

	projector := &ticketProjectorFake{}
	runReconcilerOver(t, run, projector, nil)

	if len(projector.calls) != 0 {
		t.Fatalf("a non-ticket origin must not be projected: %+v", projector.calls)
	}
}

func TestReconcilerDoesNotProjectWhenAnotherNodeWonTheFinalizeCAS(t *testing.T) {
	projector := &ticketProjectorFake{}
	runReconcilerOver(t, failedTicketRun(t, "42"), projector,
		func(context.Context, db.AgentWorkflowRunFinalization) (db.AgentWorkflowRunFinalizationResult, bool, error) {
			return db.AgentWorkflowRunFinalizationResult{}, false, nil
		})

	if len(projector.calls) != 0 {
		t.Fatalf("the node that lost the CAS must leave the projection to the winner: %+v", projector.calls)
	}
}

func TestReconcilerIgnoresAnUnreadableTicketReference(t *testing.T) {
	for _, reference := range []string{"", "not-a-number", "0", "-3"} {
		projector := &ticketProjectorFake{}
		runReconcilerOver(t, failedTicketRun(t, reference), projector, nil)
		if len(projector.calls) != 0 {
			t.Errorf("reference %q must not be projected: %+v", reference, projector.calls)
		}
	}
}

// The run IS finalized. A projection fault must not re-classify that outcome
// as a failed reconciliation row.
func TestReconcilerProjectionFaultDoesNotFailTheRow(t *testing.T) {
	projector := &ticketProjectorFake{err: errors.New("tickets unavailable")}
	store, reporter := runReconcilerOver(t, failedTicketRun(t, "42"), projector, nil)

	if len(store.finalizations) != 1 {
		t.Fatalf("finalizations = %d, want 1", len(store.finalizations))
	}
	if reporter.rowCount(metric.WorkflowRunReconcilerRowFailed) != 0 {
		t.Errorf("rows = %+v, want no row failure", reporter.rows)
	}
	reporter.requirePasses(t, metric.WorkflowRunReconcilerPassSuccess)
}

// An already-terminal row gives a projection that failed once another chance.
func TestReconcilerReprojectsAnAlreadyTerminalRun(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	run := ticketOwned(
		reconciliationRun(snapshot.WorkflowRunID(7), db.AgentWorkflowRunStatusFailed, now),
		"42",
	)
	projector := &ticketProjectorFake{}
	store, _ := runReconcilerOver(t, run, projector, nil)

	if len(store.finalizations) != 0 {
		t.Fatalf("a terminal run must not be finalized again: %+v", store.finalizations)
	}
	if len(projector.calls) != 1 || projector.calls[0].ticketID != 42 {
		t.Fatalf("projections = %+v, want one retry for ticket 42", projector.calls)
	}
}

func TestWithTicketProjectorRejectsNil(t *testing.T) {
	_, err := NewReconciler(
		&reconciliationStoreFake{}, lager.NewLogger("test"), 2*time.Minute, time.Minute,
		WithTicketProjector(nil),
	)
	if err == nil || !containsFold(err.Error(), "ticket projector") {
		t.Fatalf("error = %v, want a ticket-projector requirement", err)
	}
}
