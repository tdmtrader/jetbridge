package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"
)

// Driver carries out a Strategy's decisions through the ports and decides
// nothing itself. It saves the Snapshot before starting a run, before a land
// and after every settle. At start, and after any failed save or land, it drops
// all it holds and replans from the Store; a run its Strategy does not know is
// dropped and its entries rerun. A Landing found on load is settled by what
// main holds. Each call first takes or renews the Store's lease and does
// nothing without it. Every save carries the lease token, so the Store refuses
// a driver that stalled past its lease; every land and reconcile carries a new,
// higher fence, so the Lander refuses any earlier land, even this driver's own.
type Driver struct {
	Store       Store
	Composer    Composer
	Runner      Runner
	Lander      Lander
	Notifier    Notifier
	NewStrategy func() Strategy
	Main        string // the branch runs compose on and land to
	Slots       int    // runs in flight at once; 0 means 1
	Log         func(format string, args ...any)
	Owner       string           // names this driver in the lease; unique per process
	TTL         time.Duration    // how long a lease lasts unrenewed; 0 means a minute
	MaxFailures int              // land errors in a row before pausing; 0 means 3
	Admissions  Admissions       // drained at the start of each Step; nil means none
	Resumes     Resumes          // checked at the start of each Step; nil means none
	Promotes    Promotes         // checked at the start of each Step; nil means none
	Cooldown    time.Duration    // how long a no-verdict pause lasts before it ends by itself; 0 means never
	Now         func() time.Time // the clock for every timestamp; nil means time.Now

	lease  Lease
	fails  int    // land errors in a row
	prefix string // of every run ID the current Strategy names
	s      *Snapshot
	q      *Queue
	st     Strategy
}

// Admit queues an entry that builds on the given entry IDs.
func (d *Driver) Admit(ctx context.Context, e Entry, buildsOn ...string) error {
	if err := d.hold(ctx); err != nil {
		return err
	}
	buildsOn = slices.Clone(buildsOn) // the caller may reuse its slice
	b, queued := maps.Clone(d.s.BuildsOn), map[string]bool{}
	b[e.ID] = buildsOn
	for id, s := range d.q.states {
		queued[id] = s == Queued
	}
	up := func(id string) []string { a, _, _ := reach(id, b, queued, nil); return a }
	anc := up(e.ID)
	slices.Sort(anc)
	for i, x := range anc {
		for _, y := range anc[i+1:] {
			if !slices.Contains(up(x), y) && !slices.Contains(up(y), x) {
				return &DivergentError{e.ID, x, y}
			}
		}
	}
	if err := d.q.Admit(e); err != nil {
		return err
	}
	if len(buildsOn) > 0 {
		d.s.BuildsOn[e.ID] = buildsOn
	}
	d.s.Commits[e.ID] = e.Commit
	return d.save(ctx)
}

// drain admits each change in Admissions, on its parents still queued, and
// marks it done once saved. A refusal is kept and announced, never queued.
func (d *Driver) drain(ctx context.Context) error {
	if d.Admissions == nil {
		return nil
	}
	ps, err := d.Admissions.Pending(ctx, d.q.SelectBatch(math.MaxInt))
	if err != nil {
		d.logf("admissions: %v", err)
	}
	for _, p := range ps {
		_, seen := d.q.states[p.ID]
		if old := d.s.Commits[p.ID]; seen && old != p.Commit && p.Why == "" { // the same commit is a crash after the save
			p.Why, seen = fmt.Sprintf("id %s already used for %.7s; admit the new commit under a new id", p.ID, old), false
		}
		if b := slices.DeleteFunc(slices.Clone(p.BuildsOn), func(id string) bool { return d.q.states[id] != Queued }); !seen && p.Why == "" {
			if err := d.Admit(ctx, Entry{ID: p.ID, Commit: p.Commit, Owner: p.Owner, AdmittedAt: d.now()}, b...); refusal(err) {
				p.Why = err.Error()
			} else if err != nil {
				return err
			}
		}
		if !seen && p.Why != "" {
			d.s.Refused = append(d.s.Refused, Refusal{p.ID, p.Commit, p.Why})[max(0, len(d.s.Refused)+1-MaxRefused):]
			ev := Event{Kind: RefusedEvent, Entries: []Entry{{ID: p.ID, Commit: p.Commit}}, Why: p.Why, At: d.now()}
			d.settled(ev)
			if err := d.save(ctx); err != nil {
				return err
			}
			d.notify(ctx, ev)
		}
		if err := d.Admissions.Done(ctx, p.ID, p.Commit); err != nil {
			d.logf("admissions: done %s: %v", p.ID, err)
		}
	}
	return nil
}

