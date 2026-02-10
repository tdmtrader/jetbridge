package tdd_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/implement/tdd"
)

var _ = Describe("CheckRegression", func() {
	It("returns clean when suite passes", func() {
		repoDir := setupGoModule(GinkgoT().TempDir())
		writeGoTest(repoDir, "ok_test.go", `package testmod

import "testing"

func TestOk(t *testing.T) {}
`)
		result, err := tdd.CheckRegression(context.Background(), repoDir, "go test ./...")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Clean).To(BeTrue())
	})

	It("returns regression when suite fails", func() {
		repoDir := setupGoModule(GinkgoT().TempDir())
		writeGoTest(repoDir, "ok_test.go", `package testmod

import "testing"

func TestOk(t *testing.T) {}
func TestBroken(t *testing.T) { t.Fatal("regression") }
`)
		result, err := tdd.CheckRegression(context.Background(), repoDir, "go test ./...")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Clean).To(BeFalse())
		Expect(result.Output).To(ContainSubstring("regression"))
	})

	It("revert removes implementation files from working tree", func() {
		repoDir := setupGoModule(GinkgoT().TempDir())
		implFile := filepath.Join(repoDir, "impl.go")
		os.WriteFile(implFile, []byte("package testmod\nfunc Impl() {}"), 0644)

		err := tdd.RevertFiles(repoDir, []string{"impl.go"})
		Expect(err).NotTo(HaveOccurred())

		_, statErr := os.Stat(implFile)
		Expect(os.IsNotExist(statErr)).To(BeTrue())
	})
})
