package scheduler_test

import (
	"context"
	"strconv"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
	. "github.com/concourse/concourse/atc/scheduler"
	"github.com/concourse/concourse/atc/scheduler/schedulerfakes"
	"github.com/concourse/concourse/tracing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

var _ = Describe("Scheduler Metrics & Observability", func() {
	var fakeScheduler *schedulerfakes.FakeBuildScheduler

	BeforeEach(func() {
		fakeScheduler = new(schedulerfakes.FakeBuildScheduler)

		// Drain metric state left by earlier specs in this process.
		metric.Metrics.JobsScheduling.Max()
		metric.Metrics.JobsScheduled.Delta()
	})

	newRequestedJob := func(
		fixture *schedulerDB,
		teamName string,
		pipelineName string,
		jobName string,
	) (db.Job, *observedSchedulerJobFactory) {
		GinkgoHelper()

		_, pipeline := persistSchedulerPipeline(
			fixture,
			teamName,
			pipelineName,
			atc.Config{Jobs: atc.JobConfigs{{Name: jobName}}},
		)
		job := schedulerPipelineJob(pipeline, jobName)
		requestSchedulerJob(fixture, job)
		return job, observeSchedulerJobFactory(fixture.JobFactory)
	}

	runAndJoin := func(
		ctx SpecContext,
		fixture *schedulerDB,
		job db.Job,
		jobFactory *observedSchedulerJobFactory,
	) {
		GinkgoHelper()

		deferSchedulerCompletions(nil, func() []*schedulerJobCompletion {
			return []*schedulerJobCompletion{jobFactory.completion(job.ID())}
		})
		runner := NewRunner(
			lagertest.NewTestLogger("test"),
			jobFactory,
			fakeScheduler,
			1,
		)
		Expect(runner.Run(ctx)).To(Succeed())
		waitForSchedulerCompletion(ctx, jobFactory.completion(job.ID()))

		requested, lastScheduled := schedulerJobTimestamps(fixture, job.ID())
		Expect(lastScheduled).To(Equal(requested))
	}

	// MO-01: JobsScheduling gauge increments during scheduling and decrements after.
	// MO-02: JobsScheduled counter increments when scheduling completes.
	Describe("JobsScheduling and JobsScheduled metrics", func() {
		It("increments JobsScheduling during scheduling and JobsScheduled on completion", func(ctx SpecContext) {
			fixture := useSchedulerDB()
			job, jobFactory := newRequestedJob(
				fixture,
				"metric-team",
				"metric-pipeline",
				"metric-job",
			)

			gaugeObserved := make(chan float64, 1)
			fakeScheduler.ScheduleStub = func(_ context.Context, _ lager.Logger, _ db.SchedulerJob) (ScheduleResult, error) {
				gaugeObserved <- metric.Metrics.JobsScheduling.Max()
				return ScheduleResult{}, nil
			}

			metric.Metrics.JobsScheduled.Delta()
			metric.Metrics.JobsScheduling.Max()
			runAndJoin(ctx, fixture, job, jobFactory)

			var schedulingGaugeSeen float64
			Eventually(ctx, gaugeObserved).Should(Receive(&schedulingGaugeSeen))
			Expect(schedulingGaugeSeen).To(BeNumerically(">=", 1))
			Expect(metric.Metrics.JobsScheduled.Delta()).To(BeNumerically(">=", 1))
		})
	})

	// MO-03: SchedulingJobDuration is emitted after persisted scheduling.
	Describe("SchedulingJobDuration emission", func() {
		It("emits after scheduling a job", func(ctx SpecContext) {
			fixture := useSchedulerDB()
			job, jobFactory := newRequestedJob(
				fixture,
				"duration-team",
				"duration-pipeline",
				"duration-job",
			)
			fakeScheduler.ScheduleReturns(ScheduleResult{}, nil)

			runAndJoin(ctx, fixture, job, jobFactory)
			Expect(fakeScheduler.ScheduleCallCount()).To(Equal(1))
		})
	})

	// MO-04/05: BuildsStarted vs CheckBuildsStarted counters.
	Describe("BuildsStarted and CheckBuildsStarted metrics", func() {
		var (
			buildStarter  BuildStarter
			fakePlanner   *schedulerfakes.FakeBuildPlanner
			fakeAlgorithm *schedulerfakes.FakeAlgorithm
		)

		BeforeEach(func() {
			fakePlanner = new(schedulerfakes.FakeBuildPlanner)
			fakeAlgorithm = new(schedulerfakes.FakeAlgorithm)
			buildStarter = NewBuildStarter(fakePlanner, fakeAlgorithm)
			fakePlanner.CreateReturns(atc.Plan{}, nil)

			metric.Metrics.BuildsStarted.Delta()
			metric.Metrics.CheckBuildsStarted.Delta()
		})

		It("increments BuildsStarted for a persisted non-check build", func() {
			fixture := useSchedulerDB()
			_, pipeline := persistSchedulerPipeline(
				fixture,
				"build-metric-team",
				"build-metric-pipeline",
				atc.Config{Jobs: atc.JobConfigs{{Name: "build-metric-job"}}},
			)
			job := schedulerPipelineJob(pipeline, "build-metric-job")
			Expect(job.SaveNextInputMapping(nil, true)).To(Succeed())
			Expect(job.EnsurePendingBuildExists(context.Background())).To(Succeed())
			pending, err := job.GetPendingBuilds()
			Expect(err).NotTo(HaveOccurred())
			Expect(pending).To(HaveLen(1))

			_, err = buildStarter.TryStartPendingBuildsForJob(
				context.Background(),
				lagertest.NewTestLogger("test"),
				db.SchedulerJob{Job: job},
				db.InputConfigs{},
			)
			Expect(err).NotTo(HaveOccurred())

			persistedBuild, found, err := fixture.BuildFactory.Build(pending[0].ID())
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(persistedBuild.Status()).To(Equal(db.BuildStatusStarted))
			Expect(metric.Metrics.BuildsStarted.Delta()).To(BeNumerically("==", 1))
			Expect(metric.Metrics.CheckBuildsStarted.Delta()).To(BeNumerically("==", 0))
		})

		It("increments CheckBuildsStarted for the check-build name", func() {
			fixture := useSchedulerDB()
			_, pipeline := persistSchedulerPipeline(
				fixture,
				"check-metric-team",
				"check-metric-pipeline",
				atc.Config{Jobs: atc.JobConfigs{{Name: "check-metric-job"}}},
			)
			job := schedulerPipelineJob(pipeline, "check-metric-job")
			Expect(job.SaveNextInputMapping(nil, true)).To(Succeed())
			Expect(job.EnsurePendingBuildExists(context.Background())).To(Succeed())
			pending, err := job.GetPendingBuilds()
			Expect(err).NotTo(HaveOccurred())
			Expect(pending).To(HaveLen(1))

			// A job-scoped pending-build query cannot return the check-build name,
			// so rename the persisted build and leave everything else real.
			checkNamedJob := wrappedPendingBuildsJob{
				Job: job,
				wrap: func(_ int, build db.Build) db.Build {
					return checkNamedBuild{Build: build}
				},
			}

			_, err = buildStarter.TryStartPendingBuildsForJob(
				context.Background(),
				lagertest.NewTestLogger("test"),
				db.SchedulerJob{Job: checkNamedJob},
				db.InputConfigs{},
			)
			Expect(err).NotTo(HaveOccurred())

			persistedBuild, found, err := fixture.BuildFactory.Build(pending[0].ID())
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(persistedBuild.Status()).To(Equal(db.BuildStatusStarted))
			Expect(metric.Metrics.CheckBuildsStarted.Delta()).To(BeNumerically("==", 1))
			Expect(metric.Metrics.BuildsStarted.Delta()).To(BeNumerically("==", 0))
		})
	})

	// MO-06: Tracing span structure.
	Describe("Tracing spans", func() {
		It("creates a schedule-job span with persisted job attributes", func(ctx SpecContext) {
			exporter := tracetest.NewInMemoryExporter()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
			tracing.ConfigureTraceProvider(tp)
			DeferCleanup(func() { tracing.Configured = false })

			fixture := useSchedulerDB()
			job, jobFactory := newRequestedJob(
				fixture,
				"traced-team",
				"traced-pipeline",
				"traced-job",
			)
			fakeScheduler.ScheduleReturns(ScheduleResult{}, nil)
			runAndJoin(ctx, fixture, job, jobFactory)

			spans := exporter.GetSpans()
			var scheduleJobSpan *tracetest.SpanStub
			for i, span := range spans {
				if span.Name == "schedule-job" {
					scheduleJobSpan = &spans[i]
					break
				}
			}
			Expect(scheduleJobSpan).NotTo(BeNil(), "expected schedule-job span to exist")

			attributes := map[string]string{}
			for _, attr := range scheduleJobSpan.Attributes {
				attributes[string(attr.Key)] = attr.Value.AsString()
			}
			Expect(attributes).To(HaveKeyWithValue("team", "traced-team"))
			Expect(attributes).To(HaveKeyWithValue("pipeline", "traced-pipeline"))
			Expect(attributes).To(HaveKeyWithValue("job", "traced-job"))
		})

		It("creates a try-start-pending-build span with persisted build attributes", func() {
			exporter := tracetest.NewInMemoryExporter()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
			tracing.ConfigureTraceProvider(tp)
			DeferCleanup(func() { tracing.Configured = false })

			fixture := useSchedulerDB()
			_, pipeline := persistSchedulerPipeline(
				fixture,
				"span-team",
				"span-pipeline",
				atc.Config{Jobs: atc.JobConfigs{{Name: "span-job"}}},
			)
			job := schedulerPipelineJob(pipeline, "span-job")
			Expect(job.SaveNextInputMapping(nil, true)).To(Succeed())
			Expect(job.EnsurePendingBuildExists(context.Background())).To(Succeed())
			pending, err := job.GetPendingBuilds()
			Expect(err).NotTo(HaveOccurred())
			Expect(pending).To(HaveLen(1))

			fakePlanner := new(schedulerfakes.FakeBuildPlanner)
			fakePlanner.CreateReturns(atc.Plan{}, nil)
			buildStarter := NewBuildStarter(fakePlanner, new(schedulerfakes.FakeAlgorithm))
			_, err = buildStarter.TryStartPendingBuildsForJob(
				context.Background(),
				lagertest.NewTestLogger("test"),
				db.SchedulerJob{Job: job},
				db.InputConfigs{},
			)
			Expect(err).NotTo(HaveOccurred())

			persistedBuild, found, err := fixture.BuildFactory.Build(pending[0].ID())
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(persistedBuild.Status()).To(Equal(db.BuildStatusStarted))

			spans := exporter.GetSpans()
			var tryStartSpan *tracetest.SpanStub
			for i, span := range spans {
				if span.Name == "scheduler.try-start-pending-build" {
					tryStartSpan = &spans[i]
					break
				}
			}
			Expect(tryStartSpan).NotTo(BeNil(), "expected scheduler.try-start-pending-build span to exist")

			attributes := map[string]string{}
			for _, attr := range tryStartSpan.Attributes {
				attributes[string(attr.Key)] = attr.Value.AsString()
			}
			Expect(attributes).To(HaveKeyWithValue("team", "span-team"))
			Expect(attributes).To(HaveKeyWithValue("pipeline", "span-pipeline"))
			Expect(attributes).To(HaveKeyWithValue("job", "span-job"))
			Expect(attributes).To(HaveKeyWithValue("build_id", strconv.Itoa(pending[0].ID())))
			Expect(attributes).To(HaveKeyWithValue("build", pending[0].Name()))
		})
	})
})
