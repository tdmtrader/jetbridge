//go:build !linux

package launcher

// becomeSubreaper is Linux-only; elsewhere orphans go to PID 1 as usual.
func becomeSubreaper() {}
