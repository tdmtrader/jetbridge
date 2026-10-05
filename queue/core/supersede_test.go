package core_test

import (
	"context"
	"encoding/json"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

var _ = Describe("Driver supersede", func() {
	var (
		ctx    context.Context
		store  *memStore
		adm    *memAdmissions
		note   *memNotifier
		runner *memRunner
		d      *core.Driver
	)
	BeforeEach(func() {
		ctx, store, adm, note, runner = context.Background(), &memStore{}, &memAdmissions{}, &memNotifier{}, &memRunner{}
		comp := &memComposer{}
		d = &core.Driver{
			Store: store, Composer: comp, Runner: runner, Lander: &memLander{c: comp}, Notifier: note, Admissions: adm,
			NewStrategy: func() core.Strategy { return idle{} }, Main: "core", Owner: "runner", Log: func(string, ...any) {},
		}
	})
	queue := func(id string, on ...string) {
		Expect(d.Admit(ctx, core.Entry{ID: id, Commit: "sha-" + id}, on...)).To(Succeed())
	}
	push := func(id string, on ...string) { adm.push(core.Pending{ID: id, Commit: "sha-" + id + "2", BuildsOn: on}) }

	It("A new commit under a queued id replaces it and keeps its position", func() {
		queue("a")
		queue("b")
		queue("c")
		before := store.snap().Queued[1].AdmittedAt
		push("b")
		Expect(d.Step(ctx)).To(Succeed())
		snap := store.snap()
		Expect(ids(snap.Queued)).To(Equal([]string{"a", "b", "c"}))
		Expect(snap.Queued[1].Commit).To(Equal("sha-b2"))
		Expect(snap.Queued[1].AdmittedAt).To(Equal(before))
		Expect(snap.Commits["b"]).To(Equal("sha-b2"))
		Expect(note.of(core.SupersededEvent)).To(HaveLen(1))
		Expect(snap.Settled[0].Kind).To(Equal(core.SupersededEvent))
		Expect(snap.Refused).To(BeEmpty())
		Expect(adm.pending).To(BeEmpty())
	})

	It("A change in flight is recomposed with the new commit", func() {
		d.NewStrategy = func() core.Strategy { return &core.Serial{Max: 2} }
		queue("a")
		queue("b")
		Expect(d.Step(ctx)).To(Succeed())
		push("a")
		Expect(d.Step(ctx)).To(Succeed())
		Expect(runner.starts).To(Equal([][]string{{"a", "b"}, {"a", "b"}}))
		snap := store.snap()
		Expect(snap.InFlight).To(HaveLen(1))
		Expect(snap.InFlight[0].Run.Entries[0].Commit).To(Equal("sha-a2"))
		Expect(snap.Ejected).To(BeEmpty())
	})

	It("A new commit under an id that another queued change builds on is refused", func() {
		queue("a")
		queue("b", "a")
		push("a")
		Expect(d.Step(ctx)).To(Succeed())
		snap := store.snap()
		Expect(snap.Queued[0].Commit).To(Equal("sha-a"))
		Expect(snap.Refused).To(HaveLen(1))
		Expect(snap.Refused[0].Why).To(ContainSubstring("build on it"))
		Expect(note.of(core.SupersededEvent)).To(BeEmpty())
	})

	It("A landing settled after a restart, once main is readable, lands only the commit it pushed; a replacement made meanwhile is refused as the id has landed", func() {
		a1 := core.Entry{ID: "a", Commit: "sha-a"}
		comp := &memComposer{entries: map[string][]string{"cand-a1": {"a"}}}
		land := &memLander{c: comp, landed: []string{"a"}, blind: errors.New("unreachable")} // a@sha-a is on main; Contains fails
		d.Composer, d.Lander = comp, land
		data, err := json.Marshal(core.Snapshot{Queued: []core.Entry{a1}, Commits: map[string]string{"a": "sha-a"},
			Landing: &core.Landing{Main: "core", Candidate: "cand-a1", Entries: []core.Entry{a1}}})
		Expect(err).NotTo(HaveOccurred())
		store.data = data
		Expect(d.Step(ctx)).To(MatchError(ContainSubstring("cannot tell whether main holds cand-a1")))
		Expect(store.snap().Paused).To(BeFalse())
		Expect(store.snap().LandFails).To(Equal(1))
		push("a")
		land.blind = nil
		Expect(d.Step(ctx)).To(Succeed())
		Expect(d.Step(ctx)).To(Succeed())
		snap := store.snap()
		Expect(snap.Landing).To(BeNil())
		Expect(snap.Landed).To(HaveKey("a"), "main holds sha-a, the commit that was pushed")
		Expect(snap.Queued).To(BeEmpty())
		Expect(snap.Refused).To(HaveLen(1), "the replacement made meanwhile is refused, as the id already landed")
		Expect(snap.Refused[0].Commit).To(Equal("sha-a2"))
	})

	It("A change built on a refused replacement is refused with it, never queued on the old commit", func() {
		queue("a")
		queue("d", "a")
		push("a")
		adm.push(core.Pending{ID: "b", Commit: "sha-b", BuildsOn: []string{"a"}, Ancestors: []string{"sha-a2"}})
		Expect(d.Step(ctx)).To(Succeed())
		snap := store.snap()
		Expect(ids(snap.Queued)).To(Equal([]string{"a", "d"}))
		Expect(snap.Queued[0].Commit).To(Equal("sha-a"))
		Expect(snap.Refused).To(HaveLen(2))
		Expect(snap.Refused[1].ID).To(Equal("b"))
		Expect(snap.Refused[1].Why).To(HavePrefix("built on sha-a2, which was refused: "))
		Expect(snap.BuildsOn).NotTo(HaveKey("b"))
		Expect(snap.Ejected).NotTo(HaveKey("b"))
		Expect(adm.pending).To(BeEmpty())
	})

	It("A change listed before the refused commit it is built on is refused all the same", func() {
		queue("a")
		queue("b", "a") // A0, then B0 on it
		// B1 on A1 is listed first; A1 on B0 is refused, as b builds on a
		adm.push(core.Pending{ID: "b", Commit: "sha-b2", BuildsOn: []string{"a"}, Ancestors: []string{"sha-a2"}})
		push("a", "b")
		Expect(d.Step(ctx)).To(Succeed())
		snap := store.snap()
		Expect(snap.Queued[0].Commit).To(Equal("sha-a"))
		Expect(snap.Queued[1].Commit).To(Equal("sha-b"))
		Expect(snap.Refused).To(HaveLen(2))
		Expect(snap.Refused[1].Why).To(HavePrefix("built on sha-a2, which was refused: "))
	})

	It("A change built on a commit refused before the drain is refused", func() {
		adm.push(core.Pending{ID: "u", Commit: "sha-u", Why: "sha-u is not a commit"},
			core.Pending{ID: "c", Commit: "sha-c", Ancestors: []string{"sha-u"}})
		Expect(d.Step(ctx)).To(Succeed())
		snap := store.snap()
		Expect(snap.Queued).To(BeEmpty())
		Expect(snap.Refused).To(HaveLen(2))
		Expect(snap.Refused[1].Why).To(Equal("built on sha-u, which was refused: sha-u is not a commit"))
	})

	It("A change built on the queued commit of an id is admitted when a new commit of that id is refused", func() {
		queue("a")
		queue("d", "a")
		push("a")
		adm.push(core.Pending{ID: "x", Commit: "sha-x", BuildsOn: []string{"a"}}) // on A0, not A1
		Expect(d.Step(ctx)).To(Succeed())
		snap := store.snap()
		Expect(ids(snap.Queued)).To(Equal([]string{"a", "d", "x"}))
		Expect(snap.BuildsOn["x"]).To(Equal([]string{"a"}))
		Expect(snap.Refused).To(HaveLen(1))
	})

	It("A new commit that merges two unrelated queued changes does not replace the queued one", func() {
		queue("a")
		queue("b")
		queue("c")
		push("c", "a", "b")
		Expect(d.Step(ctx)).To(Succeed())
		snap := store.snap()
		Expect(snap.Queued[2].Commit).To(Equal("sha-c"))
		Expect(snap.Refused).To(HaveLen(1))
		Expect(snap.Refused[0].Why).To(ContainSubstring("builds on a merge"))
	})
})
