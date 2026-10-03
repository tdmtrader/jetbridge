package core_test

import (
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

// pointAt is a Suspects that names fixed IDs and remembers what it was asked.
type pointAt struct {
	ids     []string
	calls   int
	failed  []string
	changed map[string][]string
}

func (p *pointAt) Rank(failed []string, _ []core.Entry, changed map[string][]string) []string {
	p.calls, p.failed, p.changed = p.calls+1, failed, changed
	return p.ids
}

var _ = Describe("Suspects", func() {
	var q *core.Queue
	policy := core.Policy{RetryNone: 1}
	failed := []string{"TestC"}
	changed := map[string][]string{"a": {"a.go"}, "b": {"b.go"}, "c": {"c.go"}, "d": {"d.go"}}
	BeforeEach(func() { q = &core.Queue{} })
	admit := func(names ...string) []core.Entry {
		for _, n := range names {
			Expect(q.Admit(entry(n))).To(Succeed())
		}
		return q.SelectBatch(8)
	}
	hinted := func(s core.Suspects) *core.Bisect {
		b, err := core.NewSuspectBisect(admit("a", "b", "c", "d"), policy, s, failed, changed)
		Expect(err).NotTo(HaveOccurred())
		return b
	}
	broken := func(bad ...string) func([]string) core.Verdict {
		return func(sub []string) core.Verdict {
			for _, id := range bad {
				if slices.Contains(sub, id) {
					return core.Fail
				}
			}
			return core.Pass
		}
	}
	run := func(b *core.Bisect, verdict func([]string) core.Verdict) [][]string {
		tried := [][]string{}
		for sub := b.Next(); sub != nil; sub = b.Next() {
			tried = append(tried, ids(sub))
			d, err := b.Record(verdict(ids(sub)))
			Expect(err).NotTo(HaveOccurred())
			Expect(core.Apply(q, d, sub)).To(Succeed())
		}
		return tried
	}
	states := func(names ...string) []core.State {
		out := []core.State{}
		for _, n := range names {
			out = append(out, refusedAs(q.Admit(entry(n))))
		}
		return out
	}
	landed := []core.State{core.Landed, core.Landed, core.Landed}

	It("A correct hint ejects after one solo run without bisecting", func() {
		s := &pointAt{ids: []string{"c", "a"}}
		b := hinted(s)
		Expect(run(b, broken("c"))).To(Equal([][]string{{"c"}, {"a", "b", "d"}}))
		Expect(states("c")).To(Equal([]core.State{core.Ejected}))
		Expect(states("a", "b", "d")).To(Equal(landed))
		Expect([]int{b.Hits, b.Misses, s.calls}).To(Equal([]int{1, 0, 1}))
		Expect(s.failed).To(Equal(failed))
		Expect(s.changed).To(Equal(changed))
		Expect(b.Flakes).To(BeEmpty())
	})

	It("A wrong hint falls back to bisect and the innocent change lands", func() {
		b := hinted(&pointAt{ids: []string{"a"}})
		Expect(run(b, broken("c"))).To(Equal([][]string{{"a"}, {"a", "b"}, {"c", "d"}, {"c"}, {"d"}}))
		Expect(states("c")).To(Equal([]core.State{core.Ejected}))
		Expect(states("a", "b", "d")).To(Equal(landed))
		Expect([]int{b.Hits, b.Misses}).To(Equal([]int{0, 1}))
	})

	It("bisects a red rest with no further hint", func() {
		s := &pointAt{ids: []string{"c"}}
		b := hinted(s)
		Expect(run(b, broken("a", "c"))).To(Equal([][]string{{"c"}, {"a", "b", "d"}, {"a"}, {"b", "d"}}))
		Expect(states("a", "c")).To(Equal([]core.State{core.Ejected, core.Ejected}))
		Expect(states("b", "d")).To(Equal(landed[:2]))
		Expect([]int{b.Hits, b.Misses, s.calls}).To(Equal([]int{1, 0, 1}))
	})

	It("retries then pauses on no verdict for the solo run, and never ejects", func() {
		b := hinted(&pointAt{ids: []string{"c"}})
		Expect(run(b, func([]string) core.Verdict { return core.None })).To(Equal([][]string{{"c"}, {"c"}}))
		Expect(b.Paused).To(BeTrue())
		Expect([]int{b.Hits, b.Misses}).To(Equal([]int{0, 0}))
		Expect(ids(q.SelectBatch(8))).To(Equal([]string{"a", "b", "c", "d"}))
	})

	It("bisects plainly with no suspects or a suspect outside the batch", func() {
		for _, s := range []core.Suspects{core.NoSuspects{}, &pointAt{ids: []string{"x"}}} {
			q = &core.Queue{}
			b := hinted(s)
			Expect(run(b, broken("c"))).To(Equal([][]string{{"a", "b"}, {"c", "d"}, {"c"}, {"d"}}))
			Expect([]int{b.Hits, b.Misses}).To(Equal([]int{0, 0}))
		}
		_, err := core.NewSuspectBisect(admit("e"), policy, core.NoSuspects{}, nil, nil)
		Expect(err).To(HaveOccurred())
	})
})
