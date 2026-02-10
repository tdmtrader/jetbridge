package adapter_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/plan/adapter"
	"github.com/concourse/ci-agent/schema"
)

var _ = Describe("BuildSpecPrompt", func() {
	It("includes title and description", func() {
		input := &schema.PlanningInput{
			Title:       "Add auth",
			Description: "Implement JWT auth for the API",
		}
		prompt := adapter.BuildSpecPrompt(input, adapter.SpecOpts{})
		Expect(prompt).To(ContainSubstring("Add auth"))
		Expect(prompt).To(ContainSubstring("Implement JWT auth for the API"))
	})

	It("includes acceptance criteria when present", func() {
		input := &schema.PlanningInput{
			Title:       "Add auth",
			Description: "Implement JWT auth",
			AcceptanceCriteria: []string{
				"Users can log in",
				"Tokens expire in 24h",
			},
		}
		prompt := adapter.BuildSpecPrompt(input, adapter.SpecOpts{})
		Expect(prompt).To(ContainSubstring("Users can log in"))
		Expect(prompt).To(ContainSubstring("Tokens expire in 24h"))
		Expect(prompt).To(ContainSubstring("Acceptance Criteria"))
	})

	It("includes context when present", func() {
		input := &schema.PlanningInput{
			Title:       "Add auth",
			Description: "Implement JWT auth",
			Context: &schema.PlanningContext{
				Repo:         "https://github.com/org/repo",
				Language:     "go",
				RelatedFiles: []string{"auth/handler.go", "middleware/jwt.go"},
			},
		}
		prompt := adapter.BuildSpecPrompt(input, adapter.SpecOpts{})
		Expect(prompt).To(ContainSubstring("https://github.com/org/repo"))
		Expect(prompt).To(ContainSubstring("go"))
		Expect(prompt).To(ContainSubstring("auth/handler.go"))
	})

	It("specifies JSON output format", func() {
		input := &schema.PlanningInput{
			Title:       "Task",
			Description: "Do something",
		}
		prompt := adapter.BuildSpecPrompt(input, adapter.SpecOpts{})
		Expect(prompt).To(ContainSubstring("spec_markdown"))
		Expect(prompt).To(ContainSubstring("unresolved_questions"))
		Expect(prompt).To(ContainSubstring("assumptions"))
		Expect(prompt).To(ContainSubstring("out_of_scope"))
	})

	It("omits empty optional fields", func() {
		input := &schema.PlanningInput{
			Title:       "Task",
			Description: "Do something",
		}
		prompt := adapter.BuildSpecPrompt(input, adapter.SpecOpts{})
		Expect(prompt).NotTo(ContainSubstring("Acceptance Criteria"))
		Expect(prompt).NotTo(ContainSubstring("Context"))
		Expect(prompt).NotTo(ContainSubstring("Repository"))
	})
})
