package builds_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/builds"
	"github.com/concourse/concourse/atc/builds/buildsfakes"
	"github.com/concourse/concourse/atc/component"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/postgresrunner"
	"github.com/concourse/concourse/atc/util"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

func init() {
	util.PanicSink = io.Discard
}

var trackerPostgres postgresrunner.StandardTestRunner

func TestMain(m *testing.M) {
	os.Exit(trackerPostgres.Main(m))
}

type trackerDB struct {
	Conn   db.DbConn
	Teams  db.TeamFactory
	Builds db.BuildFactory
}

func useRealTrackerDB(t *testing.T) trackerDB {
	t.Helper()

	conn := trackerPostgres.OpenConn(t)
	db.CleanupBaseResourceTypesCache()
	locks := lock.NewTestLockFactory(&trackerLockDB{held: map[string]bool{}})
	return trackerDB{
		Conn:   conn,
		Teams:  db.NewTeamFactory(conn, locks),
		Builds: db.NewBuildFactory(conn, locks, 0, time.Hour),
	}
}

// trackerLockDB gives the production test lock factory advisory-lock semantics
// without opening connections outside StandardTestRunner's clone.
type trackerLockDB struct {
	mu   sync.Mutex
	held map[string]bool
}

func (database *trackerLockDB) Acquire(id lock.LockID) (bool, error) {
	database.mu.Lock()
	defer database.mu.Unlock()

	key := fmt.Sprint([]int(id))
	if database.held[key] {
		return false, nil
	}
	database.held[key] = true
	return true, nil
}

func (database *trackerLockDB) Release(id lock.LockID) (bool, error) {
	database.mu.Lock()
	defer database.mu.Unlock()

	key := fmt.Sprint([]int(id))
	if !database.held[key] {
		return false, nil
	}
	delete(database.held, key)
	return true, nil
}

func createTrackerStartedBuild(t *testing.T, team db.Team, planID atc.PlanID) db.Build {
	t.Helper()

	build, err := team.CreateStartedBuild(atc.Plan{ID: planID})
	require.NoError(t, err)
	return build
}

func createTrackerCheckBuild(t *testing.T, team db.Team) db.Build {
	t.Helper()

	pipeline, created, err := team.SavePipeline(
		atc.PipelineRef{Name: "tracker-checks"},
		atc.Config{Resources: atc.ResourceConfigs{{
			Name: "some-resource", Type: "some-base-type", Source: atc.Source{"key": "value"},
		}}},
		db.ConfigVersion(0), false,
	)
	require.NoError(t, err)
	require.True(t, created)
	resource, found, err := pipeline.Resource("some-resource")
	require.NoError(t, err)
	require.True(t, found)
	check, created, err := resource.CreateBuild(context.Background(), true, atc.Plan{
		ID: "tracker-check",
		Check: &atc.CheckPlan{
			Name: resource.Name(), Type: resource.Type(), Source: resource.Source(), Resource: resource.Name(),
		},
	})
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, db.CheckBuildName, check.Name())
	return check
}

func receiveTrackedBuild(t *testing.T, running <-chan db.Build) db.Build {
	t.Helper()

	select {
	case build := <-running:
		return build
	case <-time.After(time.Second):
		require.FailNow(t, "timed out waiting for tracked build")
		return nil
	}
}

func requireTrackerBuildStatusStable(
	t *testing.T,
	buildFactory db.BuildFactory,
	buildID int,
	expected db.BuildStatus,
) {
	t.Helper()

	require.Never(t, func() bool {
		reloaded, found, err := buildFactory.Build(buildID)
		return err != nil || !found || reloaded.Status() != expected
	}, 250*time.Millisecond, 10*time.Millisecond,
		"build %d status changed after tracker finalization began", buildID,
	)
}

