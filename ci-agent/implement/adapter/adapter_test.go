package adapter_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/implement/adapter"
)

var _ = Describe("Adapter Types", func() {
	Describe("CodeGenRequest", func() {
		It("round-trips JSON", func() {
			req := adapter.CodeGenRequest{
				TaskDescription: "Implement widget parser",
				SpecContext:     "Widgets have names and types",
				RepoDir:         "/repo",
				TargetFiles:     []string{"widget.go", "widget_test.go"},
				PriorContext:    "Previous task created the model",
			}

			data, err := json.Marshal(req)
			Expect(err).NotTo(HaveOccurred())

			var decoded adapter.CodeGenRequest
			Expect(json.Unmarshal(data, &decoded)).To(Succeed())
			Expect(decoded.TaskDescription).To(Equal(req.TaskDescription))
			Expect(decoded.SpecContext).To(Equal(req.SpecContext))
			Expect(decoded.RepoDir).To(Equal(req.RepoDir))
			Expect(decoded.TargetFiles).To(Equal(req.TargetFiles))
			Expect(decoded.PriorContext).To(Equal(req.PriorContext))
		})
	})

	Describe("TestGenResponse", func() {
		It("round-trips JSON", func() {
			resp := adapter.TestGenResponse{
				TestFilePath: "widget/widget_test.go",
				TestContent:  "package widget_test\n\nfunc TestWidget(t *testing.T) {}",
				PackageName:  "widget_test",
			}

			data, err := json.Marshal(resp)
			Expect(err).NotTo(HaveOccurred())

			var decoded adapter.TestGenResponse
			Expect(json.Unmarshal(data, &decoded)).To(Succeed())
			Expect(decoded.TestFilePath).To(Equal(resp.TestFilePath))
			Expect(decoded.TestContent).To(Equal(resp.TestContent))
			Expect(decoded.PackageName).To(Equal(resp.PackageName))
		})
	})

	Describe("ImplGenResponse", func() {
		It("round-trips JSON", func() {
			resp := adapter.ImplGenResponse{
				Patches: []adapter.FilePatch{
					{Path: "widget/widget.go", Content: "package widget\n\ntype Widget struct{}"},
					{Path: "widget/parser.go", Content: "package widget\n\nfunc Parse() {}"},
				},
			}

			data, err := json.Marshal(resp)
			Expect(err).NotTo(HaveOccurred())

			var decoded adapter.ImplGenResponse
			Expect(json.Unmarshal(data, &decoded)).To(Succeed())
			Expect(decoded.Patches).To(HaveLen(2))
			Expect(decoded.Patches[0].Path).To(Equal("widget/widget.go"))
			Expect(decoded.Patches[1].Path).To(Equal("widget/parser.go"))
		})
	})

	Describe("FilePatch", func() {
		It("has required fields", func() {
			p := adapter.FilePatch{
				Path:    "foo.go",
				Content: "package foo",
			}
			Expect(p.Path).To(Equal("foo.go"))
			Expect(p.Content).To(Equal("package foo"))
		})
	})
})
