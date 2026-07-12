package metrics

import (
	"encoding/json"
	"fmt"

	schema "github.com/concourse/concourse/agent/schema"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

// Store is the persistence interface for agent run metrics
// (shared-contracts §1.8/§2.4). Implemented by atc/db.AgentRunMetricsFactory.
//
//counterfeiter:generate . Store
type Store interface {
	// Upsert inserts the row, replacing any existing row with the same
	// (BuildID, PlanID) key. Ingestion is idempotent across step retries
	// and web-restart resumes.
	Upsert(rm *schema.RunMetrics) error
	// UpsertReturningInserted is Upsert with a ledger-dedup discriminator:
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
	// least once". Upsert is
	// Upsert(rm) = { _, _, err := UpsertReturningInserted(rm); return err }.
	UpsertReturningInserted(rm *schema.RunMetrics) (inserted bool, prev *schema.RunMetrics, err error)
	// InsertIfAbsent writes the row only when no (BuildID, PlanID) row exists
	// yet (ON CONFLICT (build_id, plan_id) DO NOTHING) and reports whether it
	// inserted. This is the DEGRADED-ingestion write (finding F24, 2026-07-09):
	// when a re-ingestion read no flight data — a web-restart resume whose
	// in-memory volume locator is gone, or a reaped-pod rerun — its zero-cost
	// status=error row must never clobber a real row written by an earlier,
	// successful ingestion. inserted=false means a row already existed and
	// nothing was written.
	InsertIfAbsent(rm *schema.RunMetrics) (inserted bool, err error)
	// GetByBuild returns rows for a build, oldest-first.
	GetByBuild(buildID int) ([]schema.RunMetrics, error)
	// ListByTicket returns rows for a ticket, oldest-first.
	ListByTicket(ticketID int) ([]schema.RunMetrics, error)
}

// ParseSubmission validates a POST /api/v1/agent/metrics body.
func ParseSubmission(body []byte) (*schema.RunMetrics, error) {
	var rm schema.RunMetrics
	if err := json.Unmarshal(body, &rm); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if rm.BuildID <= 0 {
		return nil, fmt.Errorf("build_id is required")
	}
	if rm.PlanID == "" {
		return nil, fmt.Errorf("plan_id is required")
	}
	if rm.StepName == "" {
		return nil, fmt.Errorf("step_name is required")
	}
	switch rm.Status {
	case schema.RunStatusOK, schema.RunStatusFailed, schema.RunStatusError, schema.RunStatusParked:
	default:
		return nil, fmt.Errorf("status must be one of ok|failed|error|parked")
	}
	return &rm, nil
}
