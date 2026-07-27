// Package costs serves the agent cost-ledger API:
// GET /api/v1/agent/costs (GetAgentCostRollup). The ledger is written
// in-process by the agent step, never over HTTP.
package costs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/concourse/concourse/agent/budget"
)

type Handler struct {
	ledger  budget.Ledger
	checker budget.Checker
}

func NewHandler(ledger budget.Ledger, checker budget.Checker) *Handler {
	return &Handler{ledger: ledger, checker: checker}
}

// DailySummary reports today's spend against --agent-daily-budget-usd.
// CapUSD == 0 means uncapped.
type DailySummary struct {
	CapUSD       float64 `json:"daily_cap_usd"`
	SpentUSD     float64 `json:"daily_spent_usd"`
	RemainingUSD float64 `json:"daily_remaining_usd"`
	Exhausted    bool    `json:"daily_exhausted"`
}

// RollupResponse is the GET /api/v1/agent/costs body.
type RollupResponse struct {
	GroupBy string             `json:"group_by"`
	Summary DailySummary       `json:"summary"`
	Rows    []budget.RollupRow `json:"rows"`
}

// GetRollup handles GET /api/v1/agent/costs
// (?group_by=user|day|workflow|model|step&since=&until=).
func (h *Handler) GetRollup(w http.ResponseWriter, r *http.Request) {
	groupBy := r.URL.Query().Get("group_by")
	if groupBy == "" {
		groupBy = budget.GroupByDay
	}
	if !budget.ValidGroupBy(groupBy) {
		http.Error(w, fmt.Sprintf("group_by must be one of user|day|workflow|model|step, got %q", groupBy), http.StatusBadRequest)
		return
	}

	since, err := ParseTimeParam(r.URL.Query().Get("since"))
	if err != nil {
		http.Error(w, "invalid since: "+err.Error(), http.StatusBadRequest)
		return
	}
	if since.IsZero() {
		since = time.Now().Add(-30 * 24 * time.Hour)
	}
	until, err := ParseTimeParam(r.URL.Query().Get("until"))
	if err != nil {
		http.Error(w, "invalid until: "+err.Error(), http.StatusBadRequest)
		return
	}

	rows, err := h.ledger.Rollup(groupBy, since, until)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []budget.RollupRow{}
	}

	resp := RollupResponse{GroupBy: groupBy, Rows: rows}
	// Degrade: the summary must never block the rollup.
	if daily, err := h.checker.GlobalDailyRemaining(); err == nil {
		resp.Summary = DailySummary{
			CapUSD:       daily.LimitUSD,
			SpentUSD:     daily.SpentUSD,
			RemainingUSD: daily.RemainingUSD,
			Exhausted:    daily.Exhausted,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// ParseTimeParam accepts RFC3339 or YYYY-MM-DD; empty means zero time.
// Exported so the agent_cost_rollup MCP tool accepts the identical syntax as
// GET /api/v1/agent/costs — the two surfaces must not drift.
func ParseTimeParam(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("want RFC3339 or YYYY-MM-DD, got %q", v)
}