type TrackerSuite struct {
	suite.Suite
	*require.Assertions

	fakeEngine *buildsfakes.FakeEngine

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
	s.fakeEngine = new(buildsfakes.FakeEngine)
	s.buildChan = make(chan db.Build, 10)
}

func (s *TrackerSuite) constructTracker(buildFactory db.BuildFactory) {
	s.tracker = builds.NewTracker(
		s.logger,
		buildFactory,
		s.fakeEngine,
		s.buildChan,
	)
}

func (s *TrackerSuite) TestTrackRunsStartedBuilds() {
	fixture := useRealTrackerDB(s.T())
	team, err := fixture.Teams.CreateTeam(atc.Team{Name: "tracker-team"})
	s.Require().NoError(err)
	startedBuilds := []db.Build{
		createTrackerStartedBuild(s.T(), team, "tracker-started-1"),
		createTrackerStartedBuild(s.T(), team, "tracker-started-2"),
		createTrackerStartedBuild(s.T(), team, "tracker-started-3"),
	}
	s.constructTracker(fixture.Builds)

	running := make(chan db.Build, 3)
	s.fakeEngine.NewBuildStub = func(build db.Build) builds.Runnable {
		engineBuild := new(buildsfakes.FakeRunnable)
		engineBuild.RunStub = func(context.Context) {
			running <- build
		}

		return engineBuild
	}

	err = s.tracker.Run(context.TODO())
	s.NoError(err)

	gotIDs := make([]int, 0, len(startedBuilds))
	for range startedBuilds {
		gotIDs = append(gotIDs, receiveTrackedBuild(s.T(), running).ID())
	}
	s.ElementsMatch([]int{startedBuilds[0].ID(), startedBuilds[1].ID(), startedBuilds[2].ID()}, gotIDs)
}

func (s *TrackerSuite) TestTrackInMemoryBuilds() {
	s.constructTracker(nil)
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
		// Retained: in-memory checks have no persisted build row and are identified by ResourceID.
		fakeBuild := new(dbfakes.FakeBuild)
		// When tracked, in-memory builds have no id yet, but they do have a
		// resource ID
		fakeBuild.IDReturns(0)
		fakeBuild.ResourceIDReturns(i + 1)
		inMemoryBuilds = append(inMemoryBuilds, fakeBuild)
		s.buildChan <- fakeBuild
	}

	s.ElementsMatch([]int{
		inMemoryBuilds[0].ResourceID(),
		inMemoryBuilds[1].ResourceID(),
		inMemoryBuilds[2].ResourceID(),
	}, []int{
		receiveTrackedBuild(s.T(), running).ResourceID(),
		receiveTrackedBuild(s.T(), running).ResourceID(),
		receiveTrackedBuild(s.T(), running).ResourceID(),
	})
}

func (s *TrackerSuite) TestTrackerDoesntCrashWhenOneBuildPanic() {
	fixture := useRealTrackerDB(s.T())
	team, err := fixture.Teams.CreateTeam(atc.Team{Name: "tracker-team"})
	s.Require().NoError(err)
	startedBuilds := []db.Build{
		createTrackerStartedBuild(s.T(), team, "tracker-panic"),
		createTrackerStartedBuild(s.T(), team, "tracker-normal-1"),
		createTrackerStartedBuild(s.T(), team, "tracker-normal-2"),
	}
	s.constructTracker(fixture.Builds)

	running := make(chan db.Build, 3)
	s.fakeEngine.NewBuildStub = func(build db.Build) builds.Runnable {
		fakeEngineBuild := new(buildsfakes.FakeRunnable)
		fakeEngineBuild.RunStub = func(context.Context) {
			if build.ID() == startedBuilds[0].ID() {
				panic("something went wrong")
			} else {
				running <- build
			}
		}

		return fakeEngineBuild
	}

	err = s.tracker.Run(context.TODO())
	s.NoError(err)

	s.ElementsMatch(
		[]int{startedBuilds[1].ID(), startedBuilds[2].ID()},
		[]int{receiveTrackedBuild(s.T(), running).ID(), receiveTrackedBuild(s.T(), running).ID()},
	)

	s.Eventually(func() bool {
		panicked, panickedFound, panickedErr := fixture.Builds.Build(startedBuilds[0].ID())
		normal1, normal1Found, normal1Err := fixture.Builds.Build(startedBuilds[1].ID())
		normal2, normal2Found, normal2Err := fixture.Builds.Build(startedBuilds[2].ID())
		return panickedErr == nil && panickedFound && panicked.Status() == db.BuildStatusErrored &&
			normal1Err == nil && normal1Found && normal1.Status() == db.BuildStatusStarted &&
			normal2Err == nil && normal2Found && normal2.Status() == db.BuildStatusStarted
	}, time.Second, 10*time.Millisecond)
	requireTrackerBuildStatusStable(s.T(), fixture.Builds, startedBuilds[1].ID(), db.BuildStatusStarted)
	requireTrackerBuildStatusStable(s.T(), fixture.Builds, startedBuilds[2].ID(), db.BuildStatusStarted)
}

