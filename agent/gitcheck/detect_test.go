package gitcheck_test

import (
	"os"
	"path/filepath"

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
})
