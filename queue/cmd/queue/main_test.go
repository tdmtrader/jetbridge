package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/adapters/git"
	"github.com/concourse/concourse/queue/adapters/lognotify"
	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
)

const sample = `apiVersion: jetbridge.dev/queue/v2
repository:
  uri: %s
  main: trunk
batch:
  max: 3
lander:
  max_failures: 5
  scratch: %s
runner:
  kind: jetbridge
  url: https://ci.example.invalid
  pipeline: p
  job: j
  resource: r
  credential: file:/path/to/auth.hdr
`

// idleStrategy plans nothing, so a step only drains admissions.
type idleStrategy struct{}

func (idleStrategy) Plan(core.View) ([]core.Run, []core.Settle) { return nil, nil }
func (idleStrategy) Record(core.View, string, core.Verdict) (core.Outcome, error) {
	return core.Outcome{}, nil
}

func gitIn(dir string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := cmd.CombinedOutput()
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), string(out))
	return strings.TrimSpace(string(out))
}

var _ = Describe("queue command", func() {
	var file, remote, sha string
	queue := func(args ...string) (code int, out, errw string) {
		var o, e bytes.Buffer
		code = run(context.Background(), args, &o, &e)
		return code, o.String(), e.String()
	}
	BeforeEach(func() {
		dir := GinkgoT().TempDir()
		remote = filepath.Join(dir, "remote.git")
		Expect(exec.Command("git", "init", "-q", "--bare", remote).Run()).To(Succeed())
		work := filepath.Join(dir, "work")
		gitIn(dir, "init", "-q", work)
		sha = gitIn(work, "commit-tree", gitIn(work, "mktree"), "-m", "a")
		wd, err := os.Getwd()
		Expect(err).NotTo(HaveOccurred())
		Expect(os.Chdir(work)).To(Succeed()) // queue admit pushes from the current repo
		DeferCleanup(os.Chdir, wd)
		file = filepath.Join(dir, "queue.yaml")
		Expect(os.WriteFile(file, fmt.Appendf(nil, sample, remote, dir), 0o600)).To(Succeed())
	})

	It("refuses a run interval that is not positive", func() {
		for _, every := range []string{"0", "-1s"} {
			code, _, errw := queue("run", "--config", file, "--every", every)
			Expect(code).To(Equal(1), every)
			Expect(errw).To(ContainSubstring("--every must be positive"))
		}
	})

	It("builds the driver from the config", func() {
		c, err := config.Parse(fmt.Appendf(nil, sample, "/r.git", GinkgoT().TempDir()))
		Expect(err).NotTo(HaveOccurred())
		d, closeFn, err := newDriver(c, io.Discard, io.Discard)
		Expect(err).NotTo(HaveOccurred())
		defer closeFn()
		d2, closeFn2, err := newDriver(c, io.Discard, io.Discard)
		Expect(err).NotTo(HaveOccurred())
		defer closeFn2()
		Expect(d.Main).To(Equal("trunk"))
		Expect(d.MaxFailures).To(Equal(5))
		Expect(d.Slots).To(Equal(1))
		Expect(d.Owner).NotTo(Equal(d2.Owner))
		Expect(d.NewStrategy()).To(Equal(&core.Serial{Max: 3, Policy: core.Policy{RetryNone: 1, Order: core.ProvenFirst}}))
		Expect(d.Notifier.Notify(context.Background(), core.Event{})).To(Succeed())
	})

	It("builds an adaptive strategy afresh on each call when configured", func() {
		c, err := config.Parse(fmt.Appendf(nil, sample+"", "/r.git", GinkgoT().TempDir()))
		Expect(err).NotTo(HaveOccurred())
		c.Batch.Adaptive = &config.Adaptive{Start: 2, Min: 1, GrowAfter: 2}
		d, closeFn, err := newDriver(c, io.Discard, io.Discard)
		Expect(err).NotTo(HaveOccurred())
		defer closeFn()
		s := d.NewStrategy()
		Expect(s).To(Equal(&core.Serial{Max: 3, Policy: core.Policy{RetryNone: 1, Order: core.ProvenFirst}, Adaptive: &core.Adaptive{Start: 2, Min: 1, GrowAfter: 2}}))
		Expect(d.NewStrategy()).NotTo(BeIdenticalTo(s))
	})

	It("reads batch.order into the strategy", func() {
		c, err := config.Parse(fmt.Appendf(nil, sample, "/r.git", GinkgoT().TempDir()))
		Expect(err).NotTo(HaveOccurred())
		c.Batch.Order = "strict"
		d, closeFn, err := newDriver(c, io.Discard, io.Discard)
		Expect(err).NotTo(HaveOccurred())
		defer closeFn()
		Expect(d.NewStrategy().(*core.Serial).Policy.Order).To(Equal(core.Strict))
	})

	It("A queue config with the log notifier builds its driver", func() {
		c, err := config.Parse(fmt.Appendf(nil, sample+"notify: {kind: log, path: \"-\"}\n", "/r.git", GinkgoT().TempDir()))
		Expect(err).NotTo(HaveOccurred())
		d, closeFn, err := newDriver(c, io.Discard, io.Discard)
		Expect(err).NotTo(HaveOccurred())
		defer closeFn()
		Expect(d.Notifier).To(BeAssignableToTypeOf(&lognotify.Notifier{}))
	})

	It("refuses a log notify section without a path", func() {
		c, err := config.Parse(fmt.Appendf(nil, sample+"notify: {kind: log}\n", "/r.git", GinkgoT().TempDir()))
		Expect(err).NotTo(HaveOccurred())
		_, _, err = newDriver(c, io.Discard, io.Discard)
		Expect(err).To(MatchError(ContainSubstring("notify.path is required")))
	})

	It("refuses a notify kind it cannot wire", func() {
		c, err := config.Parse(fmt.Appendf(nil, sample+"notify:\n  kind: carrier-pigeon\n", "/r.git", GinkgoT().TempDir()))
		Expect(err).NotTo(HaveOccurred())
		_, _, err = newDriver(c, io.Discard, io.Discard)
		Expect(err).To(MatchError(ContainSubstring("carrier-pigeon")))
	})

	It("An operator admits a change while the runner holds the lease and the runner queues it on its next step", func() {
		ctx := context.Background()
		c, err := config.Parse(fmt.Appendf(nil, sample, remote, GinkgoT().TempDir()))
		Expect(err).NotTo(HaveOccurred())
		store := git.NewStore(c)
		_, err = store.Acquire(ctx, "runner", time.Minute)
		Expect(err).NotTo(HaveOccurred())
		code, out, errw := queue("admit", "--config", file, "a", sha)
		Expect(errw).To(BeEmpty())
		Expect(code).To(Equal(0))
		Expect(out).To(Equal("admitted a; the runner will queue it\n"))
		Expect(gitIn(remote, "for-each-ref", "--format=%(objectname)", "refs/queue/admit/*.a")).To(Equal(sha))
		gitIn(remote, "update-ref", "refs/queue/admit/team/x", sha)
		type status struct {
			Queued  []struct{ ID, Commit string }
			Refused []struct{ ID, Commit, Why string }
		}
		read := func() (s status) {
			code, out, _ := queue("status", "--config", file)
			Expect(code).To(Equal(0))
			Expect(json.Unmarshal([]byte(out), &s)).To(Succeed())
			Expect(out).NotTo(ContainSubstring("auth.hdr"))
			return s
		}
		Expect(read().Queued).To(BeEmpty())

		d, closeFn, err := newDriver(c, io.Discard, io.Discard)
		Expect(err).NotTo(HaveOccurred())
		defer closeFn()
		d.Owner, d.NewStrategy = "runner", func() core.Strategy { return idleStrategy{} }
		Expect(d.Step(ctx)).To(Succeed())
		s := read()
		Expect(s.Queued).To(ConsistOf(struct{ ID, Commit string }{"a", sha}))
		Expect(s.Refused).To(HaveLen(1))
		Expect(s.Refused[0].ID).To(Equal("team/x"))
		Expect(gitIn(remote, "for-each-ref", "refs/queue/admit/")).To(BeEmpty())
		l, err := store.Acquire(ctx, "runner", time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(l.Token).To(Equal(uint64(1)), "admitting never took the runner's lease")
	})

	It("A dead JetBridge, after its one auto-resume, turns health red at once", func() {
		ctx := context.Background()
		gitIn(".", "push", "-q", remote, sha+":refs/heads/trunk")
		child := gitIn(".", "commit-tree", gitIn(".", "mktree"), "-p", sha, "-m", "child")
		code, _, errw := queue("admit", "--config", file, "a", child)
		Expect(code).To(Equal(0), errw)
		c, err := config.Parse(fmt.Appendf(nil, sample, remote, GinkgoT().TempDir()))
		Expect(err).NotTo(HaveOccurred())
		d, closeFn, err := newDriver(c, io.Discard, io.Discard)
		Expect(err).NotTo(HaveOccurred())
		defer closeFn()
		clock := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
		d.Owner, d.Now = "runner", func() time.Time { return clock }
		store := git.NewStore(c)
		head := func(context.Context) (string, error) { return sha, nil }
		pauses := 0
		for i := 0; i < 20 && pauses < 2; i++ {
			Expect(d.Step(ctx)).To(Succeed())
			snap, err := store.Load(ctx)
			Expect(err).NotTo(HaveOccurred())
			if snap.Paused && int(snap.PauseSeq) > pauses {
				pauses = int(snap.PauseSeq)
				if pauses == 1 {
					clock = clock.Add(c.Pause.Cooldown) // the one auto-resume
				}
			}
			clock = clock.Add(time.Second)
		}
		Expect(pauses).To(Equal(2), "paused, auto-resumed once, paused again")
		var out bytes.Buffer
		Expect(health(c, store.Load, head, clock, &out)).To(Equal(3), out.String())
		Expect(out.String()).To(ContainSubstring("nothing auto-resumes it; run `queue resume`"))
	})

	It("queue resume asks the live runner to resume", func() {
		ctx := context.Background()
		gitIn(".", "push", "-q", remote, sha+":refs/heads/trunk")
		c, err := config.Parse(fmt.Appendf(nil, sample, remote, GinkgoT().TempDir()))
		Expect(err).NotTo(HaveOccurred())
		store := git.NewStore(c)
		l, err := store.Acquire(ctx, "runner", time.Minute)
		Expect(err).NotTo(HaveOccurred())
		snap, err := store.Load(ctx)
		Expect(err).NotTo(HaveOccurred())
		snap.Paused, snap.PauseSeq, snap.Why = true, 4, "no verdict"
		_, err = store.Save(ctx, l.Token, snap)
		Expect(err).NotTo(HaveOccurred())
		code, out, errw := queue("resume", "--config", file)
		Expect(errw).To(BeEmpty())
		Expect(code).To(Equal(0))
		Expect(out).To(BeEmpty())
		Expect(gitIn(remote, "rev-parse", "refs/queue/control/resume-4")).To(Equal(sha))

		d, closeFn, err := newDriver(c, io.Discard, io.Discard)
		Expect(err).NotTo(HaveOccurred())
		defer closeFn()
		d.Owner, d.NewStrategy = "runner", func() core.Strategy { return idleStrategy{} }
		Expect(d.Step(ctx)).To(Succeed())
		snap, err = store.Load(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(snap.Paused).To(BeFalse())
		Expect(gitIn(remote, "for-each-ref", "refs/queue/control/")).To(BeEmpty())
	})

	// drain admits each [id, sha] by the command, then runs one runner step and returns what it saved.
	drain := func(admits ...[2]string) core.Snapshot {
		ctx := context.Background()
		for _, a := range admits {
			code, _, errw := queue("admit", "--config", file, a[0], a[1])
			Expect(errw).To(BeEmpty())
			Expect(code).To(Equal(0))
		}
		c, err := config.Parse(fmt.Appendf(nil, sample, remote, GinkgoT().TempDir()))
		Expect(err).NotTo(HaveOccurred())
		d, closeFn, err := newDriver(c, io.Discard, io.Discard)
		Expect(err).NotTo(HaveOccurred())
		defer closeFn()
		d.Owner, d.NewStrategy = "runner", func() core.Strategy { return idleStrategy{} }
		Expect(d.Step(ctx)).To(Succeed())
		snap, err := git.NewStore(c).Load(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(gitIn(remote, "for-each-ref", "refs/queue/admit/")).To(BeEmpty())
		return snap
	}
	queued := func(s core.Snapshot) (out []string) {
		for _, e := range s.Queued {
			out = append(out, e.ID+" "+e.Commit)
		}
		return out
	}

	It("queue withdraw asks the live runner to remove a queued change", func() {
		gitIn(".", "push", "-q", remote, sha+":refs/heads/trunk")
		Expect(queued(drain([2]string{"a", sha}))).To(Equal([]string{"a " + sha}))
		code, _, errw := queue("admit", "--config", file, "a", sha) // an admit still waiting
		Expect(code).To(Equal(0), errw)
		code, _, errw = queue("withdraw", "--config", file, "a")
		Expect(code).To(Equal(0), errw)
		Expect(gitIn(remote, "for-each-ref", "refs/queue/admit/")).To(BeEmpty())
		Expect(gitIn(remote, "for-each-ref", "refs/queue/control/")).To(ContainSubstring("withdraw-a." + sha))
		s := drain()
		Expect(s.Queued).To(BeEmpty())
		Expect(s.Settled).To(HaveLen(1))
		Expect(s.Settled[0].Kind).To(Equal(core.WithdrawnEvent))
		Expect(gitIn(remote, "for-each-ref", "refs/queue/control/")).To(BeEmpty())
		code, _, errw = queue("withdraw", "--config", file, "a")
		Expect(code).To(Equal(1))
		Expect(errw).To(ContainSubstring("not queued"))
	})

	It("queue resolve asks the live runner to clear an eject", func() {
		ctx := context.Background()
		gitIn(".", "push", "-q", remote, sha+":refs/heads/trunk")
		c, err := config.Parse(fmt.Appendf(nil, sample, remote, GinkgoT().TempDir()))
		Expect(err).NotTo(HaveOccurred())
		store := git.NewStore(c)
		l, err := store.Acquire(ctx, "runner", time.Minute)
		Expect(err).NotTo(HaveOccurred())
		snap, err := store.Load(ctx)
		Expect(err).NotTo(HaveOccurred())
		snap.Ejected, snap.Commits = map[string]bool{"e": true}, map[string]string{"e": sha}
		_, err = store.Save(ctx, l.Token, snap)
		Expect(err).NotTo(HaveOccurred())
		code, _, errw := queue("resolve", "--config", file, "e")
		Expect(code).To(Equal(0), errw)
		Expect(gitIn(remote, "for-each-ref", "refs/queue/control/")).To(ContainSubstring("resolve-e." + sha))
		Expect(drain().Ejected).To(BeEmpty())
		Expect(gitIn(remote, "for-each-ref", "refs/queue/control/")).To(BeEmpty())
		Expect(queued(drain([2]string{"e", sha}))).To(Equal([]string{"e " + sha}))
		code, _, errw = queue("resolve", "--config", file, "e")
		Expect(code).To(Equal(1))
		Expect(errw).To(ContainSubstring("not ejected"))
	})

	It("A change admitted twice before the runner drains is queued once and the repeat is refused", func() {
		b := gitIn(".", "commit-tree", gitIn(".", "mktree"), "-m", "b")
		s := drain([2]string{"b", b}, [2]string{"a", sha}, [2]string{"b", b})
		Expect(queued(s)).To(Equal([]string{"b " + b, "a " + sha}))
		Expect(s.Refused).To(HaveLen(1))
		Expect(s.Refused[0].ID).To(Equal("b"))
	})

	It("A change on a refused repeat is refused too, never queued on the accepted one", func() {
		a2 := gitIn(".", "commit-tree", gitIn(".", "mktree"), "-m", "a2")
		cc := gitIn(".", "commit-tree", gitIn(".", "mktree"), "-p", a2, "-m", "c")
		s := drain([2]string{"a", sha}, [2]string{"a", a2}, [2]string{"c", cc})
		Expect(queued(s)).To(Equal([]string{"a " + sha}))
		Expect(s.Refused).To(HaveLen(2))
		Expect(s.Refused[0].Commit).To(Equal(a2))
		Expect(s.Refused[1].Why).To(HavePrefix(fmt.Sprintf("built on %.8s, which was refused: ", a2)))
		Expect(s.BuildsOn["c"]).To(BeEmpty())
	})

	It("exits 2 with one plain line when the change id is unsafe", func() {
		code, out, errw := queue("admit", "--config", file, "../x", sha)
		Expect(code).To(Equal(2))
		Expect(out).To(BeEmpty())
		Expect(strings.Count(errw, "\n")).To(Equal(1))
		Expect(errw).To(ContainSubstring("unsafe"))
		Expect(gitIn(remote, "for-each-ref")).To(BeEmpty())
	})

	It("exits 1 on any other error", func() {
		code, _, errw := queue("admit", "--config", filepath.Join(GinkgoT().TempDir(), "none.yaml"), "a", "1")
		Expect(code).To(Equal(1))
		Expect(errw).NotTo(BeEmpty())
		code, _, _ = queue("admit", "--config", file, "only-id")
		Expect(code).To(Equal(1))
		code, _, errw = queue("admit", "--config", file, "a", "0123456789abcdef0123456789abcdef01234567")
		Expect(code).To(Equal(1), "a sha the current repo does not have is not pushed")
		Expect(errw).To(ContainSubstring("git push"))
		code, _, _ = queue("bogus")
		Expect(code).To(Equal(1))
	})

	It("hides a credential in any error line it writes", func() {
		for _, args := range [][]string{
			{"status", "--config", "https://user:SECRET@host/queue.yaml"},
			{"admit", "--config", file, "https://user:SECRET@host/x", sha},
		} {
			code, _, errw := queue(args...)
			Expect(code).NotTo(Equal(0))
			Expect(errw).To(ContainSubstring("argument: a URL must not hold credentials"))
			Expect(errw).NotTo(ContainSubstring("SECRET"))
		}
	})

	It("hides a credential in a config it cannot parse, even one with a comma", func() {
		cfg := strings.Replace(string(must(os.ReadFile(file))), "apiVersion: jetbridge.dev/queue/v2", "apiVersion: https://user:SEC,RET@host", 1)
		Expect(os.WriteFile(file, []byte(cfg), 0o600)).To(Succeed())
		code, out, errw := queue("status", "--config", file)
		Expect(code).To(Equal(1))
		Expect(errw).To(ContainSubstring("apiVersion"))
		Expect(out + errw).NotTo(MatchRegexp("SEC|RET"))
	})

	It("refuses a runner url holding a credential at load, naming the setting and never the value", func() {
		GinkgoT().Setenv("FAKE_JB_TOKEN", "fake-token")
		orig := string(must(os.ReadFile(file)))
		gitIn(".", "push", "-q", remote, sha+":refs/heads/trunk")
		child := gitIn(".", "commit-tree", gitIn(".", "mktree"), "-p", sha, "-m", "b")
		for _, u := range []string{"https://user:F4ke/Pa55@ci.example.invalid/%zz", "https://user:F4ke,Pa55@ci.example.invalid/%zz",
			"https://user:F4ke'Pa55@ci.example.invalid/%zz", "https://F4kePa55@ci.example.invalid"} {
			old := core.Secrets
			core.Secrets = &core.SecretSet{} // no secret registered by an earlier spec
			cfg := strings.NewReplacer("https://ci.example.invalid", `"`+u+`"`, "file:/path/to/auth.hdr", "env:FAKE_JB_TOKEN").Replace(orig)
			Expect(os.WriteFile(file, []byte(cfg), 0o600)).To(Succeed())
			var o, e bytes.Buffer
			entry(context.Background(), []string{"admit", "--config", file, "b", child}, &o, &e)
			code := entry(context.Background(), []string{"run", "--config", file, "--once"}, &o, &e)
			core.Secrets = old
			Expect(o.String()+e.String()).NotTo(MatchRegexp("F4ke|Pa55"), "url %q", u)
			Expect(e.String()).To(ContainSubstring("runner.url: a URL must not hold credentials"), "url %q", u)
			Expect(code).To(Equal(1), "url %q", u)
		}
	})

	It("refuses a runner url hidden behind a !!binary tag at load, never echoing it", func() {
		GinkgoT().Setenv("FAKE_JB_TOKEN", "fake-token")
		old := core.Secrets
		core.Secrets = &core.SecretSet{} // no secret registered by an earlier spec
		DeferCleanup(func() { core.Secrets = old })
		gitIn(".", "push", "-q", remote, sha+":refs/heads/trunk")
		child := gitIn(".", "commit-tree", gitIn(".", "mktree"), "-p", sha, "-m", "b")
		hidden := "!!binary aHR0cHM6Ly91c2VyOkY0a2UvUGE1NUBob3N0LyV6eg==" // https://user:F4ke/Pa55@host/%zz
		cfg := strings.NewReplacer("https://ci.example.invalid", hidden, "file:/path/to/auth.hdr", "env:FAKE_JB_TOKEN").Replace(string(must(os.ReadFile(file))))
		Expect(os.WriteFile(file, []byte(cfg), 0o600)).To(Succeed())
		var o, e bytes.Buffer
		entry(context.Background(), []string{"admit", "--config", file, "b", child}, &o, &e)
		code := entry(context.Background(), []string{"run", "--config", file, "--once"}, &o, &e)
		Expect(o.String() + e.String()).NotTo(MatchRegexp("F4ke|Pa55"))
		Expect(code).NotTo(Equal(0))
	})

	It("refuses a notify path holding a credential at load, never echoing it", func() {
		old := core.Secrets
		core.Secrets = &core.SecretSet{} // no secret registered by an earlier spec
		DeferCleanup(func() { core.Secrets = old })
		cfg := string(must(os.ReadFile(file))) + "notify: {kind: log, path: \"https://user:F4ke,Pa55@host/events.jsonl\"}\n"
		Expect(os.WriteFile(file, []byte(cfg), 0o600)).To(Succeed())
		var o, e bytes.Buffer
		Expect(entry(context.Background(), []string{"run", "--config", file, "--once"}, &o, &e)).To(Equal(1))
		Expect(o.String() + e.String()).NotTo(MatchRegexp("F4ke|Pa55"))
		Expect(e.String()).To(ContainSubstring("notify.path: a URL must not hold credentials"))
	})

	It("reads, admits and resumes with a runner section only run refuses", func() {
		Expect(os.WriteFile(file, append(must(os.ReadFile(file)), "  wait_cap: 0s\n"...), 0o600)).To(Succeed())
		gitIn(".", "push", "-q", remote, sha+":refs/heads/trunk")
		for _, args := range [][]string{{"status"}, {"admit", "a", sha}, {"resume"}} {
			code, _, errw := queue(append([]string{args[0], "--config", file}, args[1:]...)...)
			Expect(code).To(Equal(0), "%v: %s", args, errw)
		}
		code, _, errw := queue("run", "--config", file, "--once")
		Expect(code).To(Equal(1))
		Expect(errw).To(ContainSubstring("runner.wait_cap must be more than zero"))
	})

	It("hides a password with a comma in a --config path it cannot open", func() {
		old := core.Secrets
		core.Secrets = &core.SecretSet{} // no secret registered by an earlier spec
		DeferCleanup(func() { core.Secrets = old })
		var o, e bytes.Buffer
		Expect(entry(context.Background(), []string{"status", "--config", "https://user:SEC,RET@host/queue.yaml"}, &o, &e)).To(Equal(1))
		Expect(e.String()).To(ContainSubstring("argument: a URL must not hold credentials"))
		Expect(o.String() + e.String()).NotTo(MatchRegexp("SEC|RET"))
	})

	It("never writes a reason it loaded unredacted back to the state ref", func() {
		const token = "Tk5mQw2zRb9x"
		ctx := context.Background()
		c, err := config.Parse(must(os.ReadFile(file)))
		Expect(err).NotTo(HaveOccurred())
		store := git.NewStore(c)
		l, err := store.Acquire(ctx, "seed", time.Nanosecond)
		Expect(err).NotTo(HaveOccurred())
		snap := must(store.Load(ctx))
		snap.Paused, snap.Why = true, "denied Bearer "+token // saved before the token was configured
		_, err = store.Save(ctx, l.Token, snap)
		Expect(err).NotTo(HaveOccurred())
		GinkgoT().Setenv("FAKE_JB_TOKEN", token)
		cfg := strings.Replace(string(must(os.ReadFile(file))), "file:/path/to/auth.hdr", "env:FAKE_JB_TOKEN", 1)
		Expect(os.WriteFile(file, []byte(cfg), 0o600)).To(Succeed())
		var o, e bytes.Buffer
		Expect(entry(ctx, []string{"run", "--config", file, "--once"}, &o, &e)).To(Equal(0), e.String())
		Expect(gitIn(remote, "show", "refs/queue/state:snapshot.json")).To(ContainSubstring("denied"))
		Expect(gitIn(remote, "show", "refs/queue/state:snapshot.json")).NotTo(ContainSubstring(token))
	})

	It("refuses a credential URL given as one --flag=value argument, never echoing it", func() {
		old := core.Secrets
		core.Secrets = &core.SecretSet{} // no secret registered by an earlier spec
		DeferCleanup(func() { core.Secrets = old })
		for _, a := range []string{"--config=https://user:F4ke/Pa55@host/q.yaml", "-config=https://user:F4ke/Pa55@host/q.yaml"} {
			var o, e bytes.Buffer
			code := entry(context.Background(), []string{"status", a}, &o, &e)
			Expect(code).NotTo(Equal(0), a)
			Expect(e.String()).To(ContainSubstring("argument: a URL must not hold credentials"), a)
			Expect(o.String()+e.String()).NotTo(MatchRegexp("F4ke|Pa55"), a)
		}
	})

	It("hides a credential in a flag the command refuses", func() {
		var o, e bytes.Buffer
		code := entry(context.Background(), []string{"status", "--window", "https://user:SECRET@host"}, &o, &e)
		Expect(code).To(Equal(1))
		Expect(e.String()).To(ContainSubstring("argument: a URL must not hold credentials"))
		Expect(o.String() + e.String()).NotTo(ContainSubstring("SECRET"))
	})

	It("hides every credential the whole command writes, a bad flag and a runner error alike", func() {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", "https://user:SECRET@host/%zz")
			w.WriteHeader(http.StatusTemporaryRedirect)
		}))
		DeferCleanup(srv.Close)
		GinkgoT().Setenv("FAKE_JB_TOKEN", "fake-token")
		cfg := strings.NewReplacer("https://ci.example.invalid", srv.URL, "file:/path/to/auth.hdr", "env:FAKE_JB_TOKEN").Replace(string(must(os.ReadFile(file))))
		Expect(os.WriteFile(file, []byte(cfg), 0o600)).To(Succeed())
		gitIn(".", "push", "-q", remote, sha+":refs/heads/trunk")
		child := gitIn(".", "commit-tree", gitIn(".", "mktree"), "-p", sha, "-m", "b")
		var o, e bytes.Buffer
		Expect(entry(context.Background(), []string{"admit", "--config", file, "b", child}, &o, &e)).To(Equal(0), e.String())
		Expect(entry(context.Background(), []string{"run", "--config", file, "--every", "https://user:SECRET@host"}, &o, &e)).To(Equal(1))
		Expect(entry(context.Background(), []string{"run", "--config", file, "--once"}, &o, &e)).To(Equal(0), e.String())
		Expect(e.String()).To(ContainSubstring("start "), "the runner error reaches stderr")
		Expect(e.String()).To(ContainSubstring("://***@host"))
		Expect(o.String() + e.String()).NotTo(ContainSubstring("SECRET"))
	})

	It("hides every configured secret in every encoding from every output", func() {
		ctx := context.Background()
		// the token holds & , ' and /; uri is an error quoting a URL built from it
		const token = "Xq7v&Zk9r,Lm4t'Pw8s/Yh2n"
		uri := "https://user:" + token + "@repo.example.invalid/repo"
		GinkgoT().Setenv("FAKE_JB_TOKEN", token)
		dir := GinkgoT().TempDir()
		events := filepath.Join(dir, "events.jsonl")
		cfg := strings.Replace(fmt.Sprintf(sample, remote, dir), "file:/path/to/auth.hdr", "env:FAKE_JB_TOKEN", 1) + "notify: {kind: log, path: " + events + "}\n"
		Expect(os.WriteFile(file, []byte(cfg), 0o600)).To(Succeed())
		c, err := config.Parse([]byte(cfg))
		Expect(err).NotTo(HaveOccurred())
		store := git.NewStore(c)
		l, err := store.Acquire(ctx, "runner", time.Minute)
		Expect(err).NotTo(HaveOccurred())
		snap, err := store.Load(ctx)
		Expect(err).NotTo(HaveOccurred())
		snap.Landing = &core.Landing{Main: "trunk", Candidate: sha, Entries: []core.Entry{{ID: "a", Commit: sha}}}
		_, err = store.Save(ctx, l.Token, snap)
		Expect(err).NotTo(HaveOccurred())

		var o, e bytes.Buffer
		Expect(entry(ctx, []string{"status", "--config", file}, &o, &e)).To(Equal(0), e.String()) // registers the secrets
		decoded := token
		j, _ := json.Marshal(map[string]string{"url": uri, "pw": decoded, "auth": "Bearer " + token, "path": url.PathEscape(token)})
		jj, _ := json.Marshal(string(j))
		leak := fmt.Errorf("denied %s %s %s %s %q %q %s", uri, j, jj, strings.ReplaceAll(string(jj), "/", `\\/`), uri, decoded, url.QueryEscape(decoded))
		errw := core.NewRedactWriter(&e)
		d, closeFn, err := newDriver(c, &o, errw)
		Expect(err).NotTo(HaveOccurred())
		defer closeFn()
		d.Owner, d.Lander, d.Admissions = "runner", leakyLander{d.Lander, leak}, leakyAdmissions{leak}
		Expect(d.Step(ctx)).To(Succeed())
		Expect(errw.Flush()).To(Succeed())
		Expect(entry(ctx, []string{"status", "--config", file}, &o, &e)).To(Equal(0), e.String())
		Expect(entry(ctx, []string{"view", "--config", file}, &o, &e)).To(Equal(0), e.String())
		saved, err := store.Load(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(saved.Paused).To(BeTrue(), "the leak reached the saved state")
		note := string(must(os.ReadFile(events)))
		Expect(note).To(ContainSubstring(`"paused"`))
		Expect(e.String()).To(ContainSubstring("admissions: denied"))
		outputs := map[string]string{"stderr": e.String(), "stdout (status, view)": o.String(), "notify file": note,
			"snapshot": gitIn(remote, "log", "-p", "refs/queue/state")}
		for what, text := range outputs {
			Expect(text).To(ContainSubstring("denied"), what)
			for _, part := range []string{"Xq7v", "Zk9r", "Lm4t", "Pw8s", "Yh2n"} {
				Expect(text).NotTo(ContainSubstring(part), what)
			}
		}
	})

	It("A queue step that restarts keeps waiting for the same test build", func() {
		var builds atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reply := map[string]string{"check": `{"id":1,"status":"started"}`, "input_to": `[{"id":100}]`,
				"versions": fmt.Sprintf(`[{"id":7,"version":{"ref":%q}}]`, strings.TrimPrefix(r.URL.Query().Get("filter"), "ref:"))}
			if p := path.Base(r.URL.Path); p == "builds" {
				fmt.Fprintf(w, `{"id":%d}`, 99+builds.Add(1))
			} else if reply[p] != "" || strings.HasPrefix(r.URL.Path, "/api/v1/builds/") {
				fmt.Fprint(w, cmp.Or(reply[p], `{"status":"succeeded"}`))
			}
		}))
		DeferCleanup(srv.Close)
		GinkgoT().Setenv("FAKE_JB_TOKEN", "fake-token")
		c := must(config.Parse([]byte(strings.NewReplacer("https://ci.example.invalid", srv.URL, "file:/path/to/auth.hdr", "env:FAKE_JB_TOKEN").Replace(string(must(os.ReadFile(file)))))))
		gitIn(".", "push", "-q", remote, sha+":refs/heads/trunk")
		code, _, errw := queue("admit", "--config", file, "a", gitIn(".", "commit-tree", gitIn(".", "mktree"), "-p", sha, "-m", "a"))
		Expect(code).To(Equal(0), errw)
		ctx := context.Background()
		for range 4 { // each step a new process: a new driver and runner over the same refs
			d, closeFn, err := newDriver(c, io.Discard, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			d.Owner = "runner"
			Expect(d.Step(ctx)).To(Succeed())
			closeFn()
		}
		Expect(must(git.NewStore(c).Load(ctx)).Landed).To(Equal(map[string]bool{"a": true}), "the verdict was read after the restarts")
		Expect(builds.Load()).To(Equal(int32(1)), "one build, never triggered again")
	})

	It("runs one step and stops with --once", func() {
		code, _, errw := queue("run", "--config", file, "--once")
		Expect(errw).To(BeEmpty())
		Expect(code).To(Equal(0))
	})
})

