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
	checker := budget.NewChecker(ledger, budget.NoTicketBudgets{}, budget.Config{
		GlobalDailyCapUSD: 50,
		Location:          time.UTC,
	})
	return costs.NewHandler(ledger, checker, "publish-secret"), ledger
}

func submit(t *testing.T, h *costs.Handler, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/agent/costs", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.SubmitRecord(rec, req)
	return rec
}

func TestSubmitRequiresToken(t *testing.T) {
	h, _ := newHandler()
	if rec := submit(t, h, "", `{"source":"ci_agent","cost_usd":1}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: got %d", rec.Code)
	}
	if rec := submit(t, h, "wrong", `{"source":"ci_agent","cost_usd":1}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d", rec.Code)
	}
}

func TestSubmitDisabledWithoutConfiguredToken(t *testing.T) {
	ledger := budget.NewMemoryLedger()
	checker := budget.NewChecker(ledger, budget.NoTicketBudgets{}, budget.Config{Location: time.UTC})
	h := costs.NewHandler(ledger, checker, "")
	if rec := submit(t, h, "anything", `{"source":"ci_agent","cost_usd":1}`); rec.Code != http.StatusForbidden {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestSubmitRecordsEntry(t *testing.T) {
	h, ledger := newHandler()
	rec := submit(t, h, "publish-secret",
		`{"source":"ci_agent","cost_usd":0.42,"user_name":"alice","build_id":1234,"step_name":"review/analyze","model":"claude-sonnet-5","input_tokens":100,"output_tokens":50,"turns":4}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	spent, _ := ledger.SpentSince(time.Now().Add(-time.Minute))
	if spent != 0.42 {
		t.Fatalf("ledger spent = %v", spent)
	}
}

func TestSubmitRejectsInvalidEntries(t *testing.T) {
	h, _ := newHandler()
	if rec := submit(t, h, "publish-secret", `{"source":"slack","cost_usd":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad source: got %d", rec.Code)
	}
	if rec := submit(t, h, "publish-secret", `{"source":"ci_agent","cost_usd":-1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("negative cost: got %d", rec.Code)
	}
	if rec := submit(t, h, "publish-secret", `not json`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json: got %d", rec.Code)
	}
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
