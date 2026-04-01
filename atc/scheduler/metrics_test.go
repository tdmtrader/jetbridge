package scheduler_test

import (
	"context"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/db/lock/lockfakes"
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
	var (
		fakeJobFactory *dbfakes.FakeJobFactory
		fakeScheduler  *schedulerfakes.FakeBuildScheduler
		lock           *lockfakes.FakeLock
	)

	BeforeEach(func() {
		fakeJobFactory = new(dbfakes.FakeJobFactory)
		fakeScheduler = new(schedulerfakes.FakeBuildScheduler)
		lock = new(lockfakes.FakeLock)

		// Drain any leftover metric state from other tests
		metric.Metrics.JobsScheduling.Max()
		metric.Metrics.JobsScheduled.Delta()
	})

	// MO-01: JobsScheduling gauge increments during scheduling and decrements after
	// MO-02: JobsScheduled counter increments when scheduling completes
	Describe("JobsScheduling and JobsScheduled metrics", func() {
		It("increments JobsScheduling during scheduling and JobsScheduled on completion", func() {
			fakeJob := new(dbfakes.FakeJob)
			fakeJob.IDReturns(1)
			fakeJob.NameReturns("metric-job")
			fakeJob.ReloadReturns(true, nil)
			fakeJob.ScheduleRequestedTimeReturns(time.Now())
			fakeJob.AcquireSchedulingLockReturns(lock, true, nil)

			var schedulingGaugeSeen float64
			fakeScheduler.ScheduleStub = func(_ context.Context, _ lager.Logger, _ db.SchedulerJob) (bool, error) {
				// Capture the gauge value while scheduling is in progress
				schedulingGaugeSeen = metric.Metrics.JobsScheduling.Max()
				return false, nil
			}

			fakeJobFactory.JobsToScheduleReturns([]db.SchedulerJob{
				{Job: fakeJob},
			}, nil)

			// Drain counters before test
			metric.Metrics.JobsScheduled.Delta()
			metric.Metrics.JobsScheduling.Max()

			runner := NewRunner(
				lagertest.NewTestLogger("test"),
				fakeJobFactory,
				fakeScheduler,
				1,
			)

			err := runner.Run(context.TODO())
			Expect(err).NotTo(HaveOccurred())

			// Wait for the goroutine to complete
			Eventually(fakeScheduler.ScheduleCallCount).Should(Equal(1))
			Eventually(fakeJob.UpdateLastScheduledCallCount).Should(Equal(1))

			// MO-01: JobsScheduling was incremented during scheduling
			Expect(schedulingGaugeSeen).To(BeNumerically(">=", 1))

			// MO-02: JobsScheduled was incremented after scheduling completed
			Eventually(func() float64 {
				return metric.Metrics.JobsScheduled.Delta()
			}).Should(BeNumerically(">=", 1))
		})
	})

	// MO-03: SchedulingJobDuration is emitted with pipeline name, job name, and duration
	// Note: SchedulingJobDuration.Emit() calls metric.Metrics.emit() which requires an
	// initialized emitter. We test the struct construction and emission call path.
	Describe("SchedulingJobDuration emission", func() {
		It("emits after scheduling a job", func() {
			fakeJob := new(dbfakes.FakeJob)
			fakeJob.IDReturns(42)
			fakeJob.NameReturns("duration-job")
			fakeJob.PipelineNameReturns("duration-pipeline")
			fakeJob.ReloadReturns(true, nil)
			fakeJob.ScheduleRequestedTimeReturns(time.Now())
			fakeJob.AcquireSchedulingLockReturns(lock, true, nil)
			fakeScheduler.ScheduleReturns(false, nil)

			fakeJobFactory.JobsToScheduleReturns([]db.SchedulerJob{
				{Job: fakeJob},
			}, nil)

			runner := NewRunner(
				lagertest.NewTestLogger("test"),
				fakeJobFactory,
				fakeScheduler,
				1,
			)

			err := runner.Run(context.TODO())
			Expect(err).NotTo(HaveOccurred())
			Eventually(fakeScheduler.ScheduleCallCount).Should(Equal(1))

			// The SchedulingJobDuration.Emit() is called inside scheduleJob.
			// We verify it doesn't panic and the job was fully processed
			// (UpdateLastScheduled is the last step after Emit).
			Eventually(fakeJob.UpdateLastScheduledCallCount).Should(Equal(1))
		})
	})

	// MO-04/05: BuildsStarted vs CheckBuildsStarted counters
	Describe("BuildsStarted and CheckBuildsStarted metrics", func() {
		var (
			buildStarter BuildStarter
			fakePlanner  *schedulerfakes.FakeBuildPlanner
			fakeAlgorithm *schedulerfakes.FakeAlgorithm
			fakeJob      *dbfakes.FakeJob
		)

		BeforeEach(func() {
			fakePlanner = new(schedulerfakes.FakeBuildPlanner)
			fakeAlgorithm = new(schedulerfakes.FakeAlgorithm)
			buildStarter = NewBuildStarter(fakePlanner, fakeAlgorithm)

			fakeJob = new(dbfakes.FakeJob)
			fakeJob.ConfigReturns(atc.JobConfig{}, nil)
			fakeJob.ScheduleBuildReturns(true, nil)

			fakePlanner.CreateReturns(atc.Plan{}, nil)

			// Drain counters
			metric.Metrics.BuildsStarted.Delta()
			metric.Metrics.CheckBuildsStarted.Delta()
		})

		It("increments BuildsStarted for non-check builds", func() {
			fakeBuild := new(dbfakes.FakeBuild)
			fakeBuild.IDReturns(1)
			fakeBuild.NameReturns("42") // regular build name (a number)
			fakeBuild.StartReturns(true, nil)
			fakeBuild.AdoptInputsAndPipesReturns([]db.BuildInput{}, true, nil)

			fakeJob.GetPendingBuildsReturns([]db.Build{fakeBuild}, nil)

			_, err := buildStarter.TryStartPendingBuildsForJob(
				context.Background(),
				lagertest.NewTestLogger("test"),
				db.SchedulerJob{Job: fakeJob},
				db.InputConfigs{},
			)
			Expect(err).NotTo(HaveOccurred())

			// MO-04: BuildsStarted incremented for non-check builds
			Expect(metric.Metrics.BuildsStarted.Delta()).To(BeNumerically("==", 1))
			// MO-05: CheckBuildsStarted NOT incremented
			Expect(metric.Metrics.CheckBuildsStarted.Delta()).To(BeNumerically("==", 0))
		})

		It("increments CheckBuildsStarted for check builds", func() {
			fakeBuild := new(dbfakes.FakeBuild)
			fakeBuild.IDReturns(2)
			fakeBuild.NameReturns(db.CheckBuildName) // "check"
			fakeBuild.StartReturns(true, nil)
			fakeBuild.AdoptInputsAndPipesReturns([]db.BuildInput{}, true, nil)

			fakeJob.GetPendingBuildsReturns([]db.Build{fakeBuild}, nil)

			_, err := buildStarter.TryStartPendingBuildsForJob(
				context.Background(),
				lagertest.NewTestLogger("test"),
				db.SchedulerJob{Job: fakeJob},
				db.InputConfigs{},
			)
			Expect(err).NotTo(HaveOccurred())

			// MO-05: CheckBuildsStarted incremented for check builds
			Expect(metric.Metrics.CheckBuildsStarted.Delta()).To(BeNumerically("==", 1))
			// MO-04: BuildsStarted NOT incremented
			Expect(metric.Metrics.BuildsStarted.Delta()).To(BeNumerically("==", 0))
		})
	})

	// MO-06: Tracing span structure
	Describe("Tracing spans", func() {
		It("creates schedule-job span with correct attributes", func() {
			exporter := tracetest.NewInMemoryExporter()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
			tracing.ConfigureTraceProvider(tp)
			defer func() { tracing.Configured = false }()

			fakeJob := new(dbfakes.FakeJob)
			fakeJob.IDReturns(1)
			fakeJob.NameReturns("traced-job")
			fakeJob.TeamNameReturns("traced-team")
			fakeJob.PipelineNameReturns("traced-pipeline")
			fakeJob.ReloadReturns(true, nil)
			fakeJob.ScheduleRequestedTimeReturns(time.Now())
			fakeJob.AcquireSchedulingLockReturns(lock, true, nil)
			fakeScheduler.ScheduleReturns(false, nil)

			fakeJobFactory.JobsToScheduleReturns([]db.SchedulerJob{
				{Job: fakeJob},
			}, nil)

			runner := NewRunner(
				lagertest.NewTestLogger("test"),
				fakeJobFactory,
				fakeScheduler,
				1,
			)

			err := runner.Run(context.TODO())
			Expect(err).NotTo(HaveOccurred())
			Eventually(fakeScheduler.ScheduleCallCount).Should(Equal(1))
			Eventually(fakeJob.UpdateLastScheduledCallCount).Should(Equal(1))

			// MO-06: Verify schedule-job span was created with correct attributes
			spans := exporter.GetSpans()
			var scheduleJobSpan *tracetest.SpanStub
			for i, span := range spans {
				if span.Name == "schedule-job" {
					scheduleJobSpan = &spans[i]
					break
				}
			}

			Expect(scheduleJobSpan).NotTo(BeNil(), "expected schedule-job span to exist")

			attrMap := map[string]string{}
			for _, attr := range scheduleJobSpan.Attributes {
				attrMap[string(attr.Key)] = attr.Value.AsString()
			}

			Expect(attrMap["team"]).To(Equal("traced-team"))
			Expect(attrMap["pipeline"]).To(Equal("traced-pipeline"))
			Expect(attrMap["job"]).To(Equal("traced-job"))
		})

		It("creates try-start-pending-build span with build attributes", func() {
			exporter := tracetest.NewInMemoryExporter()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
			tracing.ConfigureTraceProvider(tp)
			defer func() { tracing.Configured = false }()

			fakePlanner := new(schedulerfakes.FakeBuildPlanner)
			fakeAlgorithm := new(schedulerfakes.FakeAlgorithm)
			buildStarter := NewBuildStarter(fakePlanner, fakeAlgorithm)

			fakeJob := new(dbfakes.FakeJob)
			fakeJob.NameReturns("span-job")
			fakeJob.TeamNameReturns("span-team")
			fakeJob.PipelineNameReturns("span-pipeline")
			fakeJob.ConfigReturns(atc.JobConfig{}, nil)
			fakeJob.ScheduleBuildReturns(true, nil)
			fakePlanner.CreateReturns(atc.Plan{}, nil)

			fakeBuild := new(dbfakes.FakeBuild)
			fakeBuild.IDReturns(77)
			fakeBuild.NameReturns("77")
			fakeBuild.StartReturns(true, nil)
			fakeBuild.AdoptInputsAndPipesReturns([]db.BuildInput{}, true, nil)

			fakeJob.GetPendingBuildsReturns([]db.Build{fakeBuild}, nil)

			_, err := buildStarter.TryStartPendingBuildsForJob(
				context.Background(),
				lagertest.NewTestLogger("test"),
				db.SchedulerJob{
					Job: fakeJob,
				},
				db.InputConfigs{},
			)
			Expect(err).NotTo(HaveOccurred())

			spans := exporter.GetSpans()
			var tryStartSpan *tracetest.SpanStub
			for i, span := range spans {
				if span.Name == "scheduler.try-start-pending-build" {
					tryStartSpan = &spans[i]
					break
				}
			}

			Expect(tryStartSpan).NotTo(BeNil(), "expected scheduler.try-start-pending-build span to exist")

			attrMap := map[string]string{}
			for _, attr := range tryStartSpan.Attributes {
				attrMap[string(attr.Key)] = attr.Value.AsString()
			}

			Expect(attrMap["team"]).To(Equal("span-team"))
			Expect(attrMap["pipeline"]).To(Equal("span-pipeline"))
			Expect(attrMap["job"]).To(Equal("span-job"))
			Expect(attrMap["build_id"]).To(Equal("77"))
			Expect(attrMap["build"]).To(Equal("77"))
		})
	})
})
