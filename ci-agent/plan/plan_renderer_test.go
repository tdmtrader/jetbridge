package plan_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/plan"
	"github.com/concourse/ci-agent/plan/adapter"
	"github.com/concourse/ci-agent/schema"
)

var _ = Describe("RenderPlan", func() {
	fullPlan := func() *adapter.PlanOutput {
		return &adapter.PlanOutput{
			PlanMarkdown: "Full plan markdown",
			Phases: []adapter.Phase{
				{
					Name: "Phase 1: Setup",
					Tasks: []adapter.Task{
						{Description: "Create project structure", Files: []string{"main.go", "go.mod"}},
						{Description: "Add configuration", Files: []string{"config.go"}},
					},
				},
				{
					Name: "Phase 2: Implementation",
					Tasks: []adapter.Task{
						{Description: "Implement handler"},
						{Description: "Add middleware", Files: []string{"middleware.go"}},
					},
				},
			},
			KeyFiles: []adapter.KeyFile{
				{Path: "main.go", Change: "NEW"},
				{Path: "handler.go", Change: "MODIFY"},
			},
			Risks: []string{
				"Breaking change to API",
				"Performance regression possible",
			},
		}
	}

	It("renders phases as H2 sections", func() {
		input := &schema.PlanningInput{Title: "Feature X", Description: "D"}
		result := plan.RenderPlan(input, fullPlan())
		Expect(result).To(ContainSubstring("## Phase 1: Setup"))
		Expect(result).To(ContainSubstring("## Phase 2: Implementation"))
	})

	It("renders tasks as checkbox lists", func() {
		input := &schema.PlanningInput{Title: "Feature X", Description: "D"}
		result := plan.RenderPlan(input, fullPlan())
		Expect(result).To(ContainSubstring("- [ ] Create project structure"))
		Expect(result).To(ContainSubstring("- [ ] Add configuration"))
		Expect(result).To(ContainSubstring("- [ ] Implement handler"))
	})

	It("includes file references in tasks", func() {
		input := &schema.PlanningInput{Title: "Feature X", Description: "D"}
		result := plan.RenderPlan(input, fullPlan())
		Expect(result).To(ContainSubstring("Files: main.go, go.mod"))
		Expect(result).To(ContainSubstring("Files: middleware.go"))
	})

	It("renders key files table", func() {
		input := &schema.PlanningInput{Title: "Feature X", Description: "D"}
		result := plan.RenderPlan(input, fullPlan())
		Expect(result).To(ContainSubstring("## Key Files"))
		Expect(result).To(ContainSubstring("| `main.go` | NEW |"))
		Expect(result).To(ContainSubstring("| `handler.go` | MODIFY |"))
	})

	It("renders risks section", func() {
		input := &schema.PlanningInput{Title: "Feature X", Description: "D"}
		result := plan.RenderPlan(input, fullPlan())
		Expect(result).To(ContainSubstring("## Risks"))
		Expect(result).To(ContainSubstring("- Breaking change to API"))
		Expect(result).To(ContainSubstring("- Performance regression possible"))
	})

	It("omits key files when empty", func() {
		input := &schema.PlanningInput{Title: "T", Description: "D"}
		p := &adapter.PlanOutput{
			PlanMarkdown: "plan",
			Phases:       []adapter.Phase{{Name: "P1", Tasks: []adapter.Task{{Description: "task"}}}},
		}
		result := plan.RenderPlan(input, p)
		Expect(result).NotTo(ContainSubstring("Key Files"))
	})

	It("omits risks when empty", func() {
		input := &schema.PlanningInput{Title: "T", Description: "D"}
		p := &adapter.PlanOutput{
			PlanMarkdown: "plan",
			Phases:       []adapter.Phase{{Name: "P1", Tasks: []adapter.Task{{Description: "task"}}}},
		}
		result := plan.RenderPlan(input, p)
		Expect(result).NotTo(ContainSubstring("Risks"))
	})

	It("includes H1 title from input", func() {
		input := &schema.PlanningInput{Title: "My Feature", Description: "D"}
		p := &adapter.PlanOutput{
			PlanMarkdown: "plan",
			Phases:       []adapter.Phase{{Name: "P1", Tasks: []adapter.Task{{Description: "task"}}}},
		}
		result := plan.RenderPlan(input, p)
		Expect(result).To(HavePrefix("# Implementation Plan: My Feature\n"))
	})
})
