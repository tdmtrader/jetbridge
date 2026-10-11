package core_test

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

// headLander is a memLander that is also core.Heads: main is head, moved by
// each land or by the spec, as a push outside the queue would.
type headLander struct {
	*memLander
	head string
}

func (l *headLander) Head(context.Context, string) (string, error) { return l.head, nil }

func (l *headLander) Land(ctx context.Context, main, cand string, fence uint64) error {
	if err := l.memLander.Land(ctx, main, cand, fence); err != nil {
		return err
	}
	l.head = cand
	return nil
}

// slowRunner is a memRunner whose run led by an entry in wait is done only after that many polls.
type slowRunner struct {
	*memRunner
	wait map[string]int
}

func (r *slowRunner) Poll(ctx context.Context, id string) (core.Verdict, bool, error) {
	if es := r.started[id]; len(es) > 0 && r.wait[es[0]] > 0 {
		r.wait[es[0]]--
		return "", false, nil
	}
	return r.memRunner.Poll(ctx, id)
}

// ahead runs the first queued entry on main and the second composed ahead on
// that run; a pass lands a run's entries, anything else ejects them. It knows only its own runs.
type ahead struct {
	n     int
	runs  map[string][]core.Entry
	views []core.View
}

func (a *ahead) Plan(v core.View) ([]core.Run, []core.Settle) {
	a.views = append(a.views, v)
	if len(a.runs) > 0 || len(v.InFlight) > 0 || len(v.Queued) == 0 {
		return nil, nil
	}
	a.n++
	first := core.Run{ID: fmt.Sprintf("%s%d", v.Prefix, a.n), Entries: v.Queued[:1]}
	runs := []core.Run{first}
	if len(v.Queued) > 1 && v.Slots > 1 {
		runs = append(runs, core.Run{ID: first.ID + "-ahead", Base: first.ID, Entries: v.Queued[1:2]})
	}
	a.runs = map[string][]core.Entry{}
	for _, r := range runs {
		a.runs[r.ID] = r.Entries
	}
	return runs, nil
}

func (a *ahead) Record(_ core.View, id string, v core.Verdict) (core.Outcome, error) {
	es, ok := a.runs[id]
	if !ok {
		return core.Outcome{}, fmt.Errorf("unknown run %q", id)
	}
	delete(a.runs, id)
	d := core.Land
	if v != core.Pass {
		d = core.Eject
	}
	return core.Outcome{Settle: []core.Settle{{Entries: es, Decision: d, Why: string(v)}}}, nil
}

