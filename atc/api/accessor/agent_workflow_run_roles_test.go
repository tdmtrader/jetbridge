package accessor_test

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Agent workflow-run roles", func() {
	It("has explicit viewer and member route mappings", func() {
		want := map[string]string{
			atc.ListAgentNodes:                             accessor.ViewerRole,
			atc.ListAgentNodeVersions:                      accessor.ViewerRole,
			atc.GetAgentNodeVersion:                        accessor.ViewerRole,
			atc.CreateAgentNodeVersion:                     accessor.MemberRole,
			atc.ReleaseAgentNodeVersion:                    accessor.MemberRole,
			atc.DeprecateAgentNodeVersion:                  accessor.MemberRole,
			atc.CreateAgentNodeRun:                         accessor.MemberRole,
			atc.ListAgentNodeRuns:                          accessor.ViewerRole,
			atc.GetAgentNodeRun:                            accessor.ViewerRole,
			atc.CancelAgentNodeRun:                         accessor.MemberRole,
			atc.ListAgentNodeConsumers:                     accessor.ViewerRole,
			atc.UpgradeAgentNodeConsumers:                  accessor.MemberRole,
			atc.CreateAgentWorkflowRun:                     accessor.MemberRole,
			atc.ListAgentWorkflowRuns:                      accessor.ViewerRole,
			atc.GetAgentWorkflowRunOperationalStatusCounts: accessor.ViewerRole,
			atc.GetAgentWorkflowRun:                        accessor.ViewerRole,
			atc.CancelAgentWorkflowRun:                     accessor.MemberRole,
			atc.RetryAgentWorkflowRun:                      accessor.MemberRole,
			atc.GetAgentWorkflowRunOutputs:                 accessor.ViewerRole,
			atc.GetAgentWorkflowRunGraph:                   accessor.ViewerRole,
		}
		for route, role := range want {
			Expect(accessor.DefaultRoles).To(HaveKeyWithValue(route, role), route)
		}
	})

	It("enforces viewer and member tiers with persisted team identities", func() {
		teamFactory := useRealTeamFactory()
		viewerTeamName := "workflow-run-viewers"
		memberTeamName := "workflow-run-members"
		ownerTeamName := "workflow-run-owners"

		viewerTeam := persistRoleTeam(teamFactory, viewerTeamName, accessor.ViewerRole, "workflow-run-viewer")
		memberTeam := persistRoleTeam(teamFactory, memberTeamName, accessor.MemberRole, "workflow-run-member")
		ownerTeam := persistRoleTeam(teamFactory, ownerTeamName, accessor.OwnerRole, "workflow-run-owner")

		accessFor := func(team db.Team, userID, requiredRole string) accessor.Access {
			return accessor.NewAccessor(
				verificationForUser(userID),
				requiredRole,
				"sub",
				[]string{"system"},
				[]db.Team{team},
				nil,
			)
		}

		readRoutes := []string{
			atc.ListAgentNodes,
			atc.ListAgentNodeVersions,
			atc.GetAgentNodeVersion,
			atc.ListAgentNodeRuns,
			atc.GetAgentNodeRun,
			atc.ListAgentNodeConsumers,
			atc.ListAgentWorkflowRuns,
			atc.GetAgentWorkflowRunOperationalStatusCounts,
			atc.GetAgentWorkflowRun,
			atc.GetAgentWorkflowRunOutputs,
			atc.GetAgentWorkflowRunGraph,
		}
		for _, route := range readRoutes {
			viewerAccess := accessFor(viewerTeam, "workflow-run-viewer", accessor.DefaultRoles[route])
			Expect(viewerAccess.IsAuthorized(viewerTeamName)).To(BeTrue(), route)
		}

		writeRoutes := []string{
			atc.CreateAgentNodeVersion,
			atc.ReleaseAgentNodeVersion,
			atc.DeprecateAgentNodeVersion,
			atc.CreateAgentNodeRun,
			atc.CancelAgentNodeRun,
			atc.UpgradeAgentNodeConsumers,
			atc.CreateAgentWorkflowRun,
			atc.CancelAgentWorkflowRun,
			atc.RetryAgentWorkflowRun,
		}
		for _, route := range writeRoutes {
			requiredRole := accessor.DefaultRoles[route]
			viewerAccess := accessFor(viewerTeam, "workflow-run-viewer", requiredRole)
			memberAccess := accessFor(memberTeam, "workflow-run-member", requiredRole)
			ownerAccess := accessFor(ownerTeam, "workflow-run-owner", requiredRole)

			Expect(viewerAccess.IsAuthorized(viewerTeamName)).To(BeFalse(), route)
			Expect(memberAccess.IsAuthorized(memberTeamName)).To(BeTrue(), route)
			Expect(ownerAccess.IsAuthorized(ownerTeamName)).To(BeTrue(), route)
		}
	})
})
