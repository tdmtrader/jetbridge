package tdd_test

import (
	"context"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/implement/tdd"
)

var _ = Describe("VerifyGreen", func() {
	It("returns confirmed when test passes", func() {
		repoDir := setupGoModule(GinkgoT().TempDir())
		writeGoTest(repoDir, "pass_test.go", `package testmod

import "testing"

func TestPass(t *testing.T) {
	// passes
}
`)
		result, err := tdd.VerifyGreen(context.Background(), repoDir, filepath.Join(repoDir, "pass_test.go"))
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Confirmed).To(BeTrue())
	})

	It("returns not confirmed when test still fails", func() {
		repoDir := setupGoModule(GinkgoT().TempDir())
		writeGoTest(repoDir, "fail_test.go", `package testmod

import "testing"

func TestFail(t *testing.T) {
	t.Fatal("still failing")
}
`)
		result, err := tdd.VerifyGreen(context.Background(), repoDir, filepath.Join(repoDir, "fail_test.go"))
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Confirmed).To(BeFalse())
		Expect(result.Output).To(ContainSubstring("still failing"))
	})

	It("returns not confirmed on compilation error", func() {
		repoDir := setupGoModule(GinkgoT().TempDir())
		writeGoTest(repoDir, "bad_test.go", `package testmod

import "testing"

func TestBad(t *testing.T) {
	undefinedFunc()
}
`)
		result, err := tdd.VerifyGreen(context.Background(), repoDir, filepath.Join(repoDir, "bad_test.go"))
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Confirmed).To(BeFalse())
		Expect(result.Output).To(ContainSubstring("undefined"))
	})
})
