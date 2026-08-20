package scheduler_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	. "github.com/concourse/concourse/atc/scheduler"
	"github.com/concourse/concourse/atc/scheduler/schedulerfakes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

type countedSchedulerJobFactory struct {
	db.JobFactory
	calls int
}

func (factory *countedSchedulerJobFactory) JobsToSchedule() (db.SchedulerJobs, error) {
	factory.calls++
	return factory.JobFactory.JobsToSchedule()
}

type closeAfterScanSchedulerJobFactory struct {
	db.JobFactory
	close      func() error
	closeCalls int
	closeErr   error
}

func (factory *closeAfterScanSchedulerJobFactory) JobsToSchedule() (db.SchedulerJobs, error) {
	jobs, err := factory.JobFactory.JobsToSchedule()
	if err != nil {
		return nil, err
	}
	factory.closeCalls++
	factory.closeErr = factory.close()
	if factory.closeErr != nil {
		return nil, fmt.Errorf("close scanned job connection: %w", factory.closeErr)
	}
	return jobs, nil
}

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

type scheduleConsumption struct {
	observed time.Time
	noBuild  bool
}

type consumeRecordingJob struct {
	db.Job
	consumed chan<- scheduleConsumption
}

func (job consumeRecordingJob) ConsumeScheduleRequest(observed time.Time, noBuild bool) error {
	job.consumed <- scheduleConsumption{observed: observed, noBuild: noBuild}
	return job.Job.ConsumeScheduleRequest(observed, noBuild)
}

type consumeRecordingJobFactory struct {
	db.JobFactory
	consumed chan<- scheduleConsumption
}

