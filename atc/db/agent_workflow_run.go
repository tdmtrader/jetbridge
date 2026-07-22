package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
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
			to == AgentWorkflowRunStatusCanceling
	case AgentWorkflowRunStatusCanceling:
		legal = to == AgentWorkflowRunStatusAborted ||
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
	ErrorMessage            string
	RetryOfWorkflowRunID    *snapshot.WorkflowRunID
	PipelineRunID           *int
	TemplatePipelineID      *int
	InstancePipelineID      *int
	PlannedBuildID          *int
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
	BuildID              int
	ActualPlan           json.RawMessage
	ActualPlanHash       string
	ResolvedDependencies json.RawMessage
}

func (plan AgentWorkflowRunPlan) Validate() error {
	if plan.BuildID <= 0 || len(plan.ActualPlan) == 0 || !json.Valid(plan.ActualPlan) || strings.TrimSpace(plan.ActualPlanHash) == "" {
		return fmt.Errorf("db: workflow-run build, actual plan, and hash are required")
	}
	if len(plan.ResolvedDependencies) != 0 && !json.Valid(plan.ResolvedDependencies) {
		return fmt.Errorf("db: workflow-run resolved dependencies must be valid JSON")
	}
	return nil
}

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
	Limit           int
}

const agentWorkflowRunColumns = `
	id, team_id, team_name, workflow_definition_id, workflow_name,
	workflow_version, schema_version, signature_version, definition_content_hash,
	function_id, idempotency_key, parameterized_config, parameterized_config_hash,
	concrete_config, concrete_config_hash, actual_plan, actual_plan_hash,
	resolved_dependencies, origin_kind, origin_reference, created_by, status,
	error_message, retry_of_workflow_run_id, pipeline_run_id, template_pipeline_id,
	instance_pipeline_id, planned_build_id, created_at, updated_at, started_at, completed_at`

func scanAgentWorkflowRun(row scannable) (AgentWorkflowRun, error) {
	var (
		run                  AgentWorkflowRun
		id                   int64
		functionID           sql.NullString
		parameterizedConfig  []byte
		concreteConfig       []byte
		concreteConfigHash   sql.NullString
		actualPlan           []byte
		actualPlanHash       sql.NullString
		resolvedDependencies []byte
		status               string
		retryID              sql.NullInt64
		pipelineRunID        sql.NullInt64
		templatePipelineID   sql.NullInt64
		instancePipelineID   sql.NullInt64
		plannedBuildID       sql.NullInt64
		startedAt            sql.NullTime
		completedAt          sql.NullTime
	)
	err := row.Scan(
		&id, &run.TeamID, &run.TeamName, &run.WorkflowDefinitionID, &run.WorkflowName,
		&run.WorkflowVersion, &run.SchemaVersion, &run.SignatureVersion, &run.DefinitionContentHash,
		&functionID, &run.IdempotencyKey, &parameterizedConfig, &run.ParameterizedConfigHash,
		&concreteConfig, &concreteConfigHash, &actualPlan, &actualPlanHash,
		&resolvedDependencies, &run.OriginKind, &run.OriginReference, &run.CreatedBy, &status,
		&run.ErrorMessage, &retryID, &pipelineRunID, &templatePipelineID,
		&instancePipelineID, &plannedBuildID, &run.CreatedAt, &run.UpdatedAt, &startedAt, &completedAt,
	)
	if err != nil {
		return AgentWorkflowRun{}, err
	}
	run.ID = snapshot.WorkflowRunID(id)
	run.Status = AgentWorkflowRunStatus(status)
	run.ParameterizedConfig = cloneJSON(parameterizedConfig)
	run.ConcreteConfig = cloneJSON(concreteConfig)
	run.ActualPlan = cloneJSON(actualPlan)
	run.ResolvedDependencies = cloneJSON(resolvedDependencies)
	if functionID.Valid {
		run.FunctionID = &functionID.String
	}
	if concreteConfigHash.Valid {
		run.ConcreteConfigHash = &concreteConfigHash.String
	}
	if actualPlanHash.Valid {
		run.ActualPlanHash = &actualPlanHash.String
	}
	if retryID.Valid {
		value := snapshot.WorkflowRunID(retryID.Int64)
		run.RetryOfWorkflowRunID = &value
	}
	run.PipelineRunID = nullIntToInt(pipelineRunID)
	run.TemplatePipelineID = nullIntToInt(templatePipelineID)
	run.InstancePipelineID = nullIntToInt(instancePipelineID)
	run.PlannedBuildID = nullIntToInt(plannedBuildID)
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
