package commands

import (
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
	client := new(concoursefakes.FakeClient)
	client.HTTPClientReturns(&http.Client{Transport: agentWorkflowRoundTripper(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Status:     "500 Internal Server Error",
			Body:       io.NopCloser(strings.NewReader("unexpected request")),
			Header:     make(http.Header),
		}, nil
	})})
	target := new(rcfakes.FakeTarget)
	target.URLReturns("http://agent.test")
	target.ClientReturns(client)
	return target
}

type agentWorkflowRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip agentWorkflowRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}