var _ = Describe("status", func() {
	It("shows each ejected change with its reason from the settle records", func() {
		at := time.Date(2026, 10, 3, 14, 30, 0, 0, time.UTC)
		s := core.Snapshot{
			Ejected: map[string]bool{"b": true, "old": true},
			Settled: []core.SettleRecord{
				{ID: "b", Kind: core.EjectedEvent, At: at, Why: "failed on its own", Cause: "culprit"},
				{ID: "a", Kind: core.LandedEvent, At: at},
			},
		}
		b, err := json.Marshal(summary(s))
		Expect(err).NotTo(HaveOccurred())
		var got struct {
			Ejected []struct{ ID, Why, Cause, At string }
		}
		Expect(json.Unmarshal(b, &got)).To(Succeed())
		Expect(got.Ejected).To(Equal([]struct{ ID, Why, Cause, At string }{
			{"b", "failed on its own", "culprit", "2026-10-03T14:30:00Z"},
			{ID: "old"},
		}))
	})

	It("A credential inside a reason is hidden in status", func() {
		why := "landing failed 3 times: git push: remote: denied for https://user:SECRET@host/repo"
		s := core.Snapshot{Paused: true, Why: why, Ejected: map[string]bool{"b": true},
			Settled: []core.SettleRecord{{ID: "b", Kind: core.EjectedEvent, Why: why}, {ID: "a, b", Kind: core.FlakeEvent, Why: why}},
			Refused: []core.Refusal{{ID: "c", Why: why}}}
		b, err := json.Marshal(summary(s))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).NotTo(ContainSubstring("SECRET"))
		Expect(string(b)).To(ContainSubstring("denied for https://***@host/repo"))
	})

	It("keeps status valid JSON when a reason ends in a URL and an id holds an @", func() {
		s := core.Snapshot{Paused: true, Why: "cannot reach 'https://git.example.invalid'",
			Refused: []core.Refusal{{ID: "dev@example.invalid", Why: "unsafe id"}}}
		var b bytes.Buffer
		w := core.NewRedactWriter(&b) // as every stdout line passes
		Expect(json.NewEncoder(w).Encode(summary(s))).To(Succeed())
		var got struct {
			Why     string
			Refused []core.Refusal
		}
		Expect(json.Unmarshal(b.Bytes(), &got)).To(Succeed(), b.String())
		Expect(got.Why).To(Equal(s.Why))
		Expect(got.Refused).To(Equal(s.Refused))
	})

	It("lists the latest flakes with their batch and reason", func() {
		at := time.Date(2026, 10, 3, 14, 30, 0, 0, time.UTC)
		s := core.Snapshot{Settled: []core.SettleRecord{
			{ID: "a", Kind: core.FlakeEvent, At: at, Why: "red as a batch, green when split", Run: "r1", Batch: []string{"a", "b"}},
			{ID: "b", Kind: core.FlakeEvent, At: at, Why: "red as a batch, green when split", Run: "r1", Batch: []string{"a", "b"}},
			{ID: "c", Kind: core.LandedEvent, At: at},
		}}
		b, err := json.Marshal(summary(s))
		Expect(err).NotTo(HaveOccurred())
		var got struct {
			Flakes []struct{ ID, Why, Run string }
		}
		Expect(json.Unmarshal(b, &got)).To(Succeed())
		Expect(got.Flakes).To(Equal([]struct{ ID, Why, Run string }{
			{"a", "red as a batch, green when split", "r1"}, {"b", "red as a batch, green when split", "r1"}}))
	})
})

type leakyLander struct {
	core.Lander
	err error
}

func (l leakyLander) Contains(context.Context, string, string, uint64) (bool, error) {
	return false, l.err
}

type leakyAdmissions struct{ err error }

func (l leakyAdmissions) Pending(context.Context, []core.Entry) ([]core.Pending, error) {
	return nil, l.err
}
func (leakyAdmissions) Done(context.Context, string, string) error { return nil }

func must[T any](v T, err error) T {
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return v
}
