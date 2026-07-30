package commands

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/fly/rc/rcfakes"
	"github.com/concourse/concourse/go-concourse/concourse/concoursefakes"
)

func TestImportWorkflowDirResolvesExactReleasedReusableNodes(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, workflow.WorkflowFileName), []byte(`schema_version: 3
name: review-flow
signature_version: 1
inputs:
  - {name: base, type: repository/v1}
  - {name: candidate, type: repository/v1}
outputs:
  - {name: review, type: review/v1, from: review}
plan:
  - node: review-change
    uses: code-review@1
    input_mapping: {before: base, after: candidate}
    output_mapping: {review: review}
    params: {MINIMUM_SEVERITY: high}
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	nodeManifest := workflow.Manifest{workflow.NodeFileName: `schema_version: 1
name: code-review
inputs:
  - {name: before, type: repository/v1}
  - {name: after, type: repository/v1}
outputs:
  - {name: review, type: review/v1}
parameters:
  - {name: MINIMUM_SEVERITY, default: medium}
step:
  agent: review
  prompt: Review the immutable change.
  model: claude-sonnet
`}
	compiledNode, err := workflow.CompileNodeDefinition(nodeManifest)
	if err != nil {
		t.Fatalf("compile node fixture: %v", err)
	}
	node := workflow.NodeDefinition{
		ID: 17, Name: "code-review", Version: 1, ContentHash: nodeManifest.Hash(),
		Compiled: *compiledNode, SourceManifest: nodeManifest,
		Release: workflow.NodeRelease{
			ReleasedAt: 1, ReleasedBy: "releaser", Compatibility: workflow.ReleaseCompatible,
		},
	}

	var requests atomic.Int64
	target := agentWorkflowTarget(agentWorkflowRoundTripper(func(request *http.Request) (*http.Response, error) {
		switch call := requests.Add(1); call {
		case 1:
			if request.Method != http.MethodGet || request.URL.Path != "/api/v1/agent/nodes/code-review/versions/1" {
				t.Fatalf("node lookup = %s %s", request.Method, request.URL.Path)
			}
			return agentWorkflowJSONResponse(t, http.StatusOK, node), nil
		case 2:
			if request.Method != http.MethodPost || request.URL.Path != "/api/v1/agent/workflows/review-flow/versions" ||
				request.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("workflow import = %s %s type=%q", request.Method, request.URL.Path, request.Header.Get("Content-Type"))
			}
			var body struct {
				Files workflow.Manifest `json:"files"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatalf("decode workflow import: %v", err)
			}
			if !strings.Contains(body.Files[workflow.WorkflowFileName], "uses: code-review@1") {
				t.Fatalf("workflow import lost authored node reference: %#v", body.Files)
			}
			return agentWorkflowJSONResponse(t, http.StatusCreated, workflow.Definition{
				ID: 29, Name: "review-flow", Version: 1, ContentHash: body.Files.Hash(),
			}), nil
		default:
			t.Fatalf("unexpected HTTP request %d: %s %s", call, request.Method, request.URL.Path)
			return nil, nil
		}
	}))

	if err := importWorkflowDir(target, dir, false); err != nil {
		t.Fatalf("import node-aware workflow: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("HTTP requests = %d, want exact node lookup plus workflow import", got)
	}
}

