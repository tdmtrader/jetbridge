package git_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/adapters/git"
	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
)

const lease = "refs/queue/lease"

func run(dir string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), string(out))
	return strings.TrimSpace(string(out))
}

// remote is a local bare repo and a work repo that makes commits for it.
type remote struct{ bare, work, base string }

func newRemote() *remote {
	dir := GinkgoT().TempDir()
	r := &remote{bare: filepath.Join(dir, "remote.git"), work: filepath.Join(dir, "work")}
	run(dir, "init", "-q", "--bare", r.bare)
	run(dir, "init", "-q", r.work)
	r.base = r.commit("base", "")
	r.setMain(r.base)
	return r
}

// commit makes a commit named name on parent and pushes it as a branch.
func (r *remote) commit(name, parent string) string {
	args := []string{"commit-tree", run(r.work, "mktree"), "-m", name}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	sha := run(r.work, args...)
	run(r.work, "push", "-q", r.bare, sha+":refs/heads/"+name)
	return sha
}

func (r *remote) setMain(sha string) { run(r.work, "push", "-q", "-f", r.bare, sha+":refs/heads/main") }
func (r *remote) main() string       { return run(r.bare, "rev-parse", "refs/heads/main") }
func (r *remote) fence() string      { return run(r.bare, "log", "-1", "--format=%B", lease) }

func (r *remote) config() config.Config {
	return config.Config{Repository: config.Repository{URI: r.bare}, Lander: config.Lander{LeaseRef: lease}}
}

func (r *remote) lander() *git.Lander {
	l, err := git.New(r.config())
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(l.Close)
	return l
}

