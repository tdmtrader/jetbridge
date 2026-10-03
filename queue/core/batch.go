package core

import "fmt"

// RefusedError: the entry's State forbids the request (empty if never seen).
type RefusedError struct {
	ID    string
	State State
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("entry %q refused: state is %q", e.ID, e.State)
}

// Queue holds entries in admission order; the zero value is empty.
type Queue struct {
	order  []Entry
	states map[string]State
}

// Admit appends an entry. An ID seen before, in any state, is refused.
func (q *Queue) Admit(e Entry) error {
	if s, seen := q.states[e.ID]; seen {
		return &RefusedError{ID: e.ID, State: s}
	}
	if q.states == nil {
		q.states = map[string]State{}
	}
	q.order = append(q.order, e)
	q.states[e.ID] = Queued
	return nil
}

// Settle moves a queued entry to a final state, Landed or Ejected.
func (q *Queue) Settle(id string, s State) error {
	if q.states[id] != Queued || (s != Landed && s != Ejected) {
		return &RefusedError{ID: id, State: q.states[id]}
	}
	q.states[id] = s
	return nil
}

// SelectBatch returns up to max queued entries from the front, in order.
func (q *Queue) SelectBatch(max int) []Entry {
	batch := []Entry{}
	for _, e := range q.order {
		if len(batch) < max && q.states[e.ID] == Queued {
			batch = append(batch, e)
		}
	}
	return batch
}
