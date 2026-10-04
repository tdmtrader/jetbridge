package git_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/adapters/git"
	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
)

// composeRun runs git in dir with a fixed identity and returns its trimmed output.
func composeRun(dir string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=dev", "GIT_AUTHOR_EMAIL=dev@example.test",
		"GIT_COMMITTER_NAME=dev", "GIT_COMMITTER_EMAIL=dev@example.test")
	out, err := cmd.CombinedOutput()
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), string(out))
	return strings.TrimSpace(string(out))
}

var _ = Describe("Composer", func() {
	var remote, work string
	var composer git.Composer
	ctx := context.Background()

	// change commits file=content on a branch named id off from, pushes it and returns the commit.
	change := func(id, from, file, content string) core.Entry {
		composeRun(work, "checkout", "-q", "-B", id, from)
		Expect(os.WriteFile(filepath.Join(work, file), []byte(content), 0o644)).To(Succeed())
		composeRun(work, "add", file)
		composeRun(work, "commit", "-q", "-m", "change "+id)
		composeRun(work, "push", "-q", "origin", id)
		return core.Entry{ID: id, Commit: composeRun(work, "rev-parse", "HEAD"), Ref: id}
	}
	ref := func(name string) string { return composeRun(remote, "rev-parse", name) }
	subjects := func(rng string) []string {
		return strings.Split(composeRun(remote, "log", "--reverse", "--format=%s", rng), "\n")
	}

	BeforeEach(func() {
		dir := GinkgoT().TempDir()
		remote, work = filepath.Join(dir, "remote.git"), filepath.Join(dir, "work")
		composeRun(dir, "init", "-q", "--bare", remote)
		composeRun(dir, "clone", "-q", remote, work)
		Expect(os.WriteFile(filepath.Join(work, "shared.txt"), []byte("one\n"), 0o644)).To(Succeed())
		composeRun(work, "add", ".")
		composeRun(work, "commit", "-q", "-m", "first")
		composeRun(work, "push", "-q", "origin", "HEAD:main")
		c, err := config.Parse([]byte("apiVersion: jetbridge.dev/queue/v2\nrepository: {uri: " + remote + ", main: main}\n"))
		Expect(err).NotTo(HaveOccurred())
		composer = git.NewComposer(c)
	})

	It("Two independent changes compose into one squashed commit each, on top of main, in order", func() {
		a, b := change("a", "main", "a.txt", "a\n"), change("b", "main", "b.txt", "b\n")
		sha, err := composer.Compose(ctx, "main", []core.Entry{a, b})
		Expect(err).NotTo(HaveOccurred())
		Expect(sha).To(MatchRegexp("^[0-9a-f]{40}$"))
		Expect(subjects("main.." + sha)).To(Equal([]string{"land(a): change a", "land(b): change b"}))
		Expect(ref(sha + "~2")).To(Equal(ref("main")))
		Expect(composeRun(remote, "rev-list", "--merges", "main.."+sha)).To(BeEmpty())
		Expect(composeRun(remote, "log", "-1", "--format=%B", sha+"~1")).To(Equal("land(a): change a\n\noriginal: " + a.Commit))
		Expect(composeRun(remote, "log", "-1", "--format=%B", sha)).To(Equal("land(b): change b\n\noriginal: " + b.Commit))
		Expect(composeRun(remote, "log", "-1", "--format=%an|%cn|%ce", sha)).To(Equal("dev|merge-queue|merge-queue@localhost"))
	})

	It("A change that conflicts with an earlier one in the batch is named as the conflict", func() {
		a, b := change("a", "main", "shared.txt", "from a\n"), change("b", "main", "shared.txt", "from b\n")
		batch := []core.Entry{a, b}
		_, err := composer.Compose(ctx, "main", batch)
		Expect(err).To(MatchError(core.ConflictError{EntryID: "b"}))
		Expect(core.ComposeVerdict(err, batch)).To(Equal(core.Fail))
	})

	It("A stacked child after its parent composes cleanly", func() {
		p := change("p", "main", "p.txt", "p\n")
		c := change("c", "p", "c.txt", "c\n")
		sha, err := composer.Compose(ctx, "main", []core.Entry{p, c})
		Expect(err).NotTo(HaveOccurred())
		Expect(subjects("main.." + sha)).To(Equal([]string{"land(p): change p", "land(c): change c"}))
		again, err := composer.Compose(ctx, sha, []core.Entry{c})
		Expect(err).NotTo(HaveOccurred())
		Expect(again).To(Equal(sha))
	})

	It("A stacked change that undoes its parent's change is composed faithfully", func() {
		p := change("p", "main", "x.txt", "x\n")
		composeRun(work, "checkout", "-q", "-B", "c", "p")
		composeRun(work, "rm", "-q", "x.txt")
		composeRun(work, "commit", "-q", "-m", "change c")
		composeRun(work, "push", "-q", "origin", "c")
		c := core.Entry{ID: "c", Commit: composeRun(work, "rev-parse", "HEAD"), Ref: "c"}
		sha, err := composer.Compose(ctx, "main", []core.Entry{p, c})
		Expect(err).NotTo(HaveOccurred())
		Expect(composeRun(remote, "ls-tree", "--name-only", sha)).To(Equal("shared.txt"))
		Expect(subjects("main.." + sha)).To(Equal([]string{"land(p): change p", "land(c): change c"}))
	})

	It("A stacked child that modifies a line its parent added composes to the child's version", func() {
		p := change("p", "main", "x.txt", "from p\n")
		c := change("c", "p", "x.txt", "from c\n")
		sha, err := composer.Compose(ctx, "main", []core.Entry{p, c})
		Expect(err).NotTo(HaveOccurred())
		Expect(composeRun(remote, "show", sha+":x.txt")).To(Equal("from c"))
		Expect(composeRun(remote, "show", sha+"~1:x.txt")).To(Equal("from p"))
	})

	// landSquash puts on main a squash of p, as the queue lands it: p's tree, parent main, naming p.
	landSquash := func(p core.Entry) {
		s := composeRun(work, "commit-tree", p.Commit+"^{tree}", "-p", ref("main"), "-m", "land(p): change p", "-m", "original: "+p.Commit)
		composeRun(work, "push", "-q", "origin", s+":refs/heads/main")
	}
	// child commits, on a branch off from, the removal of rm (if set) and file=content (if file is set).
	child := func(id, from, rm, file, content string) core.Entry {
		composeRun(work, "checkout", "-q", "-B", id, from)
		if rm != "" {
			composeRun(work, "rm", "-q", rm)
		}
		if file != "" {
			Expect(os.WriteFile(filepath.Join(work, file), []byte(content), 0o644)).To(Succeed())
			composeRun(work, "add", file)
		}
		composeRun(work, "commit", "-q", "-m", "change "+id)
		composeRun(work, "push", "-q", "origin", id)
		return core.Entry{ID: id, Commit: composeRun(work, "rev-parse", "HEAD"), Ref: id}
	}

	It("a change whose parent already landed applies only its own changes", func() {
		p := change("p", "main", "x.txt", "from p\n")
		landSquash(p)
		c := change("c", "p", "x.txt", "from c\n")
		sha, err := composer.Compose(ctx, "main", []core.Entry{c})
		Expect(err).NotTo(HaveOccurred())
		Expect(composeRun(remote, "show", sha+":x.txt")).To(Equal("from c"))
		Expect(subjects("main.." + sha)).To(Equal([]string{"land(c): change c"}))
	})

	It("a change whose parent already landed keeps its deletion alongside its addition", func() {
		p := change("p", "main", "x.txt", "x\n")
		landSquash(p)
		c := child("c", "p", "x.txt", "y.txt", "y\n")
		sha, err := composer.Compose(ctx, "main", []core.Entry{c})
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.Fields(composeRun(remote, "ls-tree", "--name-only", sha))).To(Equal([]string{"shared.txt", "y.txt"}))
	})

	It("a change whose parent already landed and that only deletes is not skipped", func() {
		p := change("p", "main", "x.txt", "x\n")
		landSquash(p)
		c := child("c", "p", "x.txt", "", "")
		sha, err := composer.Compose(ctx, "main", []core.Entry{c})
		Expect(err).NotTo(HaveOccurred())
		Expect(composeRun(remote, "ls-tree", "--name-only", sha)).To(Equal("shared.txt"))
		Expect(subjects("main.." + sha)).To(Equal([]string{"land(c): change c"}))
	})

	It("a change that builds on a merge of two queued changes is refused, not composed without its deletion", func() {
		a, b := change("a", "main", "a.txt", "a\n"), change("b", "main", "x.txt", "x\n")
		composeRun(work, "checkout", "-q", "-B", "m", "a")
		composeRun(work, "merge", "-q", "--no-ff", "-m", "merge", b.Commit)
		composeRun(work, "checkout", "-q", "-B", "c")
		composeRun(work, "rm", "-q", "x.txt")
		composeRun(work, "commit", "-q", "-m", "change c")
		composeRun(work, "push", "-q", "origin", "c")
		c := core.Entry{ID: "c", Commit: composeRun(work, "rev-parse", "HEAD"), Ref: "c"}
		_, err := composer.Compose(ctx, "main", []core.Entry{a, b, c})
		Expect(err).To(MatchError(core.ConflictError{EntryID: "c"}))
		Expect(err.Error()).To(ContainSubstring("squash needs one base: c builds on a merge of queued changes a, b"))
		Expect(composeRun(remote, "for-each-ref", "refs/heads/queue-next")).To(BeEmpty())
	})

	It("The candidate cannot be main, in config or at push time", func() {
		_, err := config.Parse([]byte("apiVersion: jetbridge.dev/queue/v2\nrepository: {uri: x, main: main, candidate: main}\n"))
		Expect(err).To(MatchError(`repository.candidate "main" must be set and differ from repository.main; the queue force-pushes it`))
		_, err = config.Parse([]byte("apiVersion: jetbridge.dev/queue/v2\nrepository: {uri: x, candidate: \"\"}\n"))
		Expect(err).To(MatchError(`repository.candidate "" must be set and differ from repository.main; the queue force-pushes it`))
		a := change("a", "main", "a.txt", "a\n")
		before := ref("main")
		_, err = git.Composer{Remote: remote, Main: "main", Candidate: "main"}.Compose(ctx, "main", []core.Entry{a})
		Expect(err).To(MatchError(`refusing to push the candidate to refs/heads/main, the main branch`))
		Expect(ref("main")).To(Equal(before))
	})

	It("The candidate branch is updated and main is untouched", func() {
		composeRun(work, "checkout", "-q", "--orphan", "stray")
		composeRun(work, "commit", "-q", "--allow-empty", "-m", "unrelated")
		composeRun(work, "push", "-q", "origin", "HEAD:queue-next")
		a := change("a", "main", "a.txt", "a\n")
		before := composeRun(remote, "for-each-ref", "--format=%(refname) %(objectname)", "--exclude=refs/heads/queue-next")
		sha, err := composer.Compose(ctx, "main", []core.Entry{a})
		Expect(err).NotTo(HaveOccurred())
		Expect(ref("refs/heads/queue-next")).To(Equal(sha))
		Expect(composeRun(remote, "for-each-ref", "--format=%(refname) %(objectname)", "--exclude=refs/heads/queue-next")).To(Equal(before))
	})

	It("A missing commit is an error, not a conflict", func() {
		batch := []core.Entry{{ID: "a", Commit: strings.Repeat("0", 39) + "1"}}
		_, err := composer.Compose(ctx, "main", batch)
		Expect(err).To(HaveOccurred())
		Expect(err).NotTo(BeAssignableToTypeOf(core.ConflictError{}))
		Expect(core.ComposeVerdict(err, batch)).To(Equal(core.None))
		Expect(composeRun(remote, "for-each-ref", "refs/heads/queue-next")).To(BeEmpty())
	})

	It("a mistyped committer key names the nearest", func() {
		base := "apiVersion: jetbridge.dev/queue/v2\nrepository: {uri: x}\n"
		_, err := config.Parse([]byte(base + "compose: {committer: {emial: a@b}}\n"))
		Expect(err).To(MatchError(`unknown key "compose.committer.emial"; did you mean "compose.committer.email"?`))
	})
})
