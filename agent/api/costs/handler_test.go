package costs_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/api/costs"
	"github.com/concourse/concourse/agent/api/principals"
	"github.com/concourse/concourse/agent/budget"
)

func newHandler() (*costs.Handler, *budget.MemoryLedger) {
	ledger := budget.NewMemoryLedger()
	checker := budget.NewChecker(ledger, budget.NoTicketBudgets{}, budget.Config{
		GlobalDailyCapUSD: 50,
		Location:          time.UTC,
	})
	return costs.NewHandler(ledger, checker), ledger
}

func submit(t *testing.T, h *costs.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/agent/costs", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.SubmitRecord(rec, req)
	return rec
}

func submitWithPrincipal(t *testing.T, h *costs.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/agent/costs", strings.NewReader(body))
	req = req.WithContext(principals.NewContext(req.Context(), principals.Principal{ID: 3, Name: "itest-recorder"}))
	rec := httptest.NewRecorder()
	h.SubmitRecord(rec, req)
	return rec
}

func TestSubmitRequiresScopedPrincipal(t *testing.T) {
	h, _ := newHandler()
	if rec := submit(t, h, `{"source":"ci_agent","cost_usd":1}`); rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403 without a scoped principal", rec.Code)
	}
}

func TestSubmitWithPrincipalContext(t *testing.T) {
	h, ledger := newHandler()

	req := httptest.NewRequest("POST", "/api/v1/agent/costs",
		strings.NewReader(`{"source":"ci_agent","cost_usd":0.42}`))
	// no Authorization header at all — the wrappa already verified the principal
	req = req.WithContext(principals.NewContext(req.Context(), principals.Principal{
		ID: 3, Name: "itest-recorder",
	}))
	rec := httptest.NewRecorder()
	h.SubmitRecord(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("code = %d body %s, want 201", rec.Code, rec.Body)
	}
	spent, _ := ledger.SpentSince(time.Now().Add(-time.Minute))
	if spent != 0.42 {
		t.Fatalf("ledger spent = %v", spent)
	}
}

func TestSubmitRecordsEntry(t *testing.T) {
	h, ledger := newHandler()
	req := httptest.NewRequest("POST", "/api/v1/agent/costs", strings.NewReader(
		`{"source":"ci_agent","cost_usd":0.42,"user_name":"alice","build_id":1234,"step_name":"review/analyze","model":"claude-sonnet-5","input_tokens":100,"output_tokens":50,"turns":4}`))
	req = req.WithContext(principals.NewContext(req.Context(), principals.Principal{ID: 3, Name: "itest-recorder"}))
	rec := httptest.NewRecorder()
	h.SubmitRecord(rec, req)
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
	if rec := submitWithPrincipal(t, h, `{"source":"slack","cost_usd":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad source: got %d", rec.Code)
	}
	if rec := submitWithPrincipal(t, h, `{"source":"ci_agent","cost_usd":-1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("negative cost: got %d", rec.Code)
	}
	if rec := submitWithPrincipal(t, h, `not json`); rec.Code != http.StatusBadRequest {
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
