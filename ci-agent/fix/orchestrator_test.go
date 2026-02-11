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

	It("rolls back fix that causes test regression", func() {
		// Create a subpackage with a passing test. The adapter will break this
		// subpackage as a side-effect of the fix. RunTest only runs the root
		// package ("./"), so the proving test passes. The regression guard runs
		// "go test ./..." which catches the broken subpackage.
		Expect(os.MkdirAll(filepath.Join(repoDir, "sub"), 0755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(repoDir, "sub", "helper.go"), []byte(`package sub

func Hello() string { return "hello" }
`), 0644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(repoDir, "sub", "helper_test.go"), []byte(`package sub

import "testing"

func TestHello(t *testing.T) {
	if Hello() != "hello" {
		t.Fatal("expected hello")
	}
}
`), 0644)).To(Succeed())
		run(repoDir, "git", "add", ".")
		run(repoDir, "git", "commit", "-m", "add sub package")

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

		// Fix that correctly addresses divide-by-zero but ALSO breaks the sub package.
		fixedDivide := `package testrepo

func Divide(a, b int) int {
	if b == 0 {
		return 0
	}
	return a / b
}
`
		brokenHelper := `package sub

func Hello() string { return "broken" }
`
		adapter := &fakeFixAdapter{
			patches: map[string][]fix.FilePatch{
				"ISS-001": {
					{Path: "math.go", Content: fixedDivide},
					{Path: "sub/helper.go", Content: brokenHelper},
				},
			},
		}

		report, err := fix.RunFixPipeline(ctx, fix.FixOptions{
			RepoDir:     repoDir,
			ReviewDir:   reviewDir,
			OutputDir:   outputDir,
			Adapter:     adapter,
			FixBranch:   "fix/regression",
			MaxRetries:  1,
			TestCommand: "go test ./...",
		})
		Expect(err).NotTo(HaveOccurred())
		// The regression guard should detect the broken sub/helper test and roll back.
		Expect(report.Summary.RegressionFree).To(BeFalse())
		Expect(report.Summary.Fixed).To(Equal(0))
		// Skipped due to regression.
		hasRegressionSkip := false
		for _, s := range report.Skipped {
			if s.Reason == schema.SkipTestRegression {
				hasRegressionSkip = true
			}
		}
		Expect(hasRegressionSkip).To(BeTrue())
	})

	It("skips when adapter returns patch for wrong file (test still fails)", func() {
		review := schema.ReviewOutput{
			SchemaVersion: "1.0.0",
			ProvenIssues: []schema.ProvenIssue{
				{
					ID: "ISS-001", Severity: schema.SeverityMedium,
					Title: "divide by zero", File: "math.go", Line: 3,
					TestFile: "math_prove_test.go", TestName: "TestDivideByZero",
					Category: schema.CategoryCorrectness,
				},
			},
			Summary: "1 issue",
		}
		data, _ := json.MarshalIndent(review, "", "  ")
		Expect(os.WriteFile(filepath.Join(reviewDir, "review.json"), data, 0644)).To(Succeed())

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

		// Adapter patches the wrong file — math.go unchanged, test still fails.
		adapter := &fakeFixAdapter{
			patches: map[string][]fix.FilePatch{
				"ISS-001": {{Path: "wrong_file.go", Content: "package testrepo\n// unrelated\n"}},
			},
		}

		report, err := fix.RunFixPipeline(ctx, fix.FixOptions{
			RepoDir:    repoDir,
			ReviewDir:  reviewDir,
			OutputDir:  outputDir,
			Adapter:    adapter,
			FixBranch:  "fix/wrong-file",
			MaxRetries: 1,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(report.Summary.Fixed).To(Equal(0))
		Expect(report.Summary.Skipped).To(Equal(1))
		Expect(report.Skipped[0].Reason).To(Equal(schema.SkipFailedVerification))
	})

	It("handles first fix succeeding and second fix failing", func() {
		// ISS-002 lives in a subpackage so its proving test doesn't interfere
		// with ISS-001's proving test (RunTest runs only the target package).
		Expect(os.MkdirAll(filepath.Join(repoDir, "sub"), 0755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(repoDir, "sub", "calc.go"), []byte(`package sub

func Double(x int) int {
	if x < 0 {
		return 0
	}
	return x * 2
}
`), 0644)).To(Succeed())
		run(repoDir, "git", "add", ".")
		run(repoDir, "git", "commit", "-m", "add sub package")

		review := schema.ReviewOutput{
			SchemaVersion: "1.0.0",
			ProvenIssues: []schema.ProvenIssue{
				{
					ID: "ISS-001", Severity: schema.SeverityCritical,
					Title: "divide by zero", File: "math.go", Line: 3,
					TestFile: "math_prove_test.go", TestName: "TestDivideByZero",
					Category: schema.CategoryCorrectness,
				},
				{
					ID: "ISS-002", Severity: schema.SeverityLow,
					Title: "Double mishandles negatives", File: "sub/calc.go", Line: 3,
					TestFile: "sub/calc_prove_test.go", TestName: "TestDoubleNegative",
					Category: schema.CategoryCorrectness,
				},
			},
			Summary: "2 issues",
		}
		data, _ := json.MarshalIndent(review, "", "  ")
		Expect(os.WriteFile(filepath.Join(reviewDir, "review.json"), data, 0644)).To(Succeed())

		// Proving tests.
		divideTest := `package testrepo

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
		// This test fails because Double(-3) returns 0 (bug), not -6.
		calcTest := `package sub

import "testing"

func TestDoubleNegative(t *testing.T) {
	if Double(-3) != -6 {
		t.Fatal("Double(-3) should be -6")
	}
}
`
		Expect(os.MkdirAll(filepath.Join(reviewDir, "tests"), 0755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(reviewDir, "tests", "math_prove_test.go"), []byte(divideTest), 0644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(reviewDir, "tests", "calc_prove_test.go"), []byte(calcTest), 0644)).To(Succeed())

		// Fix adapter: can fix divide by zero, but has no fix for Double.
		fixedDivide := `package testrepo

func Divide(a, b int) int {
	if b == 0 {
		return 0
	}
	return a / b
}
`
		adapter := &fakeFixAdapter{
			patches: map[string][]fix.FilePatch{
				"ISS-001": {{Path: "math.go", Content: fixedDivide}},
				// ISS-002 gets no patch → proving test still fails → skipped.
			},
		}

		report, err := fix.RunFixPipeline(ctx, fix.FixOptions{
			RepoDir:    repoDir,
			ReviewDir:  reviewDir,
			OutputDir:  outputDir,
			Adapter:    adapter,
			FixBranch:  "fix/partial",
			MaxRetries: 1,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(report.Summary.TotalIssues).To(Equal(2))
		Expect(report.Summary.Fixed).To(Equal(1))
		Expect(report.Summary.Skipped).To(Equal(1))
	})
})
