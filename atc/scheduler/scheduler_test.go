package scheduler_test

import (
	"context"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/builds"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	. "github.com/concourse/concourse/atc/scheduler"
	"github.com/concourse/concourse/atc/scheduler/algorithm"
	"github.com/concourse/concourse/tracing"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	gocache "github.com/patrickmn/go-cache"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

var schedulerMappingPipeline = atc.Config{
	Resources: atc.ResourceConfigs{
		{Name: "resource-a", Type: dbtest.BaseResourceType, Source: atc.Source{"some": "a"}},
		{Name: "resource-b", Type: dbtest.BaseResourceType, Source: atc.Source{"some": "b"}},
	},
	Jobs: atc.JobConfigs{
		{
			Name: "some-job",
			PlanSequence: []atc.Step{
				{Config: &atc.GetStep{Name: "a", Resource: "resource-a", Trigger: true}},
				{Config: &atc.GetStep{Name: "b", Resource: "resource-b"}},
			},
		},
	},
}

// The three-resource pipeline is intentionally reserved for trace-link coverage.
// Mapping and has-new-inputs scenarios use schedulerMappingPipeline so a third
// trigger cannot silently change their persisted state.
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

func schedulerJobToSchedule(fixture *schedulerDB, job db.Job) db.SchedulerJob {
	GinkgoHelper()

	Expect(job.RequestSchedule()).To(Succeed())
	jobs, err := fixture.JobFactory.JobsToScheduleByIDs([]int{job.ID()})
	Expect(err).NotTo(HaveOccurred())
	Expect(jobs).To(HaveLen(1))
	return jobs[0]
}

func schedulerCompleteInputHistory(
	fixture *schedulerDB,
	scenario *dbtest.Scenario,
	jobName string,
	inputs dbtest.JobInputs,
) db.Build {
	GinkgoHelper()

	var historicalBuild db.Build
	scenario.Run(fixture.Builder.WithJobBuild(&historicalBuild, jobName, inputs, nil))
	Expect(historicalBuild.Finish(db.BuildStatusSucceeded)).To(Succeed())
	return historicalBuild
}

func schedulerPersistedInputs(job db.Job) map[string]db.BuildInput {
	GinkgoHelper()

	buildInputs, resolved, err := job.GetFullNextBuildInputs()
	Expect(err).NotTo(HaveOccurred())
	Expect(resolved).To(BeTrue())

	persisted := map[string]db.BuildInput{}
	for _, input := range buildInputs {
		persisted[input.Name] = input
	}
	return persisted
}

var _ = Describe("Scheduler", func() {
	newScheduler := func(fixture *schedulerDB) *Scheduler {
		return NewScheduler(
			builds.NewPlanner(atc.NewPlanFactory(0)),
			algorithm.New(schedulerVersionsDB(fixture)),
		)
	}

	schedule := func(ctx context.Context, fixture *schedulerDB, job db.SchedulerJob) bool {
		GinkgoHelper()

		needsRetry, err := newScheduler(fixture).Schedule(
			ctx,
			lagertest.NewTestLogger("test"),
			job,
		)
		Expect(err).NotTo(HaveOccurred())
		return needsRetry
	}

	Describe("a job with no configured inputs", func() {
		It("persists a resolved empty mapping without creating a build or scheduling again", func() {
			fixture := useSchedulerDB()
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
			job := schedulerPipelineJob(pipeline, "some-job-1")
			schedulerJob := schedulerJobToSchedule(fixture, job)
			requestedBefore, _ := schedulerJobTimestamps(fixture, job.ID())

			Expect(schedule(context.Background(), fixture, schedulerJob)).To(BeFalse())

			buildInputs, resolved, err := job.GetFullNextBuildInputs()
			Expect(err).NotTo(HaveOccurred())
			Expect(resolved).To(BeTrue())
			Expect(buildInputs).To(BeEmpty())
			Expect(schedulerPendingBuilds(job)).To(BeEmpty())
			Expect(schedulerJobHasNewInputs(job)).To(BeFalse())

			apiBuilds, _, err := job.BuildsWithTime(db.Page{Limit: 50})
			Expect(err).NotTo(HaveOccurred())
			Expect(apiBuilds).To(BeEmpty())

			requestedAfter, _ := schedulerJobTimestamps(fixture, job.ID())
			Expect(requestedAfter).To(Equal(requestedBefore))
		})
	})

	Describe("a job with resource inputs", func() {
		It("persists a first-occurrence trigger and a previously used non-trigger input", func() {
			fixture := useSchedulerDB()
			scenario := dbtest.Setup(
				fixture.Builder.WithTeam("scheduler-team"),
				fixture.Builder.WithPipeline(schedulerMappingPipeline),
				fixture.Builder.WithResourceVersions(
					"resource-a",
					atc.Version{"ref": "v0"},
					atc.Version{"ref": "v1"},
				),
				fixture.Builder.WithResourceVersions("resource-b", atc.Version{"ref": "v2"}),
			)
			job := scenario.Job("some-job")
			schedulerCompleteInputHistory(fixture, scenario, "some-job", dbtest.JobInputs{
				{Name: "a", Version: atc.Version{"ref": "v0"}},
				{Name: "b", Version: atc.Version{"ref": "v2"}},
			})

			Expect(schedule(context.Background(), fixture, schedulerJobToSchedule(fixture, job))).To(BeFalse())

			persisted := schedulerPersistedInputs(job)
			Expect(persisted).To(HaveLen(2))
			Expect(persisted["a"].Version).To(Equal(atc.Version{"ref": "v1"}))
			Expect(persisted["a"].ResourceID).To(Equal(scenario.Resource("resource-a").ID()))
			Expect(persisted["a"].FirstOccurrence).To(BeTrue())
			Expect(persisted["b"].Version).To(Equal(atc.Version{"ref": "v2"}))
			Expect(persisted["b"].ResourceID).To(Equal(scenario.Resource("resource-b").ID()))
			Expect(persisted["b"].FirstOccurrence).To(BeFalse())
		})

		It("starts and plans the pending build for a first occurrence of a trigger input", func() {
			fixture := useSchedulerDB()
			scenario := dbtest.Setup(
				fixture.Builder.WithTeam("scheduler-team"),
				fixture.Builder.WithPipeline(schedulerMappingPipeline),
				fixture.Builder.WithResourceVersions("resource-a", atc.Version{"ref": "v1"}),
				fixture.Builder.WithResourceVersions("resource-b", atc.Version{"ref": "v2"}),
			)
			job := scenario.Job("some-job")

			Expect(schedule(context.Background(), fixture, schedulerJobToSchedule(fixture, job))).To(BeFalse())

			Expect(schedulerPendingBuilds(job)).To(BeEmpty())
			Expect(schedulerJobHasNewInputs(job)).To(BeTrue())

			apiBuilds, _, err := job.BuildsWithTime(db.Page{Limit: 50})
			Expect(err).NotTo(HaveOccurred())
			Expect(apiBuilds).To(HaveLen(1))
			Expect(apiBuilds[0].Status()).To(Equal(db.BuildStatusStarted))
			Expect(apiBuilds[0].HasPlan()).To(BeTrue())

			persistedBuild, found, err := fixture.BuildFactory.Build(apiBuilds[0].ID())
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(persistedBuild.PrivatePlan()).NotTo(Equal(atc.Plan{}))
		})

		It("marks a new non-trigger input without creating a build", func() {
			fixture := useSchedulerDB()
			scenario := dbtest.Setup(
				fixture.Builder.WithTeam("scheduler-team"),
				fixture.Builder.WithPipeline(schedulerMappingPipeline),
				fixture.Builder.WithResourceVersions("resource-a", atc.Version{"ref": "v1"}),
				fixture.Builder.WithResourceVersions(
					"resource-b",
					atc.Version{"ref": "v1"},
					atc.Version{"ref": "v2"},
				),
			)
			job := scenario.Job("some-job")
			historicalBuild := schedulerCompleteInputHistory(fixture, scenario, "some-job", dbtest.JobInputs{
				{Name: "a", Version: atc.Version{"ref": "v1"}},
				{Name: "b", Version: atc.Version{"ref": "v1"}},
			})

			Expect(schedule(context.Background(), fixture, schedulerJobToSchedule(fixture, job))).To(BeFalse())

			persisted := schedulerPersistedInputs(job)
			Expect(persisted).To(HaveLen(2))
			Expect(persisted["a"].FirstOccurrence).To(BeFalse())
			Expect(persisted["b"].Version).To(Equal(atc.Version{"ref": "v2"}))
			Expect(persisted["b"].FirstOccurrence).To(BeTrue())
			Expect(schedulerPendingBuilds(job)).To(BeEmpty())
			Expect(schedulerJobHasNewInputs(job)).To(BeTrue())

			apiBuilds, _, err := job.BuildsWithTime(db.Page{Limit: 50})
			Expect(err).NotTo(HaveOccurred())
			Expect(apiBuilds).To(HaveLen(1))
			Expect(apiBuilds[0].ID()).To(Equal(historicalBuild.ID()))
		})

		It("clears has-new-inputs when every selected version was used by a completed build", func() {
			fixture := useSchedulerDB()
			scenario := dbtest.Setup(
				fixture.Builder.WithTeam("scheduler-team"),
				fixture.Builder.WithPipeline(schedulerMappingPipeline),
				fixture.Builder.WithResourceVersions("resource-a", atc.Version{"ref": "v1"}),
				fixture.Builder.WithResourceVersions("resource-b", atc.Version{"ref": "v2"}),
			)
			job := scenario.Job("some-job")
			historicalBuild := schedulerCompleteInputHistory(fixture, scenario, "some-job", dbtest.JobInputs{
				{Name: "a", Version: atc.Version{"ref": "v1"}},
				{Name: "b", Version: atc.Version{"ref": "v2"}},
			})
			Expect(job.SetHasNewInputs(true)).To(Succeed())
			Expect(schedulerJobHasNewInputs(job)).To(BeTrue())

			Expect(schedule(context.Background(), fixture, schedulerJobToSchedule(fixture, job))).To(BeFalse())

			persisted := schedulerPersistedInputs(job)
			Expect(persisted).To(HaveLen(2))
			Expect(persisted["a"].FirstOccurrence).To(BeFalse())
			Expect(persisted["b"].FirstOccurrence).To(BeFalse())
			Expect(schedulerPendingBuilds(job)).To(BeEmpty())
			Expect(schedulerJobHasNewInputs(job)).To(BeFalse())

			apiBuilds, _, err := job.BuildsWithTime(db.Page{Limit: 50})
			Expect(err).NotTo(HaveOccurred())
			Expect(apiBuilds).To(HaveLen(1))
			Expect(apiBuilds[0].ID()).To(Equal(historicalBuild.ID()))
		})

		It("persists an unsatisfiable mapping when a resource has no version", func() {
			fixture := useSchedulerDB()
			scenario := dbtest.Setup(
				fixture.Builder.WithTeam("scheduler-team"),
				fixture.Builder.WithPipeline(schedulerMappingPipeline),
				fixture.Builder.WithResourceVersions("resource-a", atc.Version{"ref": "v1"}),
			)
			job := scenario.Job("some-job")

			Expect(schedule(context.Background(), fixture, schedulerJobToSchedule(fixture, job))).To(BeFalse())

			buildInputs, resolved, err := job.GetFullNextBuildInputs()
			Expect(err).NotTo(HaveOccurred())
			Expect(resolved).To(BeFalse())
			Expect(buildInputs).To(BeNil())

			persistedInputs, err := job.GetNextBuildInputs()
			Expect(err).NotTo(HaveOccurred())
			persisted := map[string]db.BuildInput{}
			for _, input := range persistedInputs {
				persisted[input.Name] = input
			}
			Expect(persisted).To(HaveLen(2))
			Expect(persisted["a"].Version).To(Equal(atc.Version{"ref": "v1"}))
			Expect(persisted["b"].ResolveError).To(Equal(string(db.LatestVersionNotFound)))
			Expect(schedulerPendingBuilds(job)).To(BeEmpty())
			Expect(schedulerJobHasNewInputs(job)).To(BeFalse())

			apiBuilds, _, err := job.BuildsWithTime(db.Page{Limit: 50})
			Expect(err).NotTo(HaveOccurred())
			Expect(apiBuilds).To(BeEmpty())
		})
	})

	Describe("a job that consumes every resource version", func() {
		It("requests another schedule when two versions remain after its historical input", func() {
			fixture := useSchedulerDB()
			config := atc.Config{
				Resources: atc.ResourceConfigs{
					{Name: "some-resource", Type: dbtest.BaseResourceType, Source: atc.Source{"some": "source"}},
				},
				Jobs: atc.JobConfigs{
					{
						Name: "every-job",
						PlanSequence: []atc.Step{
							{Config: &atc.GetStep{
								Name:     "some-input",
								Resource: "some-resource",
								Version:  &atc.VersionConfig{Every: true},
							}},
						},
					},
				},
			}
			scenario := dbtest.Setup(
				fixture.Builder.WithTeam("scheduler-team"),
				fixture.Builder.WithPipeline(config),
				fixture.Builder.WithResourceVersions("some-resource", atc.Version{"ref": "v1"}),
			)
			job := scenario.Job("every-job")
			schedulerCompleteInputHistory(fixture, scenario, "every-job", dbtest.JobInputs{
				{Name: "some-input", Version: atc.Version{"ref": "v1"}},
			})
			scenario.Run(fixture.Builder.WithResourceVersions(
				"some-resource",
				atc.Version{"ref": "v2"},
				atc.Version{"ref": "v3"},
			))
			schedulerJob := schedulerJobToSchedule(fixture, job)
			requestedBefore, _ := schedulerJobTimestamps(fixture, job.ID())

			Expect(schedule(context.Background(), fixture, schedulerJob)).To(BeFalse())

			persisted := schedulerPersistedInputs(job)
			Expect(persisted).To(HaveLen(1))
			Expect(persisted["some-input"].Version).To(Equal(atc.Version{"ref": "v2"}))
			requestedAfter, _ := schedulerJobTimestamps(fixture, job.ID())
			Expect(requestedAfter).To(BeTemporally(">", requestedBefore))
		})
	})

	Describe("a job whose trigger inputs carry the span context of their check", func() {
		It("links the created build to the scheduler span and follows the triggering check", func() {
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

			job := scenario.Job("some-job")
			schedulerCtx, _ := tracing.StartSpan(context.Background(), "scheduler.Run", nil)
			Expect(schedule(schedulerCtx, fixture, schedulerJobToSchedule(fixture, job))).To(BeFalse())

			Expect(schedulerPendingBuilds(job)).To(BeEmpty())
			apiBuilds, _, err := job.BuildsWithTime(db.Page{Limit: 50})
			Expect(err).NotTo(HaveOccurred())
			Expect(apiBuilds).To(HaveLen(1))
			Expect(apiBuilds[0].Status()).To(Equal(db.BuildStatusStarted))
			Expect(apiBuilds[0].HasPlan()).To(BeTrue())

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
