package dispatch_test

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/atc/db"
)

// The mode vocabulary has ONE definition. agent/dispatch aliases the storage
// constants in atc/db (which the agent_settings CHECK constraint and the
// GET/PUT wire also use), so the two can never drift apart.
func TestModeConstantsAliasTheStorageVocabulary(t *testing.T) {
	for _, pair := range []struct{ mode, storage string }{
		{dispatch.ModeOff, db.DispatcherModeOff},
		{dispatch.ModePaused, db.DispatcherModePaused},
		{dispatch.ModeActive, db.DispatcherModeActive},
	} {
		if pair.mode != pair.storage {
			t.Errorf("dispatch mode %q must be the storage constant %q", pair.mode, pair.storage)
		}
	}
}

func TestResolveEffectiveMode(t *testing.T) {
	cases := []struct {
		name    string
		found   bool
		setting string
		want    string
	}{
		{"seeded off", true, dispatch.ModeOff, dispatch.ModeOff},
		{"seeded active", true, dispatch.ModeActive, dispatch.ModeActive},
		{"seeded paused", true, dispatch.ModePaused, dispatch.ModePaused},
		{"deleted row fails safe to off", false, "", dispatch.ModeOff},
		{"deleted row ignores a stray setting string", false, dispatch.ModeActive, dispatch.ModeOff},
		{"unrecognized stored mode fails safe to off", true, "enabled", dispatch.ModeOff},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dispatch.ResolveEffectiveMode(tc.found, tc.setting); got != tc.want {
				t.Errorf("ResolveEffectiveMode(%v,%q) = %q, want %q", tc.found, tc.setting, got, tc.want)
			}
		})
	}
}

func TestEffectiveModeFromRead(t *testing.T) {
	readErr := errors.New("connection reset")

	// A read fault fails safe to paused REGARDLESS of found/setting — an
	// admin's pause/off must never be overridden by a transient DB blip.
	for _, tc := range []struct {
		name    string
		setting string
		found   bool
	}{
		{"err with active row", dispatch.ModeActive, true},
		{"err with off row", dispatch.ModeOff, true},
		{"err with no row", "", false},
	} {
		if got := dispatch.EffectiveModeFromRead(tc.setting, tc.found, readErr); got != dispatch.ModePaused {
			t.Errorf("%s: read error must fail-safe to paused, got %q", tc.name, got)
		}
	}

	// No error delegates to ResolveEffectiveMode.
	if got := dispatch.EffectiveModeFromRead(dispatch.ModeActive, true, nil); got != dispatch.ModeActive {
		t.Errorf("no error, seeded active: got %q, want active", got)
	}
	if got := dispatch.EffectiveModeFromRead("", false, nil); got != dispatch.ModeOff {
		t.Errorf("no error, no row: got %q, want off", got)
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

func TestDispatcherModeGating(t *testing.T) {
	// Only active dispatches. paused and off are equally dormant: the loop
	// has exactly one job now.
	cases := []struct {
		mode           string
		wantDispatched bool
	}{
		{dispatch.ModeActive, true},
		{dispatch.ModePaused, false},
		{dispatch.ModeOff, false},
		{"nonsense", false},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			deps, store, _, _ := v3DispatchDeps(t)
			queuedID := queuedTicket(t, store, "smoke")
			setRepositorySnapshot(t, store, queuedID, 101)

			mode := tc.mode
			d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{
				Mode: func() string { return mode },
			})
			if err := d.Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}

			got, _, _ := store.Get(queuedID)
			dispatched := got.State != tickets.StateQueued
			if dispatched != tc.wantDispatched {
				t.Errorf("dispatched = %v (state %s), want %v", dispatched, got.State, tc.wantDispatched)
			}
		})
	}
}

func TestDispatcherNilModeDefaultsToActive(t *testing.T) {
	deps, store, ids, _ := loopDeps(t, 1)
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{}) // Mode nil
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _, _ := store.Get(ids[0])
	if got.State != tickets.StateRunning {
		t.Errorf("nil Mode must default to active (dispatch); state = %s", got.State)
	}
}
