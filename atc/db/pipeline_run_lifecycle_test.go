package db_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type runLifecycleFixture struct {
	factory  db.PipelineRunFactory
	template db.Pipeline
	run      db.PipelineRun
	payload  db.Pipeline
	jobs     map[string]db.Job
}

func createRunLifecycleFixture(config atc.Config) runLifecycleFixture {
	GinkgoHelper()

	template, _, err := defaultTeam.SavePipeline(
		atc.PipelineRef{Name: "lifecycle-template"},
		config,
		0,
		false,
	)
	Expect(err).NotTo(HaveOccurred())

	factory := db.NewPipelineRunFactory(dbConn, lockFactory)
	creation, err := factory.CreateRun(context.Background(), template, db.RunParams{}, "creator")
	Expect(err).NotTo(HaveOccurred())
	payload, found, err := defaultTeam.Pipeline(atc.PipelineRef{
		Name:         template.Name(),
		InstanceVars: atc.InstanceVars{"run": float64(creation.Run.Number())},
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())

	jobs := map[string]db.Job{}
	for _, config := range config.Jobs {
		job, found, err := payload.Job(config.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		jobs[config.Name] = job
	}

	return runLifecycleFixture{
		factory: factory, template: template, run: creation.Run, payload: payload, jobs: jobs,
	}
}

// expectTerminalRefusal asserts the refusal a settled run answers every new
// build with. The type is the assertion that matters: errormap classifies it
// into a 409 by type, so an untyped refusal reaches a manual trigger as a 500.
func expectTerminalRefusal(err error, number int, status atc.RunStatus) {
	GinkgoHelper()
	var terminal db.ErrPipelineRunTerminal
	Expect(errors.As(err, &terminal)).To(BeTrue(), "refusal must be typed, got %v", err)
	Expect(terminal.Number).To(Equal(number))
	Expect(terminal.Status).To(Equal(status))
	Expect(terminal.Error()).To(ContainSubstring("run the template again"))
}

// runSettlement is the whole of what a refused build must leave alone: a run
// that stays settled at the same status, at the same instant, with the same
// builds under it.
type runSettlement struct {
	status      atc.RunStatus
	completedAt *time.Time
	builds      int
}

func (fixture runLifecycleFixture) settlement() runSettlement {
	GinkgoHelper()
	run := fixture.reloadRun()
	var builds int
	Expect(dbConn.QueryRow(`SELECT count(*) FROM builds WHERE pipeline_run_id = $1`, fixture.run.ID()).Scan(&builds)).To(Succeed())
	return runSettlement{status: run.Status(), completedAt: run.CompletedAt(), builds: builds}
}

func (fixture runLifecycleFixture) reloadRun() db.PipelineRun {
	GinkgoHelper()
	run, found, err := fixture.factory.GetRun(fixture.template, fixture.run.Number())
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return run
}

func openRunLifecycleConn() db.DbConn {
	GinkgoHelper()
	conn := postgresRunner.OpenConn()
	DeferCleanup(func() { Expect(conn.Close()).To(Succeed()) })
	return conn
}

func loadRunLifecycleBuild(conn db.DbConn, buildID int) db.Build {
	GinkgoHelper()
	build, found, err := db.NewBuildFactory(conn, lockFactory, 0, 0).Build(buildID)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return build
}

func (fixture runLifecycleFixture) loadPayload(conn db.DbConn) db.Pipeline {
	GinkgoHelper()
	team, found, err := db.NewTeamFactory(conn, lockFactory).FindTeam(fixture.payload.TeamName())
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	payload, found, err := team.Pipeline(fixture.payload.PipelineRef())
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return payload
}

func pendingRunBuild(job db.Job) db.Build {
	GinkgoHelper()
	builds, err := job.GetPendingBuilds()
	Expect(err).NotTo(HaveOccurred())
	Expect(builds).To(HaveLen(1))
	return builds[0]
}

func consumeObservedSchedule(job db.Job) {
	GinkgoHelper()
	found, err := job.Reload()
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	Expect(job.ConsumeScheduleRequest(job.ScheduleRequestedTime())).To(Succeed())
}

func requestOutstandingSchedule(job db.Job) {
	GinkgoHelper()
	found, err := job.Reload()
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	observed := job.ScheduleRequestedTime()
	Eventually(func() time.Time {
		Expect(job.RequestSchedule()).To(Succeed())
		var requested time.Time
		Expect(dbConn.QueryRow("SELECT schedule_requested FROM jobs WHERE id = $1", job.ID()).Scan(&requested)).To(Succeed())
		return requested
	}).Should(BeTemporally(">", observed))
}

func basicRunConfig(names ...string) atc.Config {
	jobs := make(atc.JobConfigs, 0, len(names))
	for _, name := range names {
		jobs = append(jobs, atc.JobConfig{Name: name})
	}
	return atc.Config{Template: true, Jobs: jobs}
}

func downstreamRunConfig(names ...string) atc.Config {
	jobs := atc.JobConfigs{{Name: "entry", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "source"}}}}}
	for _, name := range names {
		jobs = append(jobs, atc.JobConfig{
			Name: name,
			PlanSequence: []atc.Step{{Config: &atc.GetStep{
				Name: "source", Passed: []string{"entry"}, Trigger: true,
			}}},
		})
	}
	return atc.Config{
		Template:  true,
		Resources: atc.ResourceConfigs{{Name: "source", Type: "some-base-resource-type", Source: atc.Source{"repository": "example"}}},
		Jobs:      jobs,
	}
}

