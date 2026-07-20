package workflows_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/workflows"
	schema "github.com/concourse/concourse/agent/schema"
	"github.com/concourse/concourse/agent/workflow"
)

const validYAML = `schema_version: 1
name: wf
description: handler test workflow
prompts:
  work: |
    Do the work.
steps:
- agent: work
  prompt: work
  outputs: [workspace]
`

type fakeStats struct {
	rows []schema.WorkflowVersionStats
	err  error
}

func (f fakeStats) WorkflowStats(string) ([]schema.WorkflowVersionStats, error) {
	return f.rows, f.err
}

func newHandler(t *testing.T) (*workflows.Handler, *workflow.MemoryStore) {
	t.Helper()
	store := workflow.NewMemoryStore()
	return workflows.NewHandler(store, fakeStats{}), store
}

func request(method, path string, params url.Values, body string) *http.Request {
	u := path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	if body != "" {
		return httptest.NewRequest(method, u, strings.NewReader(body))
	}
	return httptest.NewRequest(method, u, nil)
}

func TestImportCreatesAndIsIdempotent(t *testing.T) {
	h, _ := newHandler(t)

	w := httptest.NewRecorder()
	h.Import(w, request("POST", "/api/v1/agent/workflows/wf/versions",
		url.Values{":workflow_name": {"wf"}}, validYAML))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var def workflow.Definition
	if err := json.Unmarshal(w.Body.Bytes(), &def); err != nil {
		t.Fatal(err)
	}
	if def.Version != 1 || def.Name != "wf" || def.ContentHash == "" {
		t.Errorf("def = %+v", def)
	}

	w = httptest.NewRecorder()
	h.Import(w, request("POST", "/api/v1/agent/workflows/wf/versions",
		url.Values{":workflow_name": {"wf"}}, validYAML))
	if w.Code != http.StatusOK {
		t.Fatalf("idempotent re-import status = %d", w.Code)
	}
	json.Unmarshal(w.Body.Bytes(), &def)
	if def.Version != 1 {
		t.Errorf("re-import version = %d, want 1", def.Version)
	}
}

func TestImportRejectsBadDefinitions(t *testing.T) {
	h, _ := newHandler(t)

	w := httptest.NewRecorder()
	h.Import(w, request("POST", "/api/v1/agent/workflows/wf/versions",
		url.Values{":workflow_name": {"wf"}}, "schema_version: 1\nname: wf\nsteps: []\n"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid definition status = %d, want 400", w.Code)
	}

	w = httptest.NewRecorder()
	h.Import(w, request("POST", "/api/v1/agent/workflows/other/versions",
		url.Values{":workflow_name": {"other"}}, validYAML))
	if w.Code != http.StatusBadRequest {
		t.Errorf("name mismatch status = %d, want 400", w.Code)
	}

	w = httptest.NewRecorder()
	h.Import(w, request("POST", "/api/v1/agent/workflows/wf/versions",
		url.Values{":workflow_name": {"wf"}}, ""))
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty body status = %d, want 400", w.Code)
	}
}

func TestListShowsLiveVersion(t *testing.T) {
	h, store := newHandler(t)
	store.Import("wf", []byte(validYAML), "alice")
	store.Import("wf", []byte(strings.Replace(validYAML, "Do the work.", "Do it better.", 1)), "alice")
	store.Promote("wf", 1, "alice")

	w := httptest.NewRecorder()
	h.List(w, request("GET", "/api/v1/agent/workflows", nil, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var got []workflows.WorkflowSummary
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].LatestVersion != 2 || got[0].LiveVersion != 1 {
		t.Errorf("summaries = %+v", got)
	}
}

func TestVersionsAndGet(t *testing.T) {
	h, store := newHandler(t)
	store.Import("wf", []byte(validYAML), "alice")

	w := httptest.NewRecorder()
	h.Versions(w, request("GET", "/api/v1/agent/workflows/wf/versions",
		url.Values{":workflow_name": {"wf"}}, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("versions status = %d", w.Code)
	}

	w = httptest.NewRecorder()
	h.Versions(w, request("GET", "/api/v1/agent/workflows/nope/versions",
		url.Values{":workflow_name": {"nope"}}, ""))
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown name status = %d, want 404", w.Code)
	}

	w = httptest.NewRecorder()
	h.Get(w, request("GET", "/api/v1/agent/workflows/wf/versions/1",
		url.Values{":workflow_name": {"wf"}, ":version": {"1"}}, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("get status = %d", w.Code)
	}
	var def workflow.Definition
	json.Unmarshal(w.Body.Bytes(), &def)
	if def.RawYAML != validYAML {
		t.Errorf("get must include raw_yaml, got %q", def.RawYAML)
	}

	w = httptest.NewRecorder()
	h.Get(w, request("GET", "/api/v1/agent/workflows/wf/versions/9",
		url.Values{":workflow_name": {"wf"}, ":version": {"9"}}, ""))
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown version status = %d, want 404", w.Code)
	}

	w = httptest.NewRecorder()
	h.Get(w, request("GET", "/api/v1/agent/workflows/wf/versions/x",
		url.Values{":workflow_name": {"wf"}, ":version": {"x"}}, ""))
	if w.Code != http.StatusBadRequest {
		t.Errorf("non-integer version status = %d, want 400", w.Code)
	}
}

func TestPromote(t *testing.T) {
	h, store := newHandler(t)
	store.Import("wf", []byte(validYAML), "alice")

	w := httptest.NewRecorder()
	h.Promote(w, request("PUT", "/api/v1/agent/workflows/wf/versions/1/live",
		url.Values{":workflow_name": {"wf"}, ":version": {"1"}}, ""))
	if w.Code != http.StatusNoContent {
		t.Fatalf("promote status = %d body=%s", w.Code, w.Body.String())
	}
	live, found, _ := store.Live("wf")
	if !found || live.Version != 1 {
		t.Errorf("live = %+v found=%v", live, found)
	}

	w = httptest.NewRecorder()
	h.Promote(w, request("PUT", "/api/v1/agent/workflows/wf/versions/9/live",
		url.Values{":workflow_name": {"wf"}, ":version": {"9"}}, ""))
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown version status = %d, want 404", w.Code)
	}
}

