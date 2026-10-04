package core

import (
	"fmt"
	"maps"
	"slices"
)

// Run is one test run a Strategy asks for.
type Run struct {
	ID      string
	Base    string  // "" = current main at compose time; else an in-flight run's ID, whose candidate it composes on
	Entries []Entry // whole stacks, ancestors first
}

// noVerdictWhy starts the reason of a pause for no verdict, the one pause that ends by itself.
const noVerdictWhy = "no verdict after "

// Settle is one decision about entries; Why is a one-line reason. An Eject
// with Cause ParentEjected names the ejected ancestor as Parent.
type Settle struct {
	Entries  []Entry
	Decision Decision // Land, Eject or Pause
	Why      string
	Cause    string
	Parent   string
}

// ParentEjected is the Cause of an entry ejected unrun: an ancestor was ejected.
const ParentEjected = "parent-ejected"

// Culprit is the Cause of an entry ejected for failing on its own.
const Culprit = "culprit"

// View is what a Strategy reads.
type View struct {
	Queued   []Entry
	BuildsOn map[string][]string
	Landed   map[string]bool
	Ejected  map[string]bool
	InFlight []Run
	Slots    int
	Prefix   string // starts every run ID, unique per lease and Strategy
	Main     string // main's sha now: the head read this Step, or the candidate just landed; "" if unknown
}

// Outcome is what one verdict settles, the runs it makes stale, and the flaky red batches it proved.
type Outcome struct {
	Settle []Settle
	Cancel []string
	Flakes [][]Entry
	Notes  []string // lines for the log on how the verdict was decided
}

// Strategy decides; it does no I/O and cannot write the queue. Plan also
// settles queued entries that must never run.
type Strategy interface {
	Plan(v View) ([]Run, []Settle)
	Record(v View, run string, verdict Verdict) (Outcome, error)
}

// Adaptive sizes Serial's batches: from Start, halved (floor Min) after a red
// batch, doubled (cap Max) after GrowAfter green batches in a row.
type Adaptive struct{ Start, Min, GrowAfter int }

// Serial runs one batch at a time on current main: the first Max entries of
// the batch FormBatch forms from the whole queue, so a deferred entry never
// holds back the entries behind it. A red batch is bisected; no verdict retries
// then pauses. Its state is this plain struct.
type Serial struct {
	Max    int
	Policy Policy
	Flakes [][]Entry // red batches whose halves all passed
	Paused bool
	// Adaptive, if set, sizes batches instead of Max; its state is only here, so a rebuilt Serial restarts at Start.
	Adaptive *Adaptive

	runs    int
	run     *Run
	batch   Batch
	retries int
	bisect  *Bisect
	size    int // adaptive: the current batch size, 0 until the first batch
	streak  int // adaptive: green landed batches in a row

	failed  []string            // the hint for the run in flight: its failed test names
	changed map[string][]string // and the files each of its entries changed
}

// Plan starts the next run if nothing is in flight and a slot is free.
func (s *Serial) Plan(v View) ([]Run, []Settle) {
	if orphans := Orphans(v); len(orphans) > 0 {
		return nil, orphans
	}
	if s.Paused || s.run != nil || len(v.InFlight) > 0 || v.Slots < 1 {
		return nil, nil
	}
	if s.bisect == nil && s.batch.entries == nil {
		s.batch = FormBatch(v.Queued, v.BuildsOn, v.Landed) // a prefix keeps its ancestors
		s.batch.entries = s.batch.entries[:min(s.limit(), len(s.batch.entries))]
	}
	entries := s.batch.entries
	if s.bisect != nil {
		entries = s.bisect.Next()
	}
	if len(entries) == 0 {
		s.batch = Batch{}
		return nil, nil
	}
	s.runs++
	s.run = &Run{ID: fmt.Sprintf("%sserial-%d", v.Prefix, s.runs), Entries: entries}
	return []Run{*s.run}, nil
}

// Resume clears a pause: the run it paused on, a bisect's too, runs again.
func (s *Serial) Resume() {
	s.Paused = false
	if s.bisect != nil {
		s.bisect.Paused = false
	}
}

