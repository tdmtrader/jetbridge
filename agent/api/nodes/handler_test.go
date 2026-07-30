package nodes_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/agent/api/nodes"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflow/workflowtest"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/accessor/accessorfakes"
	"github.com/concourse/concourse/atc/auditor/auditorfakes"
)

const nodeSource = `schema_version: 1
name: code-review
description: Review one repository change
inputs: []
outputs: []
step:
  task: review
  config:
    platform: linux
    image_resource:
      type: registry-image
      source: {repository: busybox, digest: sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}
    run: {path: sh, args: ["-c", "true"]}
`

func nodeRequest(method, path string, params url.Values, body string) *http.Request {
	u := path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	return httptest.NewRequest(method, u, strings.NewReader(body))
}

func jsonNodeRequest(path string, params url.Values, body string) *http.Request {
	r := nodeRequest(http.MethodPost, path, params, body)
	r.Header.Set("Content-Type", "application/json")
	return r
}

func newHandler() (*nodes.Handler, *workflowtest.MemoryNodeStore) {
	store := workflowtest.NewMemoryNodeStore()
	return nodes.NewHandler(store), store
}

func nodeManifest(source string) string {
	body, _ := json.Marshal(map[string]workflow.Manifest{"files": {workflow.NodeFileName: source}})
	return string(body)
}

func importNode(t *testing.T, h *nodes.Handler, source string) workflow.NodeDefinition {
	t.Helper()
	w := httptest.NewRecorder()
	h.Import(w, jsonNodeRequest("/api/v1/agent/nodes/code-review/versions", url.Values{":node_name": {"code-review"}}, nodeManifest(source)))
	if w.Code != http.StatusOK {
		t.Fatalf("import status/body = %d/%s", w.Code, w.Body.String())
	}
	var definition workflow.NodeDefinition
	if err := json.Unmarshal(w.Body.Bytes(), &definition); err != nil {
		t.Fatal(err)
	}
	return definition
}

func TestImportRequiresStrictManifestAndReturnsStoredNode(t *testing.T) {
	h, _ := newHandler()

	for name, request := range map[string]*http.Request{
		"raw yaml":      nodeRequest(http.MethodPost, "/api/v1/agent/nodes/code-review/versions", url.Values{":node_name": {"code-review"}}, nodeSource),
		"unknown field": jsonNodeRequest("/api/v1/agent/nodes/code-review/versions", url.Values{":node_name": {"code-review"}}, `{"files":{"node.yaml":"x"},"extra":true}`),
		"empty files":   jsonNodeRequest("/api/v1/agent/nodes/code-review/versions", url.Values{":node_name": {"code-review"}}, `{"files":{}}`),
	} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.Import(w, request)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status/body = %d/%s, want 400", w.Code, w.Body.String())
			}
		})
	}

	definition := importNode(t, h, nodeSource)
	if definition.Name != "code-review" || definition.Version != 1 || definition.Compiled.Name != "code-review" || definition.SourceManifest[workflow.NodeFileName] != nodeSource {
		t.Fatalf("stored node = %+v", definition)
	}
}

func TestImportUsesPreferredUsernameThenNameForAudit(t *testing.T) {
	h, _ := newHandler()
	serve := func(claims accessor.Claims, name, source string) workflow.NodeDefinition {
		t.Helper()
		factory := new(accessorfakes.FakeAccessFactory)
		access := new(accessorfakes.FakeAccess)
		access.ClaimsReturns(claims)
		factory.CreateReturns(access, nil)
		wrapped := accessor.NewHandler(
			lagertest.NewTestLogger("node-import"), "node-import", http.HandlerFunc(h.Import),
			factory, new(auditorfakes.FakeAuditor), map[string]string{},
		)
		w := httptest.NewRecorder()
		wrapped.ServeHTTP(w, jsonNodeRequest("/api/v1/agent/nodes/"+name+"/versions", url.Values{":node_name": {name}}, nodeManifest(source)))
		if w.Code != http.StatusOK {
			t.Fatalf("import status/body = %d/%s", w.Code, w.Body.String())
		}
		var definition workflow.NodeDefinition
		if err := json.Unmarshal(w.Body.Bytes(), &definition); err != nil {
			t.Fatal(err)
		}
		return definition
	}

	if got := serve(accessor.Claims{PreferredUsername: "preferred", UserName: "fallback"}, "code-review", nodeSource).CreatedBy; got != "preferred" {
		t.Fatalf("created_by = %q, want preferred username", got)
	}
	if got := serve(accessor.Claims{UserName: "fallback"}, "lint", strings.Replace(nodeSource, "name: code-review", "name: lint", 1)).CreatedBy; got != "fallback" {
		t.Fatalf("created_by = %q, want user name fallback", got)
	}
}

