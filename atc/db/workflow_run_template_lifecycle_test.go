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

var _ = Describe("WorkflowRunTemplateLifecycle", func() {
	var (
		ctx       context.Context
		factory   db.WorkflowRunTemplateFactory
		lifecycle db.WorkflowRunTemplateLifecycle
	)

	const grace = time.Hour

	config := atc.Config{
		Template: true,
		Jobs: atc.JobConfigs{{
			Name:         "run",
			PlanSequence: []atc.Step{{Config: &atc.TaskStep{Name: "work", ConfigPath: "task.yml"}}},
		}},
	}

	BeforeEach(func() {
		ctx = context.Background()
		factory = db.NewWorkflowRunTemplateFactory(dbConn, lockFactory)
		lifecycle = db.NewWorkflowRunTemplateLifecycle(dbConn)
	})

	saveTemplate := func(name string) db.Pipeline {
		pipeline, created, err := factory.SaveWorkflowRunTemplate(ctx, defaultTeam.ID(), atc.PipelineRef{Name: name}, config)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		return pipeline
	}

	// The grace period is measured from ownership registration, so age is
	// simulated by backdating the ownership row rather than by sleeping.
	age := func(pipeline db.Pipeline, d time.Duration) db.Pipeline {
		_, err := dbConn.Exec(`
			UPDATE agent_workflow_run_templates
			SET created_at = now() - $2::interval
			WHERE pipeline_id = $1
		`, pipeline.ID(), d.String())
		Expect(err).NotTo(HaveOccurred())
		return pipeline
	}

	abandoned := func(name string) db.Pipeline {
		return age(saveTemplate(name), 24*time.Hour)
	}

	exists := func(pipeline db.Pipeline) bool {
		var present bool
		Expect(dbConn.QueryRow(`SELECT EXISTS (SELECT 1 FROM pipelines WHERE id = $1)`, pipeline.ID()).Scan(&present)).To(Succeed())
		return present
	}

	definitionID := func() int {
		var id int
		name := fmt.Sprintf("template-gc-%d", time.Now().UnixNano())
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ($1, 1, $2, 'schema_version: 3', 'alice', 3, 1)
			RETURNING id
		`, name, strings.Repeat("a", 64)).Scan(&id)).To(Succeed())
		return id
	}

	insertUnlinkedWorkflowRun := func(status string) {
		_, err := dbConn.Exec(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
				 schema_version, signature_version, definition_content_hash, idempotency_key,
				 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status)
			VALUES ($1, $2, $3, 'gc-workflow', 1, 3, 1, $4, $5, '{}'::jsonb, $4,
			        'ticket', 'GC-1', 'alice', $6)
		`, defaultTeam.ID(), defaultTeam.Name(), definitionID(), strings.Repeat("c", 64),
			fmt.Sprintf("gc-key-%d", time.Now().UnixNano()), status)
		Expect(err).NotTo(HaveOccurred())
	}

	// agent_workflow_runs_execution_link_complete_check requires the whole
	// execution link at once, so a durable citation always arrives with a
	// pipeline_runs row. Migration 1773106103 dropped the foreign keys behind
	// those columns, so a citation can outlive the rows it names.
	insertLinkedWorkflowRun := func(template db.Pipeline, status string) {
		instance, _, err := defaultTeam.SavePipeline(
			atc.PipelineRef{Name: template.Name(), InstanceVars: atc.InstanceVars{"run": 1}},
			atc.Config{Jobs: atc.JobConfigs{{Name: "run"}}}, db.ConfigVersion(0), false,
		)
		Expect(err).NotTo(HaveOccurred())
		var pipelineRunID int
		Expect(dbConn.QueryRow(`
			INSERT INTO pipeline_runs (template_pipeline_id, instance_pipeline_id, number)
			VALUES ($1, $2, 1)
			RETURNING id
		`, template.ID(), instance.ID()).Scan(&pipelineRunID)).To(Succeed())
		_, err = dbConn.Exec(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
				 schema_version, signature_version, definition_content_hash, idempotency_key,
				 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status, pipeline_run_id, template_pipeline_id, instance_pipeline_id,
				 concrete_config, concrete_config_hash)
			VALUES ($1, $2, $3, 'gc-workflow', 1, 3, 1, $4, $5, '{}'::jsonb, $4,
			        'ticket', 'GC-1', 'alice', $6, $7, $8, $9, '{}'::jsonb, $4)
		`, defaultTeam.ID(), defaultTeam.Name(), definitionID(), strings.Repeat("c", 64),
			fmt.Sprintf("gc-key-%d", time.Now().UnixNano()), status,
			pipelineRunID, template.ID(), instance.ID())
		Expect(err).NotTo(HaveOccurred())
	}

	It("destroys an owned template that was never executed and is past the grace period", func() {
		template := abandoned("abandoned-workflow-template")

		destroyed, err := lifecycle.RemoveAbandonedWorkflowRunTemplates(ctx, grace, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(Equal(1))

		Expect(exists(template)).To(BeFalse())
		owned, err := factory.IsWorkflowRunTemplate(ctx, template.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(owned).To(BeFalse())
	})

	It("keeps a template that is still inside the grace period", func() {
		template := age(saveTemplate("fresh-workflow-template"), 5*time.Minute)

		destroyed, err := lifecycle.RemoveAbandonedWorkflowRunTemplates(ctx, grace, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(Equal(0))
		Expect(exists(template)).To(BeTrue())
	})

	It("keeps a template that has execution history, because the run rows cascade with it", func() {
		template := abandoned("executed-workflow-template")
		_, err := dbConn.Exec(`
			INSERT INTO pipeline_runs (template_pipeline_id, number, params, created_by)
			VALUES ($1, 1, '{}', 'workflow-owner')
		`, template.ID())
		Expect(err).NotTo(HaveOccurred())

		destroyed, err := lifecycle.RemoveAbandonedWorkflowRunTemplates(ctx, grace, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(Equal(0))
		Expect(exists(template)).To(BeTrue())
	})

	It("keeps a template a durable workflow run still cites, even with its execution rows gone", func() {
		template := abandoned("cited-workflow-template")
		insertLinkedWorkflowRun(template, "succeeded")
		// Strip everything the first two guards would have caught, leaving only
		// the unenforced durable citation to protect the template.
		_, err := dbConn.Exec(`DELETE FROM pipeline_runs WHERE template_pipeline_id = $1`, template.ID())
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`
			DELETE FROM pipelines
			WHERE team_id = $1 AND name = $2 AND instance_vars IS NOT NULL
		`, defaultTeam.ID(), template.Name())
		Expect(err).NotTo(HaveOccurred())

		destroyed, err := lifecycle.RemoveAbandonedWorkflowRunTemplates(ctx, grace, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(Equal(0))
		Expect(exists(template)).To(BeTrue())
	})

	It("keeps a template whose name still has run instances", func() {
		template := abandoned("instanced-workflow-template")
		_, _, err := defaultTeam.SavePipeline(
			atc.PipelineRef{Name: template.Name(), InstanceVars: atc.InstanceVars{"run": 1}},
			atc.Config{Jobs: atc.JobConfigs{{Name: "run"}}}, db.ConfigVersion(0), false,
		)
		Expect(err).NotTo(HaveOccurred())

		destroyed, err := lifecycle.RemoveAbandonedWorkflowRunTemplates(ctx, grace, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(Equal(0))
		Expect(exists(template)).To(BeTrue())
	})

	It("skips, rather than waits on, a template an execution transaction already locked", func() {
		locked := abandoned("locked-workflow-template")
		free := abandoned("unlocked-workflow-template")

		// The lock validateLockedWorkflowTemplate holds for the whole of an
		// execution-creating transaction.
		admission := postgresRunner.OpenConn()
		defer admission.Close()
		tx, err := admission.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = tx.Rollback() }()
		var lockedID int
		Expect(tx.QueryRow(`SELECT id FROM pipelines WHERE id = $1 FOR UPDATE`, locked.ID()).Scan(&lockedID)).To(Succeed())

		destroyed, err := lifecycle.RemoveAbandonedWorkflowRunTemplates(ctx, grace, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(Equal(1))

		Expect(exists(locked)).To(BeTrue())
		Expect(exists(free)).To(BeFalse())
	})

	It("defers the whole team while an admission has not yet linked its template", func() {
		template := abandoned("racing-workflow-template")
		insertUnlinkedWorkflowRun("admitting")

		destroyed, err := lifecycle.RemoveAbandonedWorkflowRunTemplates(ctx, grace, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(Equal(0))
		Expect(exists(template)).To(BeTrue())
	})

	It("never touches an ordinary template pipeline the server does not own", func() {
		ordinary, _, err := defaultTeam.SavePipeline(
			atc.PipelineRef{Name: "ordinary-unowned-template"}, config, db.ConfigVersion(0), false,
		)
		Expect(err).NotTo(HaveOccurred())

		_, err = lifecycle.RemoveAbandonedWorkflowRunTemplates(ctx, grace, 10)
		Expect(err).NotTo(HaveOccurred())

		var present bool
		Expect(dbConn.QueryRow(`SELECT EXISTS (SELECT 1 FROM pipelines WHERE id = $1)`, ordinary.ID()).Scan(&present)).To(Succeed())
		Expect(present).To(BeTrue())
	})

	It("removes no more than the requested batch per pass", func() {
		first := abandoned("batched-workflow-template-one")
		second := abandoned("batched-workflow-template-two")

		destroyed, err := lifecycle.RemoveAbandonedWorkflowRunTemplates(ctx, grace, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(Equal(1))

		destroyed, err = lifecycle.RemoveAbandonedWorkflowRunTemplates(ctx, grace, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(Equal(1))

		Expect(exists(first)).To(BeFalse())
		Expect(exists(second)).To(BeFalse())
	})

	It("rejects a non-positive grace period or an out-of-range batch", func() {
		_, err := lifecycle.RemoveAbandonedWorkflowRunTemplates(ctx, 0, 10)
		Expect(err).To(HaveOccurred())

		_, err = lifecycle.RemoveAbandonedWorkflowRunTemplates(ctx, grace, 0)
		Expect(err).To(HaveOccurred())

		_, err = lifecycle.RemoveAbandonedWorkflowRunTemplates(ctx, grace, 100000)
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("WorkflowRunTemplateLifecycle retired templates", func() {
	var (
		ctx       context.Context
		factory   db.WorkflowRunTemplateFactory
		lifecycle db.WorkflowRunTemplateLifecycle
	)

	const retirement = time.Hour

	config := atc.Config{
		Template: true,
		Jobs: atc.JobConfigs{{
			Name:         "run",
			PlanSequence: []atc.Step{{Config: &atc.TaskStep{Name: "work", ConfigPath: "task.yml"}}},
		}},
	}

	BeforeEach(func() {
		ctx = context.Background()
		factory = db.NewWorkflowRunTemplateFactory(dbConn, lockFactory)
		lifecycle = db.NewWorkflowRunTemplateLifecycle(dbConn)
	})

	saveTemplate := func(name string) db.Pipeline {
		pipeline, created, err := factory.SaveWorkflowRunTemplate(ctx, defaultTeam.ID(), atc.PipelineRef{Name: name}, config)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		return pipeline
	}

	exists := func(pipeline db.Pipeline) bool {
		var present bool
		Expect(dbConn.QueryRow(`SELECT EXISTS (SELECT 1 FROM pipelines WHERE id = $1)`, pipeline.ID()).Scan(&present)).To(Succeed())
		return present
	}

	// The unique live index is per workflow name, so each spec works with its
	// own uniquely-named workflow lineage.
	workflowName := func() string {
		return fmt.Sprintf("retired-workflow-%d", time.Now().UnixNano())
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

	type executedRun struct {
		number           int
		status           string
		runArchived      bool
		instanceArchived bool
		completedAgo     time.Duration // zero means completed_at stays NULL
	}

	addRun := func(template db.Pipeline, run executedRun) (int, db.Pipeline) {
		instance, _, err := defaultTeam.SavePipeline(
			atc.PipelineRef{Name: template.Name(), InstanceVars: atc.InstanceVars{"run": run.number}},
			atc.Config{Jobs: atc.JobConfigs{{Name: "run"}}}, db.ConfigVersion(0), false,
		)
		Expect(err).NotTo(HaveOccurred())
		if run.instanceArchived {
			_, err = dbConn.Exec(`UPDATE pipelines SET archived = true, paused = true WHERE id = $1`, instance.ID())
			Expect(err).NotTo(HaveOccurred())
		}
		var completedAgo any
		if run.completedAgo > 0 {
			completedAgo = run.completedAgo.String()
		}
		var runID int
		Expect(dbConn.QueryRow(`
			INSERT INTO pipeline_runs (template_pipeline_id, instance_pipeline_id, number, status, archived, completed_at)
			VALUES ($1, $2, $3, $4, $5, CASE WHEN $6::text IS NULL THEN NULL ELSE now() - $6::interval END)
			RETURNING id
		`, template.ID(), instance.ID(), run.number, run.status, run.runArchived, completedAgo).Scan(&runID)).To(Succeed())
		return runID, instance
	}

	archivedRun := func(number int) executedRun {
		return executedRun{
			number: number, status: "succeeded",
			runArchived: true, instanceArchived: true,
			completedAgo: 24 * time.Hour,
		}
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
			        'ticket', 'GC-2', 'alice', $8, $9, $10, $11, '{}'::jsonb, $6)
		`, defaultTeam.ID(), defaultTeam.Name(), definitionID, name, version,
			strings.Repeat("d", 64), fmt.Sprintf("retired-key-%d", time.Now().UnixNano()),
			status, runID, template.ID(), instance.ID())
		Expect(err).NotTo(HaveOccurred())
	}

	// retiredTemplate assembles the fully reclaimable shape: an executed,
	// archived run history, a terminal durable citation, and a live successor
	// version of the cited workflow.
	retiredTemplate := func(templateName string) db.Pipeline {
		name := workflowName()
		cited := defineVersion(name, 1, false)
		defineVersion(name, 2, true)
		template := saveTemplate(templateName)
		runID, instance := addRun(template, archivedRun(1))
		cite(template, runID, instance, cited, name, 1, "succeeded")
		return template
	}

	runCount := func(template db.Pipeline) int {
		var count int
		Expect(dbConn.QueryRow(`SELECT COUNT(*) FROM pipeline_runs WHERE template_pipeline_id = $1`, template.ID()).Scan(&count)).To(Succeed())
		return count
	}

	instanceCount := func(template db.Pipeline) int {
		var count int
		Expect(dbConn.QueryRow(`
			SELECT COUNT(*) FROM pipelines
			WHERE team_id = $1 AND name = $2 AND id <> $3
		`, defaultTeam.ID(), template.Name(), template.ID()).Scan(&count)).To(Succeed())
		return count
	}

	It("destroys a retired template, its run history, and its instances as one unit", func() {
		name := workflowName()
		cited := defineVersion(name, 1, false)
		defineVersion(name, 2, true)
		template := saveTemplate("retired-template")
		firstRun, firstInstance := addRun(template, archivedRun(1))
		secondRun, secondInstance := addRun(template, archivedRun(2))
		cite(template, firstRun, firstInstance, cited, name, 1, "succeeded")
		cite(template, secondRun, secondInstance, cited, name, 1, "failed")

		destroyed, err := lifecycle.RemoveRetiredWorkflowRunTemplates(ctx, retirement, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(Equal(1))

		Expect(exists(template)).To(BeFalse())
		Expect(runCount(template)).To(Equal(0))
		Expect(instanceCount(template)).To(Equal(0))

		// The durable record outlives the plumbing it names.
		var citations int
		Expect(dbConn.QueryRow(`
			SELECT COUNT(*) FROM agent_workflow_runs WHERE template_pipeline_id = $1
		`, template.ID()).Scan(&citations)).To(Succeed())
		Expect(citations).To(Equal(2))
	})

	It("keeps a template whose cited workflow version has no live successor", func() {
		name := workflowName()
		cited := defineVersion(name, 1, true)
		defineVersion(name, 2, false) // a draft does not supersede
		template := saveTemplate("current-version-template")
		runID, instance := addRun(template, archivedRun(1))
		cite(template, runID, instance, cited, name, 1, "succeeded")

		destroyed, err := lifecycle.RemoveRetiredWorkflowRunTemplates(ctx, retirement, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(Equal(0))
		Expect(exists(template)).To(BeTrue())
	})

	It("keeps a template while any citing durable run is not terminal", func() {
		name := workflowName()
		cited := defineVersion(name, 1, false)
		defineVersion(name, 2, true)
		template := saveTemplate("still-running-template")
		firstRun, firstInstance := addRun(template, archivedRun(1))
		secondRun, secondInstance := addRun(template, archivedRun(2))
		cite(template, firstRun, firstInstance, cited, name, 1, "succeeded")
		cite(template, secondRun, secondInstance, cited, name, 1, "running")

		destroyed, err := lifecycle.RemoveRetiredWorkflowRunTemplates(ctx, retirement, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(Equal(0))
		Expect(exists(template)).To(BeTrue())
	})

	It("never touches an executed template no durable run cites", func() {
		// Resource-capture templates execute without a durable citation, and
		// their output reads join through the template until finalization.
		template := saveTemplate("capture-like-template")
		addRun(template, archivedRun(1))

		destroyed, err := lifecycle.RemoveRetiredWorkflowRunTemplates(ctx, retirement, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(Equal(0))
		Expect(exists(template)).To(BeTrue())
	})

	It("keeps a template while any run is not archived", func() {
		name := workflowName()
		cited := defineVersion(name, 1, false)
		defineVersion(name, 2, true)
		template := saveTemplate("unarchived-run-template")
		run := archivedRun(1)
		run.runArchived = false
		runID, instance := addRun(template, run)
		cite(template, runID, instance, cited, name, 1, "succeeded")

		destroyed, err := lifecycle.RemoveRetiredWorkflowRunTemplates(ctx, retirement, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(Equal(0))
		Expect(exists(template)).To(BeTrue())
	})

	It("keeps a template while any run completed inside the retirement period", func() {
		name := workflowName()
		cited := defineVersion(name, 1, false)
		defineVersion(name, 2, true)
		template := saveTemplate("fresh-run-template")
		run := archivedRun(1)
		run.completedAgo = 5 * time.Minute
		runID, instance := addRun(template, run)
		cite(template, runID, instance, cited, name, 1, "succeeded")

		destroyed, err := lifecycle.RemoveRetiredWorkflowRunTemplates(ctx, retirement, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(Equal(0))
		Expect(exists(template)).To(BeTrue())
	})

	It("keeps a template whose run_retention keep_last still protects runs", func() {
		template := retiredTemplate("keep-last-template")
		_, err := dbConn.Exec(`
			UPDATE pipelines SET run_retention = '{"keep_last": 1}'::jsonb WHERE id = $1
		`, template.ID())
		Expect(err).NotTo(HaveOccurred())

		destroyed, err := lifecycle.RemoveRetiredWorkflowRunTemplates(ctx, retirement, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(Equal(0))
		Expect(exists(template)).To(BeTrue())
	})

	It("keeps a template whose run_retention ttl_days has not fully elapsed", func() {
		template := retiredTemplate("inside-ttl-template")
		// The run completed 24h ago: past the retirement period, inside the TTL.
		_, err := dbConn.Exec(`
			UPDATE pipelines SET run_retention = '{"ttl_days": 7}'::jsonb WHERE id = $1
		`, template.ID())
		Expect(err).NotTo(HaveOccurred())

		destroyed, err := lifecycle.RemoveRetiredWorkflowRunTemplates(ctx, retirement, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(Equal(0))
		Expect(exists(template)).To(BeTrue())
	})

	It("destroys a template whose run_retention ttl_days has fully elapsed", func() {
		template := retiredTemplate("expired-ttl-template")
		_, err := dbConn.Exec(`
			UPDATE pipelines SET run_retention = '{"ttl_days": 1}'::jsonb WHERE id = $1
		`, template.ID())
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`
			UPDATE pipeline_runs SET completed_at = now() - '48 hours'::interval WHERE template_pipeline_id = $1
		`, template.ID())
		Expect(err).NotTo(HaveOccurred())

		destroyed, err := lifecycle.RemoveRetiredWorkflowRunTemplates(ctx, retirement, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(Equal(1))
		Expect(exists(template)).To(BeFalse())
	})

	It("keeps a template whose name still has an instance no run links", func() {
		template := retiredTemplate("stray-instance-template")
		_, _, err := defaultTeam.SavePipeline(
			atc.PipelineRef{Name: template.Name(), InstanceVars: atc.InstanceVars{"run": 999}},
			atc.Config{Jobs: atc.JobConfigs{{Name: "run"}}}, db.ConfigVersion(0), false,
		)
		Expect(err).NotTo(HaveOccurred())

		destroyed, err := lifecycle.RemoveRetiredWorkflowRunTemplates(ctx, retirement, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(Equal(0))
		Expect(exists(template)).To(BeTrue())
	})

	It("keeps a template while a linked instance is not archived", func() {
		name := workflowName()
		cited := defineVersion(name, 1, false)
		defineVersion(name, 2, true)
		template := saveTemplate("live-instance-template")
		run := archivedRun(1)
		run.instanceArchived = false
		runID, instance := addRun(template, run)
		cite(template, runID, instance, cited, name, 1, "succeeded")

		destroyed, err := lifecycle.RemoveRetiredWorkflowRunTemplates(ctx, retirement, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(Equal(0))
		Expect(exists(template)).To(BeTrue())
	})

	It("keeps a template while a linked instance still has an unfinished build", func() {
		name := workflowName()
		cited := defineVersion(name, 1, false)
		defineVersion(name, 2, true)
		template := saveTemplate("unfinished-build-template")
		runID, instance := addRun(template, archivedRun(1))
		cite(template, runID, instance, cited, name, 1, "succeeded")
		_, err := dbConn.Exec(`
			INSERT INTO builds (name, status, team_id, pipeline_id)
			VALUES ('1', 'started', $1, $2)
		`, defaultTeam.ID(), instance.ID())
		Expect(err).NotTo(HaveOccurred())

		destroyed, err := lifecycle.RemoveRetiredWorkflowRunTemplates(ctx, retirement, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(Equal(0))
		Expect(exists(template)).To(BeTrue())
	})

	It("defers the whole team while an admission has not yet linked its template", func() {
		template := retiredTemplate("racing-retired-template")
		name := workflowName()
		admittingDef := defineVersion(name, 1, true)
		_, err := dbConn.Exec(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
				 schema_version, signature_version, definition_content_hash, idempotency_key,
				 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status)
			VALUES ($1, $2, $3, $4, 1, 3, 1, $5, $6, '{}'::jsonb, $5,
			        'ticket', 'GC-2', 'alice', 'admitting')
		`, defaultTeam.ID(), defaultTeam.Name(), admittingDef, name,
			strings.Repeat("e", 64), fmt.Sprintf("retired-key-%d", time.Now().UnixNano()))
		Expect(err).NotTo(HaveOccurred())

		destroyed, err := lifecycle.RemoveRetiredWorkflowRunTemplates(ctx, retirement, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(Equal(0))
		Expect(exists(template)).To(BeTrue())
	})

	It("skips, rather than waits on, a template an execution transaction already locked", func() {
		locked := retiredTemplate("locked-retired-template")
		free := retiredTemplate("unlocked-retired-template")

		admission := postgresRunner.OpenConn()
		defer admission.Close()
		tx, err := admission.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = tx.Rollback() }()
		var lockedID int
		Expect(tx.QueryRow(`SELECT id FROM pipelines WHERE id = $1 FOR UPDATE`, locked.ID()).Scan(&lockedID)).To(Succeed())

		destroyed, err := lifecycle.RemoveRetiredWorkflowRunTemplates(ctx, retirement, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(Equal(1))

		Expect(exists(locked)).To(BeTrue())
		Expect(exists(free)).To(BeFalse())
	})

	It("removes no more than the requested batch per pass", func() {
		first := retiredTemplate("batched-retired-template-one")
		second := retiredTemplate("batched-retired-template-two")

		destroyed, err := lifecycle.RemoveRetiredWorkflowRunTemplates(ctx, retirement, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(Equal(1))

		destroyed, err = lifecycle.RemoveRetiredWorkflowRunTemplates(ctx, retirement, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(Equal(1))

		Expect(exists(first)).To(BeFalse())
		Expect(exists(second)).To(BeFalse())
	})

	It("rejects a non-positive retirement period or an out-of-range batch", func() {
		_, err := lifecycle.RemoveRetiredWorkflowRunTemplates(ctx, 0, 10)
		Expect(err).To(HaveOccurred())

		_, err = lifecycle.RemoveRetiredWorkflowRunTemplates(ctx, retirement, 0)
		Expect(err).To(HaveOccurred())

		_, err = lifecycle.RemoveRetiredWorkflowRunTemplates(ctx, retirement, 100000)
		Expect(err).To(HaveOccurred())
	})
})
