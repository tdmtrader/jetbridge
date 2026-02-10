package browserplan_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/browserplan"
	"github.com/concourse/ci-agent/specparser"
)

var _ = Describe("GenerateBrowserPlan", func() {
	It("generates plan for UI-relevant requirements", func() {
		spec := &specparser.Spec{
			AcceptanceCriteria: []specparser.AcceptanceCriterion{
				{ID: "AC1", Text: "Login page displays email and password fields"},
				{ID: "AC2", Text: "Dashboard shows user stats"},
			},
		}
		plan, err := browserplan.GenerateBrowserPlan(context.Background(), nil, spec, nil, "http://localhost:3000")
		Expect(err).NotTo(HaveOccurred())
		Expect(plan).To(ContainSubstring("Browser QA Plan"))
		Expect(plan).To(ContainSubstring("Login page"))
		Expect(plan).To(ContainSubstring("Dashboard"))
		Expect(plan).To(ContainSubstring("http://localhost:3000"))
	})

	It("returns empty when no UI requirements exist", func() {
		spec := &specparser.Spec{
			Requirements: []specparser.Requirement{
				{ID: "R1", Text: "API rate limiting is enforced"},
				{ID: "R2", Text: "Database connections are pooled"},
			},
		}
		plan, err := browserplan.GenerateBrowserPlan(context.Background(), nil, spec, nil, "http://localhost:3000")
		Expect(err).NotTo(HaveOccurred())
		Expect(plan).To(BeEmpty())
	})

	It("includes flow numbers and step structure", func() {
		spec := &specparser.Spec{
			AcceptanceCriteria: []specparser.AcceptanceCriterion{
				{ID: "AC1", Text: "Click button to submit form"},
			},
		}
		plan, err := browserplan.GenerateBrowserPlan(context.Background(), nil, spec, nil, "http://localhost:3000")
		Expect(err).NotTo(HaveOccurred())
		Expect(plan).To(ContainSubstring("## Flow 1"))
		Expect(plan).To(ContainSubstring("**Verifies:** AC1"))
		Expect(plan).To(ContainSubstring("Steps"))
	})

	It("references target URL in plan", func() {
		spec := &specparser.Spec{
			AcceptanceCriteria: []specparser.AcceptanceCriterion{
				{ID: "AC1", Text: "Page loads correctly"},
			},
		}
		plan, err := browserplan.GenerateBrowserPlan(context.Background(), nil, spec, nil, "https://app.example.com")
		Expect(err).NotTo(HaveOccurred())
		Expect(plan).To(ContainSubstring("https://app.example.com"))
	})

	It("skips non-UI requirements", func() {
		spec := &specparser.Spec{
			Requirements: []specparser.Requirement{
				{ID: "R1", Text: "API returns 200"},
			},
			AcceptanceCriteria: []specparser.AcceptanceCriterion{
				{ID: "AC1", Text: "User sees login button"},
			},
		}
		plan, err := browserplan.GenerateBrowserPlan(context.Background(), nil, spec, nil, "http://localhost")
		Expect(err).NotTo(HaveOccurred())
		Expect(plan).To(ContainSubstring("login button"))
		Expect(plan).NotTo(ContainSubstring("API returns"))
	})
})
