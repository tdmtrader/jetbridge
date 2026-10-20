package db

import "time"

// SetEventPageTimeout replaces the bound on one event page and returns a func
// that restores the previous bound.
func SetEventPageTimeout(d time.Duration) (restore func()) {
	previous := eventPageTimeout
	eventPageTimeout = d
	return func() { eventPageTimeout = previous }
}
