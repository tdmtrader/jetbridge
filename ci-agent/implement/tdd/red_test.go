package tdd_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/implement/adapter"
	"github.com/concourse/ci-agent/implement/tdd"
)

var _ = Describe("WriteTestFile", func() {
	It("writes test content to the correct path", func() {
		repoDir := GinkgoT().TempDir()
		resp := &adapter.TestGenResponse{
			TestFilePath: "pkg/widget_test.go",
			TestContent:  "package pkg_test\n\nfunc TestWidget() {}",
			PackageName:  "pkg_test",
		}

		err := tdd.WriteTestFile(repoDir, resp)
		Expect(err).NotTo(HaveOccurred())

		content, err := os.ReadFile(filepath.Join(repoDir, "pkg/widget_test.go"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(ContainSubstring("TestWidget"))
	})

	It("creates parent directories if needed", func() {
		repoDir := GinkgoT().TempDir()
		resp := &adapter.TestGenResponse{
			TestFilePath: "deep/nested/pkg/test_test.go",
			TestContent:  "package pkg_test",
			PackageName:  "pkg_test",
		}

		err := tdd.WriteTestFile(repoDir, resp)
		Expect(err).NotTo(HaveOccurred())

		_, err = os.Stat(filepath.Join(repoDir, "deep/nested/pkg/test_test.go"))
		Expect(err).NotTo(HaveOccurred())
	})

	It("overwrites existing file on retry", func() {
		repoDir := GinkgoT().TempDir()
		path := filepath.Join(repoDir, "test_test.go")
		err := os.WriteFile(path, []byte("old content"), 0644)
		Expect(err).NotTo(HaveOccurred())

		resp := &adapter.TestGenResponse{
			TestFilePath: "test_test.go",
			TestContent:  "new content",
			PackageName:  "test",
		}

		err = tdd.WriteTestFile(repoDir, resp)
		Expect(err).NotTo(HaveOccurred())

		content, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(Equal("new content"))
	})
})

var _ = Describe("VerifyRed", func() {
	It("returns confirmed when test fails", func() {
		repoDir := setupGoModule(GinkgoT().TempDir())
		writeGoTest(repoDir, "fail_test.go", `package testmod

import "testing"

func TestFail(t *testing.T) {
	t.Fatal("expected failure")
}
`)
		result, err := tdd.VerifyRed(context.Background(), repoDir, filepath.Join(repoDir, "fail_test.go"))
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Confirmed).To(BeTrue())
	})

	It("returns not confirmed when test passes", func() {
		repoDir := setupGoModule(GinkgoT().TempDir())
		writeGoTest(repoDir, "pass_test.go", `package testmod

import "testing"

func TestPass(t *testing.T) {
	// passes
}
`)
		result, err := tdd.VerifyRed(context.Background(), repoDir, filepath.Join(repoDir, "pass_test.go"))
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Confirmed).To(BeFalse())
		Expect(result.Reason).To(ContainSubstring("already passes"))
	})

	It("returns not confirmed on compilation error", func() {
		repoDir := setupGoModule(GinkgoT().TempDir())
		writeGoTest(repoDir, "bad_test.go", `package testmod

import "testing"

func TestBad(t *testing.T) {
	undefinedFunction()
}
`)
		result, err := tdd.VerifyRed(context.Background(), repoDir, filepath.Join(repoDir, "bad_test.go"))
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Confirmed).To(BeFalse())
		Expect(result.Reason).To(ContainSubstring("compilation error"))
	})
})
