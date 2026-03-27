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

var _ = Describe("Review → Fix Phase Runner Integration", func() {
	var (
		outputDir string
		phasesDir string
	)

	BeforeEach(func() {
		var err error
		outputDir, err = os.MkdirTemp("", "integ-review-fix-*")
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

	It("runs review phase then fix phase using real configs", func() {
		ctx := context.Background()

		// Step 1: Run review phase.
		reviewCfg, reviewData := loadPhase("review.yaml")
		reviewOutputDir := filepath.Join(outputDir, "review")

		reviewClient := &fakeLLMClient{
			responses: []json.RawMessage{
				json.RawMessage(`{"issues": [{"id": "ISS-001", "title": "divide by zero", "severity": "high"}]}`),
			},
		}

		reviewResults, err := phaserunner.Run(ctx, phaserunner.Options{
			ConfigPath: filepath.Join(phasesDir, "review.yaml"),
			Config:     reviewCfg,
			ConfigData: reviewData,
			OutputDir:  reviewOutputDir,
			Client:     reviewClient,
			BaseDir:    phasesDir,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(reviewResults.Status).To(BeElementOf(schema.StatusPass, schema.StatusFail))
		Expect(reviewResults.Metadata["phase"]).To(Equal("review"))

		// Verify review artifacts were written.
		Expect(filepath.Join(reviewOutputDir, "results.json")).To(BeAnExistingFile())
		Expect(filepath.Join(reviewOutputDir, "events.ndjson")).To(BeAnExistingFile())

		// Step 2: Run fix phase consuming review output.
		fixCfg, fixData := loadPhase("fix.yaml")
		fixOutputDir := filepath.Join(outputDir, "fix")

		os.Setenv("REVIEW_DIR", reviewOutputDir)
		defer os.Unsetenv("REVIEW_DIR")

		fixClient := &fakeLLMClient{
			responses: []json.RawMessage{
				json.RawMessage(`{"fixed": 1, "skipped": 0}`),
			},
		}

		fixResults, err := phaserunner.Run(ctx, phaserunner.Options{
			ConfigPath: filepath.Join(phasesDir, "fix.yaml"),
			Config:     fixCfg,
			ConfigData: fixData,
			OutputDir:  fixOutputDir,
			Client:     fixClient,
			BaseDir:    phasesDir,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(fixResults.Metadata["phase"]).To(Equal("fix"))

		// Verify fix artifacts were written.
		Expect(filepath.Join(fixOutputDir, "results.json")).To(BeAnExistingFile())
		Expect(filepath.Join(fixOutputDir, "events.ndjson")).To(BeAnExistingFile())

		// Verify results.json is valid schema.
		data, err := os.ReadFile(filepath.Join(fixOutputDir, "results.json"))
		Expect(err).NotTo(HaveOccurred())
		var decoded schema.Results
		Expect(json.Unmarshal(data, &decoded)).To(Succeed())
		Expect(decoded.SchemaVersion).To(Equal("1.0"))
	})

	It("review with no issues still produces valid output for fix", func() {
		ctx := context.Background()

		reviewCfg, reviewData := loadPhase("review.yaml")
		reviewOutputDir := filepath.Join(outputDir, "review-empty")

		reviewClient := &fakeLLMClient{
			responses: []json.RawMessage{
				json.RawMessage(`{"issues": []}`),
			},
		}

		reviewResults, err := phaserunner.Run(ctx, phaserunner.Options{
			ConfigPath: filepath.Join(phasesDir, "review.yaml"),
			Config:     reviewCfg,
			ConfigData: reviewData,
			OutputDir:  reviewOutputDir,
			Client:     reviewClient,
			BaseDir:    phasesDir,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(reviewResults.SchemaVersion).To(Equal("1.0"))

		// Results.json should be valid.
		data, err := os.ReadFile(filepath.Join(reviewOutputDir, "results.json"))
		Expect(err).NotTo(HaveOccurred())
		var decoded schema.Results
		Expect(json.Unmarshal(data, &decoded)).To(Succeed())
	})
})
