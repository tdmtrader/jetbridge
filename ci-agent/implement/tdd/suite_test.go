package tdd_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/implement/tdd"
)

var _ = Describe("RunSuite", func() {
	It("returns pass when all tests pass", func() {
		repoDir := setupGoModule(GinkgoT().TempDir())
		writeGoTest(repoDir, "a_test.go", `package testmod

import "testing"

func TestA(t *testing.T) {}
`)
		result, err := tdd.RunSuite(context.Background(), repoDir, "go test ./...")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Pass).To(BeTrue())
	})

	It("returns fail with output when tests fail", func() {
		repoDir := setupGoModule(GinkgoT().TempDir())
		writeGoTest(repoDir, "a_test.go", `package testmod

import "testing"

func TestFail(t *testing.T) {
	t.Fatal("suite failure")
}
`)
		result, err := tdd.RunSuite(context.Background(), repoDir, "go test ./...")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Pass).To(BeFalse())
		Expect(result.Output).To(ContainSubstring("suite failure"))
	})

	It("returns fail on compilation error", func() {
		repoDir := setupGoModule(GinkgoT().TempDir())
		writeGoTest(repoDir, "bad_test.go", `package testmod

func BadFunction() { undefinedCall() }
`)
		result, err := tdd.RunSuite(context.Background(), repoDir, "go test ./...")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Pass).To(BeFalse())
	})
})
