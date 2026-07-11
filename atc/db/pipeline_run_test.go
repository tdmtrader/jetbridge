package db_test

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("PipelineRun completion", func() {
	var (
		factory  db.PipelineRunFactory
		run      db.PipelineRun
		instance db.Pipeline
	)

	config := atc.Config{
		Template: true,
		Jobs: atc.JobConfigs{
			{Name: "entry", PlanSequence: []atc.Step{
				{Config: &atc.TaskStep{Name: "t", ConfigPath: "task.yml"}},
			}},
			{Name: "second", PlanSequence: []atc.Step{
				{Config: &atc.TaskStep{Name: "t", ConfigPath: "task.yml"}},
			}},
		},
	}

	// the scheduler is not running in this suite; mark all instance jobs as
	// having been scheduled so the unscheduled-jobs completion guard passes
	markScheduled := func(pipelineID int) {
		_, err := dbConn.Exec(
			`UPDATE jobs SET last_scheduled = schedule_requested WHERE pipeline_id = $1`, pipelineID)
		Expect(err).ToNot(HaveOccurred())
	}

	finishBuild := func(jobName string, status db.BuildStatus) db.Build {
		job, found, err := instance.Job(jobName)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		build, err := job.CreateBuild("test")
		Expect(err).ToNot(HaveOccurred())
		Expect(build.Finish(status)).To(Succeed())
		return build
	}

	BeforeEach(func() {
		factory = db.NewPipelineRunFactory(logger, dbConn, lockFactory, checkFactory)

		template, _, err := defaultTeam.SavePipeline(
			atc.PipelineRef{Name: "completion-template"}, config, db.ConfigVersion(0), false)
		Expect(err).ToNot(HaveOccurred())

		run, err = factory.CreateRun(template.ID(), nil, "test")
		Expect(err).ToNot(HaveOccurred())

		var found bool
		instance, found, err = run.InstancePipeline()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
	})

	It("does not complete while entry builds are pending", func() {
		markScheduled(instance.ID())
		_, complete, err := run.CheckComplete()
		Expect(err).ToNot(HaveOccurred())
		Expect(complete).To(BeFalse())
	})

	It("does not complete while a build is started — the parked-run contract", func() {
		// a parked agent step (ask_human / checkpoint) keeps its build in
		// 'started'; a parked run must therefore stay 'running'
		instanceID := instance.ID()
		_, err := dbConn.Exec(
			`UPDATE builds SET status = 'started' WHERE pipeline_id = $1`, instanceID)
		Expect(err).ToNot(HaveOccurred())
		markScheduled(instanceID)

		_, complete, err := run.CheckComplete()
		Expect(err).ToNot(HaveOccurred())
		Expect(complete).To(BeFalse())
	})

	It("does not complete while a job still awaits scheduling", func() {
		// entry build finished but downstream schedule_requested advanced
		_, err := dbConn.Exec(
			`UPDATE builds SET status = 'succeeded' WHERE pipeline_id = $1`, instance.ID())
		Expect(err).ToNot(HaveOccurred())
		// deliberately do NOT markScheduled: jobs still have
		// schedule_requested > last_scheduled from savePipeline
		_, complete, err := run.CheckComplete()
		Expect(err).ToNot(HaveOccurred())
		Expect(complete).To(BeFalse())
	})

	It("completes with worst-of aggregate status", func() {
		_, err := dbConn.Exec(
			`UPDATE builds SET status = 'succeeded' WHERE pipeline_id = $1`, instance.ID())
		Expect(err).ToNot(HaveOccurred())
		finishBuild("second", db.BuildStatusFailed)
		markScheduled(instance.ID())

		status, complete, err := run.CheckComplete()
		Expect(err).ToNot(HaveOccurred())
		Expect(complete).To(BeTrue())
		Expect(status).To(Equal(db.PipelineRunFailed))

		// errored beats failed
		finishBuild("second", db.BuildStatusErrored)
		markScheduled(instance.ID())
		status, complete, err = run.CheckComplete()
		Expect(err).ToNot(HaveOccurred())
		Expect(complete).To(BeTrue())
		Expect(status).To(Equal(db.PipelineRunErrored))
	})

	It("completes aborted runs as aborted", func() {
		_, err := dbConn.Exec(
			`UPDATE builds SET status = 'aborted' WHERE pipeline_id = $1`, instance.ID())
		Expect(err).ToNot(HaveOccurred())
		markScheduled(instance.ID())

		status, complete, err := run.CheckComplete()
		Expect(err).ToNot(HaveOccurred())
		Expect(complete).To(BeTrue())
		Expect(status).To(Equal(db.PipelineRunAborted))
	})

	It("surfaces retriggers on completed runs and reopens them", func() {
		_, err := dbConn.Exec(
			`UPDATE builds SET status = 'succeeded' WHERE pipeline_id = $1`, instance.ID())
		Expect(err).ToNot(HaveOccurred())
		markScheduled(instance.ID())
		Expect(run.Finish(db.PipelineRunSucceeded)).To(Succeed())

		// a retrigger creates a new pending build
		job, _, err := instance.Job("entry")
		Expect(err).ToNot(HaveOccurred())
		_, err = job.CreateBuild("test")
		Expect(err).ToNot(HaveOccurred())

		reactivated, err := factory.CompletedRunsWithNewActivity()
		Expect(err).ToNot(HaveOccurred())
		Expect(reactivated).To(HaveLen(1))
		Expect(reactivated[0].ID()).To(Equal(run.ID()))

		Expect(reactivated[0].Reopen()).To(Succeed())
		Expect(reactivated[0].Status()).To(Equal(db.PipelineRunRunning))
		_, hasCompletedAt := reactivated[0].CompletedAt()
		Expect(hasCompletedAt).To(BeFalse())

		again, err := factory.CompletedRunsWithNewActivity()
		Expect(err).ToNot(HaveOccurred())
		Expect(again).To(BeEmpty())
	})

	// F26 (2026-07-09): the Finish notify fires only after a build has left
	// pending/started, so a retrigger that starts AND finishes inside one
	// 10s polling gap is invisible to the pending/started predicate — the
	// run would keep a stale terminal status forever. The widened predicate
	// also matches builds that COMPLETED after the run's completed_at, and
	// is self-terminating: reopen→re-complete stamps a newer completed_at.
	It("surfaces fast-finishing retriggers that never linger in pending or started", func() {
		_, err := dbConn.Exec(
			`UPDATE builds SET status = 'succeeded' WHERE pipeline_id = $1`, instance.ID())
		Expect(err).ToNot(HaveOccurred())
		markScheduled(instance.ID())
		Expect(run.Finish(db.PipelineRunSucceeded)).To(Succeed())

		// the retrigger is created AND finished before the lifecycler ever
		// observes it: by observation time nothing is pending/started, only
		// a completed build with end_time > the run's completed_at
		finishBuild("entry", db.BuildStatusFailed)

		reactivated, err := factory.CompletedRunsWithNewActivity()
		Expect(err).ToNot(HaveOccurred())
		Expect(reactivated).To(HaveLen(1))
		Expect(reactivated[0].ID()).To(Equal(run.ID()))

		// reopen → recompute → re-finish: the fresh completed_at is newer
		// than every build end_time, so the run stops matching (no loop)
		Expect(reactivated[0].Reopen()).To(Succeed())
		markScheduled(instance.ID())
		status, complete, err := reactivated[0].CheckComplete()
		Expect(err).ToNot(HaveOccurred())
		Expect(complete).To(BeTrue())
		Expect(status).To(Equal(db.PipelineRunFailed))
		Expect(reactivated[0].Finish(status)).To(Succeed())

		again, err := factory.CompletedRunsWithNewActivity()
		Expect(err).ToNot(HaveOccurred())
		Expect(again).To(BeEmpty())
	})
})