var _ = Describe("Pipeline run lifecycle", func() {
	It("keeps a run running while a stamped build is pending or started", func() {
		fixture := createRunLifecycleFixture(basicRunConfig("entry"))
		entry := fixture.jobs["entry"]
		build := pendingRunBuild(entry)

		consumeObservedSchedule(entry)
		Expect(fixture.reloadRun().Status()).To(Equal(atc.RunStatusRunning))

		started, err := build.Start(atc.Plan{})
		Expect(err).NotTo(HaveOccurred())
		Expect(started).To(BeTrue())
		consumeObservedSchedule(entry)
		Expect(fixture.reloadRun().Status()).To(Equal(atc.RunStatusRunning))
	})

	It("keeps zero-build corruption running", func() {
		fixture := createRunLifecycleFixture(basicRunConfig("entry"))
		_, err := dbConn.Exec("DELETE FROM builds WHERE pipeline_run_id = $1", fixture.run.ID())
		Expect(err).NotTo(HaveOccurred())

		consumeObservedSchedule(fixture.jobs["entry"])
		Expect(fixture.reloadRun().Status()).To(Equal(atc.RunStatusRunning))
	})

	It("waits for the last outstanding schedule request before completing red", func() {
		fixture := createRunLifecycleFixture(downstreamRunConfig("downstream"))
		entry := fixture.jobs["entry"]
		downstream := fixture.jobs["downstream"]
		build := pendingRunBuild(entry)
		consumeObservedSchedule(entry)

		Expect(build.Finish(db.BuildStatusFailed)).To(Succeed())
		Expect(fixture.reloadRun().Status()).To(Equal(atc.RunStatusRunning))

		consumeObservedSchedule(downstream)
		run := fixture.reloadRun()
		Expect(run.Status()).To(Equal(atc.RunStatusFailed))
		Expect(run.CompletedAt()).NotTo(BeNil())
	})
	It("completes when the last build finished during a pass that started with a pending build", func() {
		fixture := createRunLifecycleFixture(basicRunConfig("entry"))
		entry := fixture.jobs["entry"]
		build := pendingRunBuild(entry)

		By("leaving schedule debt outstanding, as aborting a pending build does")
		requestOutstandingSchedule(entry)

		By("finishing the build in-pass, where Finish's own hook is blocked by that debt")
		Expect(build.Finish(db.BuildStatusAborted)).To(Succeed())
		Expect(fixture.reloadRun().Status()).To(Equal(atc.RunStatusRunning))

		By("consuming the request from a pass that found the build it would otherwise have reported")
		consumeObservedSchedule(entry)

		run := fixture.reloadRun()
		Expect(run.Status()).To(Equal(atc.RunStatusAborted), "the scheduler's no-build hint must not gate run completion")
		Expect(run.CompletedAt()).NotTo(BeNil())
	})

	It("does not consume a newer unresolved request when settling an observed token", func() {
		fixture := createRunLifecycleFixture(basicRunConfig("entry"))
		entry := fixture.jobs["entry"]
		build := pendingRunBuild(entry)
		_, err := dbConn.Exec(`
			UPDATE builds SET status = 'failed', completed = true, end_time = now()
			WHERE id = $1
		`, build.ID())
		Expect(err).NotTo(HaveOccurred())

		found, err := entry.Reload()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		observed := entry.ScheduleRequestedTime()
		Eventually(func() time.Time {
			Expect(entry.RequestSchedule()).To(Succeed())
			var requested time.Time
			Expect(dbConn.QueryRow("SELECT schedule_requested FROM jobs WHERE id = $1", entry.ID()).Scan(&requested)).To(Succeed())
			return requested
		}).Should(BeTemporally(">", observed))

		Expect(entry.ConsumeScheduleRequest(observed)).To(Succeed())
		Expect(fixture.reloadRun().Status()).To(Equal(atc.RunStatusRunning))
		consumeObservedSchedule(entry)
		Expect(fixture.reloadRun().Status()).To(Equal(atc.RunStatusFailed))
	})

	It("requires terminal evidence for every expected job before succeeding", func() {
		fixture := createRunLifecycleFixture(downstreamRunConfig("downstream"))
		entry := fixture.jobs["entry"]
		build := pendingRunBuild(entry)
		consumeObservedSchedule(entry)

		Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())
		consumeObservedSchedule(fixture.jobs["downstream"])
		Expect(fixture.reloadRun().Status()).To(Equal(atc.RunStatusRunning))
	})

	It("allows quiescent red completion when an expected downstream job never built", func() {
		fixture := createRunLifecycleFixture(downstreamRunConfig("downstream"))
		entry := fixture.jobs["entry"]
		build := pendingRunBuild(entry)
		consumeObservedSchedule(entry)

		Expect(build.Finish(db.BuildStatusErrored)).To(Succeed())
		consumeObservedSchedule(fixture.jobs["downstream"])
		Expect(fixture.reloadRun().Status()).To(Equal(atc.RunStatusErrored))
	})

	It("completes when pausing a payload clears its last schedule debt", func() {
		fixture := createRunLifecycleFixture(basicRunConfig("entry"))
		entry := fixture.jobs["entry"]
		build := pendingRunBuild(entry)
		consumeObservedSchedule(entry)
		requestOutstandingSchedule(entry)

		Expect(build.Finish(db.BuildStatusFailed)).To(Succeed())
		Expect(fixture.reloadRun().Status()).To(Equal(atc.RunStatusRunning))

		Expect(fixture.payload.Pause("alice")).To(Succeed())
		Expect(fixture.reloadRun().Status()).To(Equal(atc.RunStatusFailed))
		found, err := fixture.payload.Reload()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(fixture.payload.PausedBy()).To(Equal("alice"))
	})

	It("completes when pausing a run job clears its last schedule debt", func() {
		fixture := createRunLifecycleFixture(basicRunConfig("entry"))
		entry := fixture.jobs["entry"]
		build := pendingRunBuild(entry)
		consumeObservedSchedule(entry)
		requestOutstandingSchedule(entry)

		Expect(build.Finish(db.BuildStatusFailed)).To(Succeed())
		Expect(fixture.reloadRun().Status()).To(Equal(atc.RunStatusRunning))

		Expect(entry.Pause("alice")).To(Succeed())
		Expect(fixture.reloadRun().Status()).To(Equal(atc.RunStatusFailed))
		found, err := entry.Reload()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(entry.PausedBy()).To(Equal("alice"))
	})

	It("pauses a run job whose run has already terminalised", func() {
		// Pausing creates no build, so it must not be refused by the
		// build-admission seam's running-status assertion.
		fixture := createRunLifecycleFixture(basicRunConfig("entry"))
		entry := fixture.jobs["entry"]
		build := pendingRunBuild(entry)
		consumeObservedSchedule(entry)

		Expect(build.Finish(db.BuildStatusFailed)).To(Succeed())
		Expect(fixture.reloadRun().Status()).To(Equal(atc.RunStatusFailed))

		Expect(entry.Pause("alice")).To(Succeed())
		found, err := entry.Reload()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(entry.Paused()).To(BeTrue())
		Expect(entry.PausedBy()).To(Equal("alice"))
		Expect(fixture.reloadRun().Status()).To(Equal(atc.RunStatusFailed), "pausing must not revive or re-settle a terminal run")
	})

	DescribeTable("aggregates the rerun-aware latest status with lifecycle severity",
		func(statuses []db.BuildStatus, expected atc.RunStatus) {
			names := make([]string, len(statuses))
			for index := range statuses {
				names[index] = string(rune('a' + index))
			}
			fixture := createRunLifecycleFixture(basicRunConfig(names...))
			for _, name := range names {
				consumeObservedSchedule(fixture.jobs[name])
			}
			for index, name := range names {
				Expect(pendingRunBuild(fixture.jobs[name]).Finish(statuses[index])).To(Succeed())
			}
			Expect(fixture.reloadRun().Status()).To(Equal(expected))
		},
		Entry("failed over succeeded", []db.BuildStatus{db.BuildStatusSucceeded, db.BuildStatusFailed}, atc.RunStatusFailed),
		Entry("aborted over failed", []db.BuildStatus{db.BuildStatusFailed, db.BuildStatusAborted}, atc.RunStatusAborted),
		Entry("errored over aborted", []db.BuildStatus{db.BuildStatusAborted, db.BuildStatusErrored}, atc.RunStatusErrored),
	)

	It("uses the newest rerun result for a job", func() {
		// The rerun happens while a sibling job is still outstanding, which is
		// the only time a rerun is admitted at all: once the run has settled it
		// is refused. The claim is unchanged -- the run's status follows the
		// newest build of a job, not its first.
		fixture := createRunLifecycleFixture(basicRunConfig("entry", "sibling"))
		entry, sibling := fixture.jobs["entry"], fixture.jobs["sibling"]
		original := pendingRunBuild(entry)
		consumeObservedSchedule(entry)
		Expect(original.Finish(db.BuildStatusFailed)).To(Succeed())
		Expect(fixture.reloadRun().Status()).To(Equal(atc.RunStatusRunning))

		rerun, err := entry.RerunBuild(original, "rerun-user")
		Expect(err).NotTo(HaveOccurred())
		consumeObservedSchedule(entry)
		Expect(rerun.Finish(db.BuildStatusSucceeded)).To(Succeed())

		consumeObservedSchedule(sibling)
		Expect(pendingRunBuild(sibling).Finish(db.BuildStatusSucceeded)).To(Succeed())
		Expect(fixture.reloadRun().Status()).To(Equal(atc.RunStatusSucceeded))
	})

	It("pauses a completed payload with internal attribution", func() {
		fixture := createRunLifecycleFixture(basicRunConfig("entry"))
		entry := fixture.jobs["entry"]
		consumeObservedSchedule(entry)
		Expect(pendingRunBuild(entry).Finish(db.BuildStatusSucceeded)).To(Succeed())

		found, err := fixture.payload.Reload()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(fixture.payload.Paused()).To(BeTrue())
		Expect(fixture.payload.PausedBy()).To(Equal("run-completed"))
	})

	It("preserves user pause attribution when completing", func() {
		fixture := createRunLifecycleFixture(basicRunConfig("entry"))
		entry := fixture.jobs["entry"]
		consumeObservedSchedule(entry)
		Expect(fixture.payload.Pause("alice")).To(Succeed())
		Expect(pendingRunBuild(entry).Finish(db.BuildStatusFailed)).To(Succeed())

		found, err := fixture.payload.Reload()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(fixture.payload.Paused()).To(BeTrue())
		Expect(fixture.payload.PausedBy()).To(Equal("alice"))
		Expect(fixture.reloadRun().Status()).To(Equal(atc.RunStatusFailed))
	})

	DescribeTable("refuses direct unpause for every terminal pause attribution",
		func(pausedBy string) {
			fixture := createRunLifecycleFixture(basicRunConfig("entry"))
			entry := fixture.jobs["entry"]
			consumeObservedSchedule(entry)
			if pausedBy != "run-completed" {
				Expect(fixture.payload.Pause(pausedBy)).To(Succeed())
			}
			Expect(pendingRunBuild(entry).Finish(db.BuildStatusFailed)).To(Succeed())

			Expect(fixture.payload.Unpause()).To(MatchError(db.ErrPipelineRunNotRunning))
			found, err := fixture.payload.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(fixture.payload.Paused()).To(BeTrue())
			Expect(fixture.payload.PausedBy()).To(Equal(pausedBy))
		},
		Entry("internal completion", "run-completed"),
		Entry("user pause", "alice"), Entry("automatic pause", "automatic-pipeline-pauser"),
	)

	It("keeps ordinary running-run unpause behavior", func() {
		fixture := createRunLifecycleFixture(basicRunConfig("entry"))
		Expect(fixture.payload.Pause("alice")).To(Succeed())
		Expect(fixture.payload.Unpause()).To(Succeed())

		found, err := fixture.payload.Reload()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(fixture.payload.Paused()).To(BeFalse())
	})

	It("refuses a manual build into a terminal run and leaves it settled", func() {
		// The run is the unit of immutability. A manual trigger on any job
		// inside a settled run used to reopen it, so one run number ended up
		// describing two executions -- two completion times, two sets of
		// builds, one status. The trigger is refused instead, and everything
		// the run had settled on stays exactly as it was.
		fixture := createRunLifecycleFixture(downstreamRunConfig("manual", "stale"))
		entry := fixture.jobs["entry"]
		consumeObservedSchedule(entry)
		Expect(pendingRunBuild(entry).Finish(db.BuildStatusFailed)).To(Succeed())
		consumeObservedSchedule(fixture.jobs["manual"])
		consumeObservedSchedule(fixture.jobs["stale"])
		Expect(fixture.reloadRun().Status()).To(Equal(atc.RunStatusFailed))
		before := fixture.settlement()
		Expect(before.completedAt).NotTo(BeNil())

		manualBuild, err := fixture.jobs["manual"].CreateBuild("manual-user")
		expectTerminalRefusal(err, fixture.run.Number(), atc.RunStatusFailed)
		Expect(manualBuild).To(BeNil())
		Expect(fixture.settlement()).To(Equal(before), "a refused trigger must not touch the run's status, completion or builds")

		found, err := fixture.payload.Reload()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(fixture.payload.Paused()).To(BeTrue(), "the completion pause must not be lifted by a refused trigger")
	})

	It("refuses a rerun of a build belonging to a terminal run", func() {
		// `fly rerun-build` reaches the same admission seam as a manual
		// trigger, so it is refused by the same rule and with the same error.
		fixture := createRunLifecycleFixture(basicRunConfig("entry"))
		entry := fixture.jobs["entry"]
		original := pendingRunBuild(entry)
		consumeObservedSchedule(entry)
		Expect(original.Finish(db.BuildStatusFailed)).To(Succeed())
		Expect(fixture.reloadRun().Status()).To(Equal(atc.RunStatusFailed))
		before := fixture.settlement()

		rerun, err := entry.RerunBuild(original, "rerun-user")
		expectTerminalRefusal(err, fixture.run.Number(), atc.RunStatusFailed)
		Expect(rerun).To(BeNil())
		Expect(fixture.settlement()).To(Equal(before), "a refused rerun must not touch the run's status, completion or builds")
	})

	DescribeTable("leaves every payload pause attribution alone when refusing",
		func(pausedBy string) {
			// Reopen used to dissolve the platform attributions so the build it
			// had just admitted could schedule. With no build admitted there is
			// nothing to unblock, and a settled run's payload stays paused
			// however it came to be paused.
			fixture := createRunLifecycleFixture(basicRunConfig("entry"))
			entry := fixture.jobs["entry"]
			consumeObservedSchedule(entry)
			if pausedBy != "run-completed" {
				Expect(fixture.payload.Pause(pausedBy)).To(Succeed())
			}
			Expect(pendingRunBuild(entry).Finish(db.BuildStatusFailed)).To(Succeed())
			before := fixture.settlement()

			_, err := entry.CreateBuild("manual-user")
			expectTerminalRefusal(err, fixture.run.Number(), atc.RunStatusFailed)

			found, err := fixture.payload.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(fixture.payload.Paused()).To(BeTrue())
			Expect(fixture.payload.PausedBy()).To(Equal(pausedBy))
			Expect(fixture.settlement()).To(Equal(before))
		},
		Entry("internal completion", "run-completed"),
		Entry("user pause", "alice"),
		Entry("automatic pause", "automatic-pipeline-pauser"),
	)

	It("still builds an ordinary pipeline's job after it has completed", func() {
		// The refusal belongs to runs, not to pipelines. An ordinary pipeline
		// has no run header at all, so a job in it keeps building for ever --
		// this is the spec that fails if the terminal check is ever hoisted out
		// of the run branch of the admission seam.
		first, err := defaultJob.CreateBuild("someone")
		Expect(err).NotTo(HaveOccurred())
		Expect(first.Finish(db.BuildStatusSucceeded)).To(Succeed())

		second, err := defaultJob.CreateBuild("someone")
		Expect(err).NotTo(HaveOccurred())
		Expect(second.Status()).To(Equal(db.BuildStatusPending))
		Expect(second.ID()).NotTo(Equal(first.ID()))

		rerun, err := defaultJob.RerunBuild(first, "someone")
		Expect(err).NotTo(HaveOccurred())
		Expect(rerun.RerunOf()).To(Equal(first.ID()))
	})

	It("refuses a manual trigger and a rerun after payload reclamation", func() {
		fixture := createRunLifecycleFixture(basicRunConfig("entry"))
		entry := fixture.jobs["entry"]
		original := pendingRunBuild(entry)
		consumeObservedSchedule(entry)
		Expect(original.Finish(db.BuildStatusFailed)).To(Succeed())
		reclaimRunPayloadForTest(fixture.template, fixture.run)

		_, err := entry.CreateBuild("manual-user")
		Expect(err).To(HaveOccurred())
		_, err = entry.RerunBuild(original, "rerun-user")
		Expect(err).To(HaveOccurred())
		Expect(fixture.reloadRun().Status()).To(Equal(atc.RunStatusFailed))
	})

	It("locks the durable run before mutating a finished build", func() {
		fixture := createRunLifecycleFixture(basicRunConfig("entry"))
		entry := fixture.jobs["entry"]
		build := pendingRunBuild(entry)
		consumeObservedSchedule(entry)

		gateConn := openRunLifecycleConn()
		inspectorConn := openRunLifecycleConn()
		finishConn := openRunLifecycleConn()
		finishing := loadRunLifecycleBuild(finishConn, build.ID())
		gate, err := gateConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = gate.Rollback() })
		var locked int
		Expect(gate.QueryRow("SELECT id FROM pipeline_runs WHERE id = $1 FOR UPDATE", fixture.run.ID()).Scan(&locked)).To(Succeed())

		finished := make(chan error, 1)
		go func() { finished <- finishing.Finish(db.BuildStatusSucceeded) }()
		Consistently(finished, 150*time.Millisecond).ShouldNot(Receive())
		var status db.BuildStatus
		Expect(inspectorConn.QueryRow("SELECT status FROM builds WHERE id = $1", build.ID()).Scan(&status)).To(Succeed())
		Expect(status).To(Equal(db.BuildStatusPending))

		Expect(gate.Rollback()).To(Succeed())
		Eventually(finished).WithTimeout(3 * time.Second).Should(Receive(BeNil()))
	})

	It("lets admission queued first block completion", func() {
		fixture := createRunLifecycleFixture(basicRunConfig("entry"))
		entry := fixture.jobs["entry"]
		finishing := pendingRunBuild(entry)
		consumeObservedSchedule(entry)
		gateConn := openRunLifecycleConn()
		admissionConn := openRunLifecycleConn()
		finishConn := openRunLifecycleConn()
		legacy := fixture.loadPayload(admissionConn).(interface {
			CreateJobBuild(string) (db.Build, error)
		})
		finishing = loadRunLifecycleBuild(finishConn, finishing.ID())

		gate, err := gateConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = gate.Rollback() })
		var locked int
		Expect(gate.QueryRow("SELECT id FROM pipeline_runs WHERE id = $1 FOR UPDATE", fixture.run.ID()).Scan(&locked)).To(Succeed())

		admitted := make(chan error, 1)
		go func() { _, err := legacy.CreateJobBuild("entry"); admitted <- err }()
		Consistently(admitted, 100*time.Millisecond).ShouldNot(Receive())
		finished := make(chan error, 1)
		go func() { finished <- finishing.Finish(db.BuildStatusSucceeded) }()
		Consistently(finished, 100*time.Millisecond).ShouldNot(Receive())

		Expect(gate.Rollback()).To(Succeed())
		Eventually(admitted).WithTimeout(3 * time.Second).Should(Receive(BeNil()))
		Eventually(finished).WithTimeout(3 * time.Second).Should(Receive(BeNil()))
		Expect(fixture.reloadRun().Status()).To(Equal(atc.RunStatusRunning))
	})

	It("makes non-manual admission queued after completion conflict", func() {
		fixture := createRunLifecycleFixture(basicRunConfig("entry"))
		entry := fixture.jobs["entry"]
		finishing := pendingRunBuild(entry)
		consumeObservedSchedule(entry)
		gateConn := openRunLifecycleConn()
		admissionConn := openRunLifecycleConn()
		finishConn := openRunLifecycleConn()
		legacy := fixture.loadPayload(admissionConn).(interface {
			CreateJobBuild(string) (db.Build, error)
		})
		finishing = loadRunLifecycleBuild(finishConn, finishing.ID())

		gate, err := gateConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = gate.Rollback() })
		var locked int
		Expect(gate.QueryRow("SELECT id FROM pipeline_runs WHERE id = $1 FOR UPDATE", fixture.run.ID()).Scan(&locked)).To(Succeed())

		finished := make(chan error, 1)
		go func() { finished <- finishing.Finish(db.BuildStatusSucceeded) }()
		Consistently(finished, 100*time.Millisecond).ShouldNot(Receive())
		admitted := make(chan error, 1)
		go func() { _, err := legacy.CreateJobBuild("entry"); admitted <- err }()
		Consistently(admitted, 100*time.Millisecond).ShouldNot(Receive())

		Expect(gate.Rollback()).To(Succeed())
		Eventually(finished).WithTimeout(3 * time.Second).Should(Receive(BeNil()))
		var admissionErr error
		Eventually(admitted).WithTimeout(3 * time.Second).Should(Receive(&admissionErr))
		expectTerminalRefusal(admissionErr, fixture.run.Number(), atc.RunStatusSucceeded)
		Expect(fixture.reloadRun().Status()).To(Equal(atc.RunStatusSucceeded))
	})
})

