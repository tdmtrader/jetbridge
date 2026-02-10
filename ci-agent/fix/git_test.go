package fix_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/fix"
)

var _ = Describe("Git Operations", func() {
	var repoDir string

	BeforeEach(func() {
		var err error
		repoDir, err = os.MkdirTemp("", "fix-git-*")
		Expect(err).NotTo(HaveOccurred())

		// Initialize a git repo with an initial commit.
		run(repoDir, "git", "init")
		run(repoDir, "git", "config", "user.email", "test@test.com")
		run(repoDir, "git", "config", "user.name", "Test")
		Expect(os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# test"), 0644)).To(Succeed())
		run(repoDir, "git", "add", ".")
		run(repoDir, "git", "commit", "-m", "initial commit")
	})

	AfterEach(func() {
		os.RemoveAll(repoDir)
	})

	Describe("CreateBranch", func() {
		It("creates and checks out a new branch", func() {
			err := fix.CreateBranch(repoDir, "fix/test-branch")
			Expect(err).NotTo(HaveOccurred())

			branch := getCurrentBranch(repoDir)
			Expect(branch).To(Equal("fix/test-branch"))
		})
	})

	Describe("CommitFiles", func() {
		It("commits specific files only", func() {
			Expect(os.WriteFile(filepath.Join(repoDir, "a.go"), []byte("package a"), 0644)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(repoDir, "b.go"), []byte("package b"), 0644)).To(Succeed())

			sha, err := fix.CommitFiles(repoDir, []string{"a.go"}, "fix(a): fix issue ISS-001")
			Expect(err).NotTo(HaveOccurred())
			Expect(sha).NotTo(BeEmpty())

			// b.go should not be committed.
			out := gitOutput(repoDir, "status", "--porcelain")
			Expect(out).To(ContainSubstring("b.go"))
		})

		It("includes commit message", func() {
			Expect(os.WriteFile(filepath.Join(repoDir, "a.go"), []byte("package a"), 0644)).To(Succeed())

			_, err := fix.CommitFiles(repoDir, []string{"a.go"}, "fix(a): resolve nil pointer ISS-001")
			Expect(err).NotTo(HaveOccurred())

			log := gitOutput(repoDir, "log", "--oneline", "-1")
			Expect(log).To(ContainSubstring("ISS-001"))
		})
	})

	Describe("RevertLastCommit", func() {
		It("reverts the last commit", func() {
			Expect(os.WriteFile(filepath.Join(repoDir, "a.go"), []byte("package a"), 0644)).To(Succeed())
			run(repoDir, "git", "add", "a.go")
			run(repoDir, "git", "commit", "-m", "add a.go")

			err := fix.RevertLastCommit(repoDir)
			Expect(err).NotTo(HaveOccurred())

			// a.go should no longer exist.
			_, err = os.Stat(filepath.Join(repoDir, "a.go"))
			Expect(os.IsNotExist(err)).To(BeTrue())
		})
	})

	Describe("GetHeadSHA", func() {
		It("returns current HEAD SHA", func() {
			sha, err := fix.GetHeadSHA(repoDir)
			Expect(err).NotTo(HaveOccurred())
			Expect(sha).To(HaveLen(40))
		})
	})
})

func run(dir, name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "command %q failed: %s", name+" "+args[0], string(out))
}

func getCurrentBranch(dir string) string {
	return gitOutput(dir, "rev-parse", "--abbrev-ref", "HEAD")
}

func gitOutput(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	Expect(err).NotTo(HaveOccurred())
	return strings.TrimSpace(string(out))
}
