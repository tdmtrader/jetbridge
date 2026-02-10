package runner_test

import (
	"context"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/runner"
)

var _ = Describe("TestRunner", func() {
	var (
		ctx     context.Context
		repoDir string
	)

	BeforeEach(func() {
		ctx = context.Background()

		var err error
		repoDir, err = os.MkdirTemp("", "ci-agent-runner-test-*")
		Expect(err).NotTo(HaveOccurred())

		// Create a minimal Go module in the temp dir so `go test` works.
		err = os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module example.com/testrepo\n\ngo 1.25\n"), 0644)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		os.RemoveAll(repoDir)
	})

	Describe("RunTest", func() {
		It("returns pass for a passing test", func() {
			testCode := `package testrepo

import "testing"

func TestPassingExample(t *testing.T) {
	if 1+1 != 2 {
		t.Fatal("math is broken")
	}
}
`
			testFile := filepath.Join(repoDir, "pass_test.go")
			err := os.WriteFile(testFile, []byte(testCode), 0644)
			Expect(err).NotTo(HaveOccurred())

			result, err := runner.RunTest(ctx, repoDir, testFile)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Pass).To(BeTrue())
			Expect(result.Error).To(BeFalse())
			Expect(result.Output).To(ContainSubstring("ok"))
			Expect(result.Duration).To(BeNumerically(">", 0))
		})

		It("returns fail for a failing test", func() {
			testCode := `package testrepo

import "testing"

func TestFailingExample(t *testing.T) {
	t.Fatal("intentional failure")
}
`
			testFile := filepath.Join(repoDir, "fail_test.go")
			err := os.WriteFile(testFile, []byte(testCode), 0644)
			Expect(err).NotTo(HaveOccurred())

			result, err := runner.RunTest(ctx, repoDir, testFile)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Pass).To(BeFalse())
			Expect(result.Error).To(BeFalse())
			Expect(result.Output).To(ContainSubstring("intentional failure"))
		})

		It("returns error for a compilation error", func() {
			testCode := `package testrepo

import "testing"

func TestCompileError(t *testing.T) {
	undefinedFunction()
}
`
			testFile := filepath.Join(repoDir, "compile_test.go")
			err := os.WriteFile(testFile, []byte(testCode), 0644)
			Expect(err).NotTo(HaveOccurred())

			result, err := runner.RunTest(ctx, repoDir, testFile)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Error).To(BeTrue())
			Expect(result.Pass).To(BeFalse())
			Expect(result.Output).To(ContainSubstring("undefined"))
		})

		It("returns error on timeout", func() {
			testCode := `package testrepo

import (
	"testing"
	"time"
)

func TestTimeout(t *testing.T) {
	time.Sleep(10 * time.Second)
}
`
			testFile := filepath.Join(repoDir, "timeout_test.go")
			err := os.WriteFile(testFile, []byte(testCode), 0644)
			Expect(err).NotTo(HaveOccurred())

			shortCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()

			result, err := runner.RunTest(shortCtx, repoDir, testFile)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Error).To(BeTrue())
			Expect(result.Pass).To(BeFalse())
		})

		It("places test file in correct package directory", func() {
			// Create a subpackage.
			pkgDir := filepath.Join(repoDir, "pkg", "util")
			err := os.MkdirAll(pkgDir, 0755)
			Expect(err).NotTo(HaveOccurred())

			err = os.WriteFile(filepath.Join(pkgDir, "util.go"), []byte(`package util

func Add(a, b int) int { return a + b }
`), 0644)
			Expect(err).NotTo(HaveOccurred())

			testCode := `package util

import "testing"

func TestAdd(t *testing.T) {
	if Add(2, 3) != 5 {
		t.Fatal("Add is wrong")
	}
}
`
			testFile := filepath.Join(pkgDir, "util_test.go")
			err = os.WriteFile(testFile, []byte(testCode), 0644)
			Expect(err).NotTo(HaveOccurred())

			result, err := runner.RunTest(ctx, repoDir, testFile)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Pass).To(BeTrue())
		})
	})

	Describe("RunTests", func() {
		It("runs multiple test files independently", func() {
			// Place each test in its own subpackage so they compile independently.
			passDir := filepath.Join(repoDir, "pkga")
			failDir := filepath.Join(repoDir, "pkgb")
			Expect(os.MkdirAll(passDir, 0755)).To(Succeed())
			Expect(os.MkdirAll(failDir, 0755)).To(Succeed())

			passTest := `package pkga

import "testing"

func TestPass(t *testing.T) {}
`
			failTest := `package pkgb

import "testing"

func TestFail(t *testing.T) { t.Fatal("fail") }
`
			passFile := filepath.Join(passDir, "a_test.go")
			failFile := filepath.Join(failDir, "b_test.go")

			Expect(os.WriteFile(passFile, []byte(passTest), 0644)).To(Succeed())
			Expect(os.WriteFile(failFile, []byte(failTest), 0644)).To(Succeed())

			results, err := runner.RunTests(ctx, repoDir, []string{passFile, failFile})
			Expect(err).NotTo(HaveOccurred())
			Expect(results).To(HaveLen(2))
			Expect(results[passFile].Pass).To(BeTrue())
			Expect(results[failFile].Pass).To(BeFalse())
		})
	})
})
