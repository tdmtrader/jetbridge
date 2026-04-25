package main

import (
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// WorkerPool — bounded concurrency with clean drain semantics.
// ---------------------------------------------------------------------------

func TestWorkerPool_EnforcesMaxConcurrency(t *testing.T) {
	pool := NewWorkerPool(2)
	defer pool.Stop()

	// Every submitted job blocks on `release` until we close it. We track
	// in-flight count via an atomic counter and snapshot the peak.
	release := make(chan struct{})
	var inFlight, peak int32
	started := make(chan struct{}, 5)

	job := func() {
		n := atomic.AddInt32(&inFlight, 1)
		// Track peak in-flight count (best-effort — a small race window
		// here is fine since we sample after every increment).
		for {
			cur := atomic.LoadInt32(&peak)
			if n <= cur || atomic.CompareAndSwapInt32(&peak, cur, n) {
				break
			}
		}
		started <- struct{}{}
		<-release
		atomic.AddInt32(&inFlight, -1)
	}

	for i := 0; i < 5; i++ {
		if !pool.Submit(job) {
			t.Fatalf("Submit returned false on a running pool (i=%d)", i)
		}
	}

	// Two jobs should start; the rest queue.
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatalf("expected 2 jobs to start, got %d", i)
		}
	}

	// Give the pool a brief grace window — if concurrency is broken, a
	// third job will leak through into running state.
	select {
	case <-started:
		t.Fatal("third job started while concurrency limit should hold")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)

	// Drain the rest by waiting for the remaining 3 starts.
	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatalf("expected remaining job %d to start after release", i)
		}
	}

	if got := atomic.LoadInt32(&peak); got > 2 {
		t.Errorf("peak concurrency %d exceeded limit 2", got)
	}
}

func TestWorkerPool_DrainsOnStop(t *testing.T) {
	pool := NewWorkerPool(2)

	var completed int32
	for i := 0; i < 4; i++ {
		pool.Submit(func() {
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt32(&completed, 1)
		})
	}

	pool.Stop()
	pool.Wait()

	if got := atomic.LoadInt32(&completed); got != 4 {
		t.Errorf("expected all 4 jobs to complete on drain, got %d", got)
	}
}

func TestWorkerPool_RejectsSubmitAfterStop(t *testing.T) {
	pool := NewWorkerPool(1)
	pool.Stop()
	pool.Wait()

	if pool.Submit(func() {}) {
		t.Error("Submit returned true on a stopped pool — should reject")
	}
}
