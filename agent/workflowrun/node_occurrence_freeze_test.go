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

type nodeOccurrenceFreezerFake struct {
	runs []db.AgentWorkflowRun
	err  error
}

func (fake *nodeOccurrenceFreezerFake) FreezeRun(_ context.Context, run db.AgentWorkflowRun) error {
	fake.runs = append(fake.runs, run)
	return fake.err
}

// freezerRecorder observes the ordering between the freeze and the store's
// finalization, which is the whole point of the call site: the projection has
// to be written while the run's sources are still guaranteed to exist.
type orderedFreezer struct {
	order *[]string
	inner *nodeOccurrenceFreezerFake
}

func (freezer orderedFreezer) FreezeRun(ctx context.Context, run db.AgentWorkflowRun) error {
	*freezer.order = append(*freezer.order, "freeze")
	return freezer.inner.FreezeRun(ctx, run)
}

func runReconcilerWithFreezer(
	t *testing.T,
	run db.AgentWorkflowRun,
	freezer NodeOccurrenceFreezer,
	finalize func(context.Context, db.AgentWorkflowRunFinalization) (db.AgentWorkflowRunFinalizationResult, bool, error),
) (*reconciliationStoreFake, *reconciliationReporterFake) {
	t.Helper()
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	store := storeForSingleRun(run)
	store.finalizeFn = finalize
	reporter := &reconciliationReporterFake{}
	options := []ReconcilerOption{}
	if freezer != nil {
		options = append(options, WithNodeOccurrenceFreezer(freezer))
	}
	reconciler := mustReconciler(t, store, now, 15*time.Minute, time.Minute, reporter, nil, options...)
	if err := reconciler.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return store, reporter
}

func terminalizingRun(t *testing.T, execution db.AgentWorkflowRunExecutionStatus) db.AgentWorkflowRun {
	t.Helper()
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	return runningRun(snapshot.WorkflowRunID(7), now, &execution, true)
}

// Freezing at finalization is what makes the projection durable: it is the
// last moment at which every source it derives from is guaranteed to exist,
// because build and template GC only consider runs that are already terminal.
func TestReconcilerFreezesNodeOccurrencesWhenARunTerminalizes(t *testing.T) {
	freezer := &nodeOccurrenceFreezerFake{}
	runReconcilerWithFreezer(t, terminalizingRun(t, db.AgentWorkflowRunExecutionStatusFailed), freezer, nil)

	if len(freezer.runs) != 1 {
		t.Fatalf("freezes = %+v, want exactly one", freezer.runs)
	}
	if freezer.runs[0].ID != snapshot.WorkflowRunID(7) {
		t.Errorf("froze run %v, want run 7", freezer.runs[0].ID)
	}
}

// Every terminal outcome carries history worth keeping, including the ones
// nobody wants: an errored or aborted run is exactly when a human wants to see
// which node it got to.
func TestReconcilerFreezesEveryTerminalOutcome(t *testing.T) {
	for _, execution := range []db.AgentWorkflowRunExecutionStatus{
		db.AgentWorkflowRunExecutionStatusSucceeded,
		db.AgentWorkflowRunExecutionStatusFailed,
		db.AgentWorkflowRunExecutionStatusErrored,
		db.AgentWorkflowRunExecutionStatusAborted,
	} {
		freezer := &nodeOccurrenceFreezerFake{}
		runReconcilerWithFreezer(t, terminalizingRun(t, execution), freezer, nil)
		if len(freezer.runs) != 1 {
			t.Errorf("execution %q: freezes = %+v, want exactly one", execution, freezer.runs)
		}
	}
}

