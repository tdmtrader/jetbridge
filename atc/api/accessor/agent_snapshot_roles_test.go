package accessor_test

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Agent snapshot roles", func() {
	It("has explicit viewer and member route mappings", func() {
		want := map[string]string{
			atc.CreateAgentSnapshot:                accessor.MemberRole,
			atc.CaptureAgentResourceSnapshot:       accessor.MemberRole,
			atc.ListAgentSnapshots:                 accessor.ViewerRole,
			atc.GetAgentSnapshot:                   accessor.ViewerRole,
			atc.GetAgentRepositoryChangeProjection: accessor.ViewerRole,
			atc.DownloadAgentSnapshot:              accessor.ViewerRole,
			atc.PinAgentSnapshot:                   accessor.MemberRole,
			atc.UnpinAgentSnapshot:                 accessor.MemberRole,
		}
		for route, role := range want {
			Expect(accessor.DefaultRoles).To(HaveKeyWithValue(route, role), route)
		}
	})

	It("enforces viewer and member tiers with persisted team identities", func() {
		teamFactory := useRealTeamFactory()
		viewerTeamName := "snapshot-viewers"
		memberTeamName := "snapshot-members"
		ownerTeamName := "snapshot-owners"
		viewerTeam := persistRoleTeam(teamFactory, viewerTeamName, accessor.ViewerRole, "snapshot-viewer")
		memberTeam := persistRoleTeam(teamFactory, memberTeamName, accessor.MemberRole, "snapshot-member")
		ownerTeam := persistRoleTeam(teamFactory, ownerTeamName, accessor.OwnerRole, "snapshot-owner")

		readRoutes := []string{
			atc.ListAgentSnapshots,
			atc.GetAgentSnapshot,
			atc.GetAgentRepositoryChangeProjection,
			atc.DownloadAgentSnapshot,
		}
		viewerAccessFor := func(requiredRole string) accessor.Access {
			return accessor.NewAccessor(
				verificationForUser("snapshot-viewer"),
				requiredRole,
				"sub",
				[]string{"system"},
				[]db.Team{viewerTeam},
				nil,
			)
		}

		memberAccessFor := func(requiredRole string) accessor.Access {
			return accessor.NewAccessor(
				verificationForUser("snapshot-member"),
				requiredRole,
				"sub",
				[]string{"system"},
				[]db.Team{memberTeam},
				nil,
			)
		}

		ownerAccessFor := func(requiredRole string) accessor.Access {
			return accessor.NewAccessor(
				verificationForUser("snapshot-owner"),
				requiredRole,
				"sub",
				[]string{"system"},
				[]db.Team{ownerTeam},
				nil,
			)
		}

		for _, route := range readRoutes {
			Expect(viewerAccessFor(accessor.DefaultRoles[route]).IsAuthorized(viewerTeamName)).To(BeTrue(), route)
		}

		writeRoutes := []string{
			atc.CreateAgentSnapshot,
			atc.CaptureAgentResourceSnapshot,
			atc.PinAgentSnapshot,
			atc.UnpinAgentSnapshot,
		}
		for _, route := range writeRoutes {
			requiredRole := accessor.DefaultRoles[route]
			Expect(viewerAccessFor(requiredRole).IsAuthorized(viewerTeamName)).To(BeFalse(), route)
			Expect(memberAccessFor(requiredRole).IsAuthorized(memberTeamName)).To(BeTrue(), route)
			Expect(ownerAccessFor(requiredRole).IsAuthorized(ownerTeamName)).To(BeTrue(), route)
		}

		anonymousTeam := persistRoleTeam(
			teamFactory,
			"snapshot-anonymous",
			accessor.ViewerRole,
			"snapshot-anonymous-user",
		)
		anonymous := accessor.NewAccessor(
			accessor.Verification{},
			accessor.ViewerRole,
			"sub",
			[]string{"system"},
			[]db.Team{anonymousTeam},
			nil,
		)
		Expect(anonymous.IsAuthenticated()).To(BeFalse())
		Expect(anonymous.IsAuthorized("snapshot-anonymous")).To(BeFalse())
	})
})
