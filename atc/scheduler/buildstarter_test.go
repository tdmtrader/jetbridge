package scheduler_test

import (
	"context"
	"errors"
	"fmt"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/builds"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/scheduler"
	"github.com/concourse/concourse/atc/scheduler/schedulerfakes"

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
	Resources: atc.ResourceConfigs{
		{Name: "some-resource", Type: "some-type"},
	},
	Jobs: atc.JobConfigs{
		{
			Name: "get-job",
			PlanSequence: []atc.Step{
				{Config: &atc.GetStep{Name: "some-resource"}},
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
	var (
		fakePlanner   *schedulerfakes.FakeBuildPlanner
		fakeAlgorithm *schedulerfakes.FakeAlgorithm

		disaster error
	)

	BeforeEach(func() {
		fakePlanner = new(schedulerfakes.FakeBuildPlanner)
		fakeAlgorithm = new(schedulerfakes.FakeAlgorithm)

		disaster = errors.New("bad thing")
	})

	realStarter := func() scheduler.BuildStarter {
		return scheduler.NewBuildStarter(builds.NewPlanner(atc.NewPlanFactory(0)), fakeAlgorithm)
	}

	plannedStarter := func() scheduler.BuildStarter {
		return scheduler.NewBuildStarter(fakePlanner, fakeAlgorithm)
	}

	tryStart := func(starter scheduler.BuildStarter, job db.SchedulerJob, inputs db.InputConfigs) (bool, error) {
		return starter.TryStartPendingBuildsForJob(
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

	Describe("fetching the pending builds", func() {
		It("returns the error when the pending builds cannot be fetched", func() {
			fixture := useSchedulerDB()
			job := persistStarterJob(fixture, atc.Config{Jobs: atc.JobConfigs{starterTaskJob}}, "task-job")

			needsReschedule, err := tryStart(realStarter(), db.SchedulerJob{
				Job: pendingBuildsFailsJob{Job: job, err: disaster},
			}, db.InputConfigs{})
			Expect(err).To(Equal(fmt.Errorf("get pending builds: %w", disaster)))
			Expect(needsReschedule).To(BeFalse())
		})
	})

	Describe("an aborted pending build", func() {
		It("finishes it as aborted without planning it", func() {
			fixture := useSchedulerDB()
			job := persistStarterJob(fixture, atc.Config{Jobs: atc.JobConfigs{starterTaskJob}}, "task-job")
			pendingBuild := nextPendingBuild(job)
			Expect(pendingBuild.MarkAsAborted()).To(Succeed())

			needsReschedule, err := tryStart(realStarter(), db.SchedulerJob{Job: job}, db.InputConfigs{})
			Expect(err).NotTo(HaveOccurred())
			Expect(needsReschedule).To(BeFalse())

			abortedBuild := reloadStarterBuild(fixture, pendingBuild.ID())
			Expect(abortedBuild.Status()).To(Equal(db.BuildStatusAborted))
			Expect(abortedBuild.IsCompleted()).To(BeTrue())
			Expect(abortedBuild.IsScheduled()).To(BeFalse())
			Expect(abortedBuild.HasPlan()).To(BeFalse())
		})

		It("returns the error when finishing the aborted build fails", func() {
			fixture := useSchedulerDB()
			job := persistStarterJob(fixture, atc.Config{Jobs: atc.JobConfigs{starterTaskJob}}, "task-job")
			pendingBuild := nextPendingBuild(job)
			Expect(pendingBuild.MarkAsAborted()).To(Succeed())

			needsReschedule, err := tryStart(realStarter(), db.SchedulerJob{
				Job: wrappedPendingBuildsJob{
					Job: job,
					wrap: func(_ int, build db.Build) db.Build {
						return finishFailsBuild{Build: build, err: disaster}
					},
				},
			}, db.InputConfigs{})
			Expect(err).To(Equal(fmt.Errorf("finish aborted build: %w", disaster)))
			Expect(needsReschedule).To(BeFalse())

			expectPending(fixture, pendingBuild)
		})

		It("starts the next pending build after it", func() {
			fixture := useSchedulerDB()
			job := persistStarterJob(fixture, atc.Config{Jobs: atc.JobConfigs{starterTaskJob}}, "task-job")
			abortedBuild := nextPendingBuild(job)
			Expect(abortedBuild.MarkAsAborted()).To(Succeed())

			nextBuild, err := job.CreateBuild("test")
			Expect(err).NotTo(HaveOccurred())
			fakeAlgorithm.ComputeReturns(db.InputMapping{}, true, false, nil)

			_, err = tryStart(realStarter(), db.SchedulerJob{Job: job}, db.InputConfigs{})
			Expect(err).NotTo(HaveOccurred())

			Expect(reloadStarterBuild(fixture, abortedBuild.ID()).Status()).To(Equal(db.BuildStatusAborted))
			Expect(reloadStarterBuild(fixture, nextBuild.ID()).Status()).To(Equal(db.BuildStatusStarted))
		})
	})

	Describe("a manually triggered build", func() {
		var (
			fixture     *schedulerDB
			job         db.Job
			manualBuild db.Build
		)

		BeforeEach(func() {
			fixture = useSchedulerDB()
			job = persistStarterJob(fixture, atc.Config{Jobs: atc.JobConfigs{starterTaskJob}}, "task-job")

			var err error
			manualBuild, err = job.CreateBuild("test")
			Expect(err).NotTo(HaveOccurred())
			Expect(manualBuild.IsManuallyTriggered()).To(BeTrue())

			fakeAlgorithm.ComputeReturns(db.InputMapping{}, true, false, nil)
		})

		It("schedules it and starts it with a manually triggered plan", func() {
			needsReschedule, err := tryStart(plannedStarter(), db.SchedulerJob{Job: job}, db.InputConfigs{})
			Expect(err).NotTo(HaveOccurred())
			Expect(needsReschedule).To(BeFalse())

			Expect(fakePlanner.CreateCallCount()).To(Equal(1))
			_, _, _, _, _, manuallyTriggered := fakePlanner.CreateArgsForCall(0)
			Expect(manuallyTriggered).To(BeTrue())

			startedBuild := reloadStarterBuild(fixture, manualBuild.ID())
			Expect(startedBuild.Status()).To(Equal(db.BuildStatusStarted))
			Expect(startedBuild.IsScheduled()).To(BeTrue())
		})

		It("returns the error when scheduling the build fails", func() {
			needsReschedule, err := tryStart(realStarter(), db.SchedulerJob{
				Job: scheduleBuildFailsJob{Job: job, err: disaster},
			}, db.InputConfigs{})
			Expect(err).To(Equal(fmt.Errorf("schedule build: %w", disaster)))
			Expect(needsReschedule).To(BeFalse())

			expectPending(fixture, manualBuild)
		})

		It("returns the error when checking whether the resources were checked fails", func() {
			needsReschedule, err := tryStart(realStarter(), db.SchedulerJob{
				Job: wrappedPendingBuildsJob{
					Job: job,
					wrap: func(_ int, build db.Build) db.Build {
						return resourcesCheckedFailsBuild{Build: build, err: disaster}
					},
				},
			}, db.InputConfigs{})
			Expect(err).To(Equal(fmt.Errorf("ready to determine inputs: %w", disaster)))
			Expect(needsReschedule).To(BeFalse())
		})

		It("computes the inputs for the job it was given", func() {
			resources := db.SchedulerResources{{Name: "some-resource"}}
			jobInputs := db.InputConfigs{{Name: "input-1", ResourceID: 1}}

			schedulerJob := db.SchedulerJob{Job: job, Resources: resources}
			_, err := tryStart(realStarter(), schedulerJob, jobInputs)
			Expect(err).NotTo(HaveOccurred())

			Expect(fakeAlgorithm.ComputeCallCount()).To(Equal(1))
			_, actualJob, actualInputs := fakeAlgorithm.ComputeArgsForCall(0)
			Expect(actualJob).To(Equal(schedulerJob))
			Expect(actualInputs).To(Equal(jobInputs))
		})

		It("returns the error when computing the inputs fails", func() {
			fakeAlgorithm.ComputeReturns(nil, false, false, disaster)

			needsReschedule, err := tryStart(realStarter(), db.SchedulerJob{Job: job}, db.InputConfigs{})
			Expect(err).To(Equal(fmt.Errorf("get build inputs: %w", fmt.Errorf("compute inputs: %w", disaster))))
			Expect(needsReschedule).To(BeFalse())
		})

		It("requests schedule when the algorithm can run again", func() {
			fakeAlgorithm.ComputeReturns(db.InputMapping{}, true, true, nil)
			before, _ := schedulerJobTimestamps(fixture, job.ID())

			_, err := tryStart(realStarter(), db.SchedulerJob{Job: job}, db.InputConfigs{})
			Expect(err).NotTo(HaveOccurred())

			requested, _ := schedulerJobTimestamps(fixture, job.ID())
			Expect(requested).To(BeTemporally(">", before))
		})

		It("does not request schedule when the algorithm can not run again", func() {
			before, _ := schedulerJobTimestamps(fixture, job.ID())

			_, err := tryStart(realStarter(), db.SchedulerJob{Job: job}, db.InputConfigs{})
			Expect(err).NotTo(HaveOccurred())

			requested, _ := schedulerJobTimestamps(fixture, job.ID())
			Expect(requested).To(Equal(before))
		})

		It("returns the error when requesting schedule fails", func() {
			fakeAlgorithm.ComputeReturns(db.InputMapping{}, true, true, nil)

			needsReschedule, err := tryStart(realStarter(), db.SchedulerJob{
				Job: requestScheduleFailsJob{Job: job, err: disaster},
			}, db.InputConfigs{})
			Expect(err).To(Equal(fmt.Errorf("get build inputs: %w", fmt.Errorf("request schedule: %w", disaster))))
			Expect(needsReschedule).To(BeFalse())
		})

		It("returns the error when saving the next input mapping fails", func() {
			needsReschedule, err := tryStart(realStarter(), db.SchedulerJob{
				Job: saveNextInputMappingFailsJob{Job: job, err: disaster},
			}, db.InputConfigs{})
			Expect(err).To(Equal(fmt.Errorf("get build inputs: %w", fmt.Errorf("save next input mapping: %w", disaster))))
			Expect(needsReschedule).To(BeFalse())
		})

		It("returns the error when adopting the inputs and pipes fails", func() {
			needsReschedule, err := tryStart(realStarter(), db.SchedulerJob{
				Job: wrappedPendingBuildsJob{
					Job: job,
					wrap: func(_ int, build db.Build) db.Build {
						return adoptInputsFailsBuild{Build: build, err: disaster}
					},
				},
			}, db.InputConfigs{})
			Expect(err).To(Equal(fmt.Errorf("get build inputs: %w", fmt.Errorf("adopt inputs and pipes: %w", disaster))))
			Expect(needsReschedule).To(BeFalse())
		})

		It("leaves the build pending when the algorithm cannot resolve the inputs", func() {
			fakeAlgorithm.ComputeReturns(db.InputMapping{}, false, false, nil)

			needsReschedule, err := tryStart(realStarter(), db.SchedulerJob{Job: job}, db.InputConfigs{})
			Expect(err).NotTo(HaveOccurred())
			Expect(needsReschedule).To(BeFalse())

			blockedBuild := reloadStarterBuild(fixture, manualBuild.ID())
			Expect(blockedBuild.Status()).To(Equal(db.BuildStatusPending))
			Expect(blockedBuild.IsScheduled()).To(BeTrue())
			Expect(blockedBuild.HasPlan()).To(BeFalse())
		})
	})

	Describe("a manually triggered build whose resources have not been checked", func() {
		var (
			fixture  *schedulerDB
			scenario *dbtest.Scenario
			job      db.Job
			build    db.Build
		)

		BeforeEach(func() {
			fixture = useSchedulerDB()
			scenario = dbtest.Setup(
				fixture.Builder.WithTeam("starter-team"),
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
			job = scenario.Job("get-job")

			var err error
			build, err = job.CreateBuild("test")
			Expect(err).NotTo(HaveOccurred())

			fakeAlgorithm.ComputeReturns(db.InputMapping{}, true, false, nil)
		})

		It("asks to be rescheduled without determining any inputs", func() {
			Expect(build.ResourcesChecked()).To(BeFalse())

			needsReschedule, err := tryStart(realStarter(), db.SchedulerJob{Job: job}, db.InputConfigs{})
			Expect(err).NotTo(HaveOccurred())
			Expect(needsReschedule).To(BeTrue())

			Expect(fakeAlgorithm.ComputeCallCount()).To(BeZero())
			blockedBuild := reloadStarterBuild(fixture, build.ID())
			Expect(blockedBuild.Status()).To(Equal(db.BuildStatusPending))
			Expect(blockedBuild.HasPlan()).To(BeFalse())
		})

		It("determines the inputs once the resources have been checked again", func() {
			scenario.Run(fixture.Builder.WithResourceVersions("some-resource"))
			Expect(build.ResourcesChecked()).To(BeTrue())

			needsReschedule, err := tryStart(plannedStarter(), db.SchedulerJob{Job: job}, db.InputConfigs{})
			Expect(err).NotTo(HaveOccurred())
			Expect(needsReschedule).To(BeFalse())

			Expect(fakeAlgorithm.ComputeCallCount()).To(Equal(1))
			Expect(reloadStarterBuild(fixture, build.ID()).Status()).To(Equal(db.BuildStatusStarted))
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
			fakeAlgorithm.ComputeReturns(db.InputMapping{}, true, false, nil)
		})

		It("schedules and starts all of them", func() {
			needsReschedule, err := tryStart(realStarter(), db.SchedulerJob{Job: job}, db.InputConfigs{})
			Expect(err).NotTo(HaveOccurred())
			Expect(needsReschedule).To(BeFalse())

			for _, build := range []db.Build{schedulerBuild, rerunBuild, manualBuild} {
				started := reloadStarterBuild(fixture, build.ID())
				Expect(started.Status()).To(Equal(db.BuildStatusStarted), "build %d", build.ID())
				Expect(started.IsScheduled()).To(BeTrue())
				Expect(started.HasPlan()).To(BeTrue())
			}
			Expect(schedulerPendingBuilds(job)).To(BeEmpty())
		})

		It("adopts the inputs and pipes of every build", func() {
			_, err := tryStart(realStarter(), db.SchedulerJob{Job: job}, db.InputConfigs{})
			Expect(err).NotTo(HaveOccurred())

			for _, build := range []db.Build{schedulerBuild, rerunBuild, manualBuild} {
				Expect(reloadStarterBuild(fixture, build.ID()).InputsReady()).To(BeTrue(), "build %d", build.ID())
			}
		})

		It("stops at the first build it cannot mark as scheduled", func() {
			needsReschedule, err := tryStart(realStarter(), db.SchedulerJob{
				Job: scheduleBuildFailsJob{Job: job, err: disaster},
			}, db.InputConfigs{})
			Expect(err).To(Equal(fmt.Errorf("schedule build: %w", disaster)))
			Expect(needsReschedule).To(BeFalse())

			for _, build := range []db.Build{schedulerBuild, rerunBuild, manualBuild} {
				expectPending(fixture, build)
			}
		})

		It("keeps going after failing to create a plan and errors every build", func() {
			fakePlanner.CreateReturns(atc.Plan{}, disaster)

			needsReschedule, err := tryStart(plannedStarter(), db.SchedulerJob{Job: job}, db.InputConfigs{})
			Expect(err).NotTo(HaveOccurred())
			Expect(needsReschedule).To(BeFalse())

			Expect(fakePlanner.CreateCallCount()).To(Equal(3))
			for _, build := range []db.Build{schedulerBuild, rerunBuild, manualBuild} {
				errored := reloadStarterBuild(fixture, build.ID())
				Expect(errored.Status()).To(Equal(db.BuildStatusErrored), "build %d", build.ID())
				Expect(errored.HasPlan()).To(BeFalse())
			}
		})

		It("returns the error when marking a build as errored fails", func() {
			fakePlanner.CreateReturns(atc.Plan{}, disaster)

			needsReschedule, err := tryStart(plannedStarter(), db.SchedulerJob{
				Job: wrappedPendingBuildsJob{
					Job: job,
					wrap: func(i int, build db.Build) db.Build {
						if i == 0 {
							return finishFailsBuild{Build: build, err: disaster}
						}
						return build
					},
				},
			}, db.InputConfigs{})
			Expect(err).To(Equal(fmt.Errorf("finish build: %w", disaster)))
			Expect(needsReschedule).To(BeFalse())

			Expect(reloadStarterBuild(fixture, schedulerBuild.ID()).Status()).To(Equal(db.BuildStatusPending))
			expectPending(fixture, rerunBuild)
			expectPending(fixture, manualBuild)
		})

		It("returns the error when starting a build fails", func() {
			needsReschedule, err := tryStart(realStarter(), db.SchedulerJob{
				Job: wrappedPendingBuildsJob{
					Job: job,
					wrap: func(i int, build db.Build) db.Build {
						if i == 0 {
							return startFailsBuild{Build: build, err: disaster}
						}
						return build
					},
				},
			}, db.InputConfigs{})
			Expect(err).To(Equal(fmt.Errorf("start build: %w", disaster)))
			Expect(needsReschedule).To(BeFalse())

			expectPending(fixture, rerunBuild)
			expectPending(fixture, manualBuild)
		})

		It("finishes a build aborted after it was scanned and starts the rest", func() {
			needsReschedule, err := tryStart(realStarter(), db.SchedulerJob{
				Job: wrappedPendingBuildsJob{
					Job: job,
					wrap: func(i int, build db.Build) db.Build {
						if i == 0 {
							Expect(build.MarkAsAborted()).To(Succeed())
						}
						return build
					},
				},
			}, db.InputConfigs{})
			Expect(err).NotTo(HaveOccurred())
			Expect(needsReschedule).To(BeFalse())

			abortedBuild := reloadStarterBuild(fixture, schedulerBuild.ID())
			Expect(abortedBuild.Status()).To(Equal(db.BuildStatusAborted))
			Expect(abortedBuild.HasPlan()).To(BeFalse())
			Expect(reloadStarterBuild(fixture, manualBuild.ID()).Status()).To(Equal(db.BuildStatusStarted))
		})

		It("returns the error when a build aborted after it was scanned cannot be finished", func() {
			needsReschedule, err := tryStart(realStarter(), db.SchedulerJob{
				Job: wrappedPendingBuildsJob{
					Job: job,
					wrap: func(i int, build db.Build) db.Build {
						if i == 0 {
							Expect(build.MarkAsAborted()).To(Succeed())
							return finishFailsBuild{Build: build, err: disaster}
						}
						return build
					},
				},
			}, db.InputConfigs{})
			Expect(err).To(Equal(fmt.Errorf("finish build: %w", disaster)))
			Expect(needsReschedule).To(BeFalse())

			expectPending(fixture, manualBuild)
		})

		It("returns the error when adopting the rerun inputs and pipes fails", func() {
			needsReschedule, err := tryStart(realStarter(), db.SchedulerJob{
				Job: wrappedPendingBuildsJob{
					Job: job,
					wrap: func(i int, build db.Build) db.Build {
						if i == 1 {
							return adoptRerunInputsFailsBuild{Build: build, err: disaster}
						}
						return build
					},
				},
			}, db.InputConfigs{})
			Expect(err).To(Equal(fmt.Errorf("get build inputs: %w", fmt.Errorf("adopt rerun inputs and pipes: %w", disaster))))
			Expect(needsReschedule).To(BeFalse())

			Expect(reloadStarterBuild(fixture, schedulerBuild.ID()).Status()).To(Equal(db.BuildStatusStarted))
			expectPending(fixture, manualBuild)
		})

		It("stops scheduling when a scheduler build cannot determine its inputs", func() {
			Expect(job.SaveNextInputMapping(nil, false)).To(Succeed())

			needsReschedule, err := tryStart(realStarter(), db.SchedulerJob{Job: job}, db.InputConfigs{})
			Expect(err).NotTo(HaveOccurred())
			Expect(needsReschedule).To(BeFalse())

			for _, build := range []db.Build{schedulerBuild, rerunBuild, manualBuild} {
				expectPending(fixture, build)
			}
		})

		It("only computes the algorithm for the manually triggered build", func() {
			_, err := tryStart(realStarter(), db.SchedulerJob{Job: job}, db.InputConfigs{})
			Expect(err).NotTo(HaveOccurred())

			Expect(fakeAlgorithm.ComputeCallCount()).To(Equal(1))
		})
	})

	Describe("a rerun of a build that never determined its inputs", func() {
		It("continues on to the next pending build", func() {
			fixture := useSchedulerDB()
			job := persistStarterJob(fixture, atc.Config{Jobs: atc.JobConfigs{starterTaskJob}}, "task-job")

			originalBuild, err := job.CreateBuild("test")
			Expect(err).NotTo(HaveOccurred())
			Expect(originalBuild.Finish(db.BuildStatusFailed)).To(Succeed())
			Expect(originalBuild.InputsReady()).To(BeFalse())

			schedulerBuild := nextPendingBuild(job)
			rerunBuild, err := job.RerunBuild(originalBuild, "test")
			Expect(err).NotTo(HaveOccurred())

			Expect(schedulerPendingBuilds(job)).To(HaveLen(2))

			needsReschedule, err := tryStart(realStarter(), db.SchedulerJob{Job: job}, db.InputConfigs{})
			Expect(err).NotTo(HaveOccurred())
			Expect(needsReschedule).To(BeFalse())

			Expect(fakeAlgorithm.ComputeCallCount()).To(BeZero())
			expectPending(fixture, rerunBuild)
			Expect(reloadStarterBuild(fixture, schedulerBuild.ID()).Status()).To(Equal(db.BuildStatusStarted))
		})
	})

	Describe("a job that has reached max in flight", func() {
		It("leaves the build pending and asks to be rescheduled", func() {
			fixture := useSchedulerDB()
			serialJob := starterTaskJob
			serialJob.Name = "serial-job"
			serialJob.RawMaxInFlight = 1
			job := persistStarterJob(fixture, atc.Config{Jobs: atc.JobConfigs{serialJob}}, "serial-job")

			firstBuild := nextPendingBuild(job)
			_, err := tryStart(realStarter(), db.SchedulerJob{Job: job}, db.InputConfigs{})
			Expect(err).NotTo(HaveOccurred())
			Expect(reloadStarterBuild(fixture, firstBuild.ID()).Status()).To(Equal(db.BuildStatusStarted))

			secondBuild := nextPendingBuild(job)
			needsReschedule, err := tryStart(realStarter(), db.SchedulerJob{Job: job}, db.InputConfigs{})
			Expect(err).NotTo(HaveOccurred())
			Expect(needsReschedule).To(BeTrue())

			blockedBuild := reloadStarterBuild(fixture, secondBuild.ID())
			Expect(blockedBuild.Status()).To(Equal(db.BuildStatusPending))
			Expect(blockedBuild.IsScheduled()).To(BeFalse())
			Expect(blockedBuild.HasPlan()).To(BeFalse())
		})

		It("does not schedule the builds queued behind the blocked one", func() {
			fixture := useSchedulerDB()
			serialJob := starterTaskJob
			serialJob.Name = "serial-job"
			serialJob.RawMaxInFlight = 1
			job := persistStarterJob(fixture, atc.Config{Jobs: atc.JobConfigs{serialJob}}, "serial-job")
			fakeAlgorithm.ComputeReturns(db.InputMapping{}, true, false, nil)

			runningBuild := nextPendingBuild(job)
			_, err := tryStart(realStarter(), db.SchedulerJob{Job: job}, db.InputConfigs{})
			Expect(err).NotTo(HaveOccurred())
			Expect(reloadStarterBuild(fixture, runningBuild.ID()).Status()).To(Equal(db.BuildStatusStarted))

			blockedBuild := nextPendingBuild(job)
			queuedBuild, err := job.CreateBuild("test")
			Expect(err).NotTo(HaveOccurred())

			needsReschedule, err := tryStart(realStarter(), db.SchedulerJob{Job: job}, db.InputConfigs{})
			Expect(err).NotTo(HaveOccurred())
			Expect(needsReschedule).To(BeTrue())

			expectPending(fixture, blockedBuild)
			expectPending(fixture, queuedBuild)
		})
	})

	Describe("planning a build", func() {
		It("creates the plan from the job config and the scheduler job", func() {
			fixture := useSchedulerDB()
			job := persistStarterJob(fixture, atc.Config{Jobs: atc.JobConfigs{starterTaskJob}}, "task-job")
			pendingBuild := nextPendingBuild(job)

			resources := db.SchedulerResources{{Name: "some-resource"}}
			resourceTypes := atc.ResourceTypes{{Name: "some-resource-type"}}
			prototypes := atc.Prototypes{{Name: "some-prototype"}}

			_, err := tryStart(plannedStarter(), db.SchedulerJob{
				Job:           job,
				Resources:     resources,
				ResourceTypes: resourceTypes,
				Prototypes:    prototypes,
			}, db.InputConfigs{})
			Expect(err).NotTo(HaveOccurred())

			jobConfig, err := job.Config()
			Expect(err).NotTo(HaveOccurred())

			Expect(fakePlanner.CreateCallCount()).To(Equal(1))
			planConfig, actualResources, actualResourceTypes, actualPrototypes, actualInputs, manuallyTriggered := fakePlanner.CreateArgsForCall(0)
			Expect(planConfig).To(Equal(jobConfig.StepConfig()))
			Expect(actualResources).To(Equal(resources))
			Expect(actualResourceTypes).To(Equal(resourceTypes))
			Expect(actualPrototypes).To(Equal(prototypes))
			Expect(actualInputs).To(BeEmpty())
			Expect(manuallyTriggered).To(BeFalse())
			Expect(reloadStarterBuild(fixture, pendingBuild.ID()).Status()).To(Equal(db.BuildStatusStarted))
		})

		It("persists the plan it created onto the started build", func() {
			fixture := useSchedulerDB()
			job := persistStarterJob(fixture, atc.Config{Jobs: atc.JobConfigs{starterTaskJob}}, "task-job")
			pendingBuild := nextPendingBuild(job)

			needsReschedule, err := tryStart(realStarter(), db.SchedulerJob{Job: job}, db.InputConfigs{})
			Expect(err).NotTo(HaveOccurred())
			Expect(needsReschedule).To(BeFalse())

			startedBuild := reloadStarterBuild(fixture, pendingBuild.ID())
			Expect(startedBuild.Status()).To(Equal(db.BuildStatusStarted))
			Expect(startedBuild.IsScheduled()).To(BeTrue())
			Expect(startedBuild.HasPlan()).To(BeTrue())

			persistedPlan := startedBuild.PrivatePlan()
			Expect(persistedPlan.Do).NotTo(BeNil())
			Expect(*persistedPlan.Do).To(HaveLen(1))
			Expect((*persistedPlan.Do)[0].Task).NotTo(BeNil())
			Expect((*persistedPlan.Do)[0].Task.Name).To(Equal("some-task"))
			Expect((*persistedPlan.Do)[0].Task.ConfigPath).To(Equal("some/config/path.yml"))
		})

		It("marks the build as errored when the plan cannot be created", func() {
			fixture := useSchedulerDB()
			job := persistStarterJob(fixture, starterUnplannableJob, "get-job")
			pendingBuild := nextPendingBuild(job)

			needsReschedule, err := tryStart(realStarter(), db.SchedulerJob{
				Job:       job,
				Resources: db.SchedulerResources{{Name: "some-resource", Type: "some-type"}},
			}, db.InputConfigs{})
			Expect(err).NotTo(HaveOccurred())
			Expect(needsReschedule).To(BeFalse())

			erroredBuild := reloadStarterBuild(fixture, pendingBuild.ID())
			Expect(erroredBuild.Status()).To(Equal(db.BuildStatusErrored))
			Expect(erroredBuild.IsCompleted()).To(BeTrue())
			Expect(erroredBuild.HasPlan()).To(BeFalse())
		})

		It("starts a rerun build with its own persisted plan", func() {
			fixture := useSchedulerDB()
			job := persistStarterJob(fixture, atc.Config{Jobs: atc.JobConfigs{starterTaskJob}}, "task-job")
			originalBuild := nextPendingBuild(job)
			_, err := tryStart(realStarter(), db.SchedulerJob{Job: job}, db.InputConfigs{})
			Expect(err).NotTo(HaveOccurred())

			rerunBuild, err := job.RerunBuild(originalBuild, "test")
			Expect(err).NotTo(HaveOccurred())
			Expect(rerunBuild.RerunOf()).To(Equal(originalBuild.ID()))

			_, err = tryStart(realStarter(), db.SchedulerJob{Job: job}, db.InputConfigs{})
			Expect(err).NotTo(HaveOccurred())

			startedRerun := reloadStarterBuild(fixture, rerunBuild.ID())
			Expect(startedRerun.Status()).To(Equal(db.BuildStatusStarted))
			Expect(startedRerun.IsScheduled()).To(BeTrue())

			persistedPlan := startedRerun.PrivatePlan()
			Expect(persistedPlan.Do).NotTo(BeNil())
			Expect((*persistedPlan.Do)[0].Task.Name).To(Equal("some-task"))
		})
	})
})
