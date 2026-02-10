package fix_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/fix"
	"github.com/concourse/ci-agent/schema"
)

var _ = Describe("Fix Orchestrator", func() {
	var (
		ctx       context.Context
		repoDir   string
		reviewDir string
		outputDir string
	)

	BeforeEach(func() {
		ctx = context.Background()

		var err error
		repoDir, err = os.MkdirTemp("", "fix-orch-repo-*")
		Expect(err).NotTo(HaveOccurred())

		reviewDir, err = os.MkdirTemp("", "fix-orch-review-*")
		Expect(err).NotTo(HaveOccurred())

		outputDir, err = os.MkdirTemp("", "fix-orch-output-*")
		Expect(err).NotTo(HaveOccurred())

		// Init git repo.
		run(repoDir, "git", "init")
		run(repoDir, "git", "config", "user.email", "test@test.com")
		run(repoDir, "git", "config", "user.name", "Test")

		Expect(os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module example.com/testrepo\n\ngo 1.25\n"), 0644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(repoDir, "math.go"), []byte(`package testrepo

func Divide(a, b int) int {
	return a / b
}
`), 0644)).To(Succeed())

		run(repoDir, "git", "add", ".")
		run(repoDir, "git", "commit", "-m", "initial")
	})

	AfterEach(func() {
		os.RemoveAll(repoDir)
		os.RemoveAll(reviewDir)
		os.RemoveAll(outputDir)
	})

	It("orchestrates fix pipeline end-to-end", func() {
		// Write review.json.
		review := schema.ReviewOutput{
			SchemaVersion: "1.0.0",
			ProvenIssues: []schema.ProvenIssue{
				{
					ID: "ISS-001", Severity: schema.SeverityHigh,
					Title: "divide by zero", File: "math.go", Line: 3,
					TestFile: "math_prove_test.go", TestName: "TestDivideByZero",
					Category: schema.CategoryCorrectness,
				},
			},
			Summary: "1 proven issue",
		}
		data, _ := json.MarshalIndent(review, "", "  ")
		Expect(os.WriteFile(filepath.Join(reviewDir, "review.json"), data, 0644)).To(Succeed())

		// Write proving test.
		testCode := `package testrepo

import "testing"

func TestDivideByZero(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatal("Divide panics on zero")
		}
	}()
	Divide(10, 0)
}
`
		Expect(os.MkdirAll(filepath.Join(reviewDir, "tests"), 0755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(reviewDir, "tests", "math_prove_test.go"), []byte(testCode), 0644)).To(Succeed())

		// Fix adapter that returns a correct fix.
		fixedCode := `package testrepo

func Divide(a, b int) int {
	if b == 0 {
		return 0
	}
	return a / b
}
`
		adapter := &fakeFixAdapter{
			patches: map[string][]fix.FilePatch{
				"ISS-001": {{Path: "math.go", Content: fixedCode}},
			},
		}

		report, err := fix.RunFixPipeline(ctx, fix.FixOptions{
			RepoDir:     repoDir,
			ReviewDir:   reviewDir,
			OutputDir:   outputDir,
			Adapter:     adapter,
			FixBranch:   "fix/test",
			MaxRetries:  2,
			TestCommand: "go test ./...",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(report.Summary.Fixed).To(Equal(1))
		Expect(report.Summary.Skipped).To(Equal(0))
		Expect(report.ExitCode()).To(Equal(0))

		// Verify fix-report.json written.
		reportPath := filepath.Join(outputDir, "fix-report.json")
		_, err = os.Stat(reportPath)
		Expect(err).NotTo(HaveOccurred())
	})

	It("writes fix-report.json even when no issues fixed", func() {
		review := schema.ReviewOutput{
			SchemaVersion: "1.0.0",
			ProvenIssues:  []schema.ProvenIssue{},
			Summary:       "no issues",
		}
		data, _ := json.MarshalIndent(review, "", "  ")
		Expect(os.WriteFile(filepath.Join(reviewDir, "review.json"), data, 0644)).To(Succeed())

		adapter := &fakeFixAdapter{}

		report, err := fix.RunFixPipeline(ctx, fix.FixOptions{
			RepoDir:   repoDir,
			ReviewDir: reviewDir,
			OutputDir: outputDir,
			Adapter:   adapter,
			FixBranch: "fix/empty",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(report.Summary.Fixed).To(Equal(0))
		Expect(report.ExitCode()).To(Equal(1))
	})
})
