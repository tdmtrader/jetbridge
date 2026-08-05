package db

import (
	"database/sql"
	"encoding/json"
	"errors"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/agent/api/metrics"
	// aliased: atc/db already declares a package-level `schema` const (build.go).
	agentschema "github.com/concourse/concourse/agent/schema"
	"github.com/concourse/concourse/agent/snapshot"
)

// AgentRunMetricsFactory persists agent run metrics (shared-contracts
// §1.8/§2.4). It is exactly agent/api/metrics.Store
// (UpsertReturningInserted, InsertIfAbsent, GetByBuild, ListByWorkflowRun) —
// embedded now that both packages live on the same branch.
type AgentRunMetricsFactory interface {
	metrics.Store
}

func NewAgentRunMetricsFactory(conn DbConn) AgentRunMetricsFactory {
	return &agentRunMetricsFactory{conn: conn}
}

type agentRunMetricsFactory struct {
	conn DbConn
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
	workflowRunID := workflowRunIDValue(rm)

	tx, err := f.conn.Begin()
	if err != nil {
		return false, nil, err
	}
	defer Rollback(tx)

	var prev *agentschema.RunMetrics
	var p agentschema.RunMetrics
	err = psql.Select("cost_usd", "input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens", "turns", "ingestion_seq").
		From("agent_run_metrics").
		Where(sq.Eq{"build_id": rm.BuildID, "plan_id": rm.PlanID}).
		Suffix("FOR UPDATE").
		RunWith(tx).
		QueryRow().
		Scan(&p.CostUSD, &p.Usage.InputTokens, &p.Usage.OutputTokens, &p.Usage.CacheReadInputTokens, &p.Usage.CacheCreationInputTokens, &p.Turns, &p.IngestionSeq)
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
			"workflow_run_id", "function_id", "build_id", "plan_id", "step_name",
			"status", "summary", "model",
			"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens",
			"turns", "wall_time_seconds", "cost_usd",
			"results", "events_artifact", "event_counts",
		).
		Values(
			workflowRunID, rm.FunctionID, rm.BuildID, rm.PlanID, rm.StepName,
			rm.Status, rm.Summary, rm.Model,
			rm.Usage.InputTokens, rm.Usage.OutputTokens, rm.Usage.CacheReadInputTokens, rm.Usage.CacheCreationInputTokens,
			rm.Turns, rm.WallTimeSeconds, rm.CostUSD,
			results, rm.EventsArtifact, eventCounts,
		).
		// The DO UPDATE is NON-REGRESSING (review finding F#1/F#4,
		// 2026-07-12). A web-restart resume re-ingests the same (build_id,
		// plan_id); if that resume reads the flight recorder only partially (a
		// transient daemon/exec sever between the results.json and events.ndjson
		// reads) the incoming row carries a LOWER cost, a status forced to
		// 'error' (no step.end), and blank results/counts. An unconditional
		// overwrite would (a) corrupt the scorecards/delivery-outcomes row
		// downward and (b) — because the caller derives the append-only ledger
		// delta from the previous row's counters (§1.4 has no ledger dedup key)
		// — let a subsequent full resume re-charge the whole cost, double-
		// counting into agent_cost_ledger, violating "re-resolution can only
		// shrink, never double-count". So: ledger-relevant counters are
		// monotonic (GREATEST), an incoming 'error' never downgrades a real end
		// status (and its summary rides with it), and never blank a column that
		// once held real data. Stable-by-construction identity columns are
		// copied verbatim.
		Suffix(`ON CONFLICT (build_id, plan_id) DO UPDATE SET
			workflow_run_id = EXCLUDED.workflow_run_id,
			function_id = EXCLUDED.function_id,
			step_name = EXCLUDED.step_name,
			status = CASE WHEN agent_run_metrics.status <> 'error' AND EXCLUDED.status = 'error'
			              THEN agent_run_metrics.status ELSE EXCLUDED.status END,
			summary = CASE WHEN agent_run_metrics.status <> 'error' AND EXCLUDED.status = 'error'
			               THEN agent_run_metrics.summary ELSE EXCLUDED.summary END,
			model = COALESCE(NULLIF(EXCLUDED.model, ''), agent_run_metrics.model),
			input_tokens = GREATEST(agent_run_metrics.input_tokens, EXCLUDED.input_tokens),
			output_tokens = GREATEST(agent_run_metrics.output_tokens, EXCLUDED.output_tokens),
			cache_read_tokens = GREATEST(agent_run_metrics.cache_read_tokens, EXCLUDED.cache_read_tokens),
			cache_creation_tokens = GREATEST(agent_run_metrics.cache_creation_tokens, EXCLUDED.cache_creation_tokens),
			turns = GREATEST(agent_run_metrics.turns, EXCLUDED.turns),
			wall_time_seconds = GREATEST(agent_run_metrics.wall_time_seconds, EXCLUDED.wall_time_seconds),
			cost_usd = GREATEST(agent_run_metrics.cost_usd, EXCLUDED.cost_usd),
			results = COALESCE(EXCLUDED.results, agent_run_metrics.results),
			events_artifact = COALESCE(NULLIF(EXCLUDED.events_artifact, ''), agent_run_metrics.events_artifact),
			event_counts = COALESCE(NULLIF(EXCLUDED.event_counts, '{}'::jsonb), agent_run_metrics.event_counts),
			ingestion_seq = agent_run_metrics.ingestion_seq + 1
		RETURNING (xmax = 0) AS inserted, ingestion_seq`).
		RunWith(tx).
		QueryRow().
		Scan(&inserted, &rm.IngestionSeq)
	if err != nil {
		return false, nil, err
	}

	if err := tx.Commit(); err != nil {
		return false, nil, err
	}
	return inserted, prev, nil
}

