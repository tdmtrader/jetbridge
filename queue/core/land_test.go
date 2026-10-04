package core_test

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

// fakeLander records each call and the queue as it was during the call.
type fakeLander struct {
	q       *core.Queue
	err     error
	calls   [][2]string
	waiting []string
	holds   bool
}

func (l *fakeLander) Contains(context.Context, string, string, uint64) (bool, error) {
	return l.holds, l.err
}

func (l *fakeLander) Land(_ context.Context, main, candidate string, _ uint64) error {
	l.calls = append(l.calls, [2]string{main, candidate})
	l.waiting = ids(l.q.SelectBatch(8))
	return l.err
}

var _ = Describe("LandBatch", func() {
	var q *core.Queue
	BeforeEach(func() { q = &core.Queue{} })
	admit := func(names ...string) []core.Entry {
		for _, n := range names {
			Expect(q.Admit(entry(n))).To(Succeed())
		}
		return q.SelectBatch(8)
	}

	It("A batch is marked landed only after main moves to it", func() {
		batch := admit("a", "b")
		l := &fakeLander{q: q}
		Expect(core.LandBatch(context.Background(), l, q, "main1", "cand1", 1, batch)).To(Succeed())
		Expect(l.calls).To(Equal([][2]string{{"main1", "cand1"}}))
		Expect(l.waiting).To(Equal([]string{"a", "b"}))
		Expect(refusedAs(q.Admit(entry("a")))).To(Equal(core.Landed))
		Expect(refusedAs(q.Admit(entry("b")))).To(Equal(core.Landed))
	})

	It("A batch whose landing fails stays queued", func() {
		batch := admit("a", "b")
		moved := errors.New("main moved")
		err := core.LandBatch(context.Background(), &fakeLander{q: q, err: moved}, q, "main1", "cand1", 1, batch)
		Expect(err).To(MatchError(moved))
		Expect(ids(q.SelectBatch(8))).To(Equal([]string{"a", "b"}))
	})

	It("settles a saved landing only if main holds its candidate", func() {
		batch := admit("a", "b")
		in := core.Landing{Main: "main1", Candidate: "cand1", Entries: batch}
		unreachable := errors.New("unreachable")
		for l, want := range map[*fakeLander]error{{err: unreachable}: unreachable, {}: nil} {
			landed, err := core.ReconcileLanding(context.Background(), l, q, in, 1)
			Expect(landed).To(BeFalse())
			Expect(errors.Is(err, want)).To(BeTrue())
			Expect(ids(q.SelectBatch(8))).To(Equal([]string{"a", "b"}))
		}
		landed, err := core.ReconcileLanding(context.Background(), &fakeLander{holds: true}, q, in, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(landed).To(BeTrue())
		Expect(refusedAs(q.Admit(entry("a")))).To(Equal(core.Landed))
	})

	It("refuses to settle a land through Apply", func() {
		batch := admit("a")
		Expect(core.Apply(q, core.Land, batch)).To(HaveOccurred())
		Expect(ids(q.SelectBatch(8))).To(Equal([]string{"a"}))
	})
})

// settle carries out a decision as a caller would: a land goes through a
// lander that succeeds, everything else through Apply.
func settle(q *core.Queue, d core.Decision, batch []core.Entry) error {
	if d == core.Land {
		return core.LandBatch(context.Background(), &fakeLander{q: q}, q, "main", "candidate", 1, batch)
	}
	return core.Apply(q, d, batch)
}
