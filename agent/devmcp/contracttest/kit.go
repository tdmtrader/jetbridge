package contracttest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/devmcp"
)

// Options selects the optional execution checks. All fields are opt-in;
// zero Options runs only the universal protocol/schema/taxonomy checks.
type Options struct {
	// ExerciseComponent names a component whose build and run_tests are
	// executed and expected to return status "ok".
	ExerciseComponent string
	// FailingLintComponent names a component whose lint is expected to
	// return status "failed" (ran, found problems — never "error").
	FailingLintComponent string
	// SlowTestComponent names a component whose run_tests runs long enough
	// that at least one notifications/progress frame must be observed
	// (pair with a short DEV_MCP_PROGRESS_INTERVAL on the server).
	SlowTestComponent string
	// AffectedPath + ExpectAffected assert the affected_components mapping
	// for one known path.
	AffectedPath   string
	ExpectAffected []string
	// Timeout bounds each individual check (default 5m).
	Timeout time.Duration
}

// Run executes the universal contract checks against endpoint
// (e.g. "http://127.0.0.1:7780/mcp").
func Run(t *testing.T, endpoint string) {
	RunWithOptions(t, endpoint, Options{})
}

// RunWithOptions executes the universal checks plus any opted-in
// execution checks.
func RunWithOptions(t *testing.T, endpoint string, opts Options) {
	t.Helper()
	if opts.Timeout == 0 {
		opts.Timeout = 5 * time.Minute
	}

	run := func(name string, fn func(ctx context.Context) error) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
			defer cancel()
			if err := fn(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}

	run("initialize", func(ctx context.Context) error { return checkInitialize(ctx, endpoint) })
	run("tools_list", func(ctx context.Context) error { return checkToolsList(ctx, endpoint) })
	run("list_components", func(ctx context.Context) error { return checkListComponents(ctx, endpoint) })
	run("unknown_tool_is_rpc_error", func(ctx context.Context) error { return checkUnknownTool(ctx, endpoint) })
	run("unknown_component_is_rpc_error", func(ctx context.Context) error { return checkUnknownComponent(ctx, endpoint) })
	run("missing_changed_paths_is_rpc_error", func(ctx context.Context) error { return checkMissingChangedPaths(ctx, endpoint) })
	run("affected_components_empty_input", func(ctx context.Context) error { return checkAffectedEmpty(ctx, endpoint) })

	if opts.ExerciseComponent != "" {
		run("exercise_ok_component", func(ctx context.Context) error {
			return checkExerciseOK(ctx, endpoint, opts.ExerciseComponent)
		})
	}
	if opts.FailingLintComponent != "" {
		run("failing_lint_taxonomy", func(ctx context.Context) error {
			return checkFailingLint(ctx, endpoint, opts.FailingLintComponent)
		})
	}
	if opts.SlowTestComponent != "" {
		run("progress_emission", func(ctx context.Context) error {
			return checkProgress(ctx, endpoint, opts.SlowTestComponent)
		})
	}
	if opts.AffectedPath != "" {
		run("affected_components_mapping", func(ctx context.Context) error {
			return checkAffectedMapping(ctx, endpoint, opts.AffectedPath, opts.ExpectAffected)
		})
	}
}

