package git_test

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/adapters/git"
	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
	"github.com/concourse/concourse/queue/wire"
)

// runIn is run with stdin.
func runIn(dir, stdin string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), string(out))
	return strings.TrimSpace(string(out))
}

// verdicts is a runner whose every run ends with v.
type verdicts struct{ v core.Verdict }

func (r *verdicts) Start(context.Context, core.Run, string) error { return nil }
func (r *verdicts) Poll(context.Context, string) (core.Verdict, bool, error) {
	return r.v, true, nil
}

var _ = Describe("The existing request refs", func() {
	ctx := context.Background()
	var (
		r      *remote
		c      config.Config
		runner *verdicts
		d      *core.Driver
	)
	// change makes a commit on base adding one file, so it lands as a commit of its own.
	change := func(name string) string {
		blob := runIn(r.work, name+"\n", "hash-object", "-w", "--stdin")
		tree := runIn(r.work, "100644 blob "+blob+"\t"+name+".txt\n", "mktree")
		sha := run(r.work, "commit-tree", tree, "-p", r.base, "-m", "change "+name)
		run(r.work, "push", "-q", r.bare, sha+":refs/heads/"+name)
		return sha
	}
	// tag writes an annotated tag on sha the way the old queue's MkTag does.
	tag := func(sha, name, body string) string {
		return runIn(r.work, fmt.Sprintf("object %s\ntype commit\ntag %s\ntagger JetBridge queue <jbq@invalid> %d +0000\n\n%s\n", sha, name, time.Now().Unix(), body), "mktag")
	}
	// oldAdmit pushes what the old queue's admit pushes: the row's tag at refs/queue/<rid>,
	// its body in admitSeat's order (admit body, shipper, at-ns), and deletes any eject record of rid.
	oldAdmit := func(rid, sha string) {
		at := time.Now()
		body := fmt.Sprintf("jbq-admit v1\nrid: %s\nsha: %s\nat: %d\nnotify: agent:x\nby-person: Nick\nby-agent: Sol - Refactor\nat-ns: %d", rid, sha, at.Unix(), at.UnixNano())
		specs := []string{tag(sha, rid, body) + ":refs/queue/" + rid}
		if run(r.bare, "for-each-ref", "refs/queue-out/"+rid) != "" {
			specs = append(specs, ":refs/queue-out/"+rid)
		}
		run(r.work, append([]string{"push", "-q", "--atomic", r.bare}, specs...)...)
	}
	// oldWithdraw pushes what the old queue's withdraw of a queued row pushes, in one atomic push.
	oldWithdraw := func(rid, sha, kind string) {
		body := fmt.Sprintf("jbq-resolved v1\nrid: %s\nat: %d\nkind: %s\nsha: %s\nwas: queued\nreason: not needed\nby-person: Nick\nby-agent: Sol - Refactor", rid, time.Now().Unix(), kind, sha)
		run(r.work, "push", "-q", "--atomic", r.bare, tag(sha, "resolved", body)+":refs/queue-state/resolved/"+rid, ":refs/queue/"+rid)
	}
	steps := func(n int) {
		for range n {
			Expect(d.Step(ctx)).To(Succeed())
		}
	}
	snap := func() core.Snapshot {
		s, err := git.NewStore(c).Load(ctx)
		Expect(err).NotTo(HaveOccurred())
		return s
	}
	queuedIDs := func() []string { return ids(snap().Queued) }
	refsUnder := func(prefix string) []string {
		out := run(r.bare, "for-each-ref", "--format=%(refname)")
		return slices.DeleteFunc(strings.Fields(out), func(s string) bool { return !strings.HasPrefix(s, prefix) })
	}
	BeforeEach(func() {
		r = newRemote()
		var err error
		c, err = config.Parse(fmt.Appendf(nil, "apiVersion: jetbridge.dev/queue/v2\nrepository: {uri: %s, main: main, candidate: queue-next}\n"+
			"admission: {prefix: refs/mq/admit/, control_prefix: refs/mq/control/, legacy_refs: refs/queue/}\n"+
			"lander: {lease_ref: refs/mq/lease}\nstore: {ref: refs/mq/state}\nbatch: {max: 1}\n", r.bare))
		Expect(err).NotTo(HaveOccurred())
		runner = &verdicts{core.Pass}
		var closeFn func()
		d, closeFn, err = wire.Driver(c, io.Discard, func(string, ...any) {}, runner)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(closeFn)
	})

	It("A change admitted through the existing request refs is queued and lands", func() {
		a := change("a")
		oldAdmit("RELEASE-v1.2.3", a)
		steps(1)
		Expect(queuedIDs()).To(Equal([]string{"RELEASE-v1.2.3"}))
		Expect(refsUnder("refs/queue/")).To(Equal([]string{"refs/queue/RELEASE-v1.2.3"}), "kept while it waits, so the old queue still sees it")
		steps(2)
		Expect(snap().Landed).To(HaveKey("RELEASE-v1.2.3"))
		Expect(oldRows(run(r.bare, "log", "-1", "--format=%B", "refs/heads/main"))).To(Equal([][2]string{{"RELEASE-v1.2.3", a}}))
	})

	It("Once it lands, its request ref is removed as the old queue removes a landed row", func() {
		oldAdmit("R1", change("a"))
		steps(3)
		Expect(refsUnder("refs/queue")).To(BeEmpty())
		steps(1)
		Expect(snap().Landed).To(HaveKey("R1"))
		Expect(queuedIDs()).To(BeEmpty())
	})

	It("An eject writes the old queue's eject record and removes the request ref, in one push", func() {
		runner.v = core.Fail
		a := change("a")
		oldAdmit("R1", a)
		steps(3)
		Expect(snap().Ejected).To(HaveKey("R1"))
		Expect(refsUnder("refs/queue")).To(Equal([]string{"refs/queue-out/R1"}))
		Expect(run(r.bare, "cat-file", "-t", "refs/queue-out/R1")).To(Equal("tag"))
		Expect(run(r.bare, "rev-parse", "refs/queue-out/R1^{commit}")).To(Equal(a))
		msg := run(r.bare, "for-each-ref", "--format=%(contents)", "refs/queue-out/R1")
		Expect(msg).To(HavePrefix("jbq-eject v1\nrid: R1\nsha: " + a + "\nat: "))
		Expect(strings.Split(msg, "\n")).To(ContainElements("reason: failed on its own", "cause: real", "notify: agent:x", "by-person: Nick", "by-agent: Sol - Refactor"))

		By("the old admit of the same row id at a new commit clears the eject and queues it again")
		b := change("b")
		oldAdmit("R1", b)
		runner.v = core.None
		steps(2)
		Expect(snap().Queued).To(ConsistOf(core.Entry{ID: "R1", Commit: b, Owner: "t", AdmittedAt: snap().Queued[0].AdmittedAt}))
	})

	It("A withdraw or a supersede on the existing request refs removes the queued change", func() {
		a, b := change("a"), change("b")
		runner.v = core.None
		oldAdmit("R1", a)
		oldAdmit("R2", b)
		steps(1)
		Expect(queuedIDs()).To(Equal([]string{"R1", "R2"}))
		oldWithdraw("R1", a, "withdrawn")
		oldWithdraw("R2", b, "superseded")
		steps(1)
		Expect(queuedIDs()).To(BeEmpty())
		kinds := []core.EventKind{}
		for _, s := range snap().Settled {
			kinds = append(kinds, s.Kind)
		}
		Expect(kinds).To(ContainElements(core.WithdrawnEvent, core.WithdrawnEvent))
		Expect(refsUnder("refs/queue")).To(Equal([]string{"refs/queue-state/resolved/R1", "refs/queue-state/resolved/R2"}), "the old records are left as the old queue wrote them")
	})

	It("A withdraw through the queue retires the request ref as the old queue's withdraw does", func() {
		a := change("a")
		runner.v = core.None
		oldAdmit("R1", a)
		steps(1)
		Expect(d.Withdraw(ctx, "R1", a)).To(Succeed())
		steps(1)
		Expect(queuedIDs()).To(BeEmpty())
		Expect(refsUnder("refs/queue")).To(Equal([]string{"refs/queue-state/resolved/R1"}))
		msg := run(r.bare, "for-each-ref", "--format=%(contents)", "refs/queue-state/resolved/R1")
		Expect(msg).To(HavePrefix("jbq-resolved v1\nrid: R1\nat: "))
		Expect(strings.Split(msg, "\n")).To(ContainElements("kind: withdrawn", "sha: "+a, "was: queued"))
	})

	It("The queue's own admit still works beside the existing request refs", func() {
		a, b := change("a"), change("b")
		runner.v = core.None
		oldAdmit("R1", a)
		Expect(git.Admit(ctx, c, r.work, "own", b)).To(Succeed())
		steps(1)
		Expect(queuedIDs()).To(ConsistOf("R1", "own"))
		Expect(refsUnder("refs/mq/admit/")).To(BeEmpty())
		Expect(refsUnder("refs/queue")).To(Equal([]string{"refs/queue/R1"}))
	})

	It("The queue writes nothing under the existing request refs but the old queue's own retirements", func() {
		runner.v = core.Fail
		oldAdmit("R1", change("a"))
		steps(4)
		runner.v = core.Pass
		oldAdmit("R2", change("b"))
		steps(4)
		Expect(refsUnder("refs/queue")).To(Equal([]string{"refs/queue-out/R1"}))
		for _, ref := range strings.Fields(run(r.bare, "for-each-ref", "--format=%(refname)")) {
			Expect(ref).To(Or(HavePrefix("refs/mq/"), HavePrefix("refs/heads/"), Equal("refs/queue-out/R1")))
		}
	})
})
