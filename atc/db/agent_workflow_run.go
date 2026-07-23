package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/pagination"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db/encryption"
)

type AgentWorkflowRunStatus string

const (
	AgentWorkflowRunStatusAdmitting AgentWorkflowRunStatus = "admitting"
	AgentWorkflowRunStatusRunning   AgentWorkflowRunStatus = "running"
	AgentWorkflowRunStatusSucceeded AgentWorkflowRunStatus = "succeeded"
	AgentWorkflowRunStatusFailed    AgentWorkflowRunStatus = "failed"
	AgentWorkflowRunStatusErrored   AgentWorkflowRunStatus = "errored"
	AgentWorkflowRunStatusCanceling AgentWorkflowRunStatus = "canceling"
	AgentWorkflowRunStatusAborted   AgentWorkflowRunStatus = "aborted"
)

func (status AgentWorkflowRunStatus) Validate() error {
	switch status {
	case AgentWorkflowRunStatusAdmitting,
		AgentWorkflowRunStatusRunning,
		AgentWorkflowRunStatusSucceeded,
		AgentWorkflowRunStatusFailed,
		AgentWorkflowRunStatusErrored,
		AgentWorkflowRunStatusCanceling,
		AgentWorkflowRunStatusAborted:
		return nil
	default:
		return fmt.Errorf("db: invalid agent workflow-run status %q", status)
	}
}

type AgentWorkflowRunExecutionStatus string

const (
	AgentWorkflowRunExecutionStatusSucceeded AgentWorkflowRunExecutionStatus = "succeeded"
	AgentWorkflowRunExecutionStatusFailed    AgentWorkflowRunExecutionStatus = "failed"
	AgentWorkflowRunExecutionStatusErrored   AgentWorkflowRunExecutionStatus = "errored"
	AgentWorkflowRunExecutionStatusAborted   AgentWorkflowRunExecutionStatus = "aborted"
)

func (status AgentWorkflowRunExecutionStatus) Validate() error {
	switch status {
	case AgentWorkflowRunExecutionStatusSucceeded,
		AgentWorkflowRunExecutionStatusFailed,
		AgentWorkflowRunExecutionStatusErrored,
		AgentWorkflowRunExecutionStatusAborted:
		return nil
	default:
		return fmt.Errorf("db: invalid agent workflow-run execution status %q", status)
	}
}

func validateAgentWorkflowRunTransition(from, to AgentWorkflowRunStatus) error {
	legal := false
	switch from {
	case AgentWorkflowRunStatusAdmitting:
		legal = to == AgentWorkflowRunStatusRunning ||
			to == AgentWorkflowRunStatusErrored ||
			to == AgentWorkflowRunStatusCanceling
	case AgentWorkflowRunStatusRunning:
		legal = to == AgentWorkflowRunStatusSucceeded ||
			to == AgentWorkflowRunStatusFailed ||
			to == AgentWorkflowRunStatusErrored ||
			to == AgentWorkflowRunStatusAborted ||
			to == AgentWorkflowRunStatusCanceling
	case AgentWorkflowRunStatusCanceling:
		legal = to == AgentWorkflowRunStatusSucceeded ||
			to == AgentWorkflowRunStatusFailed ||
			to == AgentWorkflowRunStatusAborted ||
			to == AgentWorkflowRunStatusErrored
	}
	if !legal {
		return fmt.Errorf("db: invalid agent workflow-run transition %q -> %q", from, to)
	}
	return nil
}

type AgentWorkflowRunSnapshotDirection string

const (
	AgentWorkflowRunSnapshotInput  AgentWorkflowRunSnapshotDirection = "input"
	AgentWorkflowRunSnapshotOutput AgentWorkflowRunSnapshotDirection = "output"
)

func (direction AgentWorkflowRunSnapshotDirection) Validate() error {
	if direction != AgentWorkflowRunSnapshotInput && direction != AgentWorkflowRunSnapshotOutput {
		return fmt.Errorf("db: invalid agent workflow-run snapshot direction %q", direction)
	}
	return nil
}

