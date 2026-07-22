package accessor

import (
	"testing"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
)

func TestAgentSnapshotRoutesHaveExplicitTeamRoles(t *testing.T) {
	want := map[string]string{
		atc.CreateAgentSnapshot:   MemberRole,
		atc.ListAgentSnapshots:    ViewerRole,
		atc.GetAgentSnapshot:      ViewerRole,
		atc.DownloadAgentSnapshot: ViewerRole,
		atc.PinAgentSnapshot:      MemberRole,
		atc.UnpinAgentSnapshot:    MemberRole,
	}
	for route, role := range want {
		if got, found := DefaultRoles[route]; !found || got != role {
			t.Errorf("DefaultRoles[%q] = %q, %v; want %q, true", route, got, found, role)
		}
	}
}

func TestAgentSnapshotRolesEnforceViewerAndMemberTiers(t *testing.T) {
	readRoutes := []string{atc.ListAgentSnapshots, atc.GetAgentSnapshot, atc.DownloadAgentSnapshot}
	writeRoutes := []string{atc.CreateAgentSnapshot, atc.PinAgentSnapshot, atc.UnpinAgentSnapshot}
	verification := Verification{
		HasToken: true, IsTokenValid: true,
		RawClaims: map[string]any{"federated_claims": map[string]any{
			"connector_id": "test", "user_id": "alice",
		}},
	}

	accessFor := func(requiredRole, actualRole string, admin bool) Access {
		t.Helper()
		team := new(dbfakes.FakeTeam)
		team.NameReturns("main")
		team.AdminReturns(admin)
		team.AuthReturns(atc.TeamAuth{actualRole: map[string][]string{
			"users": {"test:alice"},
		}})
		return NewAccessor(verification, requiredRole, "sub", []string{"system"}, []db.Team{team}, nil)
	}

	for _, route := range readRoutes {
		if !accessFor(DefaultRoles[route], ViewerRole, false).IsAuthorized("main") {
			t.Errorf("viewer was denied read route %q", route)
		}
	}
	for _, route := range writeRoutes {
		if accessFor(DefaultRoles[route], ViewerRole, false).IsAuthorized("main") {
			t.Errorf("viewer was admitted to mutation route %q", route)
		}
		if !accessFor(DefaultRoles[route], MemberRole, false).IsAuthorized("main") {
			t.Errorf("member was denied mutation route %q", route)
		}
		if !accessFor(DefaultRoles[route], OwnerRole, true).IsAuthorized("main") {
			t.Errorf("admin owner was denied mutation route %q", route)
		}
	}

	anonymous := verification
	anonymous.HasToken = false
	anonymous.IsTokenValid = false
	team := new(dbfakes.FakeTeam)
	team.NameReturns("main")
	team.AuthReturns(atc.TeamAuth{ViewerRole: map[string][]string{"users": {"test:alice"}}})
	access := NewAccessor(anonymous, ViewerRole, "sub", []string{"system"}, []db.Team{team}, nil)
	if access.IsAuthenticated() || access.IsAuthorized("main") {
		t.Fatal("anonymous snapshot request was treated as authenticated or authorized")
	}
}