var _ = Describe("Compose ahead", func() {
	var (
		ctx   context.Context
		store *memStore
		comp  *memComposer
		run   *slowRunner
		land  *headLander
		note  *memNotifier
		st    *ahead
	)
	BeforeEach(func() {
		ctx, store, comp, note, st = context.Background(), &memStore{}, &memComposer{}, &memNotifier{}, &ahead{}
		run = &slowRunner{memRunner: &memRunner{}, wait: map[string]int{}}
		land = &headLander{memLander: &memLander{c: comp}, head: "m0"}
	})
	driver := func(strategy func() core.Strategy) *core.Driver {
		return &core.Driver{Store: store, Composer: comp, Runner: run, Lander: land, Notifier: note,
			NewStrategy: strategy, Main: "core", Slots: 2}
	}
	withAhead := func() core.Strategy { st = &ahead{}; return st } // rebuilt on every load, as a real one is
	serial := func() core.Strategy { return &core.Serial{Max: 4, Policy: core.Policy{RetryNone: 1}} }
	admit := func(d *core.Driver, id string) { Expect(d.Admit(ctx, entry(id))).To(Succeed()) }
	steps := func(d *core.Driver, n int) {
		for range n {
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

	It("A run composed ahead is tested on its base run's candidate and used only after that run lands", func() {
		run.wait["a"] = 2 // b's verdict is in first
		d := driver(withAhead)
		admit(d, "a")
		admit(d, "b")
		steps(d, 1)
		var bases []string
		for _, f := range store.snap().InFlight {
			bases = append(bases, f.BaseSHA)
		}
		Expect(bases).To(Equal([]string{"m0", "cand-1-on-m0"}))
		steps(d, 4)
		Expect(land.landed).To(Equal([]string{"a", "b"}))
		Expect(land.head).To(Equal("cand-2-on-cand-1-on-m0"))
		Expect(recomposed()).To(BeEmpty())
		Expect(st.views[len(st.views)-1].Main).To(Equal(land.head))
	})

	It("A run composed ahead on a run that goes red is recomposed, never landed or ejected", func() {
		run.verdict = failsWith("a")
		d := driver(withAhead)
		admit(d, "a")
		admit(d, "b")
		steps(d, 2)
		snap := store.snap()
		Expect(snap.Ejected).To(Equal(map[string]bool{"a": true}))
		Expect(land.landed).To(BeEmpty())
		rec := recomposed()
		Expect(rec).To(HaveLen(1))
		Expect(rec[0].ID).To(Equal("b"))
		Expect(rec[0].Base).To(Equal("cand-1-on-m0"))
		Expect(note.of(core.RecomposeEvent)[0].Base).To(Equal("cand-1-on-m0"))
		steps(d, 3)
		Expect(land.landed).To(Equal([]string{"b"}))
		Expect(land.head).To(Equal("cand-3-on-m0"), "b is composed again on main alone")
	})

	It("A green run tested on a main that has since moved is recomposed and lands on the new main", func() {
		d := driver(serial)
		admit(d, "a")
		steps(d, 1)
		land.head = "m1" // pushed outside the queue
		steps(d, 1)
		Expect(land.landed).To(BeEmpty())
		rec := recomposed()
		Expect(rec).To(HaveLen(1))
		Expect(rec[0].Base).To(Equal("m0"))
		Expect(rec[0].Why).To(And(ContainSubstring("m0"), ContainSubstring("m1")))
		Expect(store.snap().Paused).To(BeFalse())
		steps(d, 2)
		Expect(land.landed).To(Equal([]string{"a"}))
		Expect(land.head).To(Equal("cand-2-on-m1"))
	})

	It("A red run tested on a main that has since moved ejects nothing and is tested again", func() {
		run.verdict = failsWith("a")
		d := driver(serial)
		admit(d, "a")
		steps(d, 1)
		land.head = "m1"
		steps(d, 1)
		Expect(store.snap().Ejected).To(BeEmpty())
		Expect(recomposed()).To(HaveLen(1))
		steps(d, 2)
		Expect(store.snap().Ejected).To(Equal(map[string]bool{"a": true}), "ejected on the main it would land on")
	})
})

var _ = Describe("Compose ahead when main cannot be read", func() {
	It("A run whose main cannot be read now is recomposed, never landed", func() {
		ctx, store, comp, note := context.Background(), &memStore{}, &memComposer{}, &memNotifier{}
		land := &blindHeads{headLander{memLander: &memLander{c: comp}, head: "m0"}, false}
		d := &core.Driver{Store: store, Composer: comp, Runner: &memRunner{}, Lander: land, Notifier: note, Main: "core",
			NewStrategy: func() core.Strategy { return &core.Serial{Max: 4, Policy: core.Policy{RetryNone: 1}} }}
		Expect(d.Admit(ctx, entry("a"))).To(Succeed())
		Expect(d.Step(ctx)).To(Succeed())
		land.blind = true
		Expect(d.Step(ctx)).To(Succeed())
		Expect(land.landed).To(BeEmpty())
		Expect(note.of(core.RecomposeEvent)).To(HaveLen(1))
		land.blind = false
		for range 2 {
			Expect(d.Step(ctx)).To(Succeed())
		}
		Expect(land.landed).To(Equal([]string{"a"}))
	})

	It("A red run composed while main could not be read is recomposed, never ejected", func() {
		ctx, store, comp, note := context.Background(), &memStore{}, &memComposer{}, &memNotifier{}
		land := &blindHeads{headLander{memLander: &memLander{c: comp}, head: "m0"}, true}
		d := &core.Driver{Store: store, Composer: comp, Runner: &memRunner{verdict: failsWith("a")}, Lander: land, Notifier: note,
			Main: "core", NewStrategy: func() core.Strategy { return &core.Serial{Max: 4, Policy: core.Policy{RetryNone: 1}} }}
		Expect(d.Admit(ctx, entry("a"))).To(Succeed())
		Expect(d.Step(ctx)).To(Succeed()) // composed on the branch name: its base is unknown
		land.blind, land.head = false, "m1"
		Expect(d.Step(ctx)).To(Succeed())
		Expect(store.snap().Ejected).To(BeEmpty())
		Expect(land.landed).To(BeEmpty())
		Expect(note.of(core.RecomposeEvent)).To(HaveLen(1))
	})
})

// blindHeads is a headLander whose Head fails while blind.
type blindHeads struct {
	headLander
	blind bool
}

func (l *blindHeads) Head(ctx context.Context, b string) (string, error) {
	if l.blind {
		return "", fmt.Errorf("remote unreachable")
	}
	return l.headLander.Head(ctx, b)
}
