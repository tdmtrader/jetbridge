package scheduler_test

import (
	"context"
	"strconv"
	"sync"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/builds"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/metric"
	. "github.com/concourse/concourse/atc/scheduler"
	"github.com/concourse/concourse/atc/scheduler/algorithm"
	"github.com/concourse/concourse/tracing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

var _ = Describe("Scheduler Metrics & Observability", func() {
	BeforeEach(func() {
		// Delta and Max are destructive reads, so these are intentional resets.
		metric.Metrics.JobsScheduling.Max()
		metric.Metrics.JobsScheduled.Delta()
	})

	newRealScheduler := func(fixture *schedulerDB) *Scheduler {
		GinkgoHelper()
		return NewScheduler(
			builds.NewPlanner(atc.NewPlanFactory(0)),
			algorithm.New(schedulerVersionsDB(fixture)),
		)
	}

	newRequestedJob := func(
		fixture *schedulerDB,
		teamName string,
		pipelineName string,
		jobName string,
	) (db.Job, *observedSchedulerJobFactory) {
		GinkgoHelper()

		team, pipeline := persistSchedulerPipeline(
			fixture,
			teamName,
			pipelineName,
			atc.Config{
				Resources: atc.ResourceConfigs{
					{Name: "some-resource", Type: dbtest.BaseResourceType, Source: atc.Source{"some": "source"}},
				},
				Jobs: atc.JobConfigs{
					{
						Name: jobName,
						PlanSequence: []atc.Step{
							{Config: &atc.GetStep{Name: "some-input", Resource: "some-resource", Trigger: true}},
						},
					},
				},
			},
		)
		scenario := &dbtest.Scenario{Team: team, Pipeline: pipeline}
		scenario.Run(fixture.Builder.WithResourceVersions("some-resource", atc.Version{"ref": "v1"}))

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
			newRealScheduler(fixture).Schedule,
			1,
		)
		Expect(runner.Run(ctx)).To(Succeed())
		waitForSchedulerCompletion(ctx, jobFactory.completion(job.ID()))

		requested, lastScheduled := schedulerJobTimestamps(fixture, job.ID())
		Expect(lastScheduled).To(Equal(requested))
		apiBuilds, _, err := job.BuildsWithTime(db.Page{Limit: 50})
		Expect(err).NotTo(HaveOccurred())
		Expect(apiBuilds).To(HaveLen(1))
		Expect(apiBuilds[0].Status()).To(Equal(db.BuildStatusStarted))
		Expect(apiBuilds[0].HasPlan()).To(BeTrue())
		persistedBuild, found, err := fixture.BuildFactory.Build(apiBuilds[0].ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(persistedBuild.PrivatePlan()).NotTo(Equal(atc.Plan{}))
		Expect(schedulerPendingBuilds(job)).To(BeEmpty())
	}

	newPendingTaskBuild := func(
		fixture *schedulerDB,
		teamName string,
		pipelineName string,
		jobName string,
	) (db.Job, db.Build, db.SchedulerJob, db.InputConfigs) {
		GinkgoHelper()

		_, pipeline := persistSchedulerPipeline(
			fixture,
			teamName,
			pipelineName,
			atc.Config{Jobs: atc.JobConfigs{
				{
					Name: jobName,
					PlanSequence: []atc.Step{
						{Config: &atc.TaskStep{Name: "some-task", ConfigPath: "some/config/path.yml"}},
					},
				},
			}},
		)
		job := schedulerPipelineJob(pipeline, jobName)
		Expect(job.SaveNextInputMapping(nil, true)).To(Succeed())
		Expect(job.EnsurePendingBuildExists(context.Background())).To(Succeed())
		pending, err := job.GetPendingBuilds()
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(HaveLen(1))

		schedulerJob := schedulerJobToSchedule(fixture, job)
		inputs, err := schedulerJob.AlgorithmInputs()
		Expect(err).NotTo(HaveOccurred())
		return job, pending[0], schedulerJob, inputs
	}

	tryStart := func(fixture *schedulerDB, job db.SchedulerJob, inputs db.InputConfigs) {
		GinkgoHelper()
		needsRetry, err := NewBuildStarter(
			builds.NewPlanner(atc.NewPlanFactory(0)),
			algorithm.New(schedulerVersionsDB(fixture)),
		).TryStartPendingBuildsForJob(
			context.Background(),
			lagertest.NewTestLogger("test"),
			job,
			inputs,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(needsRetry).To(BeFalse())
	}

	expectStartedBuild := func(fixture *schedulerDB, build db.Build) {
		GinkgoHelper()
		persistedBuild, found, err := fixture.BuildFactory.Build(build.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(persistedBuild.Status()).To(Equal(db.BuildStatusStarted))
		Expect(persistedBuild.HasPlan()).To(BeTrue())
		Expect(persistedBuild.PrivatePlan()).NotTo(Equal(atc.Plan{}))
	}

	// MO-01: JobsScheduling gauge increments during scheduling and returns to zero.
	// MO-02: JobsScheduled counter increments when scheduling completes.
	Describe("JobsScheduling and JobsScheduled metrics", func() {
		It("reports one live scheduling job and one completed scheduling operation", func(ctx SpecContext) {
			fixture := useSchedulerDB()
			job, jobFactory := newRequestedJob(
				fixture,
				"metric-team",
				"metric-pipeline",
				"metric-job",
			)

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
				return []*schedulerJobCompletion{jobFactory.completion(job.ID())}
			})
			runner := NewRunner(
				lagertest.NewTestLogger("test"),
				jobFactory,
				schedule,
				1,
			)
			Expect(runner.Run(ctx)).To(Succeed())
			Eventually(ctx, started).Should(Receive())

			Expect(metric.Metrics.JobsScheduling.Max()).To(Equal(float64(1)))
			release()
			waitForSchedulerCompletion(ctx, jobFactory.completion(job.ID()))
			Expect(metric.Metrics.JobsScheduling.Max()).To(BeZero())
			Expect(metric.Metrics.JobsScheduled.Delta()).To(Equal(float64(1)))

			requested, lastScheduled := schedulerJobTimestamps(fixture, job.ID())
			Expect(lastScheduled).To(Equal(requested))
		})
	})

	// MO-03: SchedulingJobDuration is emitted after persisted scheduling.
	Describe("SchedulingJobDuration emission", func() {
		It("records duration with the real scheduled job's attributes", func(ctx SpecContext) {
			previousMeterProvider := otel.GetMeterProvider()
			reader := sdkmetric.NewManualReader()
			meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
			otel.SetMeterProvider(meterProvider)
			metric.InitOTelScheduling()
			DeferCleanup(func(ctx SpecContext) {
				Expect(meterProvider.Shutdown(ctx)).To(Succeed())
				otel.SetMeterProvider(previousMeterProvider)
				metric.InitOTelScheduling()
			})

			fixture := useSchedulerDB()
			job, jobFactory := newRequestedJob(
				fixture,
				"duration-team",
				"duration-pipeline",
				"duration-job",
			)
			runAndJoin(ctx, fixture, job, jobFactory)

			var resourceMetrics metricdata.ResourceMetrics
			Expect(reader.Collect(ctx, &resourceMetrics)).To(Succeed())
			var durationMetric *metricdata.Metrics
			for _, scopeMetrics := range resourceMetrics.ScopeMetrics {
				for i := range scopeMetrics.Metrics {
					if scopeMetrics.Metrics[i].Name == "concourse.jobs.scheduling_duration" {
						durationMetric = &scopeMetrics.Metrics[i]
					}
				}
			}
			Expect(durationMetric).NotTo(BeNil())
			histogram, ok := durationMetric.Data.(metricdata.Histogram[float64])
			Expect(ok).To(BeTrue())
			Expect(histogram.DataPoints).To(HaveLen(1))
			Expect(histogram.DataPoints[0].Count).To(Equal(uint64(1)))
			Expect(histogram.DataPoints[0].Sum).To(BeNumerically(">", 0))
			pipeline, ok := histogram.DataPoints[0].Attributes.Value("pipeline")
			Expect(ok).To(BeTrue())
			Expect(pipeline.AsString()).To(Equal("duration-pipeline"))
			jobName, ok := histogram.DataPoints[0].Attributes.Value("job")
			Expect(ok).To(BeTrue())
			Expect(jobName.AsString()).To(Equal("duration-job"))
		})
	})

	// MO-04/05: BuildsStarted vs CheckBuildsStarted counters.
	Describe("BuildsStarted and CheckBuildsStarted metrics", func() {
		BeforeEach(func() {
			metric.Metrics.BuildsStarted.Delta()
			metric.Metrics.CheckBuildsStarted.Delta()
		})

		It("increments BuildsStarted for a persisted non-check build", func() {
			fixture := useSchedulerDB()
			_, pending, schedulerJob, inputs := newPendingTaskBuild(
				fixture,
				"build-metric-team",
				"build-metric-pipeline",
				"build-metric-job",
			)

			tryStart(fixture, schedulerJob, inputs)
			expectStartedBuild(fixture, pending)
			Expect(metric.Metrics.BuildsStarted.Delta()).To(Equal(float64(1)))
			Expect(metric.Metrics.CheckBuildsStarted.Delta()).To(BeZero())
		})

		It("increments CheckBuildsStarted for the fixed check-build name", func() {
			fixture := useSchedulerDB()
			job, pending, schedulerJob, inputs := newPendingTaskBuild(
				fixture,
				"check-metric-team",
				"check-metric-pipeline",
				"check-metric-job",
			)

			// A job-scoped pending-build query cannot return the check-build name,
			// so change only that domain identity and leave the real build untouched.
			schedulerJob.Job = wrappedPendingBuildsJob{
				Job: job,
				wrap: func(_ int, build db.Build) db.Build {
					return checkNamedBuild{Build: build}
				},
			}
			tryStart(fixture, schedulerJob, inputs)
			expectStartedBuild(fixture, pending)
			Expect(metric.Metrics.CheckBuildsStarted.Delta()).To(Equal(float64(1)))
			Expect(metric.Metrics.BuildsStarted.Delta()).To(BeZero())
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
			_, pending, schedulerJob, inputs := newPendingTaskBuild(
				fixture,
				"span-team",
				"span-pipeline",
				"span-job",
			)
			tryStart(fixture, schedulerJob, inputs)
			expectStartedBuild(fixture, pending)

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
			Expect(attributes).To(HaveKeyWithValue("build_id", strconv.Itoa(pending.ID())))
			Expect(attributes).To(HaveKeyWithValue("build", pending.Name()))
		})
	})
})