func (s *TrackerSuite) TestTrackDoesntTrackAlreadyRunningBuilds() {
	fixture := useRealTrackerDB(s.T())
	team, err := fixture.Teams.CreateTeam(atc.Team{Name: "tracker-team"})
	s.Require().NoError(err)
	started := createTrackerStartedBuild(s.T(), team, "tracker-duplicate")
	s.constructTracker(fixture.Builds)

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

	err = s.tracker.Run(context.TODO())
	s.NoError(err)

	s.Equal(started.ID(), receiveTrackedBuild(s.T(), running).ID())

	err = s.tracker.Run(context.TODO())
	s.NoError(err)

	select {
	case <-running:
		s.Fail("another build was started!")
	case <-time.After(100 * time.Millisecond):
	}
}

func (s *TrackerSuite) TestTrackDoesntTrackAlreadyRunningInMemoryChecks() {
	s.constructTracker(nil)
	// Retained: in-memory checks have no persisted build row and are identified by ResourceID.
	fakeInMemoryCheck := new(dbfakes.FakeBuild)
	fakeInMemoryCheck.IDReturns(0)
	fakeInMemoryCheck.ResourceIDReturns(1)

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
	receiveTrackedBuild(s.T(), running)
	s.buildChan <- fakeInMemoryCheck

	select {
	case <-running:
		s.Fail("another in-memory check was started!")
	case <-time.After(100 * time.Millisecond):
	}
}

func (s *TrackerSuite) TestTrackerDrainsEngine() {
	s.constructTracker(nil)
	var _ component.Drainable = s.tracker

	ctx := context.TODO()
	s.tracker.Drain(ctx)
	s.Equal(1, s.fakeEngine.DrainCallCount())
	s.Equal(ctx, s.fakeEngine.DrainArgsForCall(0))
}

