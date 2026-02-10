package claude_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/implement/adapter"
	"github.com/concourse/ci-agent/implement/adapter/claude"
)

var _ = Describe("Claude Adapter", func() {
	It("implements the Adapter interface", func() {
		var _ adapter.Adapter = claude.New("claude")
	})

	It("builds correct test generation prompt", func() {
		a := claude.New("claude")
		req := adapter.CodeGenRequest{
			TaskDescription: "Create widget",
			SpecContext:     "Widgets have names",
		}
		prompt := a.BuildTestGenPrompt(req)
		Expect(prompt).To(ContainSubstring("Create widget"))
		Expect(prompt).To(ContainSubstring("Widgets have names"))
		Expect(prompt).To(ContainSubstring("test_file_path"))
	})

	It("builds correct impl generation prompt", func() {
		a := claude.New("claude")
		req := adapter.CodeGenRequest{
			TaskDescription: "Create widget",
		}
		prompt := a.BuildImplGenPrompt(req, "test code", "FAIL output")
		Expect(prompt).To(ContainSubstring("Create widget"))
		Expect(prompt).To(ContainSubstring("test code"))
		Expect(prompt).To(ContainSubstring("FAIL output"))
		Expect(prompt).To(ContainSubstring("patches"))
	})

	It("parses test gen response from JSON", func() {
		raw := `{"test_file_path": "a_test.go", "test_content": "package a", "package_name": "a_test"}`
		resp, err := claude.ParseTestGenResponse([]byte(raw))
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.TestFilePath).To(Equal("a_test.go"))
		Expect(resp.TestContent).To(Equal("package a"))
	})

	It("parses impl gen response from JSON", func() {
		raw, _ := json.Marshal(adapter.ImplGenResponse{
			Patches: []adapter.FilePatch{{Path: "a.go", Content: "package a"}},
		})
		resp, err := claude.ParseImplGenResponse(raw)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.Patches).To(HaveLen(1))
	})

	It("returns error on malformed JSON", func() {
		_, err := claude.ParseTestGenResponse([]byte("not json"))
		Expect(err).To(HaveOccurred())
	})
})