func (factory consumeRecordingJobFactory) JobsToSchedule() (db.SchedulerJobs, error) {
	jobs, err := factory.JobFactory.JobsToSchedule()
	if err != nil {
		return nil, err
	}
	for i := range jobs {
		jobs[i].Job = consumeRecordingJob{Job: jobs[i].Job, consumed: factory.consumed}
	}
	return jobs, nil
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
	var fakeScheduler *schedulerfakes.FakeBuildScheduler

	BeforeEach(func() {
		fakeScheduler = new(schedulerfakes.FakeBuildScheduler)
	})

	newRunner := func(jobFactory db.JobFactory, maxInFlight uint64) *Runner {
		GinkgoHelper()
		return NewRunner(
			lagertest.NewTestLogger("test"),
			jobFactory,
			fakeScheduler,
			maxInFlight,
		)
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

	It("reports no pending build before schedule consumption and passes the observed token and explicit no-build result", func(ctx SpecContext) {
		fixture := useSchedulerDB()
		_, pipeline := persistSchedulerPipeline(
			fixture,
			"consume-team",
			"consume-pipeline",
			atc.Config{Jobs: atc.JobConfigs{{Name: "consume-job"}}},
		)
		job := schedulerPipelineJob(pipeline, "consume-job")
		requestSchedulerJob(fixture, job)
		observed, _ := schedulerJobTimestamps(fixture, job.ID())

		consumed := make(chan scheduleConsumption, 1)
		recording := consumeRecordingJobFactory{JobFactory: fixture.JobFactory, consumed: consumed}
		tracked := observeSchedulerJobFactory(recording)
		deferObservedFactory(tracked, job.ID())
		fakeScheduler.ScheduleReturns(ScheduleResult{NoBuild: true}, nil)

		Expect(newRunner(tracked, 1).Run(ctx)).To(Succeed())
		waitObservedFactory(ctx, tracked, job.ID())
		Eventually(consumed).Should(Receive(Equal(scheduleConsumption{observed: observed, noBuild: true})))
	})

	It("loads the full persisted job scan and schedules jobs with their resources", func(ctx SpecContext) {
		fixture := useSchedulerDB()
		_, firstPipeline := persistSchedulerPipeline(
			fixture,
			"runner-team-one",
			"runner-pipeline-one",
			atc.Config{
				Resources: atc.ResourceConfigs{
					{Name: "some-resource", Type: "git", Source: atc.Source{"uri": "git://some-resource"}},
					{Name: "some-dependent-resource", Type: "git", Source: atc.Source{"uri": "git://some-dependent-resource"}},
				},
				Jobs: atc.JobConfigs{
					{
						Name: "some-job",
						PlanSequence: []atc.Step{
							{Config: &atc.GetStep{Name: "some-resource"}},
							{Config: &atc.GetStep{Name: "some-dependent-resource"}},
						},
					},
					{
						Name: "some-other-job",
						PlanSequence: []atc.Step{
							{Config: &atc.GetStep{Name: "some-resource"}},
							{Config: &atc.GetStep{Name: "some-dependent-resource"}},
						},
					},
					{Name: "not-requested"},
				},
			},
		)
		_, secondPipeline := persistSchedulerPipeline(
			fixture,
			"runner-team-two",
			"runner-pipeline-two",
			atc.Config{
				Resources: atc.ResourceConfigs{
					{Name: "another-resource", Type: "git", Source: atc.Source{"uri": "git://another-resource"}},
				},
				Jobs: atc.JobConfigs{
					{
						Name: "another-job",
						PlanSequence: []atc.Step{
							{Config: &atc.GetStep{Name: "another-resource"}},
						},
					},
				},
			},
		)

		jobs := []db.Job{
			schedulerPipelineJob(firstPipeline, "some-job"),
			schedulerPipelineJob(firstPipeline, "some-other-job"),
			schedulerPipelineJob(secondPipeline, "another-job"),
		}
		jobRequested := make(map[int]time.Time)
		for _, job := range jobs {
			requestSchedulerJob(fixture, job)
			scheduleRequested, _ := schedulerJobTimestamps(fixture, job.ID())
			jobRequested[job.ID()] = scheduleRequested
		}
		notRequested := schedulerPipelineJob(firstPipeline, "not-requested")
		notRequestedTime, _ := schedulerJobTimestamps(fixture, notRequested.ID())
		Expect(notRequested.UpdateLastScheduled(notRequestedTime)).To(Succeed())

		counted := &countedSchedulerJobFactory{JobFactory: fixture.JobFactory}
		observed := observeSchedulerJobFactory(counted)
		jobIDs := []int{jobs[0].ID(), jobs[1].ID(), jobs[2].ID()}
		deferObservedFactory(observed, jobIDs...)
		fakeScheduler.ScheduleReturns(ScheduleResult{NoBuild: true}, nil)

		Expect(newRunner(observed, 1).Run(ctx)).To(Succeed())
		waitObservedFactory(ctx, observed, jobIDs...)

		Expect(counted.calls).To(Equal(1))
		Expect(fakeScheduler.ScheduleCallCount()).To(Equal(3))
		scheduledResources := map[string]db.SchedulerResources{}
		for i := 0; i < fakeScheduler.ScheduleCallCount(); i++ {
			_, _, scheduledJob := fakeScheduler.ScheduleArgsForCall(i)
			scheduledResources[scheduledJob.Name()] = scheduledJob.Resources
		}
		Expect(scheduledResources).To(MatchAllKeys(Keys{
			"some-job": ConsistOf(
				db.SchedulerResource{Name: "some-resource", Type: "git", Source: atc.Source{"uri": "git://some-resource"}},
				db.SchedulerResource{Name: "some-dependent-resource", Type: "git", Source: atc.Source{"uri": "git://some-dependent-resource"}},
			),
			"some-other-job": ConsistOf(
				db.SchedulerResource{Name: "some-resource", Type: "git", Source: atc.Source{"uri": "git://some-resource"}},
				db.SchedulerResource{Name: "some-dependent-resource", Type: "git", Source: atc.Source{"uri": "git://some-dependent-resource"}},
			),
			"another-job": ConsistOf(
				db.SchedulerResource{Name: "another-resource", Type: "git", Source: atc.Source{"uri": "git://another-resource"}},
			),
		}))
		for _, job := range jobs {
			scheduleRequested, lastScheduled := schedulerJobTimestamps(fixture, job.ID())
			Expect(scheduleRequested).To(Equal(jobRequested[job.ID()]))
			Expect(lastScheduled).To(Equal(jobRequested[job.ID()]))
		}
	})

	It("skips a job whose production scheduling lock is held", func(ctx SpecContext) {
		fixture := useSchedulerDB()
		_, pipeline := persistSchedulerPipeline(
			fixture,
			"lock-team",
			"lock-pipeline",
			atc.Config{Jobs: atc.JobConfigs{{Name: "locked-job"}, {Name: "available-job"}}},
		)
		lockedJob := schedulerPipelineJob(pipeline, "locked-job")
		availableJob := schedulerPipelineJob(pipeline, "available-job")
		requestSchedulerJob(fixture, lockedJob)
		requestSchedulerJob(fixture, availableJob)
		lockedRequested, lockedLast := schedulerJobTimestamps(fixture, lockedJob.ID())
		availableRequested, _ := schedulerJobTimestamps(fixture, availableJob.ID())

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
		fakeScheduler.ScheduleReturns(ScheduleResult{NoBuild: true}, nil)
		Expect(newRunner(observed, 2).Run(ctx)).To(Succeed())
		waitObservedFactory(ctx, observed, lockedJob.ID(), availableJob.ID())

		Expect(heldLock.Release()).To(Succeed())
		released = true
		Expect(fakeScheduler.ScheduleCallCount()).To(Equal(1))
		_, _, scheduledJob := fakeScheduler.ScheduleArgsForCall(0)
		Expect(scheduledJob.ID()).To(Equal(availableJob.ID()))
		persistedLockedRequested, persistedLockedLast := schedulerJobTimestamps(fixture, lockedJob.ID())
		Expect(persistedLockedRequested).To(Equal(lockedRequested))
		Expect(persistedLockedLast).To(Equal(lockedLast))
		_, persistedAvailableLast := schedulerJobTimestamps(fixture, availableJob.ID())
		Expect(persistedAvailableLast).To(Equal(availableRequested))
	})

	It("does not schedule when the real advisory-lock connection fails", func(ctx SpecContext) {
		fixture := useSchedulerDB()
		_, pipeline := persistSchedulerPipeline(
			fixture,
			"doomed-lock-team",
			"doomed-lock-pipeline",
			atc.Config{Jobs: atc.JobConfigs{{Name: "doomed-lock-job"}}},
		)
		job := schedulerPipelineJob(pipeline, "doomed-lock-job")
		requestSchedulerJob(fixture, job)
		requested, initialLast := schedulerJobTimestamps(fixture, job.ID())

		doomedLocks, closeDoomedLocks := openSchedulerLockFactory()
		Expect(closeDoomedLocks()).To(Succeed())
		observed := observeSchedulerJobFactory(db.NewJobFactory(fixture.Conn, doomedLocks))
		deferObservedFactory(observed, job.ID())

		Expect(newRunner(observed, 1).Run(ctx)).To(Succeed())
		waitObservedFactory(ctx, observed, job.ID())
		Expect(fakeScheduler.ScheduleCallCount()).To(BeZero())
		persistedRequested, persistedLast := schedulerJobTimestamps(fixture, job.ID())
		Expect(persistedRequested).To(Equal(requested))
		Expect(persistedLast).To(Equal(initialLast))
	})

	DescribeTable("only advances successful persisted jobs",
		func(ctx SpecContext, outcome string) {
			fixture := useSchedulerDB()
			_, pipeline := persistSchedulerPipeline(
				fixture,
				"outcome-team",
				"outcome-pipeline",
				atc.Config{Jobs: atc.JobConfigs{{Name: "affected-job"}, {Name: "successful-job"}}},
			)
			affectedJob := schedulerPipelineJob(pipeline, "affected-job")
			successfulJob := schedulerPipelineJob(pipeline, "successful-job")
			requestSchedulerJob(fixture, affectedJob)
			requestSchedulerJob(fixture, successfulJob)
			affectedRequested, affectedLast := schedulerJobTimestamps(fixture, affectedJob.ID())
			successfulRequested, _ := schedulerJobTimestamps(fixture, successfulJob.ID())

			fakeScheduler.ScheduleStub = func(_ context.Context, _ lager.Logger, job db.SchedulerJob) (ScheduleResult, error) {
				if job.Name() == "successful-job" {
					return ScheduleResult{NoBuild: true}, nil
				}
				if job.Name() != "affected-job" {
					return ScheduleResult{}, fmt.Errorf("unexpected job %q", job.Name())
				}
				switch outcome {
				case "error":
					return ScheduleResult{}, errors.New("schedule failed")
				case "panic":
					panic("schedule panic")
				case "retry":
					return ScheduleResult{NeedsRetry: true}, nil
				default:
					return ScheduleResult{}, fmt.Errorf("unexpected outcome %q", outcome)
				}
			}

			observed := observeSchedulerJobFactory(fixture.JobFactory)
			deferObservedFactory(observed, affectedJob.ID(), successfulJob.ID())
			Expect(newRunner(observed, 2).Run(ctx)).To(Succeed())
			waitObservedFactory(ctx, observed, affectedJob.ID(), successfulJob.ID())

			persistedAffectedRequested, persistedAffectedLast := schedulerJobTimestamps(fixture, affectedJob.ID())
			Expect(persistedAffectedRequested).To(Equal(affectedRequested))
			Expect(persistedAffectedLast).To(Equal(affectedLast))
			_, persistedSuccessfulLast := schedulerJobTimestamps(fixture, successfulJob.ID())
			Expect(persistedSuccessfulLast).To(Equal(successfulRequested))
		},
		Entry("after a scheduler error", "error"),
		Entry("after a scheduler panic", "panic"),
		Entry("when scheduling needs a retry", "retry"),
	)

	It("deduplicates a deliberately duplicated real job input", func(ctx SpecContext) {
		fixture := useSchedulerDB()
		_, pipeline := persistSchedulerPipeline(
			fixture,
			"duplicate-team",
			"duplicate-pipeline",
			atc.Config{Jobs: atc.JobConfigs{{Name: "duplicate-job"}}},
		)
		job := schedulerPipelineJob(pipeline, "duplicate-job")
		requestSchedulerJob(fixture, job)
		requested, _ := schedulerJobTimestamps(fixture, job.ID())

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

		scheduling := make(chan struct{}, 2)
		unblock := make(chan struct{})
		var once sync.Once
		release := func() { once.Do(func() { close(unblock) }) }
		fakeScheduler.ScheduleStub = func(context.Context, lager.Logger, db.SchedulerJob) (ScheduleResult, error) {
			scheduling <- struct{}{}
			<-unblock
			return ScheduleResult{NoBuild: true}, nil
		}
		deferSchedulerCompletions(release, func() []*schedulerJobCompletion {
			return []*schedulerJobCompletion{completion}
		})

		// The single scheduling slot is held for as long as Schedule blocks, so
		// Run can only return while the duplicate is still in flight if the
		// duplicate was skipped outright.
		runErr := make(chan error, 1)
		go func() {
			defer GinkgoRecover()
			runErr <- newRunner(duplicateFactory, 1).Run(ctx)
		}()

		Eventually(ctx, scheduling, 10*time.Second).Should(Receive())
		Eventually(ctx, runErr, 10*time.Second).Should(Receive(BeNil()))

		release()
		waitForSchedulerCompletion(ctx, completion)
		Expect(fakeScheduler.ScheduleCallCount()).To(Equal(1))
		_, lastScheduled := schedulerJobTimestamps(fixture, job.ID())
		Expect(lastScheduled).To(Equal(requested))
	})

	It("does not advance a job whose reload uses a closed secondary connection", func(ctx SpecContext) {
		fixture := useSchedulerDB()
		_, pipeline := persistSchedulerPipeline(
			fixture,
			"reload-error-team",
			"reload-error-pipeline",
			atc.Config{Jobs: atc.JobConfigs{{Name: "reload-error-job"}}},
		)
		job := schedulerPipelineJob(pipeline, "reload-error-job")
		requestSchedulerJob(fixture, job)
		requested, initialLast := schedulerJobTimestamps(fixture, job.ID())

		secondaryConn := schedulerPostgresRunner.OpenConn()
		closingFactory := &closeAfterScanSchedulerJobFactory{
			JobFactory: db.NewJobFactory(secondaryConn, fixture.LockFactory),
			close:      secondaryConn.Close,
		}
		observed := observeSchedulerJobFactory(closingFactory)
		deferObservedFactory(observed, job.ID())

		Expect(newRunner(observed, 1).Run(ctx)).To(Succeed())
		waitObservedFactory(ctx, observed, job.ID())
		Expect(closingFactory.closeCalls).To(Equal(1))
		Expect(closingFactory.closeErr).NotTo(HaveOccurred())
		Expect(fakeScheduler.ScheduleCallCount()).To(BeZero())
		persistedRequested, persistedLast := schedulerJobTimestamps(fixture, job.ID())
		Expect(persistedRequested).To(Equal(requested))
		Expect(persistedLast).To(Equal(initialLast))
	})

	It("does not schedule a real job deleted with its pipeline after scanning", func(ctx SpecContext) {
		fixture := useSchedulerDB()
		_, pipeline := persistSchedulerPipeline(
			fixture,
			"reload-missing-team",
			"reload-missing-pipeline",
			atc.Config{Jobs: atc.JobConfigs{{Name: "reload-missing-job"}}},
		)
		job := schedulerPipelineJob(pipeline, "reload-missing-job")
		requestSchedulerJob(fixture, job)

		destroyingFactory := &destroyPipelineAfterScanJobFactory{
			JobFactory: fixture.JobFactory,
			pipeline:   pipeline,
		}
		observed := observeSchedulerJobFactory(destroyingFactory)
		deferObservedFactory(observed, job.ID())

		Expect(newRunner(observed, 1).Run(ctx)).To(Succeed())
		waitObservedFactory(ctx, observed, job.ID())
		Expect(destroyingFactory.destroyErr).NotTo(HaveOccurred())
		Expect(fakeScheduler.ScheduleCallCount()).To(BeZero())
		_, found, err := pipeline.Job("reload-missing-job")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("does not advance last_scheduled when its update connection closes", func(ctx SpecContext) {
		fixture := useSchedulerDB()
		_, pipeline := persistSchedulerPipeline(
			fixture,
			"update-error-team",
			"update-error-pipeline",
			atc.Config{Jobs: atc.JobConfigs{{Name: "update-error-job"}}},
		)
		job := schedulerPipelineJob(pipeline, "update-error-job")
		requestSchedulerJob(fixture, job)
		requested, initialLast := schedulerJobTimestamps(fixture, job.ID())

		secondaryConn := schedulerPostgresRunner.OpenConn()
		closeCalls := 0
		var closeErr error
		fakeScheduler.ScheduleStub = func(_ context.Context, _ lager.Logger, _ db.SchedulerJob) (ScheduleResult, error) {
			closeCalls++
			closeErr = secondaryConn.Close()
			return ScheduleResult{}, closeErr
		}
		observed := observeSchedulerJobFactory(db.NewJobFactory(secondaryConn, fixture.LockFactory))
		deferObservedFactory(observed, job.ID())

		Expect(newRunner(observed, 1).Run(ctx)).To(Succeed())
		waitObservedFactory(ctx, observed, job.ID())
		Expect(closeCalls).To(Equal(1))
		Expect(closeErr).NotTo(HaveOccurred())
		persistedRequested, persistedLast := schedulerJobTimestamps(fixture, job.ID())
		Expect(persistedRequested).To(Equal(requested))
		Expect(persistedLast).To(Equal(initialLast))
	})

	It("returns a direct persisted job-scan error", func(ctx SpecContext) {
		fixture := useSchedulerDB()
		doomedConn := schedulerPostgresRunner.OpenConn()
		Expect(doomedConn.Close()).To(Succeed())

		err := newRunner(db.NewJobFactory(doomedConn, fixture.LockFactory), 1).Run(ctx)
		Expect(err).To(MatchError(ContainSubstring("find jobs to schedule")))
		Expect(fakeScheduler.ScheduleCallCount()).To(BeZero())
	})
})
