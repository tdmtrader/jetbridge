package workflows_test

import (
	"testing"

	"github.com/concourse/concourse/atc"
)

// TestWorkflowRoutesRegistered guards against handlers existing without
// atc.Routes entries (unreachable in production).
func TestWorkflowRoutesRegistered(t *testing.T) {
	required := []struct {
		name   string
		method string
		path   string
	}{
		{atc.ListAgentWorkflows, "GET", "/api/v1/agent/workflows"},
		{atc.ListAgentWorkflowVersions, "GET", "/api/v1/agent/workflows/:workflow_name/versions"},
		{atc.GetAgentWorkflowVersion, "GET", "/api/v1/agent/workflows/:workflow_name/versions/:version"},
		{atc.CreateAgentWorkflowVersion, "POST", "/api/v1/agent/workflows/:workflow_name/versions"},
		{atc.PromoteAgentWorkflowVersion, "PUT", "/api/v1/agent/workflows/:workflow_name/versions/:version/live"},
	}
	for _, rr := range required {
		found := false
		for _, route := range atc.Routes {
			if route.Name == rr.name {
				found = true
				if route.Method != rr.method {
					t.Errorf("route %q: method %s, want %s", rr.name, route.Method, rr.method)
				}
				if route.Path != rr.path {
					t.Errorf("route %q: path %s, want %s", rr.name, route.Path, rr.path)
				}
			}
		}
		if !found {
			t.Errorf("route %q not registered in atc.Routes", rr.name)
		}
	}
}
