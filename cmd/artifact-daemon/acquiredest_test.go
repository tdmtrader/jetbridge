package main

// acquireDest must give MUTUAL EXCLUSION per destination even under a storm of
// acquire/release cycles that churn the map entry's lifetime. The previous
// lock-free refcount passed the "bounded map" test yet allowed two goroutines
// to hold one dest at once, because the decrement-to-zero and the map delete
// were separate steps and a newcomer could register in the gap. This test
// drives exactly that churn and asserts at most one holder per key at any
// instant; run it under -race for the memory-model half.

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
)

func TestAcquireDest_MutualExclusionUnderLifetimeChurn(t *testing.T) {
	root := t.TempDir()
	s := newServerT(t, lagertest.NewTestLogger("acq"), root, "node")

	const (
		keys         = 4
		goroutines   = 32
		perGoroutine = 400
	)

	// One live-holder counter per key. Incremented inside the critical section,
	// decremented on release; if it ever exceeds 1 the lock is not exclusive.
	var holders [keys]atomic.Int32
	var violations atomic.Int64

	// Real in-store destinations: the lock keys on the storage-root-relative
	// form, so a path outside the store has no identity to lock on.
	keyFor := func(i int) string {
		return filepath.Join(root, "steps", "h", []string{"a", "b", "c", "d"}[i%keys])
	}

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				k := (g + i) % keys
				release, err := s.acquireDest(context.Background(), keyFor(k))
				if err != nil {
					t.Errorf("acquire: %v", err)
					return
				}
				if n := holders[k].Add(1); n != 1 {
					violations.Add(1)
				}
				holders[k].Add(-1)
				release()
			}
		}(g)
	}
	wg.Wait()

	if v := violations.Load(); v != 0 {
		t.Fatalf("mutual exclusion violated %d times — two goroutines held one dest", v)
	}

	// The map must be empty once every holder has released.
	s.destLocksMu.Lock()
	n := len(s.destLocks)
	s.destLocksMu.Unlock()
	if n != 0 {
		t.Errorf("dest-lock map retained %d entries after all releases", n)
	}
}

// A waiter blocked on a held destination gives up when its context dies, and
// does not leave the map entry stuck or double-decremented.
func TestAcquireDest_WaiterCancels(t *testing.T) {
	root := t.TempDir()
	s := newServerT(t, lagertest.NewTestLogger("acq"), root, "node")
	dest := filepath.Join(root, "steps", "h", "x")

	hold, err := s.acquireDest(context.Background(), dest)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.acquireDest(ctx, dest); err == nil {
		t.Fatal("a waiter acquired a held destination")
	}

	hold()

	// After the holder releases and the cancelled waiter has left, the entry is
	// reclaimed and re-acquirable.
	release, err := s.acquireDest(context.Background(), dest)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	release()

	s.destLocksMu.Lock()
	n := len(s.destLocks)
	s.destLocksMu.Unlock()
	if n != 0 {
		t.Errorf("map retained %d entries", n)
	}
}
