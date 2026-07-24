package tickets_test

import (
	"encoding/json"
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
)

func TestTicketRevisionIsPublicAndLossless(t *testing.T) {
	encoded, err := json.Marshal(tickets.Ticket{ID: 1, Revision: 9007199254740993})
	if err != nil {
		t.Fatal(err)
	}
	var decoded tickets.Ticket
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Revision != 9007199254740993 {
		t.Fatalf("revision = %d", decoded.Revision)
	}
}

func TestValidTransitionMatrix(t *testing.T) {
	allowed := []struct{ from, to tickets.State }{
		{tickets.StateDraft, tickets.StateQueued},
		{tickets.StateDraft, tickets.StateAbandoned},
		{tickets.StateQueued, tickets.StateRunning},
		{tickets.StateQueued, tickets.StateDraft},
		{tickets.StateQueued, tickets.StateAbandoned},
		// running→queued records a retry attempt. Do not narrow this edge.
		{tickets.StateRunning, tickets.StateQueued},
		// running→needs_review: TWO writers — harvest (primary, 09) and
		// dispatch's run-completion reconciler (backup/safety net). Do not
		// narrow this edge either.
		{tickets.StateRunning, tickets.StateNeedsReview},
		{tickets.StateRunning, tickets.StateFailed},
		{tickets.StateRunning, tickets.StateErrored},
		{tickets.StateNeedsReview, tickets.StateMerged},
		{tickets.StateNeedsReview, tickets.StateMergedWithFixes},
		{tickets.StateNeedsReview, tickets.StateSentBack},
		{tickets.StateNeedsReview, tickets.StateAbandoned},
		// needs_review→concluded: TERMINAL positive sibling of abandoned —
		// "run finished, human reviewed, no merge intended" (spike/research
		// flows; FLOWS.md §3 spike-research, §4 state-enum decision).
		// Explicit human disposition ONLY; added pre-freeze (2026-07-09) so
		// the frozen enum never needs a later migration.
		{tickets.StateNeedsReview, tickets.StateConcluded},
		{tickets.StateNeedsReview, tickets.StateQueued},
		{tickets.StateSentBack, tickets.StateQueued},
		{tickets.StateFailed, tickets.StateQueued},
		{tickets.StateErrored, tickets.StateQueued},
		// failed/errored→abandoned: human write-off of a dead ticket. The
		// only other exit is a PAID re-dispatch, so without this edge dead
		// tickets pile up in every active listing forever.
		{tickets.StateFailed, tickets.StateAbandoned},
		{tickets.StateErrored, tickets.StateAbandoned},
	}
	for _, tr := range allowed {
		if !tickets.ValidTransition(tr.from, tr.to) {
			t.Errorf("ValidTransition(%s, %s) = false, want true", tr.from, tr.to)
		}
	}

	forbidden := []struct{ from, to tickets.State }{
		{tickets.StateDraft, tickets.StateRunning},      // must queue first
		{tickets.StateDraft, tickets.StateDraft},        // self-transition
		{tickets.StateQueued, tickets.StateNeedsReview}, // must run first
		{tickets.StateRunning, tickets.StateDraft},
		{tickets.StateRunning, tickets.StateMerged},
		{tickets.StateMerged, tickets.StateQueued}, // merged is terminal
		{tickets.StateMergedWithFixes, tickets.StateQueued},
		{tickets.StateAbandoned, tickets.StateQueued},    // abandoned is terminal
		{tickets.StateNeedsReview, tickets.StateRunning}, // re-dispatch goes via queued
		{tickets.StateDraft, tickets.StateConcluded},     // concluding requires a reviewed run
		{tickets.StateRunning, tickets.StateConcluded},   // must land in needs_review first
		{tickets.StateConcluded, tickets.StateQueued},    // concluded is terminal — no exits
	}
	for _, tr := range forbidden {
		if tickets.ValidTransition(tr.from, tr.to) {
			t.Errorf("ValidTransition(%s, %s) = true, want false", tr.from, tr.to)
		}
	}
}

func TestValidStateOriginTaskStatus(t *testing.T) {
	for _, s := range []tickets.State{
		tickets.StateDraft, tickets.StateQueued, tickets.StateRunning,
		tickets.StateNeedsReview, tickets.StateMerged, tickets.StateMergedWithFixes,
		tickets.StateSentBack, tickets.StateAbandoned, tickets.StateConcluded,
		tickets.StateFailed, tickets.StateErrored,
	} {
		if !tickets.ValidState(s) {
			t.Errorf("ValidState(%q) = false, want true", s)
		}
	}
	if tickets.ValidState("open") || tickets.ValidState("") {
		t.Error("ValidState accepted an unknown state")
	}

	for _, o := range []string{"web", "fly", "jira", "retrospective"} {
		if !tickets.ValidOrigin(o) {
			t.Errorf("ValidOrigin(%q) = false, want true", o)
		}
	}
	if tickets.ValidOrigin("email") || tickets.ValidOrigin("") {
		t.Error("ValidOrigin accepted an unknown origin")
	}

	for _, s := range []tickets.TaskStatus{
		tickets.TaskPending, tickets.TaskInProgress, tickets.TaskDone,
		tickets.TaskSkipped, tickets.TaskBlocked,
	} {
		if !tickets.ValidTaskStatus(s) {
			t.Errorf("ValidTaskStatus(%q) = false, want true", s)
		}
	}
	if tickets.ValidTaskStatus("started") {
		t.Error("ValidTaskStatus accepted an unknown status")
	}
}

func TestTerminalStates(t *testing.T) {
	// The terminal set is exactly the valid states with no outgoing edges:
	// nothing will ever run for the ticket again, so the lifecycler may
	// archive its pipelines. Pin both directions so a state-machine edit
	// that adds or removes an exit keeps the set honest.
	terminal := map[tickets.State]bool{}
	for _, s := range tickets.TerminalStates() {
		terminal[s] = true
		if !tickets.IsTerminal(s) {
			t.Errorf("IsTerminal(%q) = false for a member of TerminalStates", s)
		}
		if !tickets.ValidState(s) {
			t.Errorf("terminal state %q is not a valid state", s)
		}
	}

	want := []tickets.State{
		tickets.StateMerged, tickets.StateMergedWithFixes,
		tickets.StateAbandoned, tickets.StateConcluded,
	}
	if len(terminal) != len(want) {
		t.Fatalf("TerminalStates() = %v, want exactly %v", tickets.TerminalStates(), want)
	}
	for _, s := range want {
		if !terminal[s] {
			t.Errorf("TerminalStates() missing %q", s)
		}
	}

	for _, s := range []tickets.State{
		tickets.StateDraft, tickets.StateQueued, tickets.StateRunning,
		tickets.StateNeedsReview, tickets.StateSentBack,
		tickets.StateFailed, tickets.StateErrored,
	} {
		if tickets.IsTerminal(s) {
			t.Errorf("IsTerminal(%q) = true for a state with outgoing edges", s)
		}
	}
}