type AgentWorkflowRun struct {
	ID                      snapshot.WorkflowRunID
	TeamID                  int
	TeamName                string
	WorkflowDefinitionID    int
	WorkflowName            string
	WorkflowVersion         int
	SchemaVersion           int
	SignatureVersion        int
	DefinitionContentHash   string
	FunctionID              *string
	IdempotencyKey          string
	ParameterizedConfig     json.RawMessage
	ParameterizedConfigHash string
	ConcreteConfig          json.RawMessage
	ConcreteConfigHash      *string
	ActualPlan              json.RawMessage
	ActualPlanHash          *string
	ResolvedDependencies    json.RawMessage
	OriginKind              string
	OriginReference         string
	CreatedBy               string
	Status                  AgentWorkflowRunStatus
	ExecutionStatus         *AgentWorkflowRunExecutionStatus
	ErrorMessage            string
	RetryOfWorkflowRunID    *snapshot.WorkflowRunID
	PipelineRunID           *int
	TemplatePipelineID      *int
	InstancePipelineID      *int
	PlannedBuildID          *int64
	ReconcileAfter          time.Time
	CreatedAt               time.Time
	UpdatedAt               time.Time
	StartedAt               *time.Time
	CompletedAt             *time.Time
}

type AgentWorkflowRunCreateRequest struct {
	TeamID                  int
	TeamName                string
	WorkflowDefinitionID    int
	WorkflowName            string
	WorkflowVersion         int
	SchemaVersion           int
	SignatureVersion        int
	DefinitionContentHash   string
	FunctionID              *string
	IdempotencyKey          string
	ParameterizedConfig     json.RawMessage
	ParameterizedConfigHash string
	OriginKind              string
	OriginReference         string
	CreatedBy               string
	Status                  AgentWorkflowRunStatus
	RetryOfWorkflowRunID    *snapshot.WorkflowRunID
	Inputs                  map[string]snapshot.SnapshotRef
	ExperimentAdmission     *AgentWorkflowRunExperimentAdmission
}

type AgentWorkflowRunExperimentAdmission struct {
	ExperimentID int64
	CellID       int64
	Phase        string
}

func (admission AgentWorkflowRunExperimentAdmission) Validate(
	teamID int,
	originKind string,
	originReference string,
) error {
	if admission.ExperimentID <= 0 || admission.CellID <= 0 || teamID <= 0 ||
		originKind != "experiment" {
		return fmt.Errorf("db: invalid experiment workflow-run admission gate")
	}
	expectedReference := fmt.Sprintf(
		"experiment:%d:cell:%d", admission.ExperimentID, admission.CellID,
	)
	switch admission.Phase {
	case "candidate":
	case "evaluator":
		expectedReference += ":evaluator"
	default:
		return fmt.Errorf("db: invalid experiment workflow-run admission phase")
	}
	if originReference != expectedReference {
		return fmt.Errorf("db: experiment workflow-run admission origin does not match its gate")
	}
	return nil
}

func (request AgentWorkflowRunCreateRequest) Validate() error {
	if request.TeamID <= 0 || request.WorkflowDefinitionID <= 0 || request.WorkflowVersion <= 0 ||
		request.SchemaVersion <= 0 || request.SignatureVersion <= 0 {
		return fmt.Errorf("db: workflow-run identity and versions must be positive")
	}
	for label, value := range map[string]string{
		"team name": request.TeamName, "workflow name": request.WorkflowName,
		"definition content hash":   request.DefinitionContentHash,
		"idempotency key":           request.IdempotencyKey,
		"parameterized config hash": request.ParameterizedConfigHash,
		"origin kind":               request.OriginKind, "creator": request.CreatedBy,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("db: workflow-run %s is required", label)
		}
	}
	if request.FunctionID != nil && strings.TrimSpace(*request.FunctionID) == "" {
		return fmt.Errorf("db: workflow-run function ID must be nonblank when present")
	}
	if len(request.ParameterizedConfig) == 0 || !json.Valid(request.ParameterizedConfig) {
		return fmt.Errorf("db: workflow-run parameterized config must be valid JSON")
	}
	if request.Status != AgentWorkflowRunStatusAdmitting {
		return fmt.Errorf("db: workflow-run initial status must be admitting")
	}
	if request.RetryOfWorkflowRunID != nil {
		if err := request.RetryOfWorkflowRunID.Validate(); err != nil {
			return err
		}
	}
	if request.ExperimentAdmission != nil {
		if err := request.ExperimentAdmission.Validate(
			request.TeamID, request.OriginKind, request.OriginReference,
		); err != nil {
			return err
		}
	}
	for port, ref := range request.Inputs {
		if strings.TrimSpace(port) == "" {
			return fmt.Errorf("db: workflow-run input port is required")
		}
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("db: workflow-run input %q: %w", port, err)
		}
	}
	return nil
}

