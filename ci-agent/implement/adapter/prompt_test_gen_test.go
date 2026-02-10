package adapter_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/implement/adapter"
)

var _ = Describe("BuildTestPrompt", func() {
	It("includes task description", func() {
		req := adapter.CodeGenRequest{
			TaskDescription: "Create widget parser",
		}
		prompt := adapter.BuildTestPrompt(req)
		Expect(prompt).To(ContainSubstring("Create widget parser"))
	})

	It("includes spec context when provided", func() {
		req := adapter.CodeGenRequest{
			TaskDescription: "Create parser",
			SpecContext:     "Widgets must have names and types",
		}
		prompt := adapter.BuildTestPrompt(req)
		Expect(prompt).To(ContainSubstring("Widgets must have names and types"))
	})

	It("omits spec section when spec context is empty", func() {
		req := adapter.CodeGenRequest{
			TaskDescription: "Create parser",
		}
		prompt := adapter.BuildTestPrompt(req)
		Expect(prompt).NotTo(ContainSubstring("Specification Context"))
	})

	It("includes target file paths", func() {
		req := adapter.CodeGenRequest{
			TaskDescription: "Create parser",
			TargetFiles:     []string{"widget/parser.go", "widget/model.go"},
		}
		prompt := adapter.BuildTestPrompt(req)
		Expect(prompt).To(ContainSubstring("widget/parser.go"))
		Expect(prompt).To(ContainSubstring("widget/model.go"))
	})

	It("specifies JSON output format", func() {
		req := adapter.CodeGenRequest{
			TaskDescription: "Create parser",
		}
		prompt := adapter.BuildTestPrompt(req)
		Expect(prompt).To(ContainSubstring("test_file_path"))
		Expect(prompt).To(ContainSubstring("test_content"))
	})
})
