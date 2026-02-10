package claude_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/adapter/claude"
	"github.com/concourse/ci-agent/config"
)

var _ = Describe("ClaudeAdapter", func() {
	Describe("BuildCommand", func() {
		It("constructs correct CLI invocation", func() {
			a := claude.New("claude", "")
			cmd := a.BuildCommand("/tmp/repo", "review this code")
			Expect(cmd.Path).To(ContainSubstring("claude"))
			Expect(cmd.Args).To(ContainElement("--output-format"))
			Expect(cmd.Args).To(ContainElement("json"))
			Expect(cmd.Dir).To(Equal("/tmp/repo"))
		})

		It("uses custom CLI path", func() {
			a := claude.New("/usr/local/bin/claude-code", "")
			cmd := a.BuildCommand("/tmp/repo", "review prompt")
			Expect(cmd.Path).To(ContainSubstring("claude-code"))
		})

		It("passes model flag when specified", func() {
			a := claude.New("claude", "opus")
			cmd := a.BuildCommand("/tmp/repo", "review prompt")
			Expect(cmd.Args).To(ContainElement("--model"))
			Expect(cmd.Args).To(ContainElement("opus"))
		})
	})
})

var _ = Describe("BuildReviewPrompt", func() {
	It("includes output format specification", func() {
		cfg := config.DefaultConfig()
		prompt, err := claude.BuildReviewPrompt("/tmp/repo", cfg, false, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(prompt).To(ContainSubstring("severity_hint"))
		Expect(prompt).To(ContainSubstring("test_code"))
		Expect(prompt).To(ContainSubstring("JSON"))
	})

	It("includes category constraints from config", func() {
		cfg := config.DefaultConfig()
		cfg.Categories = map[string]config.CategoryConfig{
			"security":    {Enabled: true},
			"correctness": {Enabled: true},
		}
		prompt, err := claude.BuildReviewPrompt("/tmp/repo", cfg, false, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(prompt).To(ContainSubstring("security"))
		Expect(prompt).To(ContainSubstring("correctness"))
	})

	It("includes include/exclude patterns", func() {
		cfg := config.DefaultConfig()
		cfg.Include = []string{"**/*.go"}
		cfg.Exclude = []string{"vendor/**"}
		prompt, err := claude.BuildReviewPrompt("/tmp/repo", cfg, false, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(prompt).To(ContainSubstring("**/*.go"))
		Expect(prompt).To(ContainSubstring("vendor/**"))
	})

	It("enables diff-only mode", func() {
		cfg := config.DefaultConfig()
		prompt, err := claude.BuildReviewPrompt("/tmp/repo", cfg, true, "main")
		Expect(err).NotTo(HaveOccurred())
		Expect(prompt).To(ContainSubstring("diff"))
		Expect(prompt).To(ContainSubstring("main"))
	})
})
