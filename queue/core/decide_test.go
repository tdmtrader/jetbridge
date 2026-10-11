package core_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

var _ = Describe("Decide and Apply", func() {
	var q *core.Queue
	policy := core.Policy{RetryNone: 1}
	BeforeEach(func() { q = &core.Queue{} })
	admit := func(names ...string) []core.Entry {
		for _, n := range names {
			Expect(q.Admit(entry(n))).To(Succeed())
		}
		return q.SelectBatch(8)
	}
	decide := func(v core.Verdict, batch []core.Entry, retries int) core.Decision {
		d, err := core.Decide(v, batch, retries, policy)
		Expect(err).NotTo(HaveOccurred())
		return d
	}

	It("A green batch lands", func() {
		batch := admit("a", "b")
		d := decide(core.Pass, batch, 0)
		Expect(d).To(Equal(core.Land))
		Expect(settle(q, d, batch)).To(Succeed())
		Expect(refusedAs(q.Admit(entry("a")))).To(Equal(core.Landed))
		Expect(refusedAs(q.Admit(entry("b")))).To(Equal(core.Landed))
		Expect(q.SelectBatch(8)).To(BeEmpty())
	})

	It("No verdict retries and never ejects", func() {
		batch := admit("a")
		Expect(decide(core.None, batch, 0)).To(Equal(core.Retry))
		d := decide(core.None, batch, 1)
		Expect(d).To(Equal(core.Pause))
		Expect(core.Apply(q, d, batch)).To(Succeed())
		Expect(ids(q.SelectBatch(8))).To(Equal([]string{"a"}))
	})

	It("never ejects on no verdict, at any size or retry count", func() {
		for _, batch := range [][]core.Entry{admit("a"), admit("b")} {
			for retries := range 5 {
				Expect(decide(core.None, batch, retries)).NotTo(Equal(core.Eject))
			}
		}
	})

	It("ejects a red batch of one and splits a larger red batch", func() {
		one := admit("a")
		d := decide(core.Fail, one, 0)
		Expect(d).To(Equal(core.Eject))
		Expect(core.Apply(q, d, one)).To(Succeed())
		Expect(refusedAs(q.Admit(entry("a")))).To(Equal(core.Ejected))

		two := admit("b", "c")
		d = decide(core.Fail, two, 0)
		Expect(d).To(Equal(core.Split))
		Expect(core.Apply(q, d, two)).To(Succeed())
		Expect(ids(q.SelectBatch(8))).To(Equal([]string{"b", "c"}))
	})

	It("refuses an unknown verdict or an empty batch", func() {
		_, err := core.Decide("maybe", admit("a"), 0, policy)
		Expect(err).To(HaveOccurred())
		_, err = core.Decide(core.Pass, nil, 0, policy)
		Expect(err).To(HaveOccurred())
	})
})