type AgentWorkflowRunExecutionLink struct {
	PipelineRunID      int
	TemplatePipelineID int
	InstancePipelineID int
	ConcreteConfig     json.RawMessage
	ConcreteConfigHash string
}

func (link AgentWorkflowRunExecutionLink) Validate() error {
	if link.PipelineRunID <= 0 || link.TemplatePipelineID <= 0 || link.InstancePipelineID <= 0 {
		return fmt.Errorf("db: workflow-run execution IDs must be positive")
	}
	if len(link.ConcreteConfig) == 0 || !json.Valid(link.ConcreteConfig) || strings.TrimSpace(link.ConcreteConfigHash) == "" {
		return fmt.Errorf("db: workflow-run concrete config and hash are required")
	}
	return nil
}

type AgentWorkflowRunPlan struct {
	BuildID              int64
	ActualPlan           json.RawMessage
	ActualPlanHash       string
	ResolvedDependencies json.RawMessage
}

func (plan AgentWorkflowRunPlan) Validate() error {
	if plan.BuildID <= 0 || len(plan.ActualPlan) == 0 || !json.Valid(plan.ActualPlan) || strings.TrimSpace(plan.ActualPlanHash) == "" {
		return fmt.Errorf("db: workflow-run build, actual plan, and hash are required")
	}
	if len(plan.ResolvedDependencies) == 0 || !json.Valid(plan.ResolvedDependencies) {
		return fmt.Errorf("db: workflow-run resolved dependencies must be valid JSON")
	}
	return nil
}

type AgentWorkflowRunReconciliationView struct {
	Run                 AgentWorkflowRun
	SelectedBuildExists bool
	SelectedBuildStatus BuildStatus
}

type AgentWorkflowRunExpectedProducer struct {
	PlanID          string
	StepKind        string
	StepName        string
	LocalOutputPort string
}

func (producer AgentWorkflowRunExpectedProducer) Validate() error {
	if strings.TrimSpace(producer.PlanID) == "" ||
		(producer.StepKind != "task" && producer.StepKind != "agent" && producer.StepKind != "await_snapshot") ||
		strings.TrimSpace(producer.StepName) == "" ||
		strings.TrimSpace(producer.LocalOutputPort) == "" {
		return fmt.Errorf("db: workflow-run expected producer requires a plan ID, supported step identity, and local output")
	}
	return nil
}

type AgentWorkflowRunExpectedOutput struct {
	Port                 string
	Type                 snapshot.TypeRef
	Optional             bool
	WorkflowDefinitionID int
	WorkflowRunID        snapshot.WorkflowRunID
	Producers            []AgentWorkflowRunExpectedProducer
}

func (output AgentWorkflowRunExpectedOutput) Validate() error {
	if strings.TrimSpace(output.Port) == "" || output.WorkflowDefinitionID <= 0 {
		return fmt.Errorf("db: workflow-run expected output port and definition are required")
	}
	if err := output.Type.Validate(); err != nil {
		return err
	}
	if err := output.WorkflowRunID.Validate(); err != nil {
		return err
	}
	if len(output.Producers) == 0 {
		return fmt.Errorf("db: workflow-run expected output %q requires at least one producer", output.Port)
	}
	seenPlanIDs := make(map[string]struct{}, len(output.Producers))
	for _, producer := range output.Producers {
		if err := producer.Validate(); err != nil {
			return fmt.Errorf("db: workflow-run expected output %q: %w", output.Port, err)
		}
		if _, found := seenPlanIDs[producer.PlanID]; found {
			return fmt.Errorf("db: workflow-run expected output %q has duplicate producer plan ID %q", output.Port, producer.PlanID)
		}
		seenPlanIDs[producer.PlanID] = struct{}{}
	}
	return nil
}

type AgentWorkflowRunFinalization struct {
	WorkflowRunID           snapshot.WorkflowRunID
	ExpectedStatus          AgentWorkflowRunStatus
	ExpectedExecutionStatus *AgentWorkflowRunExecutionStatus
	ExpectedActualPlanHash  *string
	TerminalStatus          AgentWorkflowRunStatus
	ErrorMessage            string
	ExpectedOutputs         []AgentWorkflowRunExpectedOutput
}

