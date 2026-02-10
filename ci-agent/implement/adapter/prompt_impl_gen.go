package adapter

import (
	"fmt"
	"strings"
)

// BuildImplPrompt constructs a prompt for generating implementation code.
func BuildImplPrompt(req CodeGenRequest, testCode string, testOutput string) string {
	var b strings.Builder

	b.WriteString("# Task: Write Implementation Code\n\n")
	b.WriteString("## Task Description\n\n")
	b.WriteString(req.TaskDescription)
	b.WriteString("\n\n")

	if req.SpecContext != "" {
		b.WriteString("## Specification Context\n\n")
		b.WriteString(req.SpecContext)
		b.WriteString("\n\n")
	}

	b.WriteString("## Failing Test\n\n")
	b.WriteString("```go\n")
	b.WriteString(testCode)
	b.WriteString("\n```\n\n")

	b.WriteString("## Test Output (Failure)\n\n")
	b.WriteString("```\n")
	b.WriteString(testOutput)
	b.WriteString("\n```\n\n")

	if len(req.TargetFiles) > 0 {
		b.WriteString("## Target Files\n\n")
		for _, f := range req.TargetFiles {
			fmt.Fprintf(&b, "- %s\n", f)
		}
		b.WriteString("\n")
	}

	if req.PriorContext != "" {
		b.WriteString("## Prior Context\n\n")
		b.WriteString(req.PriorContext)
		b.WriteString("\n\n")
	}

	b.WriteString("## Instructions\n\n")
	b.WriteString("Write the MINIMUM Go code to make the failing test pass.\n")
	b.WriteString("- DO NOT modify the test file.\n")
	b.WriteString("- DO NOT add functionality beyond what the test requires.\n")
	b.WriteString("- Create new files or modify existing files as needed.\n\n")

	b.WriteString("## Output Format\n\n")
	b.WriteString("Respond with ONLY a JSON object:\n")
	b.WriteString("```json\n")
	b.WriteString("{\n")
	b.WriteString("  \"patches\": [\n")
	b.WriteString("    {\n")
	b.WriteString("      \"path\": \"<relative path from repo root>\",\n")
	b.WriteString("      \"content\": \"<full file content>\"\n")
	b.WriteString("    }\n")
	b.WriteString("  ]\n")
	b.WriteString("}\n")
	b.WriteString("```\n")

	return b.String()
}
