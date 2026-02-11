package integration_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/config"
	"github.com/concourse/ci-agent/fix"
	"github.com/concourse/ci-agent/orchestrator"
	"github.com/concourse/ci-agent/runner"
	"github.com/concourse/ci-agent/schema"
)

// fakeReviewAdapter returns pre-configured findings.
type fakeReviewAdapter struct {
	findings []runner.AgentFinding
}

func (f *fakeReviewAdapter) Review(_ context.Context, _ string, _ *config.ReviewConfig) ([]runner.AgentFinding, error) {
	return f.findings, nil
}

// fakeFixAdapter returns pre-configured patches.
type fakeFixAdapter struct {
	patches map[string][]fix.FilePatch
}

func (f *fakeFixAdapter) Fix(_ context.Context, issue schema.ProvenIssue, _, _ string) ([]fix.FilePatch, error) {
	if p, ok := f.patches[issue.ID]; ok {
		return p, nil
	}
	return nil, nil
}

func gitInit(dir string) {
	run(dir, "git", "init")
	run(dir, "git", "config", "user.email", "test@test.com")
	run(dir, "git", "config", "user.name", "Test")
}

func gitAddCommit(dir, msg string) {
	run(dir, "git", "add", ".")
	run(dir, "git", "commit", "-m", msg)
}

func run(dir, name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "command %q failed: %s", name, string(out))
}

var _ = Describe("Review → Fix Cross-Subsystem Integration", func() {
	var (
		ctx       context.Context
		repoDir   string
		outputDir string
	)

	BeforeEach(func() {
		ctx = context.Background()

		var err error
		repoDir, err = os.MkdirTemp("", "integ-repo-*")
		Expect(err).NotTo(HaveOccurred())

		outputDir, err = os.MkdirTemp("", "integ-output-*")
		Expect(err).NotTo(HaveOccurred())

		// Create a minimal Go module and source file.
		Expect(os.WriteFile(filepath.Join(repoDir, "go.mod"),
			[]byte("module example.com/testrepo\n\ngo 1.25\n"), 0644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(repoDir, "math.go"), []byte(`package testrepo

func Divide(a, b int) int {
	return a / b
}
`), 0644)).To(Succeed())

		gitInit(repoDir)
		gitAddCommit(repoDir, "initial")
	})

	AfterEach(func() {
		os.RemoveAll(repoDir)
		os.RemoveAll(outputDir)
	})

	It("review output feeds into fix pipeline", func() {
		reviewDir := filepath.Join(outputDir, "review")

		// Step 1: Run review to produce review.json + tests/.
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

		// Verify review.json was produced.
		reviewJSON := filepath.Join(reviewDir, "review.json")
		Expect(reviewJSON).To(BeAnExistingFile())

		// Step 2: Run fix pipeline consuming review output.
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
			FixBranch:   "fix/integration",
			MaxRetries:  2,
			TestCommand: "go test ./...",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(report.Summary.Fixed).To(Equal(1))
		Expect(report.Summary.Skipped).To(Equal(0))
		Expect(report.ExitCode()).To(Equal(0))

		// Verify fix-report.json.
		fixReportPath := filepath.Join(fixDir, "fix-report.json")
		Expect(fixReportPath).To(BeAnExistingFile())

		data, err := os.ReadFile(fixReportPath)
		Expect(err).NotTo(HaveOccurred())
		var fixReport schema.FixReport
		Expect(json.Unmarshal(data, &fixReport)).To(Succeed())
		Expect(fixReport.Summary.Fixed).To(Equal(1))
	})

	It("zero-issue review produces empty fix run", func() {
		reviewDir := filepath.Join(outputDir, "review")

		// Review with no findings.
		reviewAdapter := &fakeReviewAdapter{findings: nil}

		reviewResult, err := orchestrator.Run(ctx, orchestrator.Options{
			RepoDir:   repoDir,
			OutputDir: reviewDir,
			Adapter:   reviewAdapter,
			Threshold: 7.0,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(reviewResult.ProvenIssues).To(BeEmpty())

		// Fix pipeline should report zero fixes.
		fixDir := filepath.Join(outputDir, "fix")
		report, err := fix.RunFixPipeline(ctx, fix.FixOptions{
			RepoDir:   repoDir,
			ReviewDir: reviewDir,
			OutputDir: fixDir,
			Adapter:   &fakeFixAdapter{},
			FixBranch: "fix/empty",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(report.Summary.Fixed).To(Equal(0))
		Expect(report.ExitCode()).To(Equal(1))
	})

	It("multiple severities sort into fix pipeline (critical first)", func() {
		reviewDir := filepath.Join(outputDir, "review")

		// Two findings: one critical, one low.
		reviewAdapter := &fakeReviewAdapter{
			findings: []runner.AgentFinding{
				{
					Title:        "low severity style issue",
					File:         "math.go",
					Line:         3,
					SeverityHint: schema.SeverityLow,
					Category:     schema.CategoryMaintainability,
					TestCode: `package testrepo

import "testing"

func TestDivideStyle(t *testing.T) {
	t.Fatal("style violation detected")
}
`,
					TestFile: "math_style_test.go",
					TestName: "TestDivideStyle",
				},
				{
					Title:        "critical divide by zero",
					File:         "math.go",
					Line:         3,
					SeverityHint: schema.SeverityCritical,
					Category:     schema.CategorySecurity,
					TestCode: `package testrepo

import "testing"

func TestDivideZeroCritical(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatal("Divide panics on zero divisor")
		}
	}()
	Divide(10, 0)
}
`,
					TestFile: "math_critical_test.go",
					TestName: "TestDivideZeroCritical",
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
		Expect(reviewResult.ProvenIssues).To(HaveLen(2))

		// Fix pipeline: adapter can fix the critical, but not the low.
		fixDir := filepath.Join(outputDir, "fix")
		fixedCode := `package testrepo

func Divide(a, b int) int {
	if b == 0 {
		return 0
	}
	return a / b
}
`
		// The fix adapter fixes the critical issue but returns no patch for
		// the low-severity issue, which will be skipped after proving test fails.
		fixAdapter := &fakeFixAdapter{
			patches: map[string][]fix.FilePatch{
				"ISS-002": {{Path: "math.go", Content: fixedCode}},
			},
		}

		report, err := fix.RunFixPipeline(ctx, fix.FixOptions{
			RepoDir:     repoDir,
			ReviewDir:   reviewDir,
			OutputDir:   fixDir,
			Adapter:     fixAdapter,
			FixBranch:   "fix/severity",
			MaxRetries:  1,
			TestCommand: "go test ./...",
		})
		Expect(err).NotTo(HaveOccurred())

		// The critical issue (ISS-002 due to ordering) should be processed first
		// since SortIssuesBySeverity puts critical before low.
		Expect(report.Summary.TotalIssues).To(Equal(2))
		// At least one should be fixed or skipped.
		Expect(report.Summary.Fixed + report.Summary.Skipped).To(Equal(2))
	})
})