// InsertIfAbsent is the degraded-ingestion write (finding F24): identical
// column/value construction to UpsertReturningInserted, but on conflict it
// leaves every data column alone, so a real row from an earlier successful
// ingestion is never overwritten by a partial one. It is NOT DO NOTHING: the
// conflict path still advances ingestion_seq, because a degraded ingestion is
// an execution — an interrupted agent writes no flight output and lands
// exactly here, and the interruption cap has nothing to count if the
// collision silently absorbs it. Reports inserted=false on that path and
// writes the resulting sequence back to rm.IngestionSeq.
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
	workflowRunID := workflowRunIDValue(rm)

	var inserted bool
	err := psql.Insert("agent_run_metrics").
		Columns(
			"workflow_run_id", "function_id", "build_id", "plan_id", "step_name",
			"status", "summary", "model",
			"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens",
			"turns", "wall_time_seconds", "cost_usd",
			"results", "events_artifact", "event_counts",
		).
		Values(
			workflowRunID, rm.FunctionID, rm.BuildID, rm.PlanID, rm.StepName,
			rm.Status, rm.Summary, rm.Model,
			rm.Usage.InputTokens, rm.Usage.OutputTokens, rm.Usage.CacheReadInputTokens, rm.Usage.CacheCreationInputTokens,
			rm.Turns, rm.WallTimeSeconds, rm.CostUSD,
			results, rm.EventsArtifact, eventCounts,
		).
		// Every data column is left alone on conflict -- that is the whole
		// point of this path -- but the sequence still advances. A degraded
		// ingestion IS an execution: an interrupted agent writes no flight
		// output and lands here, and the interruption cap has nothing to count
		// if the row it collides with silently absorbs it. DO NOTHING cannot
		// express "preserve but count", so this is a DO UPDATE that touches
		// exactly one column.
		Suffix(`ON CONFLICT (build_id, plan_id) DO UPDATE SET
			ingestion_seq = agent_run_metrics.ingestion_seq + 1
		RETURNING (xmax = 0) AS inserted, ingestion_seq`).
		RunWith(f.conn).
		QueryRow().
		Scan(&inserted, &rm.IngestionSeq)
	return inserted, err
}

