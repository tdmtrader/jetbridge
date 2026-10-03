package core

import "fmt"

// Bisect proves blame in a red batch. It splits the batch into halves and
// tries the first half first, recursively, so a change is ejected only when
// it fails on its own. It does no I/O: the caller runs Next and passes each
// verdict to Record, then carries out the Decision Record returns with Apply.
type Bisect struct {
	policy  Policy
	pending []*part // sub-batches still to run, next first
	retries int     // no-verdict retries of the next sub-batch
	Flakes  [][]Entry
	Paused  bool
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
