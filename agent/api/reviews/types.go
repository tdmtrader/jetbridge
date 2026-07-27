package reviews

import (
	"context"
	"encoding/json"

	"github.com/concourse/concourse/agent/snapshot"
)

// StoredReview is the projection of ONE sealed review/v1 snapshot.
//
// Two kinds of field live here and they are not interchangeable. Conclusion,
// Summary and SeverityCounts are the record's own judgment, projected from the
// sealed bytes at seal time. Everything above them is occurrence context read
// from the exact agent_snapshot_productions row a query selected — a review
// snapshot can be produced in more than one build or run, so those fields
// describe the occurrence being rendered, not the review.
type StoredReview struct {
	BuildID      int    `json:"build_id,omitempty"`
	BuildName    string `json:"build_name"`
	TeamName     string `json:"team_name"`
	PipelineName string `json:"pipeline_name"`
	JobName      string `json:"job_name"`
	// WorkflowName pairs with WorkflowRunID: the durable run route needs both,
	// and a run id alone cannot be turned into a link.
	WorkflowName string `json:"workflow_name"`

	// Conclusion is review/v1's verdict verbatim: accept, changes-required or
	// inconclusive. It is NOT projected onto a numeric scale — the contract
	// makes conclusion the judgment and a score would be a second, weaker
	// description of it.
	Conclusion string `json:"conclusion"`
	Summary    string `json:"summary"`
	// SeverityCounts counts findings by review/v1 severity (observation, low,
	// medium, high, critical). Severities with no findings are absent.
	SeverityCounts map[string]int `json:"severity_counts"`

	Review json.RawMessage `json:"review,omitempty"`

	// SnapshotID is the review's identity — every stored review has one.
	// WorkflowRunID and ProductionID name the selected occurrence and are
	// absent when the read resolved no production (a value shared to a team
	// that never produced it).
	SnapshotID    snapshot.SnapshotID     `json:"snapshot_id"`
	WorkflowRunID *snapshot.WorkflowRunID `json:"workflow_run_id,omitempty"`
	ProductionID  *snapshot.DatabaseID    `json:"production_id,omitempty"`
	CreatedAt     int64                   `json:"created_at"`
	// EvaluatedCount is filled by the store's feedback join.
	EvaluatedCount int `json:"evaluated_count"`
	// SubmittedBy is the verified writing principal's name.
	SubmittedBy string `json:"submitted_by"`
}

// FindingCount totals SeverityCounts. It is the count the projection claims,
// used when the stored record cannot be decoded at read time.
func (r StoredReview) FindingCount() int {
	total := 0
	for _, count := range r.SeverityCounts {
		total += count
	}
	return total
}

// ListFilter narrows ListByTeam results.
type ListFilter struct {
	Pipeline string
	Limit    int
}

// Store is the interface for review reads. There is exactly one writer —
// ProjectionWriter — because there is exactly one thing that produces a review:
// sealing a review/v1 snapshot.
type Store interface {
	// GetByBuild returns the reviews produced in the build, ordered
	// oldest-production-first.
	GetByBuild(buildID int) ([]StoredReview, error)
	// ListByTeam returns the team's reviews ordered newest-first
	// (production created descending) — ListFilter.Limit therefore keeps the
	// newest N.
	ListByTeam(team string, filter ListFilter) ([]StoredReview, error)
}

// ProjectionWriter is the single review writer, keyed by snapshot ID.
type ProjectionWriter interface {
	UpsertReviewProjection(context.Context, *StoredReview) error
}

// ProjectionReader exposes the canonical snapshot/run identities.
type ProjectionReader interface {
	GetBySnapshot(teamName string, snapshotID snapshot.SnapshotID) (StoredReview, bool, error)
	ListByWorkflowRun(teamName, workflowName string, workflowRunID snapshot.WorkflowRunID) ([]StoredReview, error)
}
