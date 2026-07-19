package gitcheck_test

import (
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/agent/gitcheck"
)

func TestGitcheck(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Gitcheck Suite")
}

// RandString isolates clone dirs across specs.
func RandString() string {
	return strconv.FormatInt(GinkgoRandomSeed(), 10) + strconv.Itoa(rand.Int())
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
var humanEnv = []string{
	"GIT_AUTHOR_NAME=Alice", "GIT_AUTHOR_EMAIL=alice@example.com",
	"GIT_COMMITTER_NAME=Alice", "GIT_COMMITTER_EMAIL=alice@example.com",
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

var _ = Describe("gitcheck.Mirror", func() {
	var tmp, bare, base string

	BeforeEach(func() {
		tmp = GinkgoT().TempDir()
		bare, base = setupOrigin(tmp)
	})

	// clone builds a working clone of the origin.
	clone := func() string {
		ws := filepath.Join(tmp, "ws"+RandString())
		git(tmp, botEnv, "clone", bare, ws)
		return ws
	}

	It("clones a mirror and detects a fast-forward merge (branch head is ancestor of main)", func() {
		ws := clone()
		Expect(os.WriteFile(filepath.Join(ws, "f.go"), []byte("package f\n"), 0o644)).To(Succeed())
		git(ws, botEnv, "add", ".")
		git(ws, botEnv, "commit", "-m", "agent work")
		pushed := git(ws, botEnv, "rev-parse", "HEAD")
		git(ws, botEnv, "push", "origin", "HEAD:refs/heads/agent/ticket-1")
		// fast-forward main to the agent branch
		git(ws, botEnv, "push", "origin", "HEAD:main")

		m, err := gitcheck.OpenMirror(filepath.Join(tmp, "cache"), "tdmtrader/concourse", bare, gitcheck.Auth{})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Fetch()).To(Succeed())

		anc, err := m.IsAncestor(pushed, "main")
		Expect(err).NotTo(HaveOccurred())
		Expect(anc).To(BeTrue())

		mp, err := m.MergePoint(pushed, "main")
		Expect(err).NotTo(HaveOccurred())
		Expect(mp.Merged).To(BeTrue())
		Expect(mp.TipAtMerge).To(Equal(pushed)) // fast-forward: tip == pushed

		// BranchHead: present ref resolves to a sha; absent ref is "" (not an error)
		Expect(m.BranchHead("agent/ticket-1")).To(Equal(pushed))
		Expect(m.BranchHead("no/such/branch")).To(BeEmpty())
	})

	It("computes the human-touch delta excluding bot commits, first-parent", func() {
		ws := clone()
		Expect(os.WriteFile(filepath.Join(ws, "f.go"), []byte("package f\n"), 0o644)).To(Succeed())
		git(ws, botEnv, "add", ".")
		git(ws, botEnv, "commit", "-m", "agent work")
		pushed := git(ws, botEnv, "rev-parse", "HEAD")
		// a human amends the branch: +3 lines / -0
		Expect(os.WriteFile(filepath.Join(ws, "f.go"), []byte("package f\n\n// fix\nvar X = 1\n"), 0o644)).To(Succeed())
		git(ws, humanEnv, "add", ".")
		git(ws, humanEnv, "commit", "-m", "human fix")
		tip := git(ws, humanEnv, "rev-parse", "HEAD")
		git(ws, humanEnv, "push", "origin", "HEAD:refs/heads/agent/ticket-1")

		m, err := gitcheck.OpenMirror(filepath.Join(tmp, "cache"), "tdmtrader/concourse", bare, gitcheck.Auth{})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Fetch()).To(Succeed())

		delta, err := m.HumanDelta(pushed, tip)
		Expect(err).NotTo(HaveOccurred())
		Expect(delta.CommitCount).To(Equal(1))
		Expect(delta.LinesAdded).To(Equal(3)) // blank + comment + var line
		Expect(delta.LinesDeleted).To(Equal(0))
	})

	It("matches a squashed branch via patch-id", func() {
		ws := clone()
		Expect(os.WriteFile(filepath.Join(ws, "f.go"), []byte("package f\nvar A = 1\n"), 0o644)).To(Succeed())
		git(ws, botEnv, "add", ".")
		git(ws, botEnv, "commit", "-m", "c1")
		Expect(os.WriteFile(filepath.Join(ws, "g.go"), []byte("package g\n"), 0o644)).To(Succeed())
		git(ws, botEnv, "add", ".")
		git(ws, botEnv, "commit", "-m", "c2")
		branchTip := git(ws, botEnv, "rev-parse", "HEAD")
		git(ws, botEnv, "push", "origin", "HEAD:refs/heads/agent/ticket-1")

		// simulate a squash-merge onto main: one commit carrying the same net diff
		sq := clone()
		Expect(os.WriteFile(filepath.Join(sq, "f.go"), []byte("package f\nvar A = 1\n"), 0o644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(sq, "g.go"), []byte("package g\n"), 0o644)).To(Succeed())
		git(sq, humanEnv, "add", ".")
		git(sq, humanEnv, "commit", "-m", "squash ticket-1")
		squashSha := git(sq, humanEnv, "rev-parse", "HEAD")
		git(sq, humanEnv, "push", "origin", "HEAD:main")

		m, err := gitcheck.OpenMirror(filepath.Join(tmp, "cache"), "tdmtrader/concourse", bare, gitcheck.Auth{})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Fetch()).To(Succeed())

		anc, err := m.IsAncestor(branchTip, "main")
		Expect(err).NotTo(HaveOccurred())
		Expect(anc).To(BeFalse()) // squash: branch tip is NOT an ancestor

		match, err := m.PatchIDMatch(base, branchTip, "main", 200)
		Expect(err).NotTo(HaveOccurred())
		Expect(match.Found).To(BeTrue())
		Expect(match.Sha).To(Equal(squashSha))
	})
})
