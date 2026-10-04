package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
		d, closeFn, err := newDriver(c)
		Expect(err).NotTo(HaveOccurred())
		defer closeFn()
		d2, closeFn2, err := newDriver(c)
		Expect(err).NotTo(HaveOccurred())
		defer closeFn2()
		Expect(d.Main).To(Equal("trunk"))
		Expect(d.MaxFailures).To(Equal(5))
		Expect(d.Slots).To(Equal(1))
		Expect(d.Owner).NotTo(Equal(d2.Owner))
		Expect(d.NewStrategy()).To(Equal(&core.Serial{Max: 3, Policy: core.Policy{RetryNone: 1}}))
		Expect(d.Notifier.Notify(context.Background(), core.Event{})).To(Succeed())
	})

	It("builds an adaptive strategy afresh on each call when configured", func() {
		c, err := config.Parse(fmt.Appendf(nil, sample+"", "/r.git", GinkgoT().TempDir()))
		Expect(err).NotTo(HaveOccurred())
		c.Batch.Adaptive = &config.Adaptive{Start: 2, Min: 1, GrowAfter: 2}
		d, closeFn, err := newDriver(c)
		Expect(err).NotTo(HaveOccurred())
		defer closeFn()
		s := d.NewStrategy()
		Expect(s).To(Equal(&core.Serial{Max: 3, Policy: core.Policy{RetryNone: 1}, Adaptive: &core.Adaptive{Start: 2, Min: 1, GrowAfter: 2}}))
		Expect(d.NewStrategy()).NotTo(BeIdenticalTo(s))
	})

	It("A queue config with the log notifier builds its driver", func() {
		c, err := config.Parse(fmt.Appendf(nil, sample+"notify: {kind: log, path: \"-\"}\n", "/r.git", GinkgoT().TempDir()))
		Expect(err).NotTo(HaveOccurred())
		d, closeFn, err := newDriver(c)
		Expect(err).NotTo(HaveOccurred())
		defer closeFn()
		Expect(d.Notifier).To(BeAssignableToTypeOf(&lognotify.Notifier{}))
	})

	It("refuses a log notify section without a path", func() {
		c, err := config.Parse(fmt.Appendf(nil, sample+"notify: {kind: log}\n", "/r.git", GinkgoT().TempDir()))
		Expect(err).NotTo(HaveOccurred())
		_, _, err = newDriver(c)
		Expect(err).To(MatchError(ContainSubstring("notify.path is required")))
	})

	It("refuses a notify kind it cannot wire", func() {
		c, err := config.Parse(fmt.Appendf(nil, sample+"notify:\n  kind: carrier-pigeon\n", "/r.git", GinkgoT().TempDir()))
		Expect(err).NotTo(HaveOccurred())
		_, _, err = newDriver(c)
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
		Expect(gitIn(remote, "rev-parse", "refs/queue/admit/a")).To(Equal(sha))
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

		d, closeFn, err := newDriver(c)
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

		d, closeFn, err := newDriver(c)
		Expect(err).NotTo(HaveOccurred())
		defer closeFn()
		d.Owner, d.NewStrategy = "runner", func() core.Strategy { return idleStrategy{} }
		Expect(d.Step(ctx)).To(Succeed())
		snap, err = store.Load(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(snap.Paused).To(BeFalse())
		Expect(gitIn(remote, "for-each-ref", "refs/queue/control/")).To(BeEmpty())
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
			Expect(errw).To(ContainSubstring("https://***@host/"))
			Expect(errw).NotTo(ContainSubstring("SECRET"))
		}
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
