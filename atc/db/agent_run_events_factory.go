package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/concourse/concourse/agent/checkpoint"
)

// AgentRunEventsFactory owns the bounded, append-only operational history for
// one recovery head. It is intentionally separate from the checkpoint store so
// event ingestion cannot make a checkpoint transaction depend on telemetry.
type AgentRunEventsFactory interface {
	checkpoint.EventRecorder
	List(context.Context, checkpoint.Identity) ([]checkpoint.RunEvent, error)
}

func NewAgentRunEventsFactory(conn DbConn) AgentRunEventsFactory {
	return &agentRunEventsFactory{conn: conn}
}

type agentRunEventsFactory struct {
	conn DbConn
}

var _ checkpoint.EventRecorder = (*agentRunEventsFactory)(nil)

func (factory *agentRunEventsFactory) Record(ctx context.Context, event checkpoint.RunEvent) error {
	if err := event.Validate(); err != nil {
		return err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer Rollback(tx)
	head, err := getOrCreateCheckpointHead(ctx, tx, event.Identity)
	if err != nil {
		return err
	}
	details := []byte(`{}`)
	if len(event.Details) != 0 {
		details = event.Details
	}
	var generation any
	if event.CheckpointGeneration > 0 {
		generation = event.CheckpointGeneration
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO agent_run_events
			(head_id, execution_attempt, event_type, reason, checkpoint_generation, details)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb)
	`, head.id, event.ExecutionAttempt, string(event.Type), event.Reason, generation, string(details))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (factory *agentRunEventsFactory) List(ctx context.Context, identity checkpoint.Identity) ([]checkpoint.RunEvent, error) {
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	head, err := checkpointHead(ctx, factory.conn, identity)
	if errors.Is(err, sql.ErrNoRows) {
		return []checkpoint.RunEvent{}, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := factory.conn.QueryContext(ctx, `
		SELECT execution_attempt, event_type, reason, checkpoint_generation, details, created_at
		FROM agent_run_events
		WHERE head_id = $1
		ORDER BY created_at ASC, id ASC
	`, head.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []checkpoint.RunEvent{}
	for rows.Next() {
		var event checkpoint.RunEvent
		var generation sql.NullInt64
		var details []byte
		if err := rows.Scan(&event.ExecutionAttempt, &event.Type, &event.Reason, &generation, &details, &event.CreatedAt); err != nil {
			return nil, err
		}
		if generation.Valid {
			event.CheckpointGeneration = int(generation.Int64)
		}
		event.Identity = identity
		event.Details = json.RawMessage(append([]byte(nil), details...))
		if err := event.Validate(); err != nil {
			return nil, fmt.Errorf("checkpoint: persisted event is invalid: %w", err)
		}
		events = append(events, event)
	}
	return events, rows.Err()
}
