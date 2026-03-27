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

var _ = Describe("Review → Fix → QA Three-Phase Integration", func() {
	var (
		outputDir string
		phasesDir string
	)

	BeforeEach(func() {
		var err error
		outputDir, err = os.MkdirTemp("", "integ-rfq-*")
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

	It("chains review → fix → qa phases producing valid artifacts at each stage", func() {
		ctx := context.Background()

		// Stage 1: Review
		reviewCfg, reviewData := loadPhase("review.yaml")
		reviewOutputDir := filepath.Join(outputDir, "review")

		reviewClient := &fakeLLMClient{
			responses: []json.RawMessage{
				json.RawMessage(`{"issues": [{"id": "ISS-001", "title": "bug found", "severity": "high"}]}`),
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
		Expect(reviewResults.Metadata["phase"]).To(Equal("review"))

		// Stage 2: Fix
		fixCfg, fixData := loadPhase("fix.yaml")
		fixOutputDir := filepath.Join(outputDir, "fix")

		os.Setenv("REVIEW_DIR", reviewOutputDir)
		defer os.Unsetenv("REVIEW_DIR")

		fixClient := &fakeLLMClient{
			responses: []json.RawMessage{
				json.RawMessage(`{"fixed": 1}`),
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

		// Stage 3: QA
		qaCfg, qaData := loadPhase("qa.yaml")
		qaOutputDir := filepath.Join(outputDir, "qa")

		qaClient := &fakeLLMClient{
			responses: []json.RawMessage{
				json.RawMessage(`{"coverage": 0.9, "requirements_met": true}`),
			},
		}

		qaResults, err := phaserunner.Run(ctx, phaserunner.Options{
			ConfigPath: filepath.Join(phasesDir, "qa.yaml"),
			Config:     qaCfg,
			ConfigData: qaData,
			OutputDir:  qaOutputDir,
			Client:     qaClient,
			BaseDir:    phasesDir,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(qaResults.Metadata["phase"]).To(Equal("qa"))

		// Verify all three stages produced valid results.json.
		for _, dir := range []string{reviewOutputDir, fixOutputDir, qaOutputDir} {
			data, err := os.ReadFile(filepath.Join(dir, "results.json"))
			Expect(err).NotTo(HaveOccurred())
			var decoded schema.Results
			Expect(json.Unmarshal(data, &decoded)).To(Succeed())
			Expect(decoded.SchemaVersion).To(Equal("1.0"))
		}

		// Verify all stages produced events.
		for _, dir := range []string{reviewOutputDir, fixOutputDir, qaOutputDir} {
			Expect(filepath.Join(dir, "events.ndjson")).To(BeAnExistingFile())
		}
	})
})
