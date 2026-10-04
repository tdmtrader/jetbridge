package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

// memStore keeps the snapshot as JSON, so whatever the driver saves must
// round-trip, and refuses a save from a stale version. Its lease runs on a
// clock that only the spec moves.
type memStore struct {
	data    []byte
	version int
	crash   bool // the next Save panics, as if the process died there
	refuse  int  // the next refuse Saves fail
	lease   core.Lease
	now     time.Time
}

func (s *memStore) Acquire(_ context.Context, owner string, ttl time.Duration) (core.Lease, error) {
	if s.lease.Owner != owner && s.now.Before(s.lease.Expires) {
		return core.Lease{}, fmt.Errorf("lease held by %s", s.lease.Owner)
	}
	if s.lease.Owner != owner || s.lease.Token == 0 {
		s.lease.Token++
	}
	s.lease.Owner, s.lease.Expires = owner, s.now.Add(ttl)
	return s.lease, nil
}

func (s *memStore) Load(context.Context) (core.Snapshot, error) {
	var snap core.Snapshot
	if s.data != nil {
		if err := json.Unmarshal(s.data, &snap); err != nil {
			return snap, err
		}
	}
	snap.Version = strconv.Itoa(s.version)
	return snap, nil
}

func (s *memStore) Save(_ context.Context, token uint64, snap core.Snapshot) (string, error) {
	if s.crash {
		panic("killed")
	}
	if s.refuse > 0 {
		s.refuse--
		return "", errors.New("store unavailable")
	}
	if token < s.lease.Token {
		return "", fmt.Errorf("fenced: lease %d is older than %d", token, s.lease.Token)
	}
	if snap.Version != strconv.Itoa(s.version) {
		return "", fmt.Errorf("stale save: have %d, got %s", s.version, snap.Version)
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return "", err
	}
	s.data, s.version = b, s.version+1
	return strconv.Itoa(s.version), nil
}

// afterLanding fails the first Save after one that holds a Landing of "b", as
// if the store went away just after the land; kept is what was stored then.
type afterLanding struct {
	*memStore
	armed, failed bool
	kept          core.Snapshot
}

func (s *afterLanding) Save(ctx context.Context, token uint64, snap core.Snapshot) (string, error) {
	if s.armed && !s.failed {
		s.failed, s.kept = true, s.memStore.snap()
		return "", errors.New("store unavailable")
	}
	v, err := s.memStore.Save(ctx, token, snap)
	s.armed = err == nil && snap.Landing != nil && slices.Contains(ids(snap.Landing.Entries), "b")
	return v, err
}

func (s *memStore) snap() core.Snapshot {
	snap, err := s.Load(context.Background())
	Expect(err).NotTo(HaveOccurred())
	return snap
}

// memComposer names each candidate and remembers which entries it holds. A
// batch holding conflict fails with a ConflictError naming it.
type memComposer struct {
	n         int
	entries   map[string][]string
	conflict  string
	onCompose func() // runs once a candidate is composed
}

func (c *memComposer) Compose(_ context.Context, base string, es []core.Entry) (string, error) {
	if c.conflict != "" && slices.Contains(ids(es), c.conflict) {
		return "", core.ConflictError{EntryID: c.conflict}
	}
	c.n++
	cand := fmt.Sprintf("cand-%d-on-%s", c.n, base)
	if c.entries == nil {
		c.entries = map[string][]string{}
	}
	c.entries[cand] = ids(es)
	if c.onCompose != nil {
		c.onCompose()
	}
	return cand, nil
}

// memRunner gives each started run its verdict on the next Poll. With
// timeout set every Poll fails; with kill set Start panics, as if the
// process died there. Poll of a run it never started fails.
type memRunner struct {
	verdict func([]string) core.Verdict
	timeout bool
	kill    bool
	started map[string][]string
	starts  [][]string
	onStart func([]string)
	before  func() // runs as Start is called, before the run is recorded
}

func (r *memRunner) Start(_ context.Context, run core.Run, _ string) error {
	if r.kill {
		panic("killed")
	}
	if r.before != nil {
		r.before()
	}
	if r.started == nil {
		r.started = map[string][]string{}
	}
	r.started[run.ID] = ids(run.Entries)
	r.starts = append(r.starts, ids(run.Entries))
	if r.onStart != nil {
		r.onStart(ids(run.Entries))
	}
	return nil
}

func (r *memRunner) Poll(_ context.Context, id string) (core.Verdict, bool, error) {
	if r.timeout {
		return "", false, errors.New("timed out waiting for a result")
	}
	es, ok := r.started[id]
	if !ok {
		return "", false, fmt.Errorf("no run %q", id)
	}
	if r.verdict == nil {
		return core.Pass, true, nil
	}
	return r.verdict(es), true, nil
}

