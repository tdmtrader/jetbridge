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

	It("deducts for duplicate findings on same file/line", func() {
		adapter := &fakeAdapter{
			findings: []runner.AgentFinding{
				{
					Title: "duplicate issue A", File: "dup.go", Line: 5,
					SeverityHint: schema.SeverityHigh, Category: schema.CategoryCorrectness,
					TestCode: `package testrepo

import "testing"

func TestDupA(t *testing.T) { t.Fatal("proven A") }
`,
					TestFile: "dup_a_test.go", TestName: "TestDupA",
				},
				{
					Title: "duplicate issue B", File: "dup.go", Line: 5,
					SeverityHint: schema.SeverityHigh, Category: schema.CategoryCorrectness,
					TestCode: `package testrepo

import "testing"

func TestDupB(t *testing.T) { t.Fatal("proven B") }
`,
					TestFile: "dup_b_test.go", TestName: "TestDupB",
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
		// Both findings are separate tests, both proven, both deducted.
		Expect(result.ProvenIssues).To(HaveLen(2))
		Expect(result.Score.Value).To(Equal(7.0)) // 10 - 1.5 - 1.5
		Expect(result.Score.Deductions).To(HaveLen(2))
	})

	It("skips finding with missing TestCode", func() {
		adapter := &fakeAdapter{
			findings: []runner.AgentFinding{
				{
					Title:        "no test code",
					File:         "missing.go",
					Line:         1,
					SeverityHint: schema.SeverityMedium,
					Category:     schema.CategoryCorrectness,
					TestCode:     "",     // empty TestCode
					TestFile:     "",     // empty TestFile
					TestName:     "",
				},
				{
					Title: "has test code", File: "ok.go", Line: 1,
					SeverityHint: schema.SeverityMedium, Category: schema.CategoryCorrectness,
					TestCode: `package testrepo

import "testing"

func TestOK(t *testing.T) { t.Fatal("proven") }
`,
					TestFile: "ok_test.go", TestName: "TestOK",
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
		// Finding without TestCode becomes an observation, not a proven issue.
		Expect(result.ProvenIssues).To(HaveLen(1))
		Expect(result.Observations).To(HaveLen(1))
		Expect(result.Observations[0].Title).To(Equal("no test code"))
	})

	It("handles finding for non-existent source file", func() {
		// The orchestrator trusts the adapter; the test determines truth.
		adapter := &fakeAdapter{
			findings: []runner.AgentFinding{
				{
					Title: "issue in ghost file", File: "ghost.go", Line: 10,
					SeverityHint: schema.SeverityMedium, Category: schema.CategoryCorrectness,
					TestCode: `package testrepo

import "testing"

func TestGhost(t *testing.T) { t.Fatal("proven even without source") }
`,
					TestFile: "ghost_test.go", TestName: "TestGhost",
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
		// The test is written and executed; test result determines the outcome.
		// Since the test calls t.Fatal, it should fail → proven issue.
		Expect(result.ProvenIssues).To(HaveLen(1))
	})
})
