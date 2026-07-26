package actions_test

import (
	"testing"

	"github.com/concourse/concourse/atc"
)

// TestActionsRoutesRegistered verifies that the agent actions API routes
// are defined in the main ATC route table. Without these routes, the actions
// endpoints are unreachable in production even though the handlers exist.
func TestActionsRoutesRegistered(t *testing.T) {
	routeNames := map[string]bool{}
	for _, route := range atc.Routes {
		routeNames[route.Name] = true
	}

	requiredRoutes := []struct {
		name   string
		method string
		path   string
	}{
		{atc.GetAgentActions, "GET", "/api/v1/agent/actions"},
		{atc.SetAgentActions, "PUT", "/api/v1/agent/actions"},
	}

	for _, rr := range requiredRoutes {
		if !routeNames[rr.name] {
			t.Errorf("route %q (%s %s) not registered in atc.Routes", rr.name, rr.method, rr.path)
		}
	}

	// Also verify the routes resolve to the correct method+path.
	for _, rr := range requiredRoutes {
		found := false
		for _, route := range atc.Routes {
			if route.Name == rr.name {
				found = true
				if route.Method != rr.method {
					t.Errorf("route %q: expected method %s, got %s", rr.name, rr.method, route.Method)
				}
				if route.Path != rr.path {
					t.Errorf("route %q: expected path %s, got %s", rr.name, rr.path, route.Path)
				}
			}
		}
		if !found {
			t.Errorf("route %q not found in atc.Routes", rr.name)
		}
	}
}