func (finalization AgentWorkflowRunFinalization) Validate() error {
	if err := finalization.WorkflowRunID.Validate(); err != nil {
		return err
	}
	if err := finalization.ExpectedStatus.Validate(); err != nil {
		return err
	}
	if finalization.ExpectedStatus != AgentWorkflowRunStatusAdmitting &&
		finalization.ExpectedStatus != AgentWorkflowRunStatusRunning &&
		finalization.ExpectedStatus != AgentWorkflowRunStatusCanceling {
		return fmt.Errorf("db: workflow-run finalization expected status must be active")
	}
	if finalization.ExpectedExecutionStatus != nil {
		if err := finalization.ExpectedExecutionStatus.Validate(); err != nil {
			return err
		}
	}
	if finalization.ExpectedActualPlanHash != nil && strings.TrimSpace(*finalization.ExpectedActualPlanHash) == "" {
		return fmt.Errorf("db: workflow-run expected actual-plan hash must be nonblank")
	}
	if finalization.TerminalStatus != AgentWorkflowRunStatusSucceeded &&
		finalization.TerminalStatus != AgentWorkflowRunStatusFailed &&
		finalization.TerminalStatus != AgentWorkflowRunStatusErrored &&
		finalization.TerminalStatus != AgentWorkflowRunStatusAborted {
		return fmt.Errorf("db: workflow-run finalization status must be terminal")
	}
	if len(finalization.ErrorMessage) > 64*1024 {
		return fmt.Errorf("db: workflow-run error message exceeds 64 KiB")
	}
	if finalization.TerminalStatus == AgentWorkflowRunStatusSucceeded {
		if finalization.ExpectedExecutionStatus == nil ||
			*finalization.ExpectedExecutionStatus != AgentWorkflowRunExecutionStatusSucceeded ||
			finalization.ExpectedActualPlanHash == nil {
			return fmt.Errorf("db: successful workflow-run finalization requires succeeded execution and an actual-plan hash")
		}
	} else if len(finalization.ExpectedOutputs) != 0 {
		return fmt.Errorf("db: workflow-run output evidence is only valid for successful execution")
	}
	seenPorts := make(map[string]struct{}, len(finalization.ExpectedOutputs))
	for _, output := range finalization.ExpectedOutputs {
		if err := output.Validate(); err != nil {
			return err
		}
		if _, found := seenPorts[output.Port]; found {
			return fmt.Errorf("db: duplicate workflow-run expected output %q", output.Port)
		}
		seenPorts[output.Port] = struct{}{}
	}
	return nil
}

type AgentWorkflowRunFinalizationResult struct {
	Status       AgentWorkflowRunStatus
	ErrorMessage string
	CompletedAt  time.Time
}

type AgentWorkflowRunBuildDisposition string

const (
	AgentWorkflowRunBuildDispositionOrdinary  AgentWorkflowRunBuildDisposition = "ordinary"
	AgentWorkflowRunBuildDispositionSelected  AgentWorkflowRunBuildDisposition = "selected"
	AgentWorkflowRunBuildDispositionAnomalous AgentWorkflowRunBuildDisposition = "anomalous"
)

type AgentWorkflowRunBuildCaptureResult struct {
	WorkflowRunID        snapshot.WorkflowRunID
	WorkflowDefinitionID int
	ConcreteConfig       json.RawMessage
	Disposition          AgentWorkflowRunBuildDisposition
}

// AgentWorkflowRunBuildAssociation is the minimal durable identity copied
// into execution metadata for a selected workflow-run build. It is sourced
// only from the server-owned selected-build link and captured plan
// provenance; pipeline-authored step declarations cannot create it.
type AgentWorkflowRunBuildAssociation struct {
	WorkflowRunID        snapshot.WorkflowRunID
	WorkflowDefinitionID int
}

func (association AgentWorkflowRunBuildAssociation) Validate() error {
	if err := association.WorkflowRunID.Validate(); err != nil {
		return err
	}
	if association.WorkflowDefinitionID <= 0 {
		return fmt.Errorf("db: workflow-run build association requires a positive workflow definition ID")
	}
	return nil
}

type AgentWorkflowRunCancelingError struct {
	WorkflowRunID snapshot.WorkflowRunID
}

