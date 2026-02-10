package claude_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/fix"
	fixclaude "github.com/concourse/ci-agent/fix/adapter/claude"
)

var _ = Describe("FixClaudeAdapter", func() {
	It("satisfies the fix.FixAdapter interface", func() {
		var _ fix.FixAdapter = fixclaude.New("claude", "")
	})

	It("defaults CLI to claude when empty", func() {
		a := fixclaude.New("", "")
		Expect(a.CLI).To(Equal("claude"))
	})

	It("stores model when provided", func() {
		a := fixclaude.New("claude", "claude-haiku-4-5-20251001")
		Expect(a.Model).To(Equal("claude-haiku-4-5-20251001"))
	})
})