// StaleResumeError refuses a resume of pause Seq: the queue is at pause At, or not paused.
type StaleResumeError struct {
	Seq, At uint64
	Paused  bool
}

func (e *StaleResumeError) Error() string {
	return fmt.Sprintf("resume of pause %d refused: the queue is at pause %d, paused %t", e.Seq, e.At, e.Paused)
}

// Resume clears pause number seq, the Strategy's own too if it keeps one. It
// checks seq against the snapshot it holds under the lease, which the save's
// compare-and-swap then proves current, so it never clears a later pause.
func (d *Driver) Resume(ctx context.Context, seq uint64) error {
	if err := d.hold(ctx); err != nil {
		return err
	}
	return d.resume(ctx, seq, "resume requested")
}

// resume ends pause seq and records it with why, in the settle records and as an event.
func (d *Driver) resume(ctx context.Context, seq uint64, why string) error {
	if !d.s.Paused || d.s.PauseSeq != seq {
		return &StaleResumeError{seq, d.s.PauseSeq, d.s.Paused}
	}
	if r, ok := d.st.(interface{ Resume() }); ok {
		r.Resume()
	}
	d.s.Paused, d.s.Why = false, ""
	if why != autoResumeWhy {
		d.s.ResumedOnMain = "" // a manual resume clears the hold
	}
	ev := Event{Kind: ResumedEvent, Why: why, At: d.now()}
	d.settled(ev)
	if err := d.save(ctx); err != nil {
		return err
	}
	if d.s.Landing != nil {
		d.reset() // reconcile it again before anything runs
	}
	d.notify(ctx, ev)
	return nil
}

// autoResume ends a no-verdict pause once the cool-down has passed; any other pause waits for an operator.
func (d *Driver) autoResume(ctx context.Context) error {
	s := d.s
	if !s.Paused || !noVerdict(s.Why) || d.Cooldown <= 0 {
		return nil
	}
	at := s.PausedAt
	for i := len(s.Settled) - 1; at.IsZero() && i >= 0; i-- { // saved before auto-resume: the pause's settle record has the time
		if s.Settled[i].Kind == PausedEvent {
			at = s.Settled[i].At
		}
	}
	if d.now().Sub(at) < d.Cooldown {
		return nil
	}
	return d.resumeOncePerMain(ctx)
}

// resumeRequested resumes a paused queue on request, then deletes the request
// after the save. A request ends only the pause it names; any other is deleted unheeded.
func (d *Driver) resumeRequested(ctx context.Context) error {
	if d.Resumes == nil {
		return nil
	}
	reqs, err := d.Resumes.Pending(ctx)
	if err != nil {
		d.logf("resume request: %v", err)
	}
	for _, r := range reqs {
		var stale *StaleResumeError
		if err := d.Resume(ctx, r.Seq); errors.As(err, &stale) {
			d.logf("resume request ignored: %v", err)
		} else if err != nil {
			return err
		}
		if err := d.Resumes.Done(ctx, r); err != nil {
			d.logf("resume request: done: %v", err)
		}
		if err := d.load(ctx); err != nil { // Resume may have dropped what the driver holds
			return err
		}
	}
	return nil
}

