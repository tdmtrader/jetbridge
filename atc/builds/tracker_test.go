package builds_test

import (
	"context"
	"io"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/atc/builds"
	"github.com/concourse/concourse/atc/builds/buildsfakes"
	"github.com/concourse/concourse/atc/component"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/util"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

func init() {
	util.PanicSink = io.Discard
}

type TrackerSuite struct {
	suite.Suite
	*require.Assertions

	fakeBuildFactory *dbfakes.FakeBuildFactory
	fakeEngine       *buildsfakes.FakeEngine

	tracker   *builds.Tracker
	buildChan chan db.Build

	logger *lagertest.TestLogger
}

func TestTracker(t *testing.T) {
	suite.Run(t, &TrackerSuite{
		Assertions: require.New(t),
	})
}

func (s *TrackerSuite) SetupTest() {
	s.logger = lagertest.NewTestLogger("test")
	s.fakeBuildFactory = new(dbfakes.FakeBuildFactory)
	s.fakeEngine = new(buildsfakes.FakeEngine)
	s.buildChan = make(chan db.Build, 10)

	s.tracker = builds.NewTracker(
		s.logger,
		s.fakeBuildFactory,
		s.fakeEngine,
		s.buildChan,
	)
}

func (s *TrackerSuite) TestTrackRunsStartedBuilds() {
	startedBuilds := []db.Build{}
	for i := range 3 {
		fakeBuild := new(dbfakes.FakeBuild)
		fakeBuild.IDReturns(i + 1)
		startedBuilds = append(startedBuilds, fakeBuild)
	}

	s.fakeBuildFactory.GetAllStartedBuildsReturns(startedBuilds, nil)

	running := make(chan db.Build, 3)
	s.fakeEngine.NewBuildStub = func(build db.Build) builds.Runnable {
		engineBuild := new(buildsfakes.FakeRunnable)
		engineBuild.RunStub = func(context.Context) {
			running <- build
		}

		return engineBuild
	}

	err := s.tracker.Run(context.TODO())
	s.NoError(err)

	s.ElementsMatch([]int{
		startedBuilds[0].ID(),
		startedBuilds[1].ID(),
		startedBuilds[2].ID(),
	}, []int{
		(<-running).ID(),
		(<-running).ID(),
		(<-running).ID(),
	})
}

func (s *TrackerSuite) TestTrackInMemoryBuilds() {
	inMemoryBuilds := []db.Build{}

	running := make(chan db.Build, 3)
	s.fakeEngine.NewBuildStub = func(build db.Build) builds.Runnable {
		engineBuild := new(buildsfakes.FakeRunnable)
		engineBuild.RunStub = func(context.Context) {
			running <- build
		}
		return engineBuild
	}

	for i := range 3 {
		fakeBuild := new(dbfakes.FakeBuild)
		// When tracked, in-memory builds have no id yet, but they do have a
		// resource ID
		fakeBuild.IDReturns(0)
		fakeBuild.ResourceIDReturns(i + 1)
		inMemoryBuilds = append(inMemoryBuilds, fakeBuild)
		s.buildChan <- fakeBuild
	}

	err := s.tracker.Run(context.TODO())
	s.NoError(err)

	s.ElementsMatch([]int{
		inMemoryBuilds[0].ResourceID(),
		inMemoryBuilds[1].ResourceID(),
		inMemoryBuilds[2].ResourceID(),
	}, []int{
		(<-running).ResourceID(),
		(<-running).ResourceID(),
		(<-running).ResourceID(),
	})
}

func (s *TrackerSuite) TestTrackerDoesntCrashWhenOneBuildPanic() {
	startedBuilds := []db.Build{}
	fakeBuild1 := new(dbfakes.FakeBuild)
	fakeBuild1.IDReturns(1)
	startedBuilds = append(startedBuilds, fakeBuild1)

	// build 2 and 3 are normal running build
	for i := 1; i < 3; i++ {
		fakeBuild := new(dbfakes.FakeBuild)
		fakeBuild.IDReturns(i + 1)
		startedBuilds = append(startedBuilds, fakeBuild)
	}

	s.fakeBuildFactory.GetAllStartedBuildsReturns(startedBuilds, nil)

	running := make(chan db.Build, 3)
	s.fakeEngine.NewBuildStub = func(build db.Build) builds.Runnable {
		fakeEngineBuild := new(buildsfakes.FakeRunnable)
		fakeEngineBuild.RunStub = func(context.Context) {
			if build.ID() == 1 {
				panic("something went wrong")
			} else {
				running <- build
			}
		}

		return fakeEngineBuild
	}

	err := s.tracker.Run(context.TODO())
	s.NoError(err)

	s.ElementsMatch([]int{
		startedBuilds[1].ID(),
		startedBuilds[2].ID(),
	}, []int{
		(<-running).ID(),
		(<-running).ID(),
	})

	s.Eventually(func() bool {
		return fakeBuild1.FinishCallCount() == 1
	}, time.Second, 10*time.Millisecond)

	s.Eventually(func() bool {
		return fakeBuild1.FinishArgsForCall(0) == db.BuildStatusErrored
	}, time.Second, 10*time.Millisecond)
}

func (s *TrackerSuite) TestTrackDoesntTrackAlreadyRunningBuilds() {
	fakeBuild := new(dbfakes.FakeBuild)
	fakeBuild.IDReturns(1)
	s.fakeBuildFactory.GetAllStartedBuildsReturns([]db.Build{fakeBuild}, nil)

	wait := make(chan struct{})
	defer close(wait)

	running := make(chan db.Build, 3)
	s.fakeEngine.NewBuildStub = func(build db.Build) builds.Runnable {
		engineBuild := new(buildsfakes.FakeRunnable)
		engineBuild.RunStub = func(context.Context) {
			running <- build
			<-wait
		}

		return engineBuild
	}

	err := s.tracker.Run(context.TODO())
	s.NoError(err)

	<-running

	err = s.tracker.Run(context.TODO())
	s.NoError(err)

	select {
	case <-running:
		s.Fail("another build was started!")
	case <-time.After(100 * time.Millisecond):
	}
}

func (s *TrackerSuite) TestTrackDoesntTrackAlreadyRunningInMemoryChecks() {
	fakeInMemoryCheck := new(dbfakes.FakeBuild)
	fakeInMemoryCheck.IDReturns(0)
	fakeInMemoryCheck.ResourceIDReturns(1)
	s.fakeBuildFactory.GetAllStartedBuildsReturns([]db.Build{}, nil)

	wait := make(chan struct{})
	defer close(wait)

	running := make(chan db.Build, 3)
	s.fakeEngine.NewBuildStub = func(build db.Build) builds.Runnable {
		engineBuild := new(buildsfakes.FakeRunnable)
		engineBuild.RunStub = func(context.Context) {
			running <- build
			<-wait
		}

		return engineBuild
	}

	s.buildChan <- fakeInMemoryCheck
	<-running
	s.buildChan <- fakeInMemoryCheck

	select {
	case <-running:
		s.Fail("another in-memory check was started!")
	case <-time.After(100 * time.Millisecond):
	}
}

func (s *TrackerSuite) TestTrackerDrainsEngine() {
	var _ component.Drainable = s.tracker

	ctx := context.TODO()
	s.tracker.Drain(ctx)
	s.Equal(1, s.fakeEngine.DrainCallCount())
	s.Equal(ctx, s.fakeEngine.DrainArgsForCall(0))
}

func (s *TrackerSuite) TestTrackFinalizesOrphanedBuild() {
	// Simulate a build where Run() exits without calling Finish().
	// This happens when engineBuild.Run() hits an early-return path
	// (e.g. AcquireTrackingLock error, engine drain). The build still
	// reports IsRunning()==true because Finish() was never called.
	fakeBuild := new(dbfakes.FakeBuild)
	fakeBuild.IDReturns(1)
	fakeBuild.IsRunningReturns(true)

	s.fakeBuildFactory.GetAllStartedBuildsReturns([]db.Build{fakeBuild}, nil)

	done := make(chan struct{})
	s.fakeEngine.NewBuildStub = func(build db.Build) builds.Runnable {
		engineBuild := new(buildsfakes.FakeRunnable)
		engineBuild.RunStub = func(context.Context) {
			// Return without calling Finish — simulates early exit
			close(done)
		}
		return engineBuild
	}

	err := s.tracker.Run(context.TODO())
	s.NoError(err)

	<-done

	s.Eventually(func() bool {
		return fakeBuild.FinishCallCount() == 1
	}, time.Second, 10*time.Millisecond, "tracker should finalize orphaned build")

	s.Equal(db.BuildStatusErrored, fakeBuild.FinishArgsForCall(0))
}

func (s *TrackerSuite) TestTrackDoesNotDoubleFinishCompletedBuild() {
	// When a build completes normally (IsRunning returns false after
	// Run), the tracker should NOT call Finish a second time.
	fakeBuild := new(dbfakes.FakeBuild)
	fakeBuild.IDReturns(1)
	fakeBuild.IsRunningReturns(false) // build completed normally

	s.fakeBuildFactory.GetAllStartedBuildsReturns([]db.Build{fakeBuild}, nil)

	done := make(chan struct{})
	s.fakeEngine.NewBuildStub = func(build db.Build) builds.Runnable {
		engineBuild := new(buildsfakes.FakeRunnable)
		engineBuild.RunStub = func(context.Context) {
			close(done)
		}
		return engineBuild
	}

	err := s.tracker.Run(context.TODO())
	s.NoError(err)

	<-done

	// Give the defer time to execute
	time.Sleep(100 * time.Millisecond)

	s.Equal(0, fakeBuild.FinishCallCount(), "tracker should not call Finish on a completed build")
}

func (s *TrackerSuite) TestTrackOrphanedInMemoryCheckCleansUpInFlightTracking() {
	// End-to-end test: an in-memory check build wrapped with a cleanup
	// function (like onFinishBuild) should have its cleanup called even
	// when Run() exits without calling Finish().
	cleanedUp := make(chan struct{})
	fakeBuild := new(dbfakes.FakeBuild)
	fakeBuild.IDReturns(0)
	fakeBuild.ResourceIDReturns(42)
	fakeBuild.IsRunningReturns(true)
	// When Finish is called, signal cleanup
	fakeBuild.FinishStub = func(status db.BuildStatus) error {
		close(cleanedUp)
		return nil
	}

	done := make(chan struct{})
	s.fakeEngine.NewBuildStub = func(build db.Build) builds.Runnable {
		engineBuild := new(buildsfakes.FakeRunnable)
		engineBuild.RunStub = func(context.Context) {
			close(done)
		}
		return engineBuild
	}

	s.buildChan <- fakeBuild

	select {
	case <-cleanedUp:
		// Success — cleanup was triggered
	case <-time.After(2 * time.Second):
		s.Fail("cleanup was never triggered for orphaned in-memory check build")
	}
}

// BT-05: BuildsRunning metric incremented during build tracking
func (s *TrackerSuite) TestTrackEmitsBuildsRunningMetric() {
	// Drain stale gauge state
	metric.Metrics.BuildsRunning.Max()

	fakeBuild := new(dbfakes.FakeBuild)
	fakeBuild.IDReturns(1)
	fakeBuild.NameReturns("42") // non-check build

	s.fakeBuildFactory.GetAllStartedBuildsReturns([]db.Build{fakeBuild}, nil)

	var gaugeSeenDuringRun float64
	s.fakeEngine.NewBuildStub = func(build db.Build) builds.Runnable {
		engineBuild := new(buildsfakes.FakeRunnable)
		engineBuild.RunStub = func(context.Context) {
			gaugeSeenDuringRun = metric.Metrics.BuildsRunning.Max()
		}
		return engineBuild
	}

	err := s.tracker.Run(context.TODO())
	s.NoError(err)

	// Wait for the goroutine to finish
	time.Sleep(100 * time.Millisecond)

	s.GreaterOrEqual(gaugeSeenDuringRun, float64(1), "BuildsRunning should be >= 1 during build execution")
}

// BT-05: CheckBuildsRunning metric for check builds
func (s *TrackerSuite) TestTrackEmitsCheckBuildsRunningMetric() {
	// Drain stale gauge state
	metric.Metrics.CheckBuildsRunning.Max()

	fakeBuild := new(dbfakes.FakeBuild)
	fakeBuild.IDReturns(1)
	fakeBuild.NameReturns(db.CheckBuildName) // check build

	s.fakeBuildFactory.GetAllStartedBuildsReturns([]db.Build{fakeBuild}, nil)

	var gaugeSeenDuringRun float64
	s.fakeEngine.NewBuildStub = func(build db.Build) builds.Runnable {
		engineBuild := new(buildsfakes.FakeRunnable)
		engineBuild.RunStub = func(context.Context) {
			gaugeSeenDuringRun = metric.Metrics.CheckBuildsRunning.Max()
		}
		return engineBuild
	}

	err := s.tracker.Run(context.TODO())
	s.NoError(err)

	// Wait for the goroutine to finish
	time.Sleep(100 * time.Millisecond)

	s.GreaterOrEqual(gaugeSeenDuringRun, float64(1), "CheckBuildsRunning should be >= 1 during check build execution")
}
