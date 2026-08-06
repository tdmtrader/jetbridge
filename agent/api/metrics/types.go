package metrics

import (
	"errors"
	"strings"

	"github.com/concourse/concourse/agent/budget"
	schema "github.com/concourse/concourse/agent/schema"
	"github.com/concourse/concourse/agent/snapshot"
)

// ErrExecutionAttemptInvalid means a server-side caller did not provide an
// exact, durable attempt identity. Agent-authored metrics must never select an
// attempt: the scheduler derives it from the recovery authority.
var ErrExecutionAttemptInvalid = errors.New("invalid agent execution attempt")

// ErrExecutionAttemptIdentityDrift means a retry tried to change the durable
// identity or server-owned attribution recorded for an execution attempt.
var ErrExecutionAttemptIdentityDrift = errors.New("agent execution attempt identity drift")

// ErrExecutionAttemptAggregateAmbiguous prevents a legacy aggregate-only row
// from being silently mixed with recovery-aware attempt accounting.
var ErrExecutionAttemptAggregateAmbiguous = errors.New("agent execution attempt aggregate is ambiguous")

// ErrExecutionAttemptPresentationFinalized means another execution attempt is
// already the server-selected terminal presentation for this aggregate.
var ErrExecutionAttemptPresentationFinalized = errors.New("agent execution attempt presentation already finalized")

// ExecutionAttemptKey is the exact server-derived identity of one provider
// process. AttemptID binds it to the durable recovery record; the remaining
// fields are verified against that record before metrics are written.
type ExecutionAttemptKey struct {
	AttemptID        int64
	BuildID          int
	PlanID           string
	ExecutionAttempt int
}

// ExecutionAttemptAttribution is resolved by the scheduler/harness. Provider
// telemetry may report counters but must never choose durable accounting
// dimensions such as provider or model.
type ExecutionAttemptAttribution struct {
	UserID   *int
	UserName string
	Source   string
	Provider string
	Model    string
}

// ExecutionAttemptRequest is the server-only recovery-aware ingestion
// boundary. FinalPresentation is selected only after the executor determines
// that this exact durable attempt owns the terminal display result.
type ExecutionAttemptRequest struct {
	Key               ExecutionAttemptKey
	Metrics           *schema.RunMetrics
	FinalPresentation bool
	Attribution       ExecutionAttemptAttribution
}

// CounterDelta is the non-negative cumulative-counter change made by one
// execution attempt ingestion.
type CounterDelta struct {
	Usage           schema.Usage
	Turns           int
	WallTimeSeconds int
	CostUSD         float64
	EventCounts     map[string]int
}

// ExecutionAttemptUpdate reports the durable per-attempt delta. Metrics,
// aggregate, and ledger changes commit atomically.
type ExecutionAttemptUpdate struct {
	Inserted bool
	Previous *schema.RunMetrics
	Delta    CounterDelta
}

// AttemptStore is the recovery-aware, server-only extension of Store. Legacy
// callers retain Store's aggregate-only semantics.
type AttemptStore interface {
	UpsertExecutionAttempt(request ExecutionAttemptRequest) (ExecutionAttemptUpdate, error)
}

// ValidExecutionAttemptAttribution requires canonical server-selected ledger
// dimensions. Rejecting aliases prevents split provider/model rollups.
func ValidExecutionAttemptAttribution(a ExecutionAttemptAttribution) bool {
	return budget.ValidSource(a.Source) && canonicalExecutionAttemptDimension(a.Provider) && canonicalExecutionAttemptDimension(a.Model)
}

func canonicalExecutionAttemptDimension(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && strings.ToLower(value) == value
}

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

