package orchestrator_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/orchestrator"
)

var _ = Describe("ClaudeAgentRunner", func() {
	It("satisfies the QAAgentRunner interface", func() {
		var _ orchestrator.QAAgentRunner = orchestrator.NewClaudeAgentRunner("claude", "")
	})

	It("defaults CLI to claude when empty", func() {
		r := orchestrator.NewClaudeAgentRunner("", "")
		Expect(r.CLI).To(Equal("claude"))
	})

	It("stores model when provided", func() {
		r := orchestrator.NewClaudeAgentRunner("claude", "claude-haiku-4-5-20251001")
		Expect(r.Model).To(Equal("claude-haiku-4-5-20251001"))
	})
})
