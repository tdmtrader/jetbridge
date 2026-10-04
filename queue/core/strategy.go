package core

import (
	"fmt"
	"maps"
	"slices"
)

// Run is one test run a Strategy asks for.
type Run struct {
	ID      string
	Base    string  // "" = current main at compose time; else an in-flight run's ID
	Entries []Entry // whole stacks, ancestors first
}

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
}

// Outcome is what one verdict settles, the runs it makes stale, and the flaky red batches it proved.
type Outcome struct {
	Settle []Settle
	Cancel []string
	Flakes [][]Entry
}

// Strategy decides; it does no I/O and cannot write the queue. Plan also
// settles queued entries that must never run.
type Strategy interface {
	Plan(v View) ([]Run, []Settle)
	Record(v View, run string, verdict Verdict) (Outcome, error)
}

// Serial runs one batch at a time on current main: the first Max entries of
// the batch FormBatch forms from the whole queue, so a deferred entry never
// holds back the entries behind it. A red batch is bisected; no verdict retries
// then pauses. Its state is this plain struct.
type Serial struct {
	Max    int
	Policy Policy
	Flakes [][]Entry // red batches whose halves all passed
	Paused bool

	runs    int
	run     *Run
	batch   Batch
	retries int
	bisect  *Bisect
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
		s.batch.entries = s.batch.entries[:min(s.Max, len(s.batch.entries))]
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

// Record decides the verdict of the run in flight, as Decide and Bisect do.
func (s *Serial) Record(_ View, run string, verdict Verdict) (Outcome, error) {
	if s.run == nil || s.run.ID != run {
		return Outcome{}, fmt.Errorf("serial: run %q is not in flight", run)
	}
	cur := s.run.Entries
	var d Decision
	var err error
	if s.bisect != nil {
		d, err = s.bisect.Record(verdict)
	} else {
		d, err = Decide(verdict, cur, s.retries, s.Policy)
	}
	if err != nil {
		return Outcome{}, err
	}
	s.run = nil
	var out Outcome
	switch d {
	case Land:
		out.Settle = append(out.Settle, Settle{Entries: cur, Decision: Land, Why: "passed on main"})
	case Eject:
		out.Settle = append(out.Settle, Settle{Entries: cur, Decision: Eject, Why: "failed on its own", Cause: Culprit})
	case Pause:
		s.Paused = true
		why := fmt.Sprintf("no verdict after %d retries", s.Policy.RetryNone)
		return Outcome{Settle: []Settle{{Entries: cur, Decision: Pause, Why: why}}}, nil
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
		s.Flakes = append(s.Flakes, s.bisect.Flakes...)
		out.Flakes = s.bisect.Flakes
	}
	s.batch, s.retries, s.bisect = Batch{}, 0, nil
	return out, nil
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
