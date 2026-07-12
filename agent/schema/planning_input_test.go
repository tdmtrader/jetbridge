package schema_test

import (
	"encoding/json"
	"testing"

	schema "github.com/concourse/concourse/agent/schema"
)

func validPlanningInput() schema.PlanningInput {
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

func TestPlanningInputJSONRoundTrip(t *testing.T) {
	t.Run("marshals and unmarshals correctly", func(t *testing.T) {
		input := validPlanningInput()
		data, err := json.Marshal(input)
		requireNoErr(t, err)

		var decoded schema.PlanningInput
		requireNoErr(t, json.Unmarshal(data, &decoded))

		requireEqual(t, decoded.Title, "Add user authentication")
		requireEqual(t, decoded.Type, schema.StoryFeature)
		requireEqual(t, decoded.Priority, schema.PriorityHigh)
		requireLen(t, decoded.Labels, 2)
		requireLen(t, decoded.AcceptanceCriteria, 2)
		requireEqual(t, decoded.Context.Language, "go")
		requireLen(t, decoded.Context.RelatedFiles, 2)
	})

	t.Run("handles minimal input (title + description only)", func(t *testing.T) {
		input := schema.PlanningInput{
			Title:       "Simple task",
			Description: "Do a thing",
		}
		data, _ := json.Marshal(input)
		var decoded schema.PlanningInput
		requireNoErr(t, json.Unmarshal(data, &decoded))
		requireEqual(t, decoded.Title, "Simple task")
		requireTrue(t, decoded.Context == nil, "context should be nil")
	})

	t.Run("ignores unknown JSON fields", func(t *testing.T) {
		raw := `{"title":"test","description":"desc","unknown_field":"value"}`
		var input schema.PlanningInput
		requireNoErr(t, json.Unmarshal([]byte(raw), &input))
		requireEqual(t, input.Title, "test")
	})
}

func TestPlanningInputValidate(t *testing.T) {
	t.Run("passes for valid input", func(t *testing.T) {
		input := validPlanningInput()
		requireNoErr(t, input.Validate())
	})

	t.Run("passes for minimal input", func(t *testing.T) {
		input := schema.PlanningInput{
			Title:       "A task",
			Description: "Description here",
		}
		requireNoErr(t, input.Validate())
	})

	t.Run("requires title", func(t *testing.T) {
		input := validPlanningInput()
		input.Title = ""
		requireErrContains(t, input.Validate(), "title")
	})

	t.Run("rejects whitespace-only title", func(t *testing.T) {
		input := validPlanningInput()
		input.Title = "   "
		requireErrContains(t, input.Validate(), "title")
	})

	t.Run("requires description", func(t *testing.T) {
		input := validPlanningInput()
		input.Description = ""
		requireErrContains(t, input.Validate(), "description")
	})

	t.Run("rejects whitespace-only description", func(t *testing.T) {
		input := validPlanningInput()
		input.Description = "  \t  "
		requireErrContains(t, input.Validate(), "description")
	})

	t.Run("rejects invalid type", func(t *testing.T) {
		input := validPlanningInput()
		input.Type = "invalid"
		requireErrContains(t, input.Validate(), "invalid type")
	})

	t.Run("rejects invalid priority", func(t *testing.T) {
		input := validPlanningInput()
		input.Priority = "urgent"
		requireErrContains(t, input.Validate(), "invalid priority")
	})

	t.Run("accepts all valid story types", func(t *testing.T) {
		for _, ty := range []schema.StoryType{
			schema.StoryFeature, schema.StoryBug, schema.StoryChore, schema.StorySpike,
		} {
			input := validPlanningInput()
			input.Type = ty
			requireNoErr(t, input.Validate())
		}
	})

	t.Run("accepts all valid priorities", func(t *testing.T) {
		for _, p := range []schema.Priority{
			schema.PriorityCritical, schema.PriorityHigh, schema.PriorityMedium, schema.PriorityLow,
		} {
			input := validPlanningInput()
			input.Priority = p
			requireNoErr(t, input.Validate())
		}
	})
}
