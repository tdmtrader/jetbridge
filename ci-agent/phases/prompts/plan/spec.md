Generate a detailed technical specification for the following story.

## Title
{{.Env.title}}

## Description
{{.Env.description}}

{{if .Env.acceptance_criteria}}
## Acceptance Criteria
{{.Env.acceptance_criteria}}
{{end}}

{{if .Env.repo}}
## Context
- Repository: {{.Env.repo}}
{{end}}
{{if .Env.language}}
- Language: {{.Env.language}}
{{end}}

Respond with a JSON object containing:
{
  "spec_markdown": "<full markdown specification>",
  "unresolved_questions": ["<question1>", ...],
  "assumptions": ["<assumption1>", ...],
  "out_of_scope": ["<item1>", ...]
}
