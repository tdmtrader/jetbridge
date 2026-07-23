package db_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("PipelineRunFactory", func() {
	var (
		factory          db.PipelineRunFactory
		template         db.Pipeline
		workflowTemplate db.Pipeline
		createDurable    func() (db.AgentWorkflowRun, db.WorkflowRunTemplateRef, db.AgentWorkflowRunsFactory)
	)

	templateConfig := atc.Config{
		Template: true,
		Params: []atc.ParamSchema{
			{Name: "greeting", Type: "string", Default: "hello"},
		},
		Resources: atc.ResourceConfigs{
			// marker exercises both reserved vars: ((run)) = per-template
			// number, ((run_id)) = global pipeline_runs.id (F30, 2026-07-09)
			{Name: "some-resource", Type: "some-base-resource-type", Source: atc.Source{"some": "((greeting))", "marker": "run-((run))-id-((run_id))"}},
		},
		Jobs: atc.JobConfigs{
			{
				Name: "entry",
				PlanSequence: []atc.Step{
					{Config: &atc.TaskStep{Name: "t", ConfigPath: "task.yml"}},
				},
			},
			{
				Name: "downstream",
				PlanSequence: []atc.Step{
					{Config: &atc.GetStep{Name: "some-resource", Passed: []string{"entry"}, Trigger: true}},
				},
			},
		},
	}
	workflowTemplateConfig := atc.Config{
		Template: true,
		Params: []atc.ParamSchema{
			{Name: "greeting", Type: "string", Default: "hello"},
		},
		Resources: atc.ResourceConfigs{
			{Name: "some-resource", Type: "some-base-resource-type", Source: atc.Source{"some": "((greeting))"}},
		},
		Jobs: atc.JobConfigs{{
			Name: "run",
			PlanSequence: []atc.Step{
				{Config: &atc.TaskStep{Name: "t", ConfigPath: "task.yml"}},
			},
		}},
	}

	BeforeEach(func() {
		// logger and checkFactory are db-suite globals (db_suite_test.go:70/:47);
		// the CheckFactory is injected so CreateRun itself enqueues the frozen
		// check set (F27, 2026-07-09)
		factory = db.NewPipelineRunFactory(logger, dbConn, lockFactory, checkFactory)

		var err error
		template, _, err = defaultTeam.SavePipeline(
			atc.PipelineRef{Name: "run-template"}, templateConfig, db.ConfigVersion(0), false)
		Expect(err).ToNot(HaveOccurred())
		workflowTemplate, _, err = db.NewWorkflowRunTemplateFactory(dbConn, lockFactory).SaveWorkflowRunTemplate(
			context.Background(), defaultTeam.ID(), atc.PipelineRef{Name: "workflow-run-template"}, workflowTemplateConfig)
		Expect(err).ToNot(HaveOccurred())

		createDurable = func() (db.AgentWorkflowRun, db.WorkflowRunTemplateRef, db.AgentWorkflowRunsFactory) {
			canonical, err := workflowTemplateConfig.CanonicalJSON()
			Expect(err).NotTo(HaveOccurred())
			targetHash, err := workflow.TargetConfigHash(workflowTemplateConfig)
			Expect(err).NotTo(HaveOccurred())
			definitionName := fmt.Sprintf("pipeline-run-workflow-%d", time.Now().UnixNano())
			var definitionID int
			Expect(dbConn.QueryRow(`
				INSERT INTO agent_workflow_definitions
					(name, version, content_hash, definition, created_by, schema_version, signature_version)
				VALUES ($1, 1, $2, 'schema_version: 3', 'alice', 3, 1)
				RETURNING id
			`, definitionName, strings.Repeat("a", 64)).Scan(&definitionID)).To(Succeed())

			runStore := db.NewAgentWorkflowRunsFactory(dbConn)
			durable, created, err := runStore.CreateWithInputs(context.Background(), db.AgentWorkflowRunCreateRequest{
				TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(),
				WorkflowDefinitionID: definitionID, WorkflowName: definitionName, WorkflowVersion: 1,
				SchemaVersion: 3, SignatureVersion: 1, DefinitionContentHash: strings.Repeat("a", 64),
				IdempotencyKey:      fmt.Sprintf("pipeline-run-workflow-%d", time.Now().UnixNano()),
				ParameterizedConfig: json.RawMessage(canonical), ParameterizedConfigHash: targetHash,
				OriginKind: "manual", CreatedBy: "alice", Status: db.AgentWorkflowRunStatusAdmitting,
				Inputs: map[string]snapshot.SnapshotRef{},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(created).To(BeTrue())
			return durable, db.WorkflowRunTemplateRef{
				PipelineID: workflowTemplate.ID(), TeamID: defaultTeam.ID(), Name: workflowTemplate.Name(),
				ConfigVersion: int(workflowTemplate.ConfigVersion()), FullHash: targetHash,
			}, runStore
		}
	})

	It("creates numbered runs with materialized instance pipelines and entry builds", func() {
		run, err := factory.CreateRun(template.ID(), nil, "some-user")
		Expect(err).ToNot(HaveOccurred())

		Expect(run.Number()).To(Equal(1))
		Expect(run.Status()).To(Equal(db.PipelineRunRunning))
		Expect(run.CreatedBy()).To(Equal("some-user"))
		Expect(run.Params()).To(Equal(map[string]any{"greeting": "hello"}))
		Expect(run.TemplatePipelineID()).To(Equal(template.ID()))

		instance, found, err := run.InstancePipeline()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(instance.InstanceVars()).To(Equal(atc.InstanceVars{"run": float64(1)}))
		Expect(instance.Template()).To(BeTrue())

		instanceConfig, err := instance.Config()
		Expect(err).ToNot(HaveOccurred())
		Expect(instanceConfig.Resources[0].Source["some"]).To(Equal("hello"))
		// F30 (2026-07-09): ((run_id)) resolved to the pre-allocated
		// pipeline_runs.id, ((run)) to the per-template number
		Expect(instanceConfig.Resources[0].Source["marker"]).To(Equal(fmt.Sprintf("run-1-id-%d", run.ID())))
		// the downstream get has passed: [entry], so it KEEPS trigger: true
		// (passed-chain flow); only non-passed gets are stripped
		Expect(instanceConfig.Jobs[1].Inputs()[0].Trigger).To(BeTrue())

		entryJob, found, err := instance.Job("entry")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		pending, err := entryJob.GetPendingBuilds()
		Expect(err).ToNot(HaveOccurred())
		Expect(pending).To(HaveLen(1))

		downstreamJob, _, err := instance.Job("downstream")
		Expect(err).ToNot(HaveOccurred())
		pending, err = downstreamJob.GetPendingBuilds()
		Expect(err).ToNot(HaveOccurred())
		Expect(pending).To(BeEmpty())

		second, err := factory.CreateRun(template.ID(), map[string]any{"greeting": "hi"}, "some-user")
		Expect(err).ToNot(HaveOccurred())
		Expect(second.Number()).To(Equal(2))
	})

	It("rejects public runs of explicitly owned workflow templates without blocking ordinary templates", func() {
		_, err := factory.CreateRun(workflowTemplate.ID(), nil, "some-user")
		Expect(err).To(MatchError(db.ErrWorkflowRunOwnedPipeline))
		workflowJob, found, err := workflowTemplate.Job("run")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		_, err = workflowJob.CreateBuild("some-user")
		Expect(err).To(MatchError(db.ErrWorkflowRunOwnedPipeline))

		var allocated int
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM pipeline_runs WHERE template_pipeline_id = $1
		`, workflowTemplate.ID()).Scan(&allocated)).To(Succeed())
		Expect(allocated).To(BeZero())

		_, err = factory.CreateRun(template.ID(), nil, "some-user")
		Expect(err).NotTo(HaveOccurred())
	})

	It("executes an exact server-owned resource-capture template with one durable entry build", func() {
		captureConfig := atc.Config{
			Template: true,
			Jobs: atc.JobConfigs{{
				Name: "capture",
				PlanSequence: []atc.Step{{Config: &atc.TaskStep{
					Name:   "seal-snapshot",
					Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "/bin/true"}},
				}}},
			}},
		}
		captureTemplate, created, err := db.NewWorkflowRunTemplateFactory(dbConn, lockFactory).SaveWorkflowRunTemplate(
			context.Background(), defaultTeam.ID(),
			atc.PipelineRef{Name: "agent-resource-capture-1234567890abcdef12345678"}, captureConfig,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		fullHash, err := workflow.TargetConfigHash(captureConfig)
		Expect(err).NotTo(HaveOccurred())
		serverFactory, ok := factory.(interface {
			CreateRunForServerTemplate(context.Context, db.WorkflowRunTemplateRef, map[string]any, string) (db.PipelineRun, error)
		})
		Expect(ok).To(BeTrue())
		run, err := serverFactory.CreateRunForServerTemplate(context.Background(), db.WorkflowRunTemplateRef{
			PipelineID: captureTemplate.ID(), TeamID: defaultTeam.ID(), Name: captureTemplate.Name(),
			ConfigVersion: int(captureTemplate.ConfigVersion()), FullHash: fullHash,
		}, nil, "alice")
		Expect(err).NotTo(HaveOccurred())
		Expect(run.TemplatePipelineID()).To(Equal(captureTemplate.ID()))
		instance, found, err := run.InstancePipeline()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		job, found, err := instance.Job("capture")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		builds, err := job.GetPendingBuilds()
		Expect(err).NotTo(HaveOccurred())
		Expect(builds).To(HaveLen(1))

		_, err = factory.CreateRun(captureTemplate.ID(), nil, "mallory")
		Expect(err).To(MatchError(db.ErrWorkflowRunOwnedPipeline))
	})

	It("rejects durable execution through an exact but unowned template", func() {
		durable, _, _ := createDurable()
		unowned, _, err := defaultTeam.SavePipeline(
			atc.PipelineRef{Name: "unowned-workflow-template"}, workflowTemplateConfig, db.ConfigVersion(0), false,
		)
		Expect(err).NotTo(HaveOccurred())
		targetHash, err := workflow.TargetConfigHash(workflowTemplateConfig)
		Expect(err).NotTo(HaveOccurred())

		_, created, err := factory.CreateRunForWorkflowRun(
			context.Background(), durable.ID,
			db.WorkflowRunTemplateRef{
				PipelineID: unowned.ID(), TeamID: unowned.TeamID(), Name: unowned.Name(),
				ConfigVersion: int(unowned.ConfigVersion()), FullHash: targetHash,
			},
			nil, "alice", func(context.Context, int) error { return nil },
		)
		Expect(err).To(MatchError("db: workflow-run template reference drifted or collided"))
		Expect(created).To(BeFalse())
	})

	It("creates and links a workflow-owned run and its entry builds in one transaction", func() {
		durable, templateRef, runStore := createDurable()

		execution, created, err := factory.CreateRunForWorkflowRun(
			context.Background(), durable.ID,
			templateRef,
			nil, "alice", func(context.Context, int) error { return nil },
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		Expect(execution.PipelineRun).NotTo(BeNil())
		Expect(execution.EntryBuildIDs).To(HaveLen(1))

		stored, found, err := runStore.Get(context.Background(), defaultTeam.ID(), durable.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(stored.Status).To(Equal(db.AgentWorkflowRunStatusAdmitting))
		Expect(stored.PipelineRunID).To(Equal(func() *int { value := execution.PipelineRun.ID(); return &value }()))
		expectedPlannedBuildID := int64(execution.EntryBuildIDs[0])
		Expect(stored.PlannedBuildID).To(Equal(&expectedPlannedBuildID))
		Expect(stored.StartedAt).To(BeNil())
		Expect(stored.ConcreteConfig).To(MatchJSON(execution.InstanceCanonicalJSON))
		Expect(stored.ConcreteConfigHash).To(Equal(&execution.InstanceConfigHash))
		instanceSum := sha256.Sum256(append([]byte("workflow-instance-config/v1\x00"), execution.InstanceCanonicalJSON...))
		Expect(execution.InstanceConfigHash).To(Equal(fmt.Sprintf("%x", instanceSum[:])))
	})

	It("keeps the workflow-owned instance cleanup path available", func() {
		durable, templateRef, _ := createDurable()
		execution, created, err := factory.CreateRunForWorkflowRun(
			context.Background(), durable.ID, templateRef, nil, "alice",
			func(context.Context, int) error { return nil },
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())

		Expect(execution.PipelineRun.Archive()).To(Succeed())
		Expect(execution.PipelineRun.Archived()).To(BeTrue())
		instance, found, err := execution.PipelineRun.InstancePipeline()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(instance.Archived()).To(BeTrue())
		_, err = workflowTemplate.Reload()
		Expect(err).NotTo(HaveOccurred())
		Expect(workflowTemplate.Archived()).To(BeFalse())
	})

	It("rolls back the execution link, instance, builds, and selected build when the callback fails", func() {
		durable, templateRef, runStore := createDurable()
		callbackErr := errors.New("secret attachment failed")
		_, created, err := factory.CreateRunForWorkflowRun(
			context.Background(), durable.ID, templateRef, nil, "alice",
			func(context.Context, int) error { return callbackErr },
		)
		Expect(err).To(MatchError(callbackErr))
		Expect(created).To(BeFalse())

		stored, found, err := runStore.Get(context.Background(), defaultTeam.ID(), durable.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(stored.Status).To(Equal(db.AgentWorkflowRunStatusAdmitting))
		Expect(stored.PipelineRunID).To(BeNil())
		Expect(stored.TemplatePipelineID).To(BeNil())
		Expect(stored.InstancePipelineID).To(BeNil())
		Expect(stored.ConcreteConfig).To(BeEmpty())
		Expect(stored.ConcreteConfigHash).To(BeNil())
		Expect(stored.PlannedBuildID).To(BeNil())

		var pipelineRuns, instances, builds int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM pipeline_runs WHERE template_pipeline_id = $1`, workflowTemplate.ID()).Scan(&pipelineRuns)).To(Succeed())
		Expect(dbConn.QueryRow(`SELECT count(*) FROM pipelines WHERE team_id = $1 AND name = $2 AND instance_vars IS NOT NULL`, defaultTeam.ID(), workflowTemplate.Name()).Scan(&instances)).To(Succeed())
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM builds b
			JOIN pipelines p ON p.id = b.pipeline_id
			WHERE p.team_id = $1 AND p.name = $2 AND p.instance_vars IS NOT NULL
		`, defaultTeam.ID(), workflowTemplate.Name()).Scan(&builds)).To(Succeed())
		Expect(pipelineRuns).To(BeZero())
		Expect(instances).To(BeZero())
		Expect(builds).To(BeZero())

		execution, created, err := factory.CreateRunForWorkflowRun(
			context.Background(), durable.ID, templateRef, nil, "alice",
			func(context.Context, int) error { return nil },
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		Expect(execution.PipelineRun.Number()).To(Equal(1), "rolled-back number allocation must not leave a gap")
	})

	It("returns an exact committed replay without invoking the callback twice", func() {
		durable, templateRef, _ := createDurable()
		callbackCalls := 0
		callback := func(context.Context, int) error {
			callbackCalls++
			return nil
		}
		first, created, err := factory.CreateRunForWorkflowRun(context.Background(), durable.ID, templateRef, nil, "alice", callback)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		second, created, err := factory.CreateRunForWorkflowRun(context.Background(), durable.ID, templateRef, nil, "alice", callback)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeFalse())
		Expect(second.PipelineRun.ID()).To(Equal(first.PipelineRun.ID()))
		Expect(second.EntryBuildIDs).To(Equal(first.EntryBuildIDs))
		Expect(callbackCalls).To(Equal(1))
	})

	It("materializes from durable bytes even if the same-version template rows were mutated", func() {
		durable, templateRef, _ := createDurable()
		_, err := dbConn.Exec(`UPDATE jobs SET name = 'mutated-run' WHERE pipeline_id = $1 AND name = 'run'`, workflowTemplate.ID())
		Expect(err).NotTo(HaveOccurred())

		execution, created, err := factory.CreateRunForWorkflowRun(
			context.Background(), durable.ID, templateRef, nil, "alice",
			func(context.Context, int) error { return nil },
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		Expect(execution.InstanceConfig.Jobs).To(ContainElement(HaveField("Name", "run")))
		instance, found, err := execution.PipelineRun.InstancePipeline()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		_, found, err = instance.Job("run")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
	})

	It("keeps execution and entry builds invisible until the ownership commit", func() {
		durable, templateRef, runStore := createDurable()
		visibilityConn := postgresRunner.OpenSingleton()
		defer visibilityConn.Close()
		entered := make(chan int, 1)
		release := make(chan struct{})
		type result struct {
			execution db.WorkflowRunExecution
			created   bool
			err       error
		}
		done := make(chan result, 1)
		go func() {
			defer GinkgoRecover()
			execution, created, err := factory.CreateRunForWorkflowRun(
				context.Background(), durable.ID, templateRef, nil, "alice",
				func(_ context.Context, pipelineRunID int) error {
					entered <- pipelineRunID
					<-release
					return nil
				},
			)
			done <- result{execution: execution, created: created, err: err}
		}()

		pipelineRunID := <-entered
		var visibleRuns, visibleInstances, visibleBuilds int
		Expect(visibilityConn.QueryRow(`SELECT count(*) FROM pipeline_runs WHERE id = $1`, pipelineRunID).Scan(&visibleRuns)).To(Succeed())
		Expect(visibilityConn.QueryRow(`SELECT count(*) FROM pipelines WHERE team_id = $1 AND name = $2 AND instance_vars IS NOT NULL`, defaultTeam.ID(), workflowTemplate.Name()).Scan(&visibleInstances)).To(Succeed())
		Expect(visibilityConn.QueryRow(`
			SELECT count(*) FROM builds b
			JOIN pipelines p ON p.id = b.pipeline_id
			WHERE p.team_id = $1 AND p.name = $2 AND p.instance_vars IS NOT NULL
		`, defaultTeam.ID(), workflowTemplate.Name()).Scan(&visibleBuilds)).To(Succeed())
		Expect(visibleRuns).To(BeZero())
		Expect(visibleInstances).To(BeZero())
		Expect(visibleBuilds).To(BeZero())
		var status string
		var visiblePipelineRunID, visiblePlannedBuildID *int
		Expect(visibilityConn.QueryRow(`
			SELECT status, pipeline_run_id, planned_build_id
			FROM agent_workflow_runs
			WHERE id = $1
		`, int64(durable.ID)).Scan(&status, &visiblePipelineRunID, &visiblePlannedBuildID)).To(Succeed())
		Expect(status).To(Equal(string(db.AgentWorkflowRunStatusAdmitting)))
		Expect(visiblePipelineRunID).To(BeNil())
		Expect(visiblePlannedBuildID).To(BeNil())

		close(release)
		outcome := <-done
		Expect(outcome.err).NotTo(HaveOccurred())
		Expect(outcome.created).To(BeTrue())
		Expect(outcome.execution.EntryBuildIDs).NotTo(BeEmpty())
		stored, found, err := runStore.Get(context.Background(), defaultTeam.ID(), durable.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(stored.Status).To(Equal(db.AgentWorkflowRunStatusAdmitting))
		expectedPlannedBuildID := int64(outcome.execution.EntryBuildIDs[0])
		Expect(stored.PlannedBuildID).To(Equal(&expectedPlannedBuildID))
	})

	It("fails closed unless the durable Task 5 config has exactly one run entry job", func() {
		durable, templateRef, _ := createDurable()
		invalid := workflowTemplateConfig
		invalid.Jobs = append(atc.JobConfigs(nil), workflowTemplateConfig.Jobs...)
		invalid.Jobs = append(invalid.Jobs, atc.JobConfig{Name: "extra"})
		canonical, err := invalid.CanonicalJSON()
		Expect(err).NotTo(HaveOccurred())
		hash, err := workflow.TargetConfigHash(invalid)
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`
			UPDATE agent_workflow_runs
			SET parameterized_config = $2, parameterized_config_hash = $3
			WHERE id = $1
		`, int64(durable.ID), canonical, hash)
		Expect(err).NotTo(HaveOccurred())
		templateRef.FullHash = hash

		_, created, err := factory.CreateRunForWorkflowRun(
			context.Background(), durable.ID, templateRef, nil, "alice",
			func(context.Context, int) error { return nil },
		)
		Expect(err).To(MatchError(ContainSubstring("exactly one entry job named run")))
		Expect(created).To(BeFalse())
	})

	It("converges concurrent resumes to one execution and one entry-build set", func() {
		durable, templateRef, _ := createDurable()
		type result struct {
			execution db.WorkflowRunExecution
			created   bool
			err       error
		}
		results := make(chan result, 2)
		var callbackMu sync.Mutex
		callbackCalls := 0
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer GinkgoRecover()
				defer wg.Done()
				execution, created, err := factory.CreateRunForWorkflowRun(
					context.Background(), durable.ID, templateRef, nil, "alice",
					func(context.Context, int) error {
						callbackMu.Lock()
						callbackCalls++
						callbackMu.Unlock()
						return nil
					},
				)
				results <- result{execution: execution, created: created, err: err}
			}()
		}
		wg.Wait()
		close(results)
		var got []result
		for result := range results {
			Expect(result.err).NotTo(HaveOccurred())
			got = append(got, result)
		}
		Expect(got).To(HaveLen(2))
		Expect([]bool{got[0].created, got[1].created}).To(ConsistOf(true, false))
		Expect(got[0].execution.PipelineRun.ID()).To(Equal(got[1].execution.PipelineRun.ID()))
		Expect(got[0].execution.EntryBuildIDs).To(Equal(got[1].execution.EntryBuildIDs))
		callbackMu.Lock()
		defer callbackMu.Unlock()
		Expect(callbackCalls).To(Equal(1))
	})

	// review finding (2026-07-11): AGENT_PIPELINE_RUN_ID reaches the
	// agent-step exec via attacker-writable plan env (F30). Before the exec
	// mounts a run's `agent-run-<id>` secret into an MCP sidecar it gates on
	// this ownership check — a run id may only name its secret from within its
	// OWN instance pipeline, never another team's.
	Describe("RunBelongsToPipeline", func() {
		It("is true only for the run's own materialized instance pipeline", func() {
			run, err := factory.CreateRun(template.ID(), nil, "some-user")
			Expect(err).ToNot(HaveOccurred())

			instanceID, ok := run.InstancePipelineID()
			Expect(ok).To(BeTrue())

			owned, err := factory.RunBelongsToPipeline(run.ID(), instanceID)
			Expect(err).ToNot(HaveOccurred())
			Expect(owned).To(BeTrue())

			// A different pipeline (here the template itself) does not own the run.
			owned, err = factory.RunBelongsToPipeline(run.ID(), template.ID())
			Expect(err).ToNot(HaveOccurred())
			Expect(owned).To(BeFalse())

			// A cross-run grab: some other run's id against this pipeline.
			owned, err = factory.RunBelongsToPipeline(run.ID()+9999, instanceID)
			Expect(err).ToNot(HaveOccurred())
			Expect(owned).To(BeFalse())
		})

		It("is false for non-positive ids", func() {
			owned, err := factory.RunBelongsToPipeline(0, 5)
			Expect(err).ToNot(HaveOccurred())
			Expect(owned).To(BeFalse())

			owned, err = factory.RunBelongsToPipeline(5, 0)
			Expect(err).ToNot(HaveOccurred())
			Expect(owned).To(BeFalse())
		})
	})

	Describe("GetRunByID", func() {
		It("gets a run by its global id (additive for dispatch's reconciler, 2026-07-09)", func() {
			run, err := factory.CreateRun(template.ID(), nil, "some-user")
			Expect(err).ToNot(HaveOccurred())

			got, found, err := factory.GetRunByID(run.ID())
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(got.Number()).To(Equal(run.Number()))

			_, found, err = factory.GetRunByID(999999)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeFalse())
		})
	})

	// review finding (2026-07-11): AGENT_TICKET_ID reaches the agent-step exec
	// via the same attacker-writable plan env as the run id (F30). Before the
	// exec admits a step against a ticket's budget — or attributes its spend
	// into agent_cost_ledger under that ticket — it gates on this linkage
	// check: a claimed ticket counts only when the (already-verified) run was
	// dispatched for it (agent_tickets.pipeline_run_id, contracts §1.7).
	Describe("TicketBelongsToRun", func() {
		It("fails closed when the agent_tickets table is absent (pre-ticket-core DB / downgrade window)", func() {
			// ticket-core's migrations landed at 1773106062-64, so the table
			// exists at HEAD; the to_regclass probe still guards DBs that have
			// not migrated (or were downgraded). Simulate one.
			_, err := dbConn.Exec(`DROP TABLE agent_tickets CASCADE`)
			Expect(err).ToNot(HaveOccurred())

			linked, err := factory.TicketBelongsToRun(7, 42)
			Expect(err).ToNot(HaveOccurred())
			Expect(linked).To(BeFalse())
		})

		It("is true only for the run the ticket is currently dispatched as", func() {
			run, err := factory.CreateRun(template.ID(), nil, "some-user")
			Expect(err).ToNot(HaveOccurred())

			_, err = dbConn.Exec(`INSERT INTO agent_tickets (id, title, repo, pipeline_run_id) VALUES (7, 't', 'r', $1)`, run.ID())
			Expect(err).ToNot(HaveOccurred())

			linked, err := factory.TicketBelongsToRun(7, run.ID())
			Expect(err).ToNot(HaveOccurred())
			Expect(linked).To(BeTrue())

			// someone else's ticket, dispatched as a different run: a step
			// claiming it must never admit against its budget
			_, err = dbConn.Exec(`INSERT INTO agent_tickets (id, title, repo, pipeline_run_id) VALUES (8, 't', 'r', $1)`, run.ID()+9999)
			Expect(err).ToNot(HaveOccurred())

			linked, err = factory.TicketBelongsToRun(8, run.ID())
			Expect(err).ToNot(HaveOccurred())
			Expect(linked).To(BeFalse())

			// a ticket that does not exist at all
			linked, err = factory.TicketBelongsToRun(999, run.ID())
			Expect(err).ToNot(HaveOccurred())
			Expect(linked).To(BeFalse())
		})

		It("is false for non-positive ids", func() {
			linked, err := factory.TicketBelongsToRun(0, 5)
			Expect(err).ToNot(HaveOccurred())
			Expect(linked).To(BeFalse())

			linked, err = factory.TicketBelongsToRun(5, 0)
			Expect(err).ToNot(HaveOccurred())
			Expect(linked).To(BeFalse())
		})
	})

	// F30 hardening (2026-07-18): agent_tickets.pipeline_run_id is
	// attacker-writable through PUT .../tickets/:id/state. The transition
	// API gates HTTP-supplied run ids on this check — a run id may only be
	// recorded when it names a run of the ticket's OWN agent-ticket-<id>
	// template on the main team (the dispatch naming convention).
	Describe("RunBelongsToTicketTemplate", func() {
		var ticketTemplate db.Pipeline

		BeforeEach(func() {
			mainTeam, found, err := teamFactory.FindTeam(atc.DefaultTeamName)
			Expect(err).ToNot(HaveOccurred())
			if !found {
				mainTeam, err = teamFactory.CreateTeam(atc.Team{Name: atc.DefaultTeamName})
				Expect(err).ToNot(HaveOccurred())
			}
			ticketTemplate, _, err = mainTeam.SavePipeline(
				atc.PipelineRef{Name: "agent-ticket-7"}, templateConfig, db.ConfigVersion(0), false)
			Expect(err).ToNot(HaveOccurred())
		})

		It("is true only for runs of the ticket's own agent-ticket-<id> template", func() {
			run, err := factory.CreateRun(ticketTemplate.ID(), nil, "some-user")
			Expect(err).ToNot(HaveOccurred())

			owned, err := factory.RunBelongsToTicketTemplate(7, run.ID())
			Expect(err).ToNot(HaveOccurred())
			Expect(owned).To(BeTrue())

			// someone else's ticket pointing at this run
			owned, err = factory.RunBelongsToTicketTemplate(8, run.ID())
			Expect(err).ToNot(HaveOccurred())
			Expect(owned).To(BeFalse())

			// a victim run from an unrelated template (the suite's shared
			// run-template fixture on default-team)
			victim, err := factory.CreateRun(template.ID(), nil, "some-user")
			Expect(err).ToNot(HaveOccurred())
			owned, err = factory.RunBelongsToTicketTemplate(7, victim.ID())
			Expect(err).ToNot(HaveOccurred())
			Expect(owned).To(BeFalse())

			// a run id that does not exist at all
			owned, err = factory.RunBelongsToTicketTemplate(7, run.ID()+9999)
			Expect(err).ToNot(HaveOccurred())
			Expect(owned).To(BeFalse())
		})

		It("does not trust a same-named template on another team", func() {
			imposter, _, err := defaultTeam.SavePipeline(
				atc.PipelineRef{Name: "agent-ticket-7"}, templateConfig, db.ConfigVersion(0), false)
			Expect(err).ToNot(HaveOccurred())

			run, err := factory.CreateRun(imposter.ID(), nil, "some-user")
			Expect(err).ToNot(HaveOccurred())

			owned, err := factory.RunBelongsToTicketTemplate(7, run.ID())
			Expect(err).ToNot(HaveOccurred())
			Expect(owned).To(BeFalse())
		})

		It("is false for non-positive ids", func() {
			owned, err := factory.RunBelongsToTicketTemplate(0, 5)
			Expect(err).ToNot(HaveOccurred())
			Expect(owned).To(BeFalse())

			owned, err = factory.RunBelongsToTicketTemplate(5, 0)
			Expect(err).ToNot(HaveOccurred())
			Expect(owned).To(BeFalse())
		})
	})

	// C3 (UI audit 2026-07-17): once a ticket reaches a terminal state its
	// agent-ticket-<id> pipelines are dead dashboard cards. The lifecycler
	// archives them via these two selections.
	Describe("RunsForTerminalTickets / TemplatesForTerminalTickets", func() {
		// The linkage only trusts the dispatch naming convention: the
		// ticket's own `agent-ticket-<id>` template on the main team.
		// pipeline_run_id is caller-writable (F30), so anything else —
		// including the suite's shared `run-template` fixture — must never
		// be selected for archival.
		var ticketTemplate db.Pipeline

		BeforeEach(func() {
			mainTeam, found, err := teamFactory.FindTeam(atc.DefaultTeamName)
			Expect(err).ToNot(HaveOccurred())
			if !found {
				mainTeam, err = teamFactory.CreateTeam(atc.Team{Name: atc.DefaultTeamName})
				Expect(err).ToNot(HaveOccurred())
			}
			ticketTemplate, _, err = mainTeam.SavePipeline(
				atc.PipelineRef{Name: "agent-ticket-7"}, templateConfig, db.ConfigVersion(0), false)
			Expect(err).ToNot(HaveOccurred())
		})

		runIDs := func(runs []db.PipelineRun) []int {
			ids := []int{}
			for _, r := range runs {
				ids = append(ids, r.ID())
			}
			return ids
		}

		pipelineIDs := func(pipelines []db.Pipeline) []int {
			ids := []int{}
			for _, p := range pipelines {
				ids = append(ids, p.ID())
			}
			return ids
		}

		It("no-ops when the agent_tickets table is absent (pre-ticket-core DB / downgrade window)", func() {
			_, err := dbConn.Exec(`DROP TABLE agent_tickets CASCADE`)
			Expect(err).ToNot(HaveOccurred())

			runs, err := factory.RunsForTerminalTickets()
			Expect(err).ToNot(HaveOccurred())
			Expect(runs).To(BeEmpty())

			templates, err := factory.TemplatesForTerminalTickets()
			Expect(err).ToNot(HaveOccurred())
			Expect(templates).To(BeEmpty())
		})

		It("selects every attempt's run plus the base template, exactly while the ticket is terminal", func() {
			// two attempts = two run instances of the same template
			first, err := factory.CreateRun(ticketTemplate.ID(), nil, "some-user")
			Expect(err).ToNot(HaveOccurred())
			Expect(first.Finish(db.PipelineRunSucceeded)).To(Succeed())

			second, err := factory.CreateRun(ticketTemplate.ID(), nil, "some-user")
			Expect(err).ToNot(HaveOccurred())
			Expect(second.Finish(db.PipelineRunFailed)).To(Succeed())

			// latest attempt is what the ticket links (contracts §1.7)
			_, err = dbConn.Exec(
				`INSERT INTO agent_tickets (id, title, repo, pipeline_run_id) VALUES (7, 't', 'r', $1)`,
				second.ID())
			Expect(err).ToNot(HaveOccurred())

			// every live state keeps the pipelines alone
			for _, state := range []string{"draft", "queued", "running", "needs_review", "sent_back", "failed", "errored"} {
				_, err = dbConn.Exec(`UPDATE agent_tickets SET state = $1 WHERE id = 7`, state)
				Expect(err).ToNot(HaveOccurred())

				runs, err := factory.RunsForTerminalTickets()
				Expect(err).ToNot(HaveOccurred())
				Expect(runs).To(BeEmpty(), "state %s must not archive runs", state)

				templates, err := factory.TemplatesForTerminalTickets()
				Expect(err).ToNot(HaveOccurred())
				Expect(templates).To(BeEmpty(), "state %s must not archive the template", state)
			}

			// every terminal state selects BOTH attempts' runs and the template
			for _, state := range []string{"merged", "merged_with_fixes", "abandoned", "concluded"} {
				_, err = dbConn.Exec(`UPDATE agent_tickets SET state = $1 WHERE id = 7`, state)
				Expect(err).ToNot(HaveOccurred())

				runs, err := factory.RunsForTerminalTickets()
				Expect(err).ToNot(HaveOccurred())
				Expect(runIDs(runs)).To(ConsistOf(first.ID(), second.ID()), "state %s", state)

				templates, err := factory.TemplatesForTerminalTickets()
				Expect(err).ToNot(HaveOccurred())
				Expect(pipelineIDs(templates)).To(ConsistOf(ticketTemplate.ID()), "state %s", state)
			}

			// archiving converges to steady-state empty selections
			runs, err := factory.RunsForTerminalTickets()
			Expect(err).ToNot(HaveOccurred())
			for _, run := range runs {
				Expect(run.Archive()).To(Succeed())
			}
			templates, err := factory.TemplatesForTerminalTickets()
			Expect(err).ToNot(HaveOccurred())
			for _, p := range templates {
				Expect(p.Archive()).To(Succeed())
			}

			runs, err = factory.RunsForTerminalTickets()
			Expect(err).ToNot(HaveOccurred())
			Expect(runs).To(BeEmpty())

			templates, err = factory.TemplatesForTerminalTickets()
			Expect(err).ToNot(HaveOccurred())
			Expect(templates).To(BeEmpty())
		})

		It("holds back both the run and the template while the run is still aggregate-running", func() {
			run, err := factory.CreateRun(ticketTemplate.ID(), nil, "some-user")
			Expect(err).ToNot(HaveOccurred())
			// deliberately NOT finished: status stays 'running'

			_, err = dbConn.Exec(
				`INSERT INTO agent_tickets (id, title, repo, state, pipeline_run_id) VALUES (7, 't', 'r', 'merged', $1)`,
				run.ID())
			Expect(err).ToNot(HaveOccurred())

			runs, err := factory.RunsForTerminalTickets()
			Expect(err).ToNot(HaveOccurred())
			Expect(runs).To(BeEmpty())

			templates, err := factory.TemplatesForTerminalTickets()
			Expect(err).ToNot(HaveOccurred())
			Expect(templates).To(BeEmpty())

			// once the Finish pass completes the run, the whole group goes
			Expect(run.Finish(db.PipelineRunSucceeded)).To(Succeed())

			runs, err = factory.RunsForTerminalTickets()
			Expect(err).ToNot(HaveOccurred())
			Expect(runIDs(runs)).To(ConsistOf(run.ID()))

			templates, err = factory.TemplatesForTerminalTickets()
			Expect(err).ToNot(HaveOccurred())
			Expect(pipelineIDs(templates)).To(ConsistOf(ticketTemplate.ID()))
		})

		It("never selects a pipeline that is not the ticket's own agent-ticket template (poisoned pipeline_run_id, F30)", func() {
			// a caller-writable pipeline_run_id pointed at a run of some
			// unrelated template — here the suite's shared run-template on
			// another team — must never mark that victim for archival
			victim, err := factory.CreateRun(template.ID(), nil, "some-user")
			Expect(err).ToNot(HaveOccurred())
			Expect(victim.Finish(db.PipelineRunSucceeded)).To(Succeed())

			_, err = dbConn.Exec(
				`INSERT INTO agent_tickets (id, title, repo, state, pipeline_run_id) VALUES (7, 't', 'r', 'abandoned', $1)`,
				victim.ID())
			Expect(err).ToNot(HaveOccurred())

			runs, err := factory.RunsForTerminalTickets()
			Expect(err).ToNot(HaveOccurred())
			Expect(runs).To(BeEmpty())

			templates, err := factory.TemplatesForTerminalTickets()
			Expect(err).ToNot(HaveOccurred())
			Expect(templates).To(BeEmpty())
		})

		It("ignores terminal tickets that were never dispatched (no pipeline_run_id linkage)", func() {
			_, err := dbConn.Exec(
				`INSERT INTO agent_tickets (id, title, repo, state) VALUES (7, 't', 'r', 'abandoned')`)
			Expect(err).ToNot(HaveOccurred())

			runs, err := factory.RunsForTerminalTickets()
			Expect(err).ToNot(HaveOccurred())
			Expect(runs).To(BeEmpty())

			templates, err := factory.TemplatesForTerminalTickets()
			Expect(err).ToNot(HaveOccurred())
			Expect(templates).To(BeEmpty())
		})
	})

	// review finding (2026-07-11): a pipeline instance {name, {"run": N}} can
	// pre-exist (e.g. a user ran fly set-pipeline with those instance vars).
	// CreateRun used to call savePipeline with from=0 assuming the instance
	// never pre-exists; the tx failed and rolled back — INCLUDING the
	// run-number allocation — so every retry hit the same existing instance
	// and the template wedged permanently. The allocator must skip past
	// existing instances instead.
	It("skips run numbers whose pipeline instance already exists", func() {
		_, _, err := defaultTeam.SavePipeline(
			atc.PipelineRef{Name: "run-template", InstanceVars: atc.InstanceVars{"run": 1}},
			templateConfig, db.ConfigVersion(0), false)
		Expect(err).ToNot(HaveOccurred())

		run, err := factory.CreateRun(template.ID(), nil, "some-user")
		Expect(err).ToNot(HaveOccurred())
		Expect(run.Number()).To(Equal(2))

		instance, found, err := run.InstancePipeline()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(instance.InstanceVars()).To(Equal(atc.InstanceVars{"run": float64(2)}))

		second, err := factory.CreateRun(template.ID(), nil, "some-user")
		Expect(err).ToNot(HaveOccurred())
		Expect(second.Number()).To(Equal(3))
	})

	It("rejects invalid params and non-templates", func() {
		_, err := factory.CreateRun(template.ID(), map[string]any{"bogus": "x"}, "u")
		Expect(err).To(MatchError(ContainSubstring(`unknown param "bogus"`)))

		_, err = factory.CreateRun(defaultPipeline.ID(), nil, "u")
		Expect(err).To(MatchError(db.ErrNotATemplate))
	})

	It("gets and lists runs", func() {
		one, err := factory.CreateRun(template.ID(), nil, "u")
		Expect(err).ToNot(HaveOccurred())
		_, err = factory.CreateRun(template.ID(), nil, "u")
		Expect(err).ToNot(HaveOccurred())

		got, found, err := factory.GetRun(template.ID(), 1)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got.ID()).To(Equal(one.ID()))

		_, found, err = factory.GetRun(template.ID(), 99)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())

		runs, err := factory.ListRuns(template.ID(), 10)
		Expect(err).ToNot(HaveOccurred())
		Expect(runs).To(HaveLen(2))
		Expect(runs[0].Number()).To(Equal(2)) // newest first

		running, err := factory.RunningRuns()
		Expect(err).ToNot(HaveOccurred())
		Expect(len(running)).To(BeNumerically(">=", 2))
	})

	// F27 (2026-07-09): the frozen-check enqueue lives in the FACTORY, not
	// the API handler — lidar excludes template pipelines, so a run created
	// by an in-process consumer (dispatch, experiments) whose entry job has
	// a get step would otherwise pend forever on an empty version set.
	It("enqueues the frozen check set at creation so get-step entry jobs get versions", func() {
		getEntryConfig := atc.Config{
			Template: true,
			Resources: atc.ResourceConfigs{
				{Name: "some-resource", Type: "some-base-resource-type", Source: atc.Source{"some": "source"}},
			},
			Jobs: atc.JobConfigs{
				{Name: "entry-get", PlanSequence: []atc.Step{
					{Config: &atc.GetStep{Name: "some-resource", Trigger: true}},
				}},
			},
		}
		getTemplate, _, err := defaultTeam.SavePipeline(
			atc.PipelineRef{Name: "frozen-check-template"}, getEntryConfig, db.ConfigVersion(0), false)
		Expect(err).ToNot(HaveOccurred())

		run, err := factory.CreateRun(getTemplate.ID(), nil, "some-user")
		Expect(err).ToNot(HaveOccurred())

		instance, found, err := run.InstancePipeline()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())

		resource, found, err := instance.Resource("some-resource")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())

		// exactly one manually-triggered check build persisted for the
		// instance resource (TryCreateCheck toDB=true writes a builds row
		// with resource_id set)
		var checkBuilds int
		err = dbConn.QueryRow(
			`SELECT COUNT(*) FROM builds WHERE resource_id = $1`, resource.ID()).
			Scan(&checkBuilds)
		Expect(err).ToNot(HaveOccurred())
		Expect(checkBuilds).To(Equal(1))
	})
})
