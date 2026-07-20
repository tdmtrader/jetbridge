package dispatch

// Dispatcher runtime modes. These are the effective modes the loop honors and
// the exact strings carried on the GET/PUT /api/v1/agent/dispatcher wire and
// stored in agent_settings.dispatcher_mode.
//
//	active — auto-dispatch queued tickets AND run the completion reconciler.
//	paused — do NOT auto-dispatch, but DO run the reconciler (safety net stays
//	         alive; manual `fly agent tickets dispatch` still works — separate
//	         ungated path).
//	off    — do NOT auto-dispatch and do NOT reconcile (fully dormant;
//	         equivalent to the historical --agent-dispatcher-enabled=false).
const (
	ModeOff    = "off"
	ModePaused = "paused"
	ModeActive = "active"
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

// ResolveEffectiveMode picks the mode the dispatcher loop honors this tick.
// If a persisted setting exists (found), it wins verbatim. Otherwise fall back
// to the --agent-dispatcher-enabled boot flag: true->active, false->off. This
// preserves current live behavior (flag off -> effective off -> no
// auto-dispatch) until an admin sets a mode at runtime.
func ResolveEffectiveMode(found bool, settingMode string, bootFlag bool) string {
	if found {
		return settingMode
	}
	if bootFlag {
		return ModeActive
	}
	return ModeOff
}

// EffectiveModeFromRead resolves the mode for one tick from a settings read,
// applying the fail-safe policy: a read FAULT must never auto-dispatch. On a
// non-nil readErr we cannot tell whether an admin persisted paused/off, so we
// return ModePaused — no auto-dispatch (an admin's pause/off is never
// overridden by a transient DB blip) while keeping the reconciler safety net
// alive; dispatch resumes on the next successful read. Only when the read
// succeeds do we honor a persisted setting or the boot-flag seed. The boot
// flag is deliberately NOT consulted on error: falling back to it could resume
// auto-dispatch against an explicit pause whenever the flag seed is "active".
func EffectiveModeFromRead(settingMode string, found bool, readErr error, bootFlag bool) string {
	if readErr != nil {
		return ModePaused
	}
	return ResolveEffectiveMode(found, settingMode, bootFlag)
}
