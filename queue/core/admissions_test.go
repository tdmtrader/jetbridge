package core_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

// memAdmissions holds pushed changes until Done removes one at its sha. With
// crash set the next Done panics, as if the process died there.
type memAdmissions struct {
	pending []core.Pending
	done    []string
	crash   bool
}

func (a *memAdmissions) push(p ...core.Pending) { a.pending = append(a.pending, p...) }

func (a *memAdmissions) Pending(context.Context, []core.Entry) ([]core.Pending, error) {
	return slices.Clone(a.pending), nil
}

func (a *memAdmissions) Done(_ context.Context, id, sha string) error {
	if a.crash {
		a.crash = false
		panic("killed")
	}
	a.pending = slices.DeleteFunc(a.pending, func(p core.Pending) bool { return p.ID == id && p.Commit == sha })
	a.done = append(a.done, id)
	return nil
}

// idle plans nothing, so a spec sees only what a drain does.
type idle struct{}

func (idle) Plan(core.View) ([]core.Run, []core.Settle) { return nil, nil }
func (idle) Record(core.View, string, core.Verdict) (core.Outcome, error) {
	return core.Outcome{}, nil
}

var _ = Describe("Driver admissions", func() {
	var (
		ctx   context.Context
		store *memStore
		adm   *memAdmissions
		note  *memNotifier
	)
	BeforeEach(func() {
		ctx, store, adm, note = context.Background(), &memStore{}, &memAdmissions{}, &memNotifier{}
	})
	driver := func(owner string) *core.Driver {
		comp := &memComposer{}
		return &core.Driver{
			Store: store, Composer: comp, Runner: &memRunner{}, Lander: &memLander{c: comp}, Notifier: note,
			NewStrategy: func() core.Strategy { return idle{} }, Main: "core", Owner: owner,
			Admissions: adm, Log: func(string, ...any) {},
		}
	}
	pending := func(id string, buildsOn ...string) core.Pending {
		return core.Pending{ID: id, Commit: "sha-" + id, BuildsOn: buildsOn}
	}

	It("A change pushed while the runner holds the lease is queued on its next step", func() {
		d := driver("runner")
		Expect(d.Step(ctx)).To(Succeed())
		Expect(driver("cli").Admit(ctx, entry("x"))).To(MatchError(ContainSubstring("lease held by runner")))
		adm.push(pending("a"))
		before := time.Now()
		Expect(d.Step(ctx)).To(Succeed())
		queued := store.snap().Queued
		Expect(queued).To(HaveLen(1))
		Expect([]string{queued[0].ID, queued[0].Commit}).To(Equal([]string{"a", "sha-a"}))
		Expect(queued[0].AdmittedAt).To(BeTemporally(">=", before), "stamped by the runner's clock at the drain")
		Expect(adm.done).To(Equal([]string{"a"}))
		Expect(adm.pending).To(BeEmpty())
		Expect(store.lease.Token).To(Equal(uint64(1)), "the runner kept its lease")
	})

	It("A divergent merge is refused at drain, its admit ref deleted and announced, and the queue keeps going", func() {
		d := driver("runner")
		adm.push(pending("a"), pending("b"))
		Expect(d.Step(ctx)).To(Succeed())
		adm.push(pending("x", "a", "b"), pending("y", "a"))
		Expect(d.Step(ctx)).To(Succeed())
		snap := store.snap()
		Expect(ids(snap.Queued)).To(Equal([]string{"a", "b", "y"}))
		Expect(snap.BuildsOn).To(Equal(map[string][]string{"y": {"a"}}))
		Expect(adm.done).To(Equal([]string{"a", "b", "x", "y"}))
		why := "x builds on a merge of queued changes a and b; land them first or rebase onto one"
		Expect(snap.Refused).To(Equal([]core.Refusal{{ID: "x", Commit: "sha-x", Why: why}}))
		refused := note.of(core.RefusedEvent)
		Expect(refused).To(HaveLen(1))
		Expect(refused[0].Entries).To(Equal([]core.Entry{{ID: "x", Commit: "sha-x"}}))
		Expect(refused[0].Why).To(Equal(why))
	})

	It("A refusal leaves a settle record in the same save as the refusal", func() {
		d := driver("runner")
		adm.push(core.Pending{ID: "x", Commit: "s", Why: "unsafe"})
		Expect(d.Step(ctx)).To(Succeed())
		snap := store.snap()
		Expect(snap.Refused).To(HaveLen(1))
		Expect(snap.Settled).To(HaveLen(1))
		Expect([]any{snap.Settled[0].ID, snap.Settled[0].Commit, snap.Settled[0].Kind, snap.Settled[0].Why}).
			To(Equal([]any{"x", "s", core.RefusedEvent, "unsafe"}))
		Expect(snap.Settled[0].At).NotTo(BeZero())
	})

	It("A crash after the save and before the admit ref is deleted admits the change once", func() {
		adm.push(pending("a"))
		adm.crash = true
		Expect(func() { _ = driver("d1").Step(ctx) }).To(Panic())
		Expect(ids(store.snap().Queued)).To(Equal([]string{"a"}))
		Expect(adm.pending).To(HaveLen(1), "the admit ref outlived the crash")
		store.now = store.now.Add(2 * time.Minute)
		Expect(driver("d2").Step(ctx)).To(Succeed())
		Expect(ids(store.snap().Queued)).To(Equal([]string{"a"}))
		Expect(adm.done).To(Equal([]string{"a"}))
		Expect(adm.pending).To(BeEmpty())
		Expect(note.of(core.RefusedEvent)).To(BeEmpty())
	})

	It("A change already landed or ejected has its admit ref deleted and is not queued again", func() {
		data, err := json.Marshal(core.Snapshot{Landed: map[string]bool{"a": true}, Ejected: map[string]bool{"b": true},
			Commits: map[string]string{"a": "sha-a", "b": "sha-b"}})
		Expect(err).NotTo(HaveOccurred())
		store.data = data
		adm.push(pending("a"), pending("b"))
		Expect(driver("runner").Step(ctx)).To(Succeed())
		Expect(store.snap().Queued).To(BeEmpty())
		Expect(adm.done).To(Equal([]string{"a", "b"}))
		Expect(note.of(core.RefusedEvent)).To(BeEmpty())
	})

	It("A replacement commit under a used id is refused, announced and its ref deleted; the original stays", func() {
		data, err := json.Marshal(core.Snapshot{Landed: map[string]bool{"l": true}, Ejected: map[string]bool{"e": true},
			Commits: map[string]string{"l": "sha-l", "e": "sha-e"}})
		Expect(err).NotTo(HaveOccurred())
		store.data = data
		d := driver("runner")
		adm.push(pending("a"))
		Expect(d.Step(ctx)).To(Succeed())
		adm.push(core.Pending{ID: "a", Commit: "sha-a2"}, core.Pending{ID: "l", Commit: "sha-l2"}, core.Pending{ID: "e", Commit: "sha-e2"})
		Expect(d.Step(ctx)).To(Succeed())
		snap := store.snap()
		Expect(snap.Queued).To(HaveLen(1))
		Expect(snap.Queued[0].Commit).To(Equal("sha-a"))
		why := func(id, old string) string {
			return "id " + id + " already used for " + old + "; admit the new commit under a new id"
		}
		Expect(snap.Refused).To(Equal([]core.Refusal{{ID: "a", Commit: "sha-a2", Why: why("a", "sha-a")},
			{ID: "l", Commit: "sha-l2", Why: why("l", "sha-l")}, {ID: "e", Commit: "sha-e2", Why: why("e", "sha-e")}}))
		Expect(note.of(core.RefusedEvent)).To(HaveLen(3))
		Expect(adm.pending).To(BeEmpty(), "every replacement ref was deleted")
		Expect(note.of(core.RefusedEvent)[0].Why).To(Equal(why("a", "sha-a")))
	})

	It("ignores a parent that is no longer queued", func() {
		d := driver("runner")
		adm.push(pending("b", "gone"))
		Expect(d.Step(ctx)).To(Succeed())
		Expect(ids(store.snap().Queued)).To(Equal([]string{"b"}))
		Expect(store.snap().BuildsOn).To(BeEmpty())
	})

	It("refuses a change its source refuses, keeping only the latest refusals", func() {
		d := driver("runner")
		for i := range core.MaxRefused + 2 {
			adm.push(core.Pending{ID: fmt.Sprintf("bad%02d", i), Commit: "c", Why: "unsafe"})
		}
		Expect(d.Step(ctx)).To(Succeed())
		snap := store.snap()
		Expect(snap.Queued).To(BeEmpty())
		Expect(snap.Refused).To(HaveLen(core.MaxRefused))
		Expect(snap.Refused[0].ID).To(Equal("bad02"))
		Expect(snap.Refused[core.MaxRefused-1]).To(Equal(core.Refusal{ID: "bad21", Commit: "c", Why: "unsafe"}))
		Expect(note.of(core.RefusedEvent)).To(HaveLen(core.MaxRefused + 2))
		Expect(adm.pending).To(BeEmpty())
	})
})
