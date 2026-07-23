package reviews

import (
	"encoding/json"
	"fmt"

	"github.com/concourse/concourse/agent/snapshot"
)

// ReviewPayload is the subset of ci-agent's ReviewOutput that ATC
// needs for denormalized storage. The full raw payload is stored as-is.
type ReviewPayload struct {
	SchemaVersion string `json:"schema_version"`
	Metadata      struct {
		Repo        string `json:"repo"`
		Commit      string `json:"commit"`
		Branch      string `json:"branch"`
		AgentModel  string `json:"agent_model"`
		DurationSec int    `json:"duration_seconds"`
	} `json:"metadata"`
	Score struct {
		Value float64 `json:"value"`
		Max   float64 `json:"max"`
		Pass  bool    `json:"pass"`
	} `json:"score"`
	ProvenIssues []json.RawMessage `json:"proven_issues"`
	Observations []json.RawMessage `json:"observations"`
	Summary      string            `json:"summary"`
}

// Submission is a parsed POST /api/v1/agent/reviews body.
type Submission struct {
	BuildID int             `json:"build_id"`
	Review  json.RawMessage `json:"review"`
	Payload ReviewPayload   `json:"-"`
}

func ParseSubmission(body []byte) (*Submission, error) {
	var sub Submission
	if err := json.Unmarshal(body, &sub); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if sub.BuildID <= 0 {
		return nil, fmt.Errorf("build_id is required")
	}
	if len(sub.Review) == 0 {
		return nil, fmt.Errorf("review is required")
	}
	if err := json.Unmarshal(sub.Review, &sub.Payload); err != nil {
		return nil, fmt.Errorf("invalid review payload: %w", err)
	}
	if sub.Payload.Metadata.Repo == "" {
		return nil, fmt.Errorf("review.metadata.repo is required")
	}
	if sub.Payload.Metadata.Commit == "" {
		return nil, fmt.Errorf("review.metadata.commit is required")
	}
	return &sub, nil
}

// BuildContext is what ATC derives server-side from the build row —
// never trusted from the client.
type BuildContext struct {
	BuildName    string
	TeamName     string
	PipelineName string
	JobName      string
}

// StoredReview is the persisted form of a review.
type StoredReview struct {
	BuildID          int             `json:"build_id,omitempty"`
	BuildName        string          `json:"build_name"`
	TeamName         string          `json:"team_name"`
	PipelineName     string          `json:"pipeline_name"`
	JobName          string          `json:"job_name"`
	Repo             string          `json:"repo"`
	CommitSha        string          `json:"commit_sha"`
	Branch           string          `json:"branch"`
	Score            float64         `json:"score"`
	MaxScore         float64         `json:"max_score"`
	Pass             bool            `json:"pass"`
	ProvenCount      int             `json:"proven_count"`
	ObservationCount int             `json:"observation_count"`
	Summary          string          `json:"summary"`
	AgentModel       string          `json:"agent_model"`
	DurationSeconds  int             `json:"duration_seconds"`
	Review           json.RawMessage `json:"review,omitempty"`
	// TicketID / PipelineRunID link harvest-published evidence to a ticket
	// and a pipeline run (shared-contracts §1.10). nil = plain CI review.
	TicketID      *int `json:"ticket_id,omitempty"`
	PipelineRunID *int `json:"pipeline_run_id,omitempty"`
	// SnapshotID is the canonical review identity for projected review/v1
	// values. WorkflowRunID and ProductionID are denormalized provenance links;
	// nil on legacy build submissions.
	SnapshotID    *snapshot.SnapshotID    `json:"snapshot_id,omitempty"`
	WorkflowRunID *snapshot.WorkflowRunID `json:"workflow_run_id,omitempty"`
	ProductionID  *snapshot.DatabaseID    `json:"production_id,omitempty"`
	CreatedAt     int64                   `json:"created_at"`
	// EvaluatedCount is filled by the DB store's feedback join, not by
	// ToStoredReview or MemoryStore.Upsert.
	EvaluatedCount int `json:"evaluated_count"`
	// SubmittedBy is the writing principal's name (audit-attribution
	// convention): the verified agent principal, or 'legacy-publish'
	// for static-token writes during the dual-accept window. Filled by
	// the handler, never by ToStoredReview.
	SubmittedBy string `json:"submitted_by"`
}

func (s *Submission) ToStoredReview(ctx BuildContext) *StoredReview {
	return &StoredReview{
		BuildID:          s.BuildID,
		BuildName:        ctx.BuildName,
		TeamName:         ctx.TeamName,
		PipelineName:     ctx.PipelineName,
		JobName:          ctx.JobName,
		Repo:             s.Payload.Metadata.Repo,
		CommitSha:        s.Payload.Metadata.Commit,
		Branch:           s.Payload.Metadata.Branch,
		Score:            s.Payload.Score.Value,
		MaxScore:         s.Payload.Score.Max,
		Pass:             s.Payload.Score.Pass,
		ProvenCount:      len(s.Payload.ProvenIssues),
		ObservationCount: len(s.Payload.Observations),
		Summary:          s.Payload.Summary,
		AgentModel:       s.Payload.Metadata.AgentModel,
		DurationSeconds:  s.Payload.Metadata.DurationSec,
		Review:           s.Review,
	}
}

// ListFilter narrows ListByTeam results.
type ListFilter struct {
	Pipeline string
	Repo     string
	Limit    int
}

// Store is the interface for review persistence.
type Store interface {
	// Upsert inserts the record, replacing any existing record with the
	// same (BuildID, Repo, CommitSha) key.
	Upsert(rec *StoredReview) error
	// GetByBuild returns records for the build ordered oldest-first
	// (created ascending).
	GetByBuild(buildID int) ([]StoredReview, error)
	// ListByTeam returns records for the team ordered newest-first
	// (created descending) — ListFilter.Limit therefore keeps the
	// newest N.
	ListByTeam(team string, filter ListFilter) ([]StoredReview, error)
	// ListByTicket returns records linked to the ticket ordered oldest-first
	// (created ascending).
	ListByTicket(ticketID int) ([]StoredReview, error)
}

// ProjectionReader exposes canonical snapshot/run identities without changing
// the compatibility Store required by legacy harvest/build callers.
type ProjectionReader interface {
	GetBySnapshot(teamName string, snapshotID snapshot.SnapshotID) (StoredReview, bool, error)
	ListByWorkflowRun(teamName, workflowName string, workflowRunID snapshot.WorkflowRunID) ([]StoredReview, error)
}
