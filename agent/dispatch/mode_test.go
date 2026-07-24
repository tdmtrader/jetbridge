package dispatch_test

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/atc/db"
)

func TestResolveEffectiveMode(t *testing.T) {
	cases := []struct {
		name     string
		found    bool
		setting  string
		bootFlag bool
		want     string
	}{
		{"setting wins over boot flag on", true, dispatch.ModeOff, true, dispatch.ModeOff},
		{"setting wins over boot flag off", true, dispatch.ModeActive, false, dispatch.ModeActive},
		{"paused setting honored", true, dispatch.ModePaused, true, dispatch.ModePaused},
		{"no row, boot flag on -> active", false, "", true, dispatch.ModeActive},
		{"no row, boot flag off -> off", false, "", false, dispatch.ModeOff},
		{"no row ignores stray setting string", false, dispatch.ModeActive, false, dispatch.ModeOff},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dispatch.ResolveEffectiveMode(tc.found, tc.setting, tc.bootFlag); got != tc.want {
				t.Errorf("ResolveEffectiveMode(%v,%q,%v) = %q, want %q",
					tc.found, tc.setting, tc.bootFlag, got, tc.want)
			}
		})
	}
}

func TestEffectiveModeFromRead(t *testing.T) {
	readErr := errors.New("connection reset")

	// A read fault fails safe to paused REGARDLESS of found/setting/bootFlag —
	// an admin's pause/off must never be overridden by a transient DB blip,
	// and a boot flag seeded "active" must not resume dispatch on error.
	for _, tc := range []struct {
		name     string
		setting  string
		found    bool
		bootFlag bool
	}{
		{"err with paused row + boot active", dispatch.ModePaused, true, true},
		{"err with off row + boot active", dispatch.ModeOff, true, true},
		{"err with no row + boot active", "", false, true},
		{"err with no row + boot off", "", false, false},
	} {
		if got := dispatch.EffectiveModeFromRead(tc.setting, tc.found, readErr, tc.bootFlag); got != dispatch.ModePaused {
			t.Errorf("%s: read error must fail-safe to paused, got %q", tc.name, got)
		}
	}

	// No error delegates to ResolveEffectiveMode.
	if got := dispatch.EffectiveModeFromRead(dispatch.ModeOff, true, nil, true); got != dispatch.ModeOff {
		t.Errorf("no error, found row: got %q, want off", got)
	}
	if got := dispatch.EffectiveModeFromRead("", false, nil, true); got != dispatch.ModeActive {
		t.Errorf("no error, no row, boot on: got %q, want active", got)
	}
	if got := dispatch.EffectiveModeFromRead("", false, nil, false); got != dispatch.ModeOff {
		t.Errorf("no error, no row, boot off: got %q, want off", got)
	}
}

func TestValidMode(t *testing.T) {
	for _, m := range []string{dispatch.ModeOff, dispatch.ModePaused, dispatch.ModeActive} {
		if !dispatch.ValidMode(m) {
			t.Errorf("ValidMode(%q) = false, want true", m)
		}
	}
	for _, m := range []string{"", "on", "ACTIVE", "disabled"} {
		if dispatch.ValidMode(m) {
			t.Errorf("ValidMode(%q) = true, want false", m)
		}
	}
}

// countingRunReader records reconciler invocations without a DB. Reporting the
// run as vanished (found=false) is enough to prove the reconcile pass ran.
type countingRunReader struct{ calls int }

func (r *countingRunReader) GetRunByID(int) (db.PipelineRun, bool, error) {
	r.calls++
	return nil, false, nil
}

// runningTicketWithRun creates a ticket already in the running state with a
// pipeline_run_id — the only shape reconcileCompletedRuns acts on.
func runningTicketWithRun(t *testing.T, store *tickets.MemoryStore, runID int) int {
	t.Helper()
	id := queuedTicket(t, store, "smoke")
	if err := store.Transition(id, tickets.StateQueued, tickets.StateRunning, tickets.TransitionMeta{PipelineRunID: &runID}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestDispatcherModeGating(t *testing.T) {
	cases := []struct {
		mode           string
		wantDispatched bool // queued ticket -> running
		wantReconciled bool // reconciler visited the running ticket
	}{
		{dispatch.ModeActive, true, true},
		{dispatch.ModePaused, false, true},
		{dispatch.ModeOff, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			deps, store, _, _ := v3DispatchDeps(t)
			queuedID := queuedTicket(t, store, "smoke")
			setRepositorySnapshot(t, store, queuedID, 101)
			runningTicketWithRun(t, store, 900)

			reader := &countingRunReader{}
			mode := tc.mode
			d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{
				Mode:      func() string { return mode },
				RunReader: reader,
			})
			if err := d.Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}

			// A dispatched ticket leaves queued (active mode may then reconcile
			// it onward to needs_review in the same pass — either way, no longer
			// queued). paused/off leave it queued.
			got, _, _ := store.Get(queuedID)
			dispatched := got.State != tickets.StateQueued
			if dispatched != tc.wantDispatched {
				t.Errorf("dispatched = %v (state %s), want %v", dispatched, got.State, tc.wantDispatched)
			}

			reconciled := reader.calls > 0
			if reconciled != tc.wantReconciled {
				t.Errorf("reconciled = %v (reader.calls=%d), want %v", reconciled, reader.calls, tc.wantReconciled)
			}
		})
	}
}

func TestDispatcherNilModeDefaultsToActive(t *testing.T) {
	deps, store, ids := loopDeps(t, 1)
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{}) // Mode nil
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _, _ := store.Get(ids[0])
	if got.State != tickets.StateRunning {
		t.Errorf("nil Mode must default to active (dispatch); state = %s", got.State)
	}
}
