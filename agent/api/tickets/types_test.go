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

// v3-only cleanup (2026-07-24): pipeline_run_id is server-owned and is no
// longer a writable TransitionRequest field. A body that still carries the
// retired key must be rejected by the decoder — not silently ignored — so a
// stale client learns its write was dropped. A body without it decodes the
// remaining fields normally.
func TestTransitionRequestRejectsServerOwnedPipelineRunID(t *testing.T) {
	var withKey tickets.TransitionRequest
	err := json.Unmarshal([]byte(`{"from":"queued","to":"running","pipeline_run_id":42}`), &withKey)
	if err == nil || err.Error() != "pipeline_run_id is server-owned" {
		t.Fatalf("decoding pipeline_run_id error = %v, want \"pipeline_run_id is server-owned\"", err)
	}

	// even a null value for the retired key is a rejection: presence is what
	// matters, not the value.
	if err := json.Unmarshal([]byte(`{"from":"queued","to":"running","pipeline_run_id":null}`), &withKey); err == nil {
		t.Errorf("null pipeline_run_id decoded without error, want rejection")
	}

	var clean tickets.TransitionRequest
	if err := json.Unmarshal([]byte(`{"from":"queued","to":"running","branch":"b"}`), &clean); err != nil {
		t.Fatalf("clean transition decode = %v, want nil", err)
	}
	if clean.From != tickets.StateQueued || clean.To != tickets.StateRunning || clean.Branch != "b" {
		t.Errorf("clean transition = %+v", clean)
	}
}

func TestValidTransitionMatrix(t *testing.T) {
	allowed := []struct{ from, to tickets.State }{
		{tickets.StateDraft, tickets.StateQueued},
		{tickets.StateDraft, tickets.StateClosed},
		{tickets.StateQueued, tickets.StateRunning},
		{tickets.StateQueued, tickets.StateDraft},
		{tickets.StateQueued, tickets.StateClosed},
		// running→queued records a retry attempt. Do not narrow this edge.
		{tickets.StateRunning, tickets.StateQueued},
		// running→needs_review is the ONLY completion edge, written by
		// dispatch's run-completion reconciler however the run ended.
		{tickets.StateRunning, tickets.StateNeedsReview},
		// needs_review→closed is the single human close action; the durable
		// run's outcome carries WHY (merged / dropped / analysis-only).
		{tickets.StateNeedsReview, tickets.StateClosed},
		{tickets.StateNeedsReview, tickets.StateQueued},
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
		{tickets.StateRunning, tickets.StateClosed},      // a run lands in front of a human first
		{tickets.StateClosed, tickets.StateQueued},       // closed is terminal — no exits
		{tickets.StateClosed, tickets.StateNeedsReview},  //
		{tickets.StateNeedsReview, tickets.StateRunning}, // re-dispatch goes via queued
	}
	for _, tr := range forbidden {
		if tickets.ValidTransition(tr.from, tr.to) {
			t.Errorf("ValidTransition(%s, %s) = true, want false", tr.from, tr.to)
		}
	}
}

// The queue vocabulary is exactly five tokens. The v2 disposition verbs were
// deleted with the per-ticket outcome mirror: a client that still sends one
// must be rejected, not quietly accepted into a state nothing can leave.
func TestRetiredDispositionStatesAreRejected(t *testing.T) {
	for _, retired := range []tickets.State{
		"merged", "merged_with_fixes", "sent_back", "concluded",
		"abandoned", "failed", "errored",
	} {
		if tickets.ValidState(retired) {
			t.Errorf("ValidState(%q) = true; the disposition verbs live on the workflow run now", retired)
		}
		if tickets.ValidTransition(tickets.StateNeedsReview, retired) {
			t.Errorf("ValidTransition(needs_review, %q) = true, want false", retired)
		}
	}
}

func TestValidStateAndOrigin(t *testing.T) {
	for _, s := range []tickets.State{
		tickets.StateDraft, tickets.StateQueued, tickets.StateRunning,
		tickets.StateNeedsReview, tickets.StateClosed,
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

	want := []tickets.State{tickets.StateClosed}
	if len(terminal) != len(want) {
		t.Fatalf("TerminalStates() = %v, want exactly %v", tickets.TerminalStates(), want)
	}
	for _, s := range want {
		if !terminal[s] {
			t.Errorf("TerminalStates() missing %q", s)
		}
	}

	for _, s := range []tickets.State{
		tickets.StateDraft, tickets.StateQueued,
		tickets.StateRunning, tickets.StateNeedsReview,
	} {
		if tickets.IsTerminal(s) {
			t.Errorf("IsTerminal(%q) = true for a state with outgoing edges", s)
		}
	}
}
