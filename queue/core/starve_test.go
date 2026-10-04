package core_test

import (
	"context"
	"fmt"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

// A red batch's bisect owns the line: fresh admits queue behind it and never
// compose a batch, however many arrive and however many slots are free.
var _ = Describe("Bisect starvation", func() {
	fresh := func(run []string) bool {
		return slices.ContainsFunc(run, func(id string) bool { return strings.HasPrefix(id, "e") })
	}

	It("Fresh admits never run ahead of a pending bisect half, and the culprit is ejected within a bound", func() {
		ctx, store, comp, run, note := context.Background(), &memStore{}, &memComposer{}, &memRunner{}, &memNotifier{}
		land := &memLander{c: comp}
		run.verdict = failsWith("b")
		d := &core.Driver{
			Store: store, Composer: comp, Runner: run, Lander: land, Notifier: note, Main: "core",
			NewStrategy: func() core.Strategy { return &core.Serial{Max: 4, Policy: core.Policy{RetryNone: 1}} },
		}
		var freshStart []string
		run.onStart = func(es []string) {
			if !fresh(es) || freshStart != nil {
				return
			}
			freshStart = es
			snap := store.snap()
			for _, old := range []string{"a", "b", "c", "d"} {
				Expect(snap.Queued).NotTo(ContainElement(HaveField("ID", old)), "%s still unsettled when a fresh batch %v was planned", old, es)
			}
		}
		for _, id := range []string{"a", "b", "c", "d"} {
			Expect(d.Admit(ctx, entry(id))).To(Succeed())
		}
		runsAtEject := 0
		for i := 1; i <= 10; i++ {
			Expect(d.Admit(ctx, entry(fmt.Sprintf("e%d", i)))).To(Succeed())
			Expect(d.Step(ctx)).To(Succeed())
			if runsAtEject == 0 && len(note.of(core.EjectedEvent)) > 0 {
				runsAtEject = len(run.starts)
			}
		}
		for range 10 {
			Expect(d.Step(ctx)).To(Succeed())
		}

		Expect(run.starts[0]).To(Equal([]string{"a", "b", "c", "d"}))
		Expect(run.starts[1:5]).To(Equal([][]string{{"a", "b"}, {"a"}, {"b"}, {"c", "d"}}))
		Expect(freshStart).NotTo(BeNil(), "fresh admits must run once the bisect is done")
		for _, r := range run.starts[:5] {
			Expect(fresh(r)).To(BeFalse(), "fresh admit in %v ran ahead of the bisect", r)
		}
		Expect(runsAtEject).To(BeNumerically(">", 0))
		Expect(runsAtEject).To(BeNumerically("<=", 2*2+2), "culprit b ejected after %d runs", runsAtEject)
		ej := note.of(core.EjectedEvent)
		Expect(ids(ej[0].Entries)).To(Equal([]string{"b"}))
		Expect(land.landed).To(ContainElements("a", "c", "d", "e1", "e10"))
	})

	It("Spare slots never run a fresh batch beside or ahead of a bisect half", func() {
		// The driver offers Slots-len(InFlight) spare slots (driver.go view); Serial plans nothing while anything is in flight or its bisect has halves left.
		s := &core.Serial{Max: 4, Policy: core.Policy{RetryNone: 1}}
		queued := ents("a", "b", "c", "d")
		v := core.View{Queued: queued, Landed: map[string]bool{}, Ejected: map[string]bool{}, Slots: 3, Prefix: "p-"}
		planned, _ := s.Plan(v)
		Expect(planned).To(HaveLen(1))
		first := planned[0]

		// While the red run is in flight, spare slots and fresh admits plan nothing.
		v.Queued = append(slices.Clone(queued), ents("e1", "e2", "e3", "e4", "e5")...)
		v.InFlight, v.Slots = planned, 2
		more, _ := s.Plan(v)
		Expect(more).To(BeEmpty())

		// Red: the next plan is the first half only, with two spare slots and fresh admits waiting.
		_, err := s.Record(v, first.ID, core.Fail)
		Expect(err).NotTo(HaveOccurred())
		v.InFlight, v.Slots = nil, 3
		half, _ := s.Plan(v)
		Expect(half).To(HaveLen(1))
		Expect(ids(half[0].Entries)).To(Equal([]string{"a", "b"}))
		Expect(half[0].Base).To(BeEmpty(), "a half composes on main, never on an unresolved red")
		v.InFlight, v.Slots = half, 2
		again, _ := s.Plan(v)
		Expect(again).To(BeEmpty())
	})

	It("A driver with several slots still runs one bisect half at a time", func() {
		ctx, store, comp, run, note := context.Background(), &memStore{}, &memComposer{}, &memRunner{}, &memNotifier{}
		land := &memLander{c: comp}
		run.verdict = failsWith("b")
		d := &core.Driver{
			Store: store, Composer: comp, Runner: run, Lander: land, Notifier: note, Main: "core", Slots: 4,
			NewStrategy: func() core.Strategy { return &core.Serial{Max: 4, Policy: core.Policy{RetryNone: 1}} },
		}
		for _, id := range []string{"a", "b", "c", "d"} {
			Expect(d.Admit(ctx, entry(id))).To(Succeed())
		}
		for i := 1; i <= 10; i++ {
			Expect(d.Admit(ctx, entry(fmt.Sprintf("e%d", i)))).To(Succeed())
			Expect(d.Step(ctx)).To(Succeed())
			Expect(len(store.snap().InFlight)).To(BeNumerically("<=", 1))
		}
		for _, r := range run.starts[:5] {
			Expect(fresh(r)).To(BeFalse())
		}
	})
})
