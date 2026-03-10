package component_test

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/atc/component"
	"github.com/concourse/concourse/atc/component/cmocks"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/lock/lockfakes"
	"github.com/stretchr/testify/mock"
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
	fakeComponent := new(cmocks.Component)
	fakeRunnable := new(cmocks.Runnable)

	var fakeLock *lockfakes.FakeLock
	if test.LockAvailable {
		fakeLock = new(lockfakes.FakeLock)
		fakeLocker.AcquireReturns(fakeLock, true, nil)
	} else {
		fakeLocker.AcquireReturns(nil, false, test.LockErr)
	}

	componentName := "some-name"

	fakeComponent.On("Name").Return(componentName)

	fakeComponent.On("Reload").Return(!test.Disappeared, test.ReloadErr)

	ctx := context.Background()

	if test.Runs {
		fakeRunnable.On("Run", ctx).Return(test.RunErr).Run(func(mock.Arguments) {
			s.Equal(fakeLock.ReleaseCallCount(), 0, "lock was released too early")
		})
	}

	coordinator := &component.Coordinator{
		Locker:    fakeLocker,
		Component: fakeComponent,
		Runnable:  fakeRunnable,
	}

	coordinator.RunImmediately(ctx)

	if test.Runs {
		fakeRunnable.AssertCalled(s.T(), "Run", ctx)
	} else {
		fakeRunnable.AssertNotCalled(s.T(), "Run")
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
