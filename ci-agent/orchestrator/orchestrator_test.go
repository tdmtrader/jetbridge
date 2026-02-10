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
	"github.com/concourse/ci-agent/runner"
	"github.com/concourse/ci-agent/schema"
)

// fakeAdapter returns pre-configured findings for testing.
type fakeAdapter struct {
	findings []runner.AgentFinding
	err      error
}

func (f *fakeAdapter) Review(ctx context.Context, repoDir string, cfg *config.ReviewConfig) ([]runner.AgentFinding, error) {
	return f.findings, f.err
}

var _ = Describe("Orchestrator", func() {
	var (
		ctx       context.Context
		repoDir   string
		outputDir string
	)

	BeforeEach(func() {
		ctx = context.Background()

		var err error
		repoDir, err = os.MkdirTemp("", "orch-repo-*")
		Expect(err).NotTo(HaveOccurred())

		outputDir, err = os.MkdirTemp("", "orch-output-*")
		Expect(err).NotTo(HaveOccurred())

		// Create a minimal Go module so tests can run.
		Expect(os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module example.com/testrepo\n\ngo 1.25\n"), 0644)).To(Succeed())
	})

	AfterEach(func() {
		os.RemoveAll(repoDir)
		os.RemoveAll(outputDir)
	})

	It("runs full pipeline with zero issues and passes", func() {
		adapter := &fakeAdapter{findings: nil}

		result, err := orchestrator.Run(ctx, orchestrator.Options{
			RepoDir:        repoDir,
			OutputDir:      outputDir,
			Adapter:        adapter,
			Threshold:      7.0,
			FailOnCritical: false,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Score.Value).To(Equal(10.0))
		Expect(result.Score.Pass).To(BeTrue())
		Expect(result.ProvenIssues).To(BeEmpty())

		// Verify review.json written.
		reviewPath := filepath.Join(outputDir, "review.json")
		data, err := os.ReadFile(reviewPath)
		Expect(err).NotTo(HaveOccurred())

		var written schema.ReviewOutput
		Expect(json.Unmarshal(data, &written)).To(Succeed())
		Expect(written.Score.Value).To(Equal(10.0))
	})

	It("writes test files and runs them", func() {
		// Create a source file in the repo that the test will reference.
		Expect(os.WriteFile(filepath.Join(repoDir, "math.go"), []byte(`package testrepo

func Add(a, b int) int { return a + b }
`), 0644)).To(Succeed())

		adapter := &fakeAdapter{
			findings: []runner.AgentFinding{
				{
					Title:        "Add returns wrong result",
					File:         "math.go",
					Line:         3,
					SeverityHint: schema.SeverityHigh,
					Category:     schema.CategoryCorrectness,
					TestCode: `package testrepo

import "testing"

func TestAddWrong(t *testing.T) {
	if Add(2, 3) != 6 {
		t.Fatal("Add(2,3) should be 6 but it is not")
	}
}
`,
					TestFile: "math_review_test.go",
					TestName: "TestAddWrong",
				},
			},
		}

		result, err := orchestrator.Run(ctx, orchestrator.Options{
			RepoDir:        repoDir,
			OutputDir:      outputDir,
			Adapter:        adapter,
			Threshold:      7.0,
			FailOnCritical: false,
		})
		Expect(err).NotTo(HaveOccurred())

		// The test should fail (2+3=5, not 6), so it's a proven issue.
		Expect(result.ProvenIssues).To(HaveLen(1))
		Expect(result.Score.Value).To(Equal(8.5)) // 10 - 1.5 (high)

		// Verify test file was written to output.
		testsDir := filepath.Join(outputDir, "tests")
		_, err = os.Stat(filepath.Join(testsDir, "math_review_test.go"))
		Expect(err).NotTo(HaveOccurred())
	})

	It("returns failing score when threshold not met", func() {
		adapter := &fakeAdapter{
			findings: []runner.AgentFinding{
				{
					Title: "issue1", File: "a.go", Line: 1, SeverityHint: schema.SeverityCritical,
					Category: schema.CategorySecurity,
					TestCode: `package testrepo

import "testing"

func TestIssue1(t *testing.T) { t.Fatal("proven") }
`,
					TestFile: "a_review_test.go", TestName: "TestIssue1",
				},
				{
					Title: "issue2", File: "b.go", Line: 2, SeverityHint: schema.SeverityCritical,
					Category: schema.CategorySecurity,
					TestCode: `package testrepo

import "testing"

func TestIssue2(t *testing.T) { t.Fatal("proven") }
`,
					TestFile: "b_review_test.go", TestName: "TestIssue2",
				},
			},
		}

		result, err := orchestrator.Run(ctx, orchestrator.Options{
			RepoDir:        repoDir,
			OutputDir:      outputDir,
			Adapter:        adapter,
			Threshold:      7.0,
			FailOnCritical: false,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Score.Value).To(Equal(4.0)) // 10 - 3.0 - 3.0
		Expect(result.Score.Pass).To(BeFalse())
	})

	It("discards passing tests (unfounded concerns)", func() {
		Expect(os.WriteFile(filepath.Join(repoDir, "safe.go"), []byte(`package testrepo

func Safe() bool { return true }
`), 0644)).To(Succeed())

		adapter := &fakeAdapter{
			findings: []runner.AgentFinding{
				{
					Title: "Safe might return false", File: "safe.go", Line: 3,
					SeverityHint: schema.SeverityMedium, Category: schema.CategoryCorrectness,
					TestCode: `package testrepo

import "testing"

func TestSafe(t *testing.T) {
	if !Safe() {
		t.Fatal("Safe returned false")
	}
}
`,
					TestFile: "safe_review_test.go", TestName: "TestSafe",
				},
			},
		}

		result, err := orchestrator.Run(ctx, orchestrator.Options{
			RepoDir:   repoDir,
			OutputDir: outputDir,
			Adapter:   adapter,
			Threshold: 7.0,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.ProvenIssues).To(BeEmpty())
		Expect(result.Score.Value).To(Equal(10.0))
	})

	It("handles adapter error gracefully", func() {
		adapter := &fakeAdapter{
			err: context.DeadlineExceeded,
		}

		result, err := orchestrator.Run(ctx, orchestrator.Options{
			RepoDir:   repoDir,
			OutputDir: outputDir,
			Adapter:   adapter,
			Threshold: 7.0,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Summary).To(ContainSubstring("error"))
	})
})
