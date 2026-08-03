package dispatch_test

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/api/tickets/ticketstest"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/agent/snapshot"
)

// dispatchedTicket puts a ticket in the exact state the terminalizer acts on —
// running and durably linked to one workflow run — by actually dispatching it.
func dispatchedTicket(t *testing.T) (*ticketstest.MemoryStore, int, snapshot.WorkflowRunID) {
	t.Helper()
	deps, store, _, _ := v3DispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	setRepositorySnapshot(t, store, id, snapshot.SnapshotID(101))
	result, err := dispatch.DispatchOne(context.Background(), deps, id, "admin")
	if err != nil {
		t.Fatalf("DispatchOne: %v", err)
	}
	return store, id, result.WorkflowRunID
}

func mustProjector(t *testing.T, store tickets.Store) *dispatch.TicketProjector {
	t.Helper()
	projector, err := dispatch.NewTicketProjector(store)
	if err != nil {
		t.Fatalf("NewTicketProjector: %v", err)
	}
	return projector
}

func TestTicketProjectorMovesTheLinkedRunningTicketToNeedsReview(t *testing.T) {
	store, id, runID := dispatchedTicket(t)

	if err := mustProjector(t, store).ProjectFinalizedRun(context.Background(), id, runID); err != nil {
		t.Fatalf("ProjectFinalizedRun: %v", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateNeedsReview {
		t.Errorf("state = %s, want needs_review", got.State)
	}
}

// Every terminal outcome means the same thing to a ticket, and a second pass
// over an already-projected ticket is a no-op, not an error.
func TestTicketProjectorIsIdempotent(t *testing.T) {
	store, id, runID := dispatchedTicket(t)
	projector := mustProjector(t, store)

	for attempt := 0; attempt < 3; attempt++ {
		if err := projector.ProjectFinalizedRun(context.Background(), id, runID); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateNeedsReview {
		t.Errorf("state = %s, want needs_review", got.State)
	}
}

func TestTicketProjectorLeavesTicketsItDoesNotOwn(t *testing.T) {
	store, id, _ := dispatchedTicket(t)

	// A DIFFERENT run finishing must never terminalize this ticket.
	if err := mustProjector(t, store).ProjectFinalizedRun(
		context.Background(), id, snapshot.WorkflowRunID(999),
	); err != nil {
		t.Fatalf("ProjectFinalizedRun: %v", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateRunning {
		t.Errorf("state = %s, want the ticket left alone (running)", got.State)
	}
}

func TestTicketProjectorCannotTerminalizeANewerDispatchThatWinsTheOwnershipRace(t *testing.T) {
	deps, store, _, binder := v3DispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	setRepositorySnapshot(t, store, id, snapshot.SnapshotID(101))
	first, err := dispatch.DispatchOne(context.Background(), deps, id, "admin")
	if err != nil {
		t.Fatalf("first DispatchOne: %v", err)
	}

	var secondRunID snapshot.WorkflowRunID
	racingStore := &projectionOwnershipRaceStore{Store: store}
	racingStore.race = func() {
		if err := store.Transition(id, tickets.StateRunning, tickets.StateQueued, tickets.TransitionMeta{}); err != nil {
			t.Fatalf("requeue current dispatch: %v", err)
		}
		binder.mu.Lock()
		binder.result.Run.ID = snapshot.WorkflowRunID(304)
		binder.mu.Unlock()
		second, err := dispatch.DispatchOne(context.Background(), deps, id, "admin")
		if err != nil {
			t.Fatalf("second DispatchOne: %v", err)
		}
		secondRunID = second.WorkflowRunID
	}

	if err := mustProjector(t, racingStore).ProjectFinalizedRun(context.Background(), id, first.WorkflowRunID); err != nil {
		t.Fatalf("ProjectFinalizedRun: %v", err)
	}
	got, found, err := store.Get(id)
	if err != nil || !found {
		t.Fatalf("Get ticket = (%t, %v)", found, err)
	}
	if secondRunID.Validate() != nil || secondRunID == first.WorkflowRunID {
		t.Fatalf("second workflow run = %s, first = %s", secondRunID, first.WorkflowRunID)
	}
	if got.State != tickets.StateRunning || got.WorkflowRunID == nil || *got.WorkflowRunID != secondRunID {
		t.Fatalf("ticket after stale projection = %+v, want newer run %s left running", got, secondRunID)
	}
}

func TestTicketProjectorIgnoresTicketsThatMovedOn(t *testing.T) {
	store, id, runID := dispatchedTicket(t)
	if err := store.Transition(id, tickets.StateRunning, tickets.StateNeedsReview, tickets.TransitionMeta{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(id, tickets.StateNeedsReview, tickets.StateClosed, tickets.TransitionMeta{}); err != nil {
		t.Fatal(err)
	}

	if err := mustProjector(t, store).ProjectFinalizedRun(context.Background(), id, runID); err != nil {
		t.Fatalf("a human's decision must not be an error: %v", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateClosed {
		t.Errorf("state = %s, want the human's closed to stand", got.State)
	}
}

func TestTicketProjectorIgnoresAMissingTicket(t *testing.T) {
	store := ticketstest.NewMemoryStore()
	if err := mustProjector(t, store).ProjectFinalizedRun(
		context.Background(), 4242, snapshot.WorkflowRunID(303),
	); err != nil {
		t.Fatalf("a vanished ticket is not an error: %v", err)
	}
}

func TestTicketProjectorSurfacesRealStoreFaults(t *testing.T) {
	store, id, runID := dispatchedTicket(t)
	wantErr := errors.New("tickets unavailable")

	err := mustProjector(t, failingTransitionStore{Store: store, err: wantErr}).
		ProjectFinalizedRun(context.Background(), id, runID)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want the store fault", err)
	}
}

func TestNewTicketProjectorRequiresAStore(t *testing.T) {
	if _, err := dispatch.NewTicketProjector(nil); err == nil {
		t.Fatal("a projector without a ticket store must not construct")
	}
}

type failingTransitionStore struct {
	tickets.Store
	err error
}

func (s failingTransitionStore) TransitionCurrentRunToNeedsReview(
	context.Context,
	int,
	snapshot.WorkflowRunID,
) error {
	return s.err
}

type projectionOwnershipRaceStore struct {
	tickets.Store
	raced bool
	race  func()
}

func (store *projectionOwnershipRaceStore) triggerRace() {
	if store.raced {
		return
	}
	store.raced = true
	store.race()
}

func (store *projectionOwnershipRaceStore) Get(id int) (*tickets.Ticket, bool, error) {
	ticket, found, err := store.Store.Get(id)
	store.triggerRace()
	return ticket, found, err
}

func (store *projectionOwnershipRaceStore) TransitionCurrentRunToNeedsReview(
	ctx context.Context,
	id int,
	runID snapshot.WorkflowRunID,
) error {
	store.triggerRace()
	return store.Store.TransitionCurrentRunToNeedsReview(ctx, id, runID)
}
