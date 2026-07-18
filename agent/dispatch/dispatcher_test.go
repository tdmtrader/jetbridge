package dispatch_test

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/budget/budgetfakes"
	"github.com/concourse/concourse/agent/dispatch"
)

// loopDeps builds Deps around the landed test scaffolding (dispatchDeps)
// with n queued tickets in the MemoryStore.
func loopDeps(t *testing.T, n int) (dispatch.Deps, *tickets.MemoryStore, []int) {
	t.Helper()
	deps, store, _, _ := dispatchDeps(t)
	ids := make([]int, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, queuedTicket(t, store, "smoke"))
	}
	return deps, store, ids
}

func TestDispatcherDispatchesEachQueuedTicket(t *testing.T) {
	deps, store, ids := loopDeps(t, 2)
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

func TestDispatcherOverBudgetTicketStaysQueuedAndPassContinues(t *testing.T) {
	deps, store, ids := loopDeps(t, 2)
	checker := new(budgetfakes.FakeChecker)
	// First ticket exhausted, second admitted.
	checker.TicketRemainingStub = func(ticketID int) (budget.Remaining, error) {
		if ticketID == ids[0] {
			return budget.Remaining{LimitUSD: 1, SpentUSD: 2, Exhausted: true}, nil
		}
		return budget.Remaining{}, nil
	}
	deps.Budget = checker
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{})
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	first, _, _ := store.Get(ids[0])
	second, _, _ := store.Get(ids[1])
	if first.State != tickets.StateQueued {
		t.Errorf("over-cap ticket must stay queued, got %s", first.State)
	}
	if second.State != tickets.StateRunning {
		t.Errorf("deferral must not starve the pass; second = %s, want running", second.State)
	}
}

func TestDispatcherPlatformFaultIsolatedPerTicket(t *testing.T) {
	deps, store, ids := loopDeps(t, 2)
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

func TestDispatcherListFailureReturnsError(t *testing.T) {
	deps, _, _ := loopDeps(t, 0)
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
