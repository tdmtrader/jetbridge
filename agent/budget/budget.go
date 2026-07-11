// Package budget is the single source of budget truth (contract §2.7).
// All budget arithmetic — per-ticket remaining, global daily cap, per-step
// slices — lives here and nowhere else. Consumers: dispatch (admission),
// agent-step (slice env computation), gateway (mid-flight cutoff),
// scorecards/delivery-outcomes (rollups).
//
// Sharing rule (no double counting): the dispatcher admits a run using
// TicketRemaining/GlobalDailyRemaining at dispatch time and computes each
// step's AGENT_BUDGET_SLICE_USD via StepSlice against spend already in the
// ledger. The gateway then enforces ONLY its own step's slice against the
// spend it meters for that step; it never re-checks ticket or daily budgets
// mid-flight. Every dollar enters the ledger exactly once (Record,
// append-only), so the next dispatch-time computation sees gateway-metered
// spend without any reconciliation.
package budget

import (
	"encoding/json"
	"time"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

// Ledger source values — must match the agent_cost_ledger CHECK constraint (§1.4).
const (
	SourceAgentStep     = "agent_step"
	SourceGateway       = "gateway"
	SourceHarvestJudge  = "harvest_judge"
	SourceRetrospective = "retrospective"
	SourceCIAgent       = "ci_agent"
	SourceProbe         = "probe"
)

func ValidSource(s string) bool {
	switch s {
	case SourceAgentStep, SourceGateway, SourceHarvestJudge,
		SourceRetrospective, SourceCIAgent, SourceProbe:
		return true
	}
	return false
}

// Rollup dimensions for GetAgentCostRollup (?group_by=).
const (
	GroupByUser     = "user"
	GroupByTicket   = "ticket"
	GroupByDay      = "day"
	GroupByWorkflow = "workflow" // reads metadata->>'workflow' (see contract addendum)
)

func ValidGroupBy(g string) bool {
	switch g {
	case GroupByUser, GroupByTicket, GroupByDay, GroupByWorkflow:
		return true
	}
	return false
}

// LedgerEntry mirrors agent_cost_ledger columns (§1.4). Zero OccurredAt
// means "let the DB default to now()". Nil UserID/TicketID/PipelineRunID
// map to NULL (cross-aggregate join keys, not FKs).
type LedgerEntry struct {
	OccurredAt          time.Time       `json:"occurred_at,omitempty"`
	UserID              *int            `json:"user_id,omitempty"`
	UserName            string          `json:"user_name,omitempty"`
	TicketID            *int            `json:"ticket_id,omitempty"`
	PipelineRunID       *int            `json:"pipeline_run_id,omitempty"`
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

// Checker is consulted by the dispatcher (admission), the agent step
// (slice env computation) and the gateway (mid-flight cutoff). All
// arithmetic — including "how much is left" — lives here and nowhere else.
//counterfeiter:generate . Checker
type Checker interface {
	// TicketRemaining = ticket budget − SUM(ledger cost for ticket_id),
	// where ticket budget = tickets.budget_usd ?? workflow default.
	TicketRemaining(ticketID int) (Remaining, error)
	// GlobalDailyRemaining = daily cap − SUM(ledger cost since local midnight).
	GlobalDailyRemaining() (Remaining, error)
	// StepSlice resolves an agent step's budget slice: min(step slice from
	// the workflow definition, TicketRemaining). Zero/negative = do not start.
	StepSlice(ticketID int, sliceUSD float64) (Remaining, error)
	// Record appends a ledger row (append-only).
	Record(entry LedgerEntry) error
}

// Ledger is the persistence seam implemented by
// atc/db.NewAgentCostLedgerFactory. Rollups are queries, never
// materialized mutations; rows are append-only.
//counterfeiter:generate . Ledger
type Ledger interface {
	Insert(entry LedgerEntry) error
	// SpentForTicket sums the ticket's spend EXCLUDING harvest_judge rows:
	// per contracts §1.13 the judge must never be starved by an agent that
	// burned the ticket budget (judge spend is capped separately by the
	// workflow's judge_usd).
	SpentForTicket(ticketID int) (float64, error)
	// SpentSince sums ALL sources (the global daily cap includes platform
	// and judge spend, §1.13).
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

// TicketBudgets resolves "ticket budget = tickets.budget_usd ?? workflow
// default". Wave 1 has no tickets table, so NoTicketBudgets stands in;
// ticket-core/dispatch supply the real implementation without this
// package changing.
//counterfeiter:generate . TicketBudgets
type TicketBudgets interface {
	BudgetUSD(ticketID int) (float64, bool, error)
}

// NoTicketBudgets reports every ticket as having no configured budget
// (uncapped). Wave-1 wiring only.
type NoTicketBudgets struct{}

func (NoTicketBudgets) BudgetUSD(int) (float64, bool, error) { return 0, false, nil }

// Config tunes a Checker. GlobalDailyCapUSD comes from the web flag
// --agent-daily-budget-usd (0 = unlimited). Location defines "local
// midnight" for the daily window (nil = time.Local).
type Config struct {
	GlobalDailyCapUSD float64
	Location          *time.Location
	Now               func() time.Time
}
