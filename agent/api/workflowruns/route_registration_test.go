package workflowruns_test

import (
	"testing"

	"github.com/concourse/concourse/atc"
)

func TestWorkflowRunRoutesRegisteredExactlyOnce(t *testing.T) {
	required := map[string]struct {
		method string
		path   string
	}{
		atc.CreateAgentWorkflowRun:                     {method: "POST", path: "/api/v1/agent/workflows/:workflow_name/runs"},
		atc.ListAgentWorkflowRuns:                      {method: "GET", path: "/api/v1/agent/workflows/:workflow_name/runs"},
		atc.GetAgentWorkflowRunOperationalStatusCounts: {method: "GET", path: "/api/v1/agent/workflows/:workflow_name/runs/operational-status-counts"},
		atc.GetAgentWorkflowRun:                        {method: "GET", path: "/api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id"},
		atc.CancelAgentWorkflowRun:                     {method: "POST", path: "/api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id/cancel"},
		atc.RetryAgentWorkflowRun:                      {method: "POST", path: "/api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id/retry"},
		atc.GetAgentWorkflowRunOutputs:                 {method: "GET", path: "/api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id/outputs"},
	}

	counts := make(map[string]int, len(required))
	for _, route := range atc.Routes {
		want, relevant := required[route.Name]
		if !relevant {
			continue
		}
		counts[route.Name]++
		if route.Method != want.method {
			t.Errorf("route %q method = %q, want %q", route.Name, route.Method, want.method)
		}
		if route.Path != want.path {
			t.Errorf("route %q path = %q, want %q", route.Name, route.Path, want.path)
		}
	}
	for name := range required {
		if counts[name] != 1 {
			t.Errorf("route %q registered %d times, want exactly once", name, counts[name])
		}
	}
}
