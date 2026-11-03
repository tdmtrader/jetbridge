package engine

import "time"

// SetRunOutputPendingInterval shortens the wait between finishes of a Run
// build whose output is still settling, and returns the restore.
func SetRunOutputPendingInterval(d time.Duration) func() {
	previous := runOutputPendingInterval
	runOutputPendingInterval = d
	return func() { runOutputPendingInterval = previous }
}