// Run calls Step every interval until ctx ends; a Step error is logged.
func (d *Driver) Run(ctx context.Context, every time.Duration) error {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := d.Step(ctx); err != nil {
			d.logf("step: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Step drains admissions, polls each run in flight once and records the
// finished ones, then, unless the queue is paused, starts what Plan asks for.
func (d *Driver) Step(ctx context.Context) error {
	if err := d.hold(ctx); err != nil {
		return err
	}
	if err := d.resumeRequested(ctx); err != nil {
		return err
	}
	if err := d.autoResume(ctx); err != nil {
		return err
	}
	if err := d.promoteRequested(ctx); err != nil {
		return err
	}
	if err := d.drain(ctx); err != nil {
		return err
	}
	for _, f := range slices.Clone(d.s.InFlight) {
		if _, ok := d.flight(f.Run.ID); !ok {
			continue // cancelled by an earlier verdict
		}
		v, done, err := d.Runner.Poll(ctx, f.Run.ID)
		if err != nil {
			d.logf("poll %s: %v: no verdict", f.Run.ID, err)
			v, done = None, true
		}
		if done {
			if err := d.record(ctx, f, v); err != nil || d.s == nil { // nil: a recompose dropped what the driver holds
				return err
			}
		}
	}
	if d.s.Paused {
		return nil
	}
	runs, settles := d.st.Plan(d.view())
	if err := d.apply(ctx, Flight{}, Outcome{Settle: settles}); err != nil || d.s == nil {
		return err
	}
	for _, r := range runs {
		if err := d.start(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

func (d *Driver) start(ctx context.Context, r Run) error {
	f, base, err := Flight{Run: r}, d.Main, error(nil)
	if r.Base != "" {
		b, ok := d.flight(r.Base)
		if base = b.Candidate; !ok {
			err = fmt.Errorf("base run %q is not in flight", r.Base)
		}
	}
	if err == nil {
		f.Candidate, err = d.Composer.Compose(ctx, base, r.Entries)
	}
	if err != nil {
		d.logf("compose %s: %v", r.ID, err)
		return d.record(ctx, f, ComposeVerdict(err, r.Entries))
	}
	d.s.InFlight = append(d.s.InFlight, f)
	if err := d.save(ctx); err != nil {
		return err
	}
	d.notify(ctx, Event{Kind: BatchStarted, Entries: r.Entries, Run: r})
	if err := d.Runner.Start(ctx, r, f.Candidate); err != nil {
		d.logf("start %s: %v: no verdict", r.ID, err)
		return d.record(ctx, f, None)
	}
	return nil
}

// record applies the Strategy's Outcome for a verdict. A verdict it refuses
// (a run from before a restart) is dropped; its entries stay queued.
func (d *Driver) record(ctx context.Context, f Flight, v Verdict) error {
	d.notify(ctx, Event{Kind: VerdictIn, Entries: f.Run.Entries, Run: f.Run, Verdict: v})
	d.hint(ctx, f, v)
	out, err := d.st.Record(d.view(), f.Run.ID, v)
	for _, n := range out.Notes {
		d.logf("%s: %s", f.Run.ID, n)
	}
	d.drop(f.Run.ID)
	if err != nil {
		d.logf("record %s: %v: verdict dropped", f.Run.ID, err)
		return d.save(ctx)
	}
	return d.apply(ctx, f, out)
}

// apply carries out an Outcome of run f, saving after each settle and before
// its event. A land that fails settles nothing and resets the driver.
func (d *Driver) apply(ctx context.Context, f Flight, out Outcome) error {
	var flakes []Event // recorded first, so the save of the outcome carries them
	for _, es := range out.Flakes {
		ev := Event{Kind: FlakeEvent, Entries: es, Run: f.Run, Why: "red as a batch, green when split", At: d.now()}
		d.settled(ev)
		flakes = append(flakes, ev)
	}
	for _, st := range out.Settle {
		ev := Event{Entries: st.Entries, Run: f.Run, Why: st.Why, Cause: st.Cause, Parent: st.Parent, At: d.now()}
		switch st.Decision {
		case Land:
			if f.Candidate == "" {
				return fmt.Errorf("driver: a land needs the run that tested it")
			}
			d.s.Landing = &Landing{Main: d.Main, Candidate: f.Candidate, Entries: st.Entries, Fence: d.fence()}
			if err := d.save(ctx); err != nil {
				return err
			}
			if err := LandBatch(ctx, d.Lander, d.q, d.Main, f.Candidate, d.s.Landing.Fence, st.Entries); err != nil {
				return d.landFailed(ctx, f, st.Entries, err)
			}
			d.fails, ev.Kind, d.s.Landing = 0, LandedEvent, nil
			mark(d.s.Landed, st.Entries)
		case Eject:
			if err := Apply(d.q, Eject, st.Entries); err != nil {
				d.logf("eject: %v", err)
				continue
			}
			ev.Kind = EjectedEvent
			mark(d.s.Ejected, st.Entries)
		case Pause:
			ev.Kind = PausedEvent
			d.pause(st.Why)
		default:
			return fmt.Errorf("driver: cannot settle %q", st.Decision)
		}
		d.settled(ev)
		if err := d.save(ctx); err != nil {
			return err
		}
		d.notify(ctx, ev)
	}
	for _, id := range out.Cancel {
		d.drop(id)
	}
	if err := d.save(ctx); err != nil {
		return err
	}
	for _, ev := range flakes {
		d.notify(ctx, ev)
	}
	return nil
}

// landFailed resets the driver so the next Step settles the saved Landing. On
// the MaxFailures-th error in a row it first saves a pause, keeping the
// Landing; the count restarts only once the pause is saved.
func (d *Driver) landFailed(ctx context.Context, f Flight, es []Entry, err error) error {
	var moved *MainMovedError
	if errors.As(err, &moved) {
		return d.recompose(ctx, f, es, moved)
	}
	if d.fails++; d.fails >= cmp.Or(d.MaxFailures, 3) {
		why := fmt.Sprintf("landing failed %d times: %v", d.fails, err)
		ev := Event{Kind: PausedEvent, Entries: es, Run: f.Run, Why: why, At: d.now()}
		d.pause(why)
		d.settled(ev)
		if d.save(ctx) == nil {
			d.fails = 0
			d.notify(ctx, ev)
		}
	}
	d.reset()
	return fmt.Errorf("land %s: %w", f.Run.ID, err)
}

// recompose records that main moved under a batch that passed: nothing was
// pushed and nothing is settled, so the batch is composed again on the new main
// and retested. It is no land error, so it neither counts toward the pause nor
// clears that count. The saved Landing is reconciled on the next Step.
func (d *Driver) recompose(ctx context.Context, f Flight, es []Entry, moved *MainMovedError) error {
	ev := Event{Kind: RecomposeEvent, Entries: es, Run: f.Run, Why: moved.Error(), At: d.now()}
	d.settled(ev)
	if d.save(ctx) == nil {
		d.notify(ctx, ev)
	}
	d.reset()
	return nil
}

// hold takes or renews the lease, then loads. A new token means another
// driver may have acted meanwhile, so all the driver holds is dropped first.
func (d *Driver) hold(ctx context.Context) error {
	l, err := d.Store.Acquire(ctx, d.Owner, cmp.Or(d.TTL, time.Minute))
	if err != nil {
		d.reset()
		return fmt.Errorf("lease: %w", err)
	}
	if l.Token != d.lease.Token {
		d.reset()
	}
	d.lease = l
	return d.load(ctx)
}

// load rebuilds the Queue and a fresh Strategy from the Snapshot and settles a
// saved Landing; if main cannot be read it pauses.
func (d *Driver) load(ctx context.Context) error {
	if d.s != nil {
		return nil
	}
	s, err := d.Store.Load(ctx)
	if err != nil {
		return err
	}
	s = RedactSnapshot(s) // a reason saved before its secret was known
	q, settled := &Queue{}, map[string]State{}
	for id := range s.Landed {
		settled[id] = Landed
	}
	for id := range s.Ejected {
		settled[id] = Ejected
	}
	for id, st := range settled {
		if err := errors.Join(q.Admit(Entry{ID: id}), q.Settle(id, st)); err != nil {
			return err
		}
	}
	for _, e := range s.Queued {
		if err := q.Admit(e); err != nil {
			return err
		}
	}
	q.Protected = []string{d.Main}
	s.Landed, s.Ejected, s.BuildsOn, s.Commits = orEmpty(s.Landed), orEmpty(s.Ejected), orEmpty(s.BuildsOn), orEmpty(s.Commits)
	d.s, d.q, d.st = &s, q, d.NewStrategy()
	if s.Fence>>32 > d.lease.Token { // a newer holder has acted, so this lease is stale
		d.reset()
		return fmt.Errorf("lease: the queue belongs to newer lease %d", s.Fence>>32)
	}
	d.prefix = fmt.Sprintf("t%d.%d-", d.lease.Token, uint32(d.fence()))
	in := s.Landing
	if in == nil {
		return nil
	}
	ev := Event{Kind: LandedEvent, Entries: in.Entries, Why: "landed before a restart", At: d.now()}
	again := false // already paused for this reason: a restart is no new pause
	landed, err := ReconcileLanding(ctx, d.Lander, d.q, *in, d.fence())
	if err != nil {
		ev = Event{Kind: PausedEvent, Entries: in.Entries, Why: fmt.Sprintf("cannot tell whether main holds %s: %v", in.Candidate, err)}
		ev.At = d.now()
		again = d.s.Paused && d.s.Why == Redact(ev.Why) // as saved
		d.pause(ev.Why)
	} else if d.s.Landing = nil; landed {
		d.fails = 0
		mark(d.s.Landed, in.Entries)
	}
	if (err != nil || landed) && !again {
		d.settled(ev)
	}
	if err := d.save(ctx); err != nil {
		return err
	}
	if err != nil || landed {
		d.notify(ctx, ev)
	}
	return nil
}

func (d *Driver) save(ctx context.Context) error {
	d.s.Queued = d.q.SelectBatch(math.MaxInt)
	*d.s = RedactSnapshot(*d.s) // the one place a reason enters the Store
	v, err := d.Store.Save(ctx, d.lease.Token, *d.s)
	if err != nil {
		d.reset()
		return err
	}
	d.s.Version = v
	return nil
}

// fence is the next fence, token<<32 | sequence, above every one saved; a Land's is saved first.
func (d *Driver) fence() uint64 {
	d.s.Fence = max(d.s.Fence+1, d.lease.Token<<32)
	return d.s.Fence
}

// pause marks the queue paused; only a pause that was not already one is a new pause.
func (d *Driver) pause(why string) {
	if !d.s.Paused {
		d.s.PauseSeq++
		d.s.PausedAt = d.now()
	}
	d.s.Paused, d.s.Why = true, why
}

func (d *Driver) reset() { d.s, d.q, d.st = nil, nil, nil }

func (d *Driver) view() View {
	runs := []Run{}
	for _, f := range d.s.InFlight {
		runs = append(runs, f.Run)
	}
	return View{Queued: d.q.SelectBatch(math.MaxInt), BuildsOn: d.s.BuildsOn, Landed: d.s.Landed,
		Ejected: d.s.Ejected, InFlight: runs, Slots: max(0, max(d.Slots, 1)-len(runs)), Prefix: d.prefix}
}

func (d *Driver) flight(id string) (Flight, bool) {
	i := slices.IndexFunc(d.s.InFlight, func(f Flight) bool { return f.Run.ID == id })
	if i < 0 {
		return Flight{}, false
	}
	return d.s.InFlight[i], true
}

func (d *Driver) drop(id string) {
	d.s.InFlight = slices.DeleteFunc(d.s.InFlight, func(f Flight) bool { return f.Run.ID == id })
}

func (d *Driver) now() time.Time {
	if d.Now == nil {
		return time.Now().UTC()
	}
	return d.Now().UTC()
}

func (d *Driver) settled(ev Event) {
	var batch []string
	for _, e := range ev.Entries {
		batch = append(batch, e.ID)
	}
	es := ev.Entries
	if len(es) == 0 || ev.Kind == FlakeEvent { // a flaky batch is one record naming all its entries
		es = []Entry{{ID: strings.Join(batch, ", ")}}
	}
	for _, e := range es {
		d.s.Settled = append(d.s.Settled, SettleRecord{e.ID, e.Commit, ev.Kind, ev.At, e.AdmittedAt, ev.Why, ev.Cause, ev.Run.ID, batch, e.Owner, ev.Base})
	}
	d.s.Settled = d.s.Settled[max(0, len(d.s.Settled)-MaxSettled):]
}

func (d *Driver) notify(ctx context.Context, e Event) {
	if e.At.IsZero() {
		e.At = d.now()
	}
	e.Why = Redact(e.Why)
	if err := d.Notifier.Notify(ctx, e); err != nil {
		d.logf("notify %s: %v", e.Kind, err)
	}
}

func (d *Driver) logf(format string, args ...any) {
	if d.Log == nil {
		return
	}
	d.Log("%s", Redact(fmt.Sprintf(format, args...)))
}

func refusal(err error) bool {
	var div *DivergentError
	var refused *RefusedError
	var main *MainRefError
	return errors.As(err, &div) || errors.As(err, &refused) || errors.As(err, &main)
}

func mark(m map[string]bool, es []Entry) {
	for _, e := range es {
		m[e.ID] = true
	}
}

func orEmpty[V any](m map[string]V) map[string]V {
	if m == nil {
		return map[string]V{}
	}
	return m
}
