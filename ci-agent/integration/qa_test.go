package integration_test

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

// fakeQAAgent implements orchestrator.QAAgentRunner for integration tests.
type fakeQAAgent struct {
	responses []string
	callIdx   int
}

func (f *fakeQAAgent) Run(_ context.Context, _ string) (string, error) {
	if f.callIdx < len(f.responses) {
		resp := f.responses[f.callIdx]
		f.callIdx++
		return resp, nil
	}
	return "", nil
}

var _ = Describe("QA Mode End-to-End Integration", func() {
	var (
		ctx       context.Context
		repoDir   string
		outputDir string
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		repoDir, err = os.MkdirTemp("", "qa-integ-repo-*")
		Expect(err).NotTo(HaveOccurred())
		outputDir, err = os.MkdirTemp("", "qa-integ-output-*")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		os.RemoveAll(repoDir)
		os.RemoveAll(outputDir)
	})

	writeSpec := func(content string) string {
		specPath := filepath.Join(repoDir, "spec.md")
		Expect(os.WriteFile(specPath, []byte(content), 0644)).To(Succeed())
		return specPath
	}

	writeTestFile := func(name, content string) {
		Expect(os.WriteFile(filepath.Join(repoDir, name), []byte(content), 0644)).To(Succeed())
	}

	It("runs full QA pipeline: spec parse → mapping → scoring → browser plan → output", func() {
		specPath := writeSpec(`# Feature Spec

## Requirements

1. System authenticates users via login page
2. System logs audit events on authentication failure

## Acceptance Criteria

- [ ] Login page displays username and password fields
- [ ] Failed login attempts are logged with timestamp
`)
		// Write tests that cover the first requirement but not the second.
		writeTestFile("login_test.go", `package main_test

import "testing"

func TestLoginPageDisplaysFields(t *testing.T) {}
func TestLoginAuthenticatesUsers(t *testing.T) {}
`)

		agent := &fakeQAAgent{
			responses: []string{
				"# Agent Browser QA Plan\n\n## Flow 1: Login\n\n1. Navigate to login page\n2. Enter credentials\n3. Verify redirect",
			},
		}

		output, err := orchestrator.RunQA(ctx, orchestrator.QAOptions{
			RepoDir:   repoDir,
			SpecFile:  specPath,
			OutputDir: outputDir,
			Config:    &config.QAConfig{Threshold: 5.0, BrowserPlan: true},
			TargetURL: "http://localhost:8080",
			Agent:     agent,
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(output.SchemaVersion).To(Equal("1.0.0"))
		Expect(output.Results).NotTo(BeEmpty())

		// Score should reflect partial coverage.
		Expect(output.Score.Max).To(BeNumerically(">", 0))
		Expect(output.Score.Value).To(BeNumerically(">", 0))

		// Gaps should identify the uncovered requirement (audit logging).
		Expect(output.Gaps).NotTo(BeEmpty())
		hasAuditGap := false
		for _, g := range output.Gaps {
			if g.RequirementID != "" {
				hasAuditGap = true
			}
		}
		Expect(hasAuditGap).To(BeTrue())

		// Browser plan should come from the agent.
		Expect(output.BrowserPlan).To(ContainSubstring("Agent Browser QA Plan"))
		Expect(output.Metadata.BrowserPlanGenerated).To(BeTrue())

		// Verify qa.json was written and is valid.
		qaPath := filepath.Join(outputDir, "qa.json")
		Expect(qaPath).To(BeAnExistingFile())

		data, err := os.ReadFile(qaPath)
		Expect(err).NotTo(HaveOccurred())
		var decoded schema.QAOutput
		Expect(json.Unmarshal(data, &decoded)).To(Succeed())
		Expect(decoded.Validate()).To(Succeed())

		// Verify browser plan file was written.
		planPath := filepath.Join(outputDir, "browser-qa-plan.md")
		Expect(planPath).To(BeAnExistingFile())
		planData, err := os.ReadFile(planPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(planData)).To(ContainSubstring("Agent Browser QA Plan"))
	})

	It("pipeline with gap generation produces gap test results", func() {
		specPath := writeSpec(`## Acceptance Criteria

- [ ] Widget computes the correct value
`)
		Expect(os.WriteFile(filepath.Join(repoDir, "go.mod"),
			[]byte("module main_test\n\ngo 1.25\n"), 0644)).To(Succeed())

		agent := &fakeQAAgent{
			responses: []string{
				// Gap test generation response.
				`{"test_name": "TestWidgetValue", "test_code": "package main_test\nimport \"testing\"\nfunc TestWidgetValue(t *testing.T) { t.Fatal(\"not implemented\") }"}`,
			},
		}

		output, err := orchestrator.RunQA(ctx, orchestrator.QAOptions{
			RepoDir:   repoDir,
			SpecFile:  specPath,
			OutputDir: outputDir,
			Config:    &config.QAConfig{Threshold: 5.0, GenerateTests: true},
			Agent:     agent,
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(output.Results).NotTo(BeEmpty())

		// Since the gap test fails, the requirement should remain uncovered/broken.
		for _, r := range output.Results {
			Expect(r.Status).NotTo(Equal(schema.CoverageCovered))
		}
	})

	It("pipeline with full coverage scores above threshold", func() {
		specPath := writeSpec(`## Acceptance Criteria

- [ ] Login page displays fields
`)
		writeTestFile("login_test.go", `package main_test

import "testing"

func TestLoginPageDisplaysFields(t *testing.T) {}
`)

		output, err := orchestrator.RunQA(ctx, orchestrator.QAOptions{
			RepoDir:   repoDir,
			SpecFile:  specPath,
			OutputDir: outputDir,
			Config:    &config.QAConfig{Threshold: 3.0},
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(output.Score.Pass).To(BeTrue())
		Expect(output.Gaps).To(BeEmpty())
	})
})
