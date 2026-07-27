package repodiff_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/agent/repodiff"
)

func TestGitcheck(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Gitcheck Suite")
}

// git runs a git command in dir with the given committer env, failing on error.
func git(dir string, env []string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

var botEnv = []string{
	"GIT_AUTHOR_NAME=concourse-agent[bot]", "GIT_AUTHOR_EMAIL=agent@concourse.local",
	"GIT_COMMITTER_NAME=concourse-agent[bot]", "GIT_COMMITTER_EMAIL=agent@concourse.local",
}

// setupOrigin builds a bare origin with main at one base commit and returns
// (bareDir, baseSha).
func setupOrigin(tmp string) (string, string) {
	bare := filepath.Join(tmp, "origin.git")
	Expect(os.MkdirAll(bare, 0o755)).To(Succeed())
	git(bare, nil, "init", "--bare", "--initial-branch=main")
	seed := filepath.Join(tmp, "seed")
	git(tmp, botEnv, "clone", bare, seed)
	Expect(os.WriteFile(filepath.Join(seed, "base.txt"), []byte("base\n"), 0o644)).To(Succeed())
	git(seed, botEnv, "add", ".")
	git(seed, botEnv, "commit", "-m", "base")
	git(seed, botEnv, "push", "origin", "HEAD:main")
	base := git(seed, botEnv, "rev-parse", "HEAD")
	return bare, base
}

var _ = Describe("repodiff.DeriveRepositoryDiff", func() {
	var tmp, bare, base string

	BeforeEach(func() {
		tmp = GinkgoT().TempDir()
		bare, base = setupOrigin(tmp)
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

		diff, err := repodiff.DeriveRepositoryDiff(context.Background(), ws, projectionBase, result)
		Expect(err).NotTo(HaveOccurred())
		Expect(diff.Files).To(HaveLen(4))
		Expect(diff.FileCount).To(Equal(4))
		Expect(diff.LinesAdded).To(Equal(1))
		Expect(diff.LinesDeleted).To(Equal(1))
		Expect(diff.Truncated).To(BeFalse())
		Expect(diff.UnifiedDiff).To(ContainSubstring("diff --git"))

		byPath := map[string]repodiff.ChangedFile{}
		for _, file := range diff.Files {
			byPath[file.Path] = file
		}
		Expect(byPath["base.txt"].Status).To(Equal(repodiff.ChangeModified))
		Expect(byPath["binary.dat"].Binary).To(BeTrue())
		Expect(byPath["delete-me.txt"].Status).To(Equal(repodiff.ChangeDeleted))
		Expect(byPath["renamed.txt"].Status).To(Equal(repodiff.ChangeRenamed))
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

		diff, err := repodiff.DeriveRepositoryDiff(context.Background(), ws, base, result)
		Expect(err).NotTo(HaveOccurred())
		Expect(len(diff.UnifiedDiff)).To(BeNumerically("<=", repodiff.BoundedUnifiedDiffBytes))
		Expect(diff.Truncated).To(BeTrue())
		Expect(diff.TruncationReason).ToNot(BeEmpty())
		Expect(diff.Files).To(HaveLen(1))
		Expect(diff.Files[0].Truncated).To(BeTrue())
	})

	It("rejects a base object absent from the local immutable repository", func() {
		ws := filepath.Join(tmp, "invalid-base")
		git(tmp, botEnv, "clone", bare, ws)
		_, err := repodiff.DeriveRepositoryDiff(context.Background(), ws, strings.Repeat("0", 40), base)
		Expect(err).To(HaveOccurred())
	})
})
