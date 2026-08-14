package scheduler_test

import (
	"context"
	"fmt"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/builds"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/metric"
	. "github.com/concourse/concourse/atc/scheduler"
	"github.com/concourse/concourse/atc/scheduler/algorithm"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type duplicateScanSchedulerJobFactory struct {
	db.JobFactory
	job db.SchedulerJob
}

func (factory *duplicateScanSchedulerJobFactory) JobsToSchedule() (db.SchedulerJobs, error) {
	return db.SchedulerJobs{factory.job, factory.job}, nil
}

type destroyPipelineAfterScanJobFactory struct {
	db.JobFactory
	pipeline   db.Pipeline
	destroyErr error
}

func (factory *destroyPipelineAfterScanJobFactory) JobsToSchedule() (db.SchedulerJobs, error) {
	jobs, err := factory.JobFactory.JobsToSchedule()
	if err != nil {
		return nil, err
	}
	factory.destroyErr = factory.pipeline.Destroy()
	if factory.destroyErr != nil {
		return nil, fmt.Errorf("destroy scanned job pipeline: %w", factory.destroyErr)
	}
	return jobs, nil
}

var _ = Describe("Runner", func() {
	newRealScheduler := func(fixture *schedulerDB) *Scheduler {
		GinkgoHelper()
		return NewScheduler(
			builds.NewPlanner(atc.NewPlanFactory(0)),
			algorithm.New(schedulerVersionsDB(fixture)),
		)
	}

	newRunner := func(fixture *schedulerDB, jobFactory db.JobFactory, maxInFlight uint64) *Runner {
		GinkgoHelper()
		return NewRunner(
			lagertest.NewTestLogger("test"),
			jobFactory,
			newRealScheduler(fixture).Schedule,
			maxInFlight,
		)
	}

	triggeringJobs := func(names ...string) atc.Config {
		GinkgoHelper()
		jobs := make(atc.JobConfigs, 0, len(names))
		for _, name := range names {
			jobs = append(jobs, atc.JobConfig{
				Name: name,
				PlanSequence: []atc.Step{
					{Config: &atc.GetStep{Name: "some-input", Resource: "some-resource", Trigger: true}},
				},
			})
		}
		return atc.Config{
			Resources: atc.ResourceConfigs{
				{Name: "some-resource", Type: dbtest.BaseResourceType, Source: atc.Source{"some": "source"}},
			},
			Jobs: jobs,
		}
	}

	deferObservedFactory := func(
		factory *observedSchedulerJobFactory,
		jobIDs ...int,
	) {
		GinkgoHelper()
		deferSchedulerCompletions(nil, func() []*schedulerJobCompletion {
			completions := make([]*schedulerJobCompletion, 0, len(jobIDs))
			for _, jobID := range jobIDs {
				completions = append(completions, factory.completion(jobID))
			}
			return completions
		})
	}

	waitObservedFactory := func(
		ctx SpecContext,
		factory *observedSchedulerJobFactory,
		jobIDs ...int,
	) {
		GinkgoHelper()
		for _, jobID := range jobIDs {
			waitForSchedulerCompletion(ctx, factory.completion(jobID))
		}
	}

	expectNoJobBuild := func(job db.Job) {
		GinkgoHelper()
		apiBuilds, _, err := job.BuildsWithTime(db.Page{Limit: 50})
		Expect(err).NotTo(HaveOccurred())
		Expect(apiBuilds).To(BeEmpty())
		Expect(schedulerPendingBuilds(job)).To(BeEmpty())
	}

	expectStartedJobBuild := func(fixture *schedulerDB, job db.Job) {
		GinkgoHelper()
		apiBuilds, _, err := job.BuildsWithTime(db.Page{Limit: 50})
		Expect(err).NotTo(HaveOccurred())
		Expect(apiBuilds).To(HaveLen(1))
		Expect(apiBuilds[0].Status()).To(Equal(db.BuildStatusStarted))
		Expect(apiBuilds[0].HasPlan()).To(BeTrue())

		persisted, found, err := fixture.BuildFactory.Build(apiBuilds[0].ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(persisted.PrivatePlan()).NotTo(Equal(atc.Plan{}))
		Expect(schedulerPendingBuilds(job)).To(BeEmpty())
	}

	It("starts every requested job from the full persisted scan and leaves other jobs untouched", func(ctx SpecContext) {
		fixture := useSchedulerDB()
		scenario := dbtest.Setup(
			fixture.Builder.WithTeam("runner-team"),
			fixture.Builder.WithPipeline(triggeringJobs(
				"some-job",
				"some-other-job",
				"another-job",
				"not-requested",
			)),
			fixture.Builder.WithResourceVersions("some-resource", atc.Version{"ref": "v1"}),
		)

		jobs := []db.Job{
			scenario.Job("some-job"),
			scenario.Job("some-other-job"),
			scenario.Job("another-job"),
		}
		jobRequested := make(map[int]time.Time, len(jobs))
		for _, job := range jobs {
			jobRequested[job.ID()] = requestSchedulerJob(fixture, job)
		}
		notRequested := scenario.Job("not-requested")
		notRequestedRequested, _ := schedulerJobTimestamps(fixture, notRequested.ID())
		Expect(notRequested.UpdateLastScheduled(notRequestedRequested)).To(Succeed())
		_, notRequestedLast := schedulerJobTimestamps(fixture, notRequested.ID())

		observed := observeSchedulerJobFactory(fixture.JobFactory)
		jobIDs := []int{jobs[0].ID(), jobs[1].ID(), jobs[2].ID()}
		deferObservedFactory(observed, jobIDs...)
		Expect(newRunner(fixture, observed, 1).Run(ctx)).To(Succeed())
		waitObservedFactory(ctx, observed, jobIDs...)

		for _, job := range jobs {
			expectStartedJobBuild(fixture, job)
			scheduleRequested, lastScheduled := schedulerJobTimestamps(fixture, job.ID())
			Expect(scheduleRequested).To(Equal(jobRequested[job.ID()]))
			Expect(lastScheduled).To(Equal(jobRequested[job.ID()]))
		}
		expectNoJobBuild(notRequested)
		persistedNotRequested, persistedNotRequestedLast := schedulerJobTimestamps(fixture, notRequested.ID())
		Expect(persistedNotRequested).To(Equal(notRequestedRequested))
		Expect(persistedNotRequestedLast).To(Equal(notRequestedLast))
	})

	It("skips the exact job whose production scheduling lock is held", func(ctx SpecContext) {
		fixture := useSchedulerDB()
		scenario := dbtest.Setup(
			fixture.Builder.WithTeam("lock-team"),
			fixture.Builder.WithPipeline(triggeringJobs("locked-job", "available-job")),
			fixture.Builder.WithResourceVersions("some-resource", atc.Version{"ref": "v1"}),
		)
		lockedJob := scenario.Job("locked-job")
		availableJob := scenario.Job("available-job")
		lockedRequested := requestSchedulerJob(fixture, lockedJob)
		_, lockedLast := schedulerJobTimestamps(fixture, lockedJob.ID())
		availableRequested := requestSchedulerJob(fixture, availableJob)

		holderFactory := fixture.useIndependentLockFactory()
		heldLock, acquired, err := holderFactory.Acquire(
			lagertest.NewTestLogger("holder"),
			lock.NewJobSchedulingLockID(lockedJob.ID()),
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(acquired).To(BeTrue())
		released := false
		DeferCleanup(func() {
			if !released {
				Expect(heldLock.Release()).To(Succeed())
			}
		})

		observed := observeSchedulerJobFactory(fixture.JobFactory)
		deferObservedFactory(observed, lockedJob.ID(), availableJob.ID())
		Expect(newRunner(fixture, observed, 2).Run(ctx)).To(Succeed())
		waitObservedFactory(ctx, observed, lockedJob.ID(), availableJob.ID())

		Expect(heldLock.Release()).To(Succeed())
		released = true
		expectNoJobBuild(lockedJob)
		persistedLockedRequested, persistedLockedLast := schedulerJobTimestamps(fixture, lockedJob.ID())
		Expect(persistedLockedRequested).To(Equal(lockedRequested))
		Expect(persistedLockedLast).To(Equal(lockedLast))

		expectStartedJobBuild(fixture, availableJob)
		_, persistedAvailableLast := schedulerJobTimestamps(fixture, availableJob.ID())
		Expect(persistedAvailableLast).To(Equal(availableRequested))
	})

	It("leaves last_scheduled unchanged when a real max-in-flight build needs a retry", func(ctx SpecContext) {
		fixture := useSchedulerDB()
		config := triggeringJobs("serial-job")
		config.Jobs[0].RawMaxInFlight = 1
		scenario := dbtest.Setup(
			fixture.Builder.WithTeam("retry-team"),
			fixture.Builder.WithPipeline(config),
			fixture.Builder.WithResourceVersions("some-resource", atc.Version{"ref": "v1"}),
		)
		job := scenario.Job("serial-job")

		realScheduler := newRealScheduler(fixture)
		needsRetry, err := realScheduler.Schedule(
			context.Background(),
			lagertest.NewTestLogger("initial-schedule"),
			schedulerJobToSchedule(fixture, job),
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(needsRetry).To(BeFalse())
		expectStartedJobBuild(fixture, job)

		Expect(job.EnsurePendingBuildExists(context.Background())).To(Succeed())
		blocked := schedulerPendingBuilds(job)
		Expect(blocked).To(HaveLen(1))
		requested := requestSchedulerJob(fixture, job)
		_, initialLast := schedulerJobTimestamps(fixture, job.ID())

		observed := observeSchedulerJobFactory(fixture.JobFactory)
		deferObservedFactory(observed, job.ID())
		Expect(newRunner(fixture, observed, 1).Run(ctx)).To(Succeed())
		waitObservedFactory(ctx, observed, job.ID())

		persistedRequested, persistedLast := schedulerJobTimestamps(fixture, job.ID())
		Expect(persistedRequested).To(Equal(requested))
		Expect(persistedLast).To(Equal(initialLast))
		remaining := schedulerPendingBuilds(job)
		Expect(remaining).To(HaveLen(1))
		Expect(remaining[0].ID()).To(Equal(blocked[0].ID()))
		persistedBlocked, found, err := fixture.BuildFactory.Build(blocked[0].ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(persistedBlocked.Status()).To(Equal(db.BuildStatusPending))
		Expect(persistedBlocked.HasPlan()).To(BeFalse())
	})

	It("deduplicates a deliberately duplicated real job input", func(ctx SpecContext) {
		fixture := useSchedulerDB()
		_, pipeline := persistSchedulerPipeline(
			fixture,
			"duplicate-team",
			"duplicate-pipeline",
			atc.Config{Jobs: atc.JobConfigs{{Name: "duplicate-job"}}},
		)
		job := schedulerPipelineJob(pipeline, "duplicate-job")
		requested := requestSchedulerJob(fixture, job)

		realJobs, err := fixture.JobFactory.JobsToSchedule()
		Expect(err).NotTo(HaveOccurred())
		Expect(realJobs).To(HaveLen(1))
		completion := newSchedulerJobCompletion()
		realJobs[0].Job = &completionSchedulerJob{Job: realJobs[0].Job, completion: completion}

		// A production JobsToSchedule query cannot return the same row twice, so
		// hand the runner the same real row twice to exercise its in-memory guard.
		duplicateFactory := &duplicateScanSchedulerJobFactory{
			JobFactory: fixture.JobFactory,
			job:        realJobs[0],
		}

		started := make(chan struct{}, 1)
		releaseSchedule := make(chan struct{})
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(releaseSchedule) }) }
		schedule := ScheduleFunc(func(context.Context, lager.Logger, db.SchedulerJob) (bool, error) {
			started <- struct{}{}
			<-releaseSchedule
			return false, nil
		})
		deferSchedulerCompletions(release, func() []*schedulerJobCompletion {
			return []*schedulerJobCompletion{completion}
		})

		metric.Metrics.JobsScheduling.Max()
		runErr := make(chan error, 1)
		go func() {
			defer GinkgoRecover()
			runErr <- NewRunner(
				lagertest.NewTestLogger("test"),
				duplicateFactory,
				schedule,
				1,
			).Run(ctx)
		}()

		Eventually(ctx, started, 10*time.Second).Should(Receive())
		Eventually(ctx, runErr, 10*time.Second).Should(Receive(BeNil()))
		Expect(metric.Metrics.JobsScheduling.Max()).To(Equal(float64(1)))
		Consistently(started).ShouldNot(Receive())

		release()
		waitForSchedulerCompletion(ctx, completion)
		Expect(metric.Metrics.JobsScheduling.Max()).To(BeZero())
		_, lastScheduled := schedulerJobTimestamps(fixture, job.ID())
		Expect(lastScheduled).To(Equal(requested))
	})

	It("does not leave a job build when its pipeline is destroyed after scanning", func(ctx SpecContext) {
		fixture := useSchedulerDB()
		scenario := dbtest.Setup(
			fixture.Builder.WithTeam("reload-missing-team"),
			fixture.Builder.WithPipeline(triggeringJobs("reload-missing-job")),
			fixture.Builder.WithResourceVersions("some-resource", atc.Version{"ref": "v1"}),
		)
		job := scenario.Job("reload-missing-job")
		jobID := job.ID()
		requestSchedulerJob(fixture, job)

		destroyingFactory := &destroyPipelineAfterScanJobFactory{
			JobFactory: fixture.JobFactory,
			pipeline:   scenario.Pipeline,
		}
		observed := observeSchedulerJobFactory(destroyingFactory)
		deferObservedFactory(observed, jobID)
		Expect(newRunner(fixture, observed, 1).Run(ctx)).To(Succeed())
		waitObservedFactory(ctx, observed, jobID)

		Expect(destroyingFactory.destroyErr).NotTo(HaveOccurred())
		_, found, err := scenario.Pipeline.Job("reload-missing-job")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		var remainingBuilds int
		Expect(fixture.Conn.QueryRow(`SELECT COUNT(*) FROM builds WHERE job_id = $1`, jobID).Scan(&remainingBuilds)).To(Succeed())
		Expect(remainingBuilds).To(BeZero())
	})
})
