package core_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

var _ = Describe("Entry owner", func() {
	It("An admitted change keeps its owner through the eject record and the notice", func() {
		ctx, store, comp, note, adm := context.Background(), &memStore{}, &memComposer{}, &memNotifier{}, &memAdmissions{}
		run := &memRunner{verdict: failsWith("b")}
		d := &core.Driver{
			Store: store, Composer: comp, Runner: run, Lander: &memLander{c: comp}, Notifier: note, Admissions: adm,
			NewStrategy: func() core.Strategy { return &core.Serial{Max: 1, Policy: core.Policy{RetryNone: 1}} },
			Main:        "core", Owner: "runner", Log: func(string, ...any) {},
		}
		adm.push(core.Pending{ID: "b", Commit: "sha-b", Owner: "alice"})
		for range 12 {
			Expect(d.Step(ctx)).To(Succeed())
		}
		ej := note.of(core.EjectedEvent)
		Expect(ej).To(HaveLen(1))
		Expect(ej[0].Entries[0].Owner).To(Equal("alice"), "the notice names who to tell")
		rec := store.snap().Settled
		Expect(rec).To(HaveLen(1))
		Expect(rec[0].Owner).To(Equal("alice"))
	})
})
