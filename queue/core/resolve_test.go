package core_test

import (
	"context"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

var _ = Describe("Driver resolve", func() {
	var (
		ctx   context.Context
		store *memStore
		adm   *memAdmissions
		life  *memLifecycle
		note  *memNotifier
		d     *core.Driver
	)
	BeforeEach(func() {
		ctx, store, adm, life, note = context.Background(), &memStore{}, &memAdmissions{}, &memLifecycle{}, &memNotifier{}
		comp := &memComposer{}
		d = &core.Driver{
			Store: store, Composer: comp, Runner: &memRunner{}, Lander: &memLander{c: comp}, Notifier: note, Admissions: adm, Lifecycle: life,
			NewStrategy: func() core.Strategy { return idle{} }, Main: "core", Owner: "runner", Log: func(string, ...any) {},
		}
		data, err := json.Marshal(core.Snapshot{Ejected: map[string]bool{"e": true}, Commits: map[string]string{"e": "sha-e"}})
		Expect(err).NotTo(HaveOccurred())
		store.data = data
	})
	ask := func(id, commit string) {
		life.reqs = append(life.reqs, core.LifecycleRequest{Kind: core.ResolvedEvent, ID: id, Commit: commit, SHA: "sha-main"})
	}

	It("An ejected change is resolved and its id can be admitted again", func() {
		ask("e", "sha-e")
		Expect(d.Step(ctx)).To(Succeed())
		snap := store.snap()
		Expect(snap.Ejected).To(BeEmpty())
		Expect(note.of(core.ResolvedEvent)).To(HaveLen(1))
		Expect(snap.Settled).To(HaveLen(1))
		Expect(snap.Settled[0].ID).To(Equal("e"))
		Expect(snap.Settled[0].Kind).To(Equal(core.ResolvedEvent))
		Expect(life.reqs).To(BeEmpty())
		adm.push(core.Pending{ID: "e", Commit: "sha-e2"})
		Expect(d.Step(ctx)).To(Succeed())
		Expect(store.snap().Queued).To(Equal([]core.Entry{{ID: "e", Commit: "sha-e2", AdmittedAt: store.snap().Queued[0].AdmittedAt}}))
		Expect(store.snap().Refused).To(BeEmpty())
	})

	It("A resolved change admitted again on main builds on nothing it built on before", func() {
		data, err := json.Marshal(core.Snapshot{Ejected: map[string]bool{"a": true, "b": true},
			Commits: map[string]string{"a": "sha-a", "b": "sha-b"}, BuildsOn: map[string][]string{"b": {"a"}}})
		Expect(err).NotTo(HaveOccurred())
		store.data = data
		ask("b", "sha-b")
		Expect(d.Step(ctx)).To(Succeed())
		adm.push(core.Pending{ID: "b", Commit: "sha-b2"}) // rebased onto main
		Expect(d.Step(ctx)).To(Succeed())
		snap := store.snap()
		Expect(ids(snap.Queued)).To(Equal([]string{"b"}))
		Expect(snap.BuildsOn).NotTo(HaveKey("b"))
	})

	It("A resolve request for a change that is not ejected is deleted unheeded", func() {
		Expect(d.Admit(ctx, core.Entry{ID: "a", Commit: "sha-a"})).To(Succeed())
		ask("a", "sha-a")
		Expect(d.Step(ctx)).To(Succeed())
		Expect(ids(store.snap().Queued)).To(Equal([]string{"a"}))
		Expect(note.of(core.ResolvedEvent)).To(BeEmpty())
		Expect(life.reqs).To(BeEmpty())
	})

	It("A resolve request for a commit the eject has left is deleted unheeded", func() {
		ask("e", "old")
		Expect(d.Step(ctx)).To(Succeed())
		Expect(store.snap().Ejected).To(HaveKey("e"))
		Expect(note.of(core.ResolvedEvent)).To(BeEmpty())
		Expect(life.reqs).To(BeEmpty())
	})
})
