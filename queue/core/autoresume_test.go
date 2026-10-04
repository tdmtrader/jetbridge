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
		main  string
	)
	const cooldown = 5 * time.Minute
	BeforeEach(func() {
		ctx, store, note, res = context.Background(), &memStore{}, &memNotifier{}, &memResumes{}
		clock, main = time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC), "aaaaaaaa11"
	})
	// driver pauses on its first run, which cannot compose and so has no verdict.
	driver := func() *core.Driver {
		return &core.Driver{
			Store: store, Composer: failCompose{}, Runner: &memRunner{}, Lander: headLander{&memLander{c: &memComposer{}}, &main}, Notifier: note,
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

	// held leaves a dead runner's second no-verdict pause on one main held.
	held := func() *core.Driver {
		d := pausedTwice()
		step(d, cooldown)
		return d
	}
	resumes := func() int { return len(note.of(core.ResumedEvent)) }

	It("A dead runner is auto-resumed once, then the pause holds", func() {
		d := held()
		Expect(resumes()).To(Equal(1))
		snap := store.snap()
		Expect(snap.Paused).To(BeTrue())
		Expect(snap.Why).To(Equal("no verdict twice on main aaaaaaaa; nothing auto-resumes it — run `queue resume`"))
		Expect(snap.Settled[len(snap.Settled)-1].Why).To(Equal(snap.Why))
		step(d, 100*cooldown)
		Expect(resumes()).To(Equal(1))
		Expect(store.snap().Ejected).To(BeEmpty())
		Expect(store.snap().Queued).To(HaveLen(1))
	})

	It("A new main allows one more auto-resume", func() {
		d := held()
		main = "bbbbbbbb22"
		step(d, time.Second)
		Expect(resumes()).To(Equal(2))
		step(d, cooldown)
		Expect(resumes()).To(Equal(2), "the second pause on the new main is held")
		Expect(store.snap().Why).To(ContainSubstring("twice on main bbbbbbbb"))
	})

	It("A restart keeps the auto-resume count for the main", func() {
		pausedTwice()
		step(driver(), cooldown) // a new process takes over the second pause
		Expect(resumes()).To(Equal(1))
		Expect(store.snap().Why).To(ContainSubstring("no verdict twice"))
	})

	It("A manual resume clears the hold", func() {
		d := held()
		Expect(d.Resume(ctx, store.snap().PauseSeq)).To(Succeed())
		Expect(store.snap().ResumedOnMain).To(BeEmpty())
		step(d, 0)
		Expect(store.snap().Paused).To(BeTrue())
		step(d, cooldown)
		Expect(resumes()).To(Equal(3), "the manual resume and one more auto-resume")
	})

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

	// saved5f4493403 is a no-verdict pause exactly as the commit before auto-resume wrote it:
	// no PausedAt and no PauseNone, only the reason and the pause's settle record.
	const saved5f4493403 = `{"Version":"0","Queued":[{"ID":"a","Commit":"ca","Ref":"","AdmittedAt":"2026-10-04T08:50:00Z"}],
		"BuildsOn":{},"Landed":{},"Ejected":{},"InFlight":null,"Paused":true,"PauseSeq":1,"Why":"no verdict after 1 retries",
		"Landing":null,"Fence":4294967297,"Refused":null,"Commits":{"a":"ca"},
		"Settled":[{"ID":"a","Commit":"ca","Kind":"paused","At":"2026-10-04T09:00:00Z","AdmittedAt":"2026-10-04T08:50:00Z",
		"Why":"no verdict after 1 retries","Cause":"","Run":"r","Batch":["a"]}]}`

	It("A no-verdict pause saved before auto-resume existed still resumes after the cool-down", func() {
		store.data = []byte(saved5f4493403)
		d := driver()
		step(d, cooldown-time.Second) // the clock is the pause's own time plus this
		Expect(note.of(core.ResumedEvent)).To(BeEmpty())
		step(d, time.Second)
		Expect(note.of(core.ResumedEvent)).To(HaveLen(1))
	})

	It("A queue paused for no verdict resumes after a restart", func() {
		d := driver()
		Expect(d.Admit(ctx, core.Entry{ID: "a", Commit: "ca"})).To(Succeed())
		step(d, 0)
		step(driver(), cooldown) // a new process takes over the saved pause
		Expect(note.of(core.ResumedEvent)).To(HaveLen(1))
	})

	It("A cool-down of zero never resumes the queue", func() {
		d := driver()
		d.Cooldown = 0
		Expect(d.Admit(ctx, core.Entry{ID: "a", Commit: "ca"})).To(Succeed())
		step(d, 0)
		step(d, 1000*time.Hour)
		Expect(store.snap().Paused).To(BeTrue())
	})

	It("A manual resume is saved as a settle record with its reason", func() {
		d := pausedTwice()
		Expect(d.Resume(ctx, 2)).To(Succeed())
		last := store.snap().Settled
		Expect(last[len(last)-1].Kind).To(Equal(core.ResumedEvent))
		Expect(last[len(last)-1].Why).To(Equal("resume requested"))
	})
})

// headLander is a Lander that also reports the sha main is at.
type headLander struct {
	*memLander
	sha *string
}

func (h headLander) Head(context.Context, string) (string, error) { return *h.sha, nil }
