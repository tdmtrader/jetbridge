package adapter_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/adapter"
	"github.com/concourse/ci-agent/schema"
)

var _ = Describe("ParseFindings", func() {
	It("parses valid structured JSON into findings", func() {
		raw := []byte(`[
			{
				"title": "nil pointer dereference",
				"description": "Handler does not check for nil request body",
				"file": "handler.go",
				"line": 42,
				"severity_hint": "high",
				"category": "correctness",
				"test_code": "package handler\n\nimport \"testing\"\n\nfunc TestNil(t *testing.T) {}",
				"test_file": "handler_test.go",
				"test_name": "TestNil"
			}
		]`)

		findings, err := adapter.ParseFindings(raw)
		Expect(err).NotTo(HaveOccurred())
		Expect(findings).To(HaveLen(1))
		Expect(findings[0].Title).To(Equal("nil pointer dereference"))
		Expect(findings[0].File).To(Equal("handler.go"))
		Expect(findings[0].Line).To(Equal(42))
		Expect(findings[0].SeverityHint).To(Equal(schema.SeverityHigh))
		Expect(findings[0].Category).To(Equal(schema.CategoryCorrectness))
		Expect(findings[0].TestCode).To(ContainSubstring("TestNil"))
		Expect(findings[0].TestFile).To(Equal("handler_test.go"))
		Expect(findings[0].TestName).To(Equal("TestNil"))
	})

	It("parses multiple findings", func() {
		raw := []byte(`[
			{
				"title": "issue A",
				"file": "a.go",
				"line": 1,
				"severity_hint": "low",
				"category": "testing",
				"test_code": "code",
				"test_file": "a_test.go",
				"test_name": "TestA"
			},
			{
				"title": "issue B",
				"file": "b.go",
				"line": 2,
				"severity_hint": "critical",
				"category": "security",
				"test_code": "code",
				"test_file": "b_test.go",
				"test_name": "TestB"
			}
		]`)

		findings, err := adapter.ParseFindings(raw)
		Expect(err).NotTo(HaveOccurred())
		Expect(findings).To(HaveLen(2))
	})

	It("marks finding as observation when test_code is missing", func() {
		raw := []byte(`[
			{
				"title": "style issue",
				"file": "util.go",
				"line": 10,
				"severity_hint": "low",
				"category": "maintainability"
			}
		]`)

		findings, err := adapter.ParseFindings(raw)
		Expect(err).NotTo(HaveOccurred())
		Expect(findings).To(HaveLen(1))
		Expect(findings[0].TestCode).To(BeEmpty())
		Expect(findings[0].TestFile).To(BeEmpty())
	})

	It("returns error for malformed JSON", func() {
		raw := []byte(`{not valid json}`)
		_, err := adapter.ParseFindings(raw)
		Expect(err).To(HaveOccurred())
	})

	It("returns empty slice for empty array", func() {
		raw := []byte(`[]`)
		findings, err := adapter.ParseFindings(raw)
		Expect(err).NotTo(HaveOccurred())
		Expect(findings).To(BeEmpty())
	})
})