var _ = Describe("Lander", func() {
	ctx := context.Background()
	var r *remote
	BeforeEach(func() { r = newRemote() })

	It("A candidate ahead of main lands and moves the lease", func() {
		c1 := r.commit("c1", r.base)
		Expect(r.lander().Land(ctx, "main", c1, 5)).To(Succeed())
		Expect(r.main()).To(Equal(c1))
		Expect(r.fence()).To(Equal("fence 5"))
	})

	It("A candidate that is not ahead of main is refused", func() {
		c1, c2 := r.commit("c1", r.base), r.commit("c2", r.base)
		r.setMain(c1)
		err := r.lander().Land(ctx, "main", c2, 5)
		Expect(err).To(MatchError(ContainSubstring("not ahead of main")))
		var moved *core.MainMovedError
		Expect(errors.As(err, &moved)).To(BeTrue(), "named as main moved so the driver recomposes")
		Expect(r.main()).To(Equal(c1))
	})

	It("A landing with an older fence is refused and main does not move", func() {
		l, c1 := r.lander(), r.commit("c1", r.base)
		Expect(l.Land(ctx, "main", c1, 7)).To(Succeed())
		c2 := r.commit("c2", c1)
		Expect(l.Land(ctx, "main", c2, 6)).To(MatchError(ContainSubstring("fence is 7")))
		Expect(r.main()).To(Equal(c1))
	})

	It("A landing still in flight with an old fence cannot land after a newer check fenced it", func() {
		a, b, c1 := r.lander(), r.lander(), r.commit("c1", r.base)
		Expect(b.Contains(ctx, "main", c1, 9)).To(BeFalse())
		Expect(a.Land(ctx, "main", c1, 8)).To(MatchError(ContainSubstring("fence is 9")))
		Expect(r.main()).To(Equal(r.base))
	})

	It("Main moving under the lander refuses the push and the lease does not move", func() {
		l := r.lander()
		Expect(l.Contains(ctx, "main", r.base, 1)).To(BeTrue())
		c1, other := r.commit("c1", r.base), r.commit("other", r.base)
		git.SetBeforePush(l, func() { r.setMain(other) })
		Expect(l.Land(ctx, "main", c1, 5)).To(HaveOccurred())
		Expect(r.main()).To(Equal(other))
		Expect(r.fence()).To(Equal("fence 1"))
	})

	It("Contains reports a landed candidate", func() {
		l, c1, c2 := r.lander(), r.commit("c1", r.base), r.commit("c2", r.base)
		Expect(l.Land(ctx, "main", c1, 5)).To(Succeed())
		Expect(l.Contains(ctx, "main", c1, 6)).To(BeTrue())
		Expect(r.lander().Contains(ctx, "main", c2, 7)).To(BeFalse())
	})

	It("A delayed landing is refused when a newer check fenced it, and main stays", func() {
		a, b, c1 := r.lander(), r.lander(), r.commit("c1", r.base)
		Expect(b.Contains(ctx, "main", r.base, 7)).To(BeTrue())
		git.SetBeforePush(a, func() { Expect(b.Contains(ctx, "main", c1, 9)).To(BeFalse()) })
		Expect(a.Land(ctx, "main", c1, 8)).To(HaveOccurred())
		Expect(r.main()).To(Equal(r.base))
	})

	DescribeTable("Contains reports a git failure, never a false answer",
		func(failing string) {
			l, c1 := r.lander(), r.commit("c1", r.base)
			Expect(l.Land(ctx, "main", c1, 5)).To(Succeed())
			real, err := exec.LookPath("git")
			Expect(err).NotTo(HaveOccurred())
			bin := GinkgoT().TempDir()
			script := "#!/bin/sh\ncase \" $* \" in *\" " + failing + " \"*) echo broken >&2; exit 128;; esac\nexec " + real + " \"$@\"\n"
			Expect(os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755)).To(Succeed())
			GinkgoT().Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			_, err = l.Contains(ctx, "main", c1, 6)
			Expect(err).To(MatchError(ContainSubstring("broken")))
		},
		Entry("cat-file", "cat-file"),
		Entry("merge-base", "merge-base"),
	)

	It("Contains reports a cancelled context as an error", func() {
		l, c1 := r.lander(), r.commit("c1", r.base)
		Expect(l.Land(ctx, "main", c1, 5)).To(Succeed())
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := l.Contains(cctx, "main", c1, 6)
		Expect(err).To(HaveOccurred())
	})

	It("a malformed lease is refused, never read as zero", func() {
		l, c1 := r.lander(), r.commit("c1", r.base)
		for _, message := range []string{"fence 0x100000009", "fence 5 and more", "fence +5", "fence 18446744073709551616", "hello"} {
			obj := run(r.work, "commit-tree", run(r.work, "mktree"), "-m", message)
			run(r.work, "push", "-q", "-f", r.bare, obj+":"+lease)
			Expect(l.Land(ctx, "main", c1, 1)).To(MatchError(ContainSubstring("does not hold a fence")), message)
			Expect(r.main()).To(Equal(r.base))
			_, err := l.Contains(ctx, "main", c1, 1)
			Expect(err).To(MatchError(ContainSubstring("does not hold a fence")), message)
		}
	})

	It("The private repo is at an absolute path, so Close cannot remove another directory", func() {
		here, err := os.Getwd()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(os.Chdir, here)
		first, other := GinkgoT().TempDir(), GinkgoT().TempDir()
		Expect(os.Chdir(first)).To(Succeed())
		Expect(os.Mkdir("scratch", 0o755)).To(Succeed())
		cfg := r.config()
		cfg.Lander.Scratch = "scratch"
		l, err := git.New(cfg)
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.IsAbs(git.Dir(l))).To(BeTrue())
		innocent := filepath.Join(other, "scratch", filepath.Base(git.Dir(l)))
		Expect(os.MkdirAll(innocent, 0o755)).To(Succeed())
		Expect(os.Chdir(other)).To(Succeed())
		Expect(l.Close()).To(Succeed())
		Expect(innocent).To(BeADirectory())
		Expect(git.Dir(l)).NotTo(BeADirectory())
	})
})

