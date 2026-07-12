package db

import (
	"database/sql"
	"encoding/json"
	"errors"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/agent/api/metrics"
	// aliased: atc/db already declares a package-level `schema` const (build.go).
	agentschema "github.com/concourse/concourse/agent/schema"
)

// AgentRunMetricsFactory persists agent run metrics (shared-contracts
// §1.8/§2.4). It is exactly agent/api/metrics.Store (Upsert,
// UpsertReturningInserted, InsertIfAbsent, GetByBuild, ListByTicket) —
// embedded now that both packages live on the same branch.
//
//counterfeiter:generate . AgentRunMetricsFactory
type AgentRunMetricsFactory interface {
	metrics.Store
}

func NewAgentRunMetricsFactory(conn DbConn) AgentRunMetricsFactory {
	return &agentRunMetricsFactory{conn: conn}
}

type agentRunMetricsFactory struct {
	conn DbConn
}

func (f *agentRunMetricsFactory) Upsert(rm *agentschema.RunMetrics) error {
	_, _, err := f.UpsertReturningInserted(rm)
	return err
}

// UpsertReturningInserted performs the ON CONFLICT (build_id, plan_id) upsert,
// reports whether the row was newly inserted, and — when a row already existed
// — returns that previous row's ledger-relevant counters (cost/usage/turns) so
// the caller can append the spend DELTA to the append-only agent_cost_ledger
// (§1.4 has no ledger dedup key; a pure first-insert gate loses the step's
// entire spend when the first ingestion was a severed exec's zero-cost partial
// and the resume's full ingestion arrives as an update). The previous row is
// read with FOR UPDATE in the same transaction as the upsert, serializing
// concurrent ingestions of the same key: the loser blocks until the winner
// commits and then observes the winner's committed counters, so the same
// dollar can never be charged twice. The inserted discriminator is Postgres's
// system column `xmax`: on a fresh INSERT the tuple has no prior version so
// `xmax = 0`; when the ON CONFLICT DO UPDATE fires, the update replaces an
// existing tuple and `xmax <> 0`. (When the pre-read sees no row but the
// insert still loses a concurrent race, inserted=false with prev=nil is
// returned — indeterminate, and callers skip their ledger append.)
func (f *agentRunMetricsFactory) UpsertReturningInserted(rm *agentschema.RunMetrics) (bool, *agentschema.RunMetrics, error) {
	var eventCounts, results any
	if rm.EventCounts != nil {
		b, err := json.Marshal(rm.EventCounts)
		if err != nil {
			return false, nil, err
		}
		eventCounts = b
	}
	if len(rm.Results) > 0 {
		results = []byte(rm.Results)
	}

	tx, err := f.conn.Begin()
	if err != nil {
		return false, nil, err
	}
	defer Rollback(tx)

	var prev *agentschema.RunMetrics
	var p agentschema.RunMetrics
	err = psql.Select("cost_usd", "input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens", "turns").
		From("agent_run_metrics").
		Where(sq.Eq{"build_id": rm.BuildID, "plan_id": rm.PlanID}).
		Suffix("FOR UPDATE").
		RunWith(tx).
		QueryRow().
		Scan(&p.CostUSD, &p.Usage.InputTokens, &p.Usage.OutputTokens, &p.Usage.CacheReadInputTokens, &p.Usage.CacheCreationInputTokens, &p.Turns)
	switch {
	case err == nil:
		prev = &p
	case errors.Is(err, sql.ErrNoRows):
		// no prior row — fresh insert below
	default:
		return false, nil, err
	}

	var inserted bool
	err = psql.Insert("agent_run_metrics").
		Columns(
			"ticket_id", "pipeline_run_id", "build_id", "plan_id", "step_name",
			"workflow_name", "workflow_version", "workflow_hash",
			"status", "summary", "model",
			"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens",
			"turns", "wall_time_seconds", "cost_usd",
			"results", "events_artifact", "event_counts",
		).
		Values(
			rm.TicketID, rm.PipelineRunID, rm.BuildID, rm.PlanID, rm.StepName,
			rm.WorkflowName, rm.WorkflowVersion, rm.WorkflowHash,
			rm.Status, rm.Summary, rm.Model,
			rm.Usage.InputTokens, rm.Usage.OutputTokens, rm.Usage.CacheReadInputTokens, rm.Usage.CacheCreationInputTokens,
			rm.Turns, rm.WallTimeSeconds, rm.CostUSD,
			results, rm.EventsArtifact, eventCounts,
		).
		Suffix(`ON CONFLICT (build_id, plan_id) DO UPDATE SET
			ticket_id = EXCLUDED.ticket_id,
			pipeline_run_id = EXCLUDED.pipeline_run_id,
			step_name = EXCLUDED.step_name,
			workflow_name = EXCLUDED.workflow_name,
			workflow_version = EXCLUDED.workflow_version,
			workflow_hash = EXCLUDED.workflow_hash,
			status = EXCLUDED.status,
			summary = EXCLUDED.summary,
			model = EXCLUDED.model,
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			cache_creation_tokens = EXCLUDED.cache_creation_tokens,
			turns = EXCLUDED.turns,
			wall_time_seconds = EXCLUDED.wall_time_seconds,
			cost_usd = EXCLUDED.cost_usd,
			results = EXCLUDED.results,
			events_artifact = EXCLUDED.events_artifact,
			event_counts = EXCLUDED.event_counts
		RETURNING (xmax = 0) AS inserted`).
		RunWith(tx).
		QueryRow().
		Scan(&inserted)
	if err != nil {
		return false, nil, err
	}

	if err := tx.Commit(); err != nil {
		return false, nil, err
	}
	return inserted, prev, nil
}

