package db_test

import (
	"context"
	"fmt"
	"strings"
	"time"

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

	It("does not complete while a build is started", func() {
		// a run whose entry build is still open is still running, whatever the
		// other builds did
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

	// review finding (2026-07-11): TOCTOU between CheckComplete and Finish —
	// a retrigger created AND completed in that window used to get
	// end_time < completed_at (Finish stamped now() at finish time), so the
	// F26 reopen predicate (end_time > completed_at) never matched and the
	// run kept a stale terminal status forever. Finish must stamp
	// completed_at from CheckComplete's read snapshot so anything that
	// finishes after the reads is strictly newer.
	It("reopens runs for retriggers that complete between CheckComplete and Finish", func() {
		_, err := dbConn.Exec(
			`UPDATE builds SET status = 'succeeded' WHERE pipeline_id = $1`, instance.ID())
		Expect(err).ToNot(HaveOccurred())
		markScheduled(instance.ID())

		// the lifecycler observes the run as complete...
		status, complete, err := run.CheckComplete()
		Expect(err).ToNot(HaveOccurred())
		Expect(complete).To(BeTrue())
		Expect(status).To(Equal(db.PipelineRunSucceeded))

		// ...a retrigger is created AND finishes inside the window...
		finishBuild("entry", db.BuildStatusFailed)

		// ...and only then does the lifecycler stamp the run finished
		Expect(run.Finish(status)).To(Succeed())

		reactivated, err := factory.CompletedRunsWithNewActivity()
		Expect(err).ToNot(HaveOccurred())
		Expect(reactivated).To(HaveLen(1))
		Expect(reactivated[0].ID()).To(Equal(run.ID()))
	})

	// review finding (2026-07-11): instance_pipeline_id is ON DELETE SET
	// NULL, and CheckComplete used to report not-complete on the NULLed FK —
	// so a run whose instance pipeline was destroyed stayed 'running'
	// forever. A destroyed instance can never become quiescent; the run must
	// terminate as errored on the next lifecycler tick.
	It("completes as errored when the instance pipeline is destroyed", func() {
		Expect(instance.Destroy()).To(Succeed())

		// reload the run the way the lifecycler does (RunningRuns rescans):
		// the destroy NULLed the FK in the DB
		reloaded, found, err := factory.GetRun(run.TemplatePipelineID(), run.Number())
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		_, hasInstance := reloaded.InstancePipelineID()
		Expect(hasInstance).To(BeFalse())

		status, complete, err := reloaded.CheckComplete()
		Expect(err).ToNot(HaveOccurred())
		Expect(complete).To(BeTrue())
		Expect(status).To(Equal(db.PipelineRunErrored))
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

var _ = Describe("PipelineRun retention", func() {
	var factory db.PipelineRunFactory

	makeTemplate := func(name string, retention *atc.RunRetentionConfig) db.Pipeline {
		config := atc.Config{
			Template:     true,
			RunRetention: retention,
			Jobs: atc.JobConfigs{
				{Name: "entry", PlanSequence: []atc.Step{
					{Config: &atc.TaskStep{Name: "t", ConfigPath: "task.yml"}},
				}},
			},
		}
		template, _, err := defaultTeam.SavePipeline(
			atc.PipelineRef{Name: name}, config, db.ConfigVersion(0), false)
		Expect(err).ToNot(HaveOccurred())
		return template
	}

	completedRun := func(template db.Pipeline) db.PipelineRun {
		run, err := factory.CreateRun(template.ID(), nil, "test")
		Expect(err).ToNot(HaveOccurred())
		Expect(run.Finish(db.PipelineRunSucceeded)).To(Succeed())
		return run
	}

	BeforeEach(func() {
		factory = db.NewPipelineRunFactory(logger, dbConn, lockFactory, checkFactory)
	})

	It("selects completed runs beyond keep_last, newest kept", func() {
		template := makeTemplate("retention-keep-last", &atc.RunRetentionConfig{KeepLast: 2})
		one := completedRun(template)
		completedRun(template)
		completedRun(template)

		toArchive, err := factory.RunsToArchive()
		Expect(err).ToNot(HaveOccurred())
		Expect(toArchive).To(HaveLen(1))
		Expect(toArchive[0].ID()).To(Equal(one.ID()))
	})

	It("selects completed runs older than ttl_days", func() {
		template := makeTemplate("retention-ttl", &atc.RunRetentionConfig{TTLDays: 5})
		old := completedRun(template)
		completedRun(template)

		_, err := dbConn.Exec(
			`UPDATE pipeline_runs SET completed_at = now() - interval '10 days' WHERE id = $1`, old.ID())
		Expect(err).ToNot(HaveOccurred())

		toArchive, err := factory.RunsToArchive()
		Expect(err).ToNot(HaveOccurred())
		Expect(toArchive).To(HaveLen(1))
		Expect(toArchive[0].ID()).To(Equal(old.ID()))
	})

	It("never selects running runs or templates without retention", func() {
		noRetention := makeTemplate("retention-none", nil)
		completedRun(noRetention)

		withRetention := makeTemplate("retention-running", &atc.RunRetentionConfig{KeepLast: 0, TTLDays: 1})
		_, err := factory.CreateRun(withRetention.ID(), nil, "test") // stays running
		Expect(err).ToNot(HaveOccurred())

		toArchive, err := factory.RunsToArchive()
		Expect(err).ToNot(HaveOccurred())
		Expect(toArchive).To(BeEmpty())
	})

	It("Archive archives the instance pipeline and the run row", func() {
		template := makeTemplate("retention-archive", &atc.RunRetentionConfig{KeepLast: 0})
		run := completedRun(template)

		Expect(run.Archive()).To(Succeed())
		Expect(run.Archived()).To(BeTrue())

		instance, found, err := run.InstancePipeline()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(instance.Archived()).To(BeTrue())

		// archived runs are never re-selected
		toArchive, err := factory.RunsToArchive()
		Expect(err).ToNot(HaveOccurred())
		Expect(toArchive).To(BeEmpty())
	})
})

var _ = Describe("PipelineRun retirement archiving", func() {
	var factory db.PipelineRunFactory

	const retirement = time.Hour

	templateConfig := atc.Config{
		Template: true,
		Jobs: atc.JobConfigs{{
			Name:         "run",
			PlanSequence: []atc.Step{{Config: &atc.TaskStep{Name: "work", ConfigPath: "task.yml"}}},
		}},
	}

	BeforeEach(func() {
		factory = db.NewPipelineRunFactory(logger, dbConn, lockFactory, checkFactory)
	})

	ownedTemplate := func(name string) db.Pipeline {
		template, created, err := db.NewWorkflowRunTemplateFactory(dbConn, lockFactory).SaveWorkflowRunTemplate(
			context.Background(), defaultTeam.ID(), atc.PipelineRef{Name: name}, templateConfig)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		return template
	}

	addRun := func(template db.Pipeline, number int, status string, archived bool, completedAgo time.Duration) (int, db.Pipeline) {
		instance, _, err := defaultTeam.SavePipeline(
			atc.PipelineRef{Name: template.Name(), InstanceVars: atc.InstanceVars{"run": number}},
			atc.Config{Jobs: atc.JobConfigs{{Name: "run"}}}, db.ConfigVersion(0), false,
		)
		Expect(err).NotTo(HaveOccurred())
		var completedAgoParam any
		if completedAgo > 0 {
			completedAgoParam = completedAgo.String()
		}
		var runID int
		Expect(dbConn.QueryRow(`
			INSERT INTO pipeline_runs (template_pipeline_id, instance_pipeline_id, number, status, archived, completed_at)
			VALUES ($1, $2, $3, $4, $5, CASE WHEN $6::text IS NULL THEN NULL ELSE now() - $6::interval END)
			RETURNING id
		`, template.ID(), instance.ID(), number, status, archived, completedAgoParam).Scan(&runID)).To(Succeed())
		return runID, instance
	}

	workflowName := func() string {
		return fmt.Sprintf("retirement-workflow-%d", time.Now().UnixNano())
	}

	defineVersion := func(name string, version int, live bool) int {
		var id int
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version, live)
			VALUES ($1, $2, $3, 'schema_version: 3', 'alice', 3, 1, $4)
			RETURNING id
		`, name, version, fmt.Sprintf("%064d", version), live).Scan(&id)).To(Succeed())
		return id
	}

	defineNodeVersion := func(name string, version int, released bool) int {
		var id int
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(definition_kind, name, version, content_hash, definition, created_by,
				 schema_version, signature_version, released_at)
			VALUES ('node', $1, $2, $3, 'schema_version: 1', 'alice', 3, 1,
				 CASE WHEN $4 THEN now() ELSE NULL END)
			RETURNING id
		`, name, version, fmt.Sprintf("%064d", version), released).Scan(&id)).To(Succeed())
		return id
	}

	cite := func(template db.Pipeline, runID int, instance db.Pipeline, definitionID int, name string, version int, status string) {
		_, err := dbConn.Exec(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
				 schema_version, signature_version, definition_content_hash, idempotency_key,
				 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status, pipeline_run_id, template_pipeline_id, instance_pipeline_id,
				 concrete_config, concrete_config_hash)
			VALUES ($1, $2, $3, $4, $5, 3, 1, $6, $7, '{}'::jsonb, $6,
			        'ticket', 'GC-3', 'alice', $8, $9, $10, $11, '{}'::jsonb, $6)
		`, defaultTeam.ID(), defaultTeam.Name(), definitionID, name, version,
			strings.Repeat("f", 64), fmt.Sprintf("retirement-key-%d", time.Now().UnixNano()),
			status, runID, template.ID(), instance.ID())
		Expect(err).NotTo(HaveOccurred())
	}

	citeNode := func(template db.Pipeline, runID int, instance db.Pipeline, definitionID int, name string, version int, status string) {
		_, err := dbConn.Exec(`
			INSERT INTO agent_workflow_runs
				(definition_kind, team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
				 schema_version, signature_version, definition_content_hash, idempotency_key,
				 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status, pipeline_run_id, template_pipeline_id, instance_pipeline_id,
				 concrete_config, concrete_config_hash)
			VALUES ('node', $1, $2, $3, $4, $5, 3, 1, $6, $7, '{}'::jsonb, $6,
				        'ticket', 'GC-node', 'alice', $8, $9, $10, $11, '{}'::jsonb, $6)
		`, defaultTeam.ID(), defaultTeam.Name(), definitionID, name, version,
			strings.Repeat("e", 64), fmt.Sprintf("node-retirement-key-%d", time.Now().UnixNano()),
			status, runID, template.ID(), instance.ID())
		Expect(err).NotTo(HaveOccurred())
	}

	retiredRun := func(templateName string) int {
		name := workflowName()
		cited := defineVersion(name, 1, false)
		defineVersion(name, 2, true)
		template := ownedTemplate(templateName)
		runID, instance := addRun(template, 1, "succeeded", false, 24*time.Hour)
		cite(template, runID, instance, cited, name, 1, "succeeded")
		return runID
	}

	It("selects completed runs of retired owned templates past the retirement period", func() {
		runID := retiredRun("retirement-eligible-template")

		toArchive, err := factory.RunsOfRetiredTemplatesToArchive(retirement)
		Expect(err).NotTo(HaveOccurred())
		Expect(toArchive).To(HaveLen(1))
		Expect(toArchive[0].ID()).To(Equal(runID))
	})

	It("selects completed node runs only after a newer node version is released", func() {
		name := workflowName()
		cited := defineNodeVersion(name, 1, false)
		defineNodeVersion(name, 2, true)
		template := ownedTemplate("retirement-released-node-template")
		runID, instance := addRun(template, 1, "succeeded", false, 24*time.Hour)
		citeNode(template, runID, instance, cited, name, 1, "succeeded")

		toArchive, err := factory.RunsOfRetiredTemplatesToArchive(retirement)
		Expect(err).NotTo(HaveOccurred())
		Expect(toArchive).To(HaveLen(1))
		Expect(toArchive[0].ID()).To(Equal(runID))
	})

	It("skips runs completed inside the retirement period", func() {
		name := workflowName()
		cited := defineVersion(name, 1, false)
		defineVersion(name, 2, true)
		template := ownedTemplate("retirement-fresh-template")
		runID, instance := addRun(template, 1, "succeeded", false, 5*time.Minute)
		cite(template, runID, instance, cited, name, 1, "succeeded")

		toArchive, err := factory.RunsOfRetiredTemplatesToArchive(retirement)
		Expect(err).NotTo(HaveOccurred())
		Expect(toArchive).To(BeEmpty())
	})

	It("defers to run_retention on templates that declare their own policy", func() {
		runID := retiredRun("retirement-owned-policy-template")
		var templateID int
		Expect(dbConn.QueryRow(`SELECT template_pipeline_id FROM pipeline_runs WHERE id = $1`, runID).Scan(&templateID)).To(Succeed())
		_, err := dbConn.Exec(`
			UPDATE pipelines SET run_retention = '{"keep_last": 5}'::jsonb WHERE id = $1
		`, templateID)
		Expect(err).NotTo(HaveOccurred())

		toArchive, err := factory.RunsOfRetiredTemplatesToArchive(retirement)
		Expect(err).NotTo(HaveOccurred())
		Expect(toArchive).To(BeEmpty())
	})

	It("skips templates while any citing durable run is not terminal", func() {
		name := workflowName()
		cited := defineVersion(name, 1, false)
		defineVersion(name, 2, true)
		template := ownedTemplate("retirement-running-template")
		firstRun, firstInstance := addRun(template, 1, "succeeded", false, 24*time.Hour)
		secondRun, secondInstance := addRun(template, 2, "running", false, 0)
		cite(template, firstRun, firstInstance, cited, name, 1, "succeeded")
		cite(template, secondRun, secondInstance, cited, name, 1, "running")

		toArchive, err := factory.RunsOfRetiredTemplatesToArchive(retirement)
		Expect(err).NotTo(HaveOccurred())
		Expect(toArchive).To(BeEmpty())
	})

	It("skips templates that no durable run cites", func() {
		template := ownedTemplate("retirement-capture-template")
		addRun(template, 1, "succeeded", false, 24*time.Hour)

		toArchive, err := factory.RunsOfRetiredTemplatesToArchive(retirement)
		Expect(err).NotTo(HaveOccurred())
		Expect(toArchive).To(BeEmpty())
	})

	It("skips templates whose cited version has no live successor", func() {
		name := workflowName()
		cited := defineVersion(name, 1, true)
		template := ownedTemplate("retirement-live-template")
		runID, instance := addRun(template, 1, "succeeded", false, 24*time.Hour)
		cite(template, runID, instance, cited, name, 1, "succeeded")

		toArchive, err := factory.RunsOfRetiredTemplatesToArchive(retirement)
		Expect(err).NotTo(HaveOccurred())
		Expect(toArchive).To(BeEmpty())
	})

	It("skips archived and running runs of retired templates", func() {
		name := workflowName()
		cited := defineVersion(name, 1, false)
		defineVersion(name, 2, true)
		template := ownedTemplate("retirement-archived-template")
		firstRun, firstInstance := addRun(template, 1, "succeeded", true, 24*time.Hour)
		cite(template, firstRun, firstInstance, cited, name, 1, "succeeded")

		toArchive, err := factory.RunsOfRetiredTemplatesToArchive(retirement)
		Expect(err).NotTo(HaveOccurred())
		Expect(toArchive).To(BeEmpty())
	})

	It("never selects runs of templates the server does not own", func() {
		name := workflowName()
		cited := defineVersion(name, 1, false)
		defineVersion(name, 2, true)
		template, _, err := defaultTeam.SavePipeline(
			atc.PipelineRef{Name: "retirement-unowned-template"}, templateConfig, db.ConfigVersion(0), false)
		Expect(err).NotTo(HaveOccurred())
		runID, instance := addRun(template, 1, "succeeded", false, 24*time.Hour)
		cite(template, runID, instance, cited, name, 1, "succeeded")

		toArchive, err := factory.RunsOfRetiredTemplatesToArchive(retirement)
		Expect(err).NotTo(HaveOccurred())
		Expect(toArchive).To(BeEmpty())
	})

	It("rejects a non-positive retirement period", func() {
		_, err := factory.RunsOfRetiredTemplatesToArchive(0)
		Expect(err).To(HaveOccurred())
	})

	It("archiving a retired template's runs makes it destroyable by the tier-2 collector pass", func() {
		runID := retiredRun("retirement-end-to-end-template")

		toArchive, err := factory.RunsOfRetiredTemplatesToArchive(retirement)
		Expect(err).NotTo(HaveOccurred())
		Expect(toArchive).To(HaveLen(1))
		Expect(toArchive[0].ID()).To(Equal(runID))
		Expect(toArchive[0].Archive()).To(Succeed())

		lifecycle := db.NewWorkflowRunTemplateLifecycle(dbConn)
		destroyed, err := lifecycle.RemoveRetiredWorkflowRunTemplates(context.Background(), retirement, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(Equal(1))

		var remaining int
		Expect(dbConn.QueryRow(`SELECT COUNT(*) FROM pipeline_runs WHERE id = $1`, runID).Scan(&remaining)).To(Succeed())
		Expect(remaining).To(Equal(0))
	})
})
