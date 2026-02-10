package adapter_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/plan/adapter"
	"github.com/concourse/ci-agent/schema"
)

var _ = Describe("BuildPlanPrompt", func() {
	It("includes title and description", func() {
		input := &schema.PlanningInput{
			Title:       "Add auth",
			Description: "Implement JWT auth",
		}
		prompt := adapter.BuildPlanPrompt(input, "# Spec", adapter.PlanOpts{})
		Expect(prompt).To(ContainSubstring("Add auth"))
		Expect(prompt).To(ContainSubstring("Implement JWT auth"))
	})

	It("includes full spec markdown", func() {
		input := &schema.PlanningInput{
			Title:       "Task",
			Description: "Desc",
		}
		specMd := "# Full Spec\n\nDetailed specification content here."
		prompt := adapter.BuildPlanPrompt(input, specMd, adapter.PlanOpts{})
		Expect(prompt).To(ContainSubstring("## Specification"))
		Expect(prompt).To(ContainSubstring("Full Spec"))
		Expect(prompt).To(ContainSubstring("Detailed specification content here."))
	})

	It("includes acceptance criteria when present", func() {
		input := &schema.PlanningInput{
			Title:       "Task",
			Description: "Desc",
			AcceptanceCriteria: []string{
				"AC 1",
				"AC 2",
			},
		}
		prompt := adapter.BuildPlanPrompt(input, "spec", adapter.PlanOpts{})
		Expect(prompt).To(ContainSubstring("Acceptance Criteria"))
		Expect(prompt).To(ContainSubstring("AC 1"))
		Expect(prompt).To(ContainSubstring("AC 2"))
	})

	It("includes context when present", func() {
		input := &schema.PlanningInput{
			Title:       "Task",
			Description: "Desc",
			Context: &schema.PlanningContext{
				Repo:         "https://github.com/org/repo",
				Language:     "go",
				RelatedFiles: []string{"main.go"},
			},
		}
		prompt := adapter.BuildPlanPrompt(input, "spec", adapter.PlanOpts{})
		Expect(prompt).To(ContainSubstring("https://github.com/org/repo"))
		Expect(prompt).To(ContainSubstring("go"))
		Expect(prompt).To(ContainSubstring("main.go"))
	})

	It("specifies JSON output format for PlanOutput", func() {
		input := &schema.PlanningInput{
			Title:       "Task",
			Description: "Desc",
		}
		prompt := adapter.BuildPlanPrompt(input, "spec", adapter.PlanOpts{})
		Expect(prompt).To(ContainSubstring("plan_markdown"))
		Expect(prompt).To(ContainSubstring("phases"))
		Expect(prompt).To(ContainSubstring("key_files"))
		Expect(prompt).To(ContainSubstring("risks"))
	})
})
