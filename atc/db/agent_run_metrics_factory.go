package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

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
//
//counterfeiter:generate . AgentRunMetricsFactory
type AgentRunMetricsFactory interface {
	metrics.Store
	metrics.AttemptStore
}

func NewAgentRunMetricsFactory(conn DbConn) AgentRunMetricsFactory {
	return &agentRunMetricsFactory{conn: conn}
}

type agentRunMetricsFactory struct {
	conn DbConn
}

// UpsertExecutionAttempt persists cumulative recorder counters under the
// exact durable recovery attempt selected by the server. Provider counters
// reset when recovery creates a fresh process, so the legacy aggregate adds
// this attempt's monotonic delta rather than taking a GREATEST across attempts.
// The attempt, aggregate, and cost-ledger delta share one transaction.
func (f *agentRunMetricsFactory) UpsertExecutionAttempt(request metrics.ExecutionAttemptRequest) (metrics.ExecutionAttemptUpdate, error) {
	key, incoming := request.Key, request.Metrics
	if err := validateExecutionAttempt(key, incoming); err != nil {
		return metrics.ExecutionAttemptUpdate{}, err
	}
	if !metrics.ValidExecutionAttemptAttribution(request.Attribution) {
		return metrics.ExecutionAttemptUpdate{}, fmt.Errorf("%w: source, provider, and model must be server-owned canonical values", metrics.ErrExecutionAttemptInvalid)
	}
	if incoming.Model != "" && incoming.Model != request.Attribution.Model {
		return metrics.ExecutionAttemptUpdate{}, fmt.Errorf("%w: telemetry model conflicts with server attribution", metrics.ErrExecutionAttemptIdentityDrift)
	}

	tx, err := f.conn.Begin()
	if err != nil {
		return metrics.ExecutionAttemptUpdate{}, err
	}
	defer Rollback(tx)
	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended($1, 1773106146))`, fmt.Sprintf("%d:%s", key.BuildID, key.PlanID)); err != nil {
		return metrics.ExecutionAttemptUpdate{}, err
	}

	durable, err := readDurableMetricAttempt(tx, key)
	if err != nil {
		return metrics.ExecutionAttemptUpdate{}, err
	}
	if durable.buildID != key.BuildID || durable.planID != key.PlanID || durable.executionAttempt != key.ExecutionAttempt {
		return metrics.ExecutionAttemptUpdate{}, fmt.Errorf("%w: attempt_id=%d does not match build_id=%d plan_id=%q execution_attempt=%d", metrics.ErrExecutionAttemptInvalid, key.AttemptID, key.BuildID, key.PlanID, key.ExecutionAttempt)
	}
	if incoming.FunctionID != "" && incoming.FunctionID != durable.functionID {
		return metrics.ExecutionAttemptUpdate{}, fmt.Errorf("%w: telemetry function conflicts with durable attempt", metrics.ErrExecutionAttemptIdentityDrift)
	}
	if incoming.WorkflowRunID != nil && (durable.workflowRunID == nil || *incoming.WorkflowRunID != *durable.workflowRunID) {
		return metrics.ExecutionAttemptUpdate{}, fmt.Errorf("%w: telemetry workflow run conflicts with durable attempt", metrics.ErrExecutionAttemptIdentityDrift)
	}
	serverMetrics := *incoming
	serverMetrics.FunctionID = durable.functionID
	serverMetrics.WorkflowRunID = durable.workflowRunID
	serverMetrics.Model = request.Attribution.Model
	rm := &serverMetrics

	previous, previousAttribution, found, err := readExecutionAttemptMetric(tx, key.AttemptID)
	if err != nil {
		return metrics.ExecutionAttemptUpdate{}, err
	}
	if found && (!sameAttemptMetricIdentity(previous, rm) || !sameExecutionAttemptAttribution(previousAttribution, request.Attribution)) {
		return metrics.ExecutionAttemptUpdate{}, fmt.Errorf("%w: attempt_id=%d", metrics.ErrExecutionAttemptIdentityDrift, key.AttemptID)
	}

	delta := counterValues(rm)
	stored := *rm
	if found {
		delta = counterDelta(previous, rm)
		stored.EventCounts = mergeAttemptEventCounts(previous.EventCounts, rm.EventCounts)
	}

	aggregate, aggregateFound, err := readAggregateForUpdate(tx, key.BuildID, key.PlanID)
	if err != nil {
		return metrics.ExecutionAttemptUpdate{}, err
	}
	if aggregateFound && !sameAggregateMetricIdentity(aggregate, rm) {
		return metrics.ExecutionAttemptUpdate{}, fmt.Errorf("%w: aggregate build_id=%d plan_id=%q", metrics.ErrExecutionAttemptIdentityDrift, key.BuildID, key.PlanID)
	}
	if aggregateFound && !found {
		hasAttempt, err := hasAttemptMetrics(tx, key.BuildID, key.PlanID)
		if err != nil {
			return metrics.ExecutionAttemptUpdate{}, err
		}
		if !hasAttempt {
			return metrics.ExecutionAttemptUpdate{}, fmt.Errorf("%w: build_id=%d plan_id=%q", metrics.ErrExecutionAttemptAggregateAmbiguous, key.BuildID, key.PlanID)
		}
	}
	if request.FinalPresentation {
		finalAttemptID, hasFinal, err := finalizedAttemptMetric(tx, key.BuildID, key.PlanID)
		if err != nil {
			return metrics.ExecutionAttemptUpdate{}, err
		}
		if hasFinal && finalAttemptID != key.AttemptID {
			return metrics.ExecutionAttemptUpdate{}, fmt.Errorf("%w: build_id=%d plan_id=%q", metrics.ErrExecutionAttemptPresentationFinalized, key.BuildID, key.PlanID)
		}
	}

	if err := upsertExecutionAttemptMetric(tx, key.AttemptID, key.ExecutionAttempt, request.Attribution, &stored, request.FinalPresentation); err != nil {
		return metrics.ExecutionAttemptUpdate{}, err
	}
	if aggregateFound {
		if err := applyAttemptAggregateDelta(tx, key.BuildID, key.PlanID, rm, delta, addEventCounts(aggregate.EventCounts, delta.EventCounts), request.FinalPresentation); err != nil {
			return metrics.ExecutionAttemptUpdate{}, err
		}
	} else if err := insertAttemptAggregate(tx, rm); err != nil {
		return metrics.ExecutionAttemptUpdate{}, err
	}
	if hasLedgerCounters(delta) {
		if err := lockAgentBudgetAccounting(context.Background(), tx); err != nil {
			return metrics.ExecutionAttemptUpdate{}, err
		}
		if err := insertExecutionAttemptLedger(tx, request.Attribution, key, rm, delta); err != nil {
			return metrics.ExecutionAttemptUpdate{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return metrics.ExecutionAttemptUpdate{}, err
	}
	if found {
		return metrics.ExecutionAttemptUpdate{Previous: previous, Delta: delta}, nil
	}
	return metrics.ExecutionAttemptUpdate{Inserted: true, Delta: delta}, nil
}

type durableMetricAttempt struct {
	buildID          int
	planID           string
	executionAttempt int
	functionID       string
	workflowRunID    *agentschema.WorkflowRunID
}

func readDurableMetricAttempt(tx Tx, key metrics.ExecutionAttemptKey) (durableMetricAttempt, error) {
	var durable durableMetricAttempt
	var workflowRunID sql.NullInt64
	err := tx.QueryRow(`SELECT h.build_id, h.plan_id, a.attempt_number, h.function_id, h.workflow_run_provenance_id
		FROM agent_run_attempts a
		JOIN agent_run_checkpoint_heads h ON h.id = a.head_id
		WHERE a.id = $1 FOR UPDATE`, key.AttemptID).Scan(
		&durable.buildID, &durable.planID, &durable.executionAttempt, &durable.functionID, &workflowRunID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return durableMetricAttempt{}, fmt.Errorf("%w: durable attempt does not exist", metrics.ErrExecutionAttemptInvalid)
	}
	if err != nil {
		return durableMetricAttempt{}, err
	}
	if workflowRunID.Valid {
		value := agentschema.WorkflowRunID(workflowRunID.Int64)
		durable.workflowRunID = &value
	}
	return durable, nil
}

func validateExecutionAttempt(key metrics.ExecutionAttemptKey, rm *agentschema.RunMetrics) error {
	if key.AttemptID <= 0 || key.BuildID <= 0 || key.PlanID == "" || key.ExecutionAttempt <= 0 || rm == nil || rm.BuildID != key.BuildID || rm.PlanID != key.PlanID {
		return fmt.Errorf("%w: exact attempt, build, plan, and execution identity is required", metrics.ErrExecutionAttemptInvalid)
	}
	if rm.Usage.InputTokens < 0 || rm.Usage.OutputTokens < 0 || rm.Usage.CacheReadInputTokens < 0 || rm.Usage.CacheCreationInputTokens < 0 || rm.Turns < 0 || rm.WallTimeSeconds < 0 || rm.CostUSD < 0 {
		return fmt.Errorf("%w: provider counters must be non-negative", metrics.ErrExecutionAttemptInvalid)
	}
	for name, count := range rm.EventCounts {
		if name == "" || count < 0 {
			return fmt.Errorf("%w: event counts must be named and non-negative", metrics.ErrExecutionAttemptInvalid)
		}
	}
	return nil
}

func counterValues(rm *agentschema.RunMetrics) metrics.CounterDelta {
	return metrics.CounterDelta{Usage: rm.Usage, Turns: rm.Turns, WallTimeSeconds: rm.WallTimeSeconds, CostUSD: rm.CostUSD, EventCounts: cloneEventCounts(rm.EventCounts)}
}

func counterDelta(previous, incoming *agentschema.RunMetrics) metrics.CounterDelta {
	delta := metrics.CounterDelta{}
	delta.Usage.InputTokens = maxInt64(0, incoming.Usage.InputTokens-previous.Usage.InputTokens)
	delta.Usage.OutputTokens = maxInt64(0, incoming.Usage.OutputTokens-previous.Usage.OutputTokens)
	delta.Usage.CacheReadInputTokens = maxInt64(0, incoming.Usage.CacheReadInputTokens-previous.Usage.CacheReadInputTokens)
	delta.Usage.CacheCreationInputTokens = maxInt64(0, incoming.Usage.CacheCreationInputTokens-previous.Usage.CacheCreationInputTokens)
	delta.Turns = maxInt(0, incoming.Turns-previous.Turns)
	delta.WallTimeSeconds = maxInt(0, incoming.WallTimeSeconds-previous.WallTimeSeconds)
	if incoming.CostUSD > previous.CostUSD {
		delta.CostUSD = incoming.CostUSD - previous.CostUSD
	}
	for name, count := range incoming.EventCounts {
		if count > previous.EventCounts[name] {
			if delta.EventCounts == nil {
				delta.EventCounts = map[string]int{}
			}
			delta.EventCounts[name] = count - previous.EventCounts[name]
		}
	}
	return delta
}

func cloneEventCounts(in map[string]int) map[string]int {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int, len(in))
	for name, count := range in {
		out[name] = count
	}
	return out
}

func mergeAttemptEventCounts(previous, incoming map[string]int) map[string]int {
	out := cloneEventCounts(previous)
	if out == nil {
		out = map[string]int{}
	}
	for name, count := range incoming {
		if count > out[name] {
			out[name] = count
		}
	}
	return out
}

func addEventCounts(previous, delta map[string]int) map[string]int {
	out := cloneEventCounts(previous)
	if out == nil {
		out = map[string]int{}
	}
	for name, count := range delta {
		out[name] += count
	}
	return out
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func sameAttemptMetricIdentity(a, b *agentschema.RunMetrics) bool {
	return sameAggregateMetricIdentity(a, b) && a.Model == b.Model
}

func sameAggregateMetricIdentity(a, b *agentschema.RunMetrics) bool {
	if a == nil || b == nil || a.BuildID != b.BuildID || a.PlanID != b.PlanID || a.FunctionID != b.FunctionID || a.StepName != b.StepName {
		return false
	}
	return (a.WorkflowRunID == nil) == (b.WorkflowRunID == nil) && (a.WorkflowRunID == nil || *a.WorkflowRunID == *b.WorkflowRunID)
}

func sameExecutionAttemptAttribution(a, b metrics.ExecutionAttemptAttribution) bool {
	return nullableIntEqual(a.UserID, b.UserID) && a.UserName == b.UserName && a.Source == b.Source && a.Provider == b.Provider && a.Model == b.Model
}

func nullableIntEqual(a, b *int) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

func readExecutionAttemptMetric(tx Tx, attemptID int64) (*agentschema.RunMetrics, metrics.ExecutionAttemptAttribution, bool, error) {
	var rm agentschema.RunMetrics
	var attribution metrics.ExecutionAttemptAttribution
	var workflowRunID, userID sql.NullInt64
	var eventCounts []byte
	err := tx.QueryRow(`SELECT workflow_run_id, function_id, build_id, plan_id, step_name, model,
		input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, turns, wall_time_seconds, cost_usd, event_counts,
		user_id, user_name, source, provider
		FROM agent_run_attempt_metrics WHERE attempt_id = $1 FOR UPDATE`, attemptID).Scan(
		&workflowRunID, &rm.FunctionID, &rm.BuildID, &rm.PlanID, &rm.StepName, &rm.Model,
		&rm.Usage.InputTokens, &rm.Usage.OutputTokens, &rm.Usage.CacheReadInputTokens, &rm.Usage.CacheCreationInputTokens, &rm.Turns, &rm.WallTimeSeconds, &rm.CostUSD, &eventCounts,
		&userID, &attribution.UserName, &attribution.Source, &attribution.Provider,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, metrics.ExecutionAttemptAttribution{}, false, nil
	}
	if err != nil {
		return nil, metrics.ExecutionAttemptAttribution{}, false, err
	}
	if workflowRunID.Valid {
		value := agentschema.WorkflowRunID(workflowRunID.Int64)
		rm.WorkflowRunID = &value
	}
	if len(eventCounts) > 0 {
		if err := json.Unmarshal(eventCounts, &rm.EventCounts); err != nil {
			return nil, metrics.ExecutionAttemptAttribution{}, false, err
		}
	}
	if userID.Valid {
		value := int(userID.Int64)
		attribution.UserID = &value
	}
	attribution.Model = rm.Model
	return &rm, attribution, true, nil
}

func readAggregateForUpdate(tx Tx, buildID int, planID string) (*agentschema.RunMetrics, bool, error) {
	var rm agentschema.RunMetrics
	var workflowRunID sql.NullInt64
	var eventCounts []byte
	err := tx.QueryRow(`SELECT workflow_run_id, function_id, build_id, plan_id, step_name, model,
		input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, turns, wall_time_seconds, cost_usd, event_counts
		FROM agent_run_metrics WHERE build_id = $1 AND plan_id = $2 FOR UPDATE`, buildID, planID).Scan(
		&workflowRunID, &rm.FunctionID, &rm.BuildID, &rm.PlanID, &rm.StepName, &rm.Model,
		&rm.Usage.InputTokens, &rm.Usage.OutputTokens, &rm.Usage.CacheReadInputTokens, &rm.Usage.CacheCreationInputTokens, &rm.Turns, &rm.WallTimeSeconds, &rm.CostUSD, &eventCounts,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if workflowRunID.Valid {
		value := agentschema.WorkflowRunID(workflowRunID.Int64)
		rm.WorkflowRunID = &value
	}
	if len(eventCounts) > 0 {
		if err := json.Unmarshal(eventCounts, &rm.EventCounts); err != nil {
			return nil, false, err
		}
	}
	return &rm, true, nil
}

func hasAttemptMetrics(tx Tx, buildID int, planID string) (bool, error) {
	var exists bool
	err := tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM agent_run_attempt_metrics WHERE build_id = $1 AND plan_id = $2)`, buildID, planID).Scan(&exists)
	return exists, err
}