// memLander moves main by recording each entry of the candidate it lands,
// then calls onLand. Main contains a candidate once all its entries landed.
// It fences: a fence below the highest it has seen is refused.
type memLander struct {
	c      *memComposer
	landed []string
	fence  uint64
	lands  int // Land calls
	fails  int
	moved  int // Lands refused with a MainMovedError
	kill   bool
	before func() // runs as Land is called, before main moves
	onLand func()
	blind  error        // Contains fails with it
	hang   bool         // the next Land times out with its push still pending
	late   func() error // the pending push arriving late, as the Land it was
}

func (l *memLander) fenced(fence uint64) error {
	if fence < l.fence {
		return fmt.Errorf("fenced: %#x is older than %#x", fence, l.fence)
	}
	l.fence = fence
	return nil
}

func (l *memLander) Contains(_ context.Context, _, cand string, fence uint64) (bool, error) {
	if err := l.fenced(fence); err != nil {
		return false, err
	}
	if l.blind != nil {
		return false, l.blind
	}
	for _, id := range l.c.entries[cand] {
		if !slices.Contains(l.landed, id) {
			return false, nil
		}
	}
	return true, nil
}

func (l *memLander) Land(ctx context.Context, main, cand string, fence uint64) error {
	if l.hang {
		l.hang = false
		l.late = func() error { return l.Land(ctx, main, cand, fence) }
		return errors.New("timed out waiting for the push")
	}
	if l.kill {
		panic("killed")
	}
	if l.lands++; l.before != nil {
		l.before()
	}
	if err := l.fenced(fence); err != nil {
		return err
	}
	if l.moved > 0 {
		l.moved--
		return &core.MainMovedError{Main: main, Candidate: cand}
	}
	if l.fails > 0 {
		l.fails--
		return errors.New("main moved")
	}
	l.landed = append(l.landed, l.c.entries[cand]...)
	if l.onLand != nil {
		l.onLand()
	}
	return nil
}

type memNotifier struct {
	events []core.Event
	err    error
}

func (n *memNotifier) Notify(_ context.Context, e core.Event) error {
	n.events = append(n.events, e)
	return n.err
}

