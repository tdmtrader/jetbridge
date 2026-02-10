package fix_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/fix"
	"github.com/concourse/ci-agent/schema"
)

type fakeFixAdapter struct {
	patches map[string][]fix.FilePatch // keyed by issue ID
	err     error
}

func (f *fakeFixAdapter) Fix(ctx context.Context, issue schema.ProvenIssue, fileContent, testCode string) ([]fix.FilePatch, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.patches[issue.ID], nil
}

var _ = Describe("FixEngine", func() {
	var (
		ctx     context.Context
		repoDir string
	)

	BeforeEach(func() {
		ctx = context.Background()

		var err error
		repoDir, err = os.MkdirTemp("", "fix-engine-*")
		Expect(err).NotTo(HaveOccurred())

		// Init git repo with go module and a buggy file.
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
		run(repoDir, "git", "commit", "-m", "initial commit")
	})

	AfterEach(func() {
		os.RemoveAll(repoDir)
	})

	Describe("FixSingleIssue", func() {
		It("fixes an issue when proving test passes after patch", func() {
			// Write a proving test that fails before the fix.
			testCode := `package testrepo

import "testing"

func TestDivideByZero(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatal("Divide panics on zero divisor")
		}
	}()
	Divide(10, 0)
}
`
			testFile := filepath.Join(repoDir, "math_prove_test.go")
			Expect(os.WriteFile(testFile, []byte(testCode), 0644)).To(Succeed())

			issue := schema.ProvenIssue{
				ID:       "ISS-001",
				Severity: schema.SeverityHigh,
				Title:    "Divide panics on zero",
				File:     "math.go",
				Line:     3,
				TestFile: "math_prove_test.go",
				TestName: "TestDivideByZero",
				Category: schema.CategoryCorrectness,
			}

			// The fix: add zero check.
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

			engine := fix.NewEngine(adapter, 2)
			result := engine.FixSingleIssue(ctx, repoDir, issue, testCode)

			Expect(result.Status).To(Equal("fixed"))
			Expect(result.FilesChanged).To(ContainElement("math.go"))
			Expect(result.CommitSHA).NotTo(BeEmpty())
			Expect(result.Attempts).To(Equal(1))
		})

		It("skips when proving test still fails after max retries", func() {
			testCode := `package testrepo

import "testing"

func TestAlwaysFails(t *testing.T) {
	t.Fatal("always fails")
}
`
			testFile := filepath.Join(repoDir, "always_test.go")
			Expect(os.WriteFile(testFile, []byte(testCode), 0644)).To(Succeed())

			issue := schema.ProvenIssue{
				ID:       "ISS-002",
				Severity: schema.SeverityMedium,
				Title:    "Unfixable",
				File:     "math.go",
				Line:     1,
				TestFile: "always_test.go",
				TestName: "TestAlwaysFails",
				Category: schema.CategoryCorrectness,
			}

			// Adapter returns patch that doesn't fix the issue.
			adapter := &fakeFixAdapter{
				patches: map[string][]fix.FilePatch{
					"ISS-002": {{Path: "math.go", Content: "package testrepo\n\nfunc Divide(a, b int) int { return a / b }\n"}},
				},
			}

			engine := fix.NewEngine(adapter, 2)
			result := engine.FixSingleIssue(ctx, repoDir, issue, testCode)

			Expect(result.Status).To(Equal("skipped"))
			Expect(result.Reason).To(Equal(schema.SkipFailedVerification))
			Expect(result.Attempts).To(Equal(2))
		})

		It("skips on adapter error", func() {
			issue := schema.ProvenIssue{
				ID:       "ISS-003",
				Severity: schema.SeverityLow,
				Title:    "Something",
				File:     "math.go",
				Line:     1,
				TestFile: "test.go",
				TestName: "TestX",
				Category: schema.CategoryCorrectness,
			}

			adapter := &fakeFixAdapter{err: context.DeadlineExceeded}
			engine := fix.NewEngine(adapter, 2)
			result := engine.FixSingleIssue(ctx, repoDir, issue, "test code")

			Expect(result.Status).To(Equal("skipped"))
			Expect(result.Reason).To(Equal(schema.SkipAgentError))
		})
	})
})

func readFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// initGitRepo creates a temp dir with a git repo, go.mod, and initial commit.
// Returns the repo dir path.
func initGitRepo() string {
	dir, _ := os.MkdirTemp("", "fix-test-*")
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	cmd.Run()
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = dir
	cmd.Run()
	cmd = exec.Command("git", "config", "user.name", "Test")
	cmd.Dir = dir
	cmd.Run()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/testrepo\n\ngo 1.25\n"), 0644)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package testrepo\n"), 0644)
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = dir
	cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "init")
	cmd.Dir = dir
	cmd.Run()
	return dir
}