// A run that errors out of admission never ran a step, but it still terminates
// and still gets a projection — an all-pending one is the honest record that
// it reached nothing.
func TestReconcilerFreezesARunThatFailedAdmission(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	run := reconciliationRun(snapshot.WorkflowRunID(7), db.AgentWorkflowRunStatusAdmitting, now.Add(-time.Hour))

	freezer := &nodeOccurrenceFreezerFake{}
	store, _ := runReconcilerWithFreezer(t, run, freezer, nil)

	if len(store.finalizations) != 1 {
		t.Fatalf("finalizations = %+v, want one", store.finalizations)
	}
	if len(freezer.runs) != 1 {
		t.Fatalf("freezes = %+v, want exactly one", freezer.runs)
	}
}

// The winner of the CAS owns the projection, exactly as it owns the ticket
// projection. A loser that also froze would race the winner for the same key.
func TestReconcilerDoesNotFreezeWhenAnotherNodeWonTheFinalizeCAS(t *testing.T) {
	freezer := &nodeOccurrenceFreezerFake{}
	runReconcilerWithFreezer(t, terminalizingRun(t, db.AgentWorkflowRunExecutionStatusFailed), freezer,
		func(context.Context, db.AgentWorkflowRunFinalization) (db.AgentWorkflowRunFinalizationResult, bool, error) {
			return db.AgentWorkflowRunFinalizationResult{}, false, nil
		})

	if len(freezer.runs) != 0 {
		t.Fatalf("the node that lost the CAS must leave the freeze to the winner: %+v", freezer.runs)
	}
}

// A run that never terminalizes this pass has nothing to freeze yet; freezing
// early would write a snapshot of a run still in flight as immutable history.
func TestReconcilerDoesNotFreezeARunThatIsStillDeferred(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	run := reconciliationRun(snapshot.WorkflowRunID(7), db.AgentWorkflowRunStatusAdmitting, now)

	freezer := &nodeOccurrenceFreezerFake{}
	store, reporter := runReconcilerWithFreezer(t, run, freezer, nil)

	if len(store.finalizations) != 0 {
		t.Fatalf("a deferred run must not be finalized: %+v", store.finalizations)
	}
	if len(freezer.runs) != 0 {
		t.Fatalf("a deferred run must not be frozen: %+v", freezer.runs)
	}
	reporter.requirePasses(t, metric.WorkflowRunReconcilerPassSuccess)
}

// Frozen history is immutable, so a freeze retried after GC had reclaimed the
// sources would write a degraded projection as permanent truth. Re-claiming an
// already-terminal run therefore re-attempts the ticket projection but NOT the
// freeze.
func TestReconcilerDoesNotRefreezeAnAlreadyTerminalRun(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	run := reconciliationRun(snapshot.WorkflowRunID(7), db.AgentWorkflowRunStatusFailed, now)

	freezer := &nodeOccurrenceFreezerFake{}
	store, _ := runReconcilerWithFreezer(t, run, freezer, nil)

	if len(store.finalizations) != 0 {
		t.Fatalf("a terminal run must not be finalized again: %+v", store.finalizations)
	}
	if len(freezer.runs) != 0 {
		t.Fatalf("a terminal run must not be frozen again: %+v", freezer.runs)
	}
}

// The run IS finalized. Losing history is bad; stranding a run in a
// non-terminal state forever is worse, so a freeze fault must neither fail the
// row nor stop the ticket from moving.
func TestReconcilerFreezeFaultDoesNotBlockTerminalization(t *testing.T) {
	freezer := &nodeOccurrenceFreezerFake{err: errors.New("projection unavailable")}
	run := ticketOwned(terminalizingRun(t, db.AgentWorkflowRunExecutionStatusFailed), "42")

	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	store := storeForSingleRun(run)
	reporter := &reconciliationReporterFake{}
	projector := &ticketProjectorFake{}
	reconciler := mustReconciler(t, store, now, 15*time.Minute, time.Minute, reporter, nil,
		WithNodeOccurrenceFreezer(freezer), WithTicketProjector(projector))
	if err := reconciler.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(store.finalizations) != 1 {
		t.Fatalf("finalizations = %d, want 1", len(store.finalizations))
	}
	if store.finalizations[0].TerminalStatus != db.AgentWorkflowRunStatusFailed {
		t.Errorf("terminal status = %q, want failed", store.finalizations[0].TerminalStatus)
	}
	if reporter.rowCount(metric.WorkflowRunReconcilerRowFailed) != 0 {
		t.Errorf("rows = %+v, want no row failure", reporter.rows)
	}
	if len(projector.calls) != 1 {
		t.Errorf("a freeze fault must not stop the ticket from moving: %+v", projector.calls)
	}
	reporter.requirePasses(t, metric.WorkflowRunReconcilerPassSuccess)
}

