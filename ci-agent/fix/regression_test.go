package fix_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/fix"
)

var _ = Describe("Regression", func() {
	var (
		ctx     context.Context
		repoDir string
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		repoDir, err = os.MkdirTemp("", "fix-regression-*")
		Expect(err).NotTo(HaveOccurred())

		Expect(os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module example.com/testrepo\n\ngo 1.25\n"), 0644)).To(Succeed())
	})

	AfterEach(func() {
		os.RemoveAll(repoDir)
	})

	Describe("RunFullTestSuite", func() {
		It("passes when all tests pass", func() {
			Expect(os.WriteFile(filepath.Join(repoDir, "ok_test.go"), []byte(`package testrepo

import "testing"

func TestOK(t *testing.T) {}
`), 0644)).To(Succeed())

			result, err := fix.RunFullTestSuite(ctx, repoDir, "go test ./...")
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Pass).To(BeTrue())
		})

		It("fails when tests fail", func() {
			Expect(os.WriteFile(filepath.Join(repoDir, "bad_test.go"), []byte(`package testrepo

import "testing"

func TestBad(t *testing.T) { t.Fatal("fail") }
`), 0644)).To(Succeed())

			result, err := fix.RunFullTestSuite(ctx, repoDir, "go test ./...")
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Pass).To(BeFalse())
			Expect(result.Output).To(ContainSubstring("fail"))
		})

		It("uses default command when empty", func() {
			Expect(os.WriteFile(filepath.Join(repoDir, "ok_test.go"), []byte(`package testrepo

import "testing"

func TestOK2(t *testing.T) {}
`), 0644)).To(Succeed())

			result, err := fix.RunFullTestSuite(ctx, repoDir, "")
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Pass).To(BeTrue())
		})
	})
})
