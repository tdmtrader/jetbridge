package devmcp_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/concourse/ci-agent/devmcp"
)

// TestCoreKeepsTheMCPExecutionContract catches a refactor that changes the
// retained MCP server's command semantics while moving them behind Core.
func TestCoreKeepsTheMCPExecutionContract(t *testing.T) {
	workdir := t.TempDir()
	config := devmcp.Config{
		SchemaVersion: 1,
		Repo: &devmcp.ToolCommands{
			Build: &devmcp.CommandSpec{Cmd: []string{"sh", "-c", "printf 'repo stdout\\n'; printf 'repo stderr\\n' >&2; pwd"}},
		},
		Components: []devmcp.ComponentConfig{
			{
				ID: "app", Description: "application", Paths: []string{"app/"}, Kind: "service",
				Build: &devmcp.CommandSpec{Cmd: []string{"sh", "-c", "printf 'component stdout\\n'; printf 'component stderr\\n' >&2; pwd"}},
				Test:  &devmcp.CommandSpec{Cmd: []string{"sh", "-c", "printf '%s\\n' \"$@\"", "test-command"}, FocusFlag: "--focus"},
			},
		},
	}

	core, err := devmcp.NewCore(config, workdir)
	if err != nil {
		t.Fatalf("new core: %v", err)
	}
	// Changing the caller's config after construction must not replace a
	// configured binary or its arguments inside the immutable execution core.
	config.Repo.Build.Cmd = []string{"sh", "-c", "printf 'candidate replacement'"}

	result, err := core.Execute(context.Background(), devmcp.Request{Operation: devmcp.OperationBuild}, nil)
	if err != nil {
		t.Fatalf("execute repo build: %v", err)
	}
	if result.Status != devmcp.StatusOK {
		t.Fatalf("status = %q, want ok; summary=%s", result.Status, result.Summary)
	}
	for _, want := range []string{"repo stdout", "repo stderr", workdir} {
		if !strings.Contains(result.OutputTail, want) {
			t.Fatalf("output tail %q does not contain %q", result.OutputTail, want)
		}
	}
	if !strings.HasPrefix(result.LogPath, filepath.Join(".dev-mcp", "logs")+string(filepath.Separator)) {
		t.Fatalf("log path = %q, want retained MCP log location", result.LogPath)
	}

	focused, err := core.Execute(context.Background(), devmcp.Request{
		Operation: devmcp.OperationTest,
		Component: "app",
		Focus:     "OnlyThis",
	}, nil)
	if err != nil {
		t.Fatalf("execute focused test: %v", err)
	}
	if focused.Status != devmcp.StatusOK || !strings.Contains(focused.OutputTail, "--focus=OnlyThis") {
		t.Fatalf("focused result = %#v", focused)
	}
}

// TestCorePreservesWholeRepoErrorAndCancellation catches changes to the
// current malformed whole-repo error or a timeout being surfaced as success.
func TestCorePreservesWholeRepoErrorAndCancellation(t *testing.T) {
	core, err := devmcp.NewCore(devmcp.Config{
		SchemaVersion: 1,
		Components: []devmcp.ComponentConfig{{
			ID: "app", Description: "application", Paths: []string{"app/"}, Kind: "service",
			Build: &devmcp.CommandSpec{Cmd: []string{"sh", "-c", "sleep 1"}},
		}},
	}, t.TempDir())
	if err != nil {
		t.Fatalf("new core: %v", err)
	}

	_, err = core.Execute(context.Background(), devmcp.Request{Operation: devmcp.OperationLint}, nil)
	const wantWholeRepoError = "whole-repo lint is not configured (no repo: section in dev-mcp.yml); pass a component"
	if err == nil || err.Error() != wantWholeRepoError {
		t.Fatalf("whole-repo error = %v, want %q", err, wantWholeRepoError)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	result, err := core.Execute(ctx, devmcp.Request{Operation: devmcp.OperationBuild, Component: "app"}, nil)
	if err != nil {
		t.Fatalf("timeout must be an execution result, got transport error: %v", err)
	}
	if result.Status != devmcp.StatusError {
		t.Fatalf("timeout status = %q, want %q; result=%#v", result.Status, devmcp.StatusError, result)
	}
}

func TestCoreMapsAffectedComponentsDeterministically(t *testing.T) {
	core, err := devmcp.NewCore(devmcp.Config{
		SchemaVersion: 1,
		Components: []devmcp.ComponentConfig{
			{ID: "zeta", Description: "z", Paths: []string{"zeta/"}, Kind: "library"},
			{ID: "alpha", Description: "a", Paths: []string{"alpha/"}, Kind: "service"},
		},
	}, t.TempDir())
	if err != nil {
		t.Fatalf("new core: %v", err)
	}
	got, err := core.AffectedComponents(context.Background(), []string{"zeta/code.go", "alpha/code.go", "LICENSE"})
	if err != nil {
		t.Fatalf("map paths: %v", err)
	}
	if strings.Join(got.Components, ",") != "alpha,zeta" || strings.Join(got.UnmappedPaths, ",") != "LICENSE" {
		t.Fatalf("affected = %#v", got)
	}
}

// TestMCPAdapterUsesTheSameCore catches a regression where the retained wire
// surface resolves commands independently from deterministic validation.
func TestMCPAdapterUsesTheSameCore(t *testing.T) {
	config := devmcp.Config{
		SchemaVersion: 1,
		Components: []devmcp.ComponentConfig{{
			ID: "app", Description: "application", Paths: []string{"app/"}, Kind: "service",
			Build: &devmcp.CommandSpec{Cmd: []string{"sh", "-c", "printf 'core-adapter\\n'"}},
		}},
	}
	core, err := devmcp.NewCore(config, t.TempDir())
	if err != nil {
		t.Fatalf("new core: %v", err)
	}
	server := devmcp.NewServer(0)
	devmcp.RegisterTools(server, core)
	ts := httptest.NewServer(server)
	defer ts.Close()

	request, err := http.NewRequest(http.MethodPost, ts.URL,
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"build","arguments":{"component":"app"}}}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	responseRaw, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("call adapter: %v", err)
	}
	defer responseRaw.Body.Close()
	body, err := io.ReadAll(responseRaw.Body)
	if err != nil {
		t.Fatalf("read adapter response: %v", err)
	}
	var response map[string]any
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode adapter response: %v; body=%s", err, body)
	}
	if rpcErr, found := response["error"]; found {
		t.Fatalf("unexpected MCP error: %#v", rpcErr)
	}
	text := response["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	var result devmcp.ToolResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	if result.Status != devmcp.StatusOK || !strings.Contains(result.OutputTail, "core-adapter") {
		t.Fatalf("adapter result = %#v", result)
	}
}
