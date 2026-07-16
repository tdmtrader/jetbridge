// Package costs serves the agent cost-ledger API: POST /api/v1/agent/costs
// (SubmitAgentCostRecord — principal(costs:write) via the wrappa, with a
// static-token legacy bypass per the wave-1 contract addendum) and
// GET /api/v1/agent/costs (GetAgentCostRollup).
package costs

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/api/principals"
	"github.com/concourse/concourse/agent/budget"
)

type Handler struct {
	ledger       budget.Ledger
	checker      budget.Checker
	publishToken string
}

func NewHandler(ledger budget.Ledger, checker budget.Checker, publishToken string) *Handler {
	return &Handler{ledger: ledger, checker: checker, publishToken: publishToken}
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

// SubmitRecord handles POST /api/v1/agent/costs.
//
// Auth: the wrappa wraps this route in principal(costs:write) with a
// legacy bypass (atc/wrappa/api_auth_wrappa.go). A verified principal
// arrives via the request context; anything else falls back to the
// static publish token this handler has always validated (dual-accept
// window; removed with --agent-cost-publish-token). The ledger schema
// carries no submitter column, so the principal only authorizes — the
// entry's source field is the CHECK-constrained spend type, not an
// attribution.
func (h *Handler) SubmitRecord(w http.ResponseWriter, r *http.Request) {
	if _, ok := principals.FromContext(r.Context()); !ok {
		if h.publishToken == "" {
			http.Error(w, "agent cost recording is not enabled", http.StatusForbidden)
			return
		}
		auth := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(auth, "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(h.publishToken)) != 1 {
			http.Error(w, "invalid publish token", http.StatusUnauthorized)
			return
		}
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "request body exceeds 1MB", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	var entry budget.LedgerEntry
	if err := json.Unmarshal(body, &entry); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if err := h.checker.Record(entry); err != nil {
		if errors.Is(err, budget.ErrInvalidEntry) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "recorded"})
}

// GetRollup handles GET /api/v1/agent/costs
// (?group_by=user|ticket|day|workflow&since=&until=).
func (h *Handler) GetRollup(w http.ResponseWriter, r *http.Request) {
	groupBy := r.URL.Query().Get("group_by")
	if groupBy == "" {
		groupBy = budget.GroupByDay
	}
	if !budget.ValidGroupBy(groupBy) {
		http.Error(w, fmt.Sprintf("group_by must be one of user|ticket|day|workflow, got %q", groupBy), http.StatusBadRequest)
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
