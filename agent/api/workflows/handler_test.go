package workflows_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/workflows"
	schema "github.com/concourse/concourse/agent/schema"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflow/workflowtest"
	"github.com/concourse/concourse/agent/workflowrun"
)

const validYAML = `schema_version: 3
name: wf
description: handler test workflow
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: work
    function_id: work
    prompt: Do the work.
`

type fakeStats struct {
	rows []schema.WorkflowVersionStats
	err  error
}

func (f fakeStats) WorkflowStats(string) ([]schema.WorkflowVersionStats, error) {
	return f.rows, f.err
}

func newHandler(t *testing.T) (*workflows.Handler, *workflowtest.MemoryStore) {
	t.Helper()
	store := workflowtest.NewMemoryStore(workflowrun.WorkflowTargetRenderer{
		RuntimeImage: "registry.example/agent-runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	return workflows.NewHandler(store, fakeStats{}), store
}

type promotionErrorStore struct {
	workflow.Store
	err error
}

func (store promotionErrorStore) Promote(string, int, string) (workflow.PromotionResult, error) {
	return workflow.PromotionResult{}, store.err
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
	if def.Version != 1 || def.Name != "wf" || def.ContentHash == "" || def.SchemaVersion != 3 || def.SignatureVersion != 1 ||
		def.Compiled.Function == nil {
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
		url.Values{":workflow_name": {"wf"}}, "schema_version: 3\nname: wf\nsignature_version: 1\ninputs: []\noutputs: []\nplan: []\n"))
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

func TestImportNonV3ReturnsUnsupportedSchemaVersion(t *testing.T) {
	h, _ := newHandler(t)
	const want = "workflow: unsupported schema_version 1; only schema_version 3 is supported"

	t.Run("raw", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.Import(w, request("POST", "/api/v1/agent/workflows/route-name/versions",
			url.Values{":workflow_name": {"route-name"}}, `schema_version: 1
name: source-name
steps: []
`))
		if w.Code != http.StatusUnprocessableEntity || strings.TrimSpace(w.Body.String()) != want {
			t.Fatalf("status/body = %d/%q, want 422/%q", w.Code, strings.TrimSpace(w.Body.String()), want)
		}
	})

	t.Run("manifest", func(t *testing.T) {
		body, err := json.Marshal(map[string]any{
			"files": workflow.Manifest{
				"workflow.yml": `schema_version: 2
name: manifest-v2
prompt_files:
  work: prompts/missing.md
steps:
  - agent: work
    prompt: work
    outputs: [workspace]
`,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		h.Import(w, jsonRequest("/api/v1/agent/workflows/manifest-v2/versions",
			url.Values{":workflow_name": {"manifest-v2"}}, string(body)))
		const manifestWant = "workflow: unsupported schema_version 2; only schema_version 3 is supported"
		if w.Code != http.StatusUnprocessableEntity || strings.TrimSpace(w.Body.String()) != manifestWant {
			t.Fatalf("status/body = %d/%q, want 422/%q", w.Code, strings.TrimSpace(w.Body.String()), manifestWant)
		}
	})
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
	if got[0].SchemaVersion != 3 || got[0].SignatureVersion != 1 {
		t.Errorf("summary metadata = %+v", got[0])
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

func TestVersionsUsesAStableHardBoundedCursor(t *testing.T) {
	h, store := newHandler(t)
	for _, prompt := range []string{"One.", "Two.", "Three."} {
		raw := strings.Replace(validYAML, "Do the work.", prompt, 1)
		if _, err := store.Import("wf", []byte(raw), "alice"); err != nil {
			t.Fatal(err)
		}
	}

	w := httptest.NewRecorder()
	h.Versions(w, request("GET", "/api/v1/agent/workflows/wf/versions",
		url.Values{":workflow_name": {"wf"}, "limit": {"2"}}, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("first page status/body = %d/%s", w.Code, w.Body.String())
	}
	var first []workflow.Definition
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].Version != 2 || first[1].Version != 3 {
		t.Fatalf("first page versions = %+v, want [2,3]", first)
	}
	if got := w.Header().Get("X-Next-Cursor"); got != "2" {
		t.Fatalf("next cursor = %q, want 2", got)
	}
	if link := w.Header().Get("Link"); !strings.Contains(link, "cursor=2") ||
		!strings.Contains(link, "limit=2") || !strings.Contains(link, `rel="next"`) {
		t.Fatalf("next Link = %q", link)
	}

	w = httptest.NewRecorder()
	h.Versions(w, request("GET", "/api/v1/agent/workflows/wf/versions",
		url.Values{":workflow_name": {"wf"}, "limit": {"2"}, "cursor": {"2"}}, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("second page status/body = %d/%s", w.Code, w.Body.String())
	}
	var second []workflow.Definition
	if err := json.Unmarshal(w.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].Version != 1 {
		t.Fatalf("second page versions = %+v, want [1]", second)
	}
	if got := w.Header().Get("X-Next-Cursor"); got != "" {
		t.Fatalf("terminal next cursor = %q", got)
	}
}

func TestVersionsRejectsInvalidPaginationBeforeReadingTheStore(t *testing.T) {
	h, _ := newHandler(t)
	for name, values := range map[string]url.Values{
		"zero limit":   {":workflow_name": {"wf"}, "limit": {"0"}},
		"large limit":  {":workflow_name": {"wf"}, "limit": {"101"}},
		"bad limit":    {":workflow_name": {"wf"}, "limit": {"many"}},
		"zero cursor":  {":workflow_name": {"wf"}, "cursor": {"0"}},
		"bad cursor":   {":workflow_name": {"wf"}, "cursor": {"old"}},
		"large cursor": {":workflow_name": {"wf"}, "cursor": {"2147483648"}},
		"multi cursor": {":workflow_name": {"wf"}, "cursor": {"2", "3"}},
	} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.Versions(w, request("GET", "/api/v1/agent/workflows/wf/versions", values, ""))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status/body = %d/%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestVersionsHonorsRequestCancellation(t *testing.T) {
	h, store := newHandler(t)
	if _, err := store.Import("wf", []byte(validYAML), "alice"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := request("GET", "/api/v1/agent/workflows/wf/versions",
		url.Values{":workflow_name": {"wf"}}, "").WithContext(ctx)
	w := httptest.NewRecorder()

	h.Versions(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status/body = %d/%s", w.Code, w.Body.String())
	}
}

func TestPromote(t *testing.T) {
	h, store := newHandler(t)
	store.Import("wf", []byte(validYAML), "alice")

	w := httptest.NewRecorder()
	h.Promote(w, request("PUT", "/api/v1/agent/workflows/wf/versions/1/live",
		url.Values{":workflow_name": {"wf"}, ":version": {"1"}}, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("promote status = %d body=%s", w.Code, w.Body.String())
	}
	var result workflow.PromotionResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.PreviousLive != nil || result.Target.Version != 1 || result.SignatureChanged {
		t.Fatalf("promotion result = %+v", result)
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

func TestPromoteUnsupportedSchemaVersionReturns422(t *testing.T) {
	const want = "workflow: unsupported schema_version 2; only schema_version 3 is supported"
	h := workflows.NewHandler(promotionErrorStore{
		err: workflow.InvalidPromotionError{
			Err: workflow.UnsupportedSchemaVersionError{Got: 2},
		},
	}, fakeStats{})
	w := httptest.NewRecorder()
	h.Promote(w, request("PUT", "/api/v1/agent/workflows/legacy/versions/2/live",
		url.Values{":workflow_name": {"legacy"}, ":version": {"2"}}, ""))
	if w.Code != http.StatusUnprocessableEntity || strings.TrimSpace(w.Body.String()) != want {
		t.Fatalf("status/body = %d/%q, want 422/%q", w.Code, strings.TrimSpace(w.Body.String()), want)
	}
}

func TestPromoteRejectsSchemaV3WithoutDigestPinnedTrustedRuntime(t *testing.T) {
	store := workflowtest.NewMemoryStore(workflowrun.WorkflowTargetRenderer{
		RuntimeImage: "registry.example/agent-runner:mutable",
	})
	h := workflows.NewHandler(store, fakeStats{})
	definition, err := store.ImportManifest("wf-v3", workflow.Manifest{"workflow.yml": `schema_version: 3
name: wf-v3
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: work
    function_id: work
    prompt: do the work
`}, "alice")
	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	h.Promote(w, request("PUT", "/api/v1/agent/workflows/wf-v3/versions/1/live",
		url.Values{":workflow_name": {"wf-v3"}, ":version": {strconv.Itoa(definition.Version)}}, ""))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "exact sha256 digest") {
		t.Fatalf("body = %q, want trusted runtime digest error", w.Body.String())
	}
	if _, found, liveErr := store.Live("wf-v3"); liveErr != nil || found {
		t.Fatalf("rejected version became live: found=%v err=%v", found, liveErr)
	}
}

func TestStatsReturnsDerivedRows(t *testing.T) {
	store := workflowtest.NewMemoryStore(workflowrun.WorkflowTargetRenderer{
		RuntimeImage: "registry.example/agent-runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	v := 2
	h := workflows.NewHandler(store, fakeStats{rows: []schema.WorkflowVersionStats{
		{Version: &v, Runs: 4, SucceededRuns: 3, WorkflowRuns: 3, TotalCostUSD: 8, TotalTurns: 40},
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
  "workflow.yml": "schema_version: 3\nname: wf\ndescription: manifest import\nsignature_version: 1\ninputs: []\noutputs: []\nplan:\n  - agent: work\n    function_id: work\n    prompt_file: prompts/work.md\n    skills: [tdd]\n",
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
	if def.Version != 1 || def.Compiled.Function == nil ||
		def.Compiled.Function.SkillFiles["skills/tdd/SKILL.md"] == "" {
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
		"missing skill file": {`{"files": {"workflow.yml": "schema_version: 3\nname: wf\nsignature_version: 1\ninputs: []\noutputs: []\nplan:\n  - agent: work\n    function_id: work\n    prompt: w\n    skills: [ghost]\n"}}`, http.StatusBadRequest},
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
