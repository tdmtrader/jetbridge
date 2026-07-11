package devmcp_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/devmcp"
)

var _ = Describe("the repo's own dev-mcp.yml", func() {
	It("parses and declares the five contract components", func() {
		cfg, err := devmcp.Load("../../dev-mcp.yml")
		Expect(err).NotTo(HaveOccurred())

		var ids []string
		for _, comp := range cfg.Components {
			ids = append(ids, comp.ID)
		}
		Expect(ids).To(ConsistOf("atc", "fly", "web", "ci-agent", "topgun"))

		Expect(cfg.Repo).NotTo(BeNil())
		Expect(cfg.Repo.Build).NotTo(BeNil())
		Expect(cfg.Repo.Test).NotTo(BeNil())

		// every component has test; topgun is test-only (no Makefile
		// build/lint target exists for it — pinned in the §11 addendum)
		for _, comp := range cfg.Components {
			Expect(comp.Test).NotTo(BeNil(), "component %s must define test", comp.ID)
		}
		atc, _ := cfg.Component("atc")
		Expect(atc.Test.FocusFlag).To(Equal("--focus"))
		ciAgent, _ := cfg.Component("ci-agent")
		Expect(ciAgent.Test.Dir).To(Equal("ci-agent"))
	})
})