func (err *AgentWorkflowRunCancelingError) Error() string {
	return fmt.Sprintf("db: workflow run %s is canceling before its selected build starts", err.WorkflowRunID.String())
}

type agentWorkflowRunAnomalyKind string

const (
	agentWorkflowRunAnomalyLaterBuildStarted   agentWorkflowRunAnomalyKind = "later_build_started"
	agentWorkflowRunAnomalyLaterBuildCompleted agentWorkflowRunAnomalyKind = "later_build_completed"
)

type AgentWorkflowRunSnapshotBinding struct {
	WorkflowRunID snapshot.WorkflowRunID
	Direction     AgentWorkflowRunSnapshotDirection
	PortName      string
	Snapshot      snapshot.SnapshotRef
}

type AgentWorkflowRunListFilter struct {
	TeamID          int
	WorkflowName    string
	Status          AgentWorkflowRunStatus
	OriginKind      string
	OriginReference string
	Before          *pagination.Cursor
	Limit           int
}

// AgentWorkflowRunCountFilter selects one team's immutable run population
// for an exact status aggregation. ExcludeOriginKind is used by the
// operational dashboard to keep experiment cells out of operational counts.
type AgentWorkflowRunCountFilter struct {
	TeamID            int
	WorkflowName      string
	ExcludeOriginKind string
}

const agentWorkflowRunColumns = `
	id, team_id, team_name, workflow_definition_id, workflow_name,
	workflow_version, schema_version, signature_version, definition_content_hash,
	function_id, idempotency_key, parameterized_config, parameterized_config_hash,
	concrete_config, concrete_config_hash, actual_plan, actual_plan_nonce,
	actual_plan_hash, actual_plan_hash_nonce, resolved_dependencies, resolved_dependencies_nonce,
	origin_kind, origin_reference, created_by, status,
	execution_status, error_message, retry_of_workflow_run_id, pipeline_run_id,
	template_pipeline_id, instance_pipeline_id, planned_build_id, reconcile_after,
	created_at, updated_at, started_at, completed_at`

