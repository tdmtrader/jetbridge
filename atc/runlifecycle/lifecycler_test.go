package runlifecycle_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/runlifecycle"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Lifecycler persisted PostgreSQL state", func() {
	var (
		fixture    *lifecycleDB
		lifecycler *runlifecycle.Lifecycler
	)

	const retirement = 720 * time.Hour

	templateConfig := func(retention *atc.RunRetentionConfig) atc.Config {
		return atc.Config{
			Template:     true,
			RunRetention: retention,
			Jobs: atc.JobConfigs{
				{Name: "entry", PlanSequence: []atc.Step{{Config: &atc.TaskStep{Name: "entry-task", ConfigPath: "task.yml"}}}},
				{Name: "second", PlanSequence: []atc.Step{{Config: &atc.TaskStep{Name: "second-task", ConfigPath: "task.yml"}}}},
			},
		}
	}

	saveTemplate := func(name string, retention *atc.RunRetentionConfig) db.Pipeline {
		GinkgoHelper()
		template, created, err := fixture.Team.SavePipeline(
			atc.PipelineRef{Name: name}, templateConfig(retention), db.ConfigVersion(0), false,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		return template
	}

	createRun := func(template db.Pipeline) db.PipelineRun {
		GinkgoHelper()
		run, err := fixture.Runs.CreateRun(template.ID(), nil, "runlifecycle-test")
		Expect(err).NotTo(HaveOccurred())
		return run
	}

	statusForBuild := func(status db.BuildStatus) db.PipelineRunStatus {
		switch status {
		case db.BuildStatusFailed:
			return db.PipelineRunFailed
		case db.BuildStatusErrored:
			return db.PipelineRunErrored
		case db.BuildStatusAborted:
			return db.PipelineRunAborted
		default:
			return db.PipelineRunSucceeded
		}
	}

	quiesceRun := func(run db.PipelineRun, terminalBuildStatus db.BuildStatus) {
		GinkgoHelper()

		instance, found, err := run.InstancePipeline()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		jobs, err := instance.Jobs()
		Expect(err).NotTo(HaveOccurred())
		for _, job := range jobs {
			pending, err := job.GetPendingBuilds()
			Expect(err).NotTo(HaveOccurred())
			for _, build := range pending {
				Expect(build.Finish(terminalBuildStatus)).To(Succeed())
			}
		}
		_, err = fixture.Conn.Exec(`
			UPDATE jobs
			SET last_scheduled = schedule_requested
			WHERE pipeline_id = $1
		`, instance.ID())
		Expect(err).NotTo(HaveOccurred())

		status, complete, err := run.CheckComplete()
		Expect(err).NotTo(HaveOccurred())
		Expect(complete).To(BeTrue())
		Expect(status).To(Equal(statusForBuild(terminalBuildStatus)))
		Expect(run.Status()).To(Equal(db.PipelineRunRunning))
	}

	completeRun := func(run db.PipelineRun, terminalBuildStatus db.BuildStatus) {
		GinkgoHelper()
		quiesceRun(run, terminalBuildStatus)
		Expect(run.Finish(statusForBuild(terminalBuildStatus))).To(Succeed())
	}

	reloadRun := func(template db.Pipeline, run db.PipelineRun) db.PipelineRun {
		GinkgoHelper()
		reloaded, found, err := fixture.Runs.GetRun(template.ID(), run.Number())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		return reloaded
	}

	assertArchived := func(template db.Pipeline, run db.PipelineRun, archived bool) {
		GinkgoHelper()
		reloaded := reloadRun(template, run)
		Expect(reloaded.Archived()).To(Equal(archived))
		instance, found, err := reloaded.InstancePipeline()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(instance.Archived()).To(Equal(archived))
	}

	type retiredTemplateRun struct {
		template db.Pipeline
		run      db.PipelineRun
		durable  db.AgentWorkflowRun
	}

	createRetiredTemplateRun := func() retiredTemplateRun {
		GinkgoHelper()
		ctx := context.Background()
		workflowName := fmt.Sprintf("runlifecycle-retired-%d", time.Now().UnixNano())
		definitionSource := fmt.Sprintf(`schema_version: 3
name: %s
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: run
    function_id: run
    prompt: test
`, workflowName)
		definitionHash := workflow.Manifest{
			workflow.LegacyWorkflowFileName: definitionSource,
		}.Hash()
		var definitionID int
		Expect(fixture.Conn.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(definition_kind, name, version, content_hash, definition, created_by,
				 schema_version, signature_version, live)
			VALUES ('workflow', $1, 1, $2, $3, 'alice', 3, 1, false)
			RETURNING id
		`, workflowName, definitionHash, definitionSource).Scan(&definitionID)).To(Succeed())

		rendered, err := workflow.RenderFunction(workflow.FunctionTarget{
			Kind:                 workflow.TargetWorkflow,
			WorkflowDefinitionID: definitionID,
			WorkflowName:         workflowName,
			WorkflowVersion:      1,
			SignatureVersion:     1,
			Function: workflow.FunctionConfig{
				SignatureVersion: 1,
				Plan: []atc.Step{{Config: &atc.TaskStep{
					Name:       "run",
					FunctionID: "run",
					Config: &atc.TaskConfig{
						Platform: "linux",
						ImageResource: &atc.ImageResource{
							Type:    "registry-image",
							Source:  atc.Source{"repository": "example/workflow-task"},
							Version: atc.Version{"digest": "sha256:immutable"},
						},
						Run: atc.TaskRunConfig{Path: "/bin/true"},
					},
				}}},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		canonical, err := rendered.Config.CanonicalJSON()
		Expect(err).NotTo(HaveOccurred())
		durable, created, err := fixture.WorkflowRuns.CreateWithInputs(ctx, db.AgentWorkflowRunCreateRequest{
			DefinitionKind:          workflow.DefinitionKindWorkflow,
			TeamID:                  fixture.Team.ID(),
			TeamName:                fixture.Team.Name(),
			WorkflowDefinitionID:    definitionID,
			WorkflowName:            workflowName,
			WorkflowVersion:         1,
			SchemaVersion:           3,
			SignatureVersion:        1,
			DefinitionContentHash:   definitionHash,
			IdempotencyKey:          fmt.Sprintf("runlifecycle-retirement-%d", time.Now().UnixNano()),
			ParameterizedConfig:     json.RawMessage(canonical),
			ParameterizedConfigHash: rendered.TargetConfigHash,
			OriginKind:              "manual",
			CreatedBy:               "alice",
			Status:                  db.AgentWorkflowRunStatusAdmitting,
			Inputs:                  map[string]snapshot.SnapshotRef{},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())

		template, created, err := fixture.Templates.SaveWorkflowRunTemplate(
			ctx, fixture.Team.ID(), atc.PipelineRef{Name: rendered.TemplateName}, rendered.Config,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		config, err := template.Config()
		Expect(err).NotTo(HaveOccurred())
		Expect(config.RunRetention).To(BeNil())
		owned, err := fixture.Templates.IsWorkflowRunTemplate(ctx, template.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(owned).To(BeTrue())

		envelope, err := rendered.ExecutionEnvelope(map[string]any{
			"workflow_run_id": durable.ID.String(),
		})
		Expect(err).NotTo(HaveOccurred())
		execution, created, err := fixture.Runs.CreateRunForWorkflowRun(
			ctx,
			durable.ID,
			db.WorkflowRunTemplateRef{
				PipelineID:    template.ID(),
				TeamID:        fixture.Team.ID(),
				Name:          template.Name(),
				ConfigVersion: int(template.ConfigVersion()),
				FullHash:      rendered.TargetConfigHash,
			},
			envelope,
			"alice",
			func(context.Context, int) error { return nil },
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		Expect(execution.EntryBuildIDs).To(HaveLen(1))

		transitioned, err := fixture.WorkflowRuns.Transition(
			ctx,
			durable.ID,
			db.AgentWorkflowRunStatusAdmitting,
			db.AgentWorkflowRunStatusRunning,
			"",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(transitioned).To(BeTrue())
		durable, found, err := fixture.WorkflowRuns.Get(ctx, fixture.Team.ID(), durable.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(durable.Status).To(Equal(db.AgentWorkflowRunStatusRunning))

		run := execution.PipelineRun
		quiesceRun(run, db.BuildStatusErrored)
		Expect(run.Finish(db.PipelineRunErrored)).To(Succeed())

		durable, found, err = fixture.WorkflowRuns.Get(ctx, fixture.Team.ID(), durable.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(durable.ExecutionStatus).NotTo(BeNil())
		Expect(*durable.ExecutionStatus).To(Equal(db.AgentWorkflowRunExecutionStatusErrored))
		capturedExecutionStatus := durable.ExecutionStatus
		_, applied, err := fixture.WorkflowRuns.Finalize(ctx, db.AgentWorkflowRunFinalization{
			WorkflowRunID:           durable.ID,
			ExpectedStatus:          db.AgentWorkflowRunStatusRunning,
			ExpectedExecutionStatus: capturedExecutionStatus,
			ExpectedActualPlanHash:  nil,
			TerminalStatus:          db.AgentWorkflowRunStatusErrored,
			ErrorMessage:            "selected workflow execution errored",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(applied).To(BeTrue())

		successorSource := definitionSource + "\n# successor\n"
		successorHash := workflow.Manifest{
			workflow.LegacyWorkflowFileName: successorSource,
		}.Hash()
		var successorID int
		Expect(fixture.Conn.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(definition_kind, name, version, content_hash, definition, created_by,
				 schema_version, signature_version, live)
			VALUES ('workflow', $1, 2, $2, $3, 'alice', 3, 1, true)
			RETURNING id
		`, workflowName, successorHash, successorSource).Scan(&successorID)).To(Succeed())
		Expect(successorID).To(BeNumerically(">", definitionID))
		// Keep the historical ordering internally consistent when aging the run:
		// completed builds must predate the run's completion, or the preceding
		// reopen pass correctly treats them as newly completed activity.
		_, err = fixture.Conn.Exec(`
			UPDATE builds
			SET end_time = now() - interval '32 days'
			WHERE pipeline_id = (
				SELECT instance_pipeline_id FROM pipeline_runs WHERE id = $1
			) AND completed
		`, run.ID())
		Expect(err).NotTo(HaveOccurred())
		_, err = fixture.Conn.Exec(`
			UPDATE pipeline_runs
			SET completed_at = now() - interval '31 days'
			WHERE id = $1
		`, run.ID())
		Expect(err).NotTo(HaveOccurred())

		durable, found, err = fixture.WorkflowRuns.Get(ctx, fixture.Team.ID(), durable.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(durable.Status).To(Equal(db.AgentWorkflowRunStatusErrored))
		Expect(durable.ExecutionStatus).To(Equal(capturedExecutionStatus))
		Expect(durable.TemplatePipelineID).NotTo(BeNil())
		Expect(*durable.TemplatePipelineID).To(Equal(template.ID()))
		run = reloadRun(template, run)
		Expect(run.Status()).To(Equal(db.PipelineRunErrored))
		completedAt, found := run.CompletedAt()
		Expect(found).To(BeTrue())
		Expect(completedAt).To(BeTemporally("<", time.Now().Add(-30*24*time.Hour)))
		Expect(run.Archived()).To(BeFalse())
		var (
			oldEnough             bool
			ownedWithoutRetention bool
			cited                 bool
			allCitationsRetired   bool
		)
		Expect(fixture.Conn.QueryRow(`
			SELECT
				r.completed_at < now() - $2::interval,
				EXISTS (
					SELECT 1 FROM agent_workflow_run_templates owned
					JOIN pipelines candidate ON candidate.id = owned.pipeline_id
					WHERE candidate.id = r.template_pipeline_id
					  AND candidate.run_retention IS NULL
				),
				EXISTS (
					SELECT 1 FROM agent_workflow_runs citation
					WHERE citation.template_pipeline_id = r.template_pipeline_id
				),
				NOT EXISTS (
					SELECT 1 FROM agent_workflow_runs citation
					WHERE citation.template_pipeline_id = r.template_pipeline_id
					  AND (citation.status NOT IN ('succeeded', 'failed', 'errored', 'aborted')
					       OR NOT EXISTS (
							SELECT 1 FROM agent_workflow_definitions successor
							WHERE successor.name = citation.workflow_name
							  AND successor.definition_kind = 'workflow'
							  AND successor.live
							  AND successor.version > citation.workflow_version
					       ))
				)
			FROM pipeline_runs r
			WHERE r.id = $1
		`, run.ID(), retirement.String()).Scan(
			&oldEnough, &ownedWithoutRetention, &cited, &allCitationsRetired,
		)).To(Succeed())
		Expect(oldEnough).To(BeTrue())
		Expect(ownedWithoutRetention).To(BeTrue())
		Expect(cited).To(BeTrue())
		Expect(allCitationsRetired).To(BeTrue())

		return retiredTemplateRun{template: template, run: run, durable: durable}
	}

	BeforeEach(func() {
		fixture = useLifecycleDB()
		lifecycler = runlifecycle.NewLifecycler(fixture.Runs, retirement)
	})

	It("finishes complete runs with their aggregate status", func() {
		template := saveTemplate("failed-completion", nil)
		complete := createRun(template)
		quiesceRun(complete, db.BuildStatusFailed)
		incomplete := createRun(template)

		Expect(lifecycler.Run(context.Background())).To(Succeed())

		Expect(reloadRun(template, complete).Status()).To(Equal(db.PipelineRunFailed))
		Expect(reloadRun(template, incomplete).Status()).To(Equal(db.PipelineRunRunning))
	})

	It("reopens completed runs with new activity", func() {
		template := saveTemplate("reopen-completed", nil)
		run := createRun(template)
		completeRun(run, db.BuildStatusSucceeded)
		instance, found, err := run.InstancePipeline()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		entry, found, err := instance.Job("entry")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		_, err = entry.CreateBuild("retrigger")
		Expect(err).NotTo(HaveOccurred())

		Expect(lifecycler.Run(context.Background())).To(Succeed())

		reloaded := reloadRun(template, run)
		Expect(reloaded.Status()).To(Equal(db.PipelineRunRunning))
		_, hasCompletedAt := reloaded.CompletedAt()
		Expect(hasCompletedAt).To(BeFalse())
	})

	It("archives expired runs and their instances", func() {
		template := saveTemplate("retention-keep-last", &atc.RunRetentionConfig{KeepLast: 1})
		older := createRun(template)
		completeRun(older, db.BuildStatusSucceeded)
		newer := createRun(template)
		completeRun(newer, db.BuildStatusSucceeded)

		Expect(lifecycler.Run(context.Background())).To(Succeed())

		assertArchived(template, older, true)
		assertArchived(template, newer, false)
	})

	// Generic run lifecycle is preserved after the ticket-template archival
	// passes were removed: finish, reopen, and retention archival remain
	// independent generic passes over three distinct templates.
	It("still finishes, reopens, and archives generic runs", func() {
		finishTemplate := saveTemplate("combined-finish", nil)
		finishing := createRun(finishTemplate)
		quiesceRun(finishing, db.BuildStatusFailed)

		reopenTemplate := saveTemplate("combined-reopen", nil)
		reopening := createRun(reopenTemplate)
		completeRun(reopening, db.BuildStatusSucceeded)
		reopenInstance, found, err := reopening.InstancePipeline()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		reopenJob, found, err := reopenInstance.Job("entry")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		_, err = reopenJob.CreateBuild("retrigger")
		Expect(err).NotTo(HaveOccurred())

		archiveTemplate := saveTemplate("combined-archive", &atc.RunRetentionConfig{KeepLast: 1})
		archiving := createRun(archiveTemplate)
		completeRun(archiving, db.BuildStatusSucceeded)
		kept := createRun(archiveTemplate)
		completeRun(kept, db.BuildStatusSucceeded)

		Expect(lifecycler.Run(context.Background())).To(Succeed())

		Expect(reloadRun(finishTemplate, finishing).Status()).To(Equal(db.PipelineRunFailed))
		reopened := reloadRun(reopenTemplate, reopening)
		Expect(reopened.Status()).To(Equal(db.PipelineRunRunning))
		_, hasCompletedAt := reopened.CompletedAt()
		Expect(hasCompletedAt).To(BeFalse())
		assertArchived(archiveTemplate, archiving, true)
		assertArchived(archiveTemplate, kept, false)
	})

	It("archives runs of retired server-owned templates using the configured period", func() {
		retired := createRetiredTemplateRun()

		Expect(lifecycler.Run(context.Background())).To(Succeed())

		assertArchived(retired.template, retired.run, true)
		reloadedDurable, found, err := fixture.WorkflowRuns.Get(
			context.Background(), fixture.Team.ID(), retired.durable.ID,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(reloadedDurable.Status).To(Equal(db.AgentWorkflowRunStatusErrored))
	})

	It("skips the retirement pass entirely when the period is zero", func() {
		retired := createRetiredTemplateRun()
		disabled := runlifecycle.NewLifecycler(fixture.Runs, 0)

		Expect(disabled.Run(context.Background())).To(Succeed())

		assertArchived(retired.template, retired.run, false)
	})
})

var _ = Describe("Lifecycler selective per-run database errors", func() {
	var (
		factory    *dbfakes.FakePipelineRunFactory
		lifecycler *runlifecycle.Lifecycler
	)

	const retirement = 720 * time.Hour

	BeforeEach(func() {
		// Retained: a healthy clone cannot make only one selected run's method
		// fail while allowing the following run to succeed.
		factory = new(dbfakes.FakePipelineRunFactory)
		lifecycler = runlifecycle.NewLifecycler(factory, retirement)
	})

	It("continues past CheckComplete errors", func() {
		bad := new(dbfakes.FakePipelineRun)
		bad.CheckCompleteReturns("", false, errors.New("boom"))
		good := new(dbfakes.FakePipelineRun)
		good.CheckCompleteReturns(db.PipelineRunSucceeded, true, nil)
		factory.RunningRunsReturns([]db.PipelineRun{bad, good}, nil)

		Expect(lifecycler.Run(context.Background())).To(Succeed())
		Expect(good.FinishCallCount()).To(Equal(1))
	})

	It("continues past Archive errors in the retirement pass", func() {
		bad := new(dbfakes.FakePipelineRun)
		bad.ArchiveReturns(errors.New("boom"))
		good := new(dbfakes.FakePipelineRun)
		factory.RunsOfRetiredTemplatesToArchiveReturns([]db.PipelineRun{bad, good}, nil)

		Expect(lifecycler.Run(context.Background())).To(Succeed())
		Expect(good.ArchiveCallCount()).To(Equal(1))
	})
})
