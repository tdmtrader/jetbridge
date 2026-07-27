// Package budget is the single source of budget truth (contract §2.7).
// All budget arithmetic — the global daily cap and the append-only spend
// ledger — lives here and nowhere else. Consumers: the agent step (spend
// recording) and the costs API (rollups).
//
// Per-run admission is NOT decided here: the durable reservation taken by
// atc/db's workflow/experiment budget factories is the single admission
// authority, and it reconstructs prior spend from this ledger by
// workflow_run_id. Every dollar enters the ledger exactly once (Record,
// append-only), so the next reservation sees it without any reconciliation.
package budget

import (
	"encoding/json"
	"time"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

// Ledger source values — must match the agent_cost_ledger CHECK constraint.
const (
	SourceAgentStep = "agent_step"
	SourceCIAgent   = "ci_agent"
)

func ValidSource(s string) bool {
	switch s {
	case SourceAgentStep, SourceCIAgent:
		return true
	}
	return false
}

// Rollup dimensions for GetAgentCostRollup (?group_by=).
const (
	GroupByUser     = "user"
	GroupByDay      = "day"
	GroupByWorkflow = "workflow" // the ledger row's workflow run, by workflow name
	GroupByModel    = "model"    // reads the ledger model column
	GroupByStep     = "step"     // reads the ledger step_name column
)

func ValidGroupBy(g string) bool {
	switch g {
	case GroupByUser, GroupByDay, GroupByWorkflow, GroupByModel, GroupByStep:
		return true
	}
	return false
}

// LedgerEntry mirrors agent_cost_ledger columns. Zero OccurredAt means "let
// the DB default to now()". Nil UserID/WorkflowRunID map to NULL.
// WorkflowRunID + FunctionID are the server-owned spend attribution: the
// durable agent_workflow_runs row the step ran in, and the workflow function
// that spent the money.
type LedgerEntry struct {
	OccurredAt          time.Time       `json:"occurred_at,omitempty"`
	UserID              *int            `json:"user_id,omitempty"`
	UserName            string          `json:"user_name,omitempty"`
	WorkflowRunID       *int64          `json:"workflow_run_id,omitempty"`
	FunctionID          string          `json:"function_id,omitempty"`
	BuildID             int             `json:"build_id,omitempty"`
	StepName            string          `json:"step_name,omitempty"`
	Source              string          `json:"source"`
	Provider            string          `json:"provider,omitempty"`
	Model               string          `json:"model,omitempty"`
	InputTokens         int64           `json:"input_tokens,omitempty"`
	OutputTokens        int64           `json:"output_tokens,omitempty"`
	CacheReadTokens     int64           `json:"cache_read_tokens,omitempty"`
	CacheCreationTokens int64           `json:"cache_creation_tokens,omitempty"`
	Turns               int             `json:"turns,omitempty"`
	CostUSD             float64         `json:"cost_usd"`
	Metadata            json.RawMessage `json:"metadata,omitempty"`
}

// Remaining reports budget headroom. LimitUSD == 0 means UNCAPPED (the
// same "0 = uncapped" convention as AgentStep.BudgetSliceUSD, §2.8);
// RemainingUSD is meaningless when uncapped.
type Remaining struct {
	LimitUSD     float64 `json:"limit_usd"`
	SpentUSD     float64 `json:"spent_usd"`
	RemainingUSD float64 `json:"remaining_usd"`
	Exhausted    bool    `json:"exhausted"`
}

// Checker owns the global daily cap and validates every ledger append.
//
//counterfeiter:generate . Checker
type Checker interface {
	// GlobalDailyRemaining = daily cap − SUM(ledger cost since local midnight).
	GlobalDailyRemaining() (Remaining, error)
	// Record appends a ledger row (append-only).
	Record(entry LedgerEntry) error
}

// Ledger is the persistence seam implemented by
// atc/db.NewAgentCostLedgerFactory. Rollups are queries, never
// materialized mutations; rows are append-only.
//
//counterfeiter:generate . Ledger
type Ledger interface {
	Insert(entry LedgerEntry) error
	// SpentSince sums ALL sources (the global daily cap includes platform
	// spend, §1.13).
	SpentSince(since time.Time) (float64, error)
	// Rollup groups by a GroupBy* dimension over [since, until); zero
	// until means unbounded.
	Rollup(groupBy string, since, until time.Time) ([]RollupRow, error)
}

// RollupRow is one aggregated line of GetAgentCostRollup.
type RollupRow struct {
	Key          string  `json:"key"`
	Entries      int     `json:"entries"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	Turns        int64   `json:"turns"`
	CostUSD      float64 `json:"cost_usd"`
}

// Config tunes a Checker. GlobalDailyCapUSD comes from the web flag
// --agent-daily-budget-usd (0 = unlimited). Location defines "local
// midnight" for the daily window (nil = time.Local).
type Config struct {
	GlobalDailyCapUSD float64
	Location          *time.Location
	Now               func() time.Time
}
