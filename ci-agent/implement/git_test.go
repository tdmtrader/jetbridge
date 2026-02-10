package implement_test

import (
	"os"
	"os/exec"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/implement"
)

var _ = Describe("Git Operations", func() {
	var repoDir string

	BeforeEach(func() {
		repoDir = GinkgoT().TempDir()
		runGit(repoDir, "init")
		runGit(repoDir, "config", "user.email", "test@test.com")
		runGit(repoDir, "config", "user.name", "Test")
		// Create initial commit.
		os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("init"), 0644)
		runGit(repoDir, "add", ".")
		runGit(repoDir, "commit", "-m", "initial commit")
	})

	Describe("StageFiles", func() {
		It("stages specific files", func() {
			os.WriteFile(filepath.Join(repoDir, "a.go"), []byte("package a"), 0644)
			os.WriteFile(filepath.Join(repoDir, "b.go"), []byte("package b"), 0644)

			err := implement.StageFiles(repoDir, []string{"a.go", "b.go"})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Describe("Commit", func() {
		It("creates a commit and returns SHA", func() {
			os.WriteFile(filepath.Join(repoDir, "file.go"), []byte("package x"), 0644)
			runGit(repoDir, "add", "file.go")

			sha, err := implement.Commit(repoDir, "feat: add file")
			Expect(err).NotTo(HaveOccurred())
			Expect(sha).To(HaveLen(40))
		})
	})

	Describe("RevertLast", func() {
		It("reverts the last commit", func() {
			os.WriteFile(filepath.Join(repoDir, "file.go"), []byte("content"), 0644)
			runGit(repoDir, "add", "file.go")
			runGit(repoDir, "commit", "-m", "add file")

			err := implement.RevertLast(repoDir)
			Expect(err).NotTo(HaveOccurred())

			// File should still exist but changes uncommitted.
			_, err = os.Stat(filepath.Join(repoDir, "file.go"))
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Describe("CurrentSHA", func() {
		It("returns HEAD SHA", func() {
			sha, err := implement.CurrentSHA(repoDir)
			Expect(err).NotTo(HaveOccurred())
			Expect(sha).To(HaveLen(40))
		})
	})

	Describe("CreateBranch", func() {
		It("creates and checks out a new branch", func() {
			err := implement.CreateBranch(repoDir, "test-branch")
			Expect(err).NotTo(HaveOccurred())

			cmd := exec.Command("git", "branch", "--show-current")
			cmd.Dir = repoDir
			out, err := cmd.Output()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(out)).To(ContainSubstring("test-branch"))
		})
	})
})

func runGit(dir string, args ...string) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Run()
}
