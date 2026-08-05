package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// AgentWorkflowRunNodeOccurrence is one frozen row of the node-occurrence
// projection: one attempt of one semantic node within one workflow run.
//
// RetryAttempt and Attempt are independent axes and both belong to the key.
// RetryAttempt is which copy of an authored retry closure the row describes,
// read from the plan's structure; Attempt is which recovery attempt of that
// copy it is. Pinned to 1: agent_run_metrics carries no attempt dimension.
type AgentWorkflowRunNodeOccurrence struct {
	WorkflowRunID        int64
	NodeID               string
	RetryAttempt         int
	Attempt              int
	TeamID               int
	WorkflowName         string
	WorkflowDefinitionID int
	WorkflowVersion      int
	NodeKind             string
	ReusableNodeName     string
	ReusableNodeVersion  *int
	PlanID               string
	Status               string
	WaitID               *int64
	PublicationID        *int64
	StartedAt            *time.Time
	CompletedAt          *time.Time
	DurationSeconds      int
	CostUSD              float64
	FrozenAt             time.Time
}

type AgentWorkflowRunNodeOccurrencesFactory interface {
	// Freeze writes the projection for one terminal run. It is idempotent so a
	// retried finalization cannot double-write, and it never overwrites an
	// existing row, because frozen history is immutable.
	Freeze(context.Context, []AgentWorkflowRunNodeOccurrence) error
	ForRun(context.Context, int64) ([]AgentWorkflowRunNodeOccurrence, error)
}

type agentWorkflowRunNodeOccurrencesFactory struct {
	conn DbConn
}

func NewAgentWorkflowRunNodeOccurrencesFactory(conn DbConn) AgentWorkflowRunNodeOccurrencesFactory {
	return &agentWorkflowRunNodeOccurrencesFactory{conn: conn}
}

const agentWorkflowRunNodeOccurrenceColumns = `
	workflow_run_id, node_id, retry_attempt, attempt, team_id, workflow_name,
	workflow_definition_id, workflow_version, node_kind, reusable_node_name,
	reusable_node_version, plan_id, status, wait_id, publication_id,
	started_at, completed_at, duration_seconds, cost_usd::float8, frozen_at`

func (factory *agentWorkflowRunNodeOccurrencesFactory) Freeze(ctx context.Context, occurrences []AgentWorkflowRunNodeOccurrence) error {
	if len(occurrences) == 0 {
		return nil
	}

	// ON CONFLICT DO NOTHING below exists so a RETRIED finalization cannot
	// double-write. It cannot tell that apart from two rows of ONE freeze
	// colliding, which is a derivation bug, and swallowing those would discard
	// real history with no error and no log line. Reject the batch instead:
	// the projection is written once and must be written whole.
	seen := make(map[[4]any]struct{}, len(occurrences))
	for _, occurrence := range occurrences {
		key := [4]any{
			occurrence.WorkflowRunID, occurrence.NodeID,
			occurrence.RetryAttempt, occurrence.Attempt,
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf(
				"db: node-occurrence freeze for run %d has two occurrences keyed (%s, retry %d, attempt %d)",
				occurrence.WorkflowRunID, occurrence.NodeID,
				occurrence.RetryAttempt, occurrence.Attempt,
			)
		}
		seen[key] = struct{}{}
	}

	tx, err := factory.conn.Begin()
	if err != nil {
		return fmt.Errorf("db: beginning node-occurrence freeze: %w", err)
	}
	// One transaction for the whole projection: a half-written freeze is
	// indistinguishable from a run that never reached the remaining nodes.
	defer Rollback(tx)

	for _, occurrence := range occurrences {
		var reusableVersion sql.NullInt64
		if occurrence.ReusableNodeVersion != nil {
			reusableVersion = sql.NullInt64{Int64: int64(*occurrence.ReusableNodeVersion), Valid: true}
		}

		_, err := tx.ExecContext(ctx, `
			INSERT INTO agent_workflow_run_node_occurrences (
				workflow_run_id, node_id, retry_attempt, attempt, team_id,
				workflow_name, workflow_definition_id, workflow_version,
				node_kind, reusable_node_name, reusable_node_version, plan_id,
				status, wait_id, publication_id, started_at, completed_at,
				duration_seconds, cost_usd
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
			ON CONFLICT (workflow_run_id, node_id, retry_attempt, attempt) DO NOTHING`,
			occurrence.WorkflowRunID, occurrence.NodeID, occurrence.RetryAttempt,
			occurrence.Attempt, occurrence.TeamID, occurrence.WorkflowName,
			occurrence.WorkflowDefinitionID, occurrence.WorkflowVersion,
			occurrence.NodeKind, occurrence.ReusableNodeName, reusableVersion,
			occurrence.PlanID, occurrence.Status, occurrence.WaitID,
			occurrence.PublicationID, occurrence.StartedAt, occurrence.CompletedAt,
			occurrence.DurationSeconds, occurrence.CostUSD,
		)
		if err != nil {
			return fmt.Errorf("db: freezing node occurrence %s/%s: %w", occurrence.NodeID, occurrence.PlanID, err)
		}
	}

	return tx.Commit()
}

func (factory *agentWorkflowRunNodeOccurrencesFactory) ForRun(ctx context.Context, workflowRunID int64) ([]AgentWorkflowRunNodeOccurrence, error) {
	rows, err := factory.conn.QueryContext(ctx, `
		SELECT `+agentWorkflowRunNodeOccurrenceColumns+`
		FROM agent_workflow_run_node_occurrences
		WHERE workflow_run_id = $1
		-- occurrence.ResolveEffective keeps the LAST terminal entry per node in
		-- the order it is handed them, so the rows of one node must arrive in
		-- execution order. plan_id led that ordering before, and plan IDs sort
		-- as text — copy "10" ahead of copy "2" — which handed the resolution a
		-- superseded attempt as the latest one. The attempt axes carry the real
		-- order.
		ORDER BY node_id, retry_attempt, attempt, plan_id`, workflowRunID)
	if err != nil {
		return nil, fmt.Errorf("db: reading node occurrences: %w", err)
	}
	defer Close(rows)

	var result []AgentWorkflowRunNodeOccurrence
	for rows.Next() {
		var occurrence AgentWorkflowRunNodeOccurrence
		var reusableVersion sql.NullInt64
		if err := rows.Scan(
			&occurrence.WorkflowRunID, &occurrence.NodeID, &occurrence.RetryAttempt,
			&occurrence.Attempt, &occurrence.TeamID, &occurrence.WorkflowName,
			&occurrence.WorkflowDefinitionID, &occurrence.WorkflowVersion,
			&occurrence.NodeKind, &occurrence.ReusableNodeName, &reusableVersion,
			&occurrence.PlanID, &occurrence.Status, &occurrence.WaitID,
			&occurrence.PublicationID, &occurrence.StartedAt, &occurrence.CompletedAt,
			&occurrence.DurationSeconds, &occurrence.CostUSD, &occurrence.FrozenAt,
		); err != nil {
			return nil, fmt.Errorf("db: scanning node occurrence: %w", err)
		}
		if reusableVersion.Valid {
			value := int(reusableVersion.Int64)
			occurrence.ReusableNodeVersion = &value
		}
		result = append(result, occurrence)
	}
	return result, rows.Err()
}
