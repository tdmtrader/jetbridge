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

	It("A landing settled after a restart lands only the commit it pushed; a replacement made meanwhile stays queued", func() {
		a1 := core.Entry{ID: "a", Commit: "sha-a"}
		comp := &memComposer{entries: map[string][]string{"cand-a1": {"a"}}}
		land := &memLander{c: comp, landed: []string{"a"}, blind: errors.New("unreachable")} // a@sha-a is on main; Contains fails
		d.Composer, d.Lander = comp, land
		data, err := json.Marshal(core.Snapshot{Queued: []core.Entry{a1}, Commits: map[string]string{"a": "sha-a"},
			Landing: &core.Landing{Main: "core", Candidate: "cand-a1", Entries: []core.Entry{a1}}})
		Expect(err).NotTo(HaveOccurred())
		store.data = data
		Expect(d.Step(ctx)).To(Succeed())
		Expect(store.snap().Paused).To(BeTrue())
		push("a")
		Expect(d.Step(ctx)).To(Succeed())
		Expect(store.snap().Queued[0].Commit).To(Equal("sha-a2"))
		land.blind = nil
		Expect(d.Resume(ctx, store.snap().PauseSeq)).To(Succeed())
		Expect(d.Step(ctx)).To(Succeed())
		snap := store.snap()
		Expect(snap.Landing).To(BeNil())
		Expect(snap.Landed).NotTo(HaveKey("a"))
		Expect(snap.Queued).To(HaveLen(1))
		Expect(snap.Queued[0].Commit).To(Equal("sha-a2"))
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
