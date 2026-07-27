package costs_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/api/costs"
	"github.com/concourse/concourse/agent/budget"
)

func newHandler() (*costs.Handler, *budget.MemoryLedger) {
	ledger := budget.NewMemoryLedger()
	checker := budget.NewChecker(ledger, budget.Config{
		GlobalDailyCapUSD: 50,
		Location:          time.UTC,
	})
	return costs.NewHandler(ledger, checker), ledger
}

func TestGetRollup(t *testing.T) {
	h, ledger := newHandler()
	now := time.Now().UTC()
	_ = ledger.Insert(budget.LedgerEntry{Source: budget.SourceCIAgent, UserName: "alice", CostUSD: 2, OccurredAt: now})
	_ = ledger.Insert(budget.LedgerEntry{Source: budget.SourceCIAgent, UserName: "bob", CostUSD: 3, OccurredAt: now})

	req := httptest.NewRequest("GET", "/api/v1/agent/costs?group_by=user", nil)
	rec := httptest.NewRecorder()
	h.GetRollup(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}

	var resp costs.RollupResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.GroupBy != "user" || len(resp.Rows) != 2 {
		t.Fatalf("resp: %+v", resp)
	}
	if resp.Summary.CapUSD != 50 || resp.Summary.SpentUSD != 5 || resp.Summary.RemainingUSD != 45 {
		t.Fatalf("summary: %+v", resp.Summary)
	}
}

func TestGetRollupDefaultsAndValidation(t *testing.T) {
	h, _ := newHandler()

	req := httptest.NewRequest("GET", "/api/v1/agent/costs", nil)
	rec := httptest.NewRecorder()
	h.GetRollup(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("default group_by: got %d", rec.Code)
	}
	var resp costs.RollupResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.GroupBy != "day" || resp.Rows == nil {
		t.Fatalf("resp: %+v", resp)
	}

	req = httptest.NewRequest("GET", "/api/v1/agent/costs?group_by=nonsense", nil)
	rec = httptest.NewRecorder()
	h.GetRollup(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad group_by: got %d", rec.Code)
	}

	req = httptest.NewRequest("GET", "/api/v1/agent/costs?since=garbage", nil)
	rec = httptest.NewRecorder()
	h.GetRollup(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad since: got %d", rec.Code)
	}
}

func TestGetRollupAcceptsModelAndStep(t *testing.T) {
	for _, g := range []string{"model", "step"} {
		h, _ := newHandler()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/costs?group_by="+g, nil)
		rec := httptest.NewRecorder()
		h.GetRollup(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("group_by=%s: status = %d, want 200; body=%s", g, rec.Code, rec.Body.String())
		}
	}
}

func TestGetRollupRejectsUnknownGroupByWithModelStepInMessage(t *testing.T) {
	h, _ := newHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/costs?group_by=nonsense", nil)
	rec := httptest.NewRecorder()
	h.GetRollup(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "model") || !strings.Contains(rec.Body.String(), "step") {
		t.Fatalf("error body %q must list model and step", rec.Body.String())
	}
}