func checkInitialize(ctx context.Context, endpoint string) error {
	env, err := rawCall(ctx, endpoint, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "contracttest", "version": "1"},
	})
	if err != nil {
		return err
	}
	if env.Error != nil {
		return fmt.Errorf("initialize returned error: %+v", env.Error)
	}
	var res struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct {
			Tools *struct{} `json:"tools"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(env.Result, &res); err != nil {
		return fmt.Errorf("decode initialize result: %w", err)
	}
	if res.ProtocolVersion == "" {
		return fmt.Errorf("initialize result missing protocolVersion")
	}
	if res.Capabilities.Tools == nil {
		return fmt.Errorf("server does not advertise the tools capability")
	}
	return nil
}

var requiredTools = []string{"affected_components", "build", "lint", "list_components", "run_tests"}

func checkToolsList(ctx context.Context, endpoint string) error {
	env, err := rawCall(ctx, endpoint, "tools/list", map[string]any{})
	if err != nil {
		return err
	}
	if env.Error != nil {
		return fmt.Errorf("tools/list returned error: %+v", env.Error)
	}
	var res struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(env.Result, &res); err != nil {
		return fmt.Errorf("decode tools/list result: %w", err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		if tool.Description == "" {
			return fmt.Errorf("tool %s has no description", tool.Name)
		}
		var schema map[string]any
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			return fmt.Errorf("tool %s inputSchema is not a JSON object: %w", tool.Name, err)
		}
		if schema["type"] != "object" {
			return fmt.Errorf("tool %s inputSchema type is %v, want object", tool.Name, schema["type"])
		}
	}
	for _, want := range requiredTools {
		if !slices.Contains(names, want) {
			return fmt.Errorf("missing required tool %s (got %v)", want, names)
		}
	}
	return nil
}

var validKinds = map[string]bool{
	"service": true, "library": true, "cli": true,
	"web": true, "docs": true, "other": true,
}

func checkListComponents(ctx context.Context, endpoint string) error {
	comps, err := devmcp.NewClient(endpoint).ListComponents(ctx)
	if err != nil {
		return err
	}
	if len(comps) == 0 {
		return fmt.Errorf("list_components returned no components")
	}
	seen := map[string]bool{}
	for _, comp := range comps {
		if comp.ID == "" {
			return fmt.Errorf("component with empty id")
		}
		if seen[comp.ID] {
			return fmt.Errorf("duplicate component id %q", comp.ID)
		}
		seen[comp.ID] = true
		if comp.Description == "" {
			return fmt.Errorf("component %s: empty description", comp.ID)
		}
		if len(comp.Paths) == 0 {
			return fmt.Errorf("component %s: empty paths", comp.ID)
		}
		if !validKinds[comp.Kind] {
			return fmt.Errorf("component %s: invalid kind %q", comp.ID, comp.Kind)
		}
	}
	return nil
}

func checkUnknownTool(ctx context.Context, endpoint string) error {
	env, err := rawCall(ctx, endpoint, "tools/call", map[string]any{
		"name": "no_such_tool_xyz", "arguments": map[string]any{},
	})
	if err != nil {
		return err
	}
	if env.Error == nil {
		return fmt.Errorf("calling an unknown tool must return a JSON-RPC error (MCP errors are for malformed input)")
	}
	return nil
}

func checkUnknownComponent(ctx context.Context, endpoint string) error {
	_, err := devmcp.NewClient(endpoint).Build(ctx, "no-such-component-xyz")
	var rpcErr *devmcp.RPCError
	if !errors.As(err, &rpcErr) {
		return fmt.Errorf("build of an unknown component must fail with a JSON-RPC error, got %v", err)
	}
	return nil
}

func checkMissingChangedPaths(ctx context.Context, endpoint string) error {
	env, err := rawCall(ctx, endpoint, "tools/call", map[string]any{
		"name": "affected_components", "arguments": map[string]any{},
	})
	if err != nil {
		return err
	}
	if env.Error == nil {
		return fmt.Errorf("affected_components without changed_paths must return a JSON-RPC error")
	}
	return nil
}

func checkAffectedEmpty(ctx context.Context, endpoint string) error {
	ids, err := devmcp.NewClient(endpoint).AffectedComponents(ctx, []string{})
	if err != nil {
		return err
	}
	if len(ids) != 0 {
		return fmt.Errorf("empty changed_paths must map to no components, got %v", ids)
	}
	return nil
}

func validateResult(res *devmcp.ToolResult) error {
	switch res.Status {
	case devmcp.StatusOK, devmcp.StatusFailed, devmcp.StatusError:
	default:
		return fmt.Errorf("status %q outside ok|failed|error", res.Status)
	}
	if res.Summary == "" {
		return fmt.Errorf("summary is required")
	}
	if res.DurationSeconds < 0 {
		return fmt.Errorf("duration_seconds must be >= 0")
	}
	return nil
}

func checkExerciseOK(ctx context.Context, endpoint, component string) error {
	client := devmcp.NewClient(endpoint)
	calls := []struct {
		name string
		fn   func() (*devmcp.ToolResult, error)
	}{
		{"build", func() (*devmcp.ToolResult, error) { return client.Build(ctx, component) }},
		{"run_tests", func() (*devmcp.ToolResult, error) { return client.RunTests(ctx, component, "") }},
	}
	for _, call := range calls {
		res, err := call.fn()
		if err != nil {
			return fmt.Errorf("%s(%s): %w", call.name, component, err)
		}
		if err := validateResult(res); err != nil {
			return fmt.Errorf("%s(%s): %w", call.name, component, err)
		}
		if res.Status != devmcp.StatusOK {
			return fmt.Errorf("%s(%s): status %q, want ok (summary: %s)", call.name, component, res.Status, res.Summary)
		}
	}
	return nil
}

func checkFailingLint(ctx context.Context, endpoint, component string) error {
	res, err := devmcp.NewClient(endpoint).Lint(ctx, component)
	if err != nil {
		return fmt.Errorf("lint(%s): %w", component, err)
	}
	if err := validateResult(res); err != nil {
		return fmt.Errorf("lint(%s): %w", component, err)
	}
	if res.Status != devmcp.StatusFailed {
		return fmt.Errorf("lint(%s): status %q, want failed — findings are failed, never error", component, res.Status)
	}
	return nil
}

func checkProgress(ctx context.Context, endpoint, component string) error {
	var (
		mu    sync.Mutex
		count int
	)
	client := devmcp.NewClient(endpoint, devmcp.WithProgress(func(_, _ string) {
		mu.Lock()
		count++
		mu.Unlock()
	}))
	res, err := client.RunTests(ctx, component, "")
	if err != nil {
		return fmt.Errorf("run_tests(%s): %w", component, err)
	}
	if res.Status != devmcp.StatusOK {
		return fmt.Errorf("run_tests(%s): status %q, want ok", component, res.Status)
	}
	mu.Lock()
	defer mu.Unlock()
	if count == 0 {
		return fmt.Errorf("no notifications/progress observed during a long run — the contract requires progress at least every 30s")
	}
	return nil
}

func checkAffectedMapping(ctx context.Context, endpoint, path string, expect []string) error {
	ids, err := devmcp.NewClient(endpoint).AffectedComponents(ctx, []string{path})
	if err != nil {
		return err
	}
	got := append([]string{}, ids...)
	want := append([]string{}, expect...)
	sort.Strings(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		return fmt.Errorf("affected_components(%q) = %v, want %v", path, got, want)
	}
	return nil
}
