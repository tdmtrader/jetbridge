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
