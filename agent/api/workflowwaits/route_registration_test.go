package workflowwaits_test

import (
	"testing"

	"github.com/concourse/concourse/atc"
)

func TestWorkflowWaitRoutesRegisteredExactlyOnce(t *testing.T) {
	want := map[string]struct {
		method string
		path   string
	}{
		atc.ListAgentWorkflowRunWaits: {
			method: "GET",
			path:   "/api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id/waits",
		},
		atc.ResolveAgentWorkflowRunWait: {
			method: "PUT",
			path:   "/api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id/waits/:workflow_wait_id/resolve",
		},
	}
	counts := map[string]int{}
	for _, route := range atc.Routes {
		expected, found := want[route.Name]
		if !found {
			continue
		}
		counts[route.Name]++
		if route.Method != expected.method || route.Path != expected.path {
			t.Fatalf("route %s = %s %s, want %s %s", route.Name, route.Method, route.Path, expected.method, expected.path)
		}
	}
	for name := range want {
		if counts[name] != 1 {
			t.Fatalf("route %s count = %d, want 1", name, counts[name])
		}
	}
}
