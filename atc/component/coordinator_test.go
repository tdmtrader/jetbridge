package component_test

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/atc/component"
	"github.com/concourse/concourse/atc/component/componentfakes"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/lock/lockfakes"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

func TestCoordinator(t *testing.T) {
	suite.Run(t, &CoordinatorSuite{
		Assertions: require.New(t),
	})
}

type CoordinatorSuite struct {
	suite.Suite
	*require.Assertions
}

type CoordinatorTest struct {
	It string

	LockAvailable bool
	LockErr       error

	Disappeared bool
	ReloadErr   error

	Runs   bool
	RunErr error
}

func (test CoordinatorTest) Run(s *CoordinatorSuite) {
	fakeLocker := new(lockfakes.FakeLockFactory)
	fakeComponent := new(componentfakes.FakeComponent)
	fakeRunnable := new(componentfakes.FakeRunnable)

	var fakeLock *lockfakes.FakeLock
	if test.LockAvailable {
		fakeLock = new(lockfakes.FakeLock)
		fakeLocker.AcquireReturns(fakeLock, true, nil)
	} else {
		fakeLocker.AcquireReturns(nil, false, test.LockErr)
	}

	componentName := "some-name"

	fakeComponent.NameReturns(componentName)

	fakeComponent.ReloadReturns(!test.Disappeared, test.ReloadErr)

	ctx := context.Background()

	if test.Runs {
		fakeRunnable.RunCalls(func(context.Context) error {
			s.Equal(fakeLock.ReleaseCallCount(), 0, "lock was released too early")
			return test.RunErr
		})
	}

	coordinator := &component.Coordinator{
		Locker:    fakeLocker,
		Component: fakeComponent,
		Runnable:  fakeRunnable,
	}

	coordinator.RunImmediately(ctx)

	if test.Runs {
		s.Equal(1, fakeRunnable.RunCallCount(), "component did not run")
		s.Equal(ctx, fakeRunnable.RunArgsForCall(0), "component ran with wrong context")
	} else {
		s.Equal(0, fakeRunnable.RunCallCount(), "component ran when it should not have")
	}

	if test.LockAvailable {
		_, acquiredLock := fakeLocker.AcquireArgsForCall(0)
		s.Equal(lock.NewTaskLockID(componentName), acquiredLock, "acquired wrong lock")
		s.Equal(1, fakeLock.ReleaseCallCount(), "lock was not released")
	}
}

func (s *CoordinatorSuite) TestRunImmediately() {
	someErr := errors.New("oh noes")

	for _, t := range []CoordinatorTest{
		{
			It: "runs if the lock is available",

			LockAvailable: true,

			Runs: true,
		},
		{
			It: "does not run if lock is unavailable",

			LockAvailable: false,

			Runs: false,
		},
		{
			It: "does not run if acquiring the lock errors",

			LockErr: someErr,

			Runs: false,
		},
		{
			It: "does not run if reloading the component errors",

			LockAvailable: true,
			ReloadErr:     someErr,

			Runs: false,
		},
		{
			It: "does not run if the component disappeared",

			LockAvailable: true,
			Disappeared:   true,

			Runs: false,
		},
	} {
		s.Run(t.It, func() {
			t.Run(s)
		})
	}
}
