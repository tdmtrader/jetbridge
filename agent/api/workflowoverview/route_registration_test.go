package workflowoverview_test

import (
	"testing"

	"github.com/concourse/concourse/atc"
)

// The overview path must be registered exactly once, with the method the
// handler enforces. Rata panics on a duplicate path at startup, so a
// copy-pasted route fails loudly; a MISSING one fails silently, which is what
// this guards.
func TestWorkflowOverviewRouteRegisteredExactlyOnce(t *testing.T) {
	const wantPath = "/api/v1/agent/workflows/:workflow_name/overview"

	count := 0
	for _, route := range atc.Routes {
		if route.Name != atc.GetAgentWorkflowOverview {
			continue
		}
		count++
		if route.Method != "GET" {
			t.Errorf("method = %q, want GET", route.Method)
		}
		if route.Path != wantPath {
			t.Errorf("path = %q, want %q", route.Path, wantPath)
		}
	}
	if count != 1 {
		t.Fatalf("registered %d times, want exactly once", count)
	}
}

// A sibling route that matched the overview path first would shadow it. The
// only same-shape GET under :workflow_name is stats, and the two must differ.
func TestWorkflowOverviewIsNotShadowedByASiblingRoute(t *testing.T) {
	seen := map[string]string{}
	for _, route := range atc.Routes {
		if route.Method != "GET" {
			continue
		}
		if existing, duplicate := seen[route.Path]; duplicate {
			t.Fatalf("GET %s is registered by both %q and %q", route.Path, existing, route.Name)
		}
		seen[route.Path] = route.Name
	}
}