// Store is the persistence interface for agent run metrics
// (shared-contracts §1.8/§2.4). Implemented by atc/db.AgentRunMetricsFactory.
//
//counterfeiter:generate . Store
type Store interface {
	// UpsertReturningInserted inserts the row, replacing any existing row with
	// the same (BuildID, PlanID) key — ingestion is idempotent across step
	// retries and web-restart resumes — with a ledger-dedup discriminator:
	// inserted is true only when the row was newly INSERTed (the
	// ON CONFLICT (build_id, plan_id) clause did NOT fire), and false on a
	// resume/retry that updated an existing row. On an update, prev carries
	// the replaced row's ledger-relevant counters (CostUSD, Usage, Turns),
	// read atomically with the upsert; it is nil on a fresh insert or when
	// the write failed. The append-only agent_cost_ledger has no dedup key
	// of its own (§1.4), so callers that append a ledger row per ingestion
	// (Task 13) charge rm's full cost on a fresh insert and only the DELTA
	// (rm.CostUSD - prev.CostUSD) on an update — reusing the metrics table's
	// unique key as the single dedup authority. A pure first-insert gate is
	// NOT enough: a severed exec's partial ingestion inserts a zero-cost row
	// (the runner writes cost.record only after claude exits), and the
	// resume's full ingestion is an update — its delta is the step's entire
	// real spend. inserted=false with prev==nil (a lost concurrent-insert
	// race, or a failed write) is indeterminate: callers skip the append,
	// preserving "every dollar enters the ledger exactly once" over "at
	// least once".
	UpsertReturningInserted(rm *schema.RunMetrics) (inserted bool, prev *schema.RunMetrics, err error)
	// InsertIfAbsent writes the row only when no (BuildID, PlanID) row exists
	// yet, and reports whether it inserted. On conflict it leaves every data
	// column untouched but still advances the durable ingestion sequence, and
	// both paths write that sequence back to rm.IngestionSeq — the agent step
	// reads it to bound interruption restarts, so an implementation that
	// leaves it zero makes those restarts unbounded. This is the DEGRADED-ingestion write (finding F24, 2026-07-09):
	// when a re-ingestion read no flight data — a web-restart resume whose
	// in-memory volume locator is gone, or a reaped-pod rerun — its zero-cost
	// status=error row must never clobber a real row written by an earlier,
	// successful ingestion. inserted=false means a row already existed and
	// nothing was written.
	InsertIfAbsent(rm *schema.RunMetrics) (inserted bool, err error)
	// MarkRestartPending records that the next ingestion of this
	// (BuildID, PlanID) will be a NEW execution rather than a re-read of the
	// one already stored. The agent step calls it at the moment it decides to
	// return an interruption for retry. It must be durable: the web that
	// decides to retry is not necessarily the web that ingests next. Both
	// write paths clear it and report the verdict on rm.NewExecution. A no-op
	// when no row exists yet -- an interruption before any ingestion leaves
	// nothing to reconcile against.
	MarkRestartPending(buildID int, planID string) error
	// The list methods return rows with the server-derived display fields
	// populated: BuildStatus (builds join) and Outcome (schema.RunMetrics
	// DeriveOutcome — the U3 build/step fusion), so no API consumer has to
	// re-derive the display truth.
	//
	// GetByBuild returns rows for a build, oldest-first.
	GetByBuild(buildID int) ([]schema.RunMetrics, error)
	// ListByWorkflowRun returns rows for a durable workflow run, oldest-first.
	// The run is the metric's execution identity: the query joins
	// agent_workflow_runs to check both the workflow name and the run id (a
	// run id under the wrong workflow name returns nothing), but the metric
	// row itself remains returnable after its builds row is deleted.
	ListByWorkflowRun(workflowName string, runID snapshot.WorkflowRunID) ([]schema.RunMetrics, error)
	// ListRecent returns the most-recent rows across all workflow runs/builds,
	// newest-first, bounded by limit (a non-positive or oversized limit falls
	// back to a sane default). Powers the operator dashboard's recent-runs view.
	ListRecent(limit int) ([]schema.RunMetrics, error)
	// WorkflowStats returns one aggregation row per distinct workflow_version
	// for the named workflow, newest version first (NULL version last). The
	// "run" unit is a distinct build_id; success is counted from the joined
	// builds.status = 'succeeded'; WorkflowRuns counts distinct
	// workflow_run_id, the durable execution identity (the retired ticket_id
	// column is NOT an execution identity and no longer exists). Rows carry
	// only the raw counters — callers call
	// schema.WorkflowVersionStats.WithDerived for the ratios.
	WorkflowStats(workflowName string) ([]schema.WorkflowVersionStats, error)
}
