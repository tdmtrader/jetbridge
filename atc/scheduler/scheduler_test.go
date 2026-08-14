package scheduler_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/builds"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	. "github.com/concourse/concourse/atc/scheduler"
	"github.com/concourse/concourse/atc/scheduler/algorithm"
	"github.com/concourse/concourse/atc/scheduler/schedulerfakes"
	"github.com/concourse/concourse/tracing"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	gocache "github.com/patrickmn/go-cache"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

var triggerPipeline = atc.Config{
	Resources: atc.ResourceConfigs{
		{Name: "resource-a", Type: dbtest.BaseResourceType, Source: atc.Source{"some": "a"}},
		{Name: "resource-b", Type: dbtest.BaseResourceType, Source: atc.Source{"some": "b"}},
		{Name: "resource-c", Type: dbtest.BaseResourceType, Source: atc.Source{"some": "c"}},
	},
	Jobs: atc.JobConfigs{
		{
			Name: "some-job",
			PlanSequence: []atc.Step{
				{Config: &atc.GetStep{Name: "a", Resource: "resource-a", Trigger: true}},
				{Config: &atc.GetStep{Name: "b", Resource: "resource-b"}},
				{Config: &atc.GetStep{Name: "c", Resource: "resource-c", Trigger: true}},
			},
		},
	},
}

func schedulerVersionsDB(fixture *schedulerDB) db.VersionsDB {
	return db.NewVersionsDB(fixture.Conn, 100, gocache.New(time.Minute, time.Minute))
}

func schedulerInputResult(
	versions db.VersionsDB,
	resource db.Resource,
	version atc.Version,
	firstOccurrence bool,
) db.InputResult {
	GinkgoHelper()

	digest, found, err := versions.FindVersionOfResource(context.Background(), resource.ID(), version)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())

	return db.InputResult{
		Input: &db.AlgorithmInput{
			AlgorithmVersion: db.AlgorithmVersion{
				ResourceID: resource.ID(),
				Version:    digest,
			},
			FirstOccurrence: firstOccurrence,
		},
	}
}

func schedulerPendingBuilds(job db.Job) []db.Build {
	GinkgoHelper()

	pending, err := job.GetPendingBuilds()
	Expect(err).NotTo(HaveOccurred())
	return pending
}

func schedulerJobHasNewInputs(job db.Job) bool {
	GinkgoHelper()

	found, err := job.Reload()
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return job.HasNewInputs()
}