func (n *memNotifier) of(k core.EventKind) []core.Event {
	var out []core.Event
	for _, e := range n.events {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

var _ = Describe("Driver", func() {
	var (
		ctx   context.Context
		store *memStore
		comp  *memComposer
		run   *memRunner
		land  *memLander
		note  *memNotifier
		logs  []string
		retry int
		max   int
	)
	BeforeEach(func() {
		ctx, store, comp, run, note, logs, retry, max = context.Background(), &memStore{}, &memComposer{}, &memRunner{}, &memNotifier{}, nil, 1, 4
		land = &memLander{c: comp}
	})
	driver := func() *core.Driver {
		return &core.Driver{
			Store: store, Composer: comp, Runner: run, Lander: land, Notifier: note,
			NewStrategy: func() core.Strategy { return &core.Serial{Max: max, Policy: core.Policy{RetryNone: retry}} },
			Main:        "core",
			Log:         func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) },
		}
	}
	driverAs := func(owner string) *core.Driver {
		d := driver()
		d.Owner = owner
		return d
	}
	expire := func() { store.now = store.now.Add(2 * time.Minute) } // past the default lease
	admit := func(d *core.Driver, id string, buildsOn ...string) {
		Expect(d.Admit(ctx, entry(id), buildsOn...)).To(Succeed())
	}
	steps := func(d *core.Driver, n int) {
		for range n {
			Expect(d.Step(ctx)).To(Succeed())
		}
	}

	It("A green batch lands and is announced", func() {
		d := driver()
		admit(d, "a")
		admit(d, "b")
		admit(d, "c")
		steps(d, 3)
		Expect(land.landed).To(Equal([]string{"a", "b", "c"}))
		Expect(note.of(core.BatchStarted)).To(HaveLen(1))
		Expect(note.of(core.VerdictIn)).To(HaveLen(1))
		Expect(note.of(core.VerdictIn)[0].Verdict).To(Equal(core.Pass))
		landed := note.of(core.LandedEvent)
		Expect(landed).To(HaveLen(1))
		Expect(ids(landed[0].Entries)).To(Equal([]string{"a", "b", "c"}))
		Expect(landed[0].Why).NotTo(BeEmpty())
		snap := store.snap()
		Expect(snap.Queued).To(BeEmpty())
		Expect(snap.InFlight).To(BeEmpty())
		Expect(snap.Landed).To(Equal(map[string]bool{"a": true, "b": true, "c": true}))
	})

	It("A red change is ejected with its reason and the changes built on it follow", func() {
		run.verdict = failsWith("b")
		d := driver()
		admit(d, "a")
		admit(d, "b")
		admit(d, "c", "b")
		steps(d, 12)
		Expect(land.landed).To(Equal([]string{"a"}))
		ej := note.of(core.EjectedEvent)
		Expect(ej).To(HaveLen(2))
		Expect(ids(ej[0].Entries)).To(Equal([]string{"b"}))
		Expect(ej[0].Why).To(Equal("failed on its own"))
		Expect(ids(ej[1].Entries)).To(Equal([]string{"c"}))
		Expect(ej[1].Cause).To(Equal(core.ParentEjected))
		Expect(ej[1].Parent).To(Equal("b"))
		snap := store.snap()
		Expect(snap.Queued).To(BeEmpty())
		Expect(snap.Ejected).To(Equal(map[string]bool{"b": true, "c": true}))
	})

	Describe("settle records", func() {
		at := time.Date(2026, 10, 3, 9, 30, 0, 0, time.FixedZone("x", -5*3600))
		clocked := func() *core.Driver {
			d := driver()
			d.Now = func() time.Time { return at }
			return d
		}

		It("A flaky batch is recorded and shown, never hidden", func() {
			run.verdict = func(es []string) core.Verdict {
				if len(es) == 2 {
					return core.Fail
				}
				return core.Pass
			}
			d := clocked()
			admit(d, "a")
			admit(d, "b")
			steps(d, 8)
			var flaky []core.SettleRecord
			for _, r := range store.snap().Settled {
				if r.Kind == core.FlakeEvent {
					flaky = append(flaky, r)
				}
			}
			Expect(flaky).To(HaveLen(1), "one record for the batch, not one per entry")
			Expect(flaky[0].ID).To(Equal("a, b"))
			Expect(flaky[0].Kind).To(Equal(core.EventKind("flaky")))
			Expect(flaky[0].Batch).To(Equal([]string{"a", "b"}))
			Expect(flaky[0].Run).NotTo(BeEmpty())
			Expect(flaky[0].Why).To(Equal("red as a batch, green when split"))
			Expect(flaky[0].At).To(Equal(at.UTC()))
		})

		It("A flake is saved with the outcome it accompanies", func() {
			run.verdict = func(es []string) core.Verdict {
				if len(es) == 2 {
					return core.Fail
				}
				return core.Pass
			}
			d, flakes := clocked(), func(s core.Snapshot) int {
				return len(slices.DeleteFunc(slices.Clone(s.Settled), func(r core.SettleRecord) bool { return r.Kind != core.FlakeEvent }))
			}
			gone := &afterLanding{memStore: store}
			d.Store = gone
			admit(d, "a")
			admit(d, "b")
			for range 8 {
				_ = d.Step(ctx) // the save after b's landing fails once
			}
			Expect(gone.failed).To(BeTrue())
			Expect(gone.kept.Landing).NotTo(BeNil(), "b's landing was saved")
			Expect(flakes(gone.kept)).To(Equal(1), "so its flake was saved with it")
			Expect(store.snap().Landed).To(HaveLen(2))
			Expect(flakes(store.snap())).To(Equal(1))
		})

		It("Every landed change leaves a timestamped settle record", func() {
			d := clocked()
			admit(d, "a")
			admit(d, "b")
			steps(d, 3)
			recs := store.snap().Settled
			Expect(recs).To(HaveLen(2))
			for i, id := range []string{"a", "b"} {
				Expect(recs[i].ID).To(Equal(id))
				Expect(recs[i].Commit).To(Equal("sha-" + id))
				Expect(recs[i].Kind).To(Equal(core.LandedEvent))
				Expect(recs[i].At).To(BeTemporally("==", at))
				Expect(recs[i].At.Location()).To(Equal(time.UTC))
				Expect(recs[i].AdmittedAt).To(BeTemporally("==", time.Unix(0, 0)))
				Expect(recs[i].Run).NotTo(BeEmpty())
				Expect(recs[i].Batch).To(Equal([]string{"a", "b"}))
			}
			Expect(note.of(core.LandedEvent)[0].At).To(BeTemporally("==", at))
			Expect(note.of(core.BatchStarted)[0].At).To(BeTemporally("==", at))
		})

		It("An ejected change records when it was ejected and why", func() {
			run.verdict = failsWith("b")
			d := clocked()
			admit(d, "a")
			admit(d, "b")
			admit(d, "c", "b")
			steps(d, 12)
			var got []core.SettleRecord
			for _, r := range store.snap().Settled {
				if r.Kind == core.EjectedEvent {
					got = append(got, r)
				}
			}
			Expect(got).To(HaveLen(2))
			Expect(got[0].ID).To(Equal("b"))
			Expect(got[0].Why).To(Equal("failed on its own"))
			Expect(got[0].At).To(BeTemporally("==", at))
			Expect(got[1].ID).To(Equal("c"))
			Expect(got[1].Cause).To(Equal(core.ParentEjected))
			Expect(got[1].Why).To(ContainSubstring("b"))
		})

		It("records a pause with its reason", func() {
			run.timeout = true
			d := clocked()
			admit(d, "a")
			steps(d, 6)
			recs := store.snap().Settled
			Expect(recs).To(HaveLen(1))
			Expect(recs[0].Kind).To(Equal(core.PausedEvent))
			Expect(recs[0].Why).To(ContainSubstring("no verdict"))
		})

		It("a restart that finds the queue still paused for the same reason adds no second pause record", func() {
			land.onLand = func() { store.crash = true }
			d1 := clocked()
			admit(d1, "a")
			Expect(func() {
				for range 2 {
					_ = d1.Step(ctx)
				}
			}).To(PanicWith("killed"))
			store.crash, land.onLand, land.blind = false, nil, errors.New("main unreadable")
			steps(clocked(), 2)
			expire()
			steps(clocked(), 2)
			var paused []core.SettleRecord
			for _, r := range store.snap().Settled {
				if r.Kind == core.PausedEvent {
					paused = append(paused, r)
				}
			}
			Expect(paused).To(HaveLen(1))
		})

		It("an ejected culprit has the cause culprit", func() {
			run.verdict = failsWith("a")
			d := clocked()
			admit(d, "a")
			steps(d, 3)
			recs := store.snap().Settled
			Expect(recs).To(HaveLen(1))
			Expect(recs[0].Kind).To(Equal(core.EjectedEvent))
			Expect(recs[0].Cause).To(Equal("culprit"))
		})

		It("a culprit proved by bisecting has the cause culprit and its orphan keeps parent-ejected", func() {
			run.verdict = failsWith("b")
			d := clocked()
			admit(d, "a")
			admit(d, "b")
			admit(d, "c", "b")
			steps(d, 12)
			causes := map[string]string{}
			for _, r := range store.snap().Settled {
				if r.Kind == core.EjectedEvent {
					causes[r.ID] = r.Cause
				}
			}
			Expect(causes).To(Equal(map[string]string{"b": "culprit", "c": core.ParentEjected}))
		})

		It("keeps only the last MaxSettled records", func() {
			max = core.MaxSettled + 5
			d := clocked()
			for i := range core.MaxSettled + 5 {
				admit(d, "c"+strconv.Itoa(i))
			}
			steps(d, 3)
			recs := store.snap().Settled
			Expect(recs).To(HaveLen(core.MaxSettled))
			Expect(recs[len(recs)-1].ID).To(Equal("c" + strconv.Itoa(core.MaxSettled+4)))
			Expect(recs[0].ID).To(Equal("c5"))
		})

		It("loads a snapshot saved before settle records existed", func() {
			store.data = []byte(`{"Landed":{"old":true}}`)
			d := clocked()
			admit(d, "a")
			steps(d, 3)
			snap := store.snap()
			Expect(snap.Landed).To(Equal(map[string]bool{"old": true, "a": true}))
			Expect(snap.Settled).To(HaveLen(1))
		})
	})

	It("A runner timeout is retried, then the queue pauses and ejects nothing", func() {
		run.timeout = true
		d := driver()
		admit(d, "a")
		admit(d, "b")
		steps(d, 6)
		Expect(run.starts).To(Equal([][]string{{"a", "b"}, {"a", "b"}}))
		for _, v := range note.of(core.VerdictIn) {
			Expect(v.Verdict).To(Equal(core.None))
		}
		paused := note.of(core.PausedEvent)
		Expect(paused).To(HaveLen(1))
		Expect(paused[0].Why).To(ContainSubstring("no verdict"))
		Expect(note.of(core.EjectedEvent)).To(BeEmpty())
		snap := store.snap()
		Expect(snap.Paused).To(BeTrue())
		Expect(snap.Why).To(Equal(paused[0].Why))
		Expect(ids(snap.Queued)).To(Equal([]string{"a", "b"}))
		Expect(snap.Ejected).To(BeEmpty())
	})

	It("A flaky batch lands and the flake is announced", func() {
		run.verdict = func(es []string) core.Verdict {
			if len(es) == 2 {
				return core.Fail
			}
			return core.Pass
		}
		d := driver()
		admit(d, "a")
		admit(d, "b")
		steps(d, 8)
		Expect(land.landed).To(Equal([]string{"a", "b"}))
		flakes := note.of(core.FlakeEvent)
		Expect(flakes).To(HaveLen(1))
		Expect(ids(flakes[0].Entries)).To(Equal([]string{"a", "b"}))
	})

	It("A failing notifier never blocks a landing", func() {
		note.err = errors.New("announcement refused")
		d := driver()
		admit(d, "a")
		steps(d, 3)
		Expect(land.landed).To(Equal([]string{"a"}))
		Expect(store.snap().Landed).To(Equal(map[string]bool{"a": true}))
		Expect(logs).To(ContainElement(ContainSubstring("announcement refused")))
	})

	It("A restart mid-run loses nothing and lands nothing twice", func() {
		run.kill = true
		d1 := driver()
		admit(d1, "a")
		admit(d1, "b")
		Expect(func() { _ = d1.Step(ctx) }).To(PanicWith("killed"))
		Expect(store.snap().InFlight).To(HaveLen(1), "the run is saved before it starts")

		run = &memRunner{} // the run never started
		land.kill = true
		d2 := driver()
		Expect(func() {
			for range 4 {
				_ = d2.Step(ctx)
			}
		}).To(PanicWith("killed"))
		Expect(land.landed).To(BeEmpty())

		land.kill = false
		land.onLand = func() { store.crash = true }
		d3 := driver()
		Expect(func() {
			for range 4 {
				_ = d3.Step(ctx)
			}
		}).To(PanicWith("killed"))
		Expect(land.landed).To(Equal([]string{"a", "b"}), "main moved before the crash")

		store.crash, land.onLand = false, nil
		d4 := driver()
		steps(d4, 6)
		Expect(land.landed).To(Equal([]string{"a", "b"}))
		Expect(note.of(core.EjectedEvent)).To(BeEmpty())
		snap := store.snap()
		Expect(snap.Queued).To(BeEmpty())
		Expect(snap.InFlight).To(BeEmpty())
		Expect(snap.Landed).To(Equal(map[string]bool{"a": true, "b": true}))
	})

	It("A crash right after landing never ejects what landed", func() {
		_, err := store.Save(ctx, 0, core.Snapshot{Version: "0", Queued: []core.Entry{entry("a")}})
		Expect(err).NotTo(HaveOccurred())
		verdicts := []core.Verdict{core.Pass, core.Fail} // the rerun flakes red
		run.verdict = func([]string) core.Verdict {
			v := verdicts[0]
			verdicts = verdicts[min(1, len(verdicts)-1):]
			return v
		}
		land.onLand = func() { store.crash = true }
		d1 := driver()
		Expect(func() {
			for range 2 {
				_ = d1.Step(ctx)
			}
		}).To(PanicWith("killed"))
		Expect(land.landed).To(Equal([]string{"a"}))

		store.crash, land.onLand = false, nil
		d2 := driver()
		steps(d2, 4)
		Expect(note.of(core.EjectedEvent)).To(BeEmpty())
		Expect(land.landed).To(Equal([]string{"a"}))
		snap := store.snap()
		Expect(snap.Landed).To(Equal(map[string]bool{"a": true}))
		Expect(snap.Ejected).To(BeEmpty())
		Expect(snap.Queued).To(BeEmpty())
	})

	It("pauses, ejecting nothing, when it cannot tell whether a saved landing reached main", func() {
		land.onLand = func() { store.crash = true }
		d1 := driver()
		admit(d1, "a")
		Expect(func() {
			for range 2 {
				_ = d1.Step(ctx)
			}
		}).To(PanicWith("killed"))

		store.crash, land.onLand, land.blind = false, nil, errors.New("main unreadable")
		run.verdict = failsWith("a")
		d2 := driver()
		steps(d2, 3)
		Expect(len(run.starts)).To(Equal(1), "nothing reruns while paused")
		snap := store.snap()
		Expect(snap.Paused).To(BeTrue())
		Expect(snap.Why).To(ContainSubstring("main unreadable"))
		Expect(snap.Ejected).To(BeEmpty())

		land.blind = nil
		Expect(d2.Resume(ctx, store.snap().PauseSeq)).To(Succeed())
		steps(d2, 2)
		Expect(store.snap().Landed).To(Equal(map[string]bool{"a": true}))
		Expect(store.snap().Ejected).To(BeEmpty())
	})

	It("drops its plan when another writer saved first, and ejects nothing on the other's verdict", func() {
		run.verdict = failsWith("b")
		d1 := driver()
		admit(d1, "a")
		d2 := driver()
		admit(d2, "b") // d1's snapshot is now stale
		Expect(d1.Step(ctx)).To(MatchError(ContainSubstring("stale save")))
		steps(d2, 1) // d2 starts its own run of a and b
		Expect(run.starts).To(Equal([][]string{{"a", "b"}}))
		_ = d1.Step(ctx) // d1 sees d2's red run
		Expect(note.of(core.EjectedEvent)).To(BeEmpty())
		Expect(store.snap().Ejected).To(BeEmpty())
	})

	It("never runs a change without the ancestor whose landing failed", func() {
		run.verdict = func(es []string) core.Verdict {
			if len(es) == 2 {
				return core.Fail
			}
			return core.Pass
		}
		land.fails = 1
		var alone [][]string
		run.onStart = func(es []string) {
			if slices.Contains(es, "b") && !slices.Contains(es, "a") && !slices.Contains(land.landed, "a") {
				alone = append(alone, es)
			}
		}
		d := driver()
		admit(d, "a")
		admit(d, "b", "a")
		for range 12 {
			_ = d.Step(ctx)
		}
		Expect(alone).To(BeEmpty(), "b ran without a, which never landed: %v", run.starts)
		Expect(land.landed).To(Equal([]string{"a", "b"}))
		Expect(store.snap().Ejected).To(BeEmpty())
	})

	It("A paused queue starts nothing until it is resumed", func() {
		_, err := store.Save(ctx, 0, core.Snapshot{Version: "0", Queued: []core.Entry{entry("a")}, Paused: true, Why: "no verdict after 1 retries"})
		Expect(err).NotTo(HaveOccurred())
		d := driver()
		steps(d, 3)
		Expect(run.starts).To(BeEmpty())
		Expect(d.Resume(ctx, store.snap().PauseSeq)).To(Succeed())
		Expect(note.of(core.ResumedEvent)).To(HaveLen(1))
		Expect(store.snap().Paused).To(BeFalse())
		steps(d, 3)
		Expect(land.landed).To(Equal([]string{"a"}))
	})

	It("resumes a strategy that paused itself mid-bisect", func() {
		halves := core.None
		run.verdict = func(es []string) core.Verdict {
			if len(es) == 2 {
				return core.Fail
			}
			return halves
		}
		d := driver()
		admit(d, "a")
		admit(d, "b")
		steps(d, 6)
		Expect(store.snap().Paused).To(BeTrue())
		halves = core.Pass
		Expect(d.Resume(ctx, store.snap().PauseSeq)).To(Succeed())
		steps(d, 4)
		Expect(land.landed).To(Equal([]string{"a", "b"}))
	})

	It("retries a failed landing and never records it as landed", func() {
		land.fails = 1
		d := driver()
		admit(d, "a")
		steps(d, 1)
		Expect(d.Step(ctx)).To(MatchError(ContainSubstring("main moved")))
		Expect(land.landed).To(BeEmpty())
		Expect(note.of(core.LandedEvent)).To(BeEmpty())
		Expect(ids(store.snap().Queued)).To(Equal([]string{"a"}))
		steps(d, 3)
		Expect(land.landed).To(Equal([]string{"a"}))
		Expect(note.of(core.LandedEvent)).To(HaveLen(1))
	})

	It("ejects only the change a compose conflict names", func() {
		comp.conflict = "b"
		d := driver()
		admit(d, "a")
		admit(d, "b")
		steps(d, 6)
		Expect(land.landed).To(Equal([]string{"a"}))
		ej := note.of(core.EjectedEvent)
		Expect(ej).To(HaveLen(1))
		Expect(ids(ej[0].Entries)).To(Equal([]string{"b"}))
	})

	It("never ejects what a second driver's admission raced past while landing", func() {
		max = 1
		_, err := store.Save(ctx, 0, core.Snapshot{Version: "0", Queued: []core.Entry{entry("a")}})
		Expect(err).NotTo(HaveOccurred())
		verdicts := []core.Verdict{core.Pass, core.Fail} // the rerun of a flakes red
		run.verdict = func([]string) core.Verdict {
			v := verdicts[0]
			verdicts = verdicts[min(1, len(verdicts)-1):]
			return v
		}
		d1, d2 := driverAs("d1"), driverAs("d2")
		var admitErr error
		land.before = func() { // 1. d1 saved its Landing and pauses before Land
			land.before = nil
			admitErr = d2.Admit(ctx, entry("b")) // 2. a fresh d2 admits b
		}
		land.onLand = func() { store.crash = true } // 3. d1 lands, then dies before the settle
		Expect(func() {
			for range 2 {
				_ = d1.Step(ctx)
			}
		}).To(PanicWith("killed"))
		Expect(land.landed).To(Equal([]string{"a"}))
		Expect(admitErr).To(MatchError(ContainSubstring("lease held by d1")))
		Expect(store.snap().Landing).NotTo(BeNil(), "d2 cleared nothing")

		store.crash, land.onLand = false, nil
		d3 := driverAs("d3") // 4. d3 picks up and runs
		Expect(d3.Step(ctx)).To(MatchError(ContainSubstring("lease held by d1")))
		expire()
		Expect(d3.Admit(ctx, entry("b"))).To(Succeed())
		for range 4 {
			_ = d3.Step(ctx)
		}
		snap := store.snap()
		Expect(snap.Ejected).NotTo(HaveKey("a"), "a is on main")
		Expect(snap.Landed).To(HaveKey("a"))
		Expect(land.landed).To(Equal([]string{"a"}))
	})

	It("A second driver cannot act on a queue another driver holds", func() {
		land.onLand = func() { store.crash = true }
		d1 := driverAs("d1")
		admit(d1, "a")
		Expect(func() {
			for range 2 {
				_ = d1.Step(ctx)
			}
		}).To(PanicWith("killed"))
		store.crash, land.onLand = false, nil
		before := store.snap()

		d2 := driverAs("d2")
		Expect(d2.Admit(ctx, entry("b"))).To(MatchError(ContainSubstring("lease held by d1")))
		Expect(d2.Step(ctx)).To(MatchError(ContainSubstring("lease held by d1")))
		Expect(d2.Resume(ctx, store.snap().PauseSeq)).To(MatchError(ContainSubstring("lease held by d1")))
		Expect(store.snap()).To(Equal(before), "no plan, no reconcile, no clearing")
		Expect(run.starts).To(HaveLen(1))
		Expect(land.fence>>32).To(Equal(uint64(1)), "fenced at d1's lease")
	})

	It("A stalled driver whose lease was taken over cannot land", func() {
		d1, d2 := driverAs("d1"), driverAs("d2")
		admit(d1, "a")
		steps(d1, 1)
		land.before = func() { // d1 stalls past its lease while landing a
			land.before = nil
			expire()
			admit(d2, "b") // d2 takes over, fences d1 out and clears its Landing
		}
		Expect(d1.Step(ctx)).To(MatchError(ContainSubstring("fenced")))
		Expect(land.landed).To(BeEmpty())
		Expect(store.snap().Landing).To(BeNil())
		Expect(d1.Step(ctx)).To(MatchError(ContainSubstring("lease held by d2")))
		steps(d2, 3)
		Expect(land.landed).To(Equal([]string{"a", "b"}))
		Expect(store.snap().Ejected).To(BeEmpty())
	})

	// attempt steps until the lander has been asked to land once more.
	attempt := func(d *core.Driver) {
		before := land.lands
		for range 6 {
			_ = d.Step(ctx)
			if land.lands > before {
				return
			}
		}
		Fail("no landing was attempted")
	}

	It("A landing refused because main moved is recomposed and never counts toward the pause", func() {
		land.moved = 5 // more than the 3 failures that pause
		d := driver()
		admit(d, "a")
		for range 5 {
			attempt(d)
			Expect(store.snap().Paused).To(BeFalse())
		}
		snap := store.snap()
		Expect(snap.Ejected).To(BeEmpty())
		Expect(ids(snap.Queued)).To(Equal([]string{"a"}))
		Expect(land.landed).To(BeEmpty())
		rec := note.of(core.RecomposeEvent)
		Expect(rec).To(HaveLen(5))
		Expect(rec[0].Why).To(ContainSubstring("main moved"))
		Expect(ids(rec[0].Entries)).To(Equal([]string{"a"}))
		Expect(note.of(core.PausedEvent)).To(BeEmpty())
		kept := 0
		for _, r := range snap.Settled {
			if r.Kind == core.RecomposeEvent {
				kept++
			}
		}
		Expect(kept).To(Equal(5))
		attempt(d)
		Expect(land.landed).To(Equal([]string{"a"}))
		Expect(store.snap().Landing).To(BeNil())
	})

	It("A recompose does not reset the count of other landing failures", func() {
		land.fails = 2
		d := driver()
		admit(d, "a")
		attempt(d)
		attempt(d)
		land.moved = 1
		attempt(d)
		Expect(store.snap().Paused).To(BeFalse())
		land.fails = 1
		attempt(d)
		Expect(store.snap().Paused).To(BeTrue(), "the third other failure still pauses")
	})

	It("Repeated landing failures pause the queue with the reason", func() {
		land.fails = 100
		d := driver()
		admit(d, "a")
		for range 12 {
			_ = d.Step(ctx)
		}
		Expect(land.lands).To(Equal(3))
		paused := note.of(core.PausedEvent)
		Expect(paused).To(HaveLen(1))
		Expect(paused[0].Why).To(Equal("landing failed 3 times: main moved"))
		snap := store.snap()
		Expect(snap.Paused).To(BeTrue())
		Expect(snap.Why).To(Equal(paused[0].Why))
		Expect(ids(snap.Queued)).To(Equal([]string{"a"}))
		Expect(snap.Ejected).To(BeEmpty())
		Expect(land.landed).To(BeEmpty())
	})

	It("resets the landing failure count on a landing", func() {
		land.fails = 2
		d := driver()
		admit(d, "a")
		for range 9 {
			_ = d.Step(ctx)
		}
		Expect(land.landed).To(Equal([]string{"a"}))
		land.fails = 2
		admit(d, "b")
		for range 9 {
			_ = d.Step(ctx)
		}
		Expect(land.landed).To(Equal([]string{"a", "b"}))
		Expect(note.of(core.PausedEvent)).To(BeEmpty())
	})

	It("A stalled driver's late save and start cannot change what a newer holder tests", func() {
		max = 2
		run.verdict = failsWith("b") // a passes; a and b together fail
		d1, d2 := driverAs("d1"), driverAs("d2")
		admit(d1, "a")
		comp.onCompose = func() { // 1. d1 composed a and stalls before saving its flight
			comp.onCompose = nil
			expire()
			_, err := store.Acquire(ctx, "d2", time.Minute) // 2. d2 takes the lease, not yet loaded
			Expect(err).NotTo(HaveOccurred())
		}
		admitB := func() {
			run.before = nil
			admit(d2, "b")
		}
		run.before = func() { // 3. d1's save went through; d2 starts a and b before d1's Start
			admitB()
			steps(d2, 1)
		}
		Expect(d1.Step(ctx)).To(MatchError(ContainSubstring("fenced")), "d1's save is refused")
		if run.before != nil { // d1 never reached Start
			admitB()
		}
		for range 10 {
			_ = d2.Step(ctx)
		}
		Expect(land.landed).NotTo(ContainElement("b"), "b landed on a's verdict")
		Expect(store.snap().Ejected).To(HaveKey("b"))
	})

	It("A landing that is still in flight can never land after the queue has moved on", func() {
		verdicts := []core.Verdict{core.Pass, core.Fail} // the rerun of a flakes red
		run.verdict = func([]string) core.Verdict {
			v := verdicts[0]
			verdicts = verdicts[min(1, len(verdicts)-1):]
			return v
		}
		land.hang = true // the push outlives the driver's wait
		var lateErr error
		run.onStart = func([]string) {
			if len(run.starts) == 2 { // the push arrives while a reruns
				lateErr = land.late()
			}
		}
		d := driver()
		admit(d, "a")
		for range 4 {
			_ = d.Step(ctx)
		}
		snap := store.snap()
		Expect(snap.Ejected["a"] && slices.Contains(land.landed, "a")).To(BeFalse(), "a is ejected though it is on main")
		Expect(lateErr).To(MatchError(ContainSubstring("fenced")))
		Expect(land.landed).To(BeEmpty())
	})

	It("resets the landing failure count on a reconciled landing", func() {
		land.hang = true
		d := driver()
		admit(d, "a")
		steps(d, 1)
		Expect(d.Step(ctx)).To(MatchError(ContainSubstring("timed out")))
		Expect(land.late()).To(Succeed()) // the push landed after all
		steps(d, 1)
		Expect(store.snap().Landed).To(HaveKey("a"))
		land.fails = 2
		admit(d, "b")
		for range 9 {
			_ = d.Step(ctx)
		}
		Expect(land.landed).To(Equal([]string{"a", "b"}))
		Expect(note.of(core.PausedEvent)).To(BeEmpty())
	})

	It("keeps the landing failure count when the pause cannot be saved", func() {
		land.fails = 100
		land.before = func() {
			if land.lands == 3 {
				store.refuse = 1 // the pause save fails
			}
		}
		d := driver()
		admit(d, "a")
		for range 12 {
			_ = d.Step(ctx)
		}
		Expect(land.lands).To(Equal(4), "the next failure pauses")
		paused := note.of(core.PausedEvent)
		Expect(paused).To(HaveLen(1))
		Expect(paused[0].Why).To(Equal("landing failed 4 times: main moved"))
		Expect(store.snap().Paused).To(BeTrue())
	})

	It("refuses to admit the main branch itself", func() {
		e := entry("a")
		e.Ref = "core"
		var mainRef *core.MainRefError
		Expect(errors.As(driver().Admit(ctx, e), &mainRef)).To(BeTrue())
	})

	Describe("admission of a change built on queued parents", func() {
		It("A change built on a merge of two queued changes is refused at admission with a clear reason", func() {
			d := driver()
			admit(d, "a")
			admit(d, "b")
			admit(d, "c", "a")
			var div *core.DivergentError
			Expect(errors.As(d.Admit(ctx, entry("x"), "c", "b"), &div)).To(BeTrue())
			Expect(div).To(Equal(&core.DivergentError{ID: "x", A: "a", B: "b"}))
			Expect(div.Error()).To(Equal("x builds on a merge of queued changes a and b; land them first or rebase onto one"))
		})
		It("keeps its own copy of the parents it was given", func() {
			d := driver()
			admit(d, "a")
			admit(d, "b")
			p := []string{"a"}
			admit(d, "c", p...)
			p[0] = "b"
			admit(d, "x", "a", "c")
		})
		It("admits a linear stack", func() {
			d := driver()
			admit(d, "a")
			admit(d, "b", "a")
			admit(d, "c", "b")
			admit(d, "d", "c", "a")
		})
		It("admits a change built on one queued parent", func() {
			d := driver()
			admit(d, "a")
			admit(d, "b", "a")
		})
		It("ignores a landed parent", func() {
			d := driver()
			admit(d, "a")
			steps(d, 3)
			Expect(store.snap().Landed).To(HaveKey("a"))
			admit(d, "b")
			admit(d, "c", "a", "b")
		})
		It("leaves the queue and the saved state unchanged after a refusal", func() {
			d := driver()
			admit(d, "a")
			admit(d, "b")
			before := store.snap()
			Expect(d.Admit(ctx, entry("x"), "a", "b")).To(HaveOccurred())
			Expect(store.snap()).To(Equal(before))
			Expect(d.Admit(ctx, entry("x"))).To(Succeed(), "the ID was never taken")
		})
	})
})
