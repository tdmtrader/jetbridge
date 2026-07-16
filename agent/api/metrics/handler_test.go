package metrics_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/metrics"
	schema "github.com/concourse/concourse/agent/schema"
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
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// Decode the body and assert exactly ONE row — proving the re-submit
	// deduped on (build_id, plan_id) at the handler layer (a Contains check
	// alone passes even if two rows were returned).
	var rows []schema.RunMetrics
	if err := json.NewDecoder(rec.Body).Decode(&rows); err != nil {
		t.Fatalf("decode list body: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 row after idempotent re-submit, got %d", len(rows))
	}
	if rows[0].PlanID != "abc" {
		t.Fatalf("expected plan_id abc, got %q", rows[0].PlanID)
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

func TestListRejectsInvalidTicketID(t *testing.T) {
	h := metrics.NewHandler(metrics.NewMemoryStore())
	for _, tc := range []struct{ name, id string }{
		{"missing", ""},
		{"non-numeric", "abc"},
		{"non-positive", "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/agent/tickets/x/metrics", nil)
			req.Form = map[string][]string{":ticket_id": {tc.id}}
			rec := httptest.NewRecorder()
			h.ListByTicket(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for ticket_id %q, got %d", tc.id, rec.Code)
			}
		})
	}
}

func TestStoreErrorsSurfaceAs500(t *testing.T) {
	h := metrics.NewHandler(errStore{})

	rec := httptest.NewRecorder()
	body := `{"build_id":9,"plan_id":"abc","step_name":"implement","status":"ok"}`
	h.SubmitMetrics(rec, httptest.NewRequest("POST", "/api/v1/agent/metrics", strings.NewReader(body)))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("submit: expected 500 on store error, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/agent/tickets/7/metrics", nil)
	req.Form = map[string][]string{":ticket_id": {"7"}}
	h.ListByTicket(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("list: expected 500 on store error, got %d", rec.Code)
	}
}

// errStore fails every call, exercising the handler's 500 paths.
type errStore struct{}

func (errStore) Upsert(*schema.RunMetrics) error { return errors.New("boom") }
func (errStore) UpsertReturningInserted(*schema.RunMetrics) (bool, *schema.RunMetrics, error) {
	return false, nil, errors.New("boom")
}
func (errStore) InsertIfAbsent(*schema.RunMetrics) (bool, error) { return false, errors.New("boom") }
func (errStore) GetByBuild(int) ([]schema.RunMetrics, error)     { return nil, errors.New("boom") }
func (errStore) ListByTicket(int) ([]schema.RunMetrics, error)   { return nil, errors.New("boom") }
func (errStore) ListRecent(int) ([]schema.RunMetrics, error)     { return nil, errors.New("boom") }
