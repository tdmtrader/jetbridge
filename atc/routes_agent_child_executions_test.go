package atc_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/broker/transport"
	"github.com/concourse/concourse/atc"
	"github.com/tedsuo/rata"
)

func TestAgentChildExecutionRoutesExposeOnlyExplicitAuthorityAndInspectionPaths(t *testing.T) {
	want := map[string]string{
		atc.AdmitAgentChildExecution:                   "/api/v1/internal/agent-child-executions/admit",
		atc.PhaseAgentChildExecution:                   "/api/v1/internal/agent-child-executions/:execution_id/phase",
		atc.UpdateAgentChildExecution:                  "/api/v1/internal/agent-child-executions/:execution_id/update",
		atc.TerminalAgentChildExecution:                "/api/v1/internal/agent-child-executions/:execution_id/terminal",
		atc.SealAgentChildExecution:                    "/api/v1/internal/agent-child-executions/:execution_id/seal",
		atc.CaptureWorkspaceAgentChildExecution:        "/api/v1/internal/agent-child-executions/:execution_id/workspace-capture",
		atc.CaptureWorkspaceFailureAgentChildExecution: "/api/v1/internal/agent-child-executions/:execution_id/workspace-capture-failed",
		atc.GetAgentChildExecution:                     "/api/v1/teams/:team_name/agent-child-executions/:execution_id",
	}
	got := map[string]string{}
	for _, route := range atc.Routes {
		got[route.Name] = route.Path
	}
	for name, path := range want {
		if got[name] != path {
			t.Fatalf("route %s = %q, want %q", name, got[name], path)
		}
	}

	// Exact set, not merely a subset: the subset form could not tell that a
	// path the transport client POSTs was never registered at all.
	var extra []string
	for _, route := range atc.Routes {
		if !strings.Contains(route.Path, "agent-child-executions") {
			continue
		}
		if _, ok := want[route.Name]; !ok {
			extra = append(extra, fmt.Sprintf("%s %s (%s)", route.Method, route.Path, route.Name))
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		t.Fatalf("unexpected agent-child-execution routes: %v", extra)
	}
}

// TestAgentChildExecutionTransportPathsAreRoutable drives the real router the
// API is built from — atc/api/handler.go ends in the same
// rata.NewRouter(atc.Routes, ...) — so every path the broker transport client
// can POST is proven to dispatch to the intended route. The handler-level tests
// in atc/api/agentchildexecutions call handler.ServeHTTP directly and bypass
// rata entirely, which is exactly how the workspace-capture routes came to be
// implemented everywhere except the route table and 404 in production.
func TestAgentChildExecutionTransportPathsAreRoutable(t *testing.T) {
	const executionID = "8b2f0e5c-4b1a-4d3e-9c7f-0a1b2c3d4e5f"

	cases := []struct {
		path string
		want string
	}{
		{transport.AdmitPath, atc.AdmitAgentChildExecution},
		{transport.PhasePath(executionID), atc.PhaseAgentChildExecution},
		{"/api/v1/internal/agent-child-executions/" + executionID + "/update", atc.UpdateAgentChildExecution},
		{"/api/v1/internal/agent-child-executions/" + executionID + "/terminal", atc.TerminalAgentChildExecution},
		{"/api/v1/internal/agent-child-executions/" + executionID + "/seal", atc.SealAgentChildExecution},
		{transport.WorkspaceCapturePath(executionID), atc.CaptureWorkspaceAgentChildExecution},
		{transport.WorkspaceCaptureFailurePath(executionID), atc.CaptureWorkspaceFailureAgentChildExecution},
	}

	var dispatched string
	handlers := rata.Handlers{}
	for _, route := range atc.Routes {
		name := route.Name
		handlers[name] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			dispatched = name
			w.WriteHeader(http.StatusNoContent)
		})
	}

	router, err := rata.NewRouter(atc.Routes, handlers)
	if err != nil {
		t.Fatalf("build router: %v", err)
	}

	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			dispatched = ""
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, tc.path, nil))

			if recorder.Code == http.StatusNotFound {
				t.Fatalf("POST %s is not routable (404); it must be registered in atc.Routes", tc.path)
			}
			if dispatched != tc.want {
				t.Fatalf("POST %s dispatched to %q, want %q", tc.path, dispatched, tc.want)
			}
		})
	}
}

// TestRoutesAreUniqueByNameAndPath guards the route table itself: pat matches in
// registration order, so a duplicate (method, path) silently shadows the later
// entry and makes its handler unreachable, and a duplicate name makes
// rata.Handlers collide.
func TestRoutesAreUniqueByNameAndPath(t *testing.T) {
	seenName := map[string]string{}
	seenPath := map[string]string{}

	for _, route := range atc.Routes {
		if prev, ok := seenName[route.Name]; ok {
			t.Errorf("duplicate route name %q: %q and %q", route.Name, prev, route.Path)
		}
		seenName[route.Name] = route.Path

		key := strings.ToUpper(route.Method) + " " + route.Path
		if prev, ok := seenPath[key]; ok {
			t.Errorf("duplicate route %q registered by both %q and %q", key, prev, route.Name)
		}
		seenPath[key] = route.Name
	}
}
