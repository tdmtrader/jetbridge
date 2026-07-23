package atc

import "testing"

func TestAgentWorkflowOutcomeRoutesAreRegisteredExactlyOnce(t *testing.T) {
	want := map[string]struct {
		method string
		path   string
	}{
		ListAgentWorkflowRunOutcomes: {
			"GET",
			"/api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id/outcomes",
		},
		SetAgentWorkflowRunOutputOutcome: {
			"PUT",
			"/api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id/outputs/:snapshot_id/outcome",
		},
	}
	seen := make(map[string]int, len(want))
	for _, route := range Routes {
		expected, found := want[route.Name]
		if !found {
			continue
		}
		seen[route.Name]++
		if route.Method != expected.method || route.Path != expected.path {
			t.Errorf("route %q = %s %s, want %s %s", route.Name, route.Method, route.Path, expected.method, expected.path)
		}
	}
	for name := range want {
		if seen[name] != 1 {
			t.Errorf("route %q registered %d times, want exactly once", name, seen[name])
		}
	}
}
