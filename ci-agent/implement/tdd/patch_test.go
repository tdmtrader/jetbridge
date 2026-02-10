package tdd_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/implement/adapter"
	"github.com/concourse/ci-agent/implement/tdd"
)

var _ = Describe("ApplyPatches", func() {
	It("writes files at specified paths", func() {
		repoDir := GinkgoT().TempDir()
		patches := []adapter.FilePatch{
			{Path: "widget.go", Content: "package widget\n"},
			{Path: "model.go", Content: "package widget\ntype Model struct{}\n"},
		}

		files, err := tdd.ApplyPatches(repoDir, patches)
		Expect(err).NotTo(HaveOccurred())
		Expect(files).To(ConsistOf("widget.go", "model.go"))

		content, err := os.ReadFile(filepath.Join(repoDir, "widget.go"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(Equal("package widget\n"))
	})

	It("creates parent directories for new files", func() {
		repoDir := GinkgoT().TempDir()
		patches := []adapter.FilePatch{
			{Path: "deep/nested/pkg/file.go", Content: "package pkg\n"},
		}

		_, err := tdd.ApplyPatches(repoDir, patches)
		Expect(err).NotTo(HaveOccurred())

		_, err = os.Stat(filepath.Join(repoDir, "deep/nested/pkg/file.go"))
		Expect(err).NotTo(HaveOccurred())
	})

	It("overwrites existing files", func() {
		repoDir := GinkgoT().TempDir()
		existing := filepath.Join(repoDir, "widget.go")
		os.WriteFile(existing, []byte("old"), 0644)

		patches := []adapter.FilePatch{
			{Path: "widget.go", Content: "new content"},
		}

		_, err := tdd.ApplyPatches(repoDir, patches)
		Expect(err).NotTo(HaveOccurred())

		content, err := os.ReadFile(existing)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(Equal("new content"))
	})

	It("rejects path traversal attempts", func() {
		repoDir := GinkgoT().TempDir()
		patches := []adapter.FilePatch{
			{Path: "../../../etc/passwd", Content: "evil"},
		}

		_, err := tdd.ApplyPatches(repoDir, patches)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("outside repo"))
	})

	It("returns the list of modified file paths", func() {
		repoDir := GinkgoT().TempDir()
		patches := []adapter.FilePatch{
			{Path: "a.go", Content: "a"},
			{Path: "b.go", Content: "b"},
			{Path: "sub/c.go", Content: "c"},
		}

		files, err := tdd.ApplyPatches(repoDir, patches)
		Expect(err).NotTo(HaveOccurred())
		Expect(files).To(HaveLen(3))
	})
})
