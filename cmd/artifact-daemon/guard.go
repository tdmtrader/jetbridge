package main

import (
	"context"
	"sync"
	"time"
)

// ReadGuard coordinates artifact directory reads with destructive operations
// (TTL sweeps, DELETE, stream-in replaces) inside the daemon process.
//
// Without it, deleters race in-flight tree walks: cp -R and filepath.Walk
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

// BeginReadContext is BeginRead with cancellation while waiting for a
// conflicting destructive operation. Resolve paths use it so their operation
// deadline covers guard admission instead of occupying a daemon-wide resolve
// slot behind an unbounded RWMutex wait.
func (g *ReadGuard) BeginReadContext(ctx context.Context, handle string) (func(), error) {
	if g == nil {
		return func() {}, nil
	}
	l := g.acquire(handle)
	if err := waitForGuard(ctx, l.mu.TryRLock); err != nil {
		g.release(handle, l)
		return nil, err
	}
	return func() {
		l.mu.RUnlock()
		g.release(handle, l)
	}, nil
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

// BeginSweepContext is BeginSweep with cancellation while waiting for active
// readers. It is used by peer resolve publication, whose exclusive destination
// lock is part of the bounded resolve operation.
func (g *ReadGuard) BeginSweepContext(ctx context.Context, handle string) (func(), error) {
	if g == nil {
		return func() {}, nil
	}
	l := g.acquire(handle)
	if err := waitForGuard(ctx, l.mu.TryLock); err != nil {
		g.release(handle, l)
		return nil, err
	}
	return func() {
		l.mu.Unlock()
		g.release(handle, l)
	}, nil
}

// BeginResolveContext holds the source for reading and the destination for
// destructive publication. Different handles are always acquired in lexical
// order, independent of their lock mode, preventing A->B and B->A resolves
// from deadlocking. When both paths share a handle, one exclusive lock covers
// both authorities.
func (g *ReadGuard) BeginResolveContext(ctx context.Context, sourceHandle, destinationHandle string) (func(), error) {
	if g == nil {
		return func() {}, nil
	}
	if sourceHandle == destinationHandle {
		return g.BeginSweepContext(ctx, sourceHandle)
	}
	type acquisition struct {
		handle string
		sweep  bool
	}
	first := acquisition{handle: sourceHandle}
	second := acquisition{handle: destinationHandle, sweep: true}
	if second.handle < first.handle {
		first, second = second, first
	}
	acquire := func(item acquisition) (func(), error) {
		if item.sweep {
			return g.BeginSweepContext(ctx, item.handle)
		}
		return g.BeginReadContext(ctx, item.handle)
	}
	releaseFirst, err := acquire(first)
	if err != nil {
		return nil, err
	}
	releaseSecond, err := acquire(second)
	if err != nil {
		releaseFirst()
		return nil, err
	}
	return func() {
		releaseSecond()
		releaseFirst()
	}, nil
}

func waitForGuard(ctx context.Context, try func() bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if try() {
			return nil
		}
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			// Go 1.23+ timer channels are synchronous. A failed Stop does not
			// imply a value is available to drain and attempting one can block.
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
