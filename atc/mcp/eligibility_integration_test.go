package mcp_test

import (
	"context"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	product "github.com/concourse/concourse/atc/mcp"
	"github.com/concourse/concourse/skymarshal/mcpauth"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Real PostgreSQL team authority, using claims already authenticated by the
// broker. This test belongs on the MCP side of the architecture boundary.
var _ = Describe("MCP account eligibility", func() {
	var principal mcpauth.Principal
	var factory accessor.AccessFactory
	BeforeEach(func() {
		principal = mcpauth.Principal{Claims: map[string]any{"sub": "test-sub", "federated_claims": map[string]any{"connector_id": "test", "user_id": "some-user"}}}
		factory = accessor.NewAccessFactory(accessor.NewTrustedTokenVerifier(nil), teamFactory, "sub", nil, nil)
	})

	check := func(action string, roles map[string]string) (product.AccountEligibility, error) {
		return product.AccountEligibilityForAction(context.Background(), factory, principal, roles, action)
	}

	It("preserves public metadata/output paths but prunes team writes and config reads without membership", func() {
		for _, action := range []string{atc.ListAllPipelines, atc.GetPipeline, atc.GetBuild, atc.BuildEvents, atc.ListPipelineBuilds} {
			eligibility, err := check(action, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(eligibility).To(Equal(product.AccountTargetCheckRequired), action)
		}
		for _, action := range []string{atc.GetConfig, atc.SaveConfig, atc.PausePipeline, atc.AbortBuild, atc.SetLogLevel} {
			eligibility, err := check(action, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(eligibility).To(Equal(product.AccountIneligible), action)
		}
	})

	It("uses each action's custom role and current team authority", func() {
		team := createTeam("operators")
		grantRole(team, accessor.OperatorRole)
		roles := map[string]string{atc.UnpausePipeline: accessor.OwnerRole}
		pause, err := check(atc.PausePipeline, roles)
		Expect(err).NotTo(HaveOccurred())
		Expect(pause).To(Equal(product.AccountTargetCheckRequired))
		unpause, err := check(atc.UnpausePipeline, roles)
		Expect(err).NotTo(HaveOccurred())
		Expect(unpause).To(Equal(product.AccountIneligible))
		grantRole(team, accessor.OwnerRole)
		unpause, err = check(atc.UnpausePipeline, roles)
		Expect(err).NotTo(HaveOccurred())
		Expect(unpause).To(Equal(product.AccountTargetCheckRequired))
		Expect(team.UpdateProviderAuth(atc.TeamAuth{accessor.OwnerRole: map[string][]string{"users": {"test:someone-else"}}})).To(Succeed())
		pause, err = check(atc.PausePipeline, roles)
		Expect(err).NotTo(HaveOccurred())
		Expect(pause).To(Equal(product.AccountIneligible))
	})

	It("keeps account administration separate from consent and from team ownership", func() {
		team := createTeam("owners")
		grantRole(team, accessor.OwnerRole)
		eligibility, err := check(atc.SetLogLevel, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(eligibility).To(Equal(product.AccountIneligible))
		makeAdmin(team)
		for _, action := range []string{atc.SaveConfig, atc.SetLogLevel} {
			eligibility, err = check(action, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(eligibility).To(Equal(product.AccountTargetCheckRequired))
		}
		principal.Scopes = []string{mcpauth.ScopeAdmin}
		Expect(product.AllowsAction(principal, atc.SaveConfig)).To(BeFalse())
	})

	It("reports unavailable access data as a temporary error, never a denial", func() {
		factory = accessor.NewAccessFactory(accessor.NewTrustedTokenVerifier(nil), doomedTeamFactory(), "sub", nil, nil)
		eligibility, err := check(atc.PausePipeline, nil)
		Expect(err).To(HaveOccurred())
		Expect(eligibility).NotTo(Equal(product.AccountIneligible))
	})

	It("rejects unclassified actions", func() {
		_, err := check("not-an-action", nil)
		Expect(err).To(HaveOccurred())
	})
})