func TestImportWorkflowDirDefersOnlyValidBrokerSelectorsToATC(t *testing.T) {
	dir := t.TempDir()
	source := `schema_version: 3
name: brokered-fly-workflow
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: consult
    function_id: consult
    prompt: consult
    broker_profiles:
      - tool: consult_agent
        tier: balanced
        effort: high
`
	if err := os.WriteFile(filepath.Join(dir, workflow.WorkflowFileName), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	target := agentWorkflowTarget(agentWorkflowRoundTripper(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		if request.Method != http.MethodPost || request.URL.Path != "/api/v1/agent/workflows/brokered-fly-workflow/versions" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		return agentWorkflowJSONResponse(t, http.StatusCreated, workflow.Definition{
			ID: 1, Name: "brokered-fly-workflow", Version: 1, ContentHash: "hash",
		}), nil
	}))
	if err := importWorkflowDir(target, dir, false); err != nil {
		t.Fatalf("valid broker selector should defer to ATC: %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want one authoritative import", requests.Load())
	}

	if err := os.WriteFile(
		filepath.Join(dir, workflow.WorkflowFileName),
		[]byte(strings.Replace(source, "tier: balanced", "tier: provider-model", 1)),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := importWorkflowDir(target, dir, false); err == nil || !strings.Contains(err.Error(), "tier must be economy") {
		t.Fatalf("invalid selector error = %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("invalid selector reached ATC; requests = %d", requests.Load())
	}
}

func TestImportWorkflowFileRejectsNonV3Locally(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-v1.yml")
	err := os.WriteFile(path, []byte(`schema_version: 1
name: legacy-v1
prompts:
  work: Do the work.
steps:
  - agent: work
    prompt: work
    outputs: [workspace]
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	var requests atomic.Int64
	err = importWorkflowFile(agentWorkflowTestTarget(&requests), path, false)
	assertUnsupportedWorkflowVersion(t, err, 1)
	if got := requests.Load(); got != 0 {
		t.Fatalf("HTTP requests = %d, want 0", got)
	}
}

func TestImportWorkflowDirRejectsNonV3Locally(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "workflow.yml"), []byte(`schema_version: 2
name: legacy-v2
trigger: {type: manual}
workspace: {type: git, repo: example/repo}
prompts:
  work: Do the work.
steps:
  - agent: work
    prompt: work
    outputs: [workspace]
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	var requests atomic.Int64
	err = importWorkflowDir(agentWorkflowTestTarget(&requests), dir, false)
	assertUnsupportedWorkflowVersion(t, err, 2)
	if got := requests.Load(); got != 0 {
		t.Fatalf("HTTP requests = %d, want 0", got)
	}
}

func TestImportWorkflowFileRejectsMissingAssetsLocally(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-asset.yml")
	err := os.WriteFile(path, []byte(`schema_version: 3
name: missing-asset
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: work
    function_id: work
    prompt_file: prompts/missing.md
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	var requests atomic.Int64
	err = importWorkflowFile(agentWorkflowTestTarget(&requests), path, false)
	const want = `prompt_file "prompts/missing.md": workflow: manifest file "prompts/missing.md" is not in the manifest`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want missing prompt asset", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("HTTP requests = %d, want 0", got)
	}
}

func assertUnsupportedWorkflowVersion(t *testing.T, err error, version int) {
	t.Helper()
	var unsupported workflow.UnsupportedSchemaVersionError
	if !errors.As(err, &unsupported) {
		t.Fatalf("error = %T %v, want UnsupportedSchemaVersionError", err, err)
	}
	if unsupported.Got != version {
		t.Fatalf("Got = %d, want %d", unsupported.Got, version)
	}
	want := "workflow: unsupported schema_version " + strconv.Itoa(version) + "; only schema_version 3 is supported"
	if unsupported.Error() != want {
		t.Fatalf("unsupported error = %q, want %q", unsupported.Error(), want)
	}
	if !strings.HasSuffix(err.Error(), want) {
		t.Fatalf("error = %q, want stable suffix %q", err, want)
	}
}

func agentWorkflowTestTarget(requests *atomic.Int64) rc.Target {
	return agentWorkflowTarget(agentWorkflowRoundTripper(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Status:     "500 Internal Server Error",
			Body:       io.NopCloser(strings.NewReader("unexpected request")),
			Header:     make(http.Header),
		}, nil
	}))
}

func agentWorkflowTarget(roundTripper agentWorkflowRoundTripper) rc.Target {
	client := new(concoursefakes.FakeClient)
	client.HTTPClientReturns(&http.Client{Transport: roundTripper})
	target := new(rcfakes.FakeTarget)
	target.URLReturns("http://agent.test")
	target.ClientReturns(client)
	return target
}

func agentWorkflowJSONResponse(t *testing.T, status int, value any) *http.Response {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{
		StatusCode: status,
		Status:     strconv.Itoa(status),
		Body:       io.NopCloser(strings.NewReader(string(payload))),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

type agentWorkflowRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip agentWorkflowRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}
