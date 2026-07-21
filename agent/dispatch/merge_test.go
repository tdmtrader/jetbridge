package dispatch_test

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/dispatch"
)

// reviewedTicket walks a ticket to needs_review with a delivered branch,
// the state a merge is legal from.
func reviewedTicket(t *testing.T, store *tickets.MemoryStore) int {
	t.Helper()
	id, err := store.Create(&tickets.Ticket{
		Title: "fix X", Body: "details", Origin: "fly",
		Repo: "tdmtrader/jetbridge", TargetBranch: "jetbridge",
		WorkflowName: "smoke", UserName: "tdm", CreatedBy: "tdm",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, hop := range []struct{ from, to tickets.State }{
		{tickets.StateDraft, tickets.StateQueued},
		{tickets.StateQueued, tickets.StateRunning},
	} {
		if err := store.Transition(id, hop.from, hop.to, tickets.TransitionMeta{}); err != nil {
			t.Fatal(err)
		}
	}
	err = store.Transition(id, tickets.StateRunning, tickets.StateNeedsReview,
		tickets.TransitionMeta{Branch: "agent/ticket-1"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestMergeOneRendersAndTriggers(t *testing.T) {
	deps, store, saver, runs := dispatchDeps(t)
	id := reviewedTicket(t, store)

	res, err := dispatch.MergeOne(context.Background(), deps, id, "admin", dispatch.MergeOptions{Push: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.RunID != 555 {
		t.Fatalf("expected the created run id, got %d", res.RunID)
	}
	if saver.savedName != "agent-merge-1" {
		t.Fatalf("unexpected pipeline name %q", saver.savedName)
	}
	if runs.CreateRunCallCount() != 1 {
		t.Fatalf("expected one run, got %d", runs.CreateRunCallCount())
	}
}

// The ticket must NOT move at merge-trigger time: the merge can still
// conflict or fail a gate, and the exec step records the transition only on
// a real exit-0 landing.
func TestMergeOneDoesNotTransitionTheTicket(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	id := reviewedTicket(t, store)

	if _, err := dispatch.MergeOne(context.Background(), deps, id, "admin", dispatch.MergeOptions{Push: true}); err != nil {
		t.Fatal(err)
	}
	tk, _, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if tk.State != tickets.StateNeedsReview {
		t.Fatalf("ticket must stay needs_review until the merge actually lands, got %s", tk.State)
	}
}

func TestMergeOneRefusesTicketNotInNeedsReview(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	id := queuedTicket(t, store, "smoke") // still queued

	_, err := dispatch.MergeOne(context.Background(), deps, id, "admin", dispatch.MergeOptions{Push: true})
	if !errors.Is(err, dispatch.ErrNotReviewable) {
		t.Fatalf("expected ErrNotReviewable, got %v", err)
	}
}

func TestMergeOneRefusesTicketWithNoBranch(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	id, err := store.Create(&tickets.Ticket{
		Title: "x", Body: "y", Origin: "fly",
		Repo: "tdmtrader/jetbridge", TargetBranch: "jetbridge",
		WorkflowName: "smoke", CreatedBy: "tdm",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, hop := range []struct{ from, to tickets.State }{
		{tickets.StateDraft, tickets.StateQueued},
		{tickets.StateQueued, tickets.StateRunning},
		{tickets.StateRunning, tickets.StateNeedsReview}, // no branch recorded
	} {
		if err := store.Transition(id, hop.from, hop.to, tickets.TransitionMeta{}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := dispatch.MergeOne(context.Background(), deps, id, "admin", dispatch.MergeOptions{Push: true}); err == nil {
		t.Fatal("a ticket with no delivered branch has nothing to merge")
	}
}

func TestMergeOneMissingTicket(t *testing.T) {
	deps, _, _, _ := dispatchDeps(t)
	if _, err := dispatch.MergeOne(context.Background(), deps, 4242, "admin", dispatch.MergeOptions{}); !errors.Is(err, tickets.ErrTicketNotFound) {
		t.Fatalf("expected ErrTicketNotFound, got %v", err)
	}
}
