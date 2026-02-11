package orchestrator_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/plan/orchestrator"
	"github.com/concourse/ci-agent/schema"
)

var _ = Describe("Writer", func() {
	var tmpDir string

	BeforeEach(func() {
		var err error
		tmpDir, err = os.MkdirTemp("", "writer-test")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		os.RemoveAll(tmpDir)
	})

	Describe("WriteSpec", func() {
		It("creates spec.md with correct content", func() {
			content := "# My Spec\n\n## Requirements\n\n1. Auth works"
			artifact, err := orchestrator.WriteSpec(tmpDir, content)
			Expect(err).NotTo(HaveOccurred())

			data, err := os.ReadFile(filepath.Join(tmpDir, "spec.md"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).To(Equal(content))

			Expect(artifact.Name).To(Equal("spec"))
			Expect(artifact.Path).To(Equal("spec.md"))
			Expect(artifact.MediaType).To(Equal("text/markdown"))
		})

		It("returns error for invalid directory", func() {
			_, err := orchestrator.WriteSpec("/nonexistent/dir/that/does/not/exist", "content")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("write spec.md"))
		})
	})

	Describe("WritePlan", func() {
		It("creates plan.md with correct content", func() {
			content := "# Plan\n\n## Phase 1\n\n- [ ] Task 1"
			artifact, err := orchestrator.WritePlan(tmpDir, content)
			Expect(err).NotTo(HaveOccurred())

			data, err := os.ReadFile(filepath.Join(tmpDir, "plan.md"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).To(Equal(content))

			Expect(artifact.Name).To(Equal("plan"))
			Expect(artifact.Path).To(Equal("plan.md"))
			Expect(artifact.MediaType).To(Equal("text/markdown"))
		})

		It("returns error for invalid directory", func() {
			_, err := orchestrator.WritePlan("/nonexistent/dir/that/does/not/exist", "content")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("write plan.md"))
		})
	})

	Describe("WriteResults", func() {
		It("marshals and writes results.json correctly", func() {
			results := &schema.Results{
				SchemaVersion: "1.0",
				Status:        schema.StatusPass,
				Confidence:    0.95,
				Summary:       "All checks passed",
				Artifacts: []schema.Artifact{
					{Name: "spec", Path: "spec.md", MediaType: "text/markdown"},
				},
			}
			err := orchestrator.WriteResults(tmpDir, results)
			Expect(err).NotTo(HaveOccurred())

			data, err := os.ReadFile(filepath.Join(tmpDir, "results.json"))
			Expect(err).NotTo(HaveOccurred())

			var decoded schema.Results
			Expect(json.Unmarshal(data, &decoded)).To(Succeed())
			Expect(decoded.SchemaVersion).To(Equal("1.0"))
			Expect(decoded.Status).To(Equal(schema.StatusPass))
			Expect(decoded.Confidence).To(BeNumerically("~", 0.95, 0.01))
			Expect(decoded.Summary).To(Equal("All checks passed"))
			Expect(decoded.Artifacts).To(HaveLen(1))
		})

		It("returns error for invalid directory", func() {
			results := &schema.Results{SchemaVersion: "1.0", Status: schema.StatusPass}
			err := orchestrator.WriteResults("/nonexistent/dir/that/does/not/exist", results)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("write results.json"))
		})
	})
})
