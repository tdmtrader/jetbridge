package schema_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/schema"
)

var _ = Describe("PlanningInput", func() {
	validInput := func() schema.PlanningInput {
		return schema.PlanningInput{
			Title:       "Add user authentication",
			Description: "Implement JWT-based authentication for the API",
			Type:        schema.StoryFeature,
			Priority:    schema.PriorityHigh,
			Labels:      []string{"security", "api"},
			AcceptanceCriteria: []string{
				"Users can log in with email/password",
				"JWT tokens expire after 24 hours",
			},
			Context: &schema.PlanningContext{
				Repo:         "https://github.com/org/repo.git",
				Language:     "go",
				RelatedFiles: []string{"auth/handler.go", "middleware/jwt.go"},
			},
		}
	}

	Describe("JSON round-trip", func() {
		It("marshals and unmarshals correctly", func() {
			input := validInput()
			data, err := json.Marshal(input)
			Expect(err).NotTo(HaveOccurred())

			var decoded schema.PlanningInput
			Expect(json.Unmarshal(data, &decoded)).To(Succeed())

			Expect(decoded.Title).To(Equal("Add user authentication"))
			Expect(decoded.Type).To(Equal(schema.StoryFeature))
			Expect(decoded.Priority).To(Equal(schema.PriorityHigh))
			Expect(decoded.Labels).To(HaveLen(2))
			Expect(decoded.AcceptanceCriteria).To(HaveLen(2))
			Expect(decoded.Context.Language).To(Equal("go"))
			Expect(decoded.Context.RelatedFiles).To(HaveLen(2))
		})

		It("handles minimal input (title + description only)", func() {
			input := schema.PlanningInput{
				Title:       "Simple task",
				Description: "Do a thing",
			}
			data, _ := json.Marshal(input)
			var decoded schema.PlanningInput
			Expect(json.Unmarshal(data, &decoded)).To(Succeed())
			Expect(decoded.Title).To(Equal("Simple task"))
			Expect(decoded.Context).To(BeNil())
		})

		It("ignores unknown JSON fields", func() {
			raw := `{"title":"test","description":"desc","unknown_field":"value"}`
			var input schema.PlanningInput
			Expect(json.Unmarshal([]byte(raw), &input)).To(Succeed())
			Expect(input.Title).To(Equal("test"))
		})
	})

	Describe("Validate", func() {
		It("passes for valid input", func() {
			input := validInput()
			Expect(input.Validate()).To(Succeed())
		})

		It("passes for minimal input", func() {
			input := schema.PlanningInput{
				Title:       "A task",
				Description: "Description here",
			}
			Expect(input.Validate()).To(Succeed())
		})

		It("requires title", func() {
			input := validInput()
			input.Title = ""
			Expect(input.Validate()).To(MatchError(ContainSubstring("title")))
		})

		It("rejects whitespace-only title", func() {
			input := validInput()
			input.Title = "   "
			Expect(input.Validate()).To(MatchError(ContainSubstring("title")))
		})

		It("requires description", func() {
			input := validInput()
			input.Description = ""
			Expect(input.Validate()).To(MatchError(ContainSubstring("description")))
		})

		It("rejects whitespace-only description", func() {
			input := validInput()
			input.Description = "  \t  "
			Expect(input.Validate()).To(MatchError(ContainSubstring("description")))
		})

		It("rejects invalid type", func() {
			input := validInput()
			input.Type = "invalid"
			Expect(input.Validate()).To(MatchError(ContainSubstring("invalid type")))
		})

		It("rejects invalid priority", func() {
			input := validInput()
			input.Priority = "urgent"
			Expect(input.Validate()).To(MatchError(ContainSubstring("invalid priority")))
		})

		It("accepts all valid story types", func() {
			for _, t := range []schema.StoryType{
				schema.StoryFeature, schema.StoryBug, schema.StoryChore, schema.StorySpike,
			} {
				input := validInput()
				input.Type = t
				Expect(input.Validate()).To(Succeed())
			}
		})

		It("accepts all valid priorities", func() {
			for _, p := range []schema.Priority{
				schema.PriorityCritical, schema.PriorityHigh, schema.PriorityMedium, schema.PriorityLow,
			} {
				input := validInput()
				input.Priority = p
				Expect(input.Validate()).To(Succeed())
			}
		})
	})
})
