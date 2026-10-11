package core

import (
	"fmt"
	"slices"
)

// Bisect proves blame in a red batch by trying the first half first,
// recursively, so a change is ejected only when it fails on its own. It does no
// I/O: the caller runs Next, passes each verdict to Record and applies the Decision.
type Bisect struct {
	policy  Policy
	pending []*part // sub-batches still to run, next first
	retries int     // no-verdict retries of the next sub-batch
	solo    *part   // the suspect's run, its parent the whole red batch
	Flakes  [][]Entry
	Paused  bool
	batch   Batch
	orphans []Orphan
}

// Orphan is an entry ejected unrun because an entry it builds on was ejected as
// proven red. The caller settles it Ejected, with Parent as the cause.
type Orphan struct {
	Entry  Entry
	Parent string
}

// part is one sub-batch; a red part counts how many of its halves passed.
// flake, if set, is what a flake is recorded against instead of entries.
type part struct {
	entries []Entry
	parent  *part
	passed  int
	flake   []Entry
}

// NewStackBisect starts a bisect of a failed Batch of two or more entries.
func NewStackBisect(batch Batch, p Policy) (*Bisect, error) {
	red := batch.entries
	if len(red) < 2 {
		return nil, fmt.Errorf("bisect: need a batch of two or more, got %d", len(red))
	}
	b := &Bisect{policy: p, batch: batch}
	b.split(&part{entries: red})
	return b, nil
}

// carve splits entries into the run of seeds closed under Ancestors, in batch
// order, and the rest. An ancestor outside entries has landed.
func (b *Bisect) carve(entries, seeds []Entry) (run, rest []Entry) {
	for _, e := range entries {
		if slices.ContainsFunc(seeds, func(s Entry) bool { return s == e || slices.Contains(b.batch.Ancestors(s.ID), e.ID) }) {
			run = append(run, e)
		} else {
			rest = append(rest, e)
		}
	}
	return run, rest
}

// Next is the sub-batch to run next, or nil once done or paused. The caller
// composes it on current main, which already holds every landed sub-batch.
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
		d = b.settleHint(d)
	} else if d == Split {
		b.split(cur)
	} else if p := cur.parent; d == Land && p != nil {
		if p.passed++; p.passed == 2 {
			b.Flakes = append(b.Flakes, p.flaked())
		}
	}
	if d == Eject {
		b.orphan(cur.entries[0].ID)
	}
	return d, nil
}

// TakeOrphans returns the entries ejected unrun since the last call.
func (b *Bisect) TakeOrphans() []Orphan {
	o := b.orphans
	b.orphans = nil
	return o
}

// orphan removes from every pending sub-batch each entry whose Ancestors hold
// the ejected one, so none runs without it; a sub-batch left empty is dropped.
func (b *Bisect) orphan(ejected string) {
	kept := b.pending[:0]
	for _, p := range b.pending {
		p.entries = slices.DeleteFunc(slices.Clone(p.entries), func(e Entry) bool {
			gone := slices.Contains(b.batch.Ancestors(e.ID), ejected)
			if gone {
				b.orphans = append(b.orphans, Orphan{e, ejected})
			}
			return gone
		})
		if len(p.entries) > 0 {
			kept = append(kept, p)
		}
	}
	b.pending = kept
}

// split puts the halves of a red part at the front, first half first.
func (b *Bisect) split(red *part) {
	first, second := b.carve(red.entries, red.entries[:len(red.entries)/2])
	b.pending = append([]*part{{entries: first, parent: red}, {entries: second, parent: red}}, b.pending...)
}