// Record decides the verdict of the run in flight, as Decide and Bisect do. A
// flake the bisect proves on this verdict is in this Outcome, whatever path returns it.
func (s *Serial) Record(_ View, run string, verdict Verdict) (Outcome, error) {
	if s.run == nil || s.run.ID != run {
		return Outcome{}, fmt.Errorf("serial: run %q is not in flight", run)
	}
	cur := s.run.Entries
	var d Decision
	var err error
	var out Outcome
	if s.bisect != nil {
		proven := len(s.bisect.Flakes)
		d, err = s.bisect.Record(verdict)
		out.Flakes = slices.Clone(s.bisect.Flakes[proven:])
		s.Flakes = append(s.Flakes, out.Flakes...)
	} else {
		d, err = Decide(verdict, cur, s.retries, s.Policy)
	}
	if err != nil {
		return Outcome{}, err
	}
	s.run = nil
	switch d {
	case Land:
		out.Settle = append(out.Settle, Settle{Entries: cur, Decision: Land, Why: "passed on main"})
	case Eject:
		out.Settle = append(out.Settle, Settle{Entries: cur, Decision: Eject, Why: "failed on its own", Cause: Culprit})
	case Pause:
		s.Paused = true
		why := fmt.Sprintf("%s%d retries", noVerdictWhy, s.Policy.RetryNone)
		out.Settle = append(out.Settle, Settle{Entries: cur, Decision: Pause, Why: why})
		return out, nil
	case Retry:
		if s.bisect == nil {
			s.retries++
		}
		return out, nil
	case Split:
		if s.bisect == nil {
			if s.bisect, err = NewStackBisect(s.batch, s.Policy); err != nil {
				return Outcome{}, err
			}
			out.Notes = append(out.Notes, s.suspect())
		}
	}
	if s.bisect != nil {
		for _, o := range s.bisect.TakeOrphans() {
			why := fmt.Sprintf("builds on %q, which was ejected", o.Parent)
			out.Settle = append(out.Settle, Settle{Entries: []Entry{o.Entry}, Decision: Eject, Why: why, Cause: ParentEjected, Parent: o.Parent})
		}
		if s.bisect.Next() != nil {
			return out, nil
		}
	}
	s.adapt(s.bisect != nil || d == Eject)
	s.batch, s.retries, s.bisect = Batch{}, 0, nil
	return out, nil
}

func (s *Serial) limit() int {
	if a := s.Adaptive; a != nil {
		if s.size == 0 {
			s.size = a.Start
		}
		return min(s.size, s.Max)
	}
	return s.Max
}

func (s *Serial) adapt(red bool) {
	a := s.Adaptive
	if a == nil {
		return
	}
	if s.streak++; red {
		s.size, s.streak = max(a.Min, s.size/2), 0
	} else if s.streak >= a.GrowAfter {
		// double, saturating at Max: size+min(size, Max-size) never overflows
		s.size, s.streak = s.size+min(s.size, s.Max-s.size), 0
	}
}

// Orphans ejects each queued entry with an ejected ancestor, naming it as
// Parent; ancestry is FormBatch's with every other parent taken as landed.
func Orphans(v View) []Settle {
	all, landed := slices.Clone(v.Queued), map[string]bool{}
	for _, id := range slices.Sorted(maps.Keys(v.Ejected)) {
		if v.Ejected[id] {
			all = append(all, Entry{ID: id})
		}
	}
	for _, ps := range v.BuildsOn {
		for _, p := range ps {
			landed[p] = true // only read for a parent outside all
		}
	}
	b, out := FormBatch(all, v.BuildsOn, landed), []Settle(nil)
	for _, e := range v.Queued {
		if i := slices.IndexFunc(b.Ancestors(e.ID), func(a string) bool { return v.Ejected[a] }); i >= 0 {
			p := b.Ancestors(e.ID)[i]
			why := fmt.Sprintf("builds on %q, which was ejected", p)
			out = append(out, Settle{Entries: []Entry{e}, Decision: Eject, Why: why, Cause: ParentEjected, Parent: p})
		}
	}
	return out
}