// The freeze is the only step racing GC for the run's evidence, so it runs
// before the cross-aggregate ticket write that may block on another table.
func TestReconcilerFreezesBeforeProjectingTheTicket(t *testing.T) {
	var order []string
	freezer := orderedFreezer{order: &order, inner: &nodeOccurrenceFreezerFake{}}
	projector := &orderRecordingProjector{order: &order}
	run := ticketOwned(terminalizingRun(t, db.AgentWorkflowRunExecutionStatusFailed), "42")

	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	store := storeForSingleRun(run)
	reconciler := mustReconciler(t, store, now, 15*time.Minute, time.Minute,
		&reconciliationReporterFake{}, nil,
		WithNodeOccurrenceFreezer(freezer), WithTicketProjector(projector))
	if err := reconciler.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(order) != 2 || order[0] != "freeze" || order[1] != "project" {
		t.Fatalf("order = %v, want freeze then project", order)
	}
}

type orderRecordingProjector struct {
	order *[]string
}

func (projector *orderRecordingProjector) ProjectFinalizedRun(context.Context, int, snapshot.WorkflowRunID) error {
	*projector.order = append(*projector.order, "project")
	return nil
}

// A reconciler with no freezer keeps finalizing runs; a cluster that does not
// want the projection must not lose terminalization with it.
func TestReconcilerWithoutAFreezerStillFinalizes(t *testing.T) {
	store, reporter := runReconcilerWithFreezer(t, terminalizingRun(t, db.AgentWorkflowRunExecutionStatusFailed), nil, nil)

	if len(store.finalizations) != 1 {
		t.Fatalf("finalizations = %d, want 1", len(store.finalizations))
	}
	reporter.requirePasses(t, metric.WorkflowRunReconcilerPassSuccess)
}

func TestWithNodeOccurrenceFreezerRejectsNil(t *testing.T) {
	_, err := NewReconciler(
		&reconciliationStoreFake{}, lager.NewLogger("test"), 2*time.Minute, time.Minute,
		WithNodeOccurrenceFreezer(nil),
	)
	if err == nil || !containsFold(err.Error(), "node occurrence freezer") {
		t.Fatalf("error = %v, want a node-occurrence-freezer requirement", err)
	}
}

// orderedWaitCanceler records where wait cancellation sits relative to the
// freeze. The order is the whole defect: the freeze reads the run's wait rows,
// and the row it writes is immutable, so freezing first records a wait that
// outlived its build as still 'waiting' — permanently pinning a finished run
// in the attention lens with nothing an operator can do to clear it.
type orderedWaitCanceler struct {
	order *[]string
	calls *int
	err   error
}

func (canceler orderedWaitCanceler) CancelRun(
	_ context.Context, _ int, _ snapshot.WorkflowRunID, _ string, _ time.Time,
) (int, error) {
	*canceler.order = append(*canceler.order, "cancel")
	*canceler.calls++
	if canceler.err != nil {
		return 0, canceler.err
	}
	return 1, nil
}