func TestNodeCatalogVersionsAndGet(t *testing.T) {
	h, _ := newHandler()
	importNode(t, h, nodeSource)
	importNode(t, h, strings.Replace(nodeSource, "Review one repository change", "Review it better", 1))

	w := httptest.NewRecorder()
	h.List(w, nodeRequest(http.MethodGet, "/api/v1/agent/nodes", nil, ""))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"latest_version":2`) {
		t.Fatalf("list status/body = %d/%s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	h.Versions(w, nodeRequest(http.MethodGet, "/api/v1/agent/nodes/code-review/versions", url.Values{":node_name": {"code-review"}, "limit": {"1"}}, ""))
	if w.Code != http.StatusOK || w.Header().Get("X-Next-Cursor") != "2" {
		t.Fatalf("versions status/cursor/body = %d/%q/%s", w.Code, w.Header().Get("X-Next-Cursor"), w.Body.String())
	}

	w = httptest.NewRecorder()
	h.Get(w, nodeRequest(http.MethodGet, "/api/v1/agent/nodes/code-review/versions/1", url.Values{":node_name": {"code-review"}, ":version": {"1"}}, ""))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"version":1`) {
		t.Fatalf("get status/body = %d/%s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	h.Get(w, nodeRequest(http.MethodGet, "/api/v1/agent/nodes/code-review/versions/99", url.Values{":node_name": {"code-review"}, ":version": {"99"}}, ""))
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing get status = %d, want 404", w.Code)
	}
}

func TestReleaseAndDeprecationReturnCompleteAudit(t *testing.T) {
	h, _ := newHandler()
	importNode(t, h, nodeSource)

	w := httptest.NewRecorder()
	r := nodeRequest(http.MethodPut, "/api/v1/agent/nodes/code-review/versions/1/release", url.Values{":node_name": {"code-review"}, ":version": {"1"}}, `{"compatibility":"breaking"}`)
	r.Header.Set("Content-Type", "application/json")
	h.Release(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"compatibility":"breaking"`) || !strings.Contains(w.Body.String(), `"released_at":`) {
		t.Fatalf("release status/body = %d/%s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	r = nodeRequest(http.MethodPut, "/api/v1/agent/nodes/code-review/versions/1/deprecation", url.Values{":node_name": {"code-review"}, ":version": {"1"}}, `{"deprecated":true}`)
	r.Header.Set("Content-Type", "application/json")
	h.Deprecate(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"deprecated_at":`) || !strings.Contains(w.Body.String(), `"released_at":`) {
		t.Fatalf("deprecate status/body = %d/%s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	r = nodeRequest(http.MethodPut, "/api/v1/agent/nodes/code-review/versions/1/deprecation", url.Values{":node_name": {"code-review"}, ":version": {"1"}}, `{"deprecated":false}`)
	r.Header.Set("Content-Type", "application/json")
	h.Deprecate(w, r)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"deprecated_at":`) || !strings.Contains(w.Body.String(), `"released_at":`) {
		t.Fatalf("restore status/body = %d/%s", w.Code, w.Body.String())
	}
}

func TestReleaseRejectsInvalidAndIncompatibleVersions(t *testing.T) {
	h, _ := newHandler()
	importNode(t, h, nodeSource)

	w := httptest.NewRecorder()
	r := nodeRequest(http.MethodPut, "/api/v1/agent/nodes/code-review/versions/1/release", url.Values{":node_name": {"code-review"}, ":version": {"1"}}, `{"compatibility":"breaking"}`)
	r.Header.Set("Content-Type", "application/json")
	h.Release(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("initial release status/body = %d/%s", w.Code, w.Body.String())
	}
	importNode(t, h, strings.Replace(nodeSource, "inputs: []", "inputs: []\nparameters:\n  - {name: REQUIRED}", 1))

	for name, body := range map[string]string{
		"invalid compatibility": `{"compatibility":"unsafe"}`,
		"false compatible":      `{"compatibility":"compatible"}`,
	} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := nodeRequest(http.MethodPut, "/api/v1/agent/nodes/code-review/versions/2/release", url.Values{":node_name": {"code-review"}, ":version": {"2"}}, body)
			r.Header.Set("Content-Type", "application/json")
			h.Release(w, r)
			if w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status/body = %d/%s, want 422", w.Code, w.Body.String())
			}
		})
	}
}