const runMetricsColumns = `m.workflow_run_id, m.function_id, m.build_id, m.plan_id, m.step_name,
	m.status, m.summary, m.model,
	m.input_tokens, m.output_tokens, m.cache_read_tokens, m.cache_creation_tokens,
	m.turns, m.wall_time_seconds, m.cost_usd,
	m.results, m.events_artifact, m.event_counts,
	EXTRACT(EPOCH FROM m.created_at)::bigint,
	b.status`

// runMetricsFrom joins the builds table so each metric row carries the
// server-derived build status (U3). LEFT JOIN so a metric whose build row is
// absent still returns (build_status scans as empty).
const runMetricsFrom = ` FROM agent_run_metrics m
	 LEFT JOIN builds b ON b.id = m.build_id`

func (f *agentRunMetricsFactory) GetByBuild(buildID int) ([]agentschema.RunMetrics, error) {
	rows, err := f.conn.Query(
		`SELECT `+runMetricsColumns+runMetricsFrom+`
		 WHERE m.build_id = $1 ORDER BY m.created_at ASC, m.id ASC`, buildID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRunMetricsRows(rows)
}

// ListByWorkflowRun returns the metrics of one durable workflow run,
// oldest-first. The run is the metric's execution identity: an INNER JOIN on
// agent_workflow_runs asserts the row's workflow_run_id points to an existing
// run whose workflow_name matches (identity + authz — a run id under the wrong
// workflow name returns nothing), while the builds join stays LEFT so a metric
// whose build row was deleted still returns (BuildStatus empty).
func (f *agentRunMetricsFactory) ListByWorkflowRun(workflowName string, runID snapshot.WorkflowRunID) ([]agentschema.RunMetrics, error) {
	rows, err := f.conn.Query(
		`SELECT `+runMetricsColumns+`
		 FROM agent_run_metrics m
		 JOIN agent_workflow_runs run ON run.id = m.workflow_run_id AND run.workflow_name = $1 AND run.definition_kind = 'workflow'
		 LEFT JOIN builds b ON b.id = m.build_id
		 WHERE m.workflow_run_id = $2 ORDER BY m.created_at ASC, m.id ASC`, workflowName, int64(runID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRunMetricsRows(rows)
}

// defaultRecentLimit / maxRecentLimit bound ListRecent so an unbounded or
// hostile ?limit= can never scan the whole table.
const (
	defaultRecentLimit = 100
	maxRecentLimit     = 500
)

func (f *agentRunMetricsFactory) ListRecent(limit int) ([]agentschema.RunMetrics, error) {
	if limit <= 0 || limit > maxRecentLimit {
		limit = defaultRecentLimit
	}
	rows, err := f.conn.Query(
		`SELECT `+runMetricsColumns+runMetricsFrom+`
		 ORDER BY m.created_at DESC, m.id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRunMetricsRows(rows)
}

// workflowRunIDValue converts the durable identity to a driver parameter:
// int64 when present, nil (SQL NULL) for an unbound CI invocation. Passing the
// concrete int64/nil keeps the lib/pq parameter converter off the named-type
// reflection path.
func workflowRunIDValue(rm *agentschema.RunMetrics) any {
	if rm.WorkflowRunID == nil {
		return nil
	}
	return int64(*rm.WorkflowRunID)
}

// WorkflowStats aggregates agent_run_metrics per workflow version for one
// workflow. Workflow identity is the durable run, not a tag copied onto the
// metric row: the INNER JOIN to agent_workflow_runs both selects the
// workflow's rows and supplies the version, so a metric with no run (ad-hoc
// CI) contributes to nothing. The build unit is a distinct build_id;
// cost/turns are summed across the build's step rows (the LEFT JOIN to builds
// is 1:1 so there is no fan-out) and success is counted from the joined
// build's terminal status.
//
// A reclaimed build falls back to the durable record. Workflow-run template
// retirement destroys the run instance pipeline, and builds_pipeline_id_fkey
// is ON DELETE CASCADE, so a retired version's builds are gone while its
// metrics rows (build_id carries no foreign key) remain — counting success
// from b.status alone would keep the runs and the cost but silently deflate a
// retired version's success rate to zero. agent_workflow_runs.execution_status
// is the immutable copy of that same build's terminal status (migration
// 1773106103), so it answers for the build once the build cannot. The fallback
// is deliberately scoped to b.id IS NULL AND m.build_id = run.planned_build_id:
// a live build always answers for itself, and a metric row from any other
// build never inherits the planned build's outcome.
func (f *agentRunMetricsFactory) WorkflowStats(workflowName string) ([]agentschema.WorkflowVersionStats, error) {
	rows, err := f.conn.Query(`
		SELECT
			run.workflow_version,
			COUNT(DISTINCT m.build_id)                                       AS runs,
			COUNT(DISTINCT m.workflow_run_id)                                AS workflow_runs,
			COUNT(DISTINCT m.build_id) FILTER (
				WHERE b.status = 'succeeded'
				   OR (b.id IS NULL
				       AND m.build_id = run.planned_build_id
				       AND run.execution_status = 'succeeded')
			)                                                                 AS succeeded_runs,
			COALESCE(SUM(m.cost_usd), 0)                                      AS total_cost_usd,
			COALESCE(SUM(m.turns), 0)                                         AS total_turns
		FROM agent_run_metrics m
		JOIN agent_workflow_runs run ON run.id = m.workflow_run_id AND run.workflow_name = $1
		LEFT JOIN builds b ON b.id = m.build_id
		GROUP BY run.workflow_version
		ORDER BY run.workflow_version DESC`, workflowName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []agentschema.WorkflowVersionStats{}
	for rows.Next() {
		var s agentschema.WorkflowVersionStats
		var version sql.NullInt64
		if err := rows.Scan(&version, &s.Runs, &s.WorkflowRuns, &s.SucceededRuns, &s.TotalCostUSD, &s.TotalTurns); err != nil {
			return nil, err
		}
		if version.Valid {
			v := int(version.Int64)
			s.Version = &v
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func scanRunMetricsRows(rows *sql.Rows) ([]agentschema.RunMetrics, error) {
	results := []agentschema.RunMetrics{}
	for rows.Next() {
		var rm agentschema.RunMetrics
		var resultsPayload, eventCounts []byte
		var buildStatus sql.NullString
		var workflowRunID sql.NullInt64
		err := rows.Scan(
			&workflowRunID, &rm.FunctionID, &rm.BuildID, &rm.PlanID, &rm.StepName,
			&rm.Status, &rm.Summary, &rm.Model,
			&rm.Usage.InputTokens, &rm.Usage.OutputTokens, &rm.Usage.CacheReadInputTokens, &rm.Usage.CacheCreationInputTokens,
			&rm.Turns, &rm.WallTimeSeconds, &rm.CostUSD,
			&resultsPayload, &rm.EventsArtifact, &eventCounts,
			&rm.CreatedAt,
			&buildStatus,
		)
		if err != nil {
			return nil, err
		}
		if workflowRunID.Valid {
			id := agentschema.WorkflowRunID(workflowRunID.Int64)
			rm.WorkflowRunID = &id
		}
		rm.BuildStatus = buildStatus.String
		if len(resultsPayload) > 0 {
			rm.Results = json.RawMessage(resultsPayload)
		}
		// fuse build + step status here, where BuildStatus materializes, so
		// every read carries the U3 display truth (needs Results set first)
		rm.Outcome = rm.DeriveOutcome()
		if len(eventCounts) > 0 {
			if err := json.Unmarshal(eventCounts, &rm.EventCounts); err != nil {
				return nil, err
			}
		}
		results = append(results, rm)
	}
	return results, rows.Err()
}
