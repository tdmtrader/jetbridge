package phaserunner_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/phaseconfig"
	"github.com/concourse/ci-agent/phaserunner"
	"github.com/concourse/ci-agent/schema"
)

var _ = Describe("Phase Config Integration", func() {
	var (
		tmpDir    string
		outputDir string
	)

	BeforeEach(func() {
		var err error
		tmpDir, err = os.MkdirTemp("", "phase-integration")
		Expect(err).NotTo(HaveOccurred())
		outputDir = filepath.Join(tmpDir, "output")
	})

	AfterEach(func() {
		os.RemoveAll(tmpDir)
	})

	phasesDir := findPhasesDir()

	Describe("plan.yaml", func() {
		It("loads and runs the plan phase config", func() {
			if phasesDir == "" {
				Skip("phases directory not found")
			}

			cfg, err := phaseconfig.LoadFile(filepath.Join(phasesDir, "plan.yaml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Name).To(Equal("plan"))
			Expect(cfg.Steps).To(HaveLen(2))
			Expect(cfg.Steps[0].Name).To(Equal("generate-spec"))
			Expect(cfg.Steps[1].Name).To(Equal("generate-plan"))
			Expect(cfg.Steps[1].InputFrom).To(ContainElement("generate-spec"))
			Expect(cfg.Scoring).NotTo(BeNil())
			Expect(cfg.Scoring.Threshold).To(Equal(0.6))

			// Run with fake client
			fakeClient := &fakeLLMClient{
				responses: []json.RawMessage{
					json.RawMessage(`{"spec_markdown": "# Test Spec\n\nThis is a test spec.", "unresolved_questions": [], "assumptions": [], "out_of_scope": []}`),
					json.RawMessage(`{"plan_markdown": "# Test Plan\n\n## Phase 1\n- Task 1", "phases": [{"name": "phase1", "tasks": [{"description": "task1"}]}], "key_files": [], "risks": []}`),
				},
			}

			// Set env vars the config expects
			os.Setenv("INPUT_DIR", filepath.Join(tmpDir, "story"))
			os.Setenv("OUTPUT_DIR", outputDir)
			defer os.Unsetenv("INPUT_DIR")
			defer os.Unsetenv("OUTPUT_DIR")

			results, err := phaserunner.Run(context.Background(), phaserunner.Options{
				ConfigPath: filepath.Join(phasesDir, "plan.yaml"),
				Config:     cfg,
				OutputDir:  outputDir,
				Client:     fakeClient,
				BaseDir:    phasesDir,
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(results.Status).To(Equal(schema.StatusPass))

			// Verify spec.md was written
			specData, err := os.ReadFile(filepath.Join(outputDir, "spec.md"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(specData)).To(ContainSubstring("Test Spec"))

			// Verify plan.md was written
			planData, err := os.ReadFile(filepath.Join(outputDir, "plan.md"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(planData)).To(ContainSubstring("Test Plan"))

			// Verify provenance was written
			_, err = os.Stat(filepath.Join(outputDir, "provenance.json"))
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Describe("implement.yaml", func() {
		It("loads the implement phase config", func() {
			if phasesDir == "" {
				Skip("phases directory not found")
			}

			cfg, err := phaseconfig.LoadFile(filepath.Join(phasesDir, "implement.yaml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Name).To(Equal("implement"))
			Expect(cfg.MCP).To(ContainElements("git", "test", "filesystem"))
			Expect(cfg.Steps[0].VerifyCmd).NotTo(BeEmpty())
		})
	})

	Describe("review.yaml", func() {
		It("loads the review phase config", func() {
			if phasesDir == "" {
				Skip("phases directory not found")
			}

			cfg, err := phaseconfig.LoadFile(filepath.Join(phasesDir, "review.yaml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Name).To(Equal("review"))
			Expect(cfg.MCP).To(ContainElements("test", "filesystem"))
			Expect(cfg.Scoring.Threshold).To(Equal(7.0))
		})
	})

	Describe("fix.yaml", func() {
		It("loads the fix phase config", func() {
			if phasesDir == "" {
				Skip("phases directory not found")
			}

			cfg, err := phaseconfig.LoadFile(filepath.Join(phasesDir, "fix.yaml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Name).To(Equal("fix"))
			Expect(cfg.MCP).To(ContainElements("git", "test", "filesystem"))
		})
	})

	Describe("qa.yaml", func() {
		It("loads the qa phase config", func() {
			if phasesDir == "" {
				Skip("phases directory not found")
			}

			cfg, err := phaseconfig.LoadFile(filepath.Join(phasesDir, "qa.yaml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Name).To(Equal("qa"))
			Expect(cfg.MCP).To(ContainElements("test", "filesystem"))
			Expect(cfg.Scoring.Threshold).To(Equal(0.8))
		})
	})
})

// findPhasesDir searches for the phases directory relative to the test.
func findPhasesDir() string {
	candidates := []string{
		"../phases",
		"../../ci-agent/phases",
	}
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if _, err := os.Stat(abs); err == nil {
			return abs
		}
	}
	return ""
}
