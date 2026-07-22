package workflowrun

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

var (
	ErrInvalidRequest             = errors.New("workflow run: invalid request")
	ErrDefinitionOrTargetNotFound = errors.New("workflow run: definition or target not found")
	ErrLegacyDefinition           = errors.New("workflow run: legacy definition is not admissible")
	ErrSnapshotUnavailable        = errors.New("workflow run: snapshot unavailable or unauthorized")
	ErrSnapshotTypeMismatch       = errors.New("workflow run: snapshot type mismatch")
	ErrBudgetDenied               = errors.New("workflow run: budget denied")
	ErrIdempotencyConflict        = errors.New("workflow run: idempotency conflict")
	ErrImmutableTemplateCollision = errors.New("workflow run: immutable template collision or drift")
	ErrCorruptPartialAdmission    = errors.New("workflow run: corrupt partial admission")
	ErrPlatformFailure            = errors.New("workflow run: platform failure")
)

type Origin struct {
	Kind      string
	Reference string
}

type AdmissionContext struct {
	TeamID    int
	TeamName  string
	CreatedBy string
	Origin    Origin
}

type BindRequest struct {
	WorkflowName   string
	Version        *int
	FunctionID     string
	Inputs         map[string]snapshot.SnapshotID
	IdempotencyKey string
	RetryOf        *snapshot.WorkflowRunID
}

type BindResult struct {
	Run     db.AgentWorkflowRun
	Created bool
}

type BudgetAdmission struct {
	Admission  AdmissionContext
	Definition workflow.Definition
	Target     workflow.FunctionTarget
	Inputs     map[string]snapshot.SnapshotRef
}

type ImmutableTemplateSpec struct {
	Name          string
	FullHash      string
	CanonicalJSON []byte
	Config        atc.Config
}

type WorkflowRunTemplateRef = db.WorkflowRunTemplateRef
type BeforeWorkflowRunCommit = db.BeforeWorkflowRunCommit
type WorkflowRunExecution = db.WorkflowRunExecution

//counterfeiter:generate -o workflowrunfakes/fake_definition_resolver.go . DefinitionResolver
type DefinitionResolver interface {
	Live(context.Context, string) (workflow.Definition, bool, error)
	Get(context.Context, string, int) (workflow.Definition, bool, error)
}

//counterfeiter:generate -o workflowrunfakes/fake_target_renderer.go . TargetRenderer
type TargetRenderer interface {
	FullFunctionTarget(workflow.Definition) (workflow.FunctionTarget, error)
	ExtractFunctionTarget(workflow.Definition, string) (workflow.FunctionTarget, error)
	RenderFunction(workflow.FunctionTarget) (workflow.RenderedFunction, error)
}

//counterfeiter:generate -o workflowrunfakes/fake_snapshot_authorizer.go . SnapshotAuthorizer
type SnapshotAuthorizer interface {
	GetAuthorized(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error)
}

//counterfeiter:generate -o workflowrunfakes/fake_workflow_run_store.go . WorkflowRunStore
type WorkflowRunStore interface {
	FindByIdempotencyKey(context.Context, int, string) (db.AgentWorkflowRun, bool, error)
	CreateWithInputs(context.Context, db.AgentWorkflowRunCreateRequest) (db.AgentWorkflowRun, bool, error)
	Snapshots(context.Context, snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error)
	Transition(context.Context, snapshot.WorkflowRunID, db.AgentWorkflowRunStatus, db.AgentWorkflowRunStatus, string) (bool, error)
}

//counterfeiter:generate -o workflowrunfakes/fake_budget_admitter.go . BudgetAdmitter
type BudgetAdmitter interface {
	Admit(context.Context, BudgetAdmission) error
}

//counterfeiter:generate -o workflowrunfakes/fake_immutable_template_saver.go . ImmutableTemplateSaver
type ImmutableTemplateSaver interface {
	SaveOrReuse(context.Context, AdmissionContext, ImmutableTemplateSpec) (WorkflowRunTemplateRef, error)
}

//counterfeiter:generate -o workflowrunfakes/fake_pipeline_run_creator.go . PipelineRunCreator
type PipelineRunCreator interface {
	CreateRunForWorkflowRun(
		context.Context,
		snapshot.WorkflowRunID,
		WorkflowRunTemplateRef,
		map[string]any,
		string,
		BeforeWorkflowRunCommit,
	) (WorkflowRunExecution, bool, error)
}

//counterfeiter:generate -o workflowrunfakes/fake_run_secret_preparer.go . RunSecretPreparer
type RunSecretPreparer interface {
	Prepare(context.Context, AdmissionContext, db.AgentWorkflowRun) (PreparedRunSecret, error)
}

//counterfeiter:generate -o workflowrunfakes/fake_prepared_run_secret.go . PreparedRunSecret
type PreparedRunSecret interface {
	Attach(context.Context, int) error
}

type AllowAllBudgetAdmitter struct{}

func (AllowAllBudgetAdmitter) Admit(context.Context, BudgetAdmission) error { return nil }

func cloneRaw(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}
