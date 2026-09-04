package scheduler_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/postgresrunner"
	"github.com/concourse/concourse/atc/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func init() {
	util.PanicSink = GinkgoWriter
}

func TestScheduler(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Scheduler Suite")
}

var schedulerPostgresRunner postgresrunner.Runner

var _ = postgresrunner.GinkgoRunner(&schedulerPostgresRunner)

type schedulerDB struct {
	Conn            db.DbConn
	LockFactory     lock.LockFactory
	Builder         dbtest.Builder
	TeamFactory     db.TeamFactory
	PipelineFactory db.PipelineFactory
	JobFactory      db.JobFactory
	BuildFactory    db.BuildFactory
}

type schedulerJobCompletion struct {
	done chan struct{}
	once sync.Once
}

func newSchedulerJobCompletion() *schedulerJobCompletion {
	return &schedulerJobCompletion{done: make(chan struct{})}
}

func (completion *schedulerJobCompletion) finish() {
	completion.once.Do(func() { close(completion.done) })
}

type completionSchedulerJob struct {
	db.Job
	completion *schedulerJobCompletion
}

func (job *completionSchedulerJob) AcquireSchedulingLock(logger lager.Logger) (lock.Lock, bool, error) {
	schedulingLock, acquired, err := job.Job.AcquireSchedulingLock(logger)
	if err != nil || !acquired {
		job.completion.finish()
		return schedulingLock, acquired, err
	}

	return &completionSchedulerLock{
		Lock:       schedulingLock,
		completion: job.completion,
	}, true, nil
}

type completionSchedulerLock struct {
	lock.Lock
	completion *schedulerJobCompletion
}

func (schedulingLock *completionSchedulerLock) Release() error {
	err := schedulingLock.Lock.Release()
	schedulingLock.completion.finish()
	return err
}

type observedSchedulerJobFactory struct {
	db.JobFactory

	mu          sync.Mutex
	completions map[int]*schedulerJobCompletion
}

func observeSchedulerJobFactory(jobFactory db.JobFactory) *observedSchedulerJobFactory {
	return &observedSchedulerJobFactory{
		JobFactory:  jobFactory,
		completions: map[int]*schedulerJobCompletion{},
	}
}

func (factory *observedSchedulerJobFactory) JobsToSchedule() (db.SchedulerJobs, error) {
	jobs, err := factory.JobFactory.JobsToSchedule()
	if err != nil {
		return nil, err
	}
	return factory.observe(jobs), nil
}

func (factory *observedSchedulerJobFactory) JobsToScheduleByIDs(jobIDs []int) (db.SchedulerJobs, error) {
	jobs, err := factory.JobFactory.JobsToScheduleByIDs(jobIDs)
	if err != nil {
		return nil, err
	}
	return factory.observe(jobs), nil
}

func (factory *observedSchedulerJobFactory) observe(jobs db.SchedulerJobs) db.SchedulerJobs {
	factory.mu.Lock()
	defer factory.mu.Unlock()

	for i := range jobs {
		completion := newSchedulerJobCompletion()
		factory.completions[jobs[i].ID()] = completion
		jobs[i].Job = &completionSchedulerJob{
			Job:        jobs[i].Job,
			completion: completion,
		}
	}
	return jobs
}

func (factory *observedSchedulerJobFactory) completion(jobID int) *schedulerJobCompletion {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return factory.completions[jobID]
}

type algorithmInputsFailsJob struct {
	db.Job
	err error
}

func (job algorithmInputsFailsJob) AlgorithmInputs() (db.InputConfigs, error) {
	return nil, job.err
}

type requestScheduleFailsJob struct {
	db.Job
	err error
}

func (job requestScheduleFailsJob) RequestSchedule() error {
	return job.err
}

type saveNextInputMappingFailsJob struct {
	db.Job
	err error
}

func (job saveNextInputMappingFailsJob) SaveNextInputMapping(db.InputMapping, bool) error {
	return job.err
}

type nextBuildInputsFailsJob struct {
	db.Job
	err error
}

func (job nextBuildInputsFailsJob) GetFullNextBuildInputs() ([]db.BuildInput, bool, error) {
	return nil, false, job.err
}

