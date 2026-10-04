package git_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/adapters/git"
	"github.com/concourse/concourse/queue/core"
)

var _ = Describe("Runner", func() {
	ctx := context.Background()
	var (
		r            *remote
		runner       *git.Runner
		now          time.Time
		older, newer string
	)
	at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	tr := core.Run{ID: "t1.1-r"}

	fresh := func() *git.Runner {
		g := git.NewRunner(r.bare, time.Hour)
		g.Now = func() time.Time { return now }
		return g
	}
	BeforeEach(func() {
		r, now = newRemote(), at
		older, newer = r.commit("older", r.base), r.commit("newer", r.base)
		runner = fresh()
	})
	poll := func(g *git.Runner) (core.Verdict, bool) {
		v, done, err := g.Poll(ctx, tr.ID)
		Expect(err).NotTo(HaveOccurred())
		return v, done
	}
	verdict := func(g *git.Runner) core.Verdict {
		v, done := poll(g)
		Expect(done).To(BeTrue())
		return v
	}
	start := func(c string) { Expect(runner.Start(ctx, tr, c)).To(Succeed()) }
	record := func(c string, v core.Verdict) error { return runner.RecordVerdict(ctx, tr.ID, c, v) }

	It("Starting the same run on the same candidate twice changes nothing", func() {
		start(newer)
		ref := run(r.bare, "rev-parse", "refs/mq/runs/"+tr.ID)
		Expect(run(r.bare, "rev-parse", ref+"^{commit}")).To(Equal(newer))
		now = at.Add(time.Minute)
		start(newer)
		Expect(run(r.bare, "rev-parse", "refs/mq/runs/"+tr.ID)).To(Equal(ref))
	})

	It("A test result recorded for an older candidate is not used", func() {
		start(older)
		start(newer)
		Expect(record(older, core.Pass)).To(Succeed())
		v, done := poll(runner)
		Expect(done).To(BeFalse())
		Expect(v).To(Equal(core.None))
	})

	It("A passing or failing test result is reported as pass or fail", func() {
		start(newer)
		Expect(record(newer, core.Fail)).To(Succeed())
		Expect(verdict(runner)).To(Equal(core.Fail))
		Expect(runner.Start(ctx, core.Run{ID: "t2.1-r"}, older)).To(Succeed())
		Expect(runner.RecordVerdict(ctx, "t2.1-r", older, core.Pass)).To(Succeed())
		v, done, err := runner.Poll(ctx, "t2.1-r")
		Expect(err).NotTo(HaveOccurred())
		Expect(done).To(BeTrue())
		Expect(v).To(Equal(core.Pass))
	})

	It("A run with no test result waits, then gives no verdict after the wait cap", func() {
		start(newer)
		v, done := poll(runner)
		Expect([]any{v, done}).To(Equal([]any{core.None, false}))
		now = at.Add(time.Hour + time.Second)
		v, done = poll(runner)
		Expect([]any{v, done}).To(Equal([]any{core.None, true}))
	})

	It("A restarted runner sees the same runs and results", func() {
		start(newer)
		Expect(record(newer, core.Pass)).To(Succeed())
		other := fresh()
		Expect(other.Start(ctx, tr, newer)).To(Succeed())
		Expect(verdict(other)).To(Equal(core.Pass))
	})

	It("A recorded test result cannot be overwritten", func() {
		start(newer)
		Expect(record(newer, core.Pass)).To(Succeed())
		Expect(record(newer, core.Pass)).To(Succeed(), "the same result again is accepted")
		Expect(record(newer, core.Fail)).To(MatchError(ContainSubstring("already")))
		Expect(verdict(runner)).To(Equal(core.Pass))
	})
})
