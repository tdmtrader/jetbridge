package integration_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/config"
	"github.com/concourse/ci-agent/fix"
	"github.com/concourse/ci-agent/orchestrator"
	"github.com/concourse/ci-agent/runner"
	"github.com/concourse/ci-agent/schema"
)

var _ = Describe("Review → Fix → QA Three-Stage Integration", func() {
	var (
		ctx       context.Context
		repoDir   string
		outputDir string
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		repoDir, err = os.MkdirTemp("", "rfq-integ-repo-*")
		Expect(err).NotTo(HaveOccurred())
		outputDir, err = os.MkdirTemp("", "rfq-integ-output-*")
		Expect(err).NotTo(HaveOccurred())

		// Create a minimal Go module with a bug.
		Expect(os.WriteFile(filepath.Join(repoDir, "go.mod"),
			[]byte("module example.com/testrepo\n\ngo 1.25\n"), 0644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(repoDir, "math.go"), []byte(`package testrepo

func Divide(a, b int) int {
	return a / b
}
`), 0644)).To(Succeed())

		// Write a spec for QA validation.
		Expect(os.WriteFile(filepath.Join(repoDir, "spec.md"), []byte(`# Math Spec

## Requirements

1. Divide function handles zero divisor safely

## Acceptance Criteria

- [ ] Divide returns zero when divisor is zero
`), 0644)).To(Succeed())

		gitInit(repoDir)
		gitAddCommit(repoDir, "initial commit with buggy divide")
	})

	AfterEach(func() {
		os.RemoveAll(repoDir)
		os.RemoveAll(outputDir)
	})

	It("review finds issue → fix resolves it → QA validates the fix", func() {
		// Stage 1: Review identifies divide-by-zero.
		reviewDir := filepath.Join(outputDir, "review")

		reviewAdapter := &fakeReviewAdapter{
			findings: []runner.AgentFinding{
				{
					Title:        "divide by zero panic",
					File:         "math.go",
					Line:         3,
					SeverityHint: schema.SeverityHigh,
					Category:     schema.CategoryCorrectness,
					TestCode: `package testrepo

import "testing"

func TestDivideByZero(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatal("Divide panics on zero divisor")
		}
	}()
	Divide(10, 0)
}
`,
					TestFile: "math_review_test.go",
					TestName: "TestDivideByZero",
				},
			},
		}

		reviewResult, err := orchestrator.Run(ctx, orchestrator.Options{
			RepoDir:   repoDir,
			OutputDir: reviewDir,
			Adapter:   reviewAdapter,
			Threshold: 7.0,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(reviewResult.ProvenIssues).To(HaveLen(1))

		// Verify review.json artifact exists.
		reviewJSON := filepath.Join(reviewDir, "review.json")
		Expect(reviewJSON).To(BeAnExistingFile())

		// Stage 2: Fix resolves the divide-by-zero.
		fixDir := filepath.Join(outputDir, "fix")
		fixedCode := `package testrepo

func Divide(a, b int) int {
	if b == 0 {
		return 0
	}
	return a / b
}
`
		fixAdapter := &fakeFixAdapter{
			patches: map[string][]fix.FilePatch{
				"ISS-001": {{Path: "math.go", Content: fixedCode}},
			},
		}

		report, err := fix.RunFixPipeline(ctx, fix.FixOptions{
			RepoDir:     repoDir,
			ReviewDir:   reviewDir,
			OutputDir:   fixDir,
			Adapter:     fixAdapter,
			FixBranch:   "fix/divide-by-zero",
			MaxRetries:  2,
			TestCommand: "go test ./...",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(report.Summary.Fixed).To(Equal(1))

		// Verify fix-report.json artifact exists.
		fixReportPath := filepath.Join(fixDir, "fix-report.json")
		Expect(fixReportPath).To(BeAnExistingFile())

		// Verify the source file was actually fixed.
		fixedSrc, err := os.ReadFile(filepath.Join(repoDir, "math.go"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(fixedSrc)).To(ContainSubstring("if b == 0"))

		// Stage 3: QA validates the fix against the spec.
		qaDir := filepath.Join(outputDir, "qa")
		specPath := filepath.Join(repoDir, "spec.md")

		// Write a test that proves the fix works.
		Expect(os.WriteFile(filepath.Join(repoDir, "math_qa_test.go"), []byte(`package testrepo

import "testing"

func TestDivideReturnsZeroForZeroDivisor(t *testing.T) {
	if Divide(10, 0) != 0 {
		t.Fatal("expected 0 for zero divisor")
	}
}
`), 0644)).To(Succeed())

		qaOutput, err := orchestrator.RunQA(ctx, orchestrator.QAOptions{
			RepoDir:   repoDir,
			SpecFile:  specPath,
			OutputDir: qaDir,
			Config:    &config.QAConfig{Threshold: 3.0},
		})
		Expect(err).NotTo(HaveOccurred())

		// The requirement should be covered since we have matching tests.
		hasCoverage := false
		for _, r := range qaOutput.Results {
			if r.CoveragePoints > 0 {
				hasCoverage = true
			}
		}
		Expect(hasCoverage).To(BeTrue(), "QA should find coverage after fix")

		// Verify qa.json artifact exists and is valid.
		qaPath := filepath.Join(qaDir, "qa.json")
		Expect(qaPath).To(BeAnExistingFile())

		data, err := os.ReadFile(qaPath)
		Expect(err).NotTo(HaveOccurred())
		var decoded schema.QAOutput
		Expect(json.Unmarshal(data, &decoded)).To(Succeed())
		Expect(decoded.Validate()).To(Succeed())

		// Verify the three-stage artifact chain.
		Expect(reviewJSON).To(BeAnExistingFile())
		Expect(fixReportPath).To(BeAnExistingFile())
		Expect(qaPath).To(BeAnExistingFile())
	})
})
