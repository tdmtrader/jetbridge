package adapter

import (
	"fmt"
	"strings"

	"github.com/concourse/ci-agent/schema"
)

// BuildPlanPrompt constructs a prompt for plan generation from a PlanningInput and spec.
func BuildPlanPrompt(input *schema.PlanningInput, specMarkdown string, opts PlanOpts) string {
	var sb strings.Builder

	sb.WriteString("Generate a detailed implementation plan for the following story.\n\n")
	sb.WriteString(fmt.Sprintf("## Title\n%s\n\n", input.Title))
	sb.WriteString(fmt.Sprintf("## Description\n%s\n\n", input.Description))

	if len(input.AcceptanceCriteria) > 0 {
		sb.WriteString("## Acceptance Criteria\n")
		for _, ac := range input.AcceptanceCriteria {
			sb.WriteString(fmt.Sprintf("- %s\n", ac))
		}
		sb.WriteString("\n")
	}

	if input.Context != nil {
		hasContext := false
		if input.Context.Repo != "" || input.Context.Language != "" || len(input.Context.RelatedFiles) > 0 {
			sb.WriteString("## Context\n")
			hasContext = true
		}
		if input.Context.Repo != "" {
			sb.WriteString(fmt.Sprintf("- Repository: %s\n", input.Context.Repo))
		}
		if input.Context.Language != "" {
			sb.WriteString(fmt.Sprintf("- Language: %s\n", input.Context.Language))
		}
		if len(input.Context.RelatedFiles) > 0 {
			sb.WriteString(fmt.Sprintf("- Related files: %s\n", strings.Join(input.Context.RelatedFiles, ", ")))
		}
		if hasContext {
			sb.WriteString("\n")
		}
	}

	sb.WriteString(fmt.Sprintf("## Specification\n%s\n\n", specMarkdown))

	sb.WriteString(`Respond with a JSON object containing:
{
  "plan_markdown": "<full markdown implementation plan>",
  "phases": [
    {
      "name": "<phase name>",
      "tasks": [
        {"description": "<task description>", "files": ["<file1>", ...]}
      ]
    }
  ],
  "key_files": [
    {"path": "<file path>", "change": "NEW|MODIFY"}
  ],
  "risks": ["<risk1>", ...]
}
`)

	return sb.String()
}
