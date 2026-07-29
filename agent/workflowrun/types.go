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
	ErrSnapshotUnavailable        = errors.New("workflow run: snapshot unavailable or unauthorized")
	ErrSnapshotTypeMismatch       = errors.New("workflow run: snapshot type mismatch")
	ErrBudgetDenied               = errors.New("workflow run: budget denied")
	ErrExperimentAdmissionClosed  = errors.New("workflow run: experiment admission closed")
	ErrIdempotencyConflict        = errors.New("workflow run: idempotency conflict")
	ErrImmutableTemplateCollision = errors.New("workflow run: immutable template collision or drift")
	ErrCorruptPartialAdmission    = errors.New("workflow run: corrupt partial admission")
	ErrPlatformFailure            = errors.New("workflow run: platform failure")
)

// OriginKindTicket is the Origin.Kind a ticket-adapter dispatch stamps on the
// workflow run it admits, with the ticket ID as the reference. It is the ONLY
// link from a run back to a ticket, so the reconciler's terminalizer and
// agent/dispatch both read it from here.
const OriginKindTicket = "ticket"

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
	WorkflowName                        string
	Version                             *int
	FunctionID                          string
	Inputs                              map[string]snapshot.SnapshotID
	IdempotencyKey                      string
	ExpectedWorkflowDefinitionID        int64
	ExpectedTargetConfigHash            string
	ExpectedDevValidationProvenanceHash *string
	ExperimentAdmission                 *ExperimentAdmissionGate
	RetryOf                             *snapshot.WorkflowRunID
}

type ExperimentAdmissionGate struct {
	ExperimentID int64
	CellID       int64
	Phase        string
}

type BindResult struct {
	Run     db.AgentWorkflowRun
	Created bool
}

type BudgetAdmission struct {
	WorkflowRunID snapshot.WorkflowRunID
	Config        atc.Config
	// ExperimentAdmission identifies a child whose combined candidate and
	// evaluator liability is already held by the durable cell reservation.
	ExperimentAdmission *ExperimentAdmissionGate

	// The source identity remains available to custom policy admitters. The
	// built-in daily-cap admitter deliberately computes its reservation from
	// Config, the exact durable executable that is about to run.
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
	// Authority remains in the durable compiled definition, not the ATC plan.
	// It is carried only while verifying the immutable template identity.
	DevValidationProfiles       []workflow.CompiledDevValidationProfile
	DevValidationProvenanceHash string
}

func cloneDevValidationProfiles(source []workflow.CompiledDevValidationProfile) []workflow.CompiledDevValidationProfile {
	if source == nil {
		return nil
	}
	cloned := make([]workflow.CompiledDevValidationProfile, len(source))
	for index, profile := range source {
		cloned[index] = profile
		cloned[index].BaseInputs = append([]workflow.DevValidationContract(nil), profile.BaseInputs...)
		cloned[index].Command = append([]string(nil), profile.Command...)
		cloned[index].Profile = append([]byte(nil), profile.Profile...)
		cloned[index].ProtectedConfig = append([]byte(nil), profile.ProtectedConfig...)
	}
	return cloned
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
		workflow.ExecutionEnvelope,
		string,
		BeforeWorkflowRunCommit,
	) (WorkflowRunExecution, bool, error)
}

// ModelCredentialAdmitter proves the web still has a model-credential source
// for the agent pods this run will start. It is a presence check, not a
// mounting step: pods read the platform secret directly (§8.2), so admission
// only has to fail closed before any execution side effect when no credential
// exists at all.
//
//counterfeiter:generate -o workflowrunfakes/fake_model_credential_admitter.go . ModelCredentialAdmitter
type ModelCredentialAdmitter interface {
	AdmitModelCredential(context.Context) error
}

type AllowAllBudgetAdmitter struct{}

func (AllowAllBudgetAdmitter) Admit(context.Context, BudgetAdmission) error { return nil }

func cloneRaw(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}
