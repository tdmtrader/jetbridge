package fix

import (
	"fmt"
	"strings"

	"github.com/concourse/ci-agent/schema"
)

// BuildFixPrompt builds a prompt for the agent to generate a fix.
func BuildFixPrompt(issue schema.ProvenIssue, fileContent, testCode string) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("Fix the following %s issue:\n\n", issue.Category))
	b.WriteString(fmt.Sprintf("Issue ID: %s\n", issue.ID))
	b.WriteString(fmt.Sprintf("Title: %s\n", issue.Title))
	b.WriteString(fmt.Sprintf("Severity: %s\n", issue.Severity))
	b.WriteString(fmt.Sprintf("File: %s (line %d)\n", issue.File, issue.Line))
	b.WriteString(fmt.Sprintf("\n--- File Content: %s ---\n%s\n", issue.File, fileContent))
	b.WriteString(fmt.Sprintf("\n--- Proving Test ---\n%s\n", testCode))

	b.WriteString(`
Apply a minimal fix so the proving test passes. Do not change the test.
Output ONLY a JSON array of file patches:
[
  {"path": "relative/file.go", "content": "full file content after fix"}
]

Rules:
- Make the smallest change that fixes the issue
- Do not introduce new dependencies
- Do not change any files beyond what is needed
- Output valid JSON only, no explanation
`)

	return b.String()
}
