package workflows_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/workflows"
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

func newHandler(t *testing.T) (*workflows.Handler, *workflow.MemoryStore) {
	t.Helper()
	store := workflow.NewMemoryStore()
	return workflows.NewHandler(store), store
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