func finalizedAttemptMetric(tx Tx, buildID int, planID string) (int64, bool, error) {
	var attemptID int64
	err := tx.QueryRow(`SELECT attempt_id FROM agent_run_attempt_metrics WHERE build_id = $1 AND plan_id = $2 AND display_finalized FOR UPDATE`, buildID, planID).Scan(&attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return attemptID, err == nil, err
}

func attemptMetricPayload(rm *agentschema.RunMetrics) (results, eventCounts any, err error) {
	eventCounts = []byte(`{}`)
	if len(rm.Results) > 0 {
		results = []byte(rm.Results)
	}
	if rm.EventCounts != nil {
		eventCounts, err = json.Marshal(rm.EventCounts)
	}
	return results, eventCounts, err
}

func upsertExecutionAttemptMetric(tx Tx, attemptID int64, executionAttempt int, attribution metrics.ExecutionAttemptAttribution, rm *agentschema.RunMetrics, finalPresentation bool) error {
	results, eventCounts, err := attemptMetricPayload(rm)
	if err != nil {
		return err
	}
	_, err = psql.Insert("agent_run_attempt_metrics").
		Columns("attempt_id", "build_id", "plan_id", "execution_attempt", "workflow_run_id", "function_id", "step_name", "user_id", "user_name", "source", "provider", "model", "status", "summary", "input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens", "turns", "wall_time_seconds", "cost_usd", "results", "events_artifact", "event_counts", "display_finalized").
		Values(attemptID, rm.BuildID, rm.PlanID, executionAttempt, workflowRunIDValue(rm), rm.FunctionID, rm.StepName, attribution.UserID, attribution.UserName, attribution.Source, attribution.Provider, rm.Model, rm.Status, rm.Summary, rm.Usage.InputTokens, rm.Usage.OutputTokens, rm.Usage.CacheReadInputTokens, rm.Usage.CacheCreationInputTokens, rm.Turns, rm.WallTimeSeconds, rm.CostUSD, results, rm.EventsArtifact, eventCounts, finalPresentation).
		Suffix(`ON CONFLICT (attempt_id) DO UPDATE SET
			status = CASE WHEN EXCLUDED.display_finalized THEN EXCLUDED.status ELSE agent_run_attempt_metrics.status END,
			summary = CASE WHEN EXCLUDED.display_finalized THEN EXCLUDED.summary ELSE agent_run_attempt_metrics.summary END,
			input_tokens = GREATEST(agent_run_attempt_metrics.input_tokens, EXCLUDED.input_tokens),
			output_tokens = GREATEST(agent_run_attempt_metrics.output_tokens, EXCLUDED.output_tokens),
			cache_read_tokens = GREATEST(agent_run_attempt_metrics.cache_read_tokens, EXCLUDED.cache_read_tokens),
			cache_creation_tokens = GREATEST(agent_run_attempt_metrics.cache_creation_tokens, EXCLUDED.cache_creation_tokens),
			turns = GREATEST(agent_run_attempt_metrics.turns, EXCLUDED.turns),
			wall_time_seconds = GREATEST(agent_run_attempt_metrics.wall_time_seconds, EXCLUDED.wall_time_seconds),
			cost_usd = GREATEST(agent_run_attempt_metrics.cost_usd, EXCLUDED.cost_usd),
			results = CASE WHEN EXCLUDED.display_finalized THEN EXCLUDED.results ELSE agent_run_attempt_metrics.results END,
			events_artifact = CASE WHEN EXCLUDED.display_finalized THEN EXCLUDED.events_artifact ELSE agent_run_attempt_metrics.events_artifact END,
			event_counts = EXCLUDED.event_counts,
			display_finalized = agent_run_attempt_metrics.display_finalized OR EXCLUDED.display_finalized,
			updated_at = clock_timestamp()`).RunWith(tx).Exec()
	return err
}

func insertAttemptAggregate(tx Tx, rm *agentschema.RunMetrics) error {
	results, eventCounts, err := attemptMetricPayload(rm)
	if err != nil {
		return err
	}
	_, err = psql.Insert("agent_run_metrics").Columns("workflow_run_id", "function_id", "build_id", "plan_id", "step_name", "status", "summary", "model", "input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens", "turns", "wall_time_seconds", "cost_usd", "results", "events_artifact", "event_counts").
		Values(workflowRunIDValue(rm), rm.FunctionID, rm.BuildID, rm.PlanID, rm.StepName, rm.Status, rm.Summary, rm.Model, rm.Usage.InputTokens, rm.Usage.OutputTokens, rm.Usage.CacheReadInputTokens, rm.Usage.CacheCreationInputTokens, rm.Turns, rm.WallTimeSeconds, rm.CostUSD, results, rm.EventsArtifact, eventCounts).RunWith(tx).Exec()
	return err
}

func applyAttemptAggregateDelta(tx Tx, buildID int, planID string, rm *agentschema.RunMetrics, delta metrics.CounterDelta, aggregateEvents map[string]int, finalPresentation bool) error {
	_, eventCounts, err := attemptMetricPayload(&agentschema.RunMetrics{EventCounts: aggregateEvents})
	if err != nil {
		return err
	}
	if !finalPresentation {
		_, err = tx.Exec(`UPDATE agent_run_metrics SET input_tokens = input_tokens + $1, output_tokens = output_tokens + $2, cache_read_tokens = cache_read_tokens + $3, cache_creation_tokens = cache_creation_tokens + $4, turns = turns + $5, wall_time_seconds = wall_time_seconds + $6, cost_usd = cost_usd + $7, event_counts = $8 WHERE build_id = $9 AND plan_id = $10`, delta.Usage.InputTokens, delta.Usage.OutputTokens, delta.Usage.CacheReadInputTokens, delta.Usage.CacheCreationInputTokens, delta.Turns, delta.WallTimeSeconds, delta.CostUSD, eventCounts, buildID, planID)
		return err
	}
	results, _, err := attemptMetricPayload(rm)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE agent_run_metrics SET status = $1, summary = $2, model = $3, results = $4, events_artifact = $5, event_counts = $6, input_tokens = input_tokens + $7, output_tokens = output_tokens + $8, cache_read_tokens = cache_read_tokens + $9, cache_creation_tokens = cache_creation_tokens + $10, turns = turns + $11, wall_time_seconds = wall_time_seconds + $12, cost_usd = cost_usd + $13 WHERE build_id = $14 AND plan_id = $15`, rm.Status, rm.Summary, rm.Model, results, rm.EventsArtifact, eventCounts, delta.Usage.InputTokens, delta.Usage.OutputTokens, delta.Usage.CacheReadInputTokens, delta.Usage.CacheCreationInputTokens, delta.Turns, delta.WallTimeSeconds, delta.CostUSD, buildID, planID)
	return err
}

func hasLedgerCounters(delta metrics.CounterDelta) bool {
	return delta.Usage.InputTokens > 0 || delta.Usage.OutputTokens > 0 || delta.Usage.CacheReadInputTokens > 0 || delta.Usage.CacheCreationInputTokens > 0 || delta.Turns > 0 || delta.CostUSD > 0
}

func insertExecutionAttemptLedger(tx Tx, attribution metrics.ExecutionAttemptAttribution, key metrics.ExecutionAttemptKey, rm *agentschema.RunMetrics, delta metrics.CounterDelta) error {
	metadata, err := json.Marshal(map[string]any{"attempt_id": key.AttemptID, "execution_attempt": key.ExecutionAttempt, "function_id": rm.FunctionID})
	if err != nil {
		return err
	}
	var workflowRunID any
	if rm.WorkflowRunID != nil {
		workflowRunID = int64(*rm.WorkflowRunID)
	}
	_, err = psql.Insert("agent_cost_ledger").Columns("occurred_at", "user_id", "user_name", "workflow_run_id", "function_id", "build_id", "step_name", "source", "provider", "model", "input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens", "turns", "cost_usd", "metadata").
		Values(sq.Expr("now()"), attribution.UserID, attribution.UserName, workflowRunID, rm.FunctionID, rm.BuildID, rm.StepName, attribution.Source, attribution.Provider, attribution.Model, delta.Usage.InputTokens, delta.Usage.OutputTokens, delta.Usage.CacheReadInputTokens, delta.Usage.CacheCreationInputTokens, delta.Turns, delta.CostUSD, metadata).RunWith(tx).Exec()
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
	workflowRunID := workflowRunIDValue(rm)

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
			event_counts = COALESCE(NULLIF(EXCLUDED.event_counts, '{}'::jsonb), agent_run_metrics.event_counts)
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
		Suffix(`ON CONFLICT (build_id, plan_id) DO NOTHING RETURNING true`).
		RunWith(f.conn).
		QueryRow().
		Scan(&inserted)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // conflict fired — existing row preserved
	}
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
		 JOIN agent_workflow_runs run ON run.id = m.workflow_run_id AND run.workflow_name = $1
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
