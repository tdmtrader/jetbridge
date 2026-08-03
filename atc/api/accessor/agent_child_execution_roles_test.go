package accessor

import (
	"testing"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
)

// GetAgentChildExecution is classified into the CheckAuthorizationHandler tier
// in atc/wrappa/api_auth_wrappa.go. A missing DefaultRoles entry resolves to
// requiredRole "" and hasRequiredRole's default case, which makes the route
// silently admin-only rather than team-scoped.
func TestAgentChildExecutionInspectionRouteHasExplicitTeamRole(t *testing.T) {
	got, found := DefaultRoles[atc.GetAgentChildExecution]
	if !found || got != ViewerRole {
		t.Fatalf("DefaultRoles[%q] = %q, %v; want %q, true", atc.GetAgentChildExecution, got, found, ViewerRole)
	}
}

func TestAgentChildExecutionInspectionAdmitsTeamViewersNotOnlyAdmins(t *testing.T) {
	verification := Verification{
		HasToken: true, IsTokenValid: true,
		RawClaims: map[string]any{"federated_claims": map[string]any{
			"connector_id": "test", "user_id": "alice",
		}},
	}

	accessFor := func(actualRole string, admin bool) Access {
		t.Helper()
		team := new(dbfakes.FakeTeam)
		team.NameReturns("main")
		team.AdminReturns(admin)
		team.AuthReturns(atc.TeamAuth{actualRole: map[string][]string{
			"users": {"test:alice"},
		}})
		return NewAccessor(verification, DefaultRoles[atc.GetAgentChildExecution], "sub", []string{"system"}, []db.Team{team}, nil)
	}

	for _, role := range []string{ViewerRole, MemberRole, OwnerRole} {
		if !accessFor(role, false).IsAuthorized("main") {
			t.Errorf("non-admin %s of the owning team was denied child-execution inspection", role)
		}
	}

	other := new(dbfakes.FakeTeam)
	other.NameReturns("other")
	other.AuthReturns(atc.TeamAuth{OwnerRole: map[string][]string{"users": {"test:alice"}}})
	foreign := NewAccessor(verification, DefaultRoles[atc.GetAgentChildExecution], "sub", []string{"system"}, []db.Team{other}, nil)
	if foreign.IsAuthorized("main") {
		t.Error("a member of an unrelated team was admitted to another team's child execution")
	}
}
