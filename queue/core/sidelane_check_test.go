package core_test

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

// side runs pick(v) alone and, on a red, ejects only its last entry, the half
// under test. It keeps no state, so a rebuilt one records an earlier run.
type side struct {
	n    *int
	pick func(v core.View) []core.Entry
}

func (s side) Plan(v core.View) ([]core.Run, []core.Settle) {
	if len(v.InFlight) > 0 || len(v.Queued) == 0 {
		return nil, nil
	}
	*s.n++
	return []core.Run{{ID: fmt.Sprintf("%sside-%d", v.Prefix, *s.n), Entries: s.pick(v)}}, nil
}

func (s side) Record(v core.View, id string, verdict core.Verdict) (core.Outcome, error) {
	for _, r := range v.InFlight {
		if r.ID != id {
			continue
		}
		if verdict == core.Pass {
			return core.Outcome{Settle: []core.Settle{{Entries: r.Entries, Decision: core.Land}}}, nil
		}
		half := r.Entries[len(r.Entries)-1:]
		return core.Outcome{Settle: []core.Settle{{Entries: half, Decision: core.Eject, Cause: core.Culprit}}}, nil
	}
	return core.Outcome{}, fmt.Errorf("unknown run %q", id)
}

// WouldLandBefore: the queued entries ahead of es, in queue order.
func (s side) WouldLandBefore(v core.View, es []core.Entry) []string {
	var out []string
	for _, e := range v.Queued {
		if e.ID == es[0].ID {
			break
		}
		out = append(out, e.ID)
	}
	return out
}

// hookRunner counts polls, calls on at each, and is not done while hold.
type hookRunner struct {
	*memRunner
	polls map[string]int
	on    func()
	hold  bool
}

func (r *hookRunner) Poll(ctx context.Context, id string) (core.Verdict, bool, error) {
	r.polls[id]++
	if r.on != nil {
		r.on()
	}
	if r.hold {
		return "", false, nil
	}
	return r.memRunner.Poll(ctx, id)
}

var _ = Describe("Side lane: the check just before an eject", func() {
	var (
		ctx   context.Context
		store *memStore
		comp  *memComposer
		run   *hookRunner
		land  *headLander
		n     int
	)
	BeforeEach(func() {
		ctx, store, comp, n = context.Background(), &memStore{}, &memComposer{}, 0
		run = &hookRunner{memRunner: &memRunner{verdict: failsWith("a", "c")}, polls: map[string]int{}}
		land = &headLander{memLander: &memLander{c: comp}, head: "m0"}
	})
	all := func(v core.View) []core.Entry { return v.Queued }
	lastFirst := func(v core.View) []core.Entry { // the last alone the first time, then all
		if n == 1 {
			return v.Queued[len(v.Queued)-1:]
		}
		return v.Queued
	}
	driver := func(pick func(core.View) []core.Entry) *core.Driver {
		return &core.Driver{Store: store, Composer: comp, Runner: run, Lander: land, Notifier: &memNotifier{},
			NewStrategy: func() core.Strategy { return side{&n, pick} }, Main: "core"}
	}
	admit := func(d *core.Driver, id ...string) {
		for _, x := range id {
			Expect(d.Admit(ctx, entry(x))).To(Succeed())
		}
	}
	steps := func(d *core.Driver, k int) {
		for range k {
			Expect(d.Step(ctx)).To(Succeed())
		}
	}
	recomposed := func() []core.SettleRecord {
		var out []core.SettleRecord
		for _, r := range store.snap().Settled {
			if r.Kind == core.RecomposeEvent {
				out = append(out, r)
			}
		}
		return out
	}

	It("A red whose main moves just before the eject is recomposed, not ejected", func() {
		d := driver(all)
		admit(d, "a")
		steps(d, 1)
		run.on = func() { land.head = "m1" } // pushed outside the queue after the Step read main
		steps(d, 1)
		Expect(store.snap().Ejected).To(BeEmpty())
		rec := recomposed()
		Expect(rec).To(HaveLen(1))
		Expect(rec[0].Why).To(Equal("main moved m0 to m1"))
		run.on = nil
		steps(d, 2)
		Expect(store.snap().Ejected).To(Equal(map[string]bool{"a": true}))
		Expect(recomposed()).To(HaveLen(1))
	})

	It("A green whose main moves just before the land is recomposed, not landed", func() {
		run.verdict = nil
		d := driver(all)
		admit(d, "a")
		steps(d, 1)
		run.on = func() { land.head = "m1" } // pushed outside the queue after the Step read main
		steps(d, 1)
		Expect(land.landed).To(BeEmpty())
		rec := recomposed()
		Expect(rec).To(HaveLen(1))
		Expect(rec[0].Why).To(Equal("main moved m0 to m1"))
		run.on = nil
		steps(d, 2)
		Expect(land.landed).To(Equal([]string{"a"}))
	})

	It("A red tested without a change that would land before it is recomposed, not ejected", func() {
		d := driver(lastFirst)
		admit(d, "a", "c")
		run.verdict = failsWith("c")
		steps(d, 2)
		Expect(store.snap().Ejected).To(BeEmpty())
		rec := recomposed()
		Expect(rec).To(HaveLen(1))
		Expect(rec[0].ID).To(Equal("c"))
		Expect(rec[0].Why).To(Equal("tested with [] ahead, but [a] would land before it now"))
		steps(d, 2)
		Expect(store.snap().Ejected).To(Equal(map[string]bool{"c": true}))
		Expect(run.starts[1]).To(Equal([]string{"a", "c"}))
	})

	It("A red tested on current main with exactly the changes ahead of it is ejected", func() {
		d := driver(all)
		admit(d, "a", "c")
		run.verdict = failsWith("c")
		steps(d, 2)
		Expect(store.snap().Ejected).To(Equal(map[string]bool{"c": true}))
		Expect(recomposed()).To(BeEmpty())
	})

	It("After a restart the result is read again and checked before any eject", func() {
		d := driver(lastFirst)
		admit(d, "a", "c")
		run.verdict, run.hold = failsWith("c"), true
		steps(d, 2)
		id := store.snap().InFlight[0].Run.ID
		polled := run.polls[id]
		run.hold = false
		steps(driver(lastFirst), 1) // a new process over the same store: nothing held in memory
		Expect(run.polls[id]).To(Equal(polled + 1))
		Expect(store.snap().Ejected).To(BeEmpty())
		Expect(recomposed()).To(HaveLen(1))
	})

	It("A recompose of several changes is one settle record", func() {
		d := driver(all)
		admit(d, "a", "b")
		steps(d, 1)
		land.head = "m1"
		steps(d, 1)
		rec := recomposed()
		Expect(rec).To(HaveLen(1))
		Expect(rec[0].Batch).To(Equal([]string{"a", "b"}))
		Expect(rec[0].Base).To(Equal("m0"))
	})
})
