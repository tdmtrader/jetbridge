package feedback_test

import (
	"testing"

	"github.com/concourse/concourse/atc"
)

// TestFeedbackRoutesRegistered verifies that the agent feedback API routes
// are defined in the main ATC route table. Without these routes, the feedback
// endpoints are unreachable in production even though the handlers exist.
func TestFeedbackRoutesRegistered(t *testing.T) {
	routeNames := map[string]bool{}
	for _, route := range atc.Routes {
		routeNames[route.Name] = true
	}

	requiredRoutes := []struct {
		name   string
		method string
		path   string
	}{
		{atc.SubmitAgentFeedback, "POST", "/api/v1/agent/feedback"},
	}

	// The v1 read/classify surface is gone: verdicts are read back through the
	// review projection, and the keyword classifier went with ci-agent.
	for _, path := range []string{
		"/api/v1/agent/feedback/summary",
		"/api/v1/agent/feedback/classify",
		"/api/v1/agent/reviews/:commit/findings",
	} {
		for _, route := range atc.Routes {
			if route.Path == path {
				t.Errorf("route %q (%s %s) must not be registered", route.Name, route.Method, path)
			}
		}
	}

	for _, route := range atc.Routes {
		if route.Path == "/api/v1/agent/feedback" && route.Method != "POST" {
			t.Errorf("only POST /api/v1/agent/feedback survives; found %s", route.Method)
		}
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