var _ = Describe("Pipeline run lifecycle structural guard", func() {
	It("finds every lock, completion, unpause, and refusal seam", func() {
		buildSource, err := os.ReadFile("build.go")
		Expect(err).NotTo(HaveOccurred())
		jobSource, err := os.ReadFile("job.go")
		Expect(err).NotTo(HaveOccurred())
		pipelineSource, err := os.ReadFile("pipeline.go")
		Expect(err).NotTo(HaveOccurred())
		lockSource, err := os.ReadFile("pipeline_run_lock.go")
		Expect(err).NotTo(HaveOccurred())
		lifecycleSource, err := os.ReadFile("pipeline_run_lifecycle.go")
		Expect(err).NotTo(HaveOccurred())

		finishStart := strings.Index(string(buildSource), "func (b *build) Finish")
		Expect(finishStart).To(BeNumerically(">=", 0), "guard must find Build.Finish")
		finishEnd := strings.Index(string(buildSource)[finishStart:], "\nfunc ")
		Expect(finishEnd).To(BeNumerically(">", 0), "guard must bound Build.Finish")
		finishBody := string(buildSource)[finishStart : finishStart+finishEnd]
		lockIndex := strings.Index(finishBody, "lockPipelineRun(")
		mutationIndex := strings.Index(finishBody, `Update("builds")`)
		completionIndex := strings.Index(finishBody, "attemptRunCompletion(")
		Expect(lockIndex).To(BeNumerically(">=", 0), "finish must lock a resolved durable run")
		Expect(mutationIndex).To(BeNumerically(">", lockIndex), "finish must lock before build mutation")
		Expect(completionIndex).To(BeNumerically(">", mutationIndex), "finish completion must follow build/downstream mutation")

		consumeStart := strings.Index(string(jobSource), "func (j *job) ConsumeScheduleRequest")
		Expect(consumeStart).To(BeNumerically(">=", 0), "guard must find schedule consumption")
		consumeEnd := strings.Index(string(jobSource)[consumeStart:], "\nfunc ")
		Expect(consumeEnd).To(BeNumerically(">", 0), "guard must bound schedule consumption")
		consumeBody := string(jobSource)[consumeStart : consumeStart+consumeEnd]
		Expect(consumeBody).To(ContainSubstring("attemptRunCompletion("), "schedule consumption must be the second completion call site")
		Expect(consumeBody).NotTo(ContainSubstring("noBuild"), "schedule consumption must not gate completion on a scheduler no-build hint")
		Expect(string(jobSource)).NotTo(ContainSubstring("UpdateLastScheduled"), "the obsolete independent consumer must be removed")

		pauseStart := strings.Index(string(pipelineSource), "func (p *pipeline) Pause(pausedBy string)")
		Expect(pauseStart).To(BeNumerically(">=", 0), "guard must find payload pause")
		pauseEnd := strings.Index(string(pipelineSource)[pauseStart:], "\nfunc ")
		Expect(pauseEnd).To(BeNumerically(">", 0), "guard must bound payload pause")
		pauseBody := string(pipelineSource)[pauseStart : pauseStart+pauseEnd]
		Expect(pauseBody).To(ContainSubstring("lockPipelineRunForPayload("), "payload pause must lock its durable run")
		Expect(pauseBody).To(ContainSubstring("attemptRunCompletion("), "payload pause must settle cleared schedule debt")

		jobPauseStart := strings.Index(string(jobSource), "func (j *job) Pause(pausedBy string)")
		Expect(jobPauseStart).To(BeNumerically(">=", 0), "guard must find run job pause")
		jobPauseEnd := strings.Index(string(jobSource)[jobPauseStart:], "\nfunc ")
		Expect(jobPauseEnd).To(BeNumerically(">", 0), "guard must bound run job pause")
		jobPauseBody := string(jobSource)[jobPauseStart : jobPauseStart+jobPauseEnd]
		Expect(jobPauseBody).To(ContainSubstring("lockPipelineRunForPayload("), "run job pause must lock its durable run without asserting admission")
		Expect(jobPauseBody).NotTo(ContainSubstring("lockJobBuildAdmission("), "pausing creates no build, so it must not go through the build-admission seam")
		Expect(jobPauseBody).To(ContainSubstring("attemptRunCompletion("), "run job pause must settle cleared schedule debt")

		unpauseStart := strings.Index(string(pipelineSource), "func (p *pipeline) Unpause")
		Expect(unpauseStart).To(BeNumerically(">=", 0), "guard must find payload unpause")
		unpauseEnd := strings.Index(string(pipelineSource)[unpauseStart:], "\nfunc ")
		Expect(unpauseEnd).To(BeNumerically(">", 0), "guard must bound payload unpause")
		Expect(string(pipelineSource)[unpauseStart : unpauseStart+unpauseEnd]).To(ContainSubstring("lockPipelineRunForPayload("))

		Expect(string(lockSource)).To(ContainSubstring("ErrPipelineRunTerminal{"), "canonical admission must own the terminal refusal")
		Expect(string(lockSource)).NotTo(ContainSubstring("ReopenTerminal"), "no caller may opt out of the terminal refusal")
		for name, source := range map[string][]byte{
			"pipeline_run_lock.go":      lockSource,
			"pipeline_run_lifecycle.go": lifecycleSource,
			"job.go":                    jobSource,
			"build.go":                  buildSource,
			"pipeline.go":               pipelineSource,
		} {
			Expect(string(source)).NotTo(ContainSubstring("reopenPipelineRun"), "a settled run has no reopen seam ("+name+")")
		}
		Expect(string(lifecycleSource)).To(ContainSubstring("func attemptRunCompletion("), "guard must find the single stateless completion predicate")
	})
})
