package accessor_test

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Agent workflow-wait roles", func() {
	It("has explicit viewer and member route mappings", func() {
		want := map[string]string{
			atc.ListAgentWorkflowRunWaits:   accessor.ViewerRole,
			atc.ResolveAgentWorkflowRunWait: accessor.MemberRole,
		}
		for route, role := range want {
			Expect(accessor.DefaultRoles).To(HaveKeyWithValue(route, role), route)
		}
	})

	It("enforces viewer and member tiers with persisted team identities", func() {
		teamFactory := useRealTeamFactory()

		viewerAccess := accessForPersistedRole(
			teamFactory,
			accessor.DefaultRoles[atc.ListAgentWorkflowRunWaits],
			"workflow-wait-viewers",
			accessor.ViewerRole,
			"workflow-wait-viewer",
		)
		Expect(viewerAccess.IsAuthorized("workflow-wait-viewers")).To(BeTrue())

		viewerResolutionAccess := accessForPersistedRole(
			teamFactory,
			accessor.DefaultRoles[atc.ResolveAgentWorkflowRunWait],
			"workflow-wait-resolution-viewers",
			accessor.ViewerRole,
			"workflow-wait-resolution-viewer",
		)
		Expect(viewerResolutionAccess.IsAuthorized("workflow-wait-resolution-viewers")).To(BeFalse())

		memberAccess := accessForPersistedRole(
			teamFactory,
			accessor.DefaultRoles[atc.ResolveAgentWorkflowRunWait],
			"workflow-wait-members",
			accessor.MemberRole,
			"workflow-wait-member",
		)
		Expect(memberAccess.IsAuthorized("workflow-wait-members")).To(BeTrue())
	})
})