var _ = Describe("Scheduler", func() {
	var (
		fakeAlgorithm    *schedulerfakes.FakeAlgorithm
		fakeBuildStarter *schedulerfakes.FakeBuildStarter

		scheduler *Scheduler

		disaster error
	)

	BeforeEach(func() {
		fakeAlgorithm = new(schedulerfakes.FakeAlgorithm)
		fakeBuildStarter = new(schedulerfakes.FakeBuildStarter)

		scheduler = &Scheduler{
			Algorithm:    fakeAlgorithm,
			BuildStarter: fakeBuildStarter,
		}

		disaster = errors.New("bad thing")
	})

	schedule := func(ctx context.Context, job db.Job) error {
		GinkgoHelper()

		_, err := scheduler.Schedule(ctx, lagertest.NewTestLogger("test"), db.SchedulerJob{
			Job:       job,
			Resources: db.SchedulerResources{{Name: "some-resource"}},
		})
		return err
	}

	Describe("a job with no configured inputs", func() {
		var (
			fixture *schedulerDB
			job     db.Job
		)

		BeforeEach(func() {
			fixture = useSchedulerDB()
			_, pipeline := persistSchedulerPipeline(
				fixture,
				"scheduler-team",
				"scheduler-pipeline",
				atc.Config{
					Jobs: atc.JobConfigs{
						{
							Name: "some-job-1",
							PlanSequence: []atc.Step{
								{Config: &atc.TaskStep{Name: "some-task", ConfigPath: "some/config/path.yml"}},
							},
						},
					},
				},
			)
			job = schedulerPipelineJob(pipeline, "some-job-1")

			fakeAlgorithm.ComputeReturns(db.InputMapping{}, true, false, nil)
		})

		It("computes the inputs for the job it was given", func() {
			Expect(schedule(context.Background(), job)).To(Succeed())

			Expect(fakeAlgorithm.ComputeCallCount()).To(Equal(1))
			_, actualJob, actualInputs := fakeAlgorithm.ComputeArgsForCall(0)
			Expect(actualJob.Name()).To(Equal("some-job-1"))
			Expect(actualInputs).To(BeNil())
		})

		It("persists a resolved empty input mapping with real scheduling services", func() {
			realScheduler := NewScheduler(
				builds.NewPlanner(atc.NewPlanFactory(0)),
				algorithm.New(schedulerVersionsDB(fixture)),
			)

			_, err := realScheduler.Schedule(
				context.Background(),
				lagertest.NewTestLogger("test"),
				db.SchedulerJob{Job: job},
			)
			Expect(err).NotTo(HaveOccurred())

			buildInputs, resolved, err := job.GetFullNextBuildInputs()
			Expect(err).NotTo(HaveOccurred())
			Expect(resolved).To(BeTrue())
			Expect(buildInputs).To(BeEmpty())
			Expect(schedulerPendingBuilds(job)).To(BeEmpty())
		})

		It("returns the error when the job inputs fail to fetch", func() {
			err := schedule(context.Background(), algorithmInputsFailsJob{Job: job, err: disaster})
			Expect(err).To(Equal(fmt.Errorf("inputs: %w", disaster)))
		})

		It("returns the error when computing the inputs fails", func() {
			fakeAlgorithm.ComputeReturns(nil, false, false, disaster)

			err := schedule(context.Background(), job)
			Expect(err).To(Equal(fmt.Errorf("compute inputs: %w", disaster)))
		})

		It("requests schedule when the algorithm can run again", func() {
			fakeAlgorithm.ComputeReturns(db.InputMapping{}, true, true, nil)
			before, _ := schedulerJobTimestamps(fixture, job.ID())

			Expect(schedule(context.Background(), job)).To(Succeed())

			requested, _ := schedulerJobTimestamps(fixture, job.ID())
			Expect(requested).To(BeTemporally(">", before))
		})

		It("does not request schedule when the algorithm can not run again", func() {
			before, _ := schedulerJobTimestamps(fixture, job.ID())

			Expect(schedule(context.Background(), job)).To(Succeed())

			requested, _ := schedulerJobTimestamps(fixture, job.ID())
			Expect(requested).To(Equal(before))
		})

		It("returns the error when requesting schedule fails", func() {
			fakeAlgorithm.ComputeReturns(db.InputMapping{}, true, true, nil)

			err := schedule(context.Background(), requestScheduleFailsJob{Job: job, err: disaster})
			Expect(err).To(Equal(fmt.Errorf("request schedule: %w", disaster)))
		})

		It("returns the error when saving the next input mapping fails", func() {
			err := schedule(context.Background(), saveNextInputMappingFailsJob{Job: job, err: disaster})
			Expect(err).To(Equal(fmt.Errorf("save next input mapping: %w", disaster)))
		})

		It("returns the error when getting the full next build inputs fails", func() {
			err := schedule(context.Background(), nextBuildInputsFailsJob{Job: job, err: disaster})
			Expect(err).To(Equal(fmt.Errorf("get next build inputs: %w", disaster)))
		})

		It("starts the pending builds for the job it was given", func() {
			fakeBuildStarter.TryStartPendingBuildsForJobReturns(false, disaster)

			Expect(schedule(context.Background(), job)).To(Equal(disaster))

			Expect(fakeBuildStarter.TryStartPendingBuildsForJobCallCount()).To(Equal(1))
			_, _, actualJob, actualInputs := fakeBuildStarter.TryStartPendingBuildsForJobArgsForCall(0)
			Expect(actualJob.Name()).To(Equal("some-job-1"))
			Expect(actualJob.Resources).To(Equal(db.SchedulerResources{{Name: "some-resource"}}))
			Expect(actualInputs).To(BeNil())
		})

		It("does not create a pending build or mark the job as having new inputs", func() {
			guardedJob := setHasNewInputsFailsJob{Job: job, err: disaster}

			Expect(schedule(context.Background(), guardedJob)).To(Succeed())

			Expect(schedulerPendingBuilds(job)).To(BeEmpty())
		})

		It("leaves the inputs undetermined when the algorithm can not resolve them", func() {
			fakeAlgorithm.ComputeReturns(db.InputMapping{}, false, false, nil)

			Expect(schedule(context.Background(), job)).To(Succeed())

			_, satisfiable, err := job.GetFullNextBuildInputs()
			Expect(err).NotTo(HaveOccurred())
			Expect(satisfiable).To(BeFalse())
			Expect(schedulerPendingBuilds(job)).To(BeEmpty())
			Expect(fakeBuildStarter.TryStartPendingBuildsForJobCallCount()).To(Equal(1))
		})
	})

	Describe("a job with resource inputs", func() {
		var (
			fixture  *schedulerDB
			scenario *dbtest.Scenario
			versions db.VersionsDB
			job      db.Job
		)

		BeforeEach(func() {
			fixture = useSchedulerDB()
			scenario = dbtest.Setup(
				fixture.Builder.WithTeam("scheduler-team"),
				fixture.Builder.WithPipeline(triggerPipeline),
				fixture.Builder.WithResourceVersions("resource-a", atc.Version{"ref": "v1"}),
				fixture.Builder.WithResourceVersions("resource-b", atc.Version{"ref": "v2"}),
				fixture.Builder.WithResourceVersions("resource-c", atc.Version{"ref": "v3"}),
			)
			versions = schedulerVersionsDB(fixture)
			job = scenario.Job("some-job")
		})

		firstOccurrenceOf := func(resourceName string, version atc.Version) db.InputResult {
			GinkgoHelper()
			return schedulerInputResult(versions, scenario.Resource(resourceName), version, true)
		}

		reoccurrenceOf := func(resourceName string, version atc.Version) db.InputResult {
			GinkgoHelper()
			return schedulerInputResult(versions, scenario.Resource(resourceName), version, false)
		}

		It("computes the job's persisted inputs", func() {
			fakeAlgorithm.ComputeReturns(db.InputMapping{}, true, false, nil)

			Expect(schedule(context.Background(), job)).To(Succeed())

			_, actualJob, actualInputs := fakeAlgorithm.ComputeArgsForCall(0)
			Expect(actualJob.Name()).To(Equal("some-job"))
			Expect(actualInputs).To(ConsistOf(
				db.InputConfig{Name: "a", ResourceID: scenario.Resource("resource-a").ID(), JobID: job.ID(), Trigger: true},
				db.InputConfig{Name: "b", ResourceID: scenario.Resource("resource-b").ID(), JobID: job.ID()},
				db.InputConfig{Name: "c", ResourceID: scenario.Resource("resource-c").ID(), JobID: job.ID(), Trigger: true},
			))
		})

		It("persists the computed input mapping", func() {
			fakeAlgorithm.ComputeReturns(db.InputMapping{
				"a": firstOccurrenceOf("resource-a", atc.Version{"ref": "v1"}),
				"b": reoccurrenceOf("resource-b", atc.Version{"ref": "v2"}),
			}, true, false, nil)

			Expect(schedule(context.Background(), job)).To(Succeed())

			buildInputs, satisfiable, err := job.GetFullNextBuildInputs()
			Expect(err).NotTo(HaveOccurred())
			Expect(satisfiable).To(BeTrue())

			persisted := map[string]db.BuildInput{}
			for _, input := range buildInputs {
				persisted[input.Name] = input
			}
			Expect(persisted).To(HaveLen(2))
			Expect(persisted["a"].Version).To(Equal(atc.Version{"ref": "v1"}))
			Expect(persisted["a"].ResourceID).To(Equal(scenario.Resource("resource-a").ID()))
			Expect(persisted["a"].FirstOccurrence).To(BeTrue())
			Expect(persisted["b"].Version).To(Equal(atc.Version{"ref": "v2"}))
			Expect(persisted["b"].ResourceID).To(Equal(scenario.Resource("resource-b").ID()))
			Expect(persisted["b"].FirstOccurrence).To(BeFalse())
		})

		It("creates a pending build for a first occurrence of a trigger input", func() {
			fakeAlgorithm.ComputeReturns(db.InputMapping{
				"a": firstOccurrenceOf("resource-a", atc.Version{"ref": "v1"}),
				"b": reoccurrenceOf("resource-b", atc.Version{"ref": "v2"}),
			}, true, false, nil)

			Expect(schedule(context.Background(), job)).To(Succeed())

			Expect(schedulerPendingBuilds(job)).To(HaveLen(1))
			Expect(schedulerJobHasNewInputs(job)).To(BeTrue())
		})

		It("returns the error when creating the pending build fails", func() {
			fakeAlgorithm.ComputeReturns(db.InputMapping{
				"a": firstOccurrenceOf("resource-a", atc.Version{"ref": "v1"}),
			}, true, false, nil)

			err := schedule(context.Background(), ensurePendingBuildFailsJob{Job: job, err: disaster})
			Expect(err).To(Equal(fmt.Errorf("ensure pending build exists: %w", disaster)))
		})

		It("marks new inputs without a pending build when no first occurrence triggers", func() {
			fakeAlgorithm.ComputeReturns(db.InputMapping{
				"a": reoccurrenceOf("resource-a", atc.Version{"ref": "v1"}),
				"b": firstOccurrenceOf("resource-b", atc.Version{"ref": "v2"}),
			}, true, false, nil)

			Expect(schedule(context.Background(), job)).To(Succeed())

			Expect(schedulerPendingBuilds(job)).To(BeEmpty())
			Expect(schedulerJobHasNewInputs(job)).To(BeTrue())
		})

		It("returns the error when marking the job as having new inputs fails", func() {
			fakeAlgorithm.ComputeReturns(db.InputMapping{
				"b": firstOccurrenceOf("resource-b", atc.Version{"ref": "v2"}),
			}, true, false, nil)

			err := schedule(context.Background(), setHasNewInputsFailsJob{Job: job, err: disaster})
			Expect(err).To(Equal(fmt.Errorf("set has new inputs: %w", disaster)))
		})

		It("does not mark the job again when it already has new inputs", func() {
			Expect(job.SetHasNewInputs(true)).To(Succeed())
			Expect(schedulerJobHasNewInputs(job)).To(BeTrue())

			fakeAlgorithm.ComputeReturns(db.InputMapping{
				"b": firstOccurrenceOf("resource-b", atc.Version{"ref": "v2"}),
			}, true, false, nil)

			err := schedule(context.Background(), setHasNewInputsFailsJob{Job: job, err: disaster})
			Expect(err).NotTo(HaveOccurred())
		})

		It("clears new inputs when nothing is a first occurrence any more", func() {
			Expect(job.SetHasNewInputs(true)).To(Succeed())
			Expect(schedulerJobHasNewInputs(job)).To(BeTrue())

			fakeAlgorithm.ComputeReturns(db.InputMapping{
				"a": reoccurrenceOf("resource-a", atc.Version{"ref": "v1"}),
				"b": reoccurrenceOf("resource-b", atc.Version{"ref": "v2"}),
			}, true, false, nil)

			Expect(schedule(context.Background(), job)).To(Succeed())

			Expect(schedulerJobHasNewInputs(job)).To(BeFalse())
		})

		It("does not clear new inputs the job never had", func() {
			fakeAlgorithm.ComputeReturns(db.InputMapping{
				"a": reoccurrenceOf("resource-a", atc.Version{"ref": "v1"}),
				"b": reoccurrenceOf("resource-b", atc.Version{"ref": "v2"}),
			}, true, false, nil)

			err := schedule(context.Background(), setHasNewInputsFailsJob{Job: job, err: disaster})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Describe("a job whose trigger inputs carry the span context of their check", func() {
		It("links the pending build to the scheduler span and follows the triggering check", func() {
			exporter := tracetest.NewInMemoryExporter()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
			tracing.ConfigureTraceProvider(tp)
			DeferCleanup(func() { tracing.Configured = false })

			fixture := useSchedulerDB()
			scenario := dbtest.Setup(
				fixture.Builder.WithTeam("scheduler-team"),
				fixture.Builder.WithPipeline(triggerPipeline),
				fixture.Builder.WithResourceVersions("resource-b", atc.Version{"ref": "v2"}),
			)

			checkCtxA, _ := tracing.StartSpan(context.Background(), "checker.Run", nil)
			scenario.SpanContext = db.NewSpanContext(checkCtxA)
			scenario.Run(fixture.Builder.WithResourceVersions("resource-a", atc.Version{"ref": "v1"}))

			checkCtxC, _ := tracing.StartSpan(context.Background(), "checker.Run", nil)
			scenario.SpanContext = db.NewSpanContext(checkCtxC)
			scenario.Run(fixture.Builder.WithResourceVersions("resource-c", atc.Version{"ref": "v3"}))

			versions := schedulerVersionsDB(fixture)
			job := scenario.Job("some-job")
			fakeAlgorithm.ComputeReturns(db.InputMapping{
				"a": schedulerInputResult(versions, scenario.Resource("resource-a"), atc.Version{"ref": "v1"}, true),
				"b": schedulerInputResult(versions, scenario.Resource("resource-b"), atc.Version{"ref": "v2"}, false),
				"c": schedulerInputResult(versions, scenario.Resource("resource-c"), atc.Version{"ref": "v3"}, true),
			}, true, false, nil)

			schedulerCtx, _ := tracing.StartSpan(context.Background(), "scheduler.Run", nil)
			Expect(schedule(schedulerCtx, job)).To(Succeed())

			Expect(schedulerPendingBuilds(job)).To(HaveLen(1))

			var pendingBuildSpans []tracetest.SpanStub
			for _, span := range exporter.GetSpans() {
				if span.Name == "job.EnsurePendingBuildExists" {
					pendingBuildSpans = append(pendingBuildSpans, span)
				}
			}
			Expect(pendingBuildSpans).To(HaveLen(1))
			Expect(pendingBuildSpans[0].Links).To(ConsistOf(sdktrace.Link{
				SpanContext: tracing.FromContext(schedulerCtx).SpanContext(),
			}))
			Expect(pendingBuildSpans[0].Parent.SpanID()).To(BeElementOf(
				tracing.FromContext(checkCtxA).SpanContext().SpanID(),
				tracing.FromContext(checkCtxC).SpanContext().SpanID(),
			))
		})
	})
})
