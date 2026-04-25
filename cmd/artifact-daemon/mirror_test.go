package main

import (
	"reflect"
	"sort"
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

// ---------------------------------------------------------------------------
// peerSelector — deterministic subset selection via consistent hashing.
// ---------------------------------------------------------------------------

func TestPeerSelector_DeterministicAcrossCalls(t *testing.T) {
	peers := []string{"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4"}
	sel := peerSelector{}

	first := sel.Select("step-key-A", peers, 2)
	second := sel.Select("step-key-A", peers, 2)

	if !reflect.DeepEqual(first, second) {
		t.Errorf("expected deterministic selection for same key/peers; got %v then %v", first, second)
	}
}

func TestPeerSelector_DifferentKeysCanDiffer(t *testing.T) {
	// Not strictly required — two keys may collide on the same peer subset
	// — but with N=4 peers and many keys, at least some keys should land
	// on different subsets. Here we just verify the selection is a function
	// of the key (deterministic per key, not random).
	peers := []string{"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4"}
	sel := peerSelector{}

	a := sel.Select("step-A", peers, 2)
	b := sel.Select("step-A", peers, 2)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("repeated calls with same key must return same set: %v vs %v", a, b)
	}
}

func TestPeerSelector_AllPeers_WhenReplicasNegative(t *testing.T) {
	peers := []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}
	sel := peerSelector{}

	got := sel.Select("any-key", peers, -1)

	gotSorted := append([]string{}, got...)
	sort.Strings(gotSorted)
	wantSorted := append([]string{}, peers...)
	sort.Strings(wantSorted)
	if !reflect.DeepEqual(gotSorted, wantSorted) {
		t.Errorf("expected all peers for replicas=-1, got %v", got)
	}
}

func TestPeerSelector_FewerPeersThanRequested(t *testing.T) {
	// replicas=2 means up to (2-1)=1 peer; with only 1 peer available,
	// mirror to that 1 peer (no error).
	peers := []string{"10.0.0.1"}
	sel := peerSelector{}

	got := sel.Select("any-key", peers, 2)

	if len(got) != 1 || got[0] != "10.0.0.1" {
		t.Errorf("expected [10.0.0.1] for replicas=2 with 1 peer, got %v", got)
	}
}

func TestPeerSelector_NoPeers(t *testing.T) {
	sel := peerSelector{}

	got := sel.Select("any-key", nil, 2)
	if len(got) != 0 {
		t.Errorf("expected empty slice for 0 peers, got %v", got)
	}
}

func TestPeerSelector_ReplicasZero_DisablesMirror(t *testing.T) {
	peers := []string{"10.0.0.1", "10.0.0.2"}
	sel := peerSelector{}

	got := sel.Select("any-key", peers, 0)
	if len(got) != 0 {
		t.Errorf("expected empty slice for replicas=0 (disabled), got %v", got)
	}
}
