package accessor

import (
	"testing"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
)

func TestAgentWorkflowRunRoutesHaveExplicitMainTeamRoles(t *testing.T) {
	want := map[string]string{
		atc.ListAgentNodes:                             ViewerRole,
		atc.ListAgentNodeVersions:                      ViewerRole,
		atc.GetAgentNodeVersion:                        ViewerRole,
		atc.CreateAgentNodeVersion:                     MemberRole,
		atc.ReleaseAgentNodeVersion:                    MemberRole,
		atc.DeprecateAgentNodeVersion:                  MemberRole,
		atc.CreateAgentNodeRun:                         MemberRole,
		atc.ListAgentNodeRuns:                          ViewerRole,
		atc.GetAgentNodeRun:                            ViewerRole,
		atc.CreateAgentWorkflowRun:                     MemberRole,
		atc.ListAgentWorkflowRuns:                      ViewerRole,
		atc.GetAgentWorkflowRunOperationalStatusCounts: ViewerRole,
		atc.GetAgentWorkflowRun:                        ViewerRole,
		atc.CancelAgentWorkflowRun:                     MemberRole,
		atc.RetryAgentWorkflowRun:                      MemberRole,
		atc.GetAgentWorkflowRunOutputs:                 ViewerRole,
	}
	for route, role := range want {
		if got, found := DefaultRoles[route]; !found || got != role {
			t.Errorf("DefaultRoles[%q] = %q, %v; want %q, true", route, got, found, role)
		}
	}
}

func TestAgentWorkflowRunRolesEnforceViewerAndMemberTiers(t *testing.T) {
	readRoutes := []string{
		atc.ListAgentNodes,
		atc.ListAgentNodeVersions,
		atc.GetAgentNodeVersion,
		atc.ListAgentNodeRuns,
		atc.GetAgentNodeRun,
		atc.ListAgentWorkflowRuns,
		atc.GetAgentWorkflowRunOperationalStatusCounts,
		atc.GetAgentWorkflowRun,
		atc.GetAgentWorkflowRunOutputs,
	}
	writeRoutes := []string{
		atc.CreateAgentNodeVersion,
		atc.ReleaseAgentNodeVersion,
		atc.DeprecateAgentNodeVersion,
		atc.CreateAgentNodeRun,
		atc.CreateAgentWorkflowRun,
		atc.CancelAgentWorkflowRun,
		atc.RetryAgentWorkflowRun,
	}
	verification := Verification{
		HasToken: true, IsTokenValid: true,
		RawClaims: map[string]any{"federated_claims": map[string]any{
			"connector_id": "test", "user_id": "alice",
		}},
	}

	accessFor := func(requiredRole, actualRole string, admin bool) Access {
		t.Helper()
		team := new(dbfakes.FakeTeam)
		team.NameReturns(atc.DefaultTeamName)
		team.AdminReturns(admin)
		team.AuthReturns(atc.TeamAuth{actualRole: map[string][]string{
			"users": {"test:alice"},
		}})
		return NewAccessor(verification, requiredRole, "sub", []string{"system"}, []db.Team{team}, nil)
	}

	for _, route := range readRoutes {
		if !accessFor(DefaultRoles[route], ViewerRole, false).IsAuthorized(atc.DefaultTeamName) {
			t.Errorf("viewer was denied workflow-run read route %q", route)
		}
	}
	for _, route := range writeRoutes {
		if accessFor(DefaultRoles[route], ViewerRole, false).IsAuthorized(atc.DefaultTeamName) {
			t.Errorf("viewer was admitted to workflow-run mutation route %q", route)
		}
		if !accessFor(DefaultRoles[route], MemberRole, false).IsAuthorized(atc.DefaultTeamName) {
			t.Errorf("member was denied workflow-run mutation route %q", route)
		}
		if !accessFor(DefaultRoles[route], OwnerRole, true).IsAuthorized(atc.DefaultTeamName) {
			t.Errorf("admin owner was denied workflow-run mutation route %q", route)
		}
	}
}
