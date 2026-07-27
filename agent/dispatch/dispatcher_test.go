package dispatch_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagerctx"
	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/api/tickets/ticketstest"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/agent/workflowrun"
)

// loopDeps builds schema-v3 Deps with n queued tickets in the MemoryStore.
func loopDeps(t *testing.T, n int) (dispatch.Deps, *ticketstest.MemoryStore, []int, *fakeWorkflowBinder) {
	t.Helper()
	deps, store, _, binder := v3DispatchDeps(t)
	ids := make([]int, 0, n)
	for i := 0; i < n; i++ {
		id := queuedTicket(t, store, "smoke")
		setRepositorySnapshot(t, store, id, 101)
		ids = append(ids, id)
	}
	return deps, store, ids, binder
}

func TestDispatcherDispatchesEachQueuedTicket(t *testing.T) {
	deps, store, ids, _ := loopDeps(t, 2)
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{})
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, id := range ids {
		got, _, _ := store.Get(id)
		if got.State != tickets.StateRunning {
			t.Errorf("ticket %d state = %s, want running", id, got.State)
		}
	}
}

func TestDispatcherBudgetDeniedTicketStaysQueuedAndPassContinues(t *testing.T) {
	deps, store, ids, binder := loopDeps(t, 2)
	// The binder's durable reservation is the single budget authority; deny
	// whichever ticket the pass reaches first and admit the other.
	binder.err = workflowrun.ErrBudgetDenied
	binder.errOnce = true
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{})
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	states := map[tickets.State]int{}
	for _, id := range ids {
		got, _, _ := store.Get(id)
		states[got.State]++
	}
	if states[tickets.StateQueued] != 1 {
		t.Errorf("the denied ticket must STAY queued (never failed), states=%v", states)
	}
	if states[tickets.StateRunning] != 1 {
		t.Errorf("a deferral must not starve the pass, states=%v", states)
	}
}

func TestDispatcherPlatformFaultIsolatedPerTicket(t *testing.T) {
	deps, store, ids, _ := loopDeps(t, 2)
	// Poison the first ticket: dangling workflow ref → refused, stays queued.
	bad := "no-such-workflow"
	if err := store.Update(ids[0], tickets.Update{WorkflowName: &bad}); err != nil {
		t.Fatal(err)
	}
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{})
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("a per-ticket failure must not fail the pass: %v", err)
	}
	first, _, _ := store.Get(ids[0])
	second, _, _ := store.Get(ids[1])
	if first.State != tickets.StateQueued {
		t.Errorf("refused ticket stays queued, got %s", first.State)
	}
	if second.State != tickets.StateRunning {
		t.Errorf("second ticket must still dispatch, got %s", second.State)
	}
}

func TestDispatcherUndispatchableWorkflowLogsDispatchRefused(t *testing.T) {
	deps, store, ids, _ := loopDeps(t, 2)
	deps.Workflows.(*fakeWorkflows).byName["no-repo"] =
		definitionWithInputs(t, portSpec{"work-item", "work-item/v1"})
	workflowName := "no-repo"
	if err := store.Update(ids[0], tickets.Update{WorkflowName: &workflowName}); err != nil {
		t.Fatal(err)
	}
	logger := lagertest.NewTestLogger("test")
	ctx := lagerctx.NewContext(context.Background(), logger)

	if err := dispatch.NewDispatcher(deps, dispatch.LoopConfig{}).Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	first, _, _ := store.Get(ids[0])
	second, _, _ := store.Get(ids[1])
	if first.State != tickets.StateQueued {
		t.Errorf("undispatchable ticket state = %s, want queued", first.State)
	}
	if second.State != tickets.StateRunning {
		t.Errorf("next queued ticket state = %s, want running", second.State)
	}
	_, binderCalls := deps.WorkflowBinder.(*fakeWorkflowBinder).Calls()
	if len(binderCalls) != 1 {
		t.Errorf("binder calls = %d, want 1 for the next queued ticket", len(binderCalls))
	}

	refused, failed := false, false
	for _, message := range logger.LogMessages() {
		refused = refused || strings.HasSuffix(message, ".dispatch-refused")
		failed = failed || strings.HasSuffix(message, ".failed-to-dispatch")
	}
	if !refused {
		t.Error("dispatch-refused was not logged")
	}
	if failed {
		t.Error("failed-to-dispatch was logged for an inspectable refusal")
	}
	if len(logger.Errors) != 1 || !errors.Is(logger.Errors[0], dispatch.ErrNotTicketDispatchable) {
		t.Errorf("logged errors = %v, want one error wrapping ErrNotTicketDispatchable", logger.Errors)
	}
}

func TestDispatcherListFailureReturnsError(t *testing.T) {
	deps, _, _, _ := loopDeps(t, 0)
	deps.Tickets = failingListStore{}
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{})
	if err := d.Run(context.Background()); err == nil {
		t.Fatal("listing failure must surface (component retries on interval)")
	}
}

type failingListStore struct{ tickets.Store }

func (failingListStore) List(tickets.ListFilter) ([]tickets.Ticket, error) {
	return nil, errors.New("db down")
}
