package metrics_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/metrics"
	schema "github.com/concourse/concourse/agent/schema"
	"github.com/concourse/concourse/agent/snapshot"
)

// runMetricsRequest builds a GET carrying the rata route params the handler
// reads (rata.Param → req.URL.Query().Get(":name")).
func runMetricsRequest(workflowName, runID string) *http.Request {
	req := httptest.NewRequest("GET", "/api/v1/agent/workflows/x/runs/y/metrics", nil)
	req.URL.RawQuery = ":workflow_name=" + url.QueryEscape(workflowName) + "&:workflow_run_id=" + url.QueryEscape(runID)
	return req
}

func TestListByWorkflowRun(t *testing.T) {
	store := metrics.NewMemoryStore()
	h := metrics.NewHandler(store)

	// Seed the way the exec's direct Upsert materializes durable identity —
	// the submit API path clears it (server-owned). The metric field is the
	// schema-local WorkflowRunID; the Store method still takes snapshot's.
	runID := schema.WorkflowRunID(71)
	if err := store.Upsert(&schema.RunMetrics{
		BuildID: 9001, PlanID: "p1", StepName: "review-diff", Status: "ok",
		WorkflowName: "code-review", WorkflowRunID: &runID, FunctionID: "review",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// a metric from a different run must not leak into this run's list
	other := schema.WorkflowRunID(72)
	if err := store.Upsert(&schema.RunMetrics{
		BuildID: 9002, PlanID: "p1", StepName: "x", Status: "ok",
		WorkflowName: "code-review", WorkflowRunID: &other,
	}); err != nil {
		t.Fatalf("seed other: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ListByWorkflowRun(rec, runMetricsRequest("code-review", "71"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var rows []schema.RunMetrics
	if err := json.NewDecoder(rec.Body).Decode(&rows); err != nil {
		t.Fatalf("decode list body: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 row for run 71, got %d", len(rows))
	}
	if rows[0].WorkflowRunID == nil || *rows[0].WorkflowRunID != runID {
		t.Fatalf("expected row bound to run 71, got %+v", rows[0].WorkflowRunID)
	}
	if rows[0].FunctionID != "review" {
		t.Fatalf("expected function_id review, got %q", rows[0].FunctionID)
	}
}

func TestListByBuild(t *testing.T) {
	store := metrics.NewMemoryStore()
	h := metrics.NewHandler(store)

	for _, body := range []string{
		`{"build_id":9,"plan_id":"abc","step_name":"implement","status":"ok","cost_usd":0.5}`,
		`{"build_id":9,"plan_id":"def","step_name":"review","status":"ok","cost_usd":0.25}`,
		`{"build_id":10,"plan_id":"abc","step_name":"implement","status":"ok","cost_usd":1.0}`,
	} {
		rec := httptest.NewRecorder()
		h.SubmitMetrics(rec, httptest.NewRequest("POST", "/api/v1/agent/metrics", strings.NewReader(body)))
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
		}
	}

	req := httptest.NewRequest("GET", "/api/v1/builds/9/agent-metrics", nil)
	req.Form = map[string][]string{":build_id": {"9"}}
	rec := httptest.NewRecorder()
	h.ListByBuild(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var rows []schema.RunMetrics
	if err := json.NewDecoder(rec.Body).Decode(&rows); err != nil {
		t.Fatalf("decode list body: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected exactly 2 rows for build 9, got %d", len(rows))
	}
	for _, row := range rows {
		if row.BuildID != 9 {
			t.Fatalf("expected only build 9 rows, got build %d", row.BuildID)
		}
	}
}

func TestListByBuildEmptyIsJSONArray(t *testing.T) {
	h := metrics.NewHandler(metrics.NewMemoryStore())
	req := httptest.NewRequest("GET", "/api/v1/builds/42/agent-metrics", nil)
	req.Form = map[string][]string{":build_id": {"42"}}
	rec := httptest.NewRecorder()
	h.ListByBuild(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Fatalf("expected empty JSON array, got %q", body)
	}
}

func TestListByBuildRejectsInvalidBuildID(t *testing.T) {
	h := metrics.NewHandler(metrics.NewMemoryStore())
	for _, tc := range []struct{ name, id string }{
		{"missing", ""},
		{"non-numeric", "abc"},
		{"non-positive", "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/builds/x/agent-metrics", nil)
			req.Form = map[string][]string{":build_id": {tc.id}}
			rec := httptest.NewRecorder()
			h.ListByBuild(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for build_id %q, got %d", tc.id, rec.Code)
			}
		})
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

func TestListByWorkflowRunRejectsBadParams(t *testing.T) {
	h := metrics.NewHandler(metrics.NewMemoryStore())
	for _, tc := range []struct{ name, workflow, run string }{
		{"missing workflow name", "", "71"},
		{"invalid workflow name", "Bad Name!", "71"},
		{"missing run id", "code-review", ""},
		{"non-numeric run id", "code-review", "abc"},
		{"non-positive run id", "code-review", "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ListByWorkflowRun(rec, runMetricsRequest(tc.workflow, tc.run))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for %q/%q, got %d", tc.workflow, tc.run, rec.Code)
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
	h.ListByWorkflowRun(rec, runMetricsRequest("code-review", "7"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("list: expected 500 on store error, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/builds/9/agent-metrics", nil)
	req.Form = map[string][]string{":build_id": {"9"}}
	h.ListByBuild(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("list-by-build: expected 500 on store error, got %d", rec.Code)
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
func (errStore) ListByWorkflowRun(string, snapshot.WorkflowRunID) ([]schema.RunMetrics, error) {
	return nil, errors.New("boom")
}
func (errStore) ListRecent(int) ([]schema.RunMetrics, error) { return nil, errors.New("boom") }

func TestListCarriesDerivedOutcome(t *testing.T) {
	store := metrics.NewMemoryStore()
	h := metrics.NewHandler(store)

	// The exact truth split U3 kills: a green step inside a failed build.
	// Ingestion never accepts build_status, so seed the store directly the
	// way the DB factory materializes a read (builds-join sets BuildStatus).
	if err := store.Upsert(&schema.RunMetrics{
		BuildID: 41, PlanID: "p1", StepName: "implement",
		Status: schema.RunStatusOK, Summary: "agent reported ok",
		BuildStatus: "failed",
	}); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ListRecent(rec, httptest.NewRequest("GET", "/api/v1/agent/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, `"outcome":"failed"`) {
		t.Fatalf("expected the fused outcome on the wire, got %s", body)
	}

	var rows []schema.RunMetrics
	if err := json.NewDecoder(strings.NewReader(rec.Body.String())).Decode(&rows); err != nil {
		t.Fatalf("decode list body: %v", err)
	}
	if len(rows) != 1 || rows[0].Outcome != schema.RunOutcomeFailed {
		t.Fatalf("expected one row with outcome failed, got %+v", rows)
	}
}