type ensurePendingBuildFailsJob struct {
	db.Job
	err error
}

func (job ensurePendingBuildFailsJob) EnsurePendingBuildExists(context.Context) error {
	return job.err
}

type setHasNewInputsFailsJob struct {
	db.Job
	err error
}

func (job setHasNewInputsFailsJob) SetHasNewInputs(bool) error {
	return job.err
}

type pendingBuildsCountingJob struct {
	db.Job
	calls *int
}

func (job pendingBuildsCountingJob) GetPendingBuilds() ([]db.Build, error) {
	*job.calls++
	return job.Job.GetPendingBuilds()
}

type pendingBuildsFailsJob struct {
	db.Job
	err error
}

func (job pendingBuildsFailsJob) GetPendingBuilds() ([]db.Build, error) {
	return nil, job.err
}

type scheduleBuildFailsJob struct {
	db.Job
	err error
}

func (job scheduleBuildFailsJob) ScheduleBuild(db.Build) (bool, error) {
	return false, job.err
}

type wrappedPendingBuildsJob struct {
	db.Job
	wrap func(int, db.Build) db.Build
}

func (job wrappedPendingBuildsJob) GetPendingBuilds() ([]db.Build, error) {
	pending, err := job.Job.GetPendingBuilds()
	if err != nil {
		return nil, err
	}

	wrapped := make([]db.Build, len(pending))
	for i, build := range pending {
		wrapped[i] = job.wrap(i, build)
	}
	return wrapped, nil
}

type resourcesCheckedFailsBuild struct {
	db.Build
	err error
}

func (build resourcesCheckedFailsBuild) ResourcesChecked() (bool, error) {
	return false, build.err
}

type adoptInputsFailsBuild struct {
	db.Build
	err error
}

func (build adoptInputsFailsBuild) AdoptInputsAndPipes() ([]db.BuildInput, bool, error) {
	return nil, false, build.err
}

type adoptRerunInputsFailsBuild struct {
	db.Build
	err error
}

func (build adoptRerunInputsFailsBuild) AdoptRerunInputsAndPipes() ([]db.BuildInput, bool, error) {
	return nil, false, build.err
}

type startFailsBuild struct {
	db.Build
	err error
}

func (build startFailsBuild) Start(atc.Plan) (bool, error) {
	return false, build.err
}

type finishFailsBuild struct {
	db.Build
	err error
}

func (build finishFailsBuild) Finish(db.BuildStatus) error {
	return build.err
}

type checkNamedBuild struct {
	db.Build
}

func (build checkNamedBuild) Name() string {
	return db.CheckBuildName
}

func deferSchedulerCompletions(unblock func(), completions func() []*schedulerJobCompletion) {
	GinkgoHelper()

	DeferCleanup(func(ctx SpecContext) {
		if unblock != nil {
			unblock()
		}
		for _, completion := range completions() {
			if completion != nil {
				Eventually(ctx, completion.done).Should(BeClosed())
			}
		}
	})
}

func waitForSchedulerCompletion(ctx context.Context, completion *schedulerJobCompletion) {
	GinkgoHelper()
	Eventually(ctx, completion.done).Should(BeClosed())
}

func useSchedulerDB() *schedulerDB {
	GinkgoHelper()

	schedulerPostgresRunner.CreateTestDBFromTemplate()
	// DeferCleanup executes in LIFO order. Register the drop first so every
	// ordinary and advisory-lock connection closes before the clone is dropped.
	DeferCleanup(schedulerPostgresRunner.DropTestDB)

	conn := schedulerPostgresRunner.OpenConn()
	DeferCleanup(func() { Expect(conn.Close()).To(Succeed()) })
	db.CleanupBaseResourceTypesCache()

	lockFactory, closeLocks := openSchedulerLockFactory()
	DeferCleanup(func() { Expect(closeLocks()).To(Succeed()) })

	return &schedulerDB{
		Conn:            conn,
		LockFactory:     lockFactory,
		Builder:         dbtest.NewBuilder(conn, lockFactory),
		TeamFactory:     db.NewTeamFactory(conn, lockFactory),
		PipelineFactory: db.NewPipelineFactory(conn, lockFactory),
		JobFactory:      db.NewJobFactory(conn, lockFactory),
		BuildFactory:    db.NewBuildFactory(conn, lockFactory, 0, time.Hour),
	}
}

