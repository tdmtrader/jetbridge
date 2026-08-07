package accessor_test

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Agent workflow-outcome roles", func() {
	It("has explicit viewer and member route mappings", func() {
		want := map[string]string{
			atc.ListAgentWorkflowRunOutcomes:     accessor.ViewerRole,
			atc.SetAgentWorkflowRunOutputOutcome: accessor.MemberRole,
		}
		for route, role := range want {
			Expect(accessor.DefaultRoles).To(HaveKeyWithValue(route, role), route)
		}
	})

	It("enforces viewer and member tiers with persisted team identities", func() {
		teamFactory := useRealTeamFactory()

		viewerAccess := accessForPersistedRole(
			teamFactory,
			accessor.DefaultRoles[atc.ListAgentWorkflowRunOutcomes],
			"workflow-outcome-viewers",
			accessor.ViewerRole,
			"workflow-outcome-viewer",
		)
		Expect(viewerAccess.IsAuthorized("workflow-outcome-viewers")).To(BeTrue())

		viewerMutationAccess := accessForPersistedRole(
			teamFactory,
			accessor.DefaultRoles[atc.SetAgentWorkflowRunOutputOutcome],
			"workflow-outcome-mutation-viewers",
			accessor.ViewerRole,
			"workflow-outcome-mutation-viewer",
		)
		Expect(viewerMutationAccess.IsAuthorized("workflow-outcome-mutation-viewers")).To(BeFalse())

		memberAccess := accessForPersistedRole(
			teamFactory,
			accessor.DefaultRoles[atc.SetAgentWorkflowRunOutputOutcome],
			"workflow-outcome-members",
			accessor.MemberRole,
			"workflow-outcome-member",
		)
		Expect(memberAccess.IsAuthorized("workflow-outcome-members")).To(BeTrue())
	})
})