// Nothing on the running -> aborted/failed/errored path cancelled open waits at
// all: cancelOpenWaits was reachable only from the 'canceling' and
// already-'aborted' arms, both of which run on a LATER pass than the freeze.
func TestReconcilerCancelsOpenWaitsBeforeFreezingTheProjection(t *testing.T) {
	for _, execution := range []db.AgentWorkflowRunExecutionStatus{
		db.AgentWorkflowRunExecutionStatusAborted,
		db.AgentWorkflowRunExecutionStatusFailed,
		db.AgentWorkflowRunExecutionStatusErrored,
	} {
		t.Run(string(execution), func(t *testing.T) {
			var order []string
			calls := 0
			inner := &nodeOccurrenceFreezerFake{}
			run := terminalizingRun(t, execution)
			now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
			store := storeForSingleRun(run)
			reconciler := mustReconciler(t, store, now, 15*time.Minute, time.Minute,
				&reconciliationReporterFake{}, nil,
				WithNodeOccurrenceFreezer(orderedFreezer{order: &order, inner: inner}),
				WithWaitCanceler(orderedWaitCanceler{order: &order, calls: &calls}))
			if err := reconciler.Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}

			if len(order) != 2 || order[0] != "cancel" || order[1] != "freeze" {
				t.Fatalf("order = %v, want the open waits cancelled before the freeze", order)
			}
			if len(inner.runs) != 1 {
				t.Fatalf("freezes = %+v, want exactly one", inner.runs)
			}
			// The projection must describe the run that finished, not the row
			// this pass started from — otherwise Derive cannot tell that a wait
			// still marked 'waiting' has outlived its build.
			if inner.runs[0].Status != db.AgentWorkflowRunStatus(execution) {
				t.Fatalf("froze status %q, want the terminal status %q",
					inner.runs[0].Status, execution)
			}
		})
	}
}

// A wait that could not be cancelled must not strand the run or re-classify a
// finalization that already applied; ClaimForReconciliation re-claims the run
// and repairs it on a later pass.
func TestReconcilerFinalizesEvenWhenWaitCancellationFails(t *testing.T) {
	var order []string
	calls := 0
	freezer := &nodeOccurrenceFreezerFake{}
	run := terminalizingRun(t, db.AgentWorkflowRunExecutionStatusFailed)
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	store := storeForSingleRun(run)
	reporter := &reconciliationReporterFake{}
	reconciler := mustReconciler(t, store, now, 15*time.Minute, time.Minute, reporter, nil,
		WithNodeOccurrenceFreezer(freezer),
		WithWaitCanceler(orderedWaitCanceler{
			order: &order, calls: &calls, err: errors.New("wait store unavailable"),
		}))
	if err := reconciler.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(store.finalizations) != 1 {
		t.Fatalf("finalizations = %d, want 1", len(store.finalizations))
	}
	if len(freezer.runs) != 1 {
		t.Fatalf("freezes = %+v, want the projection written anyway", freezer.runs)
	}
	if reporter.rowCount(metric.WorkflowRunReconcilerRowFailed) != 0 {
		t.Fatalf("rows = %+v, want no row failure", reporter.rows)
	}
}

// A wait left open by ANY terminal outcome is orphaned — the build that would
// have answered it is gone — so the re-claim repair must not be specific to
// abortion, which is all the terminal arm used to cover.
func TestReconcilerRepairsOpenWaitsForEveryTerminalStatus(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	for _, status := range []db.AgentWorkflowRunStatus{
		db.AgentWorkflowRunStatusSucceeded,
		db.AgentWorkflowRunStatusFailed,
		db.AgentWorkflowRunStatusErrored,
		db.AgentWorkflowRunStatusAborted,
	} {
		t.Run(string(status), func(t *testing.T) {
			var order []string
			calls := 0
			run := reconciliationRun(snapshot.WorkflowRunID(7), status, now)
			reconciler := mustReconciler(t, storeForSingleRun(run), now, 15*time.Minute, time.Minute,
				&reconciliationReporterFake{}, nil,
				WithWaitCanceler(orderedWaitCanceler{order: &order, calls: &calls}))
			if err := reconciler.Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if calls != 1 {
				t.Fatalf("CancelRun calls = %d, want the open wait repaired", calls)
			}
		})
	}
}
