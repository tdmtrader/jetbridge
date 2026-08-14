package scheduler_test

import (
	"context"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/builds"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/scheduler"
	"github.com/concourse/concourse/atc/scheduler/algorithm"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var starterTaskJob = atc.JobConfig{
	Name: "task-job",
	PlanSequence: []atc.Step{
		{
			Config: &atc.TaskStep{
				Name:       "some-task",
				ConfigPath: "some/config/path.yml",
			},
		},
	},
}

var starterUnplannableJob = atc.Config{
	Jobs: atc.JobConfigs{
		{
			Name: "run-job",
			PlanSequence: []atc.Step{
				{Config: &atc.RunStep{Message: "hello", Type: "missing-prototype"}},
			},
		},
	},
}

func persistStarterJob(fixture *schedulerDB, config atc.Config, jobName string) db.Job {
	GinkgoHelper()

	_, pipeline := persistSchedulerPipeline(
		fixture,
		"starter-team",
		"starter-pipeline",
		config,
	)
	job := schedulerPipelineJob(pipeline, jobName)
	Expect(job.SaveNextInputMapping(nil, true)).To(Succeed())
	return job
}

func nextPendingBuild(job db.Job) db.Build {
	GinkgoHelper()

	Expect(job.EnsurePendingBuildExists(context.Background())).To(Succeed())
	pending, err := job.GetPendingBuilds()
	Expect(err).NotTo(HaveOccurred())
	Expect(pending).NotTo(BeEmpty())
	return pending[len(pending)-1]
}

func reloadStarterBuild(fixture *schedulerDB, buildID int) db.Build {
	GinkgoHelper()

	build, found, err := fixture.BuildFactory.Build(buildID)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return build
}

var _ = Describe("BuildStarter", func() {
	tryStart := func(fixture *schedulerDB, job db.SchedulerJob) (bool, error) {
		GinkgoHelper()

		inputs, err := job.AlgorithmInputs()
		Expect(err).NotTo(HaveOccurred())

		return scheduler.NewBuildStarter(
			builds.NewPlanner(atc.NewPlanFactory(0)),
			algorithm.New(schedulerVersionsDB(fixture)),
		).TryStartPendingBuildsForJob(
			context.Background(),
			lagertest.NewTestLogger("test"),
			job,
			inputs,
		)
	}

	expectPending := func(fixture *schedulerDB, build db.Build) {
		GinkgoHelper()

		reloaded := reloadStarterBuild(fixture, build.ID())
		Expect(reloaded.Status()).To(Equal(db.BuildStatusPending))
		Expect(reloaded.HasPlan()).To(BeFalse())
	}

	expectTaskPlan := func(fixture *schedulerDB, build db.Build, manuallyTriggered bool) {
		GinkgoHelper()

		reloaded := reloadStarterBuild(fixture, build.ID())
		Expect(reloaded.Status()).To(Equal(db.BuildStatusStarted))
		Expect(reloaded.IsScheduled()).To(BeTrue())

		persistedPlan := reloaded.PrivatePlan()
		Expect(persistedPlan.Do).NotTo(BeNil())
		Expect(*persistedPlan.Do).To(HaveLen(1))
		Expect((*persistedPlan.Do)[0].Task).NotTo(BeNil())
		Expect((*persistedPlan.Do)[0].Task.Name).To(Equal("some-task"))
		Expect((*persistedPlan.Do)[0].Task.ConfigPath).To(Equal("some/config/path.yml"))
		Expect((*persistedPlan.Do)[0].Task.CheckSkipInterval).To(Equal(manuallyTriggered))
	}

	Describe("an aborted pending build", func() {
		It("finishes it as aborted and continues to the next pending build", func() {
			fixture := useSchedulerDB()
			job := persistStarterJob(fixture, atc.Config{Jobs: atc.JobConfigs{starterTaskJob}}, "task-job")
			abortedBuild := nextPendingBuild(job)
			Expect(abortedBuild.MarkAsAborted()).To(Succeed())

			nextBuild, err := job.CreateBuild("test")
			Expect(err).NotTo(HaveOccurred())

			needsRetry, err := tryStart(fixture, schedulerJobToSchedule(fixture, job))
			Expect(err).NotTo(HaveOccurred())
			Expect(needsRetry).To(BeFalse())

			aborted := reloadStarterBuild(fixture, abortedBuild.ID())
			Expect(aborted.Status()).To(Equal(db.BuildStatusAborted))
			Expect(aborted.IsCompleted()).To(BeTrue())
			Expect(aborted.IsScheduled()).To(BeFalse())
			Expect(aborted.HasPlan()).To(BeFalse())
			expectTaskPlan(fixture, nextBuild, true)
		})
	})

	Describe("a manually triggered task build", func() {
		It("persists a task plan that skips the image check interval", func() {
			fixture := useSchedulerDB()
			job := persistStarterJob(fixture, atc.Config{Jobs: atc.JobConfigs{starterTaskJob}}, "task-job")
			manualBuild, err := job.CreateBuild("test")
			Expect(err).NotTo(HaveOccurred())
			Expect(manualBuild.IsManuallyTriggered()).To(BeTrue())

			needsRetry, err := tryStart(fixture, schedulerJobToSchedule(fixture, job))
			Expect(err).NotTo(HaveOccurred())
			Expect(needsRetry).To(BeFalse())

			expectTaskPlan(fixture, manualBuild, true)
		})
	})

	Describe("a manually triggered build whose resources have not been checked", func() {
		It("retries, then adopts a real checked version and starts", func() {
			fixture := useSchedulerDB()
			scenario := dbtest.Setup(
				fixture.Builder.WithTeam("starter-team"),
				fixture.Builder.WithPipeline(atc.Config{
					Resources: atc.ResourceConfigs{
						{Name: "some-resource", Type: dbtest.BaseResourceType, Source: atc.Source{"some": "source"}},
					},
					Jobs: atc.JobConfigs{
						{
							Name: "get-job",
							PlanSequence: []atc.Step{
								{Config: &atc.GetStep{Name: "some-input", Resource: "some-resource"}},
							},
						},
					},
				}),
				fixture.Builder.WithResourceVersions("some-resource", atc.Version{"ref": "v1"}),
			)
			job := scenario.Job("get-job")
			build, err := job.CreateBuild("test")
			Expect(err).NotTo(HaveOccurred())

			schedulerJob := schedulerJobToSchedule(fixture, job)
			algorithmInputs, err := schedulerJob.AlgorithmInputs()
			Expect(err).NotTo(HaveOccurred())
			Expect(algorithmInputs).To(HaveLen(1))
			Expect(algorithmInputs[0].Name).To(Equal("some-input"))
			Expect(algorithmInputs[0].ResourceID).To(Equal(scenario.Resource("some-resource").ID()))

			Expect(build.ResourcesChecked()).To(BeFalse())
			needsRetry, err := tryStart(fixture, schedulerJob)
			Expect(err).NotTo(HaveOccurred())
			Expect(needsRetry).To(BeTrue())

			blocked := reloadStarterBuild(fixture, build.ID())
			Expect(blocked.Status()).To(Equal(db.BuildStatusPending))
			Expect(blocked.IsScheduled()).To(BeTrue())
			Expect(blocked.HasPlan()).To(BeFalse())

			scenario.Run(fixture.Builder.WithResourceVersions("some-resource"))
			Expect(build.ResourcesChecked()).To(BeTrue())

			needsRetry, err = tryStart(fixture, schedulerJob)
			Expect(err).NotTo(HaveOccurred())
			Expect(needsRetry).To(BeFalse())

			started := reloadStarterBuild(fixture, build.ID())
			Expect(started.Status()).To(Equal(db.BuildStatusStarted))
			Expect(started.InputsReady()).To(BeTrue())
			Expect(started.PrivatePlan().Do).NotTo(BeNil())
			Expect(*started.PrivatePlan().Do).To(HaveLen(1))
			Expect((*started.PrivatePlan().Do)[0].Get).NotTo(BeNil())
			Expect((*started.PrivatePlan().Do)[0].Get.Name).To(Equal("some-input"))
			Expect((*started.PrivatePlan().Do)[0].Get.Version).NotTo(BeNil())
			Expect(*(*started.PrivatePlan().Do)[0].Get.Version).To(Equal(atc.Version{"ref": "v1"}))
		})
	})

	Describe("several pending builds", func() {
		var (
			fixture *schedulerDB
			job     db.Job

			schedulerBuild db.Build
			rerunBuild     db.Build
			manualBuild    db.Build
		)

		BeforeEach(func() {
			fixture = useSchedulerDB()
			job = persistStarterJob(fixture, atc.Config{Jobs: atc.JobConfigs{starterTaskJob}}, "task-job")
			schedulerBuild = nextPendingBuild(job)

			var err error
			rerunBuild, err = job.RerunBuild(schedulerBuild, "test")
			Expect(err).NotTo(HaveOccurred())
			manualBuild, err = job.CreateBuild("test")
			Expect(err).NotTo(HaveOccurred())

			Expect(buildIDs(schedulerPendingBuilds(job))).To(Equal(
				buildIDs([]db.Build{schedulerBuild, rerunBuild, manualBuild}),
			))
		})

		It("starts every kind with real adopted inputs and a persisted plan", func() {
			needsRetry, err := tryStart(fixture, schedulerJobToSchedule(fixture, job))
			Expect(err).NotTo(HaveOccurred())
			Expect(needsRetry).To(BeFalse())

			for _, build := range []db.Build{schedulerBuild, rerunBuild, manualBuild} {
				started := reloadStarterBuild(fixture, build.ID())
				Expect(started.Status()).To(Equal(db.BuildStatusStarted), "build %d", build.ID())
				Expect(started.IsScheduled()).To(BeTrue())
				Expect(started.InputsReady()).To(BeTrue())
				Expect(started.HasPlan()).To(BeTrue())
			}
			Expect(schedulerPendingBuilds(job)).To(BeEmpty())
		})

		It("finishes a build aborted after the scan and starts the remaining builds", func() {
			schedulerJob := schedulerJobToSchedule(fixture, job)
			schedulerJob.Job = wrappedPendingBuildsJob{
				Job: job,
				wrap: func(i int, build db.Build) db.Build {
					if i == 0 {
						Expect(build.MarkAsAborted()).To(Succeed())
					}
					return build
				},
			}

			needsRetry, err := tryStart(fixture, schedulerJob)
			Expect(err).NotTo(HaveOccurred())
			Expect(needsRetry).To(BeFalse())

			aborted := reloadStarterBuild(fixture, schedulerBuild.ID())
			Expect(aborted.Status()).To(Equal(db.BuildStatusAborted))
			Expect(aborted.IsCompleted()).To(BeTrue())
			Expect(aborted.HasPlan()).To(BeFalse())
			expectTaskPlan(fixture, rerunBuild, false)
			expectTaskPlan(fixture, manualBuild, true)
		})

		It("stops when the scheduler input mapping is undetermined", func() {
			Expect(job.SaveNextInputMapping(nil, false)).To(Succeed())

			needsRetry, err := tryStart(fixture, schedulerJobToSchedule(fixture, job))
			Expect(err).NotTo(HaveOccurred())
			Expect(needsRetry).To(BeFalse())

			for _, build := range []db.Build{schedulerBuild, rerunBuild, manualBuild} {
				expectPending(fixture, build)
			}
			Expect(reloadStarterBuild(fixture, schedulerBuild.ID()).IsScheduled()).To(BeTrue())
			Expect(reloadStarterBuild(fixture, rerunBuild.ID()).IsScheduled()).To(BeFalse())
			Expect(reloadStarterBuild(fixture, manualBuild.ID()).IsScheduled()).To(BeFalse())
		})
	})

	Describe("a rerun of a build that never determined its inputs", func() {
		It("leaves the stale rerun pending and continues to the scheduler build", func() {
			fixture := useSchedulerDB()
			job := persistStarterJob(fixture, atc.Config{Jobs: atc.JobConfigs{starterTaskJob}}, "task-job")

			originalBuild, err := job.CreateBuild("test")
			Expect(err).NotTo(HaveOccurred())
			Expect(originalBuild.Finish(db.BuildStatusFailed)).To(Succeed())
			Expect(originalBuild.InputsReady()).To(BeFalse())

			schedulerBuild := nextPendingBuild(job)
			rerunBuild, err := job.RerunBuild(originalBuild, "test")
			Expect(err).NotTo(HaveOccurred())

			needsRetry, err := tryStart(fixture, schedulerJobToSchedule(fixture, job))
			Expect(err).NotTo(HaveOccurred())
			Expect(needsRetry).To(BeFalse())

			expectPending(fixture, rerunBuild)
			Expect(reloadStarterBuild(fixture, rerunBuild.ID()).IsScheduled()).To(BeTrue())
			expectTaskPlan(fixture, schedulerBuild, false)
		})
	})

	Describe("a job that has reached max in flight", func() {
		It("asks to retry without scheduling builds queued behind the blocked one", func() {
			fixture := useSchedulerDB()
			serialJob := starterTaskJob
			serialJob.Name = "serial-job"
			serialJob.RawMaxInFlight = 1
			job := persistStarterJob(fixture, atc.Config{Jobs: atc.JobConfigs{serialJob}}, "serial-job")

			runningBuild := nextPendingBuild(job)
			needsRetry, err := tryStart(fixture, schedulerJobToSchedule(fixture, job))
			Expect(err).NotTo(HaveOccurred())
			Expect(needsRetry).To(BeFalse())
			expectTaskPlan(fixture, runningBuild, false)

			blockedBuild := nextPendingBuild(job)
			queuedBuild, err := job.CreateBuild("test")
			Expect(err).NotTo(HaveOccurred())

			needsRetry, err = tryStart(fixture, schedulerJobToSchedule(fixture, job))
			Expect(err).NotTo(HaveOccurred())
			Expect(needsRetry).To(BeTrue())

			for _, build := range []db.Build{blockedBuild, queuedBuild} {
				expectPending(fixture, build)
				Expect(reloadStarterBuild(fixture, build.ID()).IsScheduled()).To(BeFalse())
			}
		})
	})

	Describe("planning a build", func() {
		It("persists an automatic task plan that respects the image check interval", func() {
			fixture := useSchedulerDB()
			job := persistStarterJob(fixture, atc.Config{Jobs: atc.JobConfigs{starterTaskJob}}, "task-job")
			pendingBuild := nextPendingBuild(job)

			needsRetry, err := tryStart(fixture, schedulerJobToSchedule(fixture, job))
			Expect(err).NotTo(HaveOccurred())
			Expect(needsRetry).To(BeFalse())

			expectTaskPlan(fixture, pendingBuild, false)
		})

		It("finishes an unknown prototype build as errored without a private plan", func() {
			fixture := useSchedulerDB()
			job := persistStarterJob(fixture, starterUnplannableJob, "run-job")
			pendingBuild := nextPendingBuild(job)

			needsRetry, err := tryStart(fixture, schedulerJobToSchedule(fixture, job))
			Expect(err).NotTo(HaveOccurred())
			Expect(needsRetry).To(BeFalse())

			errored := reloadStarterBuild(fixture, pendingBuild.ID())
			Expect(errored.Status()).To(Equal(db.BuildStatusErrored))
			Expect(errored.IsCompleted()).To(BeTrue())
			Expect(errored.HasPlan()).To(BeFalse())
			Expect(errored.PrivatePlan()).To(Equal(atc.Plan{}))
		})
	})
})
