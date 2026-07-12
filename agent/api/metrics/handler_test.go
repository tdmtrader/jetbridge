package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/metrics"
)

func TestSubmitAndListByTicket(t *testing.T) {
	store := metrics.NewMemoryStore()
	h := metrics.NewHandler(store)

	body := `{"build_id":9,"plan_id":"abc","step_name":"implement","status":"ok","ticket_id":7,"cost_usd":0.5}`
	req := httptest.NewRequest("POST", "/api/v1/agent/metrics", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.SubmitMetrics(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// idempotent upsert on (build_id, plan_id)
	rec = httptest.NewRecorder()
	h.SubmitMetrics(rec, httptest.NewRequest("POST", "/api/v1/agent/metrics", strings.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 on re-submit, got %d", rec.Code)
	}

	listReq := httptest.NewRequest("GET", "/api/v1/agent/tickets/7/metrics", nil)
	listReq.Form = map[string][]string{":ticket_id": {"7"}}
	rec = httptest.NewRecorder()
	h.ListByTicket(rec, listReq)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"plan_id":"abc"`) {
		t.Fatalf("expected one row, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSubmitRejectsBadPayload(t *testing.T) {
	h := metrics.NewHandler(metrics.NewMemoryStore())
	rec := httptest.NewRecorder()
	h.SubmitMetrics(rec, httptest.NewRequest("POST", "/api/v1/agent/metrics", strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}
