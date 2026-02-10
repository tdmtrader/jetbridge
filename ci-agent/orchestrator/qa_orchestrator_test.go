package orchestrator_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/config"
	"github.com/concourse/ci-agent/orchestrator"
	"github.com/concourse/ci-agent/schema"
)

var _ = Describe("RunQA", func() {
	var tmpDir string

	BeforeEach(func() {
		var err error
		tmpDir, err = os.MkdirTemp("", "qa-orch-test")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		os.RemoveAll(tmpDir)
	})

	writeSpec := func(content string) string {
		specPath := filepath.Join(tmpDir, "spec.md")
		os.WriteFile(specPath, []byte(content), 0644)
		return specPath
	}

	writeTestFile := func(name, content string) {
		os.WriteFile(filepath.Join(tmpDir, name), []byte(content), 0644)
	}

	It("runs full pipeline and writes qa.json", func() {
		specPath := writeSpec(`# Spec

## Requirements

1. System authenticates users

## Acceptance Criteria

- [ ] Login page displays fields
`)
		writeTestFile("auth_test.go", `package main_test
func TestAuth(t *testing.T) {}
`)
		outputDir := filepath.Join(tmpDir, "output")

		output, err := orchestrator.RunQA(context.Background(), orchestrator.QAOptions{
			RepoDir:   tmpDir,
			SpecFile:  specPath,
			OutputDir: outputDir,
			Config:    &config.QAConfig{Threshold: 5.0, GenerateTests: false, BrowserPlan: true},
			TargetURL: "http://localhost",
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(output.SchemaVersion).To(Equal("1.0.0"))
		Expect(output.Results).To(HaveLen(2))
		Expect(output.Score.Max).To(BeNumerically("~", 10.0, 0.01))

		// Verify qa.json written
		qaPath := filepath.Join(outputDir, "qa.json")
		Expect(qaPath).To(BeAnExistingFile())

		data, _ := os.ReadFile(qaPath)
		var decoded schema.QAOutput
		Expect(json.Unmarshal(data, &decoded)).To(Succeed())
		Expect(decoded.Validate()).To(Succeed())
	})

	It("writes browser plan when enabled", func() {
		specPath := writeSpec(`## Acceptance Criteria

- [ ] Login page loads correctly
`)
		outputDir := filepath.Join(tmpDir, "output")

		_, err := orchestrator.RunQA(context.Background(), orchestrator.QAOptions{
			RepoDir:   tmpDir,
			SpecFile:  specPath,
			OutputDir: outputDir,
			Config:    &config.QAConfig{Threshold: 5.0, BrowserPlan: true},
			TargetURL: "http://localhost",
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(outputDir, "browser-qa-plan.md")).To(BeAnExistingFile())
	})

	It("skips browser plan when disabled", func() {
		specPath := writeSpec(`## Acceptance Criteria

- [ ] Login page loads
`)
		outputDir := filepath.Join(tmpDir, "output")

		_, err := orchestrator.RunQA(context.Background(), orchestrator.QAOptions{
			RepoDir:   tmpDir,
			SpecFile:  specPath,
			OutputDir: outputDir,
			Config:    &config.QAConfig{Threshold: 5.0, BrowserPlan: false},
			TargetURL: "http://localhost",
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(outputDir, "browser-qa-plan.md")).NotTo(BeAnExistingFile())
	})

	It("returns error on missing spec file", func() {
		outputDir := filepath.Join(tmpDir, "output")
		_, err := orchestrator.RunQA(context.Background(), orchestrator.QAOptions{
			RepoDir:   tmpDir,
			SpecFile:  "/nonexistent/spec.md",
			OutputDir: outputDir,
			Config:    config.DefaultQAConfig(),
		})
		Expect(err).To(HaveOccurred())
	})

	It("handles empty spec gracefully", func() {
		specPath := writeSpec("# Empty spec")
		outputDir := filepath.Join(tmpDir, "output")

		output, err := orchestrator.RunQA(context.Background(), orchestrator.QAOptions{
			RepoDir:   tmpDir,
			SpecFile:  specPath,
			OutputDir: outputDir,
			Config:    &config.QAConfig{Threshold: 5.0},
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(output.Results).To(BeEmpty())
	})
})
