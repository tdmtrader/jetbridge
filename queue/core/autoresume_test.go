package core_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

var _ = Describe("Driver auto-resume", func() {
	var (
		ctx   context.Context
		store *memStore
		res   *memResumes
		note  *memNotifier
		clock time.Time
	)
	const cooldown = 5 * time.Minute
	BeforeEach(func() {
		ctx, store, note, res = context.Background(), &memStore{}, &memNotifier{}, &memResumes{}
		clock = time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	})
	// driver pauses on its first run, which cannot compose and so has no verdict.
	driver := func() *core.Driver {
		return &core.Driver{
			Store: store, Composer: failCompose{}, Runner: &memRunner{}, Lander: &memLander{c: &memComposer{}}, Notifier: note,
			NewStrategy: func() core.Strategy { return &core.Serial{Max: 1} }, Main: "core", Owner: "runner",
			Resumes: res, Cooldown: cooldown, Now: func() time.Time { return clock }, Log: func(string, ...any) {},
		}
	}
	step := func(d *core.Driver, after time.Duration) {
		clock = clock.Add(after)
		Expect(d.Step(ctx)).To(Succeed())
	}
	// pausedTwice leaves pause 2 saved: pause 1 ended by the cool-down, the run no-verdict again.
	pausedTwice := func() *core.Driver {
		d := driver()
		Expect(d.Admit(ctx, core.Entry{ID: "a", Commit: "ca"})).To(Succeed())
		step(d, 0)
		step(d, cooldown)
		return d
	}

	It("A queue paused for no verdict resumes after the cool-down", func() {
		d := driver()
		Expect(d.Admit(ctx, core.Entry{ID: "a", Commit: "ca"})).To(Succeed())
		step(d, 0)
		Expect(store.snap().Paused).To(BeTrue())
		step(d, cooldown-time.Second)
		Expect(store.snap().PauseSeq).To(BeEquivalentTo(1), "still the first pause")
		Expect(note.of(core.ResumedEvent)).To(BeEmpty())
		step(d, time.Second)
		resumed := note.of(core.ResumedEvent)
		Expect(resumed).To(HaveLen(1))
		Expect(resumed[0].Why).To(Equal("auto-resume after cool-down"))
		var kinds []core.EventKind
		for _, r := range store.snap().Settled {
			kinds = append(kinds, r.Kind)
			if r.Kind == core.ResumedEvent {
				Expect(r.Why).To(Equal("auto-resume after cool-down"))
			}
		}
		Expect(kinds).To(Equal([]core.EventKind{core.PausedEvent, core.ResumedEvent, core.PausedEvent}))
		Expect(store.snap().PauseSeq).To(BeEquivalentTo(2), "the run had no verdict again")
		step(d, cooldown-time.Second)
		Expect(note.of(core.ResumedEvent)).To(HaveLen(1), "the second pause waits for its own cool-down")
	})

	It("A queue paused for another reason waits for resume", func() {
		_, err := store.Save(ctx, 0, core.Snapshot{Version: "0", Paused: true, PauseSeq: 1, Why: "landing failed 3 times", PausedAt: clock})
		Expect(err).NotTo(HaveOccurred())
		d := driver()
		step(d, 10*cooldown)
		Expect(store.snap().Paused).To(BeTrue())
		Expect(note.events).To(BeEmpty())
	})

	It("A resume request for a pause the cool-down already ended does not clear the next", func() {
		d := pausedTwice()
		res.reqs = []core.ResumeRequest{{Seq: 1, SHA: "sha-main"}}
		step(d, time.Second)
		Expect(store.snap().Paused).To(BeTrue())
		Expect(note.of(core.ResumedEvent)).To(HaveLen(1), "only the auto-resume")
		Expect(res.reqs).To(BeEmpty())
	})
})
