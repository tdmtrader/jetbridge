package scheduler_test

import (
	"context"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/builds"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/scheduler"
	"github.com/concourse/concourse/atc/scheduler/algorithm"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = DescribeTable("Job Scheduling",
	(Example).Run,

	Entry("one pending build that can be successfully started", Example{
		Job: DBJob{
			Builds: []DBBuild{
				{Kind: SchedulerBuild},
			},
		},

		Result: Result{
			StartedBuilds: []int{0},
			NeedsRetry:    false,
		},
	}),

	Entry("one pending build that is aborted", Example{
		Job: DBJob{
			Builds: []DBBuild{
				{Kind: SchedulerBuild, Aborted: true},
			},
		},

		Result: Result{
			StartedBuilds: []int{},
			NeedsRetry:    false,
		},
	}),

	Entry("one pending build that has reached max in flight", Example{
		Job: DBJob{
			MaxInFlightReached: true,
			Builds: []DBBuild{
				{Kind: SchedulerBuild},
			},
		},

		Result: Result{
			StartedBuilds: []int{},
			NeedsRetry:    true,
		},
	}),

	Entry("one manually triggered pending build that does not have resources checked", Example{
		Job: DBJob{
			ResourcesNotChecked: true,
			Builds: []DBBuild{
				{Kind: ManualBuild},
			},
		},

		Result: Result{
			StartedBuilds: []int{},
			NeedsRetry:    true,
		},
	}),

	Entry("one pending build that does not have inputs determined", Example{
		Job: DBJob{
			InputsUndetermined: true,
			Builds: []DBBuild{
				{Kind: SchedulerBuild},
			},
		},

		Result: Result{
			StartedBuilds: []int{},
			NeedsRetry:    false,
		},
	}),

	Entry("one pending build whose run step refers to a missing prototype", Example{
		Job: DBJob{
			UnknownPrototype: true,
			Builds: []DBBuild{
				{Kind: SchedulerBuild},
			},
		},

		Result: Result{
			StartedBuilds: []int{},
			NeedsRetry:    false,
		},
	}),

	Entry("one pending build that is unable to start", Example{
		Job: DBJob{
			Builds: []DBBuild{
				{Kind: SchedulerBuild, AbortedAfterScan: true},
			},
		},

		Result: Result{
			StartedBuilds: []int{},
			NeedsRetry:    false,
		},
	}),

	Entry("one rerun build, one scheduler build and one manually triggered build", Example{
		Job: DBJob{
			Builds: []DBBuild{
				{Kind: RerunBuild},
				{Kind: SchedulerBuild},
				{Kind: ManualBuild},
			},
		},

		Result: Result{
			StartedBuilds: []int{0, 1, 2},
			NeedsRetry:    false,
		},
	}),

	Entry("if a pending build is aborted, next build will continue to schedule", Example{
		Job: DBJob{
			Builds: []DBBuild{
				{Kind: SchedulerBuild, Aborted: true},
				{Kind: ManualBuild},
			},
		},

		Result: Result{
			StartedBuilds: []int{1},
			NeedsRetry:    false,
		},
	}),

	Entry("if the build after an aborted build has reached max in flight, it will not schedule", Example{
		Job: DBJob{
			MaxInFlightReached: true,
			Builds: []DBBuild{
				{Kind: SchedulerBuild, Aborted: true},
				{Kind: ManualBuild},
			},
		},

		Result: Result{
			StartedBuilds: []int{},
			NeedsRetry:    true,
		},
	}),

	Entry("if max in flight is reached, next builds will not schedule", Example{
		Job: DBJob{
			MaxInFlightReached: true,
			Builds: []DBBuild{
				{Kind: SchedulerBuild},
				{Kind: ManualBuild},
			},
		},

		Result: Result{
			StartedBuilds: []int{},
			NeedsRetry:    true,
		},
	}),

	Entry("if resources have not checked for a manually triggered build, next builds will not schedule", Example{
		Job: DBJob{
			ResourcesNotChecked: true,
			Builds: []DBBuild{
				{Kind: ManualBuild},
				{Kind: ManualBuild},
			},
		},

		Result: Result{
			StartedBuilds: []int{},
			NeedsRetry:    true,
		},
	}),

	Entry("if the rerun build has no inputs determined, the normal build will continue to get scheduled", Example{
		Job: DBJob{
			Builds: []DBBuild{
				{Kind: StaleRerunBuild},
				{Kind: SchedulerBuild},
			},
		},

		Result: Result{
			StartedBuilds: []int{1},
			NeedsRetry:    false,
		},
	}),

	Entry("if inputs are not determined on a regular build, next builds will not schedule", Example{
		Job: DBJob{
			InputsUndetermined: true,
			Builds: []DBBuild{
				{Kind: SchedulerBuild},
				{Kind: ManualBuild},
			},
		},

		Result: Result{
			StartedBuilds: []int{},
			NeedsRetry:    false,
		},
	}),

	Entry("if both rerun builds cannot determine inputs, next build will continue to schedule", Example{
		Job: DBJob{
			Builds: []DBBuild{
				{Kind: StaleRerunBuild},
				{Kind: StaleRerunBuild},
				{Kind: SchedulerBuild},
			},
		},

		Result: Result{
			StartedBuilds: []int{2},
			NeedsRetry:    false,
		},
	}),

	Entry("if regular build fails to schedule, next rerun build will not schedule", Example{
		Job: DBJob{
			InputsUndetermined: true,
			Builds: []DBBuild{
				{Kind: SchedulerBuild},
				{Kind: RerunBuild},
			},
		},

		Result: Result{
			StartedBuilds: []int{},
			NeedsRetry:    false,
		},
	}),
)

type BuildKind string

const (
	SchedulerBuild  BuildKind = "scheduler"
	RerunBuild      BuildKind = "rerun"
	StaleRerunBuild BuildKind = "stale-rerun"
	ManualBuild     BuildKind = "manual"
)

type Example struct {
	Job    DBJob
	Result Result
}

type DBJob struct {
	MaxInFlightReached  bool
	InputsUndetermined  bool
	ResourcesNotChecked bool
	UnknownPrototype    bool

	Builds []DBBuild
}

type DBBuild struct {
	Kind BuildKind

	Aborted          bool
	AbortedAfterScan bool
}

type Result struct {
	StartedBuilds []int
	NeedsRetry    bool
}

func (example Example) persistJob(fixture *schedulerDB) (db.Job, db.SchedulerResources) {
	GinkgoHelper()

	if example.Job.UnknownPrototype {
		return persistStarterJob(
			fixture,
			atc.Config{
				Jobs: atc.JobConfigs{
					{
						Name: "run-job",
						PlanSequence: []atc.Step{
							{Config: &atc.RunStep{Message: "hello", Type: "missing-prototype"}},
						},
					},
				},
			},
			"run-job",
		), nil
	}

	if example.Job.ResourcesNotChecked {
		scenario := dbtest.Setup(
			fixture.Builder.WithTeam("scheduling-team"),
			fixture.Builder.WithPipeline(atc.Config{
				Resources: atc.ResourceConfigs{
					{Name: "some-resource", Type: dbtest.BaseResourceType, Source: atc.Source{"some": "source"}},
				},
				Jobs: atc.JobConfigs{
					{
						Name: "get-job",
						PlanSequence: []atc.Step{
							{Config: &atc.GetStep{Name: "some-resource"}},
						},
					},
				},
			}),
			fixture.Builder.WithResourceVersions("some-resource", atc.Version{"ref": "v1"}),
		)
		job := scenario.Job("get-job")
		Expect(job.SaveNextInputMapping(nil, true)).To(Succeed())
		return job, db.SchedulerResources{{Name: "some-resource", Type: dbtest.BaseResourceType}}
	}

	jobConfig := starterTaskJob
	if example.Job.MaxInFlightReached {
		jobConfig.RawMaxInFlight = 1
	}
	return persistStarterJob(fixture, atc.Config{Jobs: atc.JobConfigs{jobConfig}}, jobConfig.Name), nil
}

// persistBuilds lays out the pending builds so that GetPendingBuilds scans them
// in the order the example declares. Reruns sort by the build they rerun, so the
// originals of any rerun declared ahead of the scheduler build have to exist
// before it, and the scheduler build itself can only be created while nothing
// else is pending.
func (example Example) persistBuilds(job db.Job) []db.Build {
	GinkgoHelper()

	created := make([]db.Build, len(example.Job.Builds))
	originals := make([]db.Build, len(example.Job.Builds))

	schedulerIndex := len(example.Job.Builds)
	for i, spec := range example.Job.Builds {
		if spec.Kind == SchedulerBuild {
			schedulerIndex = i
			break
		}
	}

	for i, spec := range example.Job.Builds {
		if i < schedulerIndex && isRerun(spec.Kind) {
			originals[i] = persistRerunOriginal(job, spec.Kind)
		}
	}

	if schedulerIndex < len(example.Job.Builds) {
		created[schedulerIndex] = nextPendingBuild(job)
	}

	for i, spec := range example.Job.Builds {
		switch {
		case i == schedulerIndex:
		case isRerun(spec.Kind):
			if originals[i] == nil {
				originals[i] = persistRerunOriginal(job, spec.Kind)
			}
			rerun, err := job.RerunBuild(originals[i], "test")
			Expect(err).NotTo(HaveOccurred())
			created[i] = rerun
		default:
			build, err := job.CreateBuild("test")
			Expect(err).NotTo(HaveOccurred())
			created[i] = build
		}
	}

	for i, spec := range example.Job.Builds {
		if spec.Aborted {
			Expect(created[i].MarkAsAborted()).To(Succeed())
		}
	}

	return created
}

func isRerun(kind BuildKind) bool {
	return kind == RerunBuild || kind == StaleRerunBuild
}

func persistRerunOriginal(job db.Job, kind BuildKind) db.Build {
	GinkgoHelper()

	original, err := job.CreateBuild("test")
	Expect(err).NotTo(HaveOccurred())

	if kind == RerunBuild {
		_, determined, err := original.AdoptInputsAndPipes()
		Expect(err).NotTo(HaveOccurred())
		Expect(determined).To(BeTrue())
	}

	Expect(original.Finish(db.BuildStatusSucceeded)).To(Succeed())
	return original
}

func buildIDs(builds []db.Build) []int {
	ids := make([]int, len(builds))
	for i, build := range builds {
		ids[i] = build.ID()
	}
	return ids
}

func (example Example) Run() {
	GinkgoHelper()

	fixture := useSchedulerDB()

	job, _ := example.persistJob(fixture)

	if example.Job.MaxInFlightReached {
		running, err := job.CreateBuild("test")
		Expect(err).NotTo(HaveOccurred())
		Expect(job.ScheduleBuild(running)).To(BeTrue())
		started, err := running.Start(atc.Plan{})
		Expect(err).NotTo(HaveOccurred())
		Expect(started).To(BeTrue())
	}

	created := example.persistBuilds(job)

	if example.Job.InputsUndetermined {
		Expect(job.SaveNextInputMapping(nil, false)).To(Succeed())
	}

	pending, err := job.GetPendingBuilds()
	Expect(err).NotTo(HaveOccurred())
	Expect(buildIDs(pending)).To(Equal(buildIDs(created)))

	abortAfterScan := map[int]struct{}{}
	for i, spec := range example.Job.Builds {
		if spec.AbortedAfterScan {
			abortAfterScan[created[i].ID()] = struct{}{}
		}
	}

	schedulerJob := schedulerJobToSchedule(fixture, job)
	if len(abortAfterScan) > 0 {
		schedulerJob.Job = abortAfterPendingBuildScanJob{
			Job:           job,
			abortBuildIDs: abortAfterScan,
		}
	}

	needsRetry, err := scheduler.NewBuildStarter(
		builds.NewPlanner(atc.NewPlanFactory(0)),
		algorithm.New(schedulerVersionsDB(fixture)),
	).TryStartPendingBuildsForJob(
		context.Background(),
		lager.NewLogger("job-scheduling-tests"),
		schedulerJob,
		db.InputConfigs{},
	)
	Expect(err).NotTo(HaveOccurred())
	Expect(needsRetry).To(Equal(example.Result.NeedsRetry))

	startedBuilds := []int{}
	for i, build := range created {
		if reloadStarterBuild(fixture, build.ID()).Status() == db.BuildStatusStarted {
			startedBuilds = append(startedBuilds, i)
		}
	}
	Expect(startedBuilds).To(Equal(example.Result.StartedBuilds))

	if example.Job.UnknownPrototype {
		erroredBuild := reloadStarterBuild(fixture, created[0].ID())
		Expect(erroredBuild.Status()).To(Equal(db.BuildStatusErrored))
		Expect(erroredBuild.IsCompleted()).To(BeTrue())
		Expect(erroredBuild.HasPlan()).To(BeFalse())
		Expect(erroredBuild.PrivatePlan()).To(Equal(atc.Plan{}))
	}

	for i, spec := range example.Job.Builds {
		if spec.Aborted || spec.AbortedAfterScan {
			abortedBuild := reloadStarterBuild(fixture, created[i].ID())
			Expect(abortedBuild.Status()).To(Equal(db.BuildStatusAborted))
			Expect(abortedBuild.IsCompleted()).To(BeTrue())
		}
	}
}
