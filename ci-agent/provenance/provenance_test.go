package provenance_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/provenance"
)

func TestProvenance(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Provenance Suite")
}

var _ = Describe("Provenance", func() {
	var tmpDir string

	BeforeEach(func() {
		var err error
		tmpDir, err = os.MkdirTemp("", "provenance-test")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		os.RemoveAll(tmpDir)
	})

	Describe("HashFile", func() {
		It("hashes a file consistently", func() {
			path := filepath.Join(tmpDir, "test.yaml")
			Expect(os.WriteFile(path, []byte("hello"), 0644)).To(Succeed())

			ref1, err := provenance.HashFile(path)
			Expect(err).NotTo(HaveOccurred())
			ref2, err := provenance.HashFile(path)
			Expect(err).NotTo(HaveOccurred())

			Expect(ref1.Hash).To(Equal(ref2.Hash))
			Expect(ref1.Path).To(Equal(path))
			Expect(ref1.Hash).To(HaveLen(64))
		})

		It("errors on missing file", func() {
			_, err := provenance.HashFile("/nonexistent")
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("Build", func() {
		It("builds a complete provenance record", func() {
			configPath := filepath.Join(tmpDir, "phase.yaml")
			promptPath := filepath.Join(tmpDir, "prompt.md")
			Expect(os.WriteFile(configPath, []byte("name: test"), 0644)).To(Succeed())
			Expect(os.WriteFile(promptPath, []byte("# Prompt"), 0644)).To(Succeed())

			record, err := provenance.Build(
				configPath,
				[]string{promptPath},
				"claude-sonnet-4-6",
				[]string{"git", "test"},
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(record.PhaseConfig.Path).To(Equal(configPath))
			Expect(record.PhaseConfig.Hash).To(HaveLen(64))
			Expect(record.PromptFiles).To(HaveLen(1))
			Expect(record.PromptFiles[0].Path).To(Equal(promptPath))
			Expect(record.Model).To(Equal("claude-sonnet-4-6"))
			Expect(record.MCPTools).To(Equal([]string{"git", "test"}))
		})

		It("errors on missing config file", func() {
			_, err := provenance.Build("/nonexistent", nil, "", nil)
			Expect(err).To(HaveOccurred())
		})

		It("errors on missing prompt file", func() {
			configPath := filepath.Join(tmpDir, "phase.yaml")
			Expect(os.WriteFile(configPath, []byte("name: test"), 0644)).To(Succeed())

			_, err := provenance.Build(configPath, []string{"/nonexistent"}, "", nil)
			Expect(err).To(HaveOccurred())
		})
	})
})
