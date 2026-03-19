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

var _ = Describe("ParseCLIEnvelope", func() {
	It("parses a full CLI envelope with usage metadata", func() {
		envelope := []byte(`{
			"type": "result",
			"subtype": "success",
			"result": "{\"findings\": []}",
			"model": "claude-sonnet-4-6",
			"cost_usd": 0.0234,
			"duration_ms": 5432,
			"duration_api_ms": 4100,
			"num_turns": 1,
			"is_error": false,
			"usage": {
				"input_tokens": 1500,
				"output_tokens": 300,
				"cache_read_input_tokens": 200,
				"cache_creation_input_tokens": 50
			},
			"session_id": "abc123"
		}`)

		cr := llm.ParseCLIEnvelope(envelope)
		Expect(cr.Model).To(Equal("claude-sonnet-4-6"))
		Expect(cr.CostUSD).To(BeNumerically("~", 0.0234, 0.0001))
		Expect(cr.DurationMS).To(Equal(5432))
		Expect(cr.DurationAPIMS).To(Equal(4100))
		Expect(cr.NumTurns).To(Equal(1))
		Expect(cr.Usage.InputTokens).To(Equal(1500))
		Expect(cr.Usage.OutputTokens).To(Equal(300))
		Expect(cr.Usage.CacheReadInputTokens).To(Equal(200))
		Expect(cr.Usage.CacheCreationInputTokens).To(Equal(50))

		// Result should be the extracted JSON from the string
		var m map[string]interface{}
		Expect(json.Unmarshal(cr.Result, &m)).To(Succeed())
		Expect(m).To(HaveKey("findings"))
	})

	It("handles result field containing markdown code block", func() {
		envelope := []byte(`{
			"type": "result",
			"subtype": "success",
			"result": "` + "```json\\n{\\\"key\\\": \\\"value\\\"}\\n```" + `",
			"model": "claude-opus-4-6",
			"cost_usd": 0.01,
			"usage": {"input_tokens": 100, "output_tokens": 50}
		}`)

		cr := llm.ParseCLIEnvelope(envelope)
		Expect(cr.Model).To(Equal("claude-opus-4-6"))

		var m map[string]string
		Expect(json.Unmarshal(cr.Result, &m)).To(Succeed())
		Expect(m["key"]).To(Equal("value"))
	})

	It("falls back to raw extraction when not a CLI envelope", func() {
		// Plain JSON (no "type" field) — legacy behavior
		raw := []byte(`{"findings": [{"title": "bug"}]}`)
		cr := llm.ParseCLIEnvelope(raw)

		Expect(cr.Model).To(BeEmpty())
		Expect(cr.Usage.InputTokens).To(Equal(0))

		var m map[string]interface{}
		Expect(json.Unmarshal(cr.Result, &m)).To(Succeed())
		Expect(m).To(HaveKey("findings"))
	})

	It("falls back gracefully on invalid JSON", func() {
		cr := llm.ParseCLIEnvelope([]byte("not json at all"))
		Expect(cr.Model).To(BeEmpty())
		// Result should still contain the raw data
		Expect(string(cr.Result)).To(Equal("not json at all"))
	})
})
