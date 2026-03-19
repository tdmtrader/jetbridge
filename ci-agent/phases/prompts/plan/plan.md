Generate a detailed implementation plan for the following story.

## Title
{{.Env.title}}

## Description
{{.Env.description}}

{{if .Env.acceptance_criteria}}
## Acceptance Criteria
{{.Env.acceptance_criteria}}
{{end}}

## Specification
{{index .StepOutputs "generate-spec"}}

Respond with a JSON object containing:
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
