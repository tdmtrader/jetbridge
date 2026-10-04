package core

import "slices"

// urgentFirst is the one urgent lane: the urgent entries and the in-batch
// ancestors they build on come first, the rest after, each group in FIFO
// order. It only reorders, so it never drops an entry or puts a child ahead of
// a parent that FormBatch's ancestor pass would hold back. A batch already
// running is never re-formed, so an urgent entry cannot jump it.
func urgentFirst(kept []Entry, ancestors map[string][]string) []Entry {
	front := map[string]bool{}
	for _, e := range kept {
		if e.Urgent {
			front[e.ID] = true
			for _, a := range ancestors[e.ID] {
				front[a] = true
			}
		}
	}
	if len(front) == 0 {
		return kept
	}
	out := slices.DeleteFunc(slices.Clone(kept), func(e Entry) bool { return !front[e.ID] })
	return append(out, slices.DeleteFunc(slices.Clone(kept), func(e Entry) bool { return front[e.ID] })...)
}

// Promote marks a queued entry urgent; anything not queued is refused.
func (q *Queue) Promote(id string) error {
	if q.states[id] != Queued {
		return &RefusedError{ID: id, State: q.states[id]}
	}
	q.order[slices.IndexFunc(q.order, func(e Entry) bool { return e.ID == id })].Urgent = true
	return nil
}