func scanAgentWorkflowRun(row scannable, encryptionStrategy encryption.Strategy) (AgentWorkflowRun, error) {
	var (
		run                       AgentWorkflowRun
		id                        int64
		functionID                sql.NullString
		parameterizedConfig       []byte
		concreteConfig            []byte
		concreteConfigHash        sql.NullString
		actualPlan                sql.NullString
		actualPlanNonce           sql.NullString
		actualPlanHash            sql.NullString
		actualPlanHashNonce       sql.NullString
		resolvedDependencies      sql.NullString
		resolvedDependenciesNonce sql.NullString
		status                    string
		executionStatus           sql.NullString
		retryID                   sql.NullInt64
		pipelineRunID             sql.NullInt64
		templatePipelineID        sql.NullInt64
		instancePipelineID        sql.NullInt64
		plannedBuildID            sql.NullInt64
		startedAt                 sql.NullTime
		completedAt               sql.NullTime
	)
	err := row.Scan(
		&id, &run.TeamID, &run.TeamName, &run.WorkflowDefinitionID, &run.WorkflowName,
		&run.WorkflowVersion, &run.SchemaVersion, &run.SignatureVersion, &run.DefinitionContentHash,
		&functionID, &run.IdempotencyKey, &parameterizedConfig, &run.ParameterizedConfigHash,
		&concreteConfig, &concreteConfigHash, &actualPlan, &actualPlanNonce,
		&actualPlanHash, &actualPlanHashNonce, &resolvedDependencies, &resolvedDependenciesNonce,
		&run.OriginKind, &run.OriginReference, &run.CreatedBy, &status,
		&executionStatus, &run.ErrorMessage, &retryID, &pipelineRunID, &templatePipelineID,
		&instancePipelineID, &plannedBuildID, &run.ReconcileAfter, &run.CreatedAt, &run.UpdatedAt,
		&startedAt, &completedAt,
	)
	if err != nil {
		return AgentWorkflowRun{}, err
	}
	run.ID = snapshot.WorkflowRunID(id)
	run.Status = AgentWorkflowRunStatus(status)
	if executionStatus.Valid {
		value := AgentWorkflowRunExecutionStatus(executionStatus.String)
		run.ExecutionStatus = &value
	}
	run.ParameterizedConfig = cloneJSON(parameterizedConfig)
	run.ConcreteConfig = cloneJSON(concreteConfig)
	if actualPlan.Valid {
		var nonce *string
		if actualPlanNonce.Valid {
			nonce = &actualPlanNonce.String
		}
		plaintext, err := encryptionStrategy.Decrypt(actualPlan.String, nonce)
		if err != nil {
			return AgentWorkflowRun{}, fmt.Errorf("db: decrypt workflow-run actual plan: %w", err)
		}
		if !json.Valid(plaintext) {
			return AgentWorkflowRun{}, fmt.Errorf("db: invalid persisted workflow-run actual plan")
		}
		run.ActualPlan = cloneJSON(plaintext)
	} else if actualPlanNonce.Valid {
		return AgentWorkflowRun{}, fmt.Errorf("db: workflow-run actual-plan nonce has no ciphertext")
	}
	if functionID.Valid {
		run.FunctionID = &functionID.String
	}
	if concreteConfigHash.Valid {
		run.ConcreteConfigHash = &concreteConfigHash.String
	}
	if actualPlanHash.Valid {
		var nonce *string
		if actualPlanHashNonce.Valid {
			nonce = &actualPlanHashNonce.String
		}
		plaintext, err := encryptionStrategy.Decrypt(actualPlanHash.String, nonce)
		if err != nil {
			return AgentWorkflowRun{}, fmt.Errorf("db: decrypt workflow-run actual plan hash: %w", err)
		}
		value := string(plaintext)
		if strings.TrimSpace(value) == "" {
			return AgentWorkflowRun{}, fmt.Errorf("db: invalid persisted workflow-run actual plan hash")
		}
		run.ActualPlanHash = &value
	} else if actualPlanHashNonce.Valid {
		return AgentWorkflowRun{}, fmt.Errorf("db: workflow-run actual-plan-hash nonce has no ciphertext")
	}
	if resolvedDependencies.Valid {
		var nonce *string
		if resolvedDependenciesNonce.Valid {
			nonce = &resolvedDependenciesNonce.String
		}
		plaintext, err := encryptionStrategy.Decrypt(resolvedDependencies.String, nonce)
		if err != nil {
			return AgentWorkflowRun{}, fmt.Errorf("db: decrypt workflow-run resolved dependencies: %w", err)
		}
		if !json.Valid(plaintext) {
			return AgentWorkflowRun{}, fmt.Errorf("db: invalid persisted workflow-run resolved dependencies")
		}
		run.ResolvedDependencies = cloneJSON(plaintext)
	} else if resolvedDependenciesNonce.Valid {
		return AgentWorkflowRun{}, fmt.Errorf("db: workflow-run resolved-dependencies nonce has no ciphertext")
	}
	if retryID.Valid {
		value := snapshot.WorkflowRunID(retryID.Int64)
		run.RetryOfWorkflowRunID = &value
	}
	run.PipelineRunID = nullIntToInt(pipelineRunID)
	run.TemplatePipelineID = nullIntToInt(templatePipelineID)
	run.InstancePipelineID = nullIntToInt(instancePipelineID)
	if plannedBuildID.Valid {
		value := plannedBuildID.Int64
		run.PlannedBuildID = &value
	}
	if startedAt.Valid {
		run.StartedAt = &startedAt.Time
	}
	if completedAt.Valid {
		run.CompletedAt = &completedAt.Time
	}
	if err := run.ID.Validate(); err != nil {
		return AgentWorkflowRun{}, fmt.Errorf("db: invalid persisted workflow run: %w", err)
	}
	if err := run.Status.Validate(); err != nil {
		return AgentWorkflowRun{}, fmt.Errorf("db: invalid persisted workflow run: %w", err)
	}
	if run.ExecutionStatus != nil {
		if err := run.ExecutionStatus.Validate(); err != nil {
			return AgentWorkflowRun{}, fmt.Errorf("db: invalid persisted workflow run: %w", err)
		}
	}
	if run.ReconcileAfter.IsZero() {
		return AgentWorkflowRun{}, fmt.Errorf("db: invalid persisted workflow run: reconcile time is required")
	}
	return run, nil
}

func nullIntToInt(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	converted := int(value.Int64)
	if int64(converted) != value.Int64 {
		return nil
	}
	return &converted
}
