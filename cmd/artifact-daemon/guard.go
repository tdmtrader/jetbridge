package main

import "sync"

// ReadGuard coordinates artifact directory reads with destructive operations
// (TTL sweeps, DELETE) inside the daemon process.
//
// Without it, the sweeper's os.RemoveAll races an in-flight resolve copy:
// cp -R silently omits files that were deleted before it enumerated their
// parent directory, so the copy exits 0 with a subset of the tree and the
// resolve reports "ok" — a partial artifact served as complete (e.g. a repo
// input missing package-lock.json).
//
// Reads (resolve copies, GET tar streams) hold the shared side for the
// duration of the read; deleters take the exclusive side per directory and
// re-check freshness before removing. The lock is global rather than
// per-key: sweeps are rare (5m tick) and deletions brief, so the coarse
// granularity costs nothing measurable.
type ReadGuard struct {
	mu sync.RWMutex
}

// NewReadGuard returns a ReadGuard.
func NewReadGuard() *ReadGuard {
	return &ReadGuard{}
}

// BeginRead takes the shared side. The returned func releases it; call it
// via defer so panics (e.g. the tar-abort path) cannot leak the lock.
func (g *ReadGuard) BeginRead() func() {
	if g == nil {
		return func() {}
	}
	g.mu.RLock()
	return g.mu.RUnlock
}

// BeginSweep takes the exclusive side, waiting out any in-flight reads.
// The returned func releases it.
func (g *ReadGuard) BeginSweep() func() {
	if g == nil {
		return func() {}
	}
	g.mu.Lock()
	return g.mu.Unlock
}
