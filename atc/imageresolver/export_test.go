package imageresolver

import "time"

// SetResolveTimeout shortens the bound on one resolve for a test and returns
// a func that restores it.
func SetResolveTimeout(d time.Duration) func() {
	previous := resolveTimeout
	resolveTimeout = d
	return func() { resolveTimeout = previous }
}
