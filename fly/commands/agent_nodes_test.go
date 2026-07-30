package commands

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/fly/rc/rcfakes"
	"github.com/concourse/concourse/go-concourse/concourse/concoursefakes"
)

const agentNodeSource = `schema_version: 1
name: code-review
description: Review one repository change
inputs: []
outputs: []
step:
  agent: review
  prompt_file: prompts/review.md
  model: claude-sonnet
`

func TestAgentNodesImportPackagesDirectoryAndSendsManifest(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, workflow.NodeFileName), []byte(agentNodeSource), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "prompts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "review.md"), []byte("review it"), 0o600); err != nil {
		t.Fatal(err)
	}

	target := nodeTarget(t, func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/v1/agent/nodes/code-review/versions" || request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("request = %s %s content-type=%q", request.Method, request.URL.Path, request.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(request.Body)
		if !strings.Contains(string(body), `"node.yaml"`) || !strings.Contains(string(body), `"prompts/review.md"`) {
			t.Fatalf("manifest payload = %s", body)
		}
		return nodeResponse(http.StatusOK, `{"name":"code-review","version":1,"content_hash":"abcdef012345"}`), nil
	})
	if err := importNodeDir(target, dir); err != nil {
		t.Fatal(err)
	}
}

func TestAgentNodesReleaseAndDeprecationUseExactLifecycleRequests(t *testing.T) {
	var calls atomic.Int64
	target := nodeTarget(t, func(request *http.Request) (*http.Response, error) {
		switch calls.Add(1) {
		case 1:
			if request.Method != http.MethodPut || request.URL.Path != "/api/v1/agent/nodes/code-review/versions/3/release" {
				t.Fatalf("release request = %s %s", request.Method, request.URL.Path)
			}
			body, _ := io.ReadAll(request.Body)
			if string(body) != `{"compatibility":"breaking"}` {
				t.Fatalf("release payload = %s", body)
			}
			return nodeResponse(http.StatusOK, `{"released_at":1,"released_by":"alice","compatibility":"breaking"}`), nil
		case 2:
			if request.Method != http.MethodPut || request.URL.Path != "/api/v1/agent/nodes/code-review/versions/3/deprecation" {
				t.Fatalf("deprecate request = %s %s", request.Method, request.URL.Path)
			}
			body, _ := io.ReadAll(request.Body)
			if string(body) != `{"deprecated":true}` {
				t.Fatalf("deprecate payload = %s", body)
			}
			return nodeResponse(http.StatusOK, `{}`), nil
		default:
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
			return nil, nil
		}
	})
	if err := releaseNodeVersion(target, "code-review", 3, workflow.ReleaseBreaking); err != nil {
		t.Fatal(err)
	}
	if err := setNodeDeprecation(target, "code-review", 3, true); err != nil {
		t.Fatal(err)
	}
}

func TestAgentNodesDefaultVersionPrefersLatestReleasedThenImported(t *testing.T) {
	target := nodeTarget(t, func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/api/v1/agent/nodes/code-review/versions" || request.URL.Query().Get("limit") != "100" {
			t.Fatalf("version request = %s", request.URL.String())
		}
		return nodeResponse(http.StatusOK, `[{"version":2,"release":{"released_at":2}},{"version":3}]`), nil
	})
	version, err := resolveDefaultNodeVersion(target, "code-review")
	if err != nil || version != 2 {
		t.Fatalf("version/error = %d/%v, want 2/nil", version, err)
	}
}

func TestAgentNodesRunUsesExactVersionAndOnlyPublicBodyFields(t *testing.T) {
	target := nodeTarget(t, func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/v1/agent/nodes/code-review/versions/5/runs" || request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("request = %s %s content-type=%q", request.Method, request.URL.Path, request.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(request.Body)
		if string(body) != `{"inputs":{"repository":"41"},"params":{"MINIMUM_SEVERITY":"high"},"idempotency_key":"node-test-1"}` {
			t.Fatalf("payload = %s", body)
		}
		return nodeResponse(http.StatusCreated, `{"workflow_run_id":"71","workflow_name":"code-review","workflow_version":5,"schema_version":1,"signature_version":1,"definition_content_hash":"hash","status":"running","origin_kind":"manual","created_by":"alice","created_at":"2026-07-29T12:00:00Z","updated_at":"2026-07-29T12:00:00Z","parameterized_config_hash":"params","inputs":[],"outputs":[]}`), nil
	})
	if _, err := runNodeVersion(target, "code-review", 5, []string{"repository=41"}, []string{"MINIMUM_SEVERITY=high"}, "node-test-1"); err != nil {
		t.Fatal(err)
	}
}

