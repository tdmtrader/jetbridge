package workflowruns

import (
	"code.cloudfoundry.org/lager/v3"
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/agent/workflowrun/occurrence"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

// ErrCancelConflict is returned by the injected cancellation service when a
// durable run cannot legally enter (or remain in) cancellation. The service
// owns the restart-safe state transition and execution abort; HTTP owns only
// the stable status mapping.
var ErrCancelConflict = workflowrun.ErrCancelConflict

// ErrorResponse is the bounded public error envelope. Dependency errors are
// deliberately never serialized because they may carry private plans,
// storage locations, credentials, or prompts.
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// TrustedTeam is resolved server-side. The teamless workflow-run routes are
// authorized against and executed within the Concourse main team.
type TrustedTeam struct {
	ID   int
	Name string
}

// IdentityFunc extracts the verified human display identity from request
// authentication state. The request body cannot select a creator.
type IdentityFunc func(*http.Request) (string, error)

// Binder is the narrow admission boundary implemented by workflowrun.Binder.
type Binder interface {
	BindAndCreate(context.Context, workflowrun.AdmissionContext, workflowrun.BindRequest) (workflowrun.BindResult, error)
}

// RunStore is the team-filtered read surface needed by HTTP. The production
// db.AgentWorkflowRunsFactory satisfies this interface.
type RunStore interface {
	Get(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error)
	List(context.Context, db.AgentWorkflowRunListFilter) ([]db.AgentWorkflowRun, error)
	CountByStatus(context.Context, db.AgentWorkflowRunCountFilter) (map[db.AgentWorkflowRunStatus]int64, error)
	Snapshots(context.Context, snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error)
}

// Definitions is the narrow read of workflow.Store the run graph needs: the
// EXACT revision a run executed. There is deliberately no Live or Latest here —
// the run page must never be able to reach for the promoted revision.
type Definitions interface {
	Get(name string, version int) (*workflow.Definition, bool, error)
}

// Occurrences answers one run's node occurrences. occurrence.Reader is the
// production implementation: frozen history for terminal runs, live derivation
// for everything still executing. Sharing it with the overview is what keeps
// the two surfaces from disagreeing about what a node did.
type Occurrences interface {
	OccurrencesForRun(context.Context, db.AgentWorkflowRun) ([]occurrence.NodeOccurrence, error)
}

// Canceler owns the restart-safe durable cancellation transition and abort of
// the exact server-verified execution. The boolean is false for team-scoped
// absence. Returning a canceling run means accepted/in progress; returning an
// aborted run means an idempotent terminal replay.
type Canceler interface {
	Cancel(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error)
}

// ManifestStore deliberately exposes only the already-authorized public
// snapshot manifest operation. It avoids importing storage/content methods
// into the workflow-run handler.
type ManifestStore interface {
	GetAuthorized(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error)
}

// SnapshotBindingStore is the kind-neutral projection read once a caller has
// already resolved the owning run through a kind-scoped store.
type SnapshotBindingStore interface {
	Snapshots(context.Context, snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error)
}

// RunPresenter is the shared redacted run projection used by workflow and
// reusable-node HTTP surfaces. It deliberately accepts only bindings and
// authorized manifests; kind authorization happens before presentation.
type RunPresenter struct {
	team      TrustedTeam
	runs      SnapshotBindingStore
	manifests ManifestStore
}

func NewRunPresenter(team TrustedTeam, runs SnapshotBindingStore, manifests ManifestStore) (*RunPresenter, error) {
	if team.ID <= 0 || team.Name != atc.DefaultTeamName || interfaceIsNil(runs) || interfaceIsNil(manifests) {
		return nil, fmt.Errorf("workflow runs API: run presenter dependencies are required")
	}
	return &RunPresenter{team: team, runs: runs, manifests: manifests}, nil
}

func (presenter *RunPresenter) Detail(request *http.Request, definitionName string, run db.AgentWorkflowRun) (RunDetail, error) {
	return presentRunDetail(presenter.team, presenter.runs, presenter.manifests, request, definitionName, run)
}

func (presenter *RunPresenter) Summary(definitionName string, run db.AgentWorkflowRun) (RunSummary, error) {
	return presentRunSummary(presenter.team, definitionName, run)
}

type Config struct {
	// Logger receives handler-side failures, notably the cause behind an
	// internal_error 500. Optional: NewHandler substitutes a sink-less logger
	// when it is nil, so tests need not supply one.
	Logger    lager.Logger
	Team      TrustedTeam
	Identity  IdentityFunc
	Binder    Binder
	Runs      RunStore
	Canceler  Canceler
	Manifests ManifestStore
	// Definitions and Occurrences serve the exact per-run DAG.
	Definitions Definitions
	Occurrences Occurrences
	// Now supplies the history window's upper bound. Optional; time.Now by
	// default.
	Now func() time.Time
}

// Windows is the complete supported history-window vocabulary, shared by the
// run list and the workflow overview. Custom and adaptive windows are out of
// scope: one explicit global control keeps the graph, the selected node
// detail, and the run list on a single shared scope. It lives here, in the
// lower-level package, so the two surfaces cannot drift apart.
var Windows = map[string]time.Duration{
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

// DefaultWindow is what an unqualified overview request means. The run list
// applies no window unless one is asked for, because it paginates its own way
// back through history.
const DefaultWindow = "7d"

// CreateRequest is the complete caller-controlled workflow admission body.
// All authority, origin, creator, target-function, and durable-state fields
// are absent and therefore server-controlled.
type CreateRequest struct {
	Version        *int                           `json:"version,omitempty"`
	Inputs         map[string]snapshot.SnapshotID `json:"inputs,omitempty"`
	IdempotencyKey string                         `json:"idempotency_key"`
}

// RetryRequest supplies only a fresh idempotency key. The server reloads the
// immutable source target and input bindings.
type RetryRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
}

// RunSummary is the redacted durable identity and lifecycle projection. The
// private parameterized/concrete configs, actual plan, resolved dependencies,
// template/instance pipeline IDs, and raw error text have no fields here.
type RunSummary struct {
	WorkflowRunID           snapshot.WorkflowRunID              `json:"workflow_run_id"`
	PipelineRunID           *int                                `json:"pipeline_run_id"`
	WorkflowName            string                              `json:"workflow_name"`
	WorkflowVersion         int                                 `json:"workflow_version"`
	SchemaVersion           int                                 `json:"schema_version"`
	SignatureVersion        int                                 `json:"signature_version"`
	DefinitionContentHash   string                              `json:"definition_content_hash"`
	FunctionID              *string                             `json:"function_id"`
	Status                  db.AgentWorkflowRunStatus           `json:"status"`
	ExecutionStatus         *db.AgentWorkflowRunExecutionStatus `json:"execution_status"`
	OriginKind              string                              `json:"origin_kind"`
	OriginReference         string                              `json:"origin_reference"`
	CreatedBy               string                              `json:"created_by"`
	RetryOfWorkflowRunID    *snapshot.WorkflowRunID             `json:"retry_of_workflow_run_id"`
	TicketID                *int64                              `json:"ticket_id"`
	TicketReference         string                              `json:"ticket_reference"`
	CreatedAt               time.Time                           `json:"created_at"`
	UpdatedAt               time.Time                           `json:"updated_at"`
	StartedAt               *time.Time                          `json:"started_at"`
	CompletedAt             *time.Time                          `json:"completed_at"`
	ParameterizedConfigHash string                              `json:"parameterized_config_hash"`
	InstanceConfigHash      *string                             `json:"instance_config_hash"`
	ActualPlanHash          *string                             `json:"actual_plan_hash"`
	PlannedBuildID          *int64                              `json:"planned_build_id"`
}

type InputBinding struct {
	Port     string               `json:"port"`
	Snapshot snapshot.SnapshotRef `json:"snapshot"`
}

// OutputManifest reuses snapshot.Snapshot, the snapshot API's validated and
// redacted public manifest. Content availability is historical evidence and
// remains present even when content has expired.
type OutputManifest struct {
	Port     string            `json:"port"`
	Snapshot snapshot.Snapshot `json:"snapshot"`
}

type RunDetail struct {
	RunSummary
	Inputs  []InputBinding   `json:"inputs"`
	Outputs []OutputManifest `json:"outputs"`
}

type OutputsResponse struct {
	WorkflowRunID snapshot.WorkflowRunID `json:"workflow_run_id"`
	Outputs       []OutputManifest       `json:"outputs"`
}

type OperationalStatusCountsResponse struct {
	WorkflowName string           `json:"workflow_name"`
	Counts       map[string]int64 `json:"counts"`
}
