package core_test

import (
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

func entry(id string) core.Entry {
	return core.Entry{ID: id, Commit: "sha-" + id, AdmittedAt: time.Unix(0, 0)}
}

func ids(batch []core.Entry) []string {
	out := []string{}
	for _, e := range batch {
		out = append(out, e.ID)
	}
	return out
}

func refusedAs(err error) core.State {
	var refused *core.RefusedError
	Expect(errors.As(err, &refused)).To(BeTrue(), "want *core.RefusedError, got %v", err)
	return refused.State
}

var _ = Describe("Admit and SelectBatch", func() {
	var q *core.Queue
	admit := func(names ...string) {
		for _, n := range names {
			Expect(q.Admit(entry(n))).To(Succeed())
		}
	}
	BeforeEach(func() { q = &core.Queue{} })

	It("Two admitted changes form one batch in order", func() {
		admit("a", "b")
		Expect(ids(q.SelectBatch(8))).To(Equal([]string{"a", "b"}))
	})

	It("An ejected change is never batched", func() {
		admit("a", "b", "c")
		Expect(q.Settle("b", core.Ejected)).To(Succeed())
		Expect(ids(q.SelectBatch(8))).To(Equal([]string{"a", "c"}))
		Expect(refusedAs(q.Admit(entry("b")))).To(Equal(core.Ejected))
	})

	It("Admitting the main branch itself is refused", func() {
		q.Protected = []string{"trunk"}
		for _, ref := range []string{"trunk", "main", "master"} {
			e := entry("m")
			e.Ref = ref
			var refused *core.MainRefError
			Expect(errors.As(q.Admit(e), &refused)).To(BeTrue(), ref)
			Expect(refused.Ref).To(Equal(ref))
		}
		e := entry("f")
		e.Ref = "feature"
		Expect(q.Admit(e)).To(Succeed())
		Expect(ids(q.SelectBatch(8))).To(Equal([]string{"f"}))
	})

	It("takes only the first max queued entries", func() {
		admit("a", "b", "c")
		Expect(ids(q.SelectBatch(2))).To(Equal([]string{"a", "b"}))
	})

	It("refuses what an entry's state forbids", func() {
		admit("a", "b")
		Expect(q.Settle("a", core.Landed)).To(Succeed())
		Expect(refusedAs(q.Admit(entry("a")))).To(Equal(core.Landed))
		Expect(refusedAs(q.Admit(entry("b")))).To(Equal(core.Queued))
		Expect(refusedAs(q.Settle("missing", core.Ejected))).To(BeEmpty())
		Expect(refusedAs(q.Settle("b", core.Queued))).To(Equal(core.Queued))
		Expect(ids(q.SelectBatch(8))).To(Equal([]string{"b"}))
	})
})
