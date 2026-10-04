package core_test

import (
	"context"
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

// memLifecycle holds the requests until Done removes one.
type memLifecycle struct{ reqs []core.LifecycleRequest }

func (l *memLifecycle) Pending(context.Context) ([]core.LifecycleRequest, error) {
	return slices.Clone(l.reqs), nil
}

func (l *memLifecycle) Done(_ context.Context, r core.LifecycleRequest) error {
	l.reqs = slices.DeleteFunc(l.reqs, func(x core.LifecycleRequest) bool { return x == r })
	return nil
}

var _ = Describe("Driver withdraw", func() {
	var (
		ctx    context.Context
		store  *memStore
		life   *memLifecycle
		note   *memNotifier
		runner *memRunner
		lander *memLander
	)
	BeforeEach(func() {
		ctx, store, life, note, runner = context.Background(), &memStore{}, &memLifecycle{}, &memNotifier{}, &memRunner{}
	})
	driver := func(max int) *core.Driver {
		comp := &memComposer{}
		lander = &memLander{c: comp}
		return &core.Driver{
			Store: store, Composer: comp, Runner: runner, Lander: lander, Notifier: note, Lifecycle: life,
			NewStrategy: func() core.Strategy { return &core.Serial{Max: max} }, Main: "core", Owner: "runner",
			Log: func(string, ...any) {},
		}
	}
	admit := func(d *core.Driver, id string, on ...string) {
		Expect(d.Admit(ctx, core.Entry{ID: id, Commit: "c" + id}, on...)).To(Succeed())
	}
	ask := func(id, commit string) {
		life.reqs = append(life.reqs, core.LifecycleRequest{Kind: core.WithdrawnEvent, ID: id, Commit: commit, SHA: "sha-main"})
	}

	It("A queued change is withdrawn without being ejected or blamed", func() {
		d := driver(0)
		d.NewStrategy = func() core.Strategy { return idle{} }
		admit(d, "a")
		admit(d, "b")
		ask("a", "ca")
		Expect(d.Step(ctx)).To(Succeed())
		snap := store.snap()
		Expect(ids(snap.Queued)).To(Equal([]string{"b"}))
		Expect(note.of(core.WithdrawnEvent)).To(HaveLen(1))
		Expect(note.of(core.WithdrawnEvent)[0].Entries[0].ID).To(Equal("a"))
		Expect(snap.Ejected).To(BeEmpty())
		Expect(snap.Settled).To(HaveLen(1))
		Expect(snap.Settled[0].ID).To(Equal("a"))
		Expect(snap.Settled[0].Kind).To(Equal(core.WithdrawnEvent))
		Expect(life.reqs).To(BeEmpty())
		admit(d, "a")
	})

	It("A change in flight is withdrawn and its batch is recomposed without it", func() {
		d := driver(2)
		admit(d, "a")
		admit(d, "b")
		Expect(d.Step(ctx)).To(Succeed())
		Expect(runner.starts).To(Equal([][]string{{"a", "b"}}))
		ask("a", "ca")
		Expect(d.Step(ctx)).To(Succeed())
		Expect(runner.starts).To(Equal([][]string{{"a", "b"}, {"b"}}))
		Expect(d.Step(ctx)).To(Succeed())
		Expect(lander.landed).To(Equal([]string{"b"}))
		Expect(store.snap().Ejected).To(BeEmpty())
		Expect(note.of(core.EjectedEvent)).To(BeEmpty())
	})

	It("A change that another queued change builds on is not withdrawn", func() {
		d := driver(0)
		d.NewStrategy = func() core.Strategy { return idle{} }
		admit(d, "a")
		admit(d, "b", "a")
		ask("a", "ca")
		Expect(d.Step(ctx)).To(Succeed())
		Expect(ids(store.snap().Queued)).To(Equal([]string{"a", "b"}))
		Expect(note.of(core.WithdrawnEvent)).To(BeEmpty())
		Expect(life.reqs).To(BeEmpty())
	})

	It("A withdraw request for a commit the change has left is deleted unheeded", func() {
		d := driver(0)
		d.NewStrategy = func() core.Strategy { return idle{} }
		admit(d, "a")
		ask("a", "old")
		Expect(d.Step(ctx)).To(Succeed())
		Expect(ids(store.snap().Queued)).To(Equal([]string{"a"}))
		Expect(note.of(core.WithdrawnEvent)).To(BeEmpty())
		Expect(life.reqs).To(BeEmpty())
	})
})
