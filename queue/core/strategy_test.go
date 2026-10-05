package core_test

import (
	"fmt"
	"math/rand"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

// drive runs a Strategy to a stop the way a driver would: plan, verdict,
// record, and drop each landed or ejected entry from the queue, marking it
// Landed or Ejected in the View. It returns the runs in order and every
// settlement by entry ID. It refuses to loop forever.
func drive(s core.Strategy, queued []core.Entry, buildsOn map[string][]string, verdict func([]string) core.Verdict) ([][]string, map[string]core.Settle) {
	v := core.View{Queued: queued, BuildsOn: buildsOn, Landed: map[string]bool{}, Ejected: map[string]bool{}, Slots: 1}
	runs, settled := [][]string{}, map[string]core.Settle{}
	apply := func(sts []core.Settle) {
		for _, st := range sts {
			for _, e := range st.Entries {
				settled[e.ID] = st
				if st.Decision == core.Land || st.Decision == core.Eject {
					v.Landed[e.ID], v.Ejected[e.ID] = st.Decision == core.Land, st.Decision == core.Eject
					v.Queued = slices.DeleteFunc(slices.Clone(v.Queued), func(q core.Entry) bool { return q.ID == e.ID })
				}
			}
		}
	}
	for step := 0; ; step++ {
		Expect(step).To(BeNumerically("<", 1000), "the strategy never stops")
		planned, sts := s.Plan(v)
		apply(sts)
		if len(planned) == 0 {
			if len(sts) == 0 {
				return runs, settled
			}
			continue
		}
		Expect(planned).To(HaveLen(1))
		r := planned[0]
		Expect(r.Base).To(BeEmpty())
		runs = append(runs, ids(r.Entries))
		out, err := s.Record(v, r.ID, verdict(ids(r.Entries)))
		Expect(err).NotTo(HaveOccurred())
		Expect(out.Cancel).To(BeEmpty())
		apply(out.Settle)
	}
}

func decisions(settled map[string]core.Settle) map[string]core.Decision {
	out := map[string]core.Decision{}
	for id, st := range settled {
		out[id] = st.Decision
	}
	return out
}

func failsWith(broken ...string) func([]string) core.Verdict {
	return func(run []string) core.Verdict {
		for _, b := range broken {
			if slices.Contains(run, b) {
				return core.Fail
			}
		}
		return core.Pass
	}
}

var _ core.Strategy = &core.Serial{}

var _ = Describe("Serial strategy", func() {
	pol := core.Policy{RetryNone: 1}

	It("A green batch is settled to land", func() {
		runs, settled := drive(&core.Serial{Max: 4, Policy: pol}, ents("a", "b", "c"), nil, failsWith())
		Expect(runs).To(Equal([][]string{{"a", "b", "c"}}))
		Expect(decisions(settled)).To(Equal(map[string]core.Decision{"a": core.Land, "b": core.Land, "c": core.Land}))
	})

	It("A red batch is bisected and the green half lands", func() {
		runs, settled := drive(&core.Serial{Max: 4, Policy: pol}, ents("a", "b", "c", "d"), nil, failsWith("d"))
		Expect(runs).To(Equal([][]string{{"a", "b", "c", "d"}, {"a", "b"}, {"c", "d"}, {"c"}, {"d"}}))
		Expect(decisions(settled)).To(Equal(map[string]core.Decision{"a": core.Land, "b": core.Land, "c": core.Land, "d": core.Eject}))
	})

	It("A missing verdict is retried then the queue pauses", func() {
		s := &core.Serial{Max: 4, Policy: pol}
		runs, settled := drive(s, ents("a", "b"), nil, func([]string) core.Verdict { return core.None })
		Expect(runs).To(Equal([][]string{{"a", "b"}, {"a", "b"}}))
		Expect(decisions(settled)).To(Equal(map[string]core.Decision{"a": core.Pause, "b": core.Pause}))
		Expect(s.Paused).To(BeTrue())
	})

	It("A change built on an ejected change never runs", func() {
		runs, settled := drive(&core.Serial{Max: 4, Policy: pol}, ents("a", "b", "c"), map[string][]string{"c": {"b"}}, failsWith("b"))
		Expect(runs).To(Equal([][]string{{"a", "b", "c"}, {"a"}, {"b", "c"}, {"b"}}))
		Expect(decisions(settled)).To(Equal(map[string]core.Decision{"a": core.Land, "b": core.Eject, "c": core.Eject}))
		Expect(settled["c"].Why).To(ContainSubstring(`"b"`))
	})

	It("A change waiting on an unlanded change stays queued", func() {
		runs, settled := drive(&core.Serial{Max: 4, Policy: pol}, ents("a", "b"), map[string][]string{"b": {"x"}}, failsWith())
		Expect(runs).To(Equal([][]string{{"a"}}))
		Expect(decisions(settled)).To(Equal(map[string]core.Decision{"a": core.Land}))
	})

	It("A change built on an ejected change outside the batch is ejected, not left waiting", func() {
		runs, settled := drive(&core.Serial{Max: 1, Policy: pol}, ents("a", "b"), map[string][]string{"b": {"a"}}, failsWith("a"))
		Expect(runs).To(Equal([][]string{{"a"}}))
		Expect(decisions(settled)).To(Equal(map[string]core.Decision{"a": core.Eject, "b": core.Eject}))
		Expect(settled["b"].Cause).To(Equal(core.ParentEjected))
		Expect(settled["b"].Parent).To(Equal("a"))
	})

	It("A change admitted after the change it builds on was ejected is ejected without running", func() {
		s := &core.Serial{Max: 4, Policy: pol}
		v := core.View{Queued: ents("b"), BuildsOn: map[string][]string{"b": {"a"}}, Landed: map[string]bool{}, Ejected: map[string]bool{"a": true}, Slots: 1}
		runs, sts := s.Plan(v)
		Expect(runs).To(BeEmpty())
		Expect(sts).To(HaveLen(1))
		Expect(ids(sts[0].Entries)).To(Equal([]string{"b"}))
		Expect(sts[0].Decision).To(Equal(core.Eject))
		Expect(sts[0].Cause).To(Equal(core.ParentEjected))
		Expect(sts[0].Parent).To(Equal("a"))
	})

	It("A flaky batch is surfaced in the outcome", func() {
		s := &core.Serial{Max: 4, Policy: pol}
		v := core.View{Queued: ents("a", "b"), Landed: map[string]bool{}, Ejected: map[string]bool{}, Slots: 1}
		var last core.Outcome
		for i, verdict := range []core.Verdict{core.Fail, core.Pass, core.Pass} {
			runs, _ := s.Plan(v)
			Expect(runs).To(HaveLen(1), "run %d", i)
			out, err := s.Record(v, runs[0].ID, verdict)
			Expect(err).NotTo(HaveOccurred())
			last = out
		}
		Expect(last.Flakes).To(HaveLen(1))
		Expect(ids(last.Flakes[0])).To(Equal([]string{"a", "b"}))
	})

	It("A flake proven inside a bisect is surfaced on the verdict that proves it, before the bisect ends", func() {
		s := &core.Serial{Max: 4, Policy: pol}
		v := core.View{Queued: ents("a", "b", "c", "d"), Landed: map[string]bool{}, Ejected: map[string]bool{}, Slots: 1}
		verdicts := map[string]core.Verdict{"a b c d": core.Fail, "a b": core.Fail, "a": core.Pass, "b": core.Pass}
		var got [][]string
		for range 4 {
			runs, _ := s.Plan(v)
			Expect(runs).To(HaveLen(1))
			out, err := s.Record(v, runs[0].ID, verdicts[strings.Join(ids(runs[0].Entries), " ")])
			Expect(err).NotTo(HaveOccurred())
			for _, f := range out.Flakes {
				got = append(got, ids(f))
			}
		}
		Expect(got).To(Equal([][]string{{"a", "b"}}), "[a b] is proven flaky while [c d] is still to run")
		runs, _ := s.Plan(v)
		Expect(ids(runs[0].Entries)).To(Equal([]string{"c", "d"}))
		out, err := s.Record(v, runs[0].ID, core.Pass)
		Expect(err).NotTo(HaveOccurred())
		Expect(out.Flakes).To(BeEmpty(), "a flake is surfaced once")
	})

	It("refuses a verdict for a run that is not in flight", func() {
		s := &core.Serial{Max: 4, Policy: pol}
		_, err := s.Record(core.View{}, "nope", core.Pass)
		Expect(err).To(MatchError(ContainSubstring(`"nope"`)))
		r, _ := s.Plan(core.View{Queued: ents("a"), Slots: 1})
		Expect(r).To(HaveLen(1))
		r, _ = s.Plan(core.View{Queued: ents("a"), Slots: 1})
		Expect(r).To(BeEmpty(), "one run at a time")
	})

	It("makes the same settlements as driving the stack bisect directly", func() {
		rng := rand.New(rand.NewSource(20261003))
		for range 300 {
			n, seed := 1+rng.Intn(8), rng.Int63()
			names, buildsOn := []string{}, map[string][]string{}
			broken := map[string]bool{}
			for i := range n {
				id := string(rune('a' + i))
				names = append(names, id)
				broken[id] = rng.Intn(4) == 0
				if i > 0 && rng.Intn(3) == 0 {
					buildsOn[id] = append(buildsOn[id], names[rng.Intn(i)])
				}
				if rng.Intn(12) == 0 {
					buildsOn[id] = append(buildsOn[id], "x") // never landed, never queued
				}
			}
			// verdict is deterministic per case: both drivers must ask for the same runs.
			verdict := func() func([]string) core.Verdict {
				r := rand.New(rand.NewSource(seed))
				return func(run []string) core.Verdict {
					switch k := r.Intn(12); {
					case k == 0:
						return core.None
					case k == 1:
						return core.Fail // a flake
					case slices.ContainsFunc(run, func(id string) bool { return broken[id] }):
						return core.Fail
					}
					return core.Pass
				}
			}
			_, viaSerial := drive(&core.Serial{Max: 16, Policy: pol}, ents(names...), buildsOn, verdict())
			want := directly(core.FormBatch(ents(names...), buildsOn, nil), pol, verdict())
			for _, id := range names {
				if _, ok := want[id]; !ok && slices.ContainsFunc(ancestry(id, buildsOn), func(a string) bool { return want[a] == core.Eject }) {
					want[id] = core.Eject // its ancestor was ejected: it never waits forever
				}
			}
			Expect(decisions(viaSerial)).To(Equal(want), "case %s builds on %v", strings.Join(names, ""), buildsOn)

			// Across a multi-batch drive, no entry waits forever: each ends landed,
			// ejected, or still waiting on an ancestor that is pending and not ejected.
			max, vd := 1+rng.Intn(3), verdict()
			_, multi := drive(&core.Serial{Max: max, Policy: pol}, ents(names...), buildsOn, func(run []string) core.Verdict {
				if v := vd(run); v != core.None {
					return v
				}
				return core.Pass
			})
			got := decisions(multi)
			for _, id := range names {
				if got[id] == core.Land || got[id] == core.Eject {
					continue
				}
				Expect(ancestry(id, buildsOn)).To(ContainElement(Satisfy(func(a string) bool { return got[a] != core.Land && got[a] != core.Eject })),
					"%s waits forever: case %s max %d builds on %v settled %v", id, strings.Join(names, ""), max, buildsOn, got)
				Expect(ancestry(id, buildsOn)).NotTo(ContainElement(Satisfy(func(a string) bool { return got[a] == core.Eject })),
					"%s waits on an ejected ancestor: case %s max %d builds on %v", id, strings.Join(names, ""), max, buildsOn)
			}
		}
	})

	It("A torn red (no verdict) never bisects, splits or ejects: it retries, then pauses", func() {
		rng := rand.New(rand.NewSource(20261003))
		for range 300 {
			n := 1 + rng.Intn(8)
			names, buildsOn, broken := []string{}, map[string][]string{}, map[string]bool{}
			for i := range n {
				id := string(rune('a' + i))
				names = append(names, id)
				broken[id] = rng.Intn(4) == 0
				if i > 0 && rng.Intn(3) == 0 {
					buildsOn[id] = append(buildsOn[id], names[rng.Intn(i)])
				}
			}
			noneOdds, retry, batchMax := 2+rng.Intn(4), 1+rng.Intn(2), 1+rng.Intn(8)
			desc := fmt.Sprintf("case %s builds on %v none 1/%d retry %d max %d", strings.Join(names, ""), buildsOn, noneOdds, retry, batchMax)
			var prev []string
			prevNone, streak, longest := false, 0, 0
			// Truthful except at random positions (whole batches and bisect halves)
			// where a torn run reports no verdict.
			verdict := func(run []string) core.Verdict {
				if prevNone {
					Expect(run).To(Equal(prev), "a no-verdict run is retried as is, never split: %s", desc)
				}
				prev, prevNone = slices.Clone(run), false
				if rng.Intn(noneOdds) == 0 {
					prevNone = true
					streak++
					longest = max(longest, streak)
					return core.None
				}
				streak = 0
				if slices.ContainsFunc(run, func(id string) bool { return broken[id] }) {
					return core.Fail
				}
				return core.Pass
			}
			s := &core.Serial{Max: batchMax, Policy: core.Policy{RetryNone: retry}}
			_, settled := drive(s, ents(names...), buildsOn, verdict)
			for id, st := range settled {
				switch {
				case st.Decision != core.Eject:
				case st.Cause == core.ParentEjected:
					Expect(settled[st.Parent].Decision).To(Equal(core.Eject), "%s ejected for a parent that was not: %s", id, desc)
				default:
					Expect(broken[id]).To(BeTrue(), "%s ejected though it is not broken (a no verdict must never eject): %s", id, desc)
				}
			}
			if longest > retry {
				Expect(s.Paused).To(BeTrue(), "more than %d no-verdicts in a row must pause: %s", retry, desc)
			}
		}
	})
})

// directly decides one batch with Decide and, if it splits, NewStackBisect.
func directly(b core.Batch, pol core.Policy, verdict func([]string) core.Verdict) map[string]core.Decision {
	out := map[string]core.Decision{}
	mark := func(es []core.Entry, d core.Decision) {
		if d == core.Land || d == core.Eject || d == core.Pause {
			for _, e := range es {
				out[e.ID] = d
			}
		}
	}
	batch := b.Entries()
	if len(batch) == 0 {
		return out
	}
	for retries := 0; ; retries++ {
		d, err := core.Decide(verdict(ids(batch)), batch, retries, pol)
		Expect(err).NotTo(HaveOccurred())
		if d == core.Split {
			break
		}
		if d != core.Retry {
			mark(batch, d)
			return out
		}
	}
	bis, err := core.NewStackBisect(b, pol)
	Expect(err).NotTo(HaveOccurred())
	for sub := bis.Next(); sub != nil; sub = bis.Next() {
		d, err := bis.Record(verdict(ids(sub)))
		Expect(err).NotTo(HaveOccurred())
		mark(sub, d)
		for _, o := range bis.TakeOrphans() {
			out[o.Entry.ID] = core.Eject
		}
	}
	return out
}

// ancestry is every ID id builds on, directly or through others.
func ancestry(id string, buildsOn map[string][]string) []string {
	seen := []string{}
	for todo := slices.Clone(buildsOn[id]); len(todo) > 0; todo = todo[1:] {
		if !slices.Contains(seen, todo[0]) {
			seen = append(seen, todo[0])
			todo = append(todo, buildsOn[todo[0]]...)
		}
	}
	return seen
}

// batchSizes plans n entries named by letter (broken ones fail), runs each
// batch to the end of its bisect, and returns the size of each batch's first run.
func batchSizes(s *core.Serial, n int, broken ...string) []int {
	names := make([]string, n)
	for i := range names {
		names[i] = string(rune('a' + i))
	}
	v := core.View{Queued: ents(names...), Landed: map[string]bool{}, Ejected: map[string]bool{}, Slots: 1}
	sizes, open := []int{}, map[string]bool{}
	for step := 0; step < 1000; step++ {
		runs, _ := s.Plan(v)
		if len(runs) == 0 {
			return sizes
		}
		if len(open) == 0 {
			sizes = append(sizes, len(runs[0].Entries))
			for _, e := range runs[0].Entries {
				open[e.ID] = true
			}
		}
		out, err := s.Record(v, runs[0].ID, failsWith(broken...)(ids(runs[0].Entries)))
		Expect(err).NotTo(HaveOccurred())
		for _, st := range out.Settle {
			for _, e := range st.Entries {
				delete(open, e.ID)
				v.Queued = slices.DeleteFunc(slices.Clone(v.Queued), func(q core.Entry) bool { return q.ID == e.ID })
			}
		}
	}
	Fail("the strategy never stops")
	return nil
}

var _ = Describe("Serial strategy fixed batch size", func() {
	pol := core.Policy{RetryNone: 1}

	It("keeps the size at max", func() {
		Expect(batchSizes(&core.Serial{Max: 4, Policy: pol}, 12, "a")).To(Equal([]int{4, 4, 4}))
	})

	// A small max splits a stack at the boundary; the child waits for the
	// landed parent, never runs without it.
	It("plans a stack's parent alone, then the child on the landed parent", func() {
		runs, settled := drive(&core.Serial{Max: 1, Policy: pol}, ents("parent", "child"), map[string][]string{"child": {"parent"}}, failsWith())
		Expect(runs).To(Equal([][]string{{"parent"}, {"child"}}))
		Expect(decisions(settled)).To(Equal(map[string]core.Decision{"parent": core.Land, "child": core.Land}))
	})
})
