package core

import (
	"fmt"
	"slices"
)

// Bisect proves blame in a red batch. It splits the batch into halves and
// tries the first half first, recursively, so a change is ejected only when
// it fails on its own. It does no I/O: the caller runs Next and passes each
// verdict to Record, then carries out the Decision Record returns with Apply.
type Bisect struct {
	policy  Policy
	pending []*part // sub-batches still to run, next first
	retries int     // no-verdict retries of the next sub-batch
	solo    *part   // the top suspect alone, its parent the whole red batch
	Flakes  [][]Entry
	Paused  bool
	Hits    int // hints proven by a red solo run
	Misses  int // hints whose solo run passed
}

// part is one sub-batch; a red part counts how many of its halves passed.
type part struct {
	entries []Entry
	parent  *part
	passed  int
}

// NewBisect starts a bisect of a batch of more than one entry that failed.
func NewBisect(red []Entry, p Policy) (*Bisect, error) {
	if len(red) < 2 {
		return nil, fmt.Errorf("bisect: need a batch of two or more, got %d", len(red))
	}
	b := &Bisect{policy: p}
	b.split(&part{entries: red})
	return b, nil
}

// NewSuspectBisect starts a bisect that first runs the top suspect alone. Red
// alone ejects it (a Hit) and the rest runs as one batch, bisected plainly
// if red. Green alone is a Miss: nothing settles and the batch is bisected.
func NewSuspectBisect(red []Entry, p Policy, s Suspects, failed []string, changed map[string][]string) (*Bisect, error) {
	b, err := NewBisect(red, p)
	if err != nil {
		return nil, err
	}
	for _, id := range s.Rank(failed, red, changed) {
		if i := slices.IndexFunc(red, func(e Entry) bool { return e.ID == id }); i >= 0 {
			b.solo = &part{entries: red[i : i+1 : i+1], parent: &part{entries: red}}
			b.pending = []*part{b.solo}
			break
		}
	}
	return b, nil
}

// Next is the sub-batch to run next, or nil once the bisect is done or paused.
func (b *Bisect) Next() []Entry {
	if b.Paused || len(b.pending) == 0 {
		return nil
	}
	return b.pending[0].entries
}

// Record takes the verdict for Next and decides it as Decide does. A red
// whose halves both pass is recorded in Flakes; no verdict retries, then pauses.
func (b *Bisect) Record(v Verdict) (Decision, error) {
	if b.Next() == nil {
		return "", fmt.Errorf("bisect: no sub-batch is waiting for a verdict")
	}
	cur := b.pending[0]
	d, err := Decide(v, cur.entries, b.retries, b.policy)
	switch {
	case err != nil:
		return "", err
	case d == Retry:
		b.retries++
		return d, nil
	case d == Pause:
		b.Paused = true
		return d, nil
	}
	b.retries, b.pending = 0, b.pending[1:]
	if cur == b.solo {
		return b.settleHint(d), nil
	}
	if d == Split {
		b.split(cur)
	} else if p := cur.parent; d == Land && p != nil {
		if p.passed++; p.passed == 2 {
			b.Flakes = append(b.Flakes, p.entries)
		}
	}
	return d, nil
}

// split puts the halves of a red part at the front, first half first.
func (b *Bisect) split(red *part) {
	mid := len(red.entries) / 2
	halves := []*part{
		{entries: red.entries[:mid], parent: red},
		{entries: red.entries[mid:], parent: red},
	}
	b.pending = append(halves, b.pending...)
}

// settleHint carries out the solo run's decision: Eject on a Hit, or Split of
// the whole batch on a Miss, which settles nothing.
func (b *Bisect) settleHint(d Decision) Decision {
	whole, suspect := b.solo.parent, b.solo.entries[0]
	b.solo = nil
	if d == Eject {
		b.Hits++
		rest := slices.DeleteFunc(slices.Clone(whole.entries), func(e Entry) bool { return e.ID == suspect.ID })
		b.pending = []*part{{entries: rest}}
		return Eject
	}
	b.Misses++
	b.split(whole)
	return Split
}
