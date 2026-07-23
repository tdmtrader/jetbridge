package gitcheck_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/agent/gitcheck"
)

var _ = Describe("gitcheck.Detect + FileDiff", func() {
	var tmp, bare, base string

	BeforeEach(func() {
		tmp = GinkgoT().TempDir()
		bare, base = setupOrigin(tmp)
	})

	openMirror := func() *gitcheck.Mirror {
		m, err := gitcheck.OpenMirror(filepath.Join(tmp, "cache"), "tdmtrader/concourse", bare, gitcheck.Auth{})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Fetch()).To(Succeed())
		return m
	}

	It("returns nil for an open (unmerged) branch", func() {
		ws := filepath.Join(tmp, "ws")
		git(tmp, botEnv, "clone", bare, ws)
		Expect(os.WriteFile(filepath.Join(ws, "f.go"), []byte("package f\n"), 0o644)).To(Succeed())
		git(ws, botEnv, "add", ".")
		git(ws, botEnv, "commit", "-m", "work")
		pushed := git(ws, botEnv, "rev-parse", "HEAD")
		git(ws, botEnv, "push", "origin", "HEAD:refs/heads/agent/ticket-1")

		m := openMirror()
		res, err := m.Detect(base, pushed, "agent/ticket-1", "main", 200)
		Expect(err).NotTo(HaveOccurred())
		Expect(res).To(BeNil())
	})

	It("returns merged (no human commits) for a fast-forward with only bot commits", func() {
		ws := filepath.Join(tmp, "ws")
		git(tmp, botEnv, "clone", bare, ws)
		Expect(os.WriteFile(filepath.Join(ws, "f.go"), []byte("package f\n"), 0o644)).To(Succeed())
		git(ws, botEnv, "add", ".")
		git(ws, botEnv, "commit", "-m", "work")
		pushed := git(ws, botEnv, "rev-parse", "HEAD")
		git(ws, botEnv, "push", "origin", "HEAD:refs/heads/agent/ticket-1")
		git(ws, botEnv, "push", "origin", "HEAD:main")

		m := openMirror()
		res, err := m.Detect(base, pushed, "agent/ticket-1", "main", 200)
		Expect(err).NotTo(HaveOccurred())
		Expect(res).NotTo(BeNil())
		Expect(res.State).To(Equal(gitcheck.StateMerged))
		Expect(res.HumanCommitCount).To(Equal(0))
	})

	It("returns merged_with_fixes when a human commit precedes the merge", func() {
		ws := filepath.Join(tmp, "ws")
		git(tmp, botEnv, "clone", bare, ws)
		Expect(os.WriteFile(filepath.Join(ws, "f.go"), []byte("package f\n"), 0o644)).To(Succeed())
		git(ws, botEnv, "add", ".")
		git(ws, botEnv, "commit", "-m", "work")
		pushed := git(ws, botEnv, "rev-parse", "HEAD")
		Expect(os.WriteFile(filepath.Join(ws, "f.go"), []byte("package f\nvar Fix = 1\n"), 0o644)).To(Succeed())
		git(ws, humanEnv, "add", ".")
		git(ws, humanEnv, "commit", "-m", "human fix")
		git(ws, humanEnv, "push", "origin", "HEAD:refs/heads/agent/ticket-1")
		git(ws, humanEnv, "push", "origin", "HEAD:main")

		m := openMirror()
		res, err := m.Detect(base, pushed, "agent/ticket-1", "main", 200)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.State).To(Equal(gitcheck.StateMergedWithFixes))
		Expect(res.HumanCommitCount).To(Equal(1))
		Expect(res.HumanLinesAdded).To(Equal(1))
	})

	It("windows the diff by file with a has_more flag", func() {
		ws := filepath.Join(tmp, "ws")
		git(tmp, botEnv, "clone", bare, ws)
		for _, f := range []string{"a.go", "b.go", "c.go"} {
			Expect(os.WriteFile(filepath.Join(ws, f), []byte("package x\n"), 0o644)).To(Succeed())
		}
		git(ws, botEnv, "add", ".")
		git(ws, botEnv, "commit", "-m", "three files")
		pushed := git(ws, botEnv, "rev-parse", "HEAD")
		git(ws, botEnv, "push", "origin", "HEAD:refs/heads/agent/ticket-1")

		m := openMirror()
		page, err := m.FileDiff(base, pushed, 0, 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(page.Files).To(HaveLen(2))
		Expect(page.HasMore).To(BeTrue())
		Expect(page.TotalFiles).To(Equal(3))

		page2, err := m.FileDiff(base, pushed, 2, 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(page2.Files).To(HaveLen(1))
		Expect(page2.HasMore).To(BeFalse())
	})

	It("derives offline changed-file statistics for binary, rename, and delete changes", func() {
		ws := filepath.Join(tmp, "projection")
		git(tmp, botEnv, "clone", bare, ws)
		Expect(os.WriteFile(filepath.Join(ws, "rename-me.txt"), []byte("rename me\n"), 0o644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(ws, "delete-me.txt"), []byte("delete me\n"), 0o644)).To(Succeed())
		git(ws, botEnv, "add", ".")
		git(ws, botEnv, "commit", "-m", "projection base")
		projectionBase := git(ws, botEnv, "rev-parse", "HEAD")

		Expect(os.WriteFile(filepath.Join(ws, "base.txt"), []byte("base\nmodified\n"), 0o644)).To(Succeed())
		git(ws, botEnv, "mv", "rename-me.txt", "renamed.txt")
		Expect(os.Remove(filepath.Join(ws, "delete-me.txt"))).To(Succeed())
		Expect(os.WriteFile(filepath.Join(ws, "binary.dat"), []byte{0, 1, 2, 3}, 0o644)).To(Succeed())
		git(ws, botEnv, "add", "-A")
		git(ws, botEnv, "commit", "-m", "mixed change")
		result := git(ws, botEnv, "rev-parse", "HEAD")

		diff, err := gitcheck.DeriveRepositoryDiff(context.Background(), ws, projectionBase, result)
		Expect(err).NotTo(HaveOccurred())
		Expect(diff.Files).To(HaveLen(4))
		Expect(diff.FileCount).To(Equal(4))
		Expect(diff.LinesAdded).To(Equal(1))
		Expect(diff.LinesDeleted).To(Equal(1))
		Expect(diff.Truncated).To(BeFalse())
		Expect(diff.UnifiedDiff).To(ContainSubstring("diff --git"))

		byPath := map[string]gitcheck.ChangedFile{}
		for _, file := range diff.Files {
			byPath[file.Path] = file
		}
		Expect(byPath["base.txt"].Status).To(Equal(gitcheck.ChangeModified))
		Expect(byPath["binary.dat"].Binary).To(BeTrue())
		Expect(byPath["delete-me.txt"].Status).To(Equal(gitcheck.ChangeDeleted))
		Expect(byPath["renamed.txt"].Status).To(Equal(gitcheck.ChangeRenamed))
		Expect(byPath["renamed.txt"].PreviousPath).To(Equal("rename-me.txt"))
	})

	It("bounds the complete persisted unified diff to 64 KiB", func() {
		ws := filepath.Join(tmp, "bounded")
		git(tmp, botEnv, "clone", bare, ws)
		large := strings.Repeat("a very long changed line for bounded projection\n", 5000)
		Expect(os.WriteFile(filepath.Join(ws, "large.txt"), []byte(large), 0o644)).To(Succeed())
		git(ws, botEnv, "add", ".")
		git(ws, botEnv, "commit", "-m", "large")
		result := git(ws, botEnv, "rev-parse", "HEAD")

		diff, err := gitcheck.DeriveRepositoryDiff(context.Background(), ws, base, result)
		Expect(err).NotTo(HaveOccurred())
		Expect(len(diff.UnifiedDiff)).To(BeNumerically("<=", gitcheck.BoundedUnifiedDiffBytes))
		Expect(diff.Truncated).To(BeTrue())
		Expect(diff.TruncationReason).ToNot(BeEmpty())
		Expect(diff.Files).To(HaveLen(1))
		Expect(diff.Files[0].Truncated).To(BeTrue())
	})

	It("rejects a base object absent from the local immutable repository", func() {
		ws := filepath.Join(tmp, "invalid-base")
		git(tmp, botEnv, "clone", bare, ws)
		_, err := gitcheck.DeriveRepositoryDiff(context.Background(), ws, strings.Repeat("0", 40), base)
		Expect(err).To(HaveOccurred())
	})
})