var _ = Describe("Git errors", func() {
	ctx := context.Background()
	// reject makes the remote refuse every push, saying where it was told to push as a URL with a password.
	reject := func(r *remote) {
		hook := "#!/bin/sh\necho 'denied for https://user:SECRET@host/repo' >&2\nexit 1\n"
		Expect(os.WriteFile(filepath.Join(r.bare, "hooks", "pre-receive"), []byte(hook), 0o755)).To(Succeed())
	}

	It("A credential in a git error is hidden", func() {
		r := newRemote()
		c1 := r.commit("c1", r.base)
		reject(r)
		err := r.lander().Land(ctx, "main", c1, 5)
		Expect(err).To(MatchError(ContainSubstring("denied for https://***@host/repo")))
		Expect(err.Error()).NotTo(ContainSubstring("SECRET"))
	})

	It("A credential in the lease ref's message is hidden in the fence error", func() {
		r := newRemote()
		c1 := r.commit("c1", r.base)
		bad := run(r.work, "commit-tree", run(r.work, "mktree"), "-m", "fence https://user:SECRET@host/repo")
		run(r.work, "push", "-q", "-f", r.bare, bad+":"+lease)
		err := r.lander().Land(ctx, "main", c1, 5)
		Expect(err).To(MatchError(ContainSubstring("does not hold a fence")))
		Expect(err.Error()).NotTo(ContainSubstring("SECRET"))
	})

	It("hides a credential in the error of a rejected save", func() {
		r := newRemote()
		c := config.Defaults()
		c.Repository.URI = r.bare
		store := git.NewStore(c)
		l, err := store.Acquire(ctx, "runner", time.Minute)
		Expect(err).NotTo(HaveOccurred())
		snap, err := store.Load(ctx)
		Expect(err).NotTo(HaveOccurred())
		reject(r)
		snap.Paused = true
		_, err = store.Save(ctx, l.Token, snap)
		Expect(err).To(MatchError(ContainSubstring("denied for https://***@host/repo")))
		Expect(err.Error()).NotTo(ContainSubstring("SECRET"))
	})
})

var _ = Describe("ssh isolation", func() {
	const uri = "ssh://example.invalid/repo.git"
	var dir, log string
	// probe is a GIT_SSH_COMMAND whose program logs the config ssh reads from its
	// arguments, then fails the connection; opts come first, as a user's would.
	probe := func(opts string) string {
		fake := filepath.Join(dir, "ssh.sh")
		Expect(os.WriteFile(fake, []byte("#!/bin/sh\nssh -G \"$@\" >> "+log+" 2>&1\nexit 255\n"), 0o755)).To(Succeed())
		return fake + opts
	}
	const userMux = " -o ControlMaster=auto -o ControlPath=/nonexistent/user-%C"
	// noMux: ssh ran n times, read its options, and ended with no control path.
	noMux := func(n int) {
		b, err := os.ReadFile(log)
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.Count(string(b), "hostname example.invalid")).To(Equal(n), string(b))
		Expect(string(b)).NotTo(ContainSubstring("controlpath"))
	}
	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		log = filepath.Join(dir, "log")
		GinkgoT().Setenv("GIT_SSH", "")
		GinkgoT().Setenv("GIT_SSH_VARIANT", "ssh")
	})
	lander := func() *git.Lander {
		c := newRemote().config()
		c.Repository.URI = uri
		l, err := git.New(c)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(l.Close)
		return l
	}

	It("Two git calls at once do not share a connection", func() {
		GinkgoT().Setenv("GIT_SSH_COMMAND", probe(""))
		l := lander()
		done := make(chan struct{}, 2)
		for range 2 {
			go func() {
				defer GinkgoRecover()
				_, _ = git.Git(l, context.Background(), "ls-remote", uri)
				done <- struct{}{}
			}()
		}
		<-done
		<-done
		noMux(2)
	})

	It("turns off multiplexing a user's ssh command turns on", func() {
		GinkgoT().Setenv("GIT_SSH_COMMAND", probe(userMux))
		_, _ = git.Git(lander(), context.Background(), "ls-remote", uri)
		noMux(1)
	})

	It("works with a temp dir whose path has a space", func() {
		GinkgoT().Setenv("GIT_SSH_COMMAND", probe(""))
		l := lander()
		spaced := filepath.Join(dir, "a b")
		Expect(os.Mkdir(spaced, 0o700)).To(Succeed())
		GinkgoT().Setenv("TMPDIR", spaced)
		_, _ = git.Git(l, context.Background(), "ls-remote", uri)
		noMux(1)
	})

	It("keeps a clone-local core.sshCommand as the ssh command", func() {
		r := newRemote()
		run(r.work, "config", "core.sshCommand", probe(userMux))
		GinkgoT().Setenv("GIT_SSH_COMMAND", "")
		Expect(os.Unsetenv("GIT_SSH_COMMAND")).To(Succeed())
		c := r.config()
		c.Repository.URI = uri
		Expect(git.Admit(context.Background(), c, r.work, "a", r.base)).NotTo(Succeed())
		noMux(1)
	})

	It("isolates the composer's fetch over ssh too", func() {
		GinkgoT().Setenv("GIT_SSH_COMMAND", probe(userMux))
		_, err := git.Composer{Remote: uri, Main: "main", Candidate: "next"}.Compose(context.Background(), "main", nil)
		Expect(err).To(HaveOccurred())
		noMux(1)
	})
})
