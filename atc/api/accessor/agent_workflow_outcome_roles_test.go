package accessor

import (
	"testing"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
)

func TestAgentWorkflowOutcomeRoutesHaveExplicitMainTeamRoles(t *testing.T) {
	want := map[string]string{
		atc.ListAgentWorkflowRunOutcomes:     ViewerRole,
		atc.SetAgentWorkflowRunOutputOutcome: MemberRole,
	}
	for route, role := range want {
		if got, found := DefaultRoles[route]; !found || got != role {
			t.Errorf("DefaultRoles[%q] = %q, %v; want %q, true", route, got, found, role)
		}
	}
}

func TestAgentWorkflowOutcomeRolesEnforceViewerAndMemberTiers(t *testing.T) {
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
	if !accessFor(DefaultRoles[atc.ListAgentWorkflowRunOutcomes], ViewerRole).IsAuthorized(atc.DefaultTeamName) {
		t.Fatal("viewer was denied workflow-outcome read route")
	}
	if accessFor(DefaultRoles[atc.SetAgentWorkflowRunOutputOutcome], ViewerRole).IsAuthorized(atc.DefaultTeamName) {
		t.Fatal("viewer was admitted to workflow-outcome mutation route")
	}
	if !accessFor(DefaultRoles[atc.SetAgentWorkflowRunOutputOutcome], MemberRole).IsAuthorized(atc.DefaultTeamName) {
		t.Fatal("member was denied workflow-outcome mutation route")
	}
}
