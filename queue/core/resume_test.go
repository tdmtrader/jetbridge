package core_test

import (
	"context"
	"errors"
	"slices"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

// memResumes holds the resume requests until Done removes one at its sha; it
// notes whether the saved queue was still paused when Done ran. A failing
// Done (failDone) leaves the request where it is.
type memResumes struct {
	reqs         []core.ResumeRequest
	done         int
	failDone     bool
	pausedAtDone func() bool
	stillPaused  bool
}

func (r *memResumes) Pending(context.Context) ([]core.ResumeRequest, error) {
	return slices.Clone(r.reqs), nil
}

func (r *memResumes) Done(_ context.Context, q core.ResumeRequest) error {
	if r.pausedAtDone != nil {
		r.stillPaused = r.pausedAtDone()
	}
	if r.done++; r.failDone {
		r.failDone = false
		return errors.New("push refused")
	}
	r.reqs = slices.DeleteFunc(r.reqs, func(x core.ResumeRequest) bool { return x == q })
	return nil
}

// stallStore runs stall once, on Acquire call number at, as if its driver stalled there.
type stallStore struct {
	*memStore
	at, n int
	stall func()
}

func (s *stallStore) Acquire(ctx context.Context, owner string, ttl time.Duration) (core.Lease, error) {
	if s.n++; s.n == s.at {
		s.stall()
	}
	return s.memStore.Acquire(ctx, owner, ttl)
}

// failCompose cannot compose anything, so a started run has no verdict.
type failCompose struct{}

func (failCompose) Compose(context.Context, string, []core.Entry) (string, error) {
	return "", errors.New("no space left")
}

var _ = Describe("Driver resume requests", func() {
	var (
		ctx   context.Context
		store *memStore
		res   *memResumes
		note  *memNotifier
	)
	BeforeEach(func() {
		ctx, store, note = context.Background(), &memStore{}, &memNotifier{}
		res = &memResumes{pausedAtDone: func() bool { return store.snap().Paused }}
	})
	driver := func() *core.Driver {
		comp := &memComposer{}
		return &core.Driver{
			Store: store, Composer: comp, Runner: &memRunner{}, Lander: &memLander{c: comp}, Notifier: note,
			NewStrategy: func() core.Strategy { return idle{} }, Main: "core", Owner: "runner",
			Resumes: res, Log: func(string, ...any) {},
		}
	}
	// serial pauses on its first run: no verdict, and no retry.
	serial := func(c core.Composer, r core.Runner) *core.Driver {
		d := driver()
		d.Composer, d.Runner = c, r
		d.NewStrategy = func() core.Strategy { return &core.Serial{Max: 1} }
		return d
	}
	ask := func() { res.reqs = append(res.reqs, core.ResumeRequest{Seq: store.snap().PauseSeq, SHA: "sha-main"}) }

	It("An operator resumes a paused queue while the runner is live", func() {
		_, err := store.Save(ctx, 0, core.Snapshot{Version: "0", Paused: true, PauseSeq: 1, Why: "no verdict after 1 retries"})
		Expect(err).NotTo(HaveOccurred())
		d := driver()
		Expect(d.Step(ctx)).To(Succeed())
		Expect(store.snap().Paused).To(BeTrue(), "no request yet")
		ask()
		Expect(d.Step(ctx)).To(Succeed())
		Expect(store.snap().Paused).To(BeFalse())
		Expect(note.of(core.ResumedEvent)).To(HaveLen(1))
		Expect(res.reqs).To(BeEmpty(), "the request is deleted")
		Expect(res.stillPaused).To(BeFalse(), "deleted only after the save")
	})

	It("A refused resume request is recorded and deleted and does not resume the queue", func() {
		_, err := store.Save(ctx, 0, core.Snapshot{Version: "0", Paused: true, PauseSeq: 1, Why: "no verdict after 1 retries"})
		Expect(err).NotTo(HaveOccurred())
		res.reqs = append(res.reqs, core.ResumeRequest{Seq: 1, SHA: "sha-main", Why: "resume request sha-mai is refused by its source"})
		Expect(driver().Step(ctx)).To(Succeed())
		Expect(store.snap().Paused).To(BeTrue())
		Expect(note.of(core.ResumedEvent)).To(BeEmpty())
		Expect(note.of(core.RefusedEvent)).To(HaveLen(1))
		Expect(store.snap().Refused).To(HaveLen(1))
		Expect(store.snap().Refused[0].Why).To(ContainSubstring("refused by its source"))
		Expect(res.reqs).To(BeEmpty(), "the request is deleted")
	})

	It("A resume request on a queue that is not paused is deleted and changes nothing", func() {
		d := driver()
		Expect(d.Step(ctx)).To(Succeed())
		before := store.snap()
		ask()
		Expect(d.Step(ctx)).To(Succeed())
		Expect(res.reqs).To(BeEmpty())
		Expect(note.of(core.ResumedEvent)).To(BeEmpty())
		after := store.snap()
		after.Version = before.Version // an idle step saves, so only the version moves
		Expect(after).To(Equal(before))
	})

	It("A resume request for an earlier pause does not clear a newer one", func() {
		runner := &memRunner{}
		runner.verdict = func([]string) core.Verdict {
			ask() // the operator resumes after this step looked, while the queue is not paused
			return core.None
		}
		d := serial(&memComposer{}, runner)
		Expect(d.Admit(ctx, core.Entry{ID: "a", Commit: "ca"})).To(Succeed())
		Expect(d.Step(ctx)).To(Succeed()) // starts the run
		Expect(d.Step(ctx)).To(Succeed()) // no verdict: pauses, after the request was made
		Expect(store.snap().Paused).To(BeTrue())
		Expect(d.Step(ctx)).To(Succeed())
		Expect(store.snap().Paused).To(BeTrue(), "the request was for no pause yet")
		Expect(note.of(core.ResumedEvent)).To(BeEmpty())
		Expect(res.reqs).To(BeEmpty(), "the stale request is deleted")
	})

	It("A resume checked against one pause never clears the next, however long its driver stalls", func() {
		_, err := store.Save(ctx, 0, core.Snapshot{Version: "0", Paused: true, PauseSeq: 1, Why: "no verdict", Queued: []core.Entry{{ID: "a", Commit: "ca"}}})
		Expect(err).NotTo(HaveOccurred())
		res.reqs = []core.ResumeRequest{{Seq: 1, SHA: "sha-main"}}
		b := serial(failCompose{}, &memRunner{})
		b.Owner = "B"
		stalled := false
		a := driver()
		a.Owner, a.Store = "A", &stallStore{memStore: store, at: 2, stall: func() {
			stalled = true
			store.now = store.now.Add(2 * time.Minute) // A's lease runs out while it stalls
			Expect(b.Step(ctx)).To(Succeed())          // B ends pause 1, then pauses again
			Expect(store.snap().PauseSeq).To(BeEquivalentTo(2))
			store.now = store.now.Add(2 * time.Minute)
		}}
		Expect(a.Step(ctx)).To(Succeed())
		Expect(stalled).To(BeTrue(), "A stalled between its check and its resume")
		Expect(store.snap().Paused).To(BeTrue(), "pause 2 is not cleared by the request for pause 1")
		Expect(store.snap().PauseSeq).To(BeEquivalentTo(2))
		Expect(note.of(core.ResumedEvent)).To(HaveLen(1))
	})

	It("A resume request whose delete failed is never applied to a later pause", func() {
		_, err := store.Save(ctx, 0, core.Snapshot{Version: "0", Paused: true, PauseSeq: 1, Why: "no verdict", Queued: []core.Entry{{ID: "a", Commit: "ca"}}})
		Expect(err).NotTo(HaveOccurred())
		d := serial(failCompose{}, &memRunner{})
		res.reqs, res.failDone = []core.ResumeRequest{{Seq: 1, SHA: "sha-main"}}, true
		Expect(d.Step(ctx)).To(Succeed()) // resumes, then the run it starts has no verdict: paused again
		Expect(note.of(core.ResumedEvent)).To(HaveLen(1))
		Expect(store.snap().Paused).To(BeTrue())
		Expect(res.reqs).To(HaveLen(1), "the request could not be deleted")
		Expect(d.Step(ctx)).To(Succeed())
		Expect(store.snap().Paused).To(BeTrue(), "the same request is not applied twice")
		Expect(note.of(core.ResumedEvent)).To(HaveLen(1))
		Expect(res.reqs).To(BeEmpty())
	})
})
