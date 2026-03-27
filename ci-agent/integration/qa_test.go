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

var _ = Describe("QA Phase Runner Integration", func() {
	var (
		outputDir string
		phasesDir string
	)

	BeforeEach(func() {
		var err error
		outputDir, err = os.MkdirTemp("", "integ-qa-*")
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

	It("runs QA phase and produces valid qa.json artifact", func() {
		ctx := context.Background()

		qaCfg, qaData := loadPhase("qa.yaml")
		qaOutputDir := filepath.Join(outputDir, "qa")

		qaClient := &fakeLLMClient{
			responses: []json.RawMessage{
				json.RawMessage(`{"coverage": 0.95, "requirements_met": true, "gaps": []}`),
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

		// Verify qa.json artifact was written.
		qaPath := filepath.Join(qaOutputDir, "qa.json")
		Expect(qaPath).To(BeAnExistingFile())

		// Verify results.json is valid.
		data, err := os.ReadFile(filepath.Join(qaOutputDir, "results.json"))
		Expect(err).NotTo(HaveOccurred())
		var decoded schema.Results
		Expect(json.Unmarshal(data, &decoded)).To(Succeed())
		Expect(decoded.SchemaVersion).To(Equal("1.0"))
	})

	It("QA phase produces provenance when config path is provided", func() {
		ctx := context.Background()

		qaCfg, qaData := loadPhase("qa.yaml")
		qaOutputDir := filepath.Join(outputDir, "qa-prov")

		qaClient := &fakeLLMClient{
			responses: []json.RawMessage{
				json.RawMessage(`{"coverage": 1.0}`),
			},
		}

		_, err := phaserunner.Run(ctx, phaserunner.Options{
			ConfigPath: filepath.Join(phasesDir, "qa.yaml"),
			Config:     qaCfg,
			ConfigData: qaData,
			OutputDir:  qaOutputDir,
			Client:     qaClient,
			BaseDir:    phasesDir,
			Model:      "claude-sonnet-4-6",
		})
		Expect(err).NotTo(HaveOccurred())

		provPath := filepath.Join(qaOutputDir, "provenance.json")
		Expect(provPath).To(BeAnExistingFile())

		provData, err := os.ReadFile(provPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(provData)).To(ContainSubstring("qa.yaml"))
		Expect(string(provData)).To(ContainSubstring("claude-sonnet-4-6"))
	})
})
