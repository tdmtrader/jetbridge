package runner

import "time"

// SetClaudeWaitDelay overrides the claude pipe-drain bound for tests and
// returns a restore func.
func SetClaudeWaitDelay(d time.Duration) (restore func()) {
	old := claudeWaitDelay
	claudeWaitDelay = d
	return func() { claudeWaitDelay = old }
}
