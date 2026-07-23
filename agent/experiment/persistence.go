package experiment

import (
	"context"
	"errors"
	"time"

	"github.com/concourse/concourse/agent/pagination"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
)

// TargetRenderer materializes the trusted runtime dependencies that are not
// authored in a workflow definition (most notably the digest-pinned agent
// runtime image). Start uses it to freeze one canonical config identity for
// every candidate variant and the evaluator.
type TargetRenderer interface {
	RenderFunction(workflow.FunctionTarget) (workflow.RenderedFunction, error)
}

var (
	ErrNotFound             = errors.New("experiment: not found")
	ErrRevisionConflict     = errors.New("experiment: revision conflict")
	ErrImmutable            = errors.New("experiment: started definition is immutable")
	ErrInvalidDefinition    = errors.New("experiment: invalid definition")
	ErrScorecardUnavailable = errors.New("experiment: scorecard unavailable")
)

type AuditAction string

const (
	AuditCreate AuditAction = "create"
	AuditUpdate AuditAction = "update"
	AuditStart  AuditAction = "start"
	AuditCancel AuditAction = "cancel"
)

type StoredExperiment struct {
	ID          ID         `json:"id"`
	TeamName    string     `json:"team_name"`
	Definition  Definition `json:"definition"`
	Revision    int64      `json:"revision"`
	CreatedBy   string     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type StoredCell struct {
	ID                       CellID                  `json:"id"`
	ExperimentID             ID                      `json:"experiment_id"`
	FixtureID                snapshot.DatabaseID     `json:"fixture_id"`
	FixtureLabel             string                  `json:"fixture_label"`
	FixtureRole              FixtureRole             `json:"fixture_role"`
	VariantID                snapshot.DatabaseID     `json:"variant_id"`
	VariantLabel             string                  `json:"variant_label"`
	Repetition               int                     `json:"repetition"`
	Status                   CellStatus              `json:"status"`
	CandidateWorkflowRunID   *snapshot.WorkflowRunID `json:"candidate_workflow_run_id,omitempty"`
	EvaluatorWorkflowRunID   *snapshot.WorkflowRunID `json:"evaluator_workflow_run_id,omitempty"`
	MeasurementSnapshotID    *snapshot.SnapshotID    `json:"measurement_snapshot_id,omitempty"`
	CandidateFailureCategory string                  `json:"candidate_failure_category,omitempty"`
	CreatedAt                time.Time               `json:"created_at"`
	UpdatedAt                time.Time               `json:"updated_at"`
	CompletedAt              *time.Time              `json:"completed_at,omitempty"`
}

// ListFilter is the bounded keyset request used by durable experiment-history
// APIs. Before is exclusive under the exact (created_at DESC, id DESC) order.
type ListFilter struct {
	Before *pagination.Cursor
	Limit  int
}

// PagedStore is kept separate from Store so older in-process adapters remain
// source compatible while the production store exposes complete history.
type PagedStore interface {
	ListPage(context.Context, int, ListFilter) ([]StoredExperiment, error)
}

// Store owns team-scoped draft mutations and frozen experiment reads.
type Store interface {
	Create(context.Context, int, string, string, Definition) (StoredExperiment, error)
	Update(context.Context, int, ID, int64, string, Definition) (StoredExperiment, error)
	Get(context.Context, int, ID) (StoredExperiment, bool, error)
	List(context.Context, int) ([]StoredExperiment, error)
	PreflightStart(context.Context, int, ID, int64) (StoredExperiment, error)
	Start(context.Context, int, ID, int64, string) (StoredExperiment, error)
	Cancel(context.Context, int, ID, int64, string) (StoredExperiment, error)
	ListCells(context.Context, int, ID) ([]StoredCell, error)
	GetCell(context.Context, int, ID, CellID) (StoredCell, bool, error)
	Scorecard(context.Context, int, ID) (Scorecard, error)
}
