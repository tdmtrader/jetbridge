package plan_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/plan"
	"github.com/concourse/ci-agent/plan/adapter"
	"github.com/concourse/ci-agent/schema"
)

var _ = Describe("RenderSpec", func() {
	It("renders with H1 title from input", func() {
		input := &schema.PlanningInput{
			Title:       "Add Authentication",
			Description: "Implement JWT auth",
		}
		spec := &adapter.SpecOutput{
			SpecMarkdown: "This is the spec content.",
		}
		result := plan.RenderSpec(input, spec)
		Expect(result).To(HavePrefix("# Add Authentication\n"))
	})

	It("includes Overview section with spec content", func() {
		input := &schema.PlanningInput{Title: "T", Description: "D"}
		spec := &adapter.SpecOutput{SpecMarkdown: "The detailed spec."}
		result := plan.RenderSpec(input, spec)
		Expect(result).To(ContainSubstring("## Overview"))
		Expect(result).To(ContainSubstring("The detailed spec."))
	})

	It("includes Acceptance Criteria as checkbox list", func() {
		input := &schema.PlanningInput{
			Title:       "T",
			Description: "D",
			AcceptanceCriteria: []string{
				"Users can log in",
				"Tokens expire",
			},
		}
		spec := &adapter.SpecOutput{SpecMarkdown: "Spec content"}
		result := plan.RenderSpec(input, spec)
		Expect(result).To(ContainSubstring("## Acceptance Criteria"))
		Expect(result).To(ContainSubstring("- [ ] Users can log in"))
		Expect(result).To(ContainSubstring("- [ ] Tokens expire"))
	})

	It("includes Assumptions section when present", func() {
		input := &schema.PlanningInput{Title: "T", Description: "D"}
		spec := &adapter.SpecOutput{
			SpecMarkdown: "Content",
			Assumptions:  []string{"Database exists", "API is RESTful"},
		}
		result := plan.RenderSpec(input, spec)
		Expect(result).To(ContainSubstring("## Assumptions"))
		Expect(result).To(ContainSubstring("- Database exists"))
	})

	It("includes Out of Scope section when present", func() {
		input := &schema.PlanningInput{Title: "T", Description: "D"}
		spec := &adapter.SpecOutput{
			SpecMarkdown: "Content",
			OutOfScope:   []string{"OAuth integration"},
		}
		result := plan.RenderSpec(input, spec)
		Expect(result).To(ContainSubstring("## Out of Scope"))
		Expect(result).To(ContainSubstring("- OAuth integration"))
	})

	It("includes Unresolved Questions section when present", func() {
		input := &schema.PlanningInput{Title: "T", Description: "D"}
		spec := &adapter.SpecOutput{
			SpecMarkdown:        "Content",
			UnresolvedQuestions: []string{"What auth provider?"},
		}
		result := plan.RenderSpec(input, spec)
		Expect(result).To(ContainSubstring("## Unresolved Questions"))
		Expect(result).To(ContainSubstring("- What auth provider?"))
	})

	It("omits Unresolved Questions when empty", func() {
		input := &schema.PlanningInput{Title: "T", Description: "D"}
		spec := &adapter.SpecOutput{SpecMarkdown: "Content"}
		result := plan.RenderSpec(input, spec)
		Expect(result).NotTo(ContainSubstring("Unresolved Questions"))
	})

	It("omits Assumptions when empty", func() {
		input := &schema.PlanningInput{Title: "T", Description: "D"}
		spec := &adapter.SpecOutput{SpecMarkdown: "Content"}
		result := plan.RenderSpec(input, spec)
		Expect(result).NotTo(ContainSubstring("Assumptions"))
	})

	It("omits Out of Scope when empty", func() {
		input := &schema.PlanningInput{Title: "T", Description: "D"}
		spec := &adapter.SpecOutput{SpecMarkdown: "Content"}
		result := plan.RenderSpec(input, spec)
		Expect(result).NotTo(ContainSubstring("Out of Scope"))
	})
})
