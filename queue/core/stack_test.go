package core_test

import (
	"maps"
	"math/rand"
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

// form makes a batch of entries with no stacks and nothing landed.
func form(es []core.Entry) core.Batch { return core.FormBatch(es, nil, nil) }

func ents(names ...string) []core.Entry {
	out := []core.Entry{}
	for _, n := range names {
		out = append(out, entry(n))
	}
	return out
}

var _ = Describe("Stacks", func() {
	Context("when admission order puts a child before its parents", func() {
		walk := func(stacks map[string][]string, names ...string) ([][]string, *core.Queue) {
			q := &core.Queue{}
			for _, n := range names {
				Expect(q.Admit(entry(n))).To(Succeed())
			}
			b, err := core.NewStackBisect(core.FormBatch(q.SelectBatch(8), stacks, nil), core.Policy{RetryNone: 1})
			Expect(err).NotTo(HaveOccurred())
			tried := [][]string{}
			for sub := b.Next(); sub != nil; sub = b.Next() {
				tried = append(tried, ids(sub))
				v := core.Pass
				if slices.Contains(ids(sub), "d") {
					v = core.Fail
				}
				d, err := b.Record(v)
				Expect(err).NotTo(HaveOccurred())
				Expect(settle(q, d, sub)).To(Succeed())
			}
			return tried, q
		}
		chain := map[string][]string{"d": {"c"}, "c": {"b"}}

		It("tries ancestors first, so a red child is ejected rather than orphaned", func() {
			tried, q := walk(chain, "d", "c", "b")
			Expect(tried).To(Equal([][]string{{"b"}, {"c", "d"}, {"c"}, {"d"}}))
			Expect(refusedAs(q.Admit(entry("b")))).To(Equal(core.Landed))
			Expect(refusedAs(q.Admit(entry("c")))).To(Equal(core.Landed))
			Expect(refusedAs(q.Admit(entry("d")))).To(Equal(core.Ejected))
		})

		It("keeps unrelated entries in their FIFO order", func() {
			batch := core.FormBatch(ents("d", "x", "c", "b", "y"), chain, nil)
			Expect(ids(batch.Entries())).To(Equal([]string{"x", "b", "c", "d", "y"}))
		})

		It("keeps FIFO whatever order a child lists its parents in", func() {
			batch := core.FormBatch(ents("child", "p", "q"), map[string][]string{"child": {"q", "p"}}, nil)
			Expect(ids(batch.Entries())).To(Equal([]string{"p", "q", "child"}))
		})

		It("follows parents through an out-of-batch node to a batch ancestor", func() {
			stacks := map[string][]string{"child": {"parent"}, "parent": {"grand"}}
			batch := core.FormBatch(ents("child", "grand"), stacks, map[string]bool{"parent": true})
			Expect(ids(batch.Entries())).To(Equal([]string{"grand", "child"}))
			b, err := core.NewStackBisect(batch, core.Policy{RetryNone: 1})
			Expect(err).NotTo(HaveOccurred())
			Expect(ids(b.Next())).To(Equal([]string{"grand"}))
		})

		It("defers an entry with an ancestor that is neither landed nor in the batch", func() {
			stacks := map[string][]string{"child": {"parent"}, "parent": {"grand"}}
			batch := core.FormBatch(ents("child", "a", "b"), stacks, nil)
			Expect(ids(batch.Entries())).To(Equal([]string{"a", "b"}))
		})

		It("defers every entry on a cycle and whatever builds on it, and reports the cycle", func() {
			stacks := map[string][]string{"a": {"b"}, "b": {"a"}, "c": {"a"}}
			batch := core.FormBatch(ents("a", "b", "c", "d", "e"), stacks, nil)
			Expect(ids(batch.Entries())).To(Equal([]string{"d", "e"}))
			Expect(batch.Cyclic()).To(ConsistOf("a", "b"))
		})

		It("decides the same outcome whatever the admission order, and never runs an entry without its ancestors", func() {
			const seed = 20261003
			rng := rand.New(rand.NewSource(seed))
			names := []string{"a", "b", "c", "d", "e", "f"}
			for i := 0; i < 300; i++ {
				// x and y are never in the batch; each is landed or not, and each
				// may build on batch entries, so an ancestor can be reachable only
				// through a node outside the batch.
				stacks, bad, landed := map[string][]string{}, map[string]bool{}, map[string]bool{"x": rng.Intn(2) == 0, "y": rng.Intn(2) == 0}
				nodes := []string{"a", "b", "x", "c", "d", "y", "e", "f"}
				for j := 1; j < len(nodes); j++ {
					for _, p := range nodes[:j] {
						if rng.Intn(4) == 0 {
							stacks[nodes[j]] = append(stacks[nodes[j]], p)
						}
					}
				}
				if rng.Intn(8) == 0 {
					stacks["b"], stacks["c"] = append(stacks["b"], "c"), append(stacks["c"], "b")
				}
				ancestors := func(id string) (out []string) {
					for todo := slices.Clone(stacks[id]); len(todo) > 0; todo = todo[1:] {
						if p := todo[0]; p != id && !slices.Contains(out, p) {
							out, todo = append(out, p), append(todo, stacks[p]...)
						}
					}
					return out
				}
				for n := rng.Intn(3); n > 0; n-- {
					id := names[rng.Intn(len(names))]
					bad[id] = true
					if anc := slices.DeleteFunc(ancestors(id), func(a string) bool { return !slices.Contains(names, a) }); len(anc) > 0 && rng.Intn(2) == 0 {
						bad[anc[rng.Intn(len(anc))]] = true // a bad ancestor of a bad entry
					}
				}
				outcome := func(order []string) map[string]core.State {
					q, done, ejected := &core.Queue{}, maps.Clone(landed), map[string]bool{}
					for _, n := range order {
						Expect(q.Admit(entry(n))).To(Succeed())
					}
					batch := core.FormBatch(q.SelectBatch(8), stacks, landed)
					in := ids(batch.Entries())
					for _, id := range in {
						want := slices.DeleteFunc(slices.Clone(order), func(n string) bool { return !slices.Contains(in, n) || !slices.Contains(ancestors(id), n) })
						Expect(slices.Equal(batch.Ancestors(id), want)).To(BeTrue(), "seed %d case %d: Ancestors(%s) = %v, want %v; order %v stacks %v landed %v", seed, i, id, batch.Ancestors(id), want, order, stacks, landed)
					}
					if len(in) >= 2 {
						b, err := core.NewStackBisect(batch, core.Policy{RetryNone: 1})
						Expect(err).NotTo(HaveOccurred())
						for sub := b.Next(); sub != nil; sub = b.Next() {
							v := core.Pass
							for _, id := range ids(sub) {
								Expect(in).To(ContainElement(id), "seed %d case %d: deferred %s ran", seed, i, id)
								for _, a := range ancestors(id) {
									Expect(ejected[a]).To(BeFalse(), "seed %d case %d: %s ran in %v after its ancestor %s was ejected; order %v stacks %v landed %v bad %v", seed, i, id, ids(sub), a, order, stacks, landed, bad)
									Expect(done[a] || slices.Contains(ids(sub), a)).To(BeTrue(), "seed %d case %d: %s ran in %v without ancestor %s; order %v stacks %v landed %v", seed, i, id, ids(sub), a, order, stacks, landed)
								}
								if bad[id] {
									v = core.Fail
								}
							}
							d, err := b.Record(v)
							Expect(err).NotTo(HaveOccurred())
							Expect(settle(q, d, sub)).To(Succeed())
							for _, id := range ids(sub) {
								done[id], ejected[id] = done[id] || d == core.Land, ejected[id] || d == core.Eject
							}
							for _, o := range b.TakeOrphans() {
								Expect(q.Settle(o.Entry.ID, core.Ejected)).To(Succeed())
								ejected[o.Entry.ID] = true
							}
						}
					}
					got := map[string]core.State{}
					for _, n := range names {
						got[n] = refusedAs(q.Admit(entry(n)))
					}
					return got
				}
				order := rng.Perm(len(names))
				shuffled := make([]string, len(names))
				for k, o := range order {
					shuffled[k] = names[o]
				}
				Expect(outcome(shuffled)).To(Equal(outcome(names)), "seed %d case %d order %v stacks %v landed %v bad %v", seed, i, shuffled, stacks, landed, bad)
			}
		})
	})

	Context("when a parent is ejected", func() {
		// walk runs a stacked bisect over a, b, c, d and returns what ran and
		// the orphans (child -> parent); verdicts fail any run holding a bad ID.
		walk := func(stacks map[string][]string, bad ...string) ([][]string, map[string]string) {
			q := &core.Queue{}
			for _, n := range []string{"a", "b", "c", "d"} {
				Expect(q.Admit(entry(n))).To(Succeed())
			}
			b, err := core.NewStackBisect(core.FormBatch(q.SelectBatch(8), stacks, nil), core.Policy{RetryNone: 1})
			Expect(err).NotTo(HaveOccurred())
			tried, orphans := [][]string{}, map[string]string{}
			for sub := b.Next(); sub != nil; sub = b.Next() {
				tried = append(tried, ids(sub))
				v := core.Pass
				if slices.ContainsFunc(ids(sub), func(id string) bool { return slices.Contains(bad, id) }) {
					v = core.Fail
				}
				d, err := b.Record(v)
				Expect(err).NotTo(HaveOccurred())
				Expect(settle(q, d, sub)).To(Succeed())
				for _, o := range b.TakeOrphans() {
					Expect(q.Settle(o.Entry.ID, core.Ejected)).To(Succeed())
					orphans[o.Entry.ID] = o.Parent
				}
			}
			return tried, orphans
		}

		It("ejects its descendants unrun, with the parent as the cause", func() {
			tried, orphans := walk(map[string][]string{"c": {"b"}}, "b")
			Expect(tried).To(Equal([][]string{{"a", "b"}, {"a"}, {"b"}, {"d"}}))
			Expect(orphans).To(Equal(map[string]string{"c": "b"}))
		})

		It("ejects every descendant of an ejected entry with several parents", func() {
			tried, orphans := walk(map[string][]string{"d": {"b", "c"}, "c": {"b"}}, "b")
			Expect(tried).To(Equal([][]string{{"a", "b"}, {"a"}, {"b"}}))
			Expect(orphans).To(Equal(map[string]string{"c": "b", "d": "b"}))
		})

		It("ejects every descendant down a chain, with the ejected entry as the cause", func() {
			tried, orphans := walk(map[string][]string{"c": {"b"}, "d": {"c"}}, "b")
			Expect(orphans).To(Equal(map[string]string{"c": "b", "d": "b"}))
			Expect(tried).To(Equal([][]string{{"a", "b"}, {"a"}, {"b"}}))
		})

		// drive runs b over batch, failing any run that holds a bad ID, and
		// returns what ran and the orphans (child -> parent).
		drive := func(batch core.Batch, b *core.Bisect, bad ...string) ([][]string, map[string]string) {
			q := &core.Queue{}
			for _, e := range batch.Entries() {
				Expect(q.Admit(e)).To(Succeed())
			}
			tried, orphans := [][]string{}, map[string]string{}
			for sub := b.Next(); sub != nil; sub = b.Next() {
				tried = append(tried, ids(sub))
				v := core.Pass
				if slices.ContainsFunc(ids(sub), func(id string) bool { return slices.Contains(bad, id) }) {
					v = core.Fail
				}
				d, err := b.Record(v)
				Expect(err).NotTo(HaveOccurred())
				Expect(settle(q, d, sub)).To(Succeed())
				for _, o := range b.TakeOrphans() {
					Expect(q.Settle(o.Entry.ID, core.Ejected)).To(Succeed())
					orphans[o.Entry.ID] = o.Parent
				}
			}
			return tried, orphans
		}

		It("a bisect never runs a child whose parent was ejected", func() {
			batch := core.FormBatch(ents("parent", "child"), map[string][]string{"child": {"parent"}}, nil)
			b, err := core.NewStackBisect(batch, core.Policy{RetryNone: 1})
			Expect(err).NotTo(HaveOccurred())
			tried, orphans := drive(batch, b, "parent")
			Expect(tried).To(Equal([][]string{{"parent"}}))
			Expect(orphans).To(Equal(map[string]string{"child": "parent"}))
		})

		It("never runs a child whose batch ancestor was ejected through a landed parent", func() {
			stacks := map[string][]string{"child": {"parent"}, "parent": {"grand"}}
			batch := core.FormBatch(ents("child", "grand"), stacks, map[string]bool{"parent": true})
			Expect(batch.Ancestors("child")).To(Equal([]string{"grand"}))
			b, err := core.NewStackBisect(batch, core.Policy{RetryNone: 1})
			Expect(err).NotTo(HaveOccurred())
			tried, orphans := drive(batch, b, "grand")
			Expect(tried).To(Equal([][]string{{"grand"}}))
			Expect(orphans).To(Equal(map[string]string{"child": "grand"}))
		})
	})
})
