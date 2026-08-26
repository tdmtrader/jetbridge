package main

import "sync"

// ReadGuard coordinates artifact directory reads with destructive operations
// (TTL sweeps, DELETE, stream-in replaces) inside the daemon process.
//
// Without it, deleters race in-flight tree walks: a copy and a tar walk
// both silently omit files that were deleted before they enumerated the
// parent directory, so the read completes "successfully" with a subset of
// the tree — a partial artifact served (or mirrored onward) as complete.
//
// Locking is keyed by step handle so contention is per-artifact: a mirror
// PUT streaming one artifact for minutes must not stall reads or replaces
// of every other artifact on the node. Readers (resolve copies, GET tar
// streams, mirror tar walks) hold the shared side for the handle; deleters
// (sweeper, DELETE, stream-in's remove+rename) take the exclusive side.
type ReadGuard struct {
	mu    sync.Mutex
	locks map[string]*handleLock
}

type handleLock struct {
	refs int
	mu   sync.RWMutex
}

// NewReadGuard returns a ReadGuard.
func NewReadGuard() *ReadGuard {
	return &ReadGuard{locks: map[string]*handleLock{}}
}

func (g *ReadGuard) acquire(handle string) *handleLock {
	g.mu.Lock()
	defer g.mu.Unlock()
	l := g.locks[handle]
	if l == nil {
		l = &handleLock{}
		g.locks[handle] = l
	}
	l.refs++
	return l
}

func (g *ReadGuard) release(handle string, l *handleLock) {
	g.mu.Lock()
	defer g.mu.Unlock()
	l.refs--
	if l.refs == 0 {
		delete(g.locks, handle)
	}
}

// BeginRead takes the shared side for the handle. The returned func releases
// it; call it via defer so panics (e.g. the tar-abort path) cannot leak the
// lock.
func (g *ReadGuard) BeginRead(handle string) func() {
	if g == nil {
		return func() {}
	}
	l := g.acquire(handle)
	l.mu.RLock()
	return func() {
		l.mu.RUnlock()
		g.release(handle, l)
	}
}

// BeginSweep takes the exclusive side for the handle, waiting out any
// in-flight reads of it. The returned func releases it.
func (g *ReadGuard) BeginSweep(handle string) func() {
	if g == nil {
		return func() {}
	}
	l := g.acquire(handle)
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		g.release(handle, l)
	}
}
