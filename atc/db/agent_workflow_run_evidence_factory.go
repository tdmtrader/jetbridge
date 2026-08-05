package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/concourse/concourse/atc/event"
)

// AgentWorkflowRunEvidence is every authoritative execution record the durable
// node-occurrence projection derives from, gathered for one run.
//
// It is deliberately a narrow read model rather than the full rows: the
// derivation lives in agent/workflowrun/occurrence, which already depends on
// this package, so the evidence types must stay here or the dependency
// inverts. Each field names the table it comes from, and nothing else about
// that table travels with it.
type AgentWorkflowRunEvidence struct {
	// AttemptMetrics is agent_run_metrics for the run's planned build, the
	// durable record of every agent step. It is named for the per-attempt
	// table it used to read; that table is only written when checkpoints are
	// enabled, which no deployment has ever done, so reading it reported every
	// agent node as having cost nothing and the freeze made that permanent.
	AttemptMetrics []AgentNodeAttemptMetric
	// Waits is agent_workflow_waits for the run.
	Waits []AgentNodeWait
	// Publications is agent_publication_occurrences for the run, restricted to
	// the rows that carry a plan identity — a publication with no plan_id
	// predates the projection and cannot be joined to a node.
	Publications []AgentNodePublication
	// BuildStepStatus maps plan ID to terminal build step status for the run's
	// planned build. It is the ONLY durable evidence a deterministic task step
	// ever has: agent steps have run metrics, awaits have waits, publishes
	// have publication occurrences, and tasks have nothing but build events,
	// which Concourse GC reclaims. Values are 'succeeded', 'failed', or
	// 'errored'.
	BuildStepStatus map[string]string
}

