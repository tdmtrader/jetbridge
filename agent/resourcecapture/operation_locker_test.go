package resourcecapture_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/agent/resourcecapture"
	dblock "github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/lock/lockfakes"
)

type releaseLockStub struct {
	calls int
	err   error
}

func (lock *releaseLockStub) Release() error { lock.calls++; return lock.err }

func TestDBOperationLockerRetriesReleasesAndHonorsCancellation(t *testing.T) {
	factory := &lockfakes.FakeLockFactory{}
	acquiredLock := &releaseLockStub{}
	attempts := 0
	factory.AcquireStub = func(lager.Logger, dblock.LockID) (dblock.Lock, bool, error) {
		attempts++
		if attempts == 1 {
			return nil, false, nil
		}
		return acquiredLock, true, nil
	}
	locker, err := resourcecapture.NewDBOperationLocker(lager.NewLogger("test"), factory)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	if err := locker.WithLock(context.Background(), "resource-capture/key", func() error { called = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if !called || attempts != 2 || acquiredLock.calls != 1 {
		t.Fatalf("called=%v attempts=%d releases=%d", called, attempts, acquiredLock.calls)
	}

	factory.AcquireStub = func(lager.Logger, dblock.LockID) (dblock.Lock, bool, error) { return nil, false, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
	defer cancel()
	if err := locker.WithLock(ctx, "resource-capture/key", func() error { t.Fatal("action called"); return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled lock error = %v", err)
	}
}

func TestDBOperationLockerJoinsActionAndReleaseErrors(t *testing.T) {
	actionErr, releaseErr := errors.New("action"), errors.New("release")
	factory := &lockfakes.FakeLockFactory{}
	factory.AcquireReturns(&releaseLockStub{err: releaseErr}, true, nil)
	locker, err := resourcecapture.NewDBOperationLocker(lager.NewLogger("test"), factory)
	if err != nil {
		t.Fatal(err)
	}
	err = locker.WithLock(context.Background(), "resource-capture/key", func() error { return actionErr })
	if !errors.Is(err, actionErr) || !errors.Is(err, releaseErr) {
		t.Fatalf("joined error = %v", err)
	}
}

// inMemoryLockDB is a lock database with pg_try_advisory_lock semantics and no
// PostgreSQL: an id that is already held is refused, and releasing an unheld id
// reports false the way the real one does.
//
// Combined with lock.NewTestLockFactory it produces a REAL lock.LockFactory —
// the same *lockFactory type production uses, with the same lockRepo and the
// same acquireMutex — so the exclusion under test is the code's own, not the
// test's. The existing tests in this file fake contention with a call counter
// and therefore prove only that WithLock retries.
type inMemoryLockDB struct {
	mu   sync.Mutex
	held map[string]bool
}

func newInMemoryLockDB() *inMemoryLockDB {
	return &inMemoryLockDB{held: map[string]bool{}}
}

func (db *inMemoryLockDB) key(id dblock.LockID) string {
	parts := make([]string, len(id))
	for i, value := range id {
		parts[i] = strconv.Itoa(value)
	}
	return strings.Join(parts, "+")
}

func (db *inMemoryLockDB) Acquire(id dblock.LockID) (bool, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	key := db.key(id)
	if db.held[key] {
		return false, nil
	}
	db.held[key] = true
	return true, nil
}

func (db *inMemoryLockDB) Release(id dblock.LockID) (bool, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	key := db.key(id)
	if !db.held[key] {
		return false, nil
	}
	delete(db.held, key)
	return true, nil
}

// TestDBOperationLockerExcludesConcurrentHoldersOfOneKey is the mutual-exclusion
// proof: eight real goroutines contend for one key against a lock factory that
// genuinely excludes, and an atomic counter incremented on entry and decremented
// on exit must never be observed above one.
//
// Overlap is counted rather than asserted per iteration so a failure reports how
// often exclusion broke, and every action sleeps long enough that two
// simultaneous holders would overlap in wall-clock time rather than merely in
// principle.
func TestDBOperationLockerExcludesConcurrentHoldersOfOneKey(t *testing.T) {
	factory := dblock.NewTestLockFactory(newInMemoryLockDB())
	locker, err := resourcecapture.NewDBOperationLocker(lager.NewLogger("test"), factory)
	if err != nil {
		t.Fatal(err)
	}

	const goroutines = 8
	var inside, overlaps, completed atomic.Int64
	start := make(chan struct{})
	failures := make(chan error, goroutines)
	var group sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			err := locker.WithLock(context.Background(), "resource-capture/shared", func() error {
				if inside.Add(1) != 1 {
					overlaps.Add(1)
				}
				time.Sleep(5 * time.Millisecond)
				inside.Add(-1)
				completed.Add(1)
				return nil
			})
			if err != nil {
				failures <- err
			}
		}()
	}
	close(start)
	group.Wait()
	close(failures)

	for err := range failures {
		t.Fatalf("WithLock() error = %v", err)
	}
	if got := overlaps.Load(); got != 0 {
		t.Fatalf("%d critical-section overlaps: the operation lock does not exclude", got)
	}
	if got := completed.Load(); got != goroutines {
		t.Fatalf("%d of %d contenders ran their action", got, goroutines)
	}
	if got := inside.Load(); got != 0 {
		t.Fatalf("critical-section counter settled at %d, want 0", got)
	}
}

// TestDBOperationLockerDoesNotSerializeDistinctKeys is the other half: exclusion
// that is really a global mutex would pass the test above and would quietly
// serialize every unrelated capture. Distinct keys must all be held at once.
func TestDBOperationLockerDoesNotSerializeDistinctKeys(t *testing.T) {
	factory := dblock.NewTestLockFactory(newInMemoryLockDB())
	locker, err := resourcecapture.NewDBOperationLocker(lager.NewLogger("test"), factory)
	if err != nil {
		t.Fatal(err)
	}

	// WithLock retries forever on an uncancellable context, so the failure path
	// below must be able to unwedge the goroutines it is about to wait on.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const goroutines = 4
	entered := make(chan struct{}, goroutines)
	release := make(chan struct{})
	failures := make(chan error, goroutines)
	var group sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		key := "resource-capture/key-" + strconv.Itoa(i)
		group.Add(1)
		go func() {
			defer group.Done()
			err := locker.WithLock(ctx, key, func() error {
				entered <- struct{}{}
				<-release
				return nil
			})
			if err != nil {
				failures <- err
			}
		}()
	}
	for i := 0; i < goroutines; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			close(release)
			cancel()
			group.Wait()
			t.Fatalf("only %d of %d distinct keys were held at once; distinct operations are being serialized", i, goroutines)
		}
	}
	close(release)
	group.Wait()
	close(failures)
	for err := range failures {
		t.Fatalf("WithLock() error = %v", err)
	}
}
