package adapter

import (
	"fmt"
	"strings"
)

// BuildTestPrompt constructs a prompt for generating a failing test.
func BuildTestPrompt(req CodeGenRequest) string {
	var b strings.Builder

	b.WriteString("# Task: Write a Failing Test\n\n")
	b.WriteString("## Task Description\n\n")
	b.WriteString(req.TaskDescription)
	b.WriteString("\n\n")

	if req.SpecContext != "" {
		b.WriteString("## Specification Context\n\n")
		b.WriteString(req.SpecContext)
		b.WriteString("\n\n")
	}

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
	b.WriteString("Write a Go test file using Ginkgo v2 and Gomega that:\n")
	b.WriteString("1. Tests the behavior described in the task description.\n")
	b.WriteString("2. MUST FAIL against the current codebase (TDD red phase).\n")
	b.WriteString("3. Uses `Describe`/`Context`/`It` blocks.\n")
	b.WriteString("4. Covers happy path and key edge cases.\n\n")

	b.WriteString("## Output Format\n\n")
	b.WriteString("Respond with ONLY a JSON object:\n")
	b.WriteString("```json\n")
	b.WriteString("{\n")
	b.WriteString("  \"test_file_path\": \"<relative path from repo root>\",\n")
	b.WriteString("  \"test_content\": \"<full test file content>\",\n")
	b.WriteString("  \"package_name\": \"<package name>\"\n")
	b.WriteString("}\n")
	b.WriteString("```\n")

	return b.String()
}
