package db

import (
	"database/sql"
	"encoding/json"
	"errors"

	// aliased: atc/db already declares a package-level `schema` const (build.go).
	agentschema "github.com/concourse/concourse/agent/schema"
)

// AgentRunMetricsFactory persists agent run metrics (shared-contracts
// §1.8/§2.4). It implements agent/api/metrics.Store — the method set below is
// kept structurally identical to that interface (declared explicitly rather
// than embedded until the metrics package lands, plan 07 Task 7).
//
//counterfeiter:generate . AgentRunMetricsFactory
type AgentRunMetricsFactory interface {
	// Upsert inserts the row, replacing any existing row with the same
	// (BuildID, PlanID) key. Ingestion is idempotent across step retries
	// and web-restart resumes.
	Upsert(rm *agentschema.RunMetrics) error
	// UpsertReturningInserted is Upsert with a first-insert discriminator:
	// inserted is true only when the row was newly INSERTed. Callers gate the
	// append-only agent_cost_ledger write on this flag (finding F3).
	UpsertReturningInserted(rm *agentschema.RunMetrics) (inserted bool, err error)
	// InsertIfAbsent writes the row only when no (BuildID, PlanID) row exists
	// yet (ON CONFLICT DO NOTHING) — the degraded-ingestion write (finding F24).
	InsertIfAbsent(rm *agentschema.RunMetrics) (inserted bool, err error)
	// GetByBuild returns rows for a build, oldest-first.
	GetByBuild(buildID int) ([]agentschema.RunMetrics, error)
	// ListByTicket returns rows for a ticket, oldest-first.
	ListByTicket(ticketID int) ([]agentschema.RunMetrics, error)
}

func NewAgentRunMetricsFactory(conn DbConn) AgentRunMetricsFactory {
	return &agentRunMetricsFactory{conn: conn}
}

type agentRunMetricsFactory struct {
	conn DbConn
}

func (f *agentRunMetricsFactory) Upsert(rm *agentschema.RunMetrics) error {
	_, err := f.UpsertReturningInserted(rm)
	return err
}

// UpsertReturningInserted performs the ON CONFLICT (build_id, plan_id) upsert and
// reports whether the row was newly inserted. The discriminator is Postgres's
// system column `xmax`: on a fresh INSERT the tuple has no prior version so
// `xmax = 0`; when the ON CONFLICT DO UPDATE fires, the update replaces an
// existing tuple and `xmax <> 0`. `RETURNING (xmax = 0) AS inserted` therefore
// distinguishes a first insert from a resume/retry update in the same statement,
// with no extra round-trip. Callers gate the append-only ledger write on this
// flag so a web-restart resume never double-charges (§1.4 has no ledger dedup key).
func (f *agentRunMetricsFactory) UpsertReturningInserted(rm *agentschema.RunMetrics) (bool, error) {
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
		RunWith(f.conn).
		QueryRow().
		Scan(&inserted)
	return inserted, err
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
