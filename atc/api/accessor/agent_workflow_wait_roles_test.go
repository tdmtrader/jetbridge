package accessor

import (
	"testing"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
)

func TestAgentWorkflowWaitRoutesHaveExplicitMainTeamRoles(t *testing.T) {
	want := map[string]string{
		atc.ListAgentWorkflowRunWaits:   ViewerRole,
		atc.ResolveAgentWorkflowRunWait: MemberRole,
	}
	for route, role := range want {
		if got, found := DefaultRoles[route]; !found || got != role {
			t.Errorf("DefaultRoles[%q] = %q, %v; want %q, true", route, got, found, role)
		}
	}
}

func TestAgentWorkflowWaitRolesEnforceViewerAndMemberTiers(t *testing.T) {
	verification := Verification{
		HasToken: true, IsTokenValid: true,
		RawClaims: map[string]any{"federated_claims": map[string]any{
			"connector_id": "test", "user_id": "alice",
		}},
	}
	accessFor := func(requiredRole, actualRole string) Access {
		t.Helper()
		team := new(dbfakes.FakeTeam)
		team.NameReturns(atc.DefaultTeamName)
		team.AuthReturns(atc.TeamAuth{actualRole: map[string][]string{"users": {"test:alice"}}})
		return NewAccessor(verification, requiredRole, "sub", []string{"system"}, []db.Team{team}, nil)
	}
	if !accessFor(DefaultRoles[atc.ListAgentWorkflowRunWaits], ViewerRole).IsAuthorized(atc.DefaultTeamName) {
		t.Fatal("viewer was denied workflow-wait read route")
	}
	if accessFor(DefaultRoles[atc.ResolveAgentWorkflowRunWait], ViewerRole).IsAuthorized(atc.DefaultTeamName) {
		t.Fatal("viewer was admitted to workflow-wait resolution route")
	}
	if !accessFor(DefaultRoles[atc.ResolveAgentWorkflowRunWait], MemberRole).IsAuthorized(atc.DefaultTeamName) {
		t.Fatal("member was denied workflow-wait resolution route")
	}
}