// InsertIfAbsent is the degraded-ingestion write (finding F24): identical
// column/value construction to UpsertReturningInserted, but the conflict
// clause is DO NOTHING, so an existing (build_id, plan_id) row — a real row
// from an earlier, successful ingestion — is never overwritten. With
// DO NOTHING, RETURNING yields no row when the conflict fires, which scans
// as sql.ErrNoRows ⇒ inserted=false, nothing written.
func (f *agentRunMetricsFactory) InsertIfAbsent(rm *agentschema.RunMetrics) (bool, error) {
	var eventCounts, results any
	if rm.EventCounts != nil {
		b, err := json.Marshal(rm.EventCounts)
		if err != nil {
			return false, err
		}
		eventCounts = b
	}
	if len(rm.Results) > 0 {
		results = []byte(rm.Results)
	}

	var inserted bool
	err := psql.Insert("agent_run_metrics").
		Columns(
			"ticket_id", "pipeline_run_id", "build_id", "plan_id", "step_name",
			"workflow_name", "workflow_version", "workflow_hash",
			"status", "summary", "model",
			"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens",
			"turns", "wall_time_seconds", "cost_usd",
			"results", "events_artifact", "event_counts",
		).
		Values(
			rm.TicketID, rm.PipelineRunID, rm.BuildID, rm.PlanID, rm.StepName,
			rm.WorkflowName, rm.WorkflowVersion, rm.WorkflowHash,
			rm.Status, rm.Summary, rm.Model,
			rm.Usage.InputTokens, rm.Usage.OutputTokens, rm.Usage.CacheReadInputTokens, rm.Usage.CacheCreationInputTokens,
			rm.Turns, rm.WallTimeSeconds, rm.CostUSD,
			results, rm.EventsArtifact, eventCounts,
		).
		Suffix(`ON CONFLICT (build_id, plan_id) DO NOTHING RETURNING true`).
		RunWith(f.conn).
		QueryRow().
		Scan(&inserted)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // conflict fired — existing row preserved
	}
	return inserted, err
}

const runMetricsColumns = `m.ticket_id, m.pipeline_run_id, m.build_id, m.plan_id, m.step_name,
	m.workflow_name, m.workflow_version, m.workflow_hash,
	m.status, m.summary, m.model,
	m.input_tokens, m.output_tokens, m.cache_read_tokens, m.cache_creation_tokens,
	m.turns, m.wall_time_seconds, m.cost_usd,
	m.results, m.events_artifact, m.event_counts,
	EXTRACT(EPOCH FROM m.created_at)::bigint`

func (f *agentRunMetricsFactory) GetByBuild(buildID int) ([]agentschema.RunMetrics, error) {
	rows, err := f.conn.Query(
		`SELECT `+runMetricsColumns+` FROM agent_run_metrics m
		 WHERE m.build_id = $1 ORDER BY m.created_at ASC, m.id ASC`, buildID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRunMetricsRows(rows)
}

func (f *agentRunMetricsFactory) ListByTicket(ticketID int) ([]agentschema.RunMetrics, error) {
	rows, err := f.conn.Query(
		`SELECT `+runMetricsColumns+` FROM agent_run_metrics m
		 WHERE m.ticket_id = $1 ORDER BY m.created_at ASC, m.id ASC`, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRunMetricsRows(rows)
}

func scanRunMetricsRows(rows *sql.Rows) ([]agentschema.RunMetrics, error) {
	results := []agentschema.RunMetrics{}
	for rows.Next() {
		var rm agentschema.RunMetrics
		var resultsPayload, eventCounts []byte
		err := rows.Scan(
			&rm.TicketID, &rm.PipelineRunID, &rm.BuildID, &rm.PlanID, &rm.StepName,
			&rm.WorkflowName, &rm.WorkflowVersion, &rm.WorkflowHash,
			&rm.Status, &rm.Summary, &rm.Model,
			&rm.Usage.InputTokens, &rm.Usage.OutputTokens, &rm.Usage.CacheReadInputTokens, &rm.Usage.CacheCreationInputTokens,
			&rm.Turns, &rm.WallTimeSeconds, &rm.CostUSD,
			&resultsPayload, &rm.EventsArtifact, &eventCounts,
			&rm.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		if len(resultsPayload) > 0 {
			rm.Results = json.RawMessage(resultsPayload)
		}
		if len(eventCounts) > 0 {
			if err := json.Unmarshal(eventCounts, &rm.EventCounts); err != nil {
				return nil, err
			}
		}
		results = append(results, rm)
	}
	return results, rows.Err()
}
