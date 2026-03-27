package integration_test

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

var _ = Describe("Plan → Implement Phase Runner Integration", func() {
	var (
		outputDir string
		phasesDir string
	)

	BeforeEach(func() {
		var err error
		outputDir, err = os.MkdirTemp("", "integ-plan-impl-*")
		Expect(err).NotTo(HaveOccurred())

		phasesDir, err = filepath.Abs("../phases")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		os.RemoveAll(outputDir)
	})

	loadPhase := func(name string) (*phaseconfig.Config, []byte) {
		path := filepath.Join(phasesDir, name)
		data, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		cfg, err := phaseconfig.Parse(data)
		Expect(err).NotTo(HaveOccurred())
		return cfg, data
	}

	It("plan outputs feed into implement phase via phaserunner", func() {
		ctx := context.Background()

		// Step 1: Run plan phase (multi-step: generate-spec → generate-plan).
		planCfg, planData := loadPhase("plan.yaml")
		planOutputDir := filepath.Join(outputDir, "plan")

		planClient := &fakeLLMClient{
			responses: []json.RawMessage{
				json.RawMessage(`{"spec_markdown": "# Spec\n\nWidget feature."}`),
				json.RawMessage(`{"plan_markdown": "## Phase 1\n\n- [ ] Create widget"}`),
			},
		}

		planResults, err := phaserunner.Run(ctx, phaserunner.Options{
			ConfigPath: filepath.Join(phasesDir, "plan.yaml"),
			Config:     planCfg,
			ConfigData: planData,
			OutputDir:  planOutputDir,
			Client:     planClient,
			BaseDir:    phasesDir,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(planResults.Metadata["phase"]).To(Equal("plan"))

		// Verify plan produced spec.md and plan.md artifacts.
		Expect(filepath.Join(planOutputDir, "spec.md")).To(BeAnExistingFile())
		Expect(filepath.Join(planOutputDir, "plan.md")).To(BeAnExistingFile())
		Expect(filepath.Join(planOutputDir, "results.json")).To(BeAnExistingFile())

		// Verify the spec was extracted from JSON correctly.
		specContent, err := os.ReadFile(filepath.Join(planOutputDir, "spec.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(specContent)).To(ContainSubstring("Widget feature"))

		// Step 2: Run implement phase consuming plan output.
		implCfg, implData := loadPhase("implement.yaml")
		implOutputDir := filepath.Join(outputDir, "impl")

		// Set required env vars for implement phase.
		os.Setenv("SPEC_DIR", planOutputDir)
		os.Setenv("REPO_DIR", outputDir)
		os.Setenv("OUTPUT_DIR", implOutputDir)
		defer os.Unsetenv("SPEC_DIR")
		defer os.Unsetenv("REPO_DIR")
		defer os.Unsetenv("OUTPUT_DIR")

		implClient := &fakeLLMClient{
			responses: []json.RawMessage{
				json.RawMessage(`{"summary_markdown": "# Summary\n\nWidget implemented successfully."}`),
			},
		}

		implResults, err := phaserunner.Run(ctx, phaserunner.Options{
			ConfigPath: filepath.Join(phasesDir, "implement.yaml"),
			Config:     implCfg,
			ConfigData: implData,
			OutputDir:  implOutputDir,
			Client:     implClient,
			BaseDir:    phasesDir,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(implResults.Metadata["phase"]).To(Equal("implement"))

		// Verify implement produced results.json.
		data, err := os.ReadFile(filepath.Join(implOutputDir, "results.json"))
		Expect(err).NotTo(HaveOccurred())
		var decoded schema.Results
		Expect(json.Unmarshal(data, &decoded)).To(Succeed())
		Expect(decoded.SchemaVersion).To(Equal("1.0"))
	})

	It("plan phase chains step outputs (spec → plan)", func() {
		ctx := context.Background()

		planCfg, planData := loadPhase("plan.yaml")
		planOutputDir := filepath.Join(outputDir, "plan-chain")

		planClient := &fakeLLMClient{
			responses: []json.RawMessage{
				json.RawMessage(`{"spec_markdown": "# Generated Spec\n\nRequirements here."}`),
				json.RawMessage(`{"plan_markdown": "# Generated Plan\n\n## Phase 1\n\n- [ ] Task A"}`),
			},
		}

		planResults, err := phaserunner.Run(ctx, phaserunner.Options{
			ConfigPath: filepath.Join(phasesDir, "plan.yaml"),
			Config:     planCfg,
			ConfigData: planData,
			OutputDir:  planOutputDir,
			Client:     planClient,
			BaseDir:    phasesDir,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(planResults.Status).To(Equal(schema.StatusPass))

		// Both steps should have been called.
		Expect(planClient.prompts).To(HaveLen(2))
	})
})
