package fix_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/fix"
	"github.com/concourse/ci-agent/schema"
)

var _ = Describe("BuildFixPrompt", func() {
	It("includes issue description and file content", func() {
		issue := schema.ProvenIssue{
			ID:       "ISS-001",
			Severity: schema.SeverityHigh,
			Title:    "nil pointer dereference in handler",
			File:     "handler.go",
			Line:     42,
			Category: schema.CategoryCorrectness,
		}
		fileContent := "package handler\n\nfunc Handle(r *Request) {}\n"
		testCode := "package handler\n\nfunc TestHandle(t *testing.T) {}\n"

		prompt := fix.BuildFixPrompt(issue, fileContent, testCode)
		Expect(prompt).To(ContainSubstring("ISS-001"))
		Expect(prompt).To(ContainSubstring("nil pointer dereference"))
		Expect(prompt).To(ContainSubstring("handler.go"))
		Expect(prompt).To(ContainSubstring("func Handle"))
		Expect(prompt).To(ContainSubstring("TestHandle"))
	})

	It("includes JSON output format", func() {
		issue := schema.ProvenIssue{
			ID: "ISS-002", Severity: schema.SeverityMedium, Title: "bug",
			File: "a.go", Line: 1, Category: schema.CategorySecurity,
		}
		prompt := fix.BuildFixPrompt(issue, "code", "test")
		Expect(prompt).To(ContainSubstring("JSON"))
		Expect(prompt).To(ContainSubstring("path"))
		Expect(prompt).To(ContainSubstring("content"))
	})

	It("varies instruction by category", func() {
		secIssue := schema.ProvenIssue{
			ID: "ISS-003", Severity: schema.SeverityCritical, Title: "injection",
			File: "q.go", Line: 5, Category: schema.CategorySecurity,
		}
		correctIssue := schema.ProvenIssue{
			ID: "ISS-004", Severity: schema.SeverityHigh, Title: "off by one",
			File: "loop.go", Line: 10, Category: schema.CategoryCorrectness,
		}

		secPrompt := fix.BuildFixPrompt(secIssue, "code", "test")
		correctPrompt := fix.BuildFixPrompt(correctIssue, "code", "test")

		Expect(secPrompt).To(ContainSubstring("security"))
		Expect(correctPrompt).To(ContainSubstring("correctness"))
	})
})
