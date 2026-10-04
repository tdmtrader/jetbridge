package core_test

import (
	"context"
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

var _ = Describe("The urgent lane", func() {
	var q *core.Queue
	form := func(buildsOn map[string][]string) []string {
		return ids(core.FormBatch(q.SelectBatch(8), buildsOn, nil).Entries())
	}
	BeforeEach(func() { q = &core.Queue{} })
	queue := func(names ...string) {
		for _, n := range names {
			Expect(q.Admit(entry(n))).To(Succeed())
		}
	}

	It("An urgent change goes to the front of the next batch", func() {
		queue("a", "b", "c")
		Expect(q.Promote("c")).To(Succeed())
		Expect(form(nil)).To(Equal([]string{"c", "a", "b"}))
	})

	It("Urgent changes keep their admission order among themselves", func() {
		queue("a", "b", "c", "d")
		Expect(q.Promote("d")).To(Succeed())
		Expect(q.Promote("b")).To(Succeed())
		Expect(form(nil)).To(Equal([]string{"b", "d", "a", "c"}))
	})

	It("An urgent change never skips the queued change it builds on", func() {
		queue("a", "b", "c")
		Expect(q.Promote("c")).To(Succeed())
		Expect(form(map[string][]string{"c": {"b"}})).To(Equal([]string{"b", "c", "a"}))
	})

	It("Only a queued change can be marked urgent", func() {
		queue("a")
		Expect(q.Settle("a", core.Ejected)).To(Succeed())
		Expect(refusedAs(q.Promote("a"))).To(Equal(core.Ejected))
		Expect(q.Promote("z")).To(HaveOccurred())
	})
})

type memPromotes struct{ reqs []core.PromoteRequest }

func (p *memPromotes) Pending(context.Context) ([]core.PromoteRequest, error) {
	return slices.Clone(p.reqs), nil
}

func (p *memPromotes) Done(_ context.Context, q core.PromoteRequest) error {
	p.reqs = slices.DeleteFunc(p.reqs, func(x core.PromoteRequest) bool { return x == q })
	return nil
}

var _ = Describe("Driver promote requests", func() {
	var (
		ctx   = context.Background()
		store *memStore
		pro   *memPromotes
		d     *core.Driver
	)
	BeforeEach(func() {
		store, pro = &memStore{}, &memPromotes{}
		comp := &memComposer{}
		d = &core.Driver{
			Store: store, Composer: comp, Runner: &memRunner{}, Lander: &memLander{c: comp}, Notifier: &memNotifier{},
			NewStrategy: func() core.Strategy { return idle{} }, Main: "core", Owner: "runner",
			Promotes: pro, Log: func(string, ...any) {},
		}
		for _, n := range []string{"a", "b", "c"} {
			Expect(d.Admit(ctx, entry(n))).To(Succeed())
		}
	})
	last := func() core.SettleRecord { s := store.snap().Settled; return s[len(s)-1] }

	It("Promoting a queued change puts it first in the next batch", func() {
		pro.reqs = []core.PromoteRequest{{ID: "c", SHA: "s"}}
		Expect(d.Step(ctx)).To(Succeed())
		q := store.snap().Queued
		Expect(core.FormBatch(q, nil, nil).Entries()[0].ID).To(Equal("c"))
		Expect(last().Why).To(Equal("promoted c to the urgent lane"))
		Expect(pro.reqs).To(BeEmpty())
	})

	It("Promoting a change that is not queued is refused and recorded", func() {
		pro.reqs = []core.PromoteRequest{{ID: "z", SHA: "s"}}
		Expect(d.Step(ctx)).To(Succeed())
		Expect(last().Kind).To(Equal(core.RefusedEvent))
		Expect(last().Why).To(ContainSubstring("promote of z refused"))
		Expect(pro.reqs).To(BeEmpty())
	})

	It("A promote request its source refuses, as unsigned, is recorded and promotes nothing", func() {
		pro.reqs = []core.PromoteRequest{{ID: "c", SHA: "s", Why: "promote request s is not signed by an operator"}}
		Expect(d.Step(ctx)).To(Succeed())
		Expect(core.FormBatch(store.snap().Queued, nil, nil).Entries()[0].ID).To(Equal("a"))
		Expect(last().Kind).To(Equal(core.RefusedEvent))
		Expect(last().Why).To(Equal("promote of c refused: promote request s is not signed by an operator"))
		Expect(pro.reqs).To(BeEmpty())
	})
})
