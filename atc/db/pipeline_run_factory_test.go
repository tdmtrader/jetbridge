package db_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
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
		workflowRendered workflow.RenderedFunction
		envelopeFor      func(snapshot.WorkflowRunID) workflow.ExecutionEnvelope
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
		workflowRendered, err = workflow.RenderFunction(workflow.FunctionTarget{
			Kind: workflow.TargetWorkflow, WorkflowDefinitionID: 1, WorkflowName: "pipeline-run-workflow", WorkflowVersion: 1, SignatureVersion: 1,
			Function: workflow.FunctionConfig{SignatureVersion: 1, Plan: []atc.Step{{Config: &atc.TaskStep{
				Name: "run", FunctionID: "run", Config: &atc.TaskConfig{Platform: "linux",
					ImageResource: &atc.ImageResource{Type: "registry-image", Source: atc.Source{"repository": "example/task"}, Version: atc.Version{"digest": "sha256:immutable"}},
					Run:           atc.TaskRunConfig{Path: "/bin/true"},
				},
			}}}},
		})
		Expect(err).NotTo(HaveOccurred())
		workflowTemplateConfig = workflowRendered.Config
		envelopeFor = func(runID snapshot.WorkflowRunID) workflow.ExecutionEnvelope {
			envelope, err := workflowRendered.ExecutionEnvelope(map[string]any{"workflow_run_id": runID.String()})
			Expect(err).NotTo(HaveOccurred())
			return envelope
		}

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
			definitionSource := fmt.Sprintf(`schema_version: 3
name: %s
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: work
    function_id: work
    prompt: test
`, definitionName)
			definitionHash := workflow.Manifest{
				workflow.LegacyWorkflowFileName: definitionSource,
			}.Hash()
			var definitionID int
			Expect(dbConn.QueryRow(`
				INSERT INTO agent_workflow_definitions
					(name, version, content_hash, definition, created_by, schema_version, signature_version)
				VALUES ($1, 1, $2, $3, 'alice', 3, 1)
				RETURNING id
			`, definitionName, definitionHash, definitionSource).Scan(&definitionID)).To(Succeed())

			runStore := db.NewAgentWorkflowRunsFactory(dbConn)
			durable, created, err := runStore.CreateWithInputs(context.Background(), db.AgentWorkflowRunCreateRequest{
				TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(),
				WorkflowDefinitionID: definitionID, WorkflowName: definitionName, WorkflowVersion: 1,
				SchemaVersion: 3, SignatureVersion: 1, DefinitionContentHash: definitionHash,
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

	Describe("server-owned resource-capture run instances", func() {
		var (
			captureConfig   atc.Config
			captureInstance db.Pipeline
			captureJob      db.Job
			entryBuild      db.Build
		)

		BeforeEach(func() {
			captureConfig = atc.Config{
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
			var found bool
			captureInstance, found, err = run.InstancePipeline()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			captureJob, found, err = captureInstance.Job("capture")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			pending, err := captureJob.GetPendingBuilds()
			Expect(err).NotTo(HaveOccurred())
			Expect(pending).To(HaveLen(1))
			entryBuild = pending[0]
		})

		It("rejects config mutation of a server-owned resource-capture run instance", func() {
			mutatedConfig := captureConfig
			mutatedConfig.Jobs[0].PlanSequence[0].Config.(*atc.TaskStep).Config.Run.Path = "/bin/false"

			_, created, err := defaultTeam.SavePipeline(
				atc.PipelineRef{Name: captureInstance.Name(), InstanceVars: captureInstance.InstanceVars()},
				mutatedConfig, captureInstance.ConfigVersion(), false,
			)
			Expect(created).To(BeFalse())
			Expect(errors.Is(err, db.ErrWorkflowRunOwnedPipeline)).To(BeTrue())

			found, err := captureInstance.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			config, err := captureInstance.Config()
			Expect(err).NotTo(HaveOccurred())
			Expect(config.Jobs[0].PlanSequence[0].Config.(*atc.TaskStep).Config.Run.Path).To(Equal("/bin/true"))
		})

		It("rejects public manual builds, reruns, and one-off builds for a server-owned resource-capture run instance", func() {
			created, err := captureJob.CreateBuild("mallory")
			Expect(created).To(BeNil())
			Expect(errors.Is(err, db.ErrWorkflowRunOwnedPipeline)).To(BeTrue())

			rerun, err := captureJob.RerunBuild(entryBuild, "mallory")
			Expect(rerun).To(BeNil())
			Expect(errors.Is(err, db.ErrWorkflowRunOwnedPipeline)).To(BeTrue())

			oneOff, err := captureInstance.CreateStartedBuild(atc.Plan{ID: "manual"})
			Expect(oneOff).To(BeNil())
			Expect(errors.Is(err, db.ErrWorkflowRunOwnedPipeline)).To(BeTrue())

			pending, err := captureJob.GetPendingBuilds()
			Expect(err).NotTo(HaveOccurred())
			Expect(pending).To(ConsistOf(entryBuild))
		})
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
			envelopeFor(durable.ID), "alice", func(context.Context, int) error { return nil },
		)
		Expect(err).To(MatchError("db: workflow-run template reference drifted or collided"))
		Expect(created).To(BeFalse())
	})

	It("creates and links a workflow-owned run and its entry builds in one transaction", func() {
		durable, templateRef, runStore := createDurable()

		execution, created, err := factory.CreateRunForWorkflowRun(
			context.Background(), durable.ID,
			templateRef,
			envelopeFor(durable.ID), "alice", func(context.Context, int) error { return nil },
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

	It("rejects forged and durable-mismatched workflow execution envelopes", func() {
		durable, templateRef, _ := createDurable()
		_, created, err := factory.CreateRunForWorkflowRun(
			context.Background(), durable.ID, templateRef, workflow.ExecutionEnvelope{}, "alice",
			func(context.Context, int) error { return nil },
		)
		Expect(err).To(MatchError(ContainSubstring("invalid workflow execution envelope")))
		Expect(created).To(BeFalse())

		mismatched, err := workflow.RenderFunction(workflow.FunctionTarget{
			Kind: workflow.TargetWorkflow, WorkflowDefinitionID: 1, WorkflowName: "other-workflow", WorkflowVersion: 1, SignatureVersion: 1,
			Function: workflow.FunctionConfig{SignatureVersion: 1, Plan: []atc.Step{{Config: &atc.TaskStep{
				Name: "other", FunctionID: "other", Config: &atc.TaskConfig{Platform: "linux",
					ImageResource: &atc.ImageResource{Type: "registry-image", Source: atc.Source{"repository": "example/other"}, Version: atc.Version{"digest": "sha256:immutable"}},
					Run:           atc.TaskRunConfig{Path: "/bin/true"},
				},
			}}}},
		})
		Expect(err).NotTo(HaveOccurred())
		envelope, err := mismatched.ExecutionEnvelope(map[string]any{"workflow_run_id": durable.ID.String()})
		Expect(err).NotTo(HaveOccurred())
		_, created, err = factory.CreateRunForWorkflowRun(
			context.Background(), durable.ID, templateRef, envelope, "alice",
			func(context.Context, int) error { return nil },
		)
		Expect(err).To(MatchError(ContainSubstring("invalid workflow execution envelope")))
		Expect(created).To(BeFalse())
	})

	It("keeps the workflow-owned instance cleanup path available", func() {
		durable, templateRef, _ := createDurable()
		execution, created, err := factory.CreateRunForWorkflowRun(
			context.Background(), durable.ID, templateRef, envelopeFor(durable.ID), "alice",
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
			context.Background(), durable.ID, templateRef, envelopeFor(durable.ID), "alice",
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
			context.Background(), durable.ID, templateRef, envelopeFor(durable.ID), "alice",
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
		first, created, err := factory.CreateRunForWorkflowRun(context.Background(), durable.ID, templateRef, envelopeFor(durable.ID), "alice", callback)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		second, created, err := factory.CreateRunForWorkflowRun(context.Background(), durable.ID, templateRef, envelopeFor(durable.ID), "alice", callback)
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
			context.Background(), durable.ID, templateRef, envelopeFor(durable.ID), "alice",
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
				context.Background(), durable.ID, templateRef, envelopeFor(durable.ID), "alice",
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

	It("fails closed before a mutated durable config can bypass its execution envelope", func() {
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
			context.Background(), durable.ID, templateRef, envelopeFor(durable.ID), "alice",
			func(context.Context, int) error { return nil },
		)
		Expect(err).To(MatchError(ContainSubstring("invalid workflow execution envelope")))
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
					context.Background(), durable.ID, templateRef, envelopeFor(durable.ID), "alice",
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
