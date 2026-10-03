package core_test

import (
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

var _ = Describe("Bisect", func() {
	var q *core.Queue
	policy := core.Policy{RetryNone: 1}
	BeforeEach(func() { q = &core.Queue{} })
	admit := func(names ...string) []core.Entry {
		for _, n := range names {
			Expect(q.Admit(entry(n))).To(Succeed())
		}
		return q.SelectBatch(8)
	}
	// run drives a bisect to its end with verdicts from a table and returns
	// the sub-batches it tried, in order.
	run := func(b *core.Bisect, verdict func(sub []string) core.Verdict) [][]string {
		tried := [][]string{}
		for sub := b.Next(); sub != nil; sub = b.Next() {
			tried = append(tried, ids(sub))
			d, err := b.Record(verdict(ids(sub)))
			Expect(err).NotTo(HaveOccurred())
			Expect(core.Apply(q, d, sub)).To(Succeed())
		}
		return tried
	}
	state := func(id string) core.State { return refusedAs(q.Admit(entry(id))) }

	It("One bad change in four is ejected, three land", func() {
		b, err := core.NewBisect(admit("a", "b", "c", "d"), policy)
		Expect(err).NotTo(HaveOccurred())
		tried := run(b, func(sub []string) core.Verdict {
			if slices.Contains(sub, "c") {
				return core.Fail
			}
			return core.Pass
		})
		Expect(tried).To(Equal([][]string{{"a", "b"}, {"c", "d"}, {"c"}, {"d"}}))
		Expect(state("c")).To(Equal(core.Ejected))
		for _, id := range []string{"a", "b", "d"} {
			Expect(state(id)).To(Equal(core.Landed))
		}
		Expect(b.Flakes).To(BeEmpty())
	})

	It("Halves both pass so the batch lands and a flake is recorded", func() {
		batch := admit("a", "b", "c", "d")
		b, err := core.NewBisect(batch, policy)
		Expect(err).NotTo(HaveOccurred())
		tried := run(b, func([]string) core.Verdict { return core.Pass })
		Expect(tried).To(Equal([][]string{{"a", "b"}, {"c", "d"}}))
		for _, id := range []string{"a", "b", "c", "d"} {
			Expect(state(id)).To(Equal(core.Landed))
		}
		Expect(b.Flakes).To(Equal([][]core.Entry{batch}))
	})

	It("records a flake on a red half whose own halves both pass", func() {
		b, _ := core.NewBisect(admit("a", "b", "c", "d"), policy)
		red := map[string]bool{"a b": true, "c d": false}
		run(b, func(sub []string) core.Verdict {
			if len(sub) == 2 && red[sub[0]+" "+sub[1]] {
				return core.Fail
			}
			return core.Pass
		})
		Expect(b.Flakes).To(HaveLen(1))
		Expect(ids(b.Flakes[0])).To(Equal([]string{"a", "b"}))
		Expect(q.SelectBatch(8)).To(BeEmpty())
	})

	It("retries then pauses on no verdict for a half, and never ejects", func() {
		b, _ := core.NewBisect(admit("a", "b", "c", "d"), policy)
		tried := run(b, func([]string) core.Verdict { return core.None })
		Expect(tried).To(Equal([][]string{{"a", "b"}, {"a", "b"}}))
		Expect(b.Paused).To(BeTrue())
		Expect(ids(q.SelectBatch(8))).To(Equal([]string{"a", "b", "c", "d"}))
	})

	It("refuses a batch of one and a verdict once done", func() {
		_, err := core.NewBisect(admit("a"), policy)
		Expect(err).To(HaveOccurred())
		q = &core.Queue{}
		b, _ := core.NewBisect(admit("b", "c"), policy)
		run(b, func([]string) core.Verdict { return core.Pass })
		_, err = b.Record(core.Pass)
		Expect(err).To(HaveOccurred())
	})
})
