package lidar

import "time"

// SetNativeResolveTimeout replaces the bound on one native registry-image
// resolve and returns a func that restores the previous bound.
func SetNativeResolveTimeout(d time.Duration) (restore func()) {
	previous := nativeResolveTimeout
	nativeResolveTimeout = d
	return func() { nativeResolveTimeout = previous }
}
