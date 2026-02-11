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

	It("maps multiple requirements independently with partial coverage", func() {
		specPath := writeSpec(`# Spec

## Requirements

1. System authenticates users
2. System logs audit events
3. System enforces rate limits

## Acceptance Criteria

- [ ] Login page displays fields
- [ ] Audit log is written on login
- [ ] Rate limit returns 429 after threshold
`)
		// Write test files that match specific requirements via keywords.
		// The mapper uses significantWords() and checks for substring containment.
		// "TestLoginPageDisplaysFields" → "testloginpagedisplaysfields" contains "login", "displays", "fields".
		writeTestFile("login_test.go", `package main_test

import "testing"

func TestLoginPageDisplaysFields(t *testing.T) {}
`)
		outputDir := filepath.Join(tmpDir, "output")

		output, err := orchestrator.RunQA(context.Background(), orchestrator.QAOptions{
			RepoDir:   tmpDir,
			SpecFile:  specPath,
			OutputDir: outputDir,
			Config:    &config.QAConfig{Threshold: 3.0, GenerateTests: false},
		})
		Expect(err).NotTo(HaveOccurred())
		// Should have 6 requirement results (3 requirements + 3 ACs).
		Expect(output.Results).To(HaveLen(6))

		// At least one requirement should be covered or partial (login test matches).
		hasCoverage := false
		for _, r := range output.Results {
			if r.CoveragePoints > 0 {
				hasCoverage = true
				break
			}
		}
		Expect(hasCoverage).To(BeTrue(), "at least one requirement should have coverage")

		// Score should reflect partial coverage (between 0 exclusive and 10).
		Expect(output.Score.Value).To(BeNumerically(">", 0.0))
		Expect(output.Score.Value).To(BeNumerically("<", 10.0))

		// Gaps should include uncovered items (audit and rate limit requirements).
		Expect(output.Gaps).NotTo(BeEmpty())
	})

	It("uses agent for browser plan when agent is provided", func() {
		specPath := writeSpec(`## Acceptance Criteria

- [ ] Login page loads correctly
`)
		outputDir := filepath.Join(tmpDir, "output")

		agentPlan := "# Agent-Generated Browser QA Plan\n\nCustom plan from agent."
		agent := &fakeQAAgent{response: agentPlan}

		output, err := orchestrator.RunQA(context.Background(), orchestrator.QAOptions{
			RepoDir:   tmpDir,
			SpecFile:  specPath,
			OutputDir: outputDir,
			Config:    &config.QAConfig{Threshold: 5.0, BrowserPlan: true},
			TargetURL: "http://localhost",
			Agent:     agent,
		})

		Expect(err).NotTo(HaveOccurred())
		// The browser plan should come from the agent, not the static generator
		Expect(output.BrowserPlan).To(Equal(agentPlan))
		Expect(agent.runCalled).To(BeTrue(), "agent.Run should have been called for browser plan")

		// Verify the file was written with agent content
		planData, err := os.ReadFile(filepath.Join(outputDir, "browser-qa-plan.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(planData)).To(Equal(agentPlan))
	})

	It("generates gap tests that fail and leaves requirement uncovered", func() {
		specPath := writeSpec(`# Spec

## Acceptance Criteria

- [ ] Widget returns correct value
`)
		outputDir := filepath.Join(tmpDir, "output")

		// Write go.mod so gap tests can compile.
		os.WriteFile(filepath.Join(tmpDir, "go.mod"),
			[]byte("module main_test\n\ngo 1.25\n"), 0644)

		// Use a fake agent that generates a failing test.
		fakeAgent := &fakeQAAgent{
			response: `{
				"test_file": "widget_gap_test.go",
				"test_code": "package main_test\nimport \"testing\"\nfunc TestWidgetGap(t *testing.T) { t.Fatal(\"not implemented\") }",
				"test_name": "TestWidgetGap"
			}`,
		}

		output, err := orchestrator.RunQA(context.Background(), orchestrator.QAOptions{
			RepoDir:   tmpDir,
			SpecFile:  specPath,
			OutputDir: outputDir,
			Config:    &config.QAConfig{Threshold: 5.0, GenerateTests: true},
			Agent:     fakeAgent,
		})
		Expect(err).NotTo(HaveOccurred())

		// The requirement should remain uncovered/broken since gap test fails.
		Expect(output.Results).NotTo(BeEmpty())
		for _, r := range output.Results {
			Expect(r.Status).NotTo(Equal(schema.CoverageCovered))
		}
	})
})

// fakeQAAgent implements the QAAgentRunner interface for testing.
type fakeQAAgent struct {
	response  string
	runCalled bool
}

func (f *fakeQAAgent) Run(_ context.Context, _ string) (string, error) {
	f.runCalled = true
	return f.response, nil
}