func TestAgentNodeConsumersUsesExactVersionAndOpaqueCursor(t *testing.T) {
	cursor := "eyJ3b3JrZmxvd19kZWZpbml0aW9uX2lkIjo5MDA3MTk5MjU0NzQwOTkzLCJpbnN0YW5jZV9uYW1lIjoicmV2aWV3In0"
	target := nodeTarget(t, func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1/agent/nodes/code-review/versions/5/consumers" ||
			request.URL.Query().Get("limit") != "25" || request.URL.Query().Get("cursor") != cursor {
			t.Fatalf("request = %s %s", request.Method, request.URL.String())
		}
		response := nodeResponse(http.StatusOK, `[{"workflow_definition_id":"9007199254740993","workflow_name":"small-fix","workflow_version":7,"live":true,"binding":{"instance_name":"review","node_definition_id":"9007199254740995","node_name":"code-review","node_version":5,"node_content_hash":"hash","input_mapping":{},"output_mapping":{},"parameters":{}}}]`)
		response.Header.Set("X-Next-Cursor", cursor)
		return response, nil
	})
	consumers, next, err := listNodeConsumers(target, "code-review", 5, 25, cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(consumers) != 1 ||
		consumers[0].WorkflowDefinitionID != 9007199254740993 ||
		consumers[0].Binding.NodeDefinitionID != 9007199254740995 ||
		next != cursor {
		t.Fatalf("consumers/next = %#v/%q", consumers, next)
	}
}

func TestAgentNodeUpgradeSendsOnlyDistinctSelectedWorkflows(t *testing.T) {
	target := nodeTarget(t, func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/v1/agent/nodes/code-review/versions/5/upgrades" ||
			request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("request = %s %s content-type=%q", request.Method, request.URL.Path, request.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(request.Body)
		if string(body) != `{"workflows":["small-fix","version-upgrade"]}` {
			t.Fatalf("payload = %s", body)
		}
		return nodeResponse(http.StatusOK, `{"node_name":"code-review","version":5,"workflows":[{"workflow":"small-fix","old_version":7,"new_version":8,"status":"created"},{"workflow":"version-upgrade","old_version":3,"new_version":0,"status":"recomposition_required","obligations":{"inputs":{"added":[],"removed":[],"changed":[]},"outputs":{"added":[],"removed":[],"changed":[]},"parameters":{"added":[],"removed":[],"changed":[]}}}]}`), nil
	})
	result, err := upgradeNodeConsumers(target, "code-review", 5, []string{"small-fix", "version-upgrade"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Workflows) != 2 || result.Workflows[0].Workflow != "small-fix" {
		t.Fatalf("result = %#v", result)
	}
}

func TestAgentNodeUpgradeRejectsDuplicateSelectionBeforeRequest(t *testing.T) {
	calls := 0
	target := nodeTarget(t, func(request *http.Request) (*http.Response, error) {
		calls++
		return nodeResponse(http.StatusInternalServerError, `{}`), nil
	})
	if _, err := upgradeNodeConsumers(target, "code-review", 5, []string{"small-fix", "small-fix"}); err == nil {
		t.Fatal("duplicate workflow selection was accepted")
	}
	if calls != 0 {
		t.Fatalf("HTTP calls = %d, want 0", calls)
	}
}

func nodeTarget(t *testing.T, handler func(*http.Request) (*http.Response, error)) rc.Target {
	t.Helper()
	client := new(concoursefakes.FakeClient)
	client.HTTPClientReturns(&http.Client{Transport: nodeRoundTripper(handler)})
	target := new(rcfakes.FakeTarget)
	target.URLReturns("http://agent.test")
	target.ClientReturns(client)
	return target
}

func nodeResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

type nodeRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip nodeRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}
