package resourcecapture_test

import (
	"context"
	"errors"
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
