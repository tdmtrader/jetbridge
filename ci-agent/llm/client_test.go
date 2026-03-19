package llm_test

import (
	"encoding/json"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/llm"
)

func TestLLM(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "LLM Suite")
}

var _ = Describe("ExtractJSON", func() {
	It("returns raw data when no code block", func() {
		input := []byte(`{"key": "value"}`)
		result := llm.ExtractJSON(input)
		Expect(json.Valid(result)).To(BeTrue())

		var m map[string]string
		Expect(json.Unmarshal(result, &m)).To(Succeed())
		Expect(m["key"]).To(Equal("value"))
	})

	It("extracts JSON from markdown code block", func() {
		input := []byte("some text\n```json\n{\"key\": \"value\"}\n```\nmore text")
		result := llm.ExtractJSON(input)

		var m map[string]string
		Expect(json.Unmarshal(result, &m)).To(Succeed())
		Expect(m["key"]).To(Equal("value"))
	})

	It("handles multiline JSON in code block", func() {
		input := []byte("```json\n{\n  \"a\": 1,\n  \"b\": 2\n}\n```")
		result := llm.ExtractJSON(input)

		var m map[string]int
		Expect(json.Unmarshal(result, &m)).To(Succeed())
		Expect(m["a"]).To(Equal(1))
		Expect(m["b"]).To(Equal(2))
	})
})

var _ = Describe("NewClaudeClient", func() {
	It("defaults CLI to claude", func() {
		c := llm.NewClaudeClient("")
		Expect(c.CLI).To(Equal("claude"))
	})

	It("uses provided CLI path", func() {
		c := llm.NewClaudeClient("/usr/bin/claude")
		Expect(c.CLI).To(Equal("/usr/bin/claude"))
	})
})
