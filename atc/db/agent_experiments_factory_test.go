package db_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	experimentsapi "github.com/concourse/concourse/agent/api/experiments"
	"github.com/concourse/concourse/agent/experiment"
	"github.com/concourse/concourse/agent/pagination"
	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type experimentTargetRendererFunc func(workflow.FunctionTarget) (workflow.RenderedFunction, error)

func (render experimentTargetRendererFunc) RenderFunction(target workflow.FunctionTarget) (workflow.RenderedFunction, error) {
	return render(target)
}

type experimentResourceSourcePreparerFunc func(
	context.Context,
	experiment.ResourceSourcePreparation,
) ([]experiment.PreparedResourceSourceAdmission, error)

func (prepare experimentResourceSourcePreparerFunc) PrepareResourceSources(
	ctx context.Context,
	request experiment.ResourceSourcePreparation,
) ([]experiment.PreparedResourceSourceAdmission, error) {
	return prepare(ctx, request)
}

var _ = Describe("AgentExperimentsFactory", func() {
	var (
		ctx                 context.Context
		factory             db.AgentExperimentsFactory
		fixtureSnapshot     snapshot.SnapshotID
		replacementSnapshot snapshot.SnapshotID
		measurementSnapshot snapshot.SnapshotID
		candidateDefinition *workflow.Definition
		evaluatorDefinition *workflow.Definition
		runSequence         int64
		targetRenderer      workflowrun.WorkflowTargetRenderer
	)

	insertSnapshot := func(kind, digestDigit string) snapshot.SnapshotID {
		var id int64
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_snapshots
				(team_id, type_name, type_version, digest, byte_size, file_count, representation)
			VALUES ($1, $2, 1, $3, 1, 1, 'filesystem-tree-v1')
			RETURNING id
		`, defaultTeam.ID(), kind, "sha256:"+strings.Repeat(digestDigit, 64)).Scan(&id)).To(Succeed())
		return snapshot.SnapshotID(id)
	}
	registerReadySourceAdmission := func(
		teamID int,
		definition *workflow.Definition,
		configHash string,
		key string,
	) int64 {
		var sourcePipelineID int
		Expect(dbConn.QueryRow(`
			INSERT INTO pipelines
				(name, team_id, version, template, secondary_ordering)
			VALUES ($1, $2, 1, false, 1)
			RETURNING id
		`, fmt.Sprintf("experiment-source-%s-%d", key, time.Now().UnixNano()),
			teamID).Scan(&sourcePipelineID)).To(Succeed())
		_, err := dbConn.Exec(`
			INSERT INTO agent_workflow_resource_source_pipelines
				(pipeline_id, team_id, workflow_definition_id,
				 workflow_name, workflow_version, pipeline_config_version,
				 config_hash, source_declarations, state)
			VALUES ($1, $2, $3, $4, $5, 1, $6, '[]'::jsonb, 'active')
		`, sourcePipelineID, teamID, definition.ID, definition.Name,
			definition.Version, configHash)
		Expect(err).NotTo(HaveOccurred())
		var selectingBuildID int64
		Expect(dbConn.QueryRow(`
			INSERT INTO builds (name, status, team_id, pipeline_id)
			VALUES ($1, 'succeeded', $2, $3)
			RETURNING id
		`, fmt.Sprintf("experiment-source-build-%s", key),
			teamID, sourcePipelineID).Scan(&selectingBuildID)).To(Succeed())
		var admissionID int64
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_resource_source_admissions
				(team_id, workflow_definition_id, source_pipeline_id,
				 source_config_hash, idempotency_key, mode,
				 selecting_build_id, status)
			VALUES ($1, $2, $3, $4, $5, 'manual', $6, 'ready')
			RETURNING id
		`, teamID, definition.ID, sourcePipelineID, configHash,
			fmt.Sprintf("experiment-source-%s", key), selectingBuildID).Scan(
			&admissionID,
		)).To(Succeed())
		return admissionID
	}
	registerAlternativeReadySourceAdmission := func(
		teamID int,
		definition *workflow.Definition,
		configHash string,
		key string,
	) int64 {
		var sourcePipelineID int
		Expect(dbConn.QueryRow(`
			SELECT pipeline_id
			FROM agent_workflow_resource_source_pipelines
			WHERE team_id = $1 AND workflow_definition_id = $2
		`, teamID, definition.ID).Scan(&sourcePipelineID)).To(Succeed())
		var selectingBuildID int64
		Expect(dbConn.QueryRow(`
			INSERT INTO builds (name, status, team_id, pipeline_id)
			VALUES ($1, 'succeeded', $2, $3)
			RETURNING id
		`, fmt.Sprintf("experiment-source-alternative-build-%s", key),
			teamID, sourcePipelineID).Scan(&selectingBuildID)).To(Succeed())
		var admissionID int64
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_resource_source_admissions
				(team_id, workflow_definition_id, source_pipeline_id,
				 source_config_hash, idempotency_key, mode,
				 selecting_build_id, status)
			VALUES ($1, $2, $3, $4, $5, 'manual', $6, 'ready')
			RETURNING id
		`, teamID, definition.ID, sourcePipelineID, configHash,
			fmt.Sprintf("experiment-source-alternative-%s", key), selectingBuildID).Scan(
			&admissionID,
		)).To(Succeed())
		return admissionID
	}

	definition := func(input snapshot.SnapshotID) experiment.Definition {
		signature := workflow.PublicSignature{
			Inputs:  []workflow.SignaturePort{{Name: "repo", Type: "repository/v1"}},
			Outputs: []workflow.SignaturePort{{Name: "review", Type: "review/v1"}},
		}
		hash, err := experiment.HashSignature(signature)
		Expect(err).NotTo(HaveOccurred())
		return experiment.Definition{
			Name: "review-prompts", State: experiment.StateDraft, Signature: signature,
			Variants: []experiment.Variant{
				{Label: "control", Control: true, Target: experiment.Target{Kind: experiment.TargetWorkflow, WorkflowName: candidateDefinition.Name, DefinitionID: int64(candidateDefinition.ID), Version: candidateDefinition.Version}, SignatureHash: hash},
				{Label: "candidate", Target: experiment.Target{Kind: experiment.TargetFunction, WorkflowName: candidateDefinition.Name, DefinitionID: int64(candidateDefinition.ID), Version: candidateDefinition.Version, FunctionID: "review"}, SignatureHash: hash},
			},
			Fixtures: []experiment.Fixture{{Label: "small", Role: experiment.FixtureNormal, Inputs: map[string]snapshot.SnapshotID{"repo": input}}},
			Evaluator: experiment.Evaluator{
				Target: experiment.Target{Kind: experiment.TargetWorkflow, WorkflowName: evaluatorDefinition.Name, DefinitionID: int64(evaluatorDefinition.ID), Version: evaluatorDefinition.Version},
				Signature: workflow.PublicSignature{
					Inputs: []workflow.SignaturePort{
						{Name: "candidate", Type: "review/v1"},
						{Name: "repo", Type: "repository/v1"},
					},
					Outputs: []workflow.SignaturePort{{Name: "measurements", Type: "measurements/v1"}},
				},
				Mappings: []experiment.EvaluatorMapping{
					{EvaluatorPort: "candidate", SourceDirection: experiment.SourceCandidateOutput, SourcePort: "review"},
					{EvaluatorPort: "repo", SourceDirection: experiment.SourceFixtureInput, SourcePort: "repo"},
				},
				MeasurementsPort: "measurements",
			},
			Repetitions: 2,
		}
	}

	insertRun := func(target experiment.Target, teamID int, teamName, originReference, targetConfigHash string) snapshot.WorkflowRunID {
		runSequence++
		var functionID any
		if target.FunctionID != "" {
			functionID = target.FunctionID
		}
		var id snapshot.WorkflowRunID
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
				 schema_version, signature_version, definition_content_hash, function_id,
				 idempotency_key, parameterized_config, parameterized_config_hash,
				 origin_kind, origin_reference, created_by, status)
			SELECT $1, $2, definition.id, definition.name, definition.version,
			       definition.schema_version, definition.signature_version, definition.content_hash, $4,
			       $5, '{}', $6, 'experiment', $7, 'alice', 'running'
			FROM agent_workflow_definitions definition
			WHERE definition.id = $3
			RETURNING id
		`, teamID, teamName, target.DefinitionID, functionID,
			fmt.Sprintf("experiment-factory-%d-%d", time.Now().UnixNano(), runSequence),
			targetConfigHash, originReference).Scan(&id)).To(Succeed())
		return id
	}

	candidateRunRequest := func(
		cell experiment.CandidateCell,
		key string,
	) db.AgentWorkflowRunCreateRequest {
		inputs := make(map[string]snapshot.SnapshotRef, len(cell.Inputs))
		for port, id := range cell.Inputs {
			var typeName, digest string
			var typeVersion int
			Expect(dbConn.QueryRow(`
				SELECT type_name, type_version, digest
				FROM agent_snapshots
				WHERE id = $1
			`, int64(id)).Scan(&typeName, &typeVersion, &digest)).To(Succeed())
			inputs[port] = snapshot.SnapshotRef{
				ID:     id,
				Type:   snapshot.TypeRef(fmt.Sprintf("%s/v%d", typeName, typeVersion)),
				Digest: snapshot.Digest(digest),
			}
		}
		var functionID *string
		if cell.Target.FunctionID != "" {
			value := cell.Target.FunctionID
			functionID = &value
		}
		return db.AgentWorkflowRunCreateRequest{
			TeamID: cell.TeamID, TeamName: cell.TeamName,
			WorkflowDefinitionID:        int(cell.Target.DefinitionID),
			WorkflowName:                cell.Target.WorkflowName,
			WorkflowVersion:             cell.Target.Version,
			SchemaVersion:               candidateDefinition.SchemaVersion,
			SignatureVersion:            candidateDefinition.SignatureVersion,
			DefinitionContentHash:       candidateDefinition.ContentHash,
			FunctionID:                  functionID,
			IdempotencyKey:              key,
			ParameterizedConfig:         json.RawMessage(`{}`),
			ParameterizedConfigHash:     cell.TargetConfigHash,
			DevValidationProvenanceHash: cell.DevValidationProvenanceHash,
			OriginKind:                  "experiment",
			OriginReference: fmt.Sprintf(
				"experiment:%s:cell:%s", cell.ExperimentID.String(), cell.ID.String(),
			),
			CreatedBy: cell.CreatedBy,
			Status:    db.AgentWorkflowRunStatusAdmitting,
			Inputs:    inputs,
			ExperimentAdmission: &db.AgentWorkflowRunExperimentAdmission{
				ExperimentID: int64(cell.ExperimentID),
				CellID:       int64(cell.ID),
				Phase:        "candidate",
			},
		}
	}

	evaluatorRunRequest := func(
		cell experiment.EvaluationCell,
		inputIDs map[string]snapshot.SnapshotID,
		key string,
	) db.AgentWorkflowRunCreateRequest {
		inputs := make(map[string]snapshot.SnapshotRef, len(inputIDs))
		for port, id := range inputIDs {
			var typeName, digest string
			var typeVersion int
			Expect(dbConn.QueryRow(`
				SELECT type_name, type_version, digest
				FROM agent_snapshots
				WHERE id = $1
			`, int64(id)).Scan(&typeName, &typeVersion, &digest)).To(Succeed())
			inputs[port] = snapshot.SnapshotRef{
				ID:     id,
				Type:   snapshot.TypeRef(fmt.Sprintf("%s/v%d", typeName, typeVersion)),
				Digest: snapshot.Digest(digest),
			}
		}
		var functionID *string
		if cell.Evaluator.Target.FunctionID != "" {
			value := cell.Evaluator.Target.FunctionID
			functionID = &value
		}
		return db.AgentWorkflowRunCreateRequest{
			TeamID: cell.TeamID, TeamName: cell.TeamName,
			WorkflowDefinitionID:        int(cell.Evaluator.Target.DefinitionID),
			WorkflowName:                cell.Evaluator.Target.WorkflowName,
			WorkflowVersion:             cell.Evaluator.Target.Version,
			SchemaVersion:               evaluatorDefinition.SchemaVersion,
			SignatureVersion:            evaluatorDefinition.SignatureVersion,
			DefinitionContentHash:       evaluatorDefinition.ContentHash,
			FunctionID:                  functionID,
			IdempotencyKey:              key,
			ParameterizedConfig:         json.RawMessage(`{}`),
			ParameterizedConfigHash:     cell.Evaluator.TargetConfigHash,
			DevValidationProvenanceHash: cell.Evaluator.DevValidationProvenanceHash,
			OriginKind:                  "experiment",
			OriginReference: fmt.Sprintf(
				"experiment:%s:cell:%s:evaluator", cell.ExperimentID.String(), cell.ID.String(),
			),
			CreatedBy: cell.CreatedBy,
			Status:    db.AgentWorkflowRunStatusAdmitting,
			Inputs:    inputs,
			ExperimentAdmission: &db.AgentWorkflowRunExperimentAdmission{
				ExperimentID: int64(cell.ExperimentID),
				CellID:       int64(cell.ID),
				Phase:        "evaluator",
			},
		}
	}

	BeforeEach(func() {
		ctx = context.Background()
		runSequence = 0
		targetRenderer = workflowrun.WorkflowTargetRenderer{
			RuntimeImage: "registry.example/agent-runner@sha256:" + strings.Repeat("a", 64),
		}
		factory = db.NewAgentExperimentsFactory(dbConn, targetRenderer)
		workflowStore := db.NewAgentWorkflowsFactory(dbConn)
		var err error
		candidateDefinition, err = workflowStore.Import("experiment-review", []byte(`
schema_version: 3
name: experiment-review
signature_version: 1
inputs:
  - name: repo
    type: repository/v1
outputs:
  - name: review
    type: review/v1
    from: review
plan:
  - agent: review
    function_id: review
    prompt: Review the immutable repository snapshot.
    budget_slice_usd: 0.25
    inputs: [repo]
    outputs: [review]
    input_types:
      repo: {type: repository/v1}
    output_types:
      review: review/v1
`), "alice")
		Expect(err).NotTo(HaveOccurred())
		evaluatorDefinition, err = workflowStore.Import("experiment-judge", []byte(`
schema_version: 3
name: experiment-judge
signature_version: 1
inputs:
  - name: candidate
    type: review/v1
  - name: repo
    type: repository/v1
outputs:
  - name: measurements
    type: measurements/v1
    from: measurements
plan:
  - agent: judge
    function_id: judge
    prompt: Evaluate the candidate output.
    budget_slice_usd: 0.25
    inputs: [candidate, repo]
    outputs: [measurements]
    input_types:
      candidate: {type: review/v1}
      repo: {type: repository/v1}
    output_types:
      measurements: measurements/v1
`), "alice")
		Expect(err).NotTo(HaveOccurred())
		fixtureSnapshot = insertSnapshot("repository", "a")
		replacementSnapshot = insertSnapshot("repository", "b")
		measurementSnapshot = insertSnapshot("measurements", "c")
	})

	It("rejects agent-bearing unlimited experiments when the global daily cap is enabled", func() {
		value := definition(fixtureSnapshot)
		created, err := factory.Create(ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", value)
		Expect(err).NotTo(HaveOccurred())

		cappedFactory := db.NewAgentExperimentsFactory(
			dbConn,
			targetRenderer,
			db.WithAgentExperimentBudgetConfig(db.AgentExperimentBudgetConfig{
				GlobalDailyCapUSD: 100,
				Location:          time.UTC,
				Now:               func() time.Time { return time.Now().UTC() },
			}),
		)
		_, err = cappedFactory.PreflightStart(ctx, defaultTeam.ID(), created.ID, created.Revision)
		Expect(errors.Is(err, experimentsapi.ErrInvalidDefinition)).To(BeTrue())
		Expect(err.Error()).To(ContainSubstring("per_cell_usd or total_usd is required"))
		_, err = cappedFactory.Start(ctx, defaultTeam.ID(), created.ID, created.Revision, "alice")
		Expect(errors.Is(err, experimentsapi.ErrInvalidDefinition)).To(BeTrue())

		unchanged, found, err := factory.Get(ctx, defaultTeam.ID(), created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(unchanged.Definition.State).To(Equal(experiment.StateDraft))
		Expect(unchanged.Revision).To(Equal(created.Revision))
		var cells int
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_experiment_cells WHERE experiment_id = $1
		`, int64(created.ID)).Scan(&cells)).To(Succeed())
		Expect(cells).To(BeZero())
	})

	It("rejects outbound publishers before materializing an experiment matrix", func() {
		created, err := factory.Create(ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", definition(fixtureSnapshot))
		Expect(err).NotTo(HaveOccurred())
		effectfulRenderer := experimentTargetRendererFunc(func(target workflow.FunctionTarget) (workflow.RenderedFunction, error) {
			rendered, renderErr := targetRenderer.RenderFunction(target)
			if renderErr != nil || target.WorkflowName != candidateDefinition.Name {
				return rendered, renderErr
			}
			rendered.Config.Jobs[0].PlanSequence = append(rendered.Config.Jobs[0].PlanSequence, atc.Step{
				Config: &atc.PublishSnapshotStep{
					Name: "comment-on-ticket", Publisher: publisher.WorkItemPublisher,
					Input: "review", InputType: "review/v1", Destination: "jira.example/ENG-123",
					Mode: publisher.ModeComment, Parameters: map[string]string{"body": "candidate review"},
					ApprovalPolicyVersion: "experiment-test/v1",
				},
			})
			hash, hashErr := workflow.TargetConfigHash(rendered.Config)
			if hashErr != nil {
				return workflow.RenderedFunction{}, hashErr
			}
			name, nameErr := workflow.TemplateName(target.Kind, target.WorkflowName, target.WorkflowVersion, target.FunctionID, hash)
			if nameErr != nil {
				return workflow.RenderedFunction{}, nameErr
			}
			rendered.TargetConfigHash = hash
			rendered.TemplateName = name
			return rendered, nil
		})
		effectfulFactory := db.NewAgentExperimentsFactory(dbConn, effectfulRenderer)
		for _, start := range []func() error{
			func() error {
				_, preflightErr := effectfulFactory.PreflightStart(ctx, defaultTeam.ID(), created.ID, created.Revision)
				return preflightErr
			},
			func() error {
				_, startErr := effectfulFactory.Start(ctx, defaultTeam.ID(), created.ID, created.Revision, "alice")
				return startErr
			},
		} {
			err = start()
			Expect(errors.Is(err, experimentsapi.ErrInvalidDefinition)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("publish_snapshot"))
			Expect(err.Error()).To(ContainSubstring("effect-free"))
		}
		unchanged, found, err := factory.Get(ctx, defaultTeam.ID(), created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(unchanged.Definition.State).To(Equal(experiment.StateDraft))
		cells, err := factory.ListCells(ctx, defaultTeam.ID(), created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(cells).To(BeEmpty())
	})

	It("freezes rendered target dependencies at start and preserves them across a runtime-image rollout", func() {
		created, err := factory.Create(ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", definition(fixtureSnapshot))
		Expect(err).NotTo(HaveOccurred())
		preflighted, err := factory.PreflightStart(ctx, defaultTeam.ID(), created.ID, created.Revision)
		Expect(err).NotTo(HaveOccurred())
		Expect(preflighted.Definition.State).To(Equal(experiment.StateDraft))
		Expect(preflighted.Revision).To(Equal(created.Revision))
		var cellsBeforeStart, frozenTargetsBeforeStart int
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_experiment_cells WHERE experiment_id = $1
		`, int64(created.ID)).Scan(&cellsBeforeStart)).To(Succeed())
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_experiment_variants
			WHERE experiment_id = $1 AND target_config_hash IS NOT NULL
		`, int64(created.ID)).Scan(&frozenTargetsBeforeStart)).To(Succeed())
		Expect(cellsBeforeStart).To(BeZero())
		Expect(frozenTargetsBeforeStart).To(BeZero())

		started, err := factory.Start(ctx, defaultTeam.ID(), created.ID, created.Revision, "alice")
		Expect(err).NotTo(HaveOccurred())

		fullCandidate, err := workflow.FullFunctionTarget(*candidateDefinition)
		Expect(err).NotTo(HaveOccurred())
		fullRendered, err := targetRenderer.RenderFunction(fullCandidate)
		Expect(err).NotTo(HaveOccurred())
		selectedCandidate, err := workflow.ExtractFunctionTarget(*candidateDefinition, "review")
		Expect(err).NotTo(HaveOccurred())
		selectedRendered, err := targetRenderer.RenderFunction(selectedCandidate)
		Expect(err).NotTo(HaveOccurred())
		fullEvaluator, err := workflow.FullFunctionTarget(*evaluatorDefinition)
		Expect(err).NotTo(HaveOccurred())
		evaluatorRendered, err := targetRenderer.RenderFunction(fullEvaluator)
		Expect(err).NotTo(HaveOccurred())

		Expect(started.Definition.Variants[0].TargetConfigHash).To(Equal(fullRendered.TargetConfigHash))
		Expect(started.Definition.Variants[1].TargetConfigHash).To(Equal(selectedRendered.TargetConfigHash))
		Expect(started.Definition.Evaluator.TargetConfigHash).To(Equal(evaluatorRendered.TargetConfigHash))
		Expect(started.Definition.Variants[0].DevValidationProvenanceHash).To(Equal(fullRendered.DevValidationProvenanceHash))
		Expect(started.Definition.Variants[1].DevValidationProvenanceHash).To(Equal(selectedRendered.DevValidationProvenanceHash))
		Expect(started.Definition.Evaluator.DevValidationProvenanceHash).To(Equal(evaluatorRendered.DevValidationProvenanceHash))

		rolledRenderer := workflowrun.WorkflowTargetRenderer{
			RuntimeImage: "registry.example/agent-runner@sha256:" + strings.Repeat("b", 64),
		}
		restartedFactory := db.NewAgentExperimentsFactory(dbConn, rolledRenderer)
		claimed, err := restartedFactory.ClaimCandidateCells(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(claimed).To(HaveLen(4))
		for _, cell := range claimed {
			if cell.Target.FunctionID == "" {
				Expect(cell.TargetConfigHash).To(Equal(fullRendered.TargetConfigHash))
				Expect(cell.DevValidationProvenanceHash).To(Equal(fullRendered.DevValidationProvenanceHash))
			} else {
				Expect(cell.TargetConfigHash).To(Equal(selectedRendered.TargetConfigHash))
				Expect(cell.DevValidationProvenanceHash).To(Equal(selectedRendered.DevValidationProvenanceHash))
			}
		}
		candidateOrigin := fmt.Sprintf("experiment:%s:cell:%s", started.ID.String(), claimed[0].ID.String())
		candidateRun := insertRun(
			claimed[0].Target, defaultTeam.ID(), defaultTeam.Name(), candidateOrigin, claimed[0].TargetConfigHash,
		)
		Expect(restartedFactory.RecordCandidateRun(ctx, claimed[0].ID, candidateRun)).To(BeTrue())
		evaluations, err := restartedFactory.ClaimEvaluationCells(ctx, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(evaluations).To(HaveLen(1))
		Expect(evaluations[0].Evaluator.TargetConfigHash).To(Equal(evaluatorRendered.TargetConfigHash))
		Expect(evaluations[0].Evaluator.DevValidationProvenanceHash).To(Equal(evaluatorRendered.DevValidationProvenanceHash))
		rolledFull, err := rolledRenderer.RenderFunction(fullCandidate)
		Expect(err).NotTo(HaveOccurred())
		Expect(rolledFull.TargetConfigHash).NotTo(Equal(fullRendered.TargetConfigHash))
		rolledEvaluator, err := rolledRenderer.RenderFunction(fullEvaluator)
		Expect(err).NotTo(HaveOccurred())
		Expect(rolledEvaluator.TargetConfigHash).NotTo(Equal(evaluatorRendered.TargetConfigHash))
	})

	It("persists one prepared source admission per definition and binds every claimed child to it", func() {
		candidateHash := strings.Repeat("e", 64)
		evaluatorHash := strings.Repeat("f", 64)
		candidateAdmissionID := registerReadySourceAdmission(
			defaultTeam.ID(), candidateDefinition, candidateHash, "candidate",
		)
		evaluatorAdmissionID := registerReadySourceAdmission(
			defaultTeam.ID(), evaluatorDefinition, evaluatorHash, "evaluator",
		)
		wrongCandidateAdmissionID := registerAlternativeReadySourceAdmission(
			defaultTeam.ID(), candidateDefinition, strings.Repeat("a", 64), "candidate-wrong",
		)
		wrongEvaluatorAdmissionID := registerAlternativeReadySourceAdmission(
			defaultTeam.ID(), evaluatorDefinition, strings.Repeat("b", 64), "evaluator-wrong",
		)
		preparerCalls := 0
		sourceFactory := db.NewAgentExperimentsFactory(
			dbConn,
			targetRenderer,
			db.WithAgentExperimentResourceSourcePreparer(
				experimentResourceSourcePreparerFunc(func(
					_ context.Context,
					request experiment.ResourceSourcePreparation,
				) ([]experiment.PreparedResourceSourceAdmission, error) {
					preparerCalls++
					Expect(request.TeamID).To(Equal(defaultTeam.ID()))
					Expect(request.TeamName).To(Equal(defaultTeam.Name()))
					Expect(request.Actor).To(Equal("alice"))
					return []experiment.PreparedResourceSourceAdmission{
						{
							WorkflowDefinitionID: int64(candidateDefinition.ID),
							SourceConfigHash:     candidateHash,
							AdmissionID:          candidateAdmissionID,
						},
						{
							WorkflowDefinitionID: int64(evaluatorDefinition.ID),
							SourceConfigHash:     evaluatorHash,
							AdmissionID:          evaluatorAdmissionID,
						},
					}, nil
				}),
			),
		)
		created, err := sourceFactory.Create(
			ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", definition(fixtureSnapshot),
		)
		Expect(err).NotTo(HaveOccurred())
		started, err := sourceFactory.Start(
			ctx, defaultTeam.ID(), created.ID, created.Revision, "alice",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(preparerCalls).To(Equal(1))

		var storedCount int
		Expect(dbConn.QueryRow(`
			SELECT count(*)
			FROM agent_experiment_resource_source_admissions
			WHERE experiment_id = $1 AND team_id = $2
		`, int64(started.ID), defaultTeam.ID()).Scan(&storedCount)).To(Succeed())
		Expect(storedCount).To(Equal(2))

		cells, err := sourceFactory.ClaimCandidateCells(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(cells).To(HaveLen(4))
		for _, cell := range cells {
			Expect(cell.ResourceSourceAdmissionID).NotTo(BeNil())
			Expect(*cell.ResourceSourceAdmissionID).To(Equal(candidateAdmissionID))
		}
		candidateRun := insertRun(
			cells[0].Target,
			defaultTeam.ID(),
			defaultTeam.Name(),
			fmt.Sprintf("experiment:%s:cell:%s", started.ID.String(), cells[0].ID.String()),
			cells[0].TargetConfigHash,
		)
		recorded, err := sourceFactory.RecordCandidateRun(ctx, cells[0].ID, candidateRun)
		Expect(err).NotTo(HaveOccurred())
		Expect(recorded).To(BeFalse())
		_, err = dbConn.Exec(
			`UPDATE agent_workflow_runs
			  SET resource_source_admission_id = $2
			  WHERE id = $1`,
			int64(candidateRun), wrongCandidateAdmissionID,
		)
		Expect(err).NotTo(HaveOccurred())
		recorded, err = sourceFactory.RecordCandidateRun(ctx, cells[0].ID, candidateRun)
		Expect(err).NotTo(HaveOccurred())
		Expect(recorded).To(BeFalse())
		_, err = dbConn.Exec(
			`UPDATE agent_workflow_runs
			  SET resource_source_admission_id = $2
			  WHERE id = $1`,
			int64(candidateRun), candidateAdmissionID,
		)
		Expect(err).NotTo(HaveOccurred())
		recorded, err = sourceFactory.RecordCandidateRun(ctx, cells[0].ID, candidateRun)
		Expect(err).NotTo(HaveOccurred())
		Expect(recorded).To(BeTrue())

		evaluations, err := sourceFactory.ClaimEvaluationCells(ctx, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(evaluations).To(HaveLen(1))
		Expect(evaluations[0].ResourceSourceAdmissionID).NotTo(BeNil())
		Expect(*evaluations[0].ResourceSourceAdmissionID).To(Equal(evaluatorAdmissionID))
		evaluatorRun := insertRun(
			evaluations[0].Evaluator.Target,
			defaultTeam.ID(),
			defaultTeam.Name(),
			fmt.Sprintf("experiment:%s:cell:%s:evaluator", started.ID.String(), evaluations[0].ID.String()),
			evaluations[0].Evaluator.TargetConfigHash,
		)
		recorded, err = sourceFactory.RecordEvaluatorRun(ctx, evaluations[0].ID, evaluatorRun)
		Expect(err).NotTo(HaveOccurred())
		Expect(recorded).To(BeFalse())
		_, err = dbConn.Exec(
			`UPDATE agent_workflow_runs
			  SET resource_source_admission_id = $2
			  WHERE id = $1`,
			int64(evaluatorRun), wrongEvaluatorAdmissionID,
		)
		Expect(err).NotTo(HaveOccurred())
		recorded, err = sourceFactory.RecordEvaluatorRun(ctx, evaluations[0].ID, evaluatorRun)
		Expect(err).NotTo(HaveOccurred())
		Expect(recorded).To(BeFalse())
		_, err = dbConn.Exec(
			`UPDATE agent_workflow_runs
			  SET resource_source_admission_id = $2
			  WHERE id = $1`,
			int64(evaluatorRun), evaluatorAdmissionID,
		)
		Expect(err).NotTo(HaveOccurred())
		recorded, err = sourceFactory.RecordEvaluatorRun(ctx, evaluations[0].ID, evaluatorRun)
		Expect(err).NotTo(HaveOccurred())
		Expect(recorded).To(BeTrue())
	})

	It("keeps a draft unchanged when prepared source admissions do not cover the locked registry", func() {
		candidateHash := strings.Repeat("e", 64)
		candidateAdmissionID := registerReadySourceAdmission(
			defaultTeam.ID(), candidateDefinition, candidateHash, "missing-evaluator",
		)
		registerReadySourceAdmission(
			defaultTeam.ID(), evaluatorDefinition, strings.Repeat("f", 64), "missing-evaluator-evaluator",
		)
		sourceFactory := db.NewAgentExperimentsFactory(
			dbConn,
			targetRenderer,
			db.WithAgentExperimentResourceSourcePreparer(
				experimentResourceSourcePreparerFunc(func(
					context.Context,
					experiment.ResourceSourcePreparation,
				) ([]experiment.PreparedResourceSourceAdmission, error) {
					return []experiment.PreparedResourceSourceAdmission{{
						WorkflowDefinitionID: int64(candidateDefinition.ID),
						SourceConfigHash:     candidateHash,
						AdmissionID:          candidateAdmissionID,
					}}, nil
				}),
			),
		)
		created, err := sourceFactory.Create(
			ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", definition(fixtureSnapshot),
		)
		Expect(err).NotTo(HaveOccurred())
		_, err = sourceFactory.Start(ctx, defaultTeam.ID(), created.ID, created.Revision, "alice")
		Expect(errors.Is(err, experiment.ErrInvalidDefinition)).To(BeTrue())
		unchanged, found, getErr := sourceFactory.Get(ctx, defaultTeam.ID(), created.ID)
		Expect(getErr).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(unchanged.Definition.State).To(Equal(experiment.StateDraft))
	})

	It("round-trips mutable drafts, retention claims, atomic cells, and optimistic revisions", func() {
		created, err := factory.Create(ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", definition(fixtureSnapshot))
		Expect(err).NotTo(HaveOccurred())
		Expect(created.Revision).To(Equal(int64(1)))
		Expect(created.Definition.State).To(Equal(experiment.StateDraft))

		var fixtureClaims int
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_snapshot_retention_claims
			WHERE team_id = $1 AND snapshot_id = $2 AND class = 'fixture' AND expires_at IS NULL
		`, defaultTeam.ID(), int64(fixtureSnapshot)).Scan(&fixtureClaims)).To(Succeed())
		Expect(fixtureClaims).To(Equal(1))

		updated, err := factory.Update(ctx, defaultTeam.ID(), created.ID, created.Revision, "bob", definition(replacementSnapshot))
		Expect(err).NotTo(HaveOccurred())
		Expect(updated.Revision).To(Equal(int64(2)))
		Expect(updated.Definition.Fixtures[0].Inputs["repo"]).To(Equal(replacementSnapshot))
		_, err = factory.Update(ctx, defaultTeam.ID(), created.ID, created.Revision, "bob", definition(fixtureSnapshot))
		Expect(errors.Is(err, experimentsapi.ErrRevisionConflict)).To(BeTrue())

		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_snapshot_retention_claims
			WHERE team_id = $1 AND snapshot_id = $2 AND class = 'fixture'
		`, defaultTeam.ID(), int64(fixtureSnapshot)).Scan(&fixtureClaims)).To(Succeed())
		Expect(fixtureClaims).To(BeZero())

		_, found, err := factory.Get(ctx, defaultTeam.ID()+1000, created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())

		started, err := factory.Start(ctx, defaultTeam.ID(), created.ID, updated.Revision, "bob")
		Expect(err).NotTo(HaveOccurred())
		Expect(started.Definition.State).To(Equal(experiment.StateRunning))
		Expect(started.Revision).To(Equal(int64(3)))
		Expect(started.StartedAt).NotTo(BeNil())

		cells, err := factory.ListCells(ctx, defaultTeam.ID(), created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(cells).To(HaveLen(4))
		identities := make(map[string]struct{})
		for _, cell := range cells {
			identities[fmt.Sprintf("%s/%s/%d", cell.FixtureLabel, cell.VariantLabel, cell.Repetition)] = struct{}{}
		}
		Expect(identities).To(HaveLen(4))

		_, err = factory.Update(ctx, defaultTeam.ID(), created.ID, started.Revision, "bob", definition(fixtureSnapshot))
		Expect(errors.Is(err, experimentsapi.ErrImmutable)).To(BeTrue())
	})

	It("pages equal-timestamp experiment history without gaps or duplicates", func() {
		created := make([]experiment.StoredExperiment, 3)
		for index := range created {
			var err error
			value := definition(fixtureSnapshot)
			value.Name = fmt.Sprintf("history-%d", index)
			created[index], err = factory.Create(ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", value)
			Expect(err).NotTo(HaveOccurred())
		}
		instant := time.Date(2026, time.July, 22, 12, 0, 0, 456000, time.UTC)
		_, err := dbConn.Exec(`
			UPDATE agent_experiments SET created_at = $4, updated_at = $4
			WHERE id IN ($1, $2, $3)
		`, int64(created[0].ID), int64(created[1].ID), int64(created[2].ID), instant)
		Expect(err).NotTo(HaveOccurred())

		paged, ok := factory.(experiment.PagedStore)
		Expect(ok).To(BeTrue())
		first, err := paged.ListPage(ctx, defaultTeam.ID(), experiment.ListFilter{Limit: 2})
		Expect(err).NotTo(HaveOccurred())
		Expect(first).To(HaveLen(2))
		Expect([]experiment.ID{first[0].ID, first[1].ID}).To(Equal([]experiment.ID{created[2].ID, created[1].ID}))
		before := pagination.Cursor{CreatedAt: first[1].CreatedAt, ID: int64(first[1].ID)}
		second, err := paged.ListPage(ctx, defaultTeam.ID(), experiment.ListFilter{Before: &before, Limit: 2})
		Expect(err).NotTo(HaveOccurred())
		Expect(second).To(HaveLen(1))
		Expect(second[0].ID).To(Equal(created[0].ID))
	})

	It("records successful actor mutations in an append-only durable audit", func() {
		created, err := factory.Create(
			ctx,
			defaultTeam.ID(),
			defaultTeam.Name(),
			"creator@example.test",
			definition(fixtureSnapshot),
		)
		Expect(err).NotTo(HaveOccurred())

		updated, err := factory.Update(
			ctx,
			defaultTeam.ID(),
			created.ID,
			created.Revision,
			"editor@example.test",
			definition(replacementSnapshot),
		)
		Expect(err).NotTo(HaveOccurred())
		_, err = factory.Update(
			ctx,
			defaultTeam.ID(),
			created.ID,
			created.Revision,
			"stale-editor@example.test",
			definition(fixtureSnapshot),
		)
		Expect(errors.Is(err, experiment.ErrRevisionConflict)).To(BeTrue())

		preflight, err := factory.PreflightStart(ctx, defaultTeam.ID(), created.ID, updated.Revision)
		Expect(err).NotTo(HaveOccurred())
		started, err := factory.Start(
			ctx,
			defaultTeam.ID(),
			created.ID,
			preflight.Revision,
			"runner@example.test",
		)
		Expect(err).NotTo(HaveOccurred())
		canceling, err := factory.Cancel(
			ctx,
			defaultTeam.ID(),
			created.ID,
			started.Revision,
			"canceler@example.test",
		)
		Expect(err).NotTo(HaveOccurred())
		_, err = factory.Cancel(
			ctx,
			defaultTeam.ID(),
			created.ID,
			canceling.Revision,
			"duplicate-canceler@example.test",
		)
		Expect(err).NotTo(HaveOccurred())

		type auditEvent struct {
			action   string
			actor    string
			revision int64
			teamID   int
			teamName string
		}
		rows, err := dbConn.Query(`
			SELECT action, actor, revision, team_id, team_name
			FROM agent_experiment_audit_events
			WHERE experiment_id = $1
			ORDER BY id
		`, int64(created.ID))
		Expect(err).NotTo(HaveOccurred())
		var events []auditEvent
		for rows.Next() {
			var event auditEvent
			Expect(rows.Scan(
				&event.action,
				&event.actor,
				&event.revision,
				&event.teamID,
				&event.teamName,
			)).To(Succeed())
			events = append(events, event)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		Expect(rows.Close()).To(Succeed())
		Expect(events).To(Equal([]auditEvent{
			{action: "create", actor: "creator@example.test", revision: 1, teamID: defaultTeam.ID(), teamName: defaultTeam.Name()},
			{action: "update", actor: "editor@example.test", revision: 2, teamID: defaultTeam.ID(), teamName: defaultTeam.Name()},
			{action: "start", actor: "runner@example.test", revision: 3, teamID: defaultTeam.ID(), teamName: defaultTeam.Name()},
			{action: "cancel", actor: "canceler@example.test", revision: 4, teamID: defaultTeam.ID(), teamName: defaultTeam.Name()},
		}))

		_, err = dbConn.Exec(`
			UPDATE agent_experiment_audit_events SET actor = 'rewritten'
			WHERE experiment_id = $1
		`, int64(created.ID))
		Expect(err).To(MatchError(ContainSubstring("append-only")))
		_, err = dbConn.Exec(`
			DELETE FROM agent_experiment_audit_events WHERE experiment_id = $1
		`, int64(created.ID))
		Expect(err).To(MatchError(ContainSubstring("append-only")))
		_, err = dbConn.Exec(`TRUNCATE TABLE agent_experiment_audit_events`)
		Expect(err).To(MatchError(ContainSubstring("append-only")))
	})

	It("leases runner and evaluator work restart-safely and builds scorecards from durable measurements", func() {
		created, err := factory.Create(ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", definition(fixtureSnapshot))
		Expect(err).NotTo(HaveOccurred())
		started, err := factory.Start(ctx, defaultTeam.ID(), created.ID, created.Revision, "alice")
		Expect(err).NotTo(HaveOccurred())

		first, err := factory.ClaimCandidateCells(ctx, 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(first).To(HaveLen(2))
		second, err := factory.ClaimCandidateCells(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(second).To(HaveLen(2))

		_, err = dbConn.Exec(`UPDATE agent_experiment_cells SET lease_until = now() - interval '1 second' WHERE id = $1`, int64(first[0].ID))
		Expect(err).NotTo(HaveOccurred())
		reclaimed, err := factory.ClaimCandidateCells(ctx, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(reclaimed).To(HaveLen(1))
		Expect(reclaimed[0].ID).To(Equal(first[0].ID))

		candidateOrigin := fmt.Sprintf("experiment:%s:cell:%s", started.ID.String(), reclaimed[0].ID.String())
		candidateRun := insertRun(reclaimed[0].Target, defaultTeam.ID(), defaultTeam.Name(), candidateOrigin, reclaimed[0].TargetConfigHash)
		conflictingCandidateRun := insertRun(reclaimed[0].Target, defaultTeam.ID(), defaultTeam.Name(), candidateOrigin, reclaimed[0].TargetConfigHash)
		wrongTeamCandidateRun := insertRun(reclaimed[0].Target, defaultTeam.ID()+1000, "other-team", candidateOrigin, reclaimed[0].TargetConfigHash)
		wrongTargetCandidateRun := insertRun(definition(fixtureSnapshot).Evaluator.Target, defaultTeam.ID(), defaultTeam.Name(), candidateOrigin, reclaimed[0].TargetConfigHash)
		driftedCandidateRun := insertRun(reclaimed[0].Target, defaultTeam.ID(), defaultTeam.Name(), candidateOrigin, strings.Repeat("f", 64))
		driftedProvenanceRun := insertRun(reclaimed[0].Target, defaultTeam.ID(), defaultTeam.Name(), candidateOrigin, reclaimed[0].TargetConfigHash)
		_, err = dbConn.Exec(`UPDATE agent_workflow_runs SET dev_validation_provenance_hash = $2 WHERE id = $1`, int64(driftedProvenanceRun), strings.Repeat("f", 64))
		Expect(err).NotTo(HaveOccurred())
		Expect(factory.RecordCandidateRun(ctx, reclaimed[0].ID, snapshot.WorkflowRunID(9_999_999))).To(BeFalse())
		Expect(factory.RecordCandidateRun(ctx, reclaimed[0].ID, wrongTeamCandidateRun)).To(BeFalse())
		Expect(factory.RecordCandidateRun(ctx, reclaimed[0].ID, wrongTargetCandidateRun)).To(BeFalse())
		Expect(factory.RecordCandidateRun(ctx, reclaimed[0].ID, driftedCandidateRun)).To(BeFalse())
		Expect(factory.RecordCandidateRun(ctx, reclaimed[0].ID, driftedProvenanceRun)).To(BeFalse())
		Expect(factory.RecordCandidateRun(ctx, reclaimed[0].ID, candidateRun)).To(BeTrue())
		Expect(factory.RecordCandidateRun(ctx, reclaimed[0].ID, candidateRun)).To(BeTrue())
		Expect(factory.RecordCandidateRun(ctx, reclaimed[0].ID, conflictingCandidateRun)).To(BeFalse())

		evaluations, err := factory.ClaimEvaluationCells(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(evaluations).To(HaveLen(1))
		Expect(evaluations[0].CandidateWorkflowRunID).To(Equal(candidateRun))
		Expect(evaluations[0].FixtureInputs["repo"]).To(Equal(fixtureSnapshot))
		Expect(evaluations[0].Evaluator.Target.DefinitionID).To(Equal(int64(evaluatorDefinition.ID)))

		evaluatorOrigin := candidateOrigin + ":evaluator"
		evaluatorRun := insertRun(evaluations[0].Evaluator.Target, defaultTeam.ID(), defaultTeam.Name(), evaluatorOrigin, evaluations[0].Evaluator.TargetConfigHash)
		conflictingEvaluatorRun := insertRun(evaluations[0].Evaluator.Target, defaultTeam.ID(), defaultTeam.Name(), evaluatorOrigin, evaluations[0].Evaluator.TargetConfigHash)
		wrongTeamEvaluatorRun := insertRun(evaluations[0].Evaluator.Target, defaultTeam.ID()+1000, "other-team", evaluatorOrigin, evaluations[0].Evaluator.TargetConfigHash)
		wrongTargetEvaluatorRun := insertRun(reclaimed[0].Target, defaultTeam.ID(), defaultTeam.Name(), evaluatorOrigin, evaluations[0].Evaluator.TargetConfigHash)
		driftedEvaluatorRun := insertRun(evaluations[0].Evaluator.Target, defaultTeam.ID(), defaultTeam.Name(), evaluatorOrigin, strings.Repeat("f", 64))
		Expect(factory.RecordEvaluatorRun(ctx, reclaimed[0].ID, snapshot.WorkflowRunID(9_999_998))).To(BeFalse())
		Expect(factory.RecordEvaluatorRun(ctx, reclaimed[0].ID, wrongTeamEvaluatorRun)).To(BeFalse())
		Expect(factory.RecordEvaluatorRun(ctx, reclaimed[0].ID, wrongTargetEvaluatorRun)).To(BeFalse())
		Expect(factory.RecordEvaluatorRun(ctx, reclaimed[0].ID, driftedEvaluatorRun)).To(BeFalse())
		Expect(factory.RecordEvaluatorRun(ctx, reclaimed[0].ID, evaluatorRun)).To(BeTrue())
		Expect(factory.RecordEvaluatorRun(ctx, reclaimed[0].ID, evaluatorRun)).To(BeTrue())
		Expect(factory.RecordEvaluatorRun(ctx, reclaimed[0].ID, conflictingEvaluatorRun)).To(BeFalse())
		candidatePlannedBuild := int64(candidateRun)*10 + 1
		candidateAnomalyBuild := int64(candidateRun)*10 + 2
		evaluatorPlannedBuild := int64(evaluatorRun)*10 + 1
		evaluatorAnomalyBuild := int64(evaluatorRun)*10 + 2
		_, err = dbConn.Exec(`UPDATE agent_workflow_runs SET planned_build_id = $2 WHERE id = $1`, int64(candidateRun), candidatePlannedBuild)
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`UPDATE agent_workflow_runs SET planned_build_id = $2 WHERE id = $1`, int64(evaluatorRun), evaluatorPlannedBuild)
		Expect(err).NotTo(HaveOccurred())
		for _, anomaly := range []struct {
			run   snapshot.WorkflowRunID
			build int64
		}{
			{run: candidateRun, build: candidateAnomalyBuild},
			{run: evaluatorRun, build: evaluatorAnomalyBuild},
		} {
			_, err = dbConn.Exec(`
				INSERT INTO agent_workflow_run_anomalies
					(workflow_run_id, kind, build_id, build_status)
				VALUES ($1, 'later_build_completed', $2, 'succeeded')
			`, int64(anomaly.run), anomaly.build)
			Expect(err).NotTo(HaveOccurred())
		}
		for index, build := range []int64{
			candidatePlannedBuild, candidateAnomalyBuild,
			evaluatorPlannedBuild, evaluatorAnomalyBuild,
		} {
			value := int64(index + 1)
			_, err = dbConn.Exec(`
				INSERT INTO agent_run_metrics
					(build_id, plan_id, step_name, status, input_tokens,
					 output_tokens, wall_time_seconds, cost_usd)
				VALUES ($1, $2, 'experiment-step', 'ok', $3, $4, $5, $6)
			`, build, fmt.Sprintf("plan-%d", index), value*10, value, value, value)
			Expect(err).NotTo(HaveOccurred())
		}
		document := contracts.MeasurementsDocument{
			Conclusion: "measured",
			Metrics:    []contracts.Measurement{{ID: "quality", Value: 0.8, Unit: "score", Direction: "higher-is-better"}},
		}
		Expect(factory.RecordMeasurements(ctx, reclaimed[0].ID, document)).To(Succeed())
		Expect(factory.CompleteEvaluation(ctx, reclaimed[0].ID, experiment.CellValidMeasurement, &measurementSnapshot)).To(BeTrue())
		Expect(factory.CompleteEvaluation(ctx, reclaimed[0].ID, experiment.CellValidMeasurement, &measurementSnapshot)).To(BeTrue())
		Expect(factory.CompleteEvaluation(ctx, reclaimed[0].ID, experiment.CellEvaluatorFailure, nil)).To(BeFalse())

		cell, found, err := factory.GetCell(ctx, defaultTeam.ID(), started.ID, reclaimed[0].ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(cell.Status).To(Equal(experiment.CellValidMeasurement))
		Expect(cell.MeasurementSnapshotID).NotTo(BeNil())

		if cell.VariantLabel != "control" {
			// Start inserts variants in definition order; keep this assertion as a
			// guard because a scorecard must never infer a different control.
			Fail("first durable variant was not the frozen control")
		}
		_, err = factory.Scorecard(ctx, defaultTeam.ID(), started.ID)
		Expect(errors.Is(err, experiment.ErrScorecardUnavailable)).To(BeTrue())
		for _, pending := range []experiment.CandidateCell{first[1], second[0], second[1]} {
			Expect(factory.RecordCandidateFailure(ctx, pending.ID, "invalid_admission")).To(Succeed())
		}
		var scorecardFrozen bool
		Expect(dbConn.QueryRow(`
			SELECT frozen_scorecard IS NOT NULL
			FROM agent_experiments WHERE id = $1
		`, int64(started.ID)).Scan(&scorecardFrozen)).To(Succeed())
		Expect(scorecardFrozen).To(BeTrue(), "the scorecard must freeze in the terminal state transition")

		scorecard, err := factory.Scorecard(ctx, defaultTeam.ID(), started.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(scorecard.Control).To(Equal("control"))
		Expect(scorecard.Cells).To(HaveLen(4))
		Expect(scorecard.Cells[0].Measurements).To(Equal(document.Metrics))
		Expect(scorecard.Cells[0].CostUSD).To(Equal(10.0))
		Expect(scorecard.Cells[0].Latency).To(Equal(10 * time.Second))
		Expect(scorecard.Cells[0].InputTokens).To(Equal(int64(100)))
		Expect(scorecard.Cells[0].OutputTokens).To(Equal(int64(10)))

		lateAnomalyBuild := int64(candidateRun)*10 + 3
		_, err = dbConn.Exec(`
			INSERT INTO agent_workflow_run_anomalies
				(workflow_run_id, kind, build_id, build_status)
			VALUES ($1, 'later_build_completed', $2, 'succeeded')
		`, int64(candidateRun), lateAnomalyBuild)
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`
			INSERT INTO agent_run_metrics
				(build_id, plan_id, step_name, status, input_tokens,
				 output_tokens, wall_time_seconds, cost_usd)
			VALUES ($1, 'late-plan', 'late-experiment-step', 'ok', 500, 50, 50, 50)
		`, lateAnomalyBuild)
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`
			UPDATE agent_run_metrics SET cost_usd = cost_usd + 100
			WHERE build_id = $1
		`, candidatePlannedBuild)
		Expect(err).NotTo(HaveOccurred())

		frozenScorecard, err := factory.Scorecard(ctx, defaultTeam.ID(), started.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(frozenScorecard).To(Equal(scorecard),
			"terminal scorecards must not change when late telemetry arrives or existing telemetry is corrected")
	})

	It("stops claims immediately and finalizes cancellation only through durable reconciliation", func() {
		created, err := factory.Create(ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", definition(fixtureSnapshot))
		Expect(err).NotTo(HaveOccurred())
		started, err := factory.Start(ctx, defaultTeam.ID(), created.ID, created.Revision, "alice")
		Expect(err).NotTo(HaveOccurred())
		canceling, err := factory.Cancel(ctx, defaultTeam.ID(), created.ID, started.Revision, "alice")
		Expect(err).NotTo(HaveOccurred())
		Expect(canceling.Definition.State).To(Equal(experiment.StateCanceling))
		Expect(canceling.CompletedAt).To(BeNil())
		claimed, err := factory.ClaimCandidateCells(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(claimed).To(BeEmpty())
		work, err := factory.ClaimCancellations(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(work).To(ConsistOf(experiment.CancellationWork{ExperimentID: created.ID, TeamID: defaultTeam.ID()}))

		finalized, err := factory.FinalizeCancellation(ctx, defaultTeam.ID(), created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(finalized).To(BeTrue())
		canceled, found, err := factory.Get(ctx, defaultTeam.ID(), created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(canceled.Definition.State).To(Equal(experiment.StateCanceled))
		Expect(canceled.CompletedAt).NotTo(BeNil())
		cells, err := factory.ListCells(ctx, defaultTeam.ID(), created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(cells).To(HaveLen(4))
		for _, cell := range cells {
			Expect(cell.Status).To(Equal(experiment.CellCanceled))
			Expect(cell.CompletedAt).NotTo(BeNil())
		}
		scorecard, err := factory.Scorecard(ctx, defaultTeam.ID(), created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(scorecard.Cells).To(HaveLen(4))
		for _, cell := range scorecard.Cells {
			Expect(cell.Status).To(Equal(experiment.CellCanceled))
		}
	})

	It("discovers and cancels a child allocated immediately before parent cancellation", func() {
		created, err := factory.Create(ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", definition(fixtureSnapshot))
		Expect(err).NotTo(HaveOccurred())
		started, err := factory.Start(ctx, defaultTeam.ID(), created.ID, created.Revision, "alice")
		Expect(err).NotTo(HaveOccurred())
		cells, err := factory.ClaimCandidateCells(ctx, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(cells).To(HaveLen(1))
		cell := cells[0]
		Expect(dbConn.Stats().MaxOpenConnections).To(Equal(1))

		runs := db.NewAgentWorkflowRunsFactory(dbConn)
		request := candidateRunRequest(cell, "experiment-gate-allocation-first")
		run, wasCreated, err := runs.CreateWithInputs(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(wasCreated).To(BeTrue())

		canceling, err := factory.Cancel(ctx, defaultTeam.ID(), started.ID, started.Revision, "alice")
		Expect(err).NotTo(HaveOccurred())
		Expect(canceling.Definition.State).To(Equal(experiment.StateCanceling))

		work, err := factory.ClaimCancellations(ctx, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(work).To(HaveLen(1))
		Expect(work[0].WorkflowRunIDs).To(ContainElement(run.ID))

		// Even an already-allocated idempotent child must re-check the live
		// parent gate instead of bypassing it through the existing-run path.
		_, _, err = runs.CreateWithInputs(ctx, request)
		Expect(errors.Is(err, db.ErrAgentWorkflowRunExperimentAdmissionClosed)).To(BeTrue())

		runCanceler, err := workflowrun.NewCanceler(runs, buildFactory)
		Expect(err).NotTo(HaveOccurred())
		adapter, err := workflowrun.NewExperimentRunCancelerAdapter(runCanceler)
		Expect(err).NotTo(HaveOccurred())
		reconciler, err := experiment.NewCancellationReconciler(factory, adapter, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(reconciler.Run(ctx)).To(Succeed())

		canceledRun, found, err := runs.Get(ctx, defaultTeam.ID(), run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(canceledRun.Status).To(Equal(db.AgentWorkflowRunStatusAborted))
		canceled, found, err := factory.Get(ctx, defaultTeam.ID(), started.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(canceled.Definition.State).To(Equal(experiment.StateCanceled))
	})

	It("allocates and associates a child without requiring a second database connection", func() {
		created, err := factory.Create(ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", definition(fixtureSnapshot))
		Expect(err).NotTo(HaveOccurred())
		_, err = factory.Start(ctx, defaultTeam.ID(), created.ID, created.Revision, "alice")
		Expect(err).NotTo(HaveOccurred())
		cells, err := factory.ClaimCandidateCells(ctx, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(cells).To(HaveLen(1))
		cell := cells[0]
		Expect(dbConn.Stats().MaxOpenConnections).To(Equal(1))

		runs := db.NewAgentWorkflowRunsFactory(dbConn)
		request := candidateRunRequest(cell, "experiment-gate-one-connection")
		admission, err := factory.CreateAndRecordCandidateRun(
			ctx,
			cell,
			func(bindContext context.Context) (experiment.BindResult, error) {
				run, _, createErr := runs.CreateWithInputs(bindContext, request)
				if createErr != nil {
					return experiment.BindResult{}, createErr
				}
				return experiment.BindResult{
					WorkflowRunID:        run.ID,
					WorkflowDefinitionID: int64(run.WorkflowDefinitionID),
					WorkflowName:         run.WorkflowName,
					WorkflowVersion:      run.WorkflowVersion,
					FunctionID:           cell.Target.FunctionID,
					TargetConfigHash:     run.ParameterizedConfigHash,
				}, nil
			},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(admission.Started).To(BeTrue())
		Expect(admission.Recorded).To(BeTrue())
		Expect(admission.Result.WorkflowRunID.Validate()).To(Succeed())
	})

	It("requires the durable cell reservation before allocating a budgeted experiment child", func() {
		value := definition(fixtureSnapshot)
		value.Budget = experiment.Budget{PerCellUSD: 1, TotalUSD: 4}
		created, err := factory.Create(ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", value)
		Expect(err).NotTo(HaveOccurred())
		_, err = factory.Start(ctx, defaultTeam.ID(), created.ID, created.Revision, "alice")
		Expect(err).NotTo(HaveOccurred())
		cells, err := factory.ClaimCandidateCells(ctx, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(cells).To(HaveLen(1))

		runs := db.NewAgentWorkflowRunsFactory(dbConn)
		request := candidateRunRequest(cells[0], "experiment-budget-gate")
		_, _, err = runs.CreateWithInputs(ctx, request)
		Expect(errors.Is(err, db.ErrAgentWorkflowRunExperimentAdmissionClosed)).To(BeTrue())

		_, err = factory.ReserveCandidateBudget(ctx, cells[0].ID)
		Expect(err).NotTo(HaveOccurred())
		run, wasCreated, err := runs.CreateWithInputs(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(wasCreated).To(BeTrue())
		Expect(run.OriginReference).To(Equal(request.OriginReference))
	})

	It("fails child allocation closed when parent cancellation commits first", func() {
		created, err := factory.Create(ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", definition(fixtureSnapshot))
		Expect(err).NotTo(HaveOccurred())
		started, err := factory.Start(ctx, defaultTeam.ID(), created.ID, created.Revision, "alice")
		Expect(err).NotTo(HaveOccurred())
		cells, err := factory.ClaimCandidateCells(ctx, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(cells).To(HaveLen(1))
		cell := cells[0]
		Expect(dbConn.Stats().MaxOpenConnections).To(Equal(1))

		runs := db.NewAgentWorkflowRunsFactory(dbConn)
		request := candidateRunRequest(cell, "experiment-gate-cancel-first")
		driftedTarget := request
		driftedTarget.IdempotencyKey = "experiment-gate-drifted-target"
		driftedTarget.ParameterizedConfigHash = strings.Repeat("f", 64)
		_, _, err = runs.CreateWithInputs(ctx, driftedTarget)
		Expect(errors.Is(err, db.ErrAgentWorkflowRunExperimentAdmissionClosed)).To(BeTrue())

		_, err = factory.Cancel(ctx, defaultTeam.ID(), started.ID, started.Revision, "alice")
		Expect(err).NotTo(HaveOccurred())
		_, _, err = runs.CreateWithInputs(ctx, request)
		Expect(errors.Is(err, db.ErrAgentWorkflowRunExperimentAdmissionClosed)).To(BeTrue())

		missingCell := request
		missingCell.IdempotencyKey = "experiment-gate-missing-cell"
		missingCell.ExperimentAdmission = &db.AgentWorkflowRunExperimentAdmission{
			ExperimentID: int64(cell.ExperimentID),
			CellID:       int64(cell.ID) + 1_000_000,
			Phase:        "candidate",
		}
		missingCell.OriginReference = fmt.Sprintf(
			"experiment:%s:cell:%d",
			cell.ExperimentID.String(), int64(cell.ID)+1_000_000,
		)
		_, _, err = runs.CreateWithInputs(ctx, missingCell)
		Expect(errors.Is(err, db.ErrAgentWorkflowRunExperimentAdmissionClosed)).To(BeTrue())

		var children int
		Expect(dbConn.QueryRow(`
			SELECT count(*)
			FROM agent_workflow_runs
			WHERE team_id = $1 AND origin_kind = 'experiment' AND origin_reference = $2
		`, defaultTeam.ID(), request.OriginReference).Scan(&children)).To(Succeed())
		Expect(children).To(BeZero())
	})

	It("applies the same cancellation gate to evaluator allocation and idempotent replay", func() {
		created, err := factory.Create(ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", definition(fixtureSnapshot))
		Expect(err).NotTo(HaveOccurred())
		started, err := factory.Start(ctx, defaultTeam.ID(), created.ID, created.Revision, "alice")
		Expect(err).NotTo(HaveOccurred())
		candidates, err := factory.ClaimCandidateCells(ctx, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(candidates).To(HaveLen(1))
		candidate := candidates[0]
		candidateOrigin := fmt.Sprintf(
			"experiment:%s:cell:%s", started.ID.String(), candidate.ID.String(),
		)
		candidateRun := insertRun(
			candidate.Target, defaultTeam.ID(), defaultTeam.Name(),
			candidateOrigin, candidate.TargetConfigHash,
		)
		Expect(factory.RecordCandidateRun(ctx, candidate.ID, candidateRun)).To(BeTrue())
		evaluations, err := factory.ClaimEvaluationCells(ctx, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(evaluations).To(HaveLen(1))
		evaluation := evaluations[0]

		reviewSnapshot := insertSnapshot("review", "d")
		request := evaluatorRunRequest(evaluation, map[string]snapshot.SnapshotID{
			"candidate": reviewSnapshot,
			"repo":      fixtureSnapshot,
		}, "experiment-gate-evaluator")
		runs := db.NewAgentWorkflowRunsFactory(dbConn)
		evaluatorRun, wasCreated, err := runs.CreateWithInputs(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(wasCreated).To(BeTrue())

		_, err = factory.Cancel(ctx, defaultTeam.ID(), started.ID, started.Revision, "alice")
		Expect(err).NotTo(HaveOccurred())
		Expect(factory.RecordEvaluatorRun(ctx, evaluation.ID, evaluatorRun.ID)).To(BeTrue())
		var evaluationStatus string
		Expect(dbConn.QueryRow(`
			SELECT status
			FROM agent_experiment_evaluations
			WHERE cell_id = $1
		`, int64(evaluation.ID)).Scan(&evaluationStatus)).To(Succeed())
		Expect(evaluationStatus).To(Equal("canceled"))
		work, err := factory.ClaimCancellations(ctx, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(work).To(HaveLen(1))
		Expect(work[0].WorkflowRunIDs).To(ContainElements(candidateRun, evaluatorRun.ID))

		_, _, err = runs.CreateWithInputs(ctx, request)
		Expect(errors.Is(err, db.ErrAgentWorkflowRunExperimentAdmissionClosed)).To(BeTrue())
	})

	It("rejects fixtures owned by another team", func() {
		definition := definition(fixtureSnapshot)
		other, err := teamFactory.CreateTeam(structTeam(fmt.Sprintf("experiment-other-%d", time.Now().UnixNano())))
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`UPDATE agent_snapshots SET team_id = $2 WHERE id = $1`, int64(fixtureSnapshot), other.ID())
		Expect(err).NotTo(HaveOccurred())
		_, err = factory.Create(ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", definition)
		Expect(errors.Is(err, experimentsapi.ErrInvalidDefinition)).To(BeTrue())
	})

	It("revalidates retained fixture content before atomically starting", func() {
		created, err := factory.Create(ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", definition(fixtureSnapshot))
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`UPDATE agent_snapshots SET content_state = 'expired' WHERE id = $1`, int64(fixtureSnapshot))
		Expect(err).NotTo(HaveOccurred())

		_, err = factory.Start(ctx, defaultTeam.ID(), created.ID, created.Revision, "alice")
		Expect(errors.Is(err, experimentsapi.ErrInvalidDefinition)).To(BeTrue())
		stored, found, getErr := factory.Get(ctx, defaultTeam.ID(), created.ID)
		Expect(getErr).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(stored.Definition.State).To(Equal(experiment.StateDraft))
		cells, listErr := factory.ListCells(ctx, defaultTeam.ID(), created.ID)
		Expect(listErr).NotTo(HaveOccurred())
		Expect(cells).To(BeEmpty())
	})

	It("starts budgeted matrices and reserves each cell exactly once across restarts", func() {
		value := definition(fixtureSnapshot)
		value.Budget = experiment.Budget{PerCellUSD: 1, TotalUSD: 2}
		created, err := factory.Create(ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", value)
		Expect(err).NotTo(HaveOccurred())

		_, err = factory.Start(ctx, defaultTeam.ID(), created.ID, created.Revision, "alice")
		Expect(err).NotTo(HaveOccurred())
		cells, err := factory.ClaimCandidateCells(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(cells).To(HaveLen(4))
		Expect([]int{cells[0].Repetition, cells[1].Repetition, cells[2].Repetition, cells[3].Repetition}).To(Equal([]int{1, 1, 2, 2}))
		Expect(cells[0].VariantID).NotTo(Equal(cells[1].VariantID))
		Expect(cells[2].VariantID).To(Equal(cells[1].VariantID))
		Expect(cells[3].VariantID).To(Equal(cells[0].VariantID))
		for _, cell := range cells {
			Expect(cell.Budget).To(Equal(value.Budget))
		}

		first, err := factory.ReserveCandidateBudget(ctx, cells[0].ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(first).To(Equal(experiment.BudgetReservation{CellID: cells[0].ID, LimitUSD: 1}))
		restarted := db.NewAgentExperimentsFactory(dbConn, targetRenderer)
		retried, err := restarted.ReserveCandidateBudget(ctx, cells[0].ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(retried).To(Equal(first))

		second, err := factory.ReserveCandidateBudget(ctx, cells[1].ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(second.LimitUSD).To(Equal(1.0))
		_, err = factory.ReserveCandidateBudget(ctx, cells[2].ID)
		Expect(errors.Is(err, experiment.ErrBudgetExhausted)).To(BeTrue())
		Expect(factory.RecordCandidateFailure(ctx, cells[2].ID, "budget_denied")).To(Succeed())
		skipped, found, err := factory.GetCell(ctx, defaultTeam.ID(), created.ID, cells[2].ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(skipped.Status).To(Equal(experiment.CellSkippedBudget))
		Expect(skipped.CandidateFailureCategory).To(Equal("budget_denied"))

		var reservations int
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_experiment_budget_reservations WHERE experiment_id = $1
		`, int64(created.ID)).Scan(&reservations)).To(Succeed())
		Expect(reservations).To(Equal(2))
	})

	It("fails start atomically when agent slices cannot fit the cell reservation", func() {
		value := definition(fixtureSnapshot)
		value.Budget = experiment.Budget{PerCellUSD: 0.4}
		created, err := factory.Create(ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", value)
		Expect(err).NotTo(HaveOccurred())

		for _, preflight := range []func() error{
			func() error {
				_, preflightErr := factory.PreflightStart(ctx, defaultTeam.ID(), created.ID, created.Revision)
				return preflightErr
			},
			func() error {
				_, startErr := factory.Start(ctx, defaultTeam.ID(), created.ID, created.Revision, "alice")
				return startErr
			},
		} {
			err = preflight()
			Expect(errors.Is(err, experiment.ErrInvalidDefinition)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("exceeds the 0.400000 USD cell reservation"))
		}
		stored, found, getErr := factory.Get(ctx, defaultTeam.ID(), created.ID)
		Expect(getErr).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(stored.Definition.State).To(Equal(experiment.StateDraft))
		cells, listErr := factory.ListCells(ctx, defaultTeam.ID(), created.ID)
		Expect(listErr).NotTo(HaveOccurred())
		Expect(cells).To(BeEmpty())
	})

	It("fails start explicitly when the runtime cannot enforce a token cap", func() {
		value := definition(fixtureSnapshot)
		value.Budget = experiment.Budget{MaxTokensPerCell: 100}
		created, err := factory.Create(ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", value)
		Expect(err).NotTo(HaveOccurred())

		_, err = factory.Start(ctx, defaultTeam.ID(), created.ID, created.Revision, "alice")
		Expect(errors.Is(err, experiment.ErrInvalidDefinition)).To(BeTrue())
		Expect(err.Error()).To(ContainSubstring("max_tokens_per_cell cannot be hard-enforced"))
	})

	It("serializes concurrent reservations against the experiment envelope", func() {
		value := definition(fixtureSnapshot)
		value.Budget = experiment.Budget{PerCellUSD: 1, TotalUSD: 1}
		created, err := factory.Create(ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", value)
		Expect(err).NotTo(HaveOccurred())
		_, err = factory.Start(ctx, defaultTeam.ID(), created.ID, created.Revision, "alice")
		Expect(err).NotTo(HaveOccurred())
		cells, err := factory.ClaimCandidateCells(ctx, 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(cells).To(HaveLen(2))

		factories := []db.AgentExperimentsFactory{
			db.NewAgentExperimentsFactory(dbConn, targetRenderer),
			db.NewAgentExperimentsFactory(dbConn, targetRenderer),
		}
		errorsByCell := make(chan error, 2)
		var wait sync.WaitGroup
		for index := range 2 {
			wait.Add(1)
			go func(index int) {
				defer GinkgoRecover()
				defer wait.Done()
				_, reserveErr := factories[index].ReserveCandidateBudget(ctx, cells[index].ID)
				errorsByCell <- reserveErr
			}(index)
		}
		wait.Wait()
		close(errorsByCell)
		var admitted, exhausted int
		for reserveErr := range errorsByCell {
			switch {
			case reserveErr == nil:
				admitted++
			case errors.Is(reserveErr, experiment.ErrBudgetExhausted):
				exhausted++
			default:
				Fail(fmt.Sprintf("unexpected reservation error: %v", reserveErr))
			}
		}
		Expect(admitted).To(Equal(1))
		Expect(exhausted).To(Equal(1))
	})

	It("releases an unused reservation when admission fails before a workflow run exists", func() {
		value := definition(fixtureSnapshot)
		value.Budget = experiment.Budget{PerCellUSD: 1, TotalUSD: 1}
		created, err := factory.Create(ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", value)
		Expect(err).NotTo(HaveOccurred())
		_, err = factory.Start(ctx, defaultTeam.ID(), created.ID, created.Revision, "alice")
		Expect(err).NotTo(HaveOccurred())
		cells, err := factory.ClaimCandidateCells(ctx, 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(cells).To(HaveLen(2))
		_, err = factory.ReserveCandidateBudget(ctx, cells[0].ID)
		Expect(err).NotTo(HaveOccurred())

		Expect(factory.RecordCandidateFailure(ctx, cells[0].ID, "platform_failure")).To(Succeed())
		var state string
		Expect(dbConn.QueryRow(`
			SELECT state FROM agent_experiment_budget_reservations WHERE cell_id = $1
		`, int64(cells[0].ID)).Scan(&state)).To(Succeed())
		Expect(state).To(Equal("released"))

		reservation, err := factory.ReserveCandidateBudget(ctx, cells[1].ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(reservation.CellID).To(Equal(cells[1].ID))
	})

	It("keeps a completed run's full reservation liable when ledger ingestion is delayed", func() {
		value := definition(fixtureSnapshot)
		value.Budget = experiment.Budget{PerCellUSD: 1, TotalUSD: 1}
		created, err := factory.Create(ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", value)
		Expect(err).NotTo(HaveOccurred())
		started, err := factory.Start(ctx, defaultTeam.ID(), created.ID, created.Revision, "alice")
		Expect(err).NotTo(HaveOccurred())
		cells, err := factory.ClaimCandidateCells(ctx, 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(cells).To(HaveLen(2))
		_, err = factory.ReserveCandidateBudget(ctx, cells[0].ID)
		Expect(err).NotTo(HaveOccurred())

		origin := fmt.Sprintf("experiment:%s:cell:%s", started.ID.String(), cells[0].ID.String())
		runID := insertRun(cells[0].Target, defaultTeam.ID(), defaultTeam.Name(), origin, cells[0].TargetConfigHash)
		Expect(factory.RecordCandidateRun(ctx, cells[0].ID, runID)).To(BeTrue())
		completed, err := factory.CompleteEvaluation(
			ctx, cells[0].ID, experiment.CellCandidatePlatformFailure, nil,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(completed).To(BeTrue())
		var state string
		Expect(dbConn.QueryRow(`
			SELECT state FROM agent_experiment_budget_reservations WHERE cell_id = $1
		`, int64(cells[0].ID)).Scan(&state)).To(Succeed())
		Expect(state).To(Equal("settled"))

		_, err = factory.ReserveCandidateBudget(ctx, cells[1].ID)
		Expect(errors.Is(err, experiment.ErrBudgetExhausted)).To(BeTrue())
	})

	It("allows the statically bounded evaluator at exact usage and enforces the final and global envelopes", func() {
		value := definition(fixtureSnapshot)
		value.Budget = experiment.Budget{PerCellUSD: 1, TotalUSD: 10}
		created, err := factory.Create(ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", value)
		Expect(err).NotTo(HaveOccurred())
		started, err := factory.Start(ctx, defaultTeam.ID(), created.ID, created.Revision, "alice")
		Expect(err).NotTo(HaveOccurred())
		cells, err := factory.ClaimCandidateCells(ctx, 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(cells).To(HaveLen(2))
		_, err = factory.ReserveCandidateBudget(ctx, cells[0].ID)
		Expect(err).NotTo(HaveOccurred())

		origin := fmt.Sprintf("experiment:%s:cell:%s", started.ID.String(), cells[0].ID.String())
		runID := insertRun(cells[0].Target, defaultTeam.ID(), defaultTeam.Name(), origin, cells[0].TargetConfigHash)
		Expect(factory.RecordCandidateRun(ctx, cells[0].ID, runID)).To(BeTrue())
		evaluations, err := factory.ClaimEvaluationCells(ctx, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(evaluations).To(HaveLen(1))
		Expect(evaluations[0].Budget).To(Equal(value.Budget))
		// spend is attributed by the durable run id the server assigned it
		_, err = dbConn.Exec(`
			INSERT INTO agent_cost_ledger
				(workflow_run_id, function_id, step_name, source, input_tokens, output_tokens, cost_usd)
			VALUES ($1, 'candidate', 'candidate', 'agent_step', 80, 30, 1.00)
		`, int64(runID))
		Expect(err).NotTo(HaveOccurred())
		usage, err := factory.CheckCellBudget(ctx, cells[0].ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(usage.CostUSD).To(BeNumerically("~", 1.0, 1e-9))
		evaluatorReservation, err := factory.AdmitEvaluatorBudget(ctx, cells[0].ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(evaluatorReservation.CellID).To(Equal(cells[0].ID))
		_, err = dbConn.Exec(`
			UPDATE agent_cost_ledger SET cost_usd = 1.01 WHERE workflow_run_id = $1
		`, int64(runID))
		Expect(err).NotTo(HaveOccurred())

		usage, err = factory.CheckCellBudget(ctx, cells[0].ID)
		Expect(errors.Is(err, experiment.ErrBudgetExhausted)).To(BeTrue())
		Expect(usage.CostUSD).To(BeNumerically("~", 1.01, 1e-9))
		Expect(usage.Tokens).To(Equal(int64(110)))

		globalFactory := db.NewAgentExperimentsFactory(dbConn, targetRenderer,
			db.WithAgentExperimentBudgetConfig(db.AgentExperimentBudgetConfig{
				GlobalDailyCapUSD: 2, Location: time.UTC, Now: func() time.Time { return time.Now().UTC() },
			}),
		)
		_, err = globalFactory.ReserveCandidateBudget(ctx, cells[1].ID)
		Expect(errors.Is(err, experiment.ErrBudgetExhausted)).To(BeTrue())
	})

	DescribeTable("rejects client-forged immutable workflow target metadata",
		func(mutate func(*experiment.Definition)) {
			value := definition(fixtureSnapshot)
			mutate(&value)
			_, err := factory.Create(ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", value)
			Expect(errors.Is(err, experimentsapi.ErrInvalidDefinition)).To(BeTrue())
		},
		Entry("variant definition ID", func(value *experiment.Definition) {
			value.Variants[0].Target.DefinitionID++
		}),
		Entry("variant workflow name", func(value *experiment.Definition) {
			value.Variants[0].Target.WorkflowName += "-forged"
		}),
		Entry("variant workflow version", func(value *experiment.Definition) {
			value.Variants[0].Target.Version++
		}),
		Entry("variant function ID", func(value *experiment.Definition) {
			value.Variants[1].Target.FunctionID = "missing"
		}),
		Entry("candidate signature", func(value *experiment.Definition) {
			value.Signature.Outputs[0].Type = "forged/v1"
			value.Evaluator.Signature.Inputs[0].Type = "forged/v1"
			hash, err := experiment.HashSignature(value.Signature)
			Expect(err).NotTo(HaveOccurred())
			for index := range value.Variants {
				value.Variants[index].SignatureHash = hash
			}
		}),
		Entry("evaluator definition ID", func(value *experiment.Definition) {
			value.Evaluator.Target.DefinitionID++
		}),
		Entry("evaluator signature", func(value *experiment.Definition) {
			value.Evaluator.Signature.Inputs[0].Optional = true
		}),
	)

	It("round-trips definitions as semantic JSON rather than mutable workflow rows", func() {
		definition := definition(fixtureSnapshot)
		var dbBefore time.Time
		Expect(dbConn.QueryRow(`SELECT clock_timestamp()`).Scan(&dbBefore)).To(Succeed())
		created, err := factory.Create(ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", definition)
		Expect(err).NotTo(HaveOccurred())
		stored, found, err := factory.Get(ctx, defaultTeam.ID(), created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		var dbAfter time.Time
		Expect(dbConn.QueryRow(`SELECT clock_timestamp()`).Scan(&dbAfter)).To(Succeed())
		left, err := json.Marshal(definition)
		Expect(err).NotTo(HaveOccurred())
		right, err := json.Marshal(stored.Definition)
		Expect(err).NotTo(HaveOccurred())
		Expect(right).To(MatchJSON(left))
		Expect(stored.CreatedAt).To(BeTemporally(">=", dbBefore))
		Expect(stored.CreatedAt).To(BeTemporally("<=", dbAfter))
	})

	Context("reusable node targets", func() {
		var nodeDefinition *workflow.NodeDefinition

		// insertKindRun is insertRun with the durable executable kind under the
		// caller's control. The kind is the one frozen coordinate that a node
		// run and a same-named workflow run need not otherwise differ on, so
		// the association matchers can only be exercised by seeding a run whose
		// kind disagrees with its cell.
		insertKindRun := func(
			kind workflow.DefinitionKind,
			target experiment.Target,
			originReference, targetConfigHash string,
		) snapshot.WorkflowRunID {
			runSequence++
			var id snapshot.WorkflowRunID
			Expect(dbConn.QueryRow(`
				INSERT INTO agent_workflow_runs
					(definition_kind, team_id, team_name, workflow_definition_id, workflow_name,
					 workflow_version, schema_version, signature_version, definition_content_hash,
					 idempotency_key, parameterized_config, parameterized_config_hash,
					 origin_kind, origin_reference, created_by, status)
				SELECT $1, $2, $3, definition.id, definition.name, definition.version,
				       definition.schema_version, definition.signature_version,
				       definition.content_hash, $5, '{}', $6, 'experiment', $7, 'alice', 'running'
				FROM agent_workflow_definitions definition
				WHERE definition.id = $4
				RETURNING id
			`, string(kind), defaultTeam.ID(), defaultTeam.Name(), target.DefinitionID,
				fmt.Sprintf("experiment-node-%d-%d", time.Now().UnixNano(), runSequence),
				targetConfigHash, originReference).Scan(&id)).To(Succeed())
			return id
		}

		nodeSignature := func() workflow.PublicSignature {
			return workflow.PublicSignature{
				Inputs:  []workflow.SignaturePort{{Name: "repo", Type: "repository/v1"}},
				Outputs: []workflow.SignaturePort{{Name: "review", Type: "review/v1"}},
			}
		}

		nodeTarget := func(parameters map[string]string) experiment.Target {
			return experiment.Target{
				Kind: experiment.TargetNode, WorkflowName: nodeDefinition.Name,
				DefinitionID: int64(nodeDefinition.ID), Version: nodeDefinition.Version,
				NodeParameters: parameters,
			}
		}

		nodeExperimentDefinition := func(input snapshot.SnapshotID) experiment.Definition {
			signature := nodeSignature()
			hash, err := experiment.HashSignature(signature)
			Expect(err).NotTo(HaveOccurred())
			return experiment.Definition{
				Name: "graded-node-prompts", State: experiment.StateDraft, Signature: signature,
				Variants: []experiment.Variant{
					{
						Label: "control", Control: true, SignatureHash: hash,
						Target: nodeTarget(map[string]string{"MINIMUM_SEVERITY": "medium"}),
					},
					{
						Label: "candidate", SignatureHash: hash,
						Target: nodeTarget(map[string]string{"MINIMUM_SEVERITY": "high"}),
					},
				},
				Fixtures: []experiment.Fixture{{
					Label: "small", Role: experiment.FixtureNormal,
					Inputs: map[string]snapshot.SnapshotID{"repo": input},
				}},
				Evaluator: experiment.Evaluator{
					Target: experiment.Target{
						Kind: experiment.TargetWorkflow, WorkflowName: evaluatorDefinition.Name,
						DefinitionID: int64(evaluatorDefinition.ID), Version: evaluatorDefinition.Version,
					},
					Signature: workflow.PublicSignature{
						Inputs: []workflow.SignaturePort{
							{Name: "candidate", Type: "review/v1"},
							{Name: "repo", Type: "repository/v1"},
						},
						Outputs: []workflow.SignaturePort{{Name: "measurements", Type: "measurements/v1"}},
					},
					Mappings: []experiment.EvaluatorMapping{
						{EvaluatorPort: "candidate", SourceDirection: experiment.SourceCandidateOutput, SourcePort: "review"},
						{EvaluatorPort: "repo", SourceDirection: experiment.SourceFixtureInput, SourcePort: "repo"},
					},
					MeasurementsPort: "measurements",
				},
				Repetitions: 1,
			}
		}

		BeforeEach(func() {
			var err error
			nodeDefinition, err = db.NewAgentNodesFactory(dbConn).ImportManifest(
				"graded-review",
				workflow.Manifest{workflow.NodeFileName: `schema_version: 1
name: graded-review
description: graded reusable review node
inputs:
  - {name: repo, type: repository/v1}
outputs:
  - {name: review, type: review/v1}
parameters:
  - {name: MINIMUM_SEVERITY, default: medium}
step:
  agent: review
  prompt: Review the immutable repository snapshot.
`},
				"alice",
			)
			Expect(err).NotTo(HaveOccurred())
		})

		It("round-trips a node variant with its frozen parameters intact", func() {
			value := nodeExperimentDefinition(fixtureSnapshot)
			created, err := factory.Create(ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", value)
			Expect(err).NotTo(HaveOccurred())

			stored, found, err := factory.Get(ctx, defaultTeam.ID(), created.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(stored.Definition.Variants).To(HaveLen(2))
			Expect(stored.Definition.Variants[0].Target.Kind).To(Equal(experiment.TargetNode))
			Expect(stored.Definition.Variants[0].Target.NodeParameters).To(
				Equal(map[string]string{"MINIMUM_SEVERITY": "medium"}))
			Expect(stored.Definition.Variants[1].Target.NodeParameters).To(
				Equal(map[string]string{"MINIMUM_SEVERITY": "high"}))
			// Nothing is dropped on the way through the durable row: the whole
			// semantic definition survives byte-for-byte.
			left, err := json.Marshal(value)
			Expect(err).NotTo(HaveOccurred())
			right, err := json.Marshal(stored.Definition)
			Expect(err).NotTo(HaveOccurred())
			Expect(right).To(MatchJSON(left))
		})

		It("keeps node parameters off a workflow variant", func() {
			value := nodeExperimentDefinition(fixtureSnapshot)
			value.Variants[1].Target = experiment.Target{
				Kind: experiment.TargetWorkflow, WorkflowName: nodeDefinition.Name,
				DefinitionID: int64(nodeDefinition.ID), Version: nodeDefinition.Version,
				NodeParameters: map[string]string{"MINIMUM_SEVERITY": "high"},
			}
			_, err := factory.Create(ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", value)
			Expect(errors.Is(err, experiment.ErrInvalidDefinition)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("only a node target may set node_parameters"))
		})

		It("freezes a distinct authoritative target per node parameter set", func() {
			created, err := factory.Create(
				ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", nodeExperimentDefinition(fixtureSnapshot))
			Expect(err).NotTo(HaveOccurred())

			started, err := factory.Start(ctx, defaultTeam.ID(), created.ID, created.Revision, "alice")
			Expect(err).NotTo(HaveOccurred())
			Expect(started.Definition.State).To(Equal(experiment.StateRunning))

			var hashes []string
			rows, err := dbConn.Query(`
				SELECT target_config_hash FROM agent_experiment_variants
				WHERE experiment_id = $1 ORDER BY id
			`, int64(created.ID))
			Expect(err).NotTo(HaveOccurred())
			defer rows.Close()
			for rows.Next() {
				var hash sql.NullString
				Expect(rows.Scan(&hash)).To(Succeed())
				Expect(hash.Valid).To(BeTrue(), "a started node variant must be frozen")
				hashes = append(hashes, hash.String)
			}
			Expect(hashes).To(HaveLen(2))
			// The parameter is the independent variable: if it did not reach
			// the frozen render, both cells would grade the same target.
			Expect(hashes[0]).NotTo(Equal(hashes[1]))
		})

		It("refuses to freeze a node target whose durable identity does not match", func() {
			value := nodeExperimentDefinition(fixtureSnapshot)
			value.Variants[1].Target.Version = nodeDefinition.Version + 1
			_, err := factory.Create(ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", value)
			Expect(errors.Is(err, experiment.ErrInvalidDefinition)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("not graded-review/v2"))

			value = nodeExperimentDefinition(fixtureSnapshot)
			value.Variants[1].Target.NodeParameters = map[string]string{"UNDECLARED": "1"}
			_, err = factory.Create(ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", value)
			Expect(errors.Is(err, experiment.ErrInvalidDefinition)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("unknown parameter"))

			// A workflow target may not borrow a node's definition ID either:
			// the workflow branch filters definition_kind and finds nothing.
			value = nodeExperimentDefinition(fixtureSnapshot)
			value.Variants[1].Target = experiment.Target{
				Kind: experiment.TargetWorkflow, WorkflowName: nodeDefinition.Name,
				DefinitionID: int64(nodeDefinition.ID), Version: nodeDefinition.Version,
			}
			_, err = factory.Create(ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", value)
			Expect(errors.Is(err, experiment.ErrInvalidDefinition)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("does not exist"))
		})

		It("carries the frozen node parameters to the dispatched candidate cell", func() {
			created, err := factory.Create(
				ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", nodeExperimentDefinition(fixtureSnapshot))
			Expect(err).NotTo(HaveOccurred())
			_, err = factory.Start(ctx, defaultTeam.ID(), created.ID, created.Revision, "alice")
			Expect(err).NotTo(HaveOccurred())

			claimed, err := factory.ClaimCandidateCells(ctx, 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(claimed).To(HaveLen(2))
			observed := make(map[string]string, len(claimed))
			for _, cell := range claimed {
				Expect(cell.Target.Kind).To(Equal(experiment.TargetNode))
				Expect(cell.Target.DefinitionKind()).To(Equal(workflow.DefinitionKindNode))
				Expect(cell.ResourceSourceAdmissionID).To(BeNil(),
					"a node declares no resource sources, so an experiment node bind is never blocked by the node source rule")
				observed[cell.TargetConfigHash] = cell.Target.NodeParameters["MINIMUM_SEVERITY"]
			}
			Expect(observed).To(HaveLen(2))
			values := []string{}
			for _, value := range observed {
				values = append(values, value)
			}
			sort.Strings(values)
			Expect(values).To(Equal([]string{"high", "medium"}))
		})

		It("refuses to associate a workflow run with a node cell", func() {
			created, err := factory.Create(
				ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", nodeExperimentDefinition(fixtureSnapshot))
			Expect(err).NotTo(HaveOccurred())
			started, err := factory.Start(ctx, defaultTeam.ID(), created.ID, created.Revision, "alice")
			Expect(err).NotTo(HaveOccurred())
			claimed, err := factory.ClaimCandidateCells(ctx, 1)
			Expect(err).NotTo(HaveOccurred())
			Expect(claimed).To(HaveLen(1))
			cell := claimed[0]

			origin := fmt.Sprintf("experiment:%s:cell:%s", started.ID.String(), cell.ID.String())
			// Identical on every other compared coordinate -- team, definition
			// ID, name, version, function ID, config hash, provenance, origin.
			// Only the durable executable kind differs.
			workflowKindRun := insertKindRun(
				workflow.DefinitionKindWorkflow, cell.Target, origin, cell.TargetConfigHash)
			Expect(factory.RecordCandidateRun(ctx, cell.ID, workflowKindRun)).To(BeFalse())

			nodeKindRun := insertKindRun(
				workflow.DefinitionKindNode, cell.Target, origin, cell.TargetConfigHash)
			Expect(factory.RecordCandidateRun(ctx, cell.ID, nodeKindRun)).To(BeTrue())
		})

		It("refuses to associate a node run with a workflow evaluator cell", func() {
			created, err := factory.Create(
				ctx, defaultTeam.ID(), defaultTeam.Name(), "alice", nodeExperimentDefinition(fixtureSnapshot))
			Expect(err).NotTo(HaveOccurred())
			started, err := factory.Start(ctx, defaultTeam.ID(), created.ID, created.Revision, "alice")
			Expect(err).NotTo(HaveOccurred())
			claimed, err := factory.ClaimCandidateCells(ctx, 1)
			Expect(err).NotTo(HaveOccurred())
			Expect(claimed).To(HaveLen(1))
			cell := claimed[0]
			origin := fmt.Sprintf("experiment:%s:cell:%s", started.ID.String(), cell.ID.String())
			Expect(factory.RecordCandidateRun(ctx, cell.ID, insertKindRun(
				workflow.DefinitionKindNode, cell.Target, origin, cell.TargetConfigHash))).To(BeTrue())

			evaluations, err := factory.ClaimEvaluationCells(ctx, 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(evaluations).To(HaveLen(1))
			evaluation := evaluations[0]
			Expect(evaluation.Evaluator.Target.Kind).To(Equal(experiment.TargetWorkflow))

			evaluatorOrigin := origin + ":evaluator"
			nodeKindRun := insertKindRun(
				workflow.DefinitionKindNode, evaluation.Evaluator.Target,
				evaluatorOrigin, evaluation.Evaluator.TargetConfigHash)
			Expect(factory.RecordEvaluatorRun(ctx, evaluation.ID, nodeKindRun)).To(BeFalse())

			workflowKindRun := insertKindRun(
				workflow.DefinitionKindWorkflow, evaluation.Evaluator.Target,
				evaluatorOrigin, evaluation.Evaluator.TargetConfigHash)
			Expect(factory.RecordEvaluatorRun(ctx, evaluation.ID, workflowKindRun)).To(BeTrue())
		})
	})
})
