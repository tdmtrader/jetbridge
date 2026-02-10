package adapter_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/implement/adapter"
)

var _ = Describe("BuildImplPrompt", func() {
	It("includes task description and test code", func() {
		req := adapter.CodeGenRequest{
			TaskDescription: "Implement widget parser",
		}
		prompt := adapter.BuildImplPrompt(req, "func TestParse(t *testing.T) {}", "FAIL: expected parse")
		Expect(prompt).To(ContainSubstring("Implement widget parser"))
		Expect(prompt).To(ContainSubstring("func TestParse"))
		Expect(prompt).To(ContainSubstring("FAIL: expected parse"))
	})

	It("includes spec context when provided", func() {
		req := adapter.CodeGenRequest{
			TaskDescription: "Implement parser",
			SpecContext:     "Widgets must have names",
		}
		prompt := adapter.BuildImplPrompt(req, "test code", "test output")
		Expect(prompt).To(ContainSubstring("Widgets must have names"))
	})

	It("forbids modifying the test file", func() {
		req := adapter.CodeGenRequest{
			TaskDescription: "Implement parser",
		}
		prompt := adapter.BuildImplPrompt(req, "test", "output")
		Expect(prompt).To(ContainSubstring("DO NOT modify"))
	})

	It("specifies JSON output format with patches", func() {
		req := adapter.CodeGenRequest{
			TaskDescription: "Implement parser",
		}
		prompt := adapter.BuildImplPrompt(req, "test", "output")
		Expect(prompt).To(ContainSubstring("patches"))
		Expect(prompt).To(ContainSubstring("path"))
		Expect(prompt).To(ContainSubstring("content"))
	})

	It("includes existing file content in target files", func() {
		req := adapter.CodeGenRequest{
			TaskDescription: "Implement parser",
			TargetFiles:     []string{"widget/parser.go"},
		}
		prompt := adapter.BuildImplPrompt(req, "test", "output")
		Expect(prompt).To(ContainSubstring("widget/parser.go"))
	})
})