func openSchedulerLockFactory() (lock.LockFactory, func() error) {
	GinkgoHelper()

	var lockConns [lock.FactoryCount]*sql.DB
	for i := range lockConns {
		lockConns[i] = schedulerPostgresRunner.OpenSingleton()
	}

	closeLocks := func() error {
		var closeErrs []error
		for _, conn := range lockConns {
			if err := conn.Close(); err != nil {
				closeErrs = append(closeErrs, err)
			}
		}
		return errors.Join(closeErrs...)
	}

	return lock.NewLockFactory(
		lockConns,
		func(lager.Logger, lock.LockID) {},
		func(lager.Logger, lock.LockID) {},
	), closeLocks
}

func (fixture *schedulerDB) useIndependentLockFactory() lock.LockFactory {
	GinkgoHelper()

	lockFactory, closeLocks := openSchedulerLockFactory()
	DeferCleanup(func() { Expect(closeLocks()).To(Succeed()) })
	return lockFactory
}

func persistSchedulerPipeline(
	fixture *schedulerDB,
	teamName string,
	pipelineName string,
	config atc.Config,
) (db.Team, db.Pipeline) {
	GinkgoHelper()

	team, err := fixture.TeamFactory.CreateTeam(atc.Team{Name: teamName})
	Expect(err).NotTo(HaveOccurred())
	pipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: pipelineName},
		config,
		0,
		false,
	)
	Expect(err).NotTo(HaveOccurred())
	return team, pipeline
}

func schedulerPipelineJob(pipeline db.Pipeline, name string) db.Job {
	GinkgoHelper()

	job, found, err := pipeline.Job(name)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return job
}

func requestSchedulerJob(fixture *schedulerDB, job db.Job) time.Time {
	GinkgoHelper()

	Expect(job.RequestSchedule()).To(Succeed())
	requested, _ := schedulerJobTimestamps(fixture, job.ID())
	return requested
}

func reloadSchedulerJob(
	conn db.DbConn,
	lockFactory lock.LockFactory,
	teamName string,
	pipelineName string,
	jobName string,
) db.Job {
	GinkgoHelper()

	team, found, err := db.NewTeamFactory(conn, lockFactory).FindTeam(teamName)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	pipeline, found, err := team.Pipeline(atc.PipelineRef{Name: pipelineName})
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return schedulerPipelineJob(pipeline, jobName)
}

func schedulerJobTimestamps(fixture *schedulerDB, jobID int) (time.Time, time.Time) {
	GinkgoHelper()

	var requested time.Time
	var lastScheduled time.Time
	err := fixture.Conn.QueryRow(`
		SELECT schedule_requested, last_scheduled
		FROM jobs
		WHERE id = $1
	`, jobID).Scan(&requested, &lastScheduled)
	Expect(err).NotTo(HaveOccurred())
	return requested, lastScheduled
}

var _ = Describe("scheduler PostgreSQL fixture", func() {
	It("persists scheduling timestamps in an opt-in clone", func() {
		fixture := useSchedulerDB()
		_, pipeline := persistSchedulerPipeline(
			fixture,
			"fixture-team",
			"fixture-pipeline",
			atc.Config{Jobs: atc.JobConfigs{{Name: "fixture-job"}}},
		)
		job := schedulerPipelineJob(pipeline, "fixture-job")
		reloadedJob := reloadSchedulerJob(
			fixture.Conn,
			fixture.LockFactory,
			"fixture-team",
			"fixture-pipeline",
			"fixture-job",
		)
		Expect(reloadedJob.ID()).To(Equal(job.ID()))

		requested := requestSchedulerJob(fixture, job)
		persistedRequested, lastScheduled := schedulerJobTimestamps(fixture, job.ID())

		Expect(persistedRequested).To(Equal(requested))
		Expect(lastScheduled).To(Equal(time.Date(1970, 1, 1, 0, 0, 0, 0, lastScheduled.Location())))
	})
})