// AgentNodeAttemptMetric is the projection of agent_run_metrics the
// node-occurrence derivation reads.
//
// agent_run_metrics is unique on (build_id, plan_id) and carries no attempt
// dimension, so ExecutionAttempt is pinned to 1. The projection's key still
// varies on retry_attempt, so nothing collides.
//
// CreatedAt and UpdatedAt are derived, not columns. agent_run_metrics has a
// single created_at, defaulted to now() on a row written after the agent
// process exits — it stamps COMPLETION. UpdatedAt is that stamp; CreatedAt is
// it minus wall_time_seconds. Reading created_at as the start would place
// every agent node one full run duration into the future. When wall time was
// never recorded the span collapses to a point, which reads as "unknown
// duration" rather than as a confidently wrong number.
type AgentNodeAttemptMetric struct {
	PlanID           string
	ExecutionAttempt int
	Status           string
	CostUSD          float64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// AgentNodeWait is the projection of agent_workflow_waits the node-occurrence
// derivation reads. TimeoutPolicy travels with it because status alone cannot
// tell a wait that expired without an answer from one that expired onto its
// declared default: both are written 'timed_out'.
type AgentNodeWait struct {
	ID            int64
	PlanID        string
	OutputName    string
	Status        string
	TimeoutPolicy string
	CreatedAt     time.Time
	ResolvedAt    *time.Time
}

// AgentNodePublication is the projection of agent_publication_occurrences the
// node-occurrence derivation reads.
type AgentNodePublication struct {
	ID        int64
	PlanID    string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Terminal build step statuses. They are the occurrence vocabulary rather than
// the event vocabulary, because a build event is evidence and these are the
// conclusion drawn from it.
const (
	AgentNodeBuildStepSucceeded = "succeeded"
	AgentNodeBuildStepFailed    = "failed"
	AgentNodeBuildStepErrored   = "errored"
)

type AgentWorkflowRunEvidenceFactory interface {
	EvidenceForRun(context.Context, AgentWorkflowRun) (AgentWorkflowRunEvidence, error)
}

type agentWorkflowRunEvidenceFactory struct {
	conn DbConn
}

func NewAgentWorkflowRunEvidenceFactory(conn DbConn) AgentWorkflowRunEvidenceFactory {
	return &agentWorkflowRunEvidenceFactory{conn: conn}
}

func (factory *agentWorkflowRunEvidenceFactory) EvidenceForRun(
	ctx context.Context,
	run AgentWorkflowRun,
) (AgentWorkflowRunEvidence, error) {
	evidence := AgentWorkflowRunEvidence{BuildStepStatus: map[string]string{}}

	waits, err := factory.waitsForRun(ctx, int64(run.ID))
	if err != nil {
		return AgentWorkflowRunEvidence{}, err
	}
	evidence.Waits = waits

	publications, err := factory.publicationsForRun(ctx, int64(run.ID))
	if err != nil {
		return AgentWorkflowRunEvidence{}, err
	}
	evidence.Publications = publications

	// A run that never planned a build reached no step at all, so there is no
	// build-scoped evidence to look for. That is a legitimate outcome — a run
	// that errors out of admission is exactly this — not a fault.
	if run.PlannedBuildID == nil {
		return evidence, nil
	}

	metrics, err := factory.attemptMetricsForBuild(ctx, *run.PlannedBuildID)
	if err != nil {
		return AgentWorkflowRunEvidence{}, err
	}
	evidence.AttemptMetrics = metrics

	stepStatus, err := factory.buildStepStatus(ctx, *run.PlannedBuildID)
	if err != nil {
		return AgentWorkflowRunEvidence{}, err
	}
	evidence.BuildStepStatus = stepStatus

	return evidence, nil
}

func (factory *agentWorkflowRunEvidenceFactory) attemptMetricsForBuild(
	ctx context.Context,
	buildID int64,
) ([]AgentNodeAttemptMetric, error) {
	rows, err := factory.conn.QueryContext(ctx, `
		SELECT plan_id,
		       1,
		       status,
		       cost_usd::float8,
		       created_at - (wall_time_seconds * INTERVAL '1 second'),
		       created_at
		FROM agent_run_metrics
		WHERE build_id = $1
		ORDER BY plan_id`, buildID)
	if err != nil {
		return nil, fmt.Errorf("db: reading node attempt metrics: %w", err)
	}
	defer Close(rows)

	var result []AgentNodeAttemptMetric
	for rows.Next() {
		var metric AgentNodeAttemptMetric
		if err := rows.Scan(
			&metric.PlanID, &metric.ExecutionAttempt, &metric.Status,
			&metric.CostUSD, &metric.CreatedAt, &metric.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("db: scanning node attempt metric: %w", err)
		}
		result = append(result, metric)
	}
	return result, rows.Err()
}

func (factory *agentWorkflowRunEvidenceFactory) waitsForRun(
	ctx context.Context,
	workflowRunID int64,
) ([]AgentNodeWait, error) {
	rows, err := factory.conn.QueryContext(ctx, `
		SELECT id, plan_id, output_name, status, timeout_policy, created_at, resolved_at
		FROM agent_workflow_waits
		WHERE workflow_run_id = $1
		ORDER BY id`, workflowRunID)
	if err != nil {
		return nil, fmt.Errorf("db: reading node waits: %w", err)
	}
	defer Close(rows)

	var result []AgentNodeWait
	for rows.Next() {
		var wait AgentNodeWait
		if err := rows.Scan(
			&wait.ID, &wait.PlanID, &wait.OutputName, &wait.Status,
			&wait.TimeoutPolicy, &wait.CreatedAt, &wait.ResolvedAt,
		); err != nil {
			return nil, fmt.Errorf("db: scanning node wait: %w", err)
		}
		result = append(result, wait)
	}
	return result, rows.Err()
}

func (factory *agentWorkflowRunEvidenceFactory) publicationsForRun(
	ctx context.Context,
	workflowRunID int64,
) ([]AgentNodePublication, error) {
	rows, err := factory.conn.QueryContext(ctx, `
		SELECT id, plan_id, status, created_at, updated_at
		FROM agent_publication_occurrences
		WHERE workflow_run_id = $1 AND plan_id IS NOT NULL
		ORDER BY id`, workflowRunID)
	if err != nil {
		return nil, fmt.Errorf("db: reading node publications: %w", err)
	}
	defer Close(rows)

	var result []AgentNodePublication
	for rows.Next() {
		var publication AgentNodePublication
		if err := rows.Scan(
			&publication.ID, &publication.PlanID, &publication.Status,
			&publication.CreatedAt, &publication.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("db: scanning node publication: %w", err)
		}
		result = append(result, publication)
	}
	return result, rows.Err()
}

// buildStepStatus reads terminal per-step status out of the build's own
// partition of build_events, keyed by plan ID.
//
// This is the reason the whole projection exists. A deterministic task step
// records nothing else durable: no attempt metric, no wait, no publication.
// Its only trace is the finish-task / finish / error event the engine wrote,
// and build events are reclaimed by log retention and by the pipeline drop
// that follows template retirement. Reading them at freeze time, while the
// run has only just terminalized, is the single moment that evidence exists
// and the outcome is settled.
//
// Events with no origin ID are build-level (the status event) and belong to no
// step. Later events win, so a step that errored after reporting a result is
// recorded as errored rather than by whichever row the scan happened to see
// first.
func (factory *agentWorkflowRunEvidenceFactory) buildStepStatus(
	ctx context.Context,
	buildID int64,
) (map[string]string, error) {
	table, found, err := factory.buildEventsTable(ctx, buildID)
	if err != nil {
		return nil, err
	}
	status := map[string]string{}
	// A build the run points at but that no longer exists has had its events
	// reclaimed with it. That is missing evidence, not a fault: Derive renders
	// a node with no evidence as pending rather than inventing a status.
	if !found {
		return status, nil
	}

	rows, err := factory.conn.QueryContext(ctx, `
		SELECT type, payload
		FROM `+table+`
		WHERE build_id = $1 AND type IN ($2, $3, $4)
		ORDER BY event_id`,
		buildID, string(event.EventTypeFinishTask), string(event.EventTypeFinish),
		string(event.EventTypeError))
	if err != nil {
		return nil, fmt.Errorf("db: reading build step events: %w", err)
	}
	defer Close(rows)

	for rows.Next() {
		var eventType string
		var payload []byte
		if err := rows.Scan(&eventType, &payload); err != nil {
			return nil, fmt.Errorf("db: scanning build step event: %w", err)
		}
		planID, stepStatus, ok := buildStepEventStatus(eventType, payload)
		if !ok {
			continue
		}
		status[planID] = stepStatus
	}
	return status, rows.Err()
}

// buildStepEvent is the shared shape of the three terminal step events. Each
// carries the exact fields this reader needs and nothing more, so a schema
// addition elsewhere in the event cannot change what is decoded here.
type buildStepEvent struct {
	Origin struct {
		ID string `json:"id"`
	} `json:"origin"`
	ExitStatus int  `json:"exit_status"`
	Succeeded  bool `json:"succeeded"`
}

func buildStepEventStatus(eventType string, payload []byte) (string, string, bool) {
	var decoded buildStepEvent
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return "", "", false
	}
	if decoded.Origin.ID == "" {
		return "", "", false
	}
	switch eventType {
	case string(event.EventTypeFinishTask):
		if decoded.ExitStatus == 0 {
			return decoded.Origin.ID, AgentNodeBuildStepSucceeded, true
		}
		return decoded.Origin.ID, AgentNodeBuildStepFailed, true
	case string(event.EventTypeFinish):
		if decoded.Succeeded {
			return decoded.Origin.ID, AgentNodeBuildStepSucceeded, true
		}
		return decoded.Origin.ID, AgentNodeBuildStepFailed, true
	case string(event.EventTypeError):
		return decoded.Origin.ID, AgentNodeBuildStepErrored, true
	default:
		return "", "", false
	}
}

// buildEventsTable resolves the partition holding one build's events, by the
// same rule build.eventsTable() uses to write them. Reading the inherited
// parent instead would scan every pipeline's partition on a table that is
// among the largest in the database.
func (factory *agentWorkflowRunEvidenceFactory) buildEventsTable(
	ctx context.Context,
	buildID int64,
) (string, bool, error) {
	var pipelineID, teamID sql.NullInt64
	var resourceID, resourceTypeID sql.NullInt64
	err := factory.conn.QueryRowContext(ctx, `
		SELECT pipeline_id, team_id, resource_id, resource_type_id
		FROM builds
		WHERE id = $1`, buildID).Scan(&pipelineID, &teamID, &resourceID, &resourceTypeID)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("db: locating build events partition: %w", err)
	}
	if resourceID.Valid || resourceTypeID.Valid {
		return "check_build_events", true, nil
	}
	if pipelineID.Valid && pipelineID.Int64 != 0 {
		return fmt.Sprintf("pipeline_build_events_%d", pipelineID.Int64), true, nil
	}
	if !teamID.Valid || teamID.Int64 == 0 {
		return "", false, fmt.Errorf("db: build %d has neither a pipeline nor a team", buildID)
	}
	return fmt.Sprintf("team_build_events_%d", teamID.Int64), true, nil
}
