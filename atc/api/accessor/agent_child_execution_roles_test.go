package accessor_test

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Agent child-execution roles", func() {
	// GetAgentChildExecution is classified into the CheckAuthorizationHandler
	// tier in atc/wrappa/api_auth_wrappa.go. A missing DefaultRoles entry
	// resolves to requiredRole "" and silently makes this route admin-only.
	It("has an explicit team-viewer route mapping", func() {
		Expect(accessor.DefaultRoles).To(HaveKeyWithValue(atc.GetAgentChildExecution, accessor.ViewerRole))
	})

	It("admits owning-team roles and rejects an unrelated team", func() {
		teamFactory := useRealTeamFactory()

		roleCases := []struct {
			role     string
			teamName string
			userID   string
		}{
			{role: accessor.ViewerRole, teamName: "child-execution-viewers", userID: "child-execution-viewer"},
			{role: accessor.MemberRole, teamName: "child-execution-members", userID: "child-execution-member"},
			{role: accessor.OwnerRole, teamName: "child-execution-owners", userID: "child-execution-owner"},
		}

		for _, roleCase := range roleCases {
			access := accessForPersistedRole(
				teamFactory,
				accessor.DefaultRoles[atc.GetAgentChildExecution],
				roleCase.teamName,
				roleCase.role,
				roleCase.userID,
			)
			Expect(access.IsAuthorized(roleCase.teamName)).To(
				BeTrue(),
				"expected %s to inspect child executions owned by %s",
				roleCase.role,
				roleCase.teamName,
			)
		}

		foreignAccess := accessForPersistedRole(
			teamFactory,
			accessor.DefaultRoles[atc.GetAgentChildExecution],
			"child-execution-foreign-owners",
			accessor.OwnerRole,
			"child-execution-foreign-owner",
		)
		Expect(foreignAccess.IsAuthorized("child-execution-viewers")).To(BeFalse())
	})
})