func (s *TrackerSuite) TestTrackDoesNotFinalizeReleasedJobBuild() {
	// A job build whose Run() exits while the build is still running is a
	// legitimate resume path (engine drain, tracking lock held by another
	// web, retryable step error). The tracker must NOT error it — the build
	// stays "started" so a later tracker cycle (possibly on a new web)
	// re-attaches to it.
	fixture := useRealTrackerDB(s.T())
	team, err := fixture.Teams.CreateTeam(atc.Team{Name: "tracker-team"})
	s.Require().NoError(err)
	pipeline, created, err := team.SavePipeline(
		atc.PipelineRef{Name: "tracker-job-pipeline"},
		atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
		db.ConfigVersion(0), false,
	)
	s.Require().NoError(err)
	s.True(created)
	job, found, err := pipeline.Job("some-job")
	s.Require().NoError(err)
	s.True(found)
	startedBuild, err := job.CreateBuild("tracker")
	s.Require().NoError(err)
	started, err := startedBuild.Start(atc.Plan{ID: "tracker-job"})
	s.Require().NoError(err)
	s.True(started)
	s.constructTracker(fixture.Builds)

	running := make(chan db.Build, 2)
	s.fakeEngine.NewBuildStub = func(build db.Build) builds.Runnable {
		engineBuild := new(buildsfakes.FakeRunnable)
		engineBuild.RunStub = func(context.Context) {
			// Return without calling Finish — simulates early exit
			running <- build
		}
		return engineBuild
	}

	err = s.tracker.Run(context.TODO())
	s.NoError(err)
	s.Equal(startedBuild.ID(), receiveTrackedBuild(s.T(), running).ID())

	// A second discovery can start only after the first tracker goroutine has
	// returned and removed this ID from its in-process running set.
	s.Eventually(func() bool {
		if err := s.tracker.Run(context.TODO()); err != nil {
			return false
		}
		select {
		case got := <-running:
			return got.ID() == startedBuild.ID()
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)

	requireTrackerBuildStatusStable(s.T(), fixture.Builds, startedBuild.ID(), db.BuildStatusStarted)
}

func (s *TrackerSuite) TestTrackFinalizesOrphanedCheckBuild() {
	// A check build whose Run() exits without calling Finish() must be
	// finalized so the checkFactory's in-flight tracking (cleared via
	// onFinishBuild.Finish) is not permanently leaked.
	fixture := useRealTrackerDB(s.T())
	team, err := fixture.Teams.CreateTeam(atc.Team{Name: "tracker-team"})
	s.Require().NoError(err)
	check := createTrackerCheckBuild(s.T(), team)
	s.constructTracker(fixture.Builds)

	done := make(chan struct{})
	s.fakeEngine.NewBuildStub = func(build db.Build) builds.Runnable {
		engineBuild := new(buildsfakes.FakeRunnable)
		engineBuild.RunStub = func(context.Context) {
			// Return without calling Finish — simulates early exit
			close(done)
		}
		return engineBuild
	}

	err = s.tracker.Run(context.TODO())
	s.NoError(err)

	select {
	case <-done:
	case <-time.After(time.Second):
		s.FailNow("check build was not tracked")
	}

	s.Eventually(func() bool {
		reloaded, found, err := fixture.Builds.Build(check.ID())
		return err == nil && found && reloaded.Status() == db.BuildStatusErrored
	}, time.Second, 10*time.Millisecond, "tracker should finalize orphaned check build")
}

func (s *TrackerSuite) TestTrackDoesNotDoubleFinishCompletedBuild() {
	// When an ordinary build completes normally, the tracker must not change
	// its persisted terminal status after Run returns.
	fixture := useRealTrackerDB(s.T())
	team, err := fixture.Teams.CreateTeam(atc.Team{Name: "tracker-team"})
	s.Require().NoError(err)
	started := createTrackerStartedBuild(s.T(), team, "tracker-completes")
	s.constructTracker(fixture.Builds)

	finished := make(chan error, 1)
	s.fakeEngine.NewBuildStub = func(build db.Build) builds.Runnable {
		engineBuild := new(buildsfakes.FakeRunnable)
		engineBuild.RunStub = func(context.Context) {
			finished <- build.Finish(db.BuildStatusSucceeded)
		}
		return engineBuild
	}

	err = s.tracker.Run(context.TODO())
	s.NoError(err)

	select {
	case err := <-finished:
		s.NoError(err)
	case <-time.After(time.Second):
		s.FailNow("build did not complete")
	}

	s.Eventually(func() bool {
		reloaded, found, err := fixture.Builds.Build(started.ID())
		return err == nil && found && reloaded.Status() == db.BuildStatusSucceeded
	}, time.Second, 10*time.Millisecond, "tracker should not change a completed build's terminal status")
	requireTrackerBuildStatusStable(s.T(), fixture.Builds, started.ID(), db.BuildStatusSucceeded)
}

func (s *TrackerSuite) TestTrackOrphanedInMemoryCheckCleansUpInFlightTracking() {
	s.constructTracker(nil)
	// End-to-end test: an in-memory check build wrapped with a cleanup
	// function (like onFinishBuild) should have its cleanup called even
	// when Run() exits without calling Finish().
	cleanedUp := make(chan struct{})
	// Retained: in-memory checks have no persisted build row and are identified by ResourceID.
	fakeBuild := new(dbfakes.FakeBuild)
	fakeBuild.IDReturns(0)
	fakeBuild.ResourceIDReturns(42)
	fakeBuild.NameReturns(db.CheckBuildName)
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

	fixture := useRealTrackerDB(s.T())
	team, err := fixture.Teams.CreateTeam(atc.Team{Name: "tracker-team"})
	s.Require().NoError(err)
	started := createTrackerStartedBuild(s.T(), team, "tracker-build-gauge")
	s.constructTracker(fixture.Builds)

	gaugeSeenDuringRun := make(chan float64, 1)
	s.fakeEngine.NewBuildStub = func(build db.Build) builds.Runnable {
		engineBuild := new(buildsfakes.FakeRunnable)
		engineBuild.RunStub = func(context.Context) {
			gaugeSeenDuringRun <- metric.Metrics.BuildsRunning.Max()
		}
		return engineBuild
	}

	err = s.tracker.Run(context.TODO())
	s.NoError(err)

	select {
	case gauge := <-gaugeSeenDuringRun:
		s.GreaterOrEqual(gauge, float64(1), "BuildsRunning should be >= 1 during build execution")
	case <-time.After(time.Second):
		s.FailNow("build gauge was not observed")
	}
	requireTrackerBuildStatusStable(s.T(), fixture.Builds, started.ID(), db.BuildStatusStarted)
}

// BT-05: CheckBuildsRunning metric for check builds
func (s *TrackerSuite) TestTrackEmitsCheckBuildsRunningMetric() {
	// Drain stale gauge state
	metric.Metrics.CheckBuildsRunning.Max()

	fixture := useRealTrackerDB(s.T())
	team, err := fixture.Teams.CreateTeam(atc.Team{Name: "tracker-team"})
	s.Require().NoError(err)
	check := createTrackerCheckBuild(s.T(), team)
	s.constructTracker(fixture.Builds)

	gaugeSeenDuringRun := make(chan float64, 1)
	s.fakeEngine.NewBuildStub = func(build db.Build) builds.Runnable {
		engineBuild := new(buildsfakes.FakeRunnable)
		engineBuild.RunStub = func(context.Context) {
			gaugeSeenDuringRun <- metric.Metrics.CheckBuildsRunning.Max()
		}
		return engineBuild
	}

	err = s.tracker.Run(context.TODO())
	s.NoError(err)

	select {
	case gauge := <-gaugeSeenDuringRun:
		s.GreaterOrEqual(gauge, float64(1), "CheckBuildsRunning should be >= 1 during check execution")
	case <-time.After(time.Second):
		s.FailNow("check gauge was not observed")
	}
	s.Eventually(func() bool {
		reloaded, found, err := fixture.Builds.Build(check.ID())
		return err == nil && found && reloaded.Status() == db.BuildStatusErrored
	}, time.Second, 10*time.Millisecond)
}

func (s *TrackerSuite) TestTrackerReturnsStartedBuildLookupError() {
	lookupErr := errors.New("lookup started builds")
	// Retained: ordinary rows cannot make GetAllStartedBuilds return an error.
	buildFactory := new(dbfakes.FakeBuildFactory)
	buildFactory.GetAllStartedBuildsReturns(nil, lookupErr)
	s.constructTracker(buildFactory)

	s.ErrorIs(s.tracker.Run(context.Background()), lookupErr)
}