func TestStatsReturnsDerivedRows(t *testing.T) {
	store := workflow.NewMemoryStore()
	v := 2
	h := workflows.NewHandler(store, fakeStats{rows: []schema.WorkflowVersionStats{
		{Version: &v, Runs: 4, SucceededRuns: 3, Tickets: 3, TotalCostUSD: 8, TotalTurns: 40},
	}})

	w := httptest.NewRecorder()
	h.Stats(w, request("GET", "/api/v1/agent/workflows/wf/stats",
		url.Values{":workflow_name": {"wf"}}, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var got []schema.WorkflowVersionStats
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SuccessRate != 0.75 || got[0].AvgTurns != 10 {
		t.Errorf("derived rows = %+v", got)
	}
}

func TestUpdateAnnotatesAndHides(t *testing.T) {
	h, store := newHandler(t)
	if _, err := store.Import("wf", []byte(validYAML), "importer"); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	h.Update(w, request("PUT", "/api/v1/agent/workflows/wf",
		url.Values{":workflow_name": {"wf"}}, `{"annotation":"note","hidden":true}`))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}

	defs, _ := store.List()
	if defs[0].Annotation != "note" || !defs[0].Hidden {
		t.Errorf("lifecycle not applied: %+v", defs[0])
	}
}

func TestUpdateUnknownWorkflowIs404(t *testing.T) {
	h, _ := newHandler(t)
	w := httptest.NewRecorder()
	h.Update(w, request("PUT", "/api/v1/agent/workflows/ghost",
		url.Values{":workflow_name": {"ghost"}}, `{"hidden":true}`))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestUpdateEmptyBodyIs400(t *testing.T) {
	h, store := newHandler(t)
	_, _ = store.Import("wf", []byte(validYAML), "importer")
	w := httptest.NewRecorder()
	h.Update(w, request("PUT", "/api/v1/agent/workflows/wf",
		url.Values{":workflow_name": {"wf"}}, `{}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func jsonRequest(path string, params url.Values, body string) *http.Request {
	r := request("POST", path, params, body)
	r.Header.Set("Content-Type", "application/json")
	return r
}

const manifestBody = `{"files": {
  "workflow.yml": "schema_version: 2\nname: wf\ndescription: manifest import\nskills: [tdd]\nprompt_files:\n  work: prompts/work.md\nsteps:\n- agent: work\n  prompt: work\n  outputs: [workspace]\n",
  "prompts/work.md": "Do the work.",
  "skills/tdd/SKILL.md": "# tdd"
}}`

func TestImportManifestBody(t *testing.T) {
	h, _ := newHandler(t)

	w := httptest.NewRecorder()
	h.Import(w, jsonRequest("/api/v1/agent/workflows/wf/versions",
		url.Values{":workflow_name": {"wf"}}, manifestBody))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var def workflow.Definition
	if err := json.Unmarshal(w.Body.Bytes(), &def); err != nil {
		t.Fatal(err)
	}
	if def.Version != 1 || def.Config.SkillFiles["skills/tdd/SKILL.md"] == "" {
		t.Fatalf("manifest not compiled: %+v", def)
	}
}

func TestImportManifestBodyRejections(t *testing.T) {
	h, _ := newHandler(t)

	cases := map[string]struct {
		body string
		code int
	}{
		"malformed json":     {"{not json", http.StatusBadRequest},
		"empty files":        {`{"files": {}}`, http.StatusBadRequest},
		"missing skill file": {`{"files": {"workflow.yml": "schema_version: 2\nname: wf\nskills: [ghost]\nprompts:\n  work: w\nsteps:\n- agent: work\n  prompt: work\n  outputs: [workspace]\n"}}`, http.StatusBadRequest},
	}
	for name, tc := range cases {
		w := httptest.NewRecorder()
		h.Import(w, jsonRequest("/api/v1/agent/workflows/wf/versions",
			url.Values{":workflow_name": {"wf"}}, tc.body))
		if w.Code != tc.code {
			t.Errorf("%s: expected %d, got %d: %s", name, tc.code, w.Code, w.Body.String())
		}
	}
}

func TestImportRawYAMLStillWorks(t *testing.T) {
	h, _ := newHandler(t)
	w := httptest.NewRecorder()
	h.Import(w, request("POST", "/api/v1/agent/workflows/wf/versions",
		url.Values{":workflow_name": {"wf"}}, validYAML))
	if w.Code != http.StatusOK {
		t.Fatalf("raw path regressed: %d %s", w.Code, w.Body.String())
	}
}
