package atc_test

import (
	"testing"

	"github.com/concourse/concourse/atc"
)

func TestAgentChildExecutionRoutesExposeOnlyExplicitAuthorityAndInspectionPaths(t *testing.T) {
	want := map[string]string{
		atc.AdmitAgentChildExecution:    "/api/v1/internal/agent-child-executions/admit",
		atc.PhaseAgentChildExecution:    "/api/v1/internal/agent-child-executions/:execution_id/phase",
		atc.UpdateAgentChildExecution:   "/api/v1/internal/agent-child-executions/:execution_id/update",
		atc.TerminalAgentChildExecution: "/api/v1/internal/agent-child-executions/:execution_id/terminal",
		atc.SealAgentChildExecution:     "/api/v1/internal/agent-child-executions/:execution_id/seal",
		atc.GetAgentChildExecution:      "/api/v1/teams/:team_name/agent-child-executions/:execution_id",
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
}
