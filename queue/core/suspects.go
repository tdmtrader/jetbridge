package core

import (
	"context"
	"fmt"
	"slices"
)

// Hinter is an optional Strategy capability. WantsHint says whether a red
// verdict of run would use a hint; Hint hands it the run's failed test names
// and the files each of its entries changed, by ID, before Record of it.
type Hinter interface {
	WantsHint(run string) bool
	Hint(run string, failed []string, changed map[string][]string)
}

// WantsHint is true for the red verdict of a batch run in flight that would
// start a bisect.
func (s *Serial) WantsHint(run string) bool {
	return s.run != nil && s.run.ID == run && s.bisect == nil && len(s.run.Entries) > 1
}

// Hint keeps the failed test names and changed files of the run in flight.
func (s *Serial) Hint(_ string, failed []string, changed map[string][]string) {
	s.failed, s.changed = failed, changed
}

// suspect runs the top suspect of the new bisect alone first, if the hint
// names one in the batch, and says in one line how the bisect starts.
func (s *Serial) suspect() string {
	failed, changed := s.failed, s.changed
	if s.failed, s.changed = nil, nil; len(failed) == 0 {
		return "no failed-test names; bisecting by halves"
	}
	if top := Rank(failed, s.batch.entries, changed); len(top) > 0 && s.bisect.alone(top[0]) {
		return fmt.Sprintf("failed tests point at %q; running it alone first", top[0])
	}
	return "failed tests point at no change; bisecting by halves"
}

// alone puts the run of id, with any entries it builds on, first. It refuses
// a run that would be the whole batch.
func (b *Bisect) alone(id string) bool {
	red := b.batch.entries
	i := slices.IndexFunc(red, func(e Entry) bool { return e.ID == id })
	if i < 0 {
		return false
	}
	run, _ := b.carve(red, red[i:i+1])
	if len(run) == len(red) {
		return false
	}
	b.solo = &part{entries: run, parent: &part{entries: red}}
	b.pending = []*part{b.solo}
	return true
}

// settleHint: a red solo run ejects the suspect and the rest run as one batch;
// a red stacked run ejects nobody and the whole batch is bisected. A green run
// lands and the rest is bisected on the new main (a rest of one runs whole); a
// rest that passes in full means the batch was a flake.
func (b *Bisect) settleHint(d Decision) Decision {
	whole := b.solo.parent
	_, left := b.carve(whole.entries, b.solo.entries)
	b.solo = nil
	rest := &part{entries: left}
	switch {
	case d == Eject:
		b.pending = []*part{rest}
	case d == Split:
		b.split(whole)
	case len(left) == 1:
		whole.passed, rest.parent = 1, whole
		b.pending = []*part{rest}
	default:
		rest.flake = whole.entries
		b.split(rest)
	}
	return d
}

// flaked is what a flake of this part is recorded against.
func (p *part) flaked() []Entry {
	if p.flake != nil {
		return p.flake
	}
	return p.entries
}

// hint hands a Hinter strategy, if it wants one, the runner's failed test
// names for a red run and the files its entries changed. Missing either, the
// strategy bisects by halves.
func (d *Driver) hint(ctx context.Context, f Flight, v Verdict) {
	h, ok := d.st.(Hinter)
	if !ok || v != Fail || !h.WantsHint(f.Run.ID) {
		return
	}
	var failed []string
	if r, ok := d.Runner.(FailureReporter); ok {
		failed = r.FailedTests(f.Run.ID)
	}
	var changed map[string][]string
	if c, ok := d.Composer.(ChangeLister); ok && len(failed) > 0 {
		var err error
		if changed, err = c.Changed(ctx, f.Run.Entries); err != nil {
			d.logf("changed files %s: %v: no hint", f.Run.ID, err)
		}
	}
	h.Hint(f.Run.ID, failed, changed)
}
