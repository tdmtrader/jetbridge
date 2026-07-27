package dispatch

import "github.com/concourse/concourse/atc/db"

// Dispatcher runtime modes. There is ONE vocabulary: these are aliases of the
// storage constants in atc/db (agent_settings.dispatcher_mode and its CHECK
// constraint), which is also the exact string set carried on the GET/PUT
// /api/v1/agent/dispatcher wire. atc/db cannot import this package (it would
// close an import cycle), so the definition lives there and the loop consumes
// it here.
//
//	active — auto-dispatch queued tickets.
//	paused — do not auto-dispatch (the fail-safe a settings-read fault
//	         resolves to; manual `fly agent tickets dispatch` is a separate,
//	         ungated path and still works).
//	off    — do not auto-dispatch (an operator's explicit disable).
//
// No mode disables ticket terminalization: a finished workflow run projects
// its owning ticket to needs_review from the always-on workflow-run
// reconciler. See docs/agentic/README.md.
const (
	ModeOff    = db.DispatcherModeOff
	ModePaused = db.DispatcherModePaused
	ModeActive = db.DispatcherModeActive
)

// ValidMode reports whether s is a recognized dispatcher mode.
func ValidMode(s string) bool {
	switch s {
	case ModeOff, ModePaused, ModeActive:
		return true
	default:
		return false
	}
}

// ResolveEffectiveMode picks the mode the dispatcher loop honors this tick from
// a successful settings read. Migration 1773106137 seeds the singleton row, so
// found is true on every migrated cluster; a deleted row or an unrecognized
// stored string fails safe to off rather than guessing.
func ResolveEffectiveMode(found bool, settingMode string) string {
	if found && ValidMode(settingMode) {
		return settingMode
	}
	return ModeOff
}

// EffectiveModeFromRead resolves the mode for one tick from a settings read,
// applying the fail-safe policy: a read FAULT must never auto-dispatch. On a
// non-nil readErr we cannot tell whether an admin persisted paused/off, so we
// return ModePaused — an admin's pause/off is never overridden by a transient
// DB blip, and dispatch resumes on the next successful read.
func EffectiveModeFromRead(settingMode string, found bool, readErr error) string {
	if readErr != nil {
		return ModePaused
	}
	return ResolveEffectiveMode(found, settingMode)
}
