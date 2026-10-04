package core

import (
	"fmt"
	"slices"
)

// RefusedError: the entry's State forbids the request (empty if never seen).
type RefusedError struct {
	ID    string
	State State
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("entry %q refused: state is %q", e.ID, e.State)
}

// MainRefError: the entry is on a protected branch, the queue's main itself.
type MainRefError struct {
	ID  string
	Ref string
}

func (e *MainRefError) Error() string {
	return fmt.Sprintf("entry %q refused: ref %q is the main branch", e.ID, e.Ref)
}

// DivergentError: the entry builds on two queued changes, neither built on the other.
type DivergentError struct{ ID, A, B string }

func (e *DivergentError) Error() string {
	return fmt.Sprintf("%s builds on a merge of queued changes %s and %s; land them first or rebase onto one", e.ID, e.A, e.B)
}

// Queue holds entries in admission order; the zero value is empty.
type Queue struct {
	// Protected names the configured main; "main" and "master" always count.
	Protected []string
	order     []Entry
	states    map[string]State
}

// Admit appends an entry. An ID seen before, in any state, is refused, and so
// is an entry whose ref is a protected main branch.
func (q *Queue) Admit(e Entry) error {
	if e.Ref == "main" || e.Ref == "master" || slices.Contains(q.Protected, e.Ref) {
		return &MainRefError{ID: e.ID, Ref: e.Ref}
	}
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

// Batch is the entries of one test run, normalised by FormBatch, the only place
// ancestry is computed: ancestors come before the entries that build on them.
type Batch struct {
	entries   []Entry
	ancestors map[string][]string
	cyclic    []string
}

func (b Batch) Entries() []Entry { return b.entries }

// Ancestors are the in-batch ancestors of id in FIFO order, found through every node.
func (b Batch) Ancestors(id string) []string { return b.ancestors[id] }

// Cyclic are the entries that lie on a cycle of buildsOn; the input is bad.
func (b Batch) Cyclic() []string { return b.cyclic }

// FormBatch forms the only valid Batch. landed says which IDs are Landed;
// buildsOn maps an entry to the IDs it builds on, followed through nodes
// outside the batch. An entry with an ancestor that is neither landed nor in
// the batch, or that sits on or builds on a cycle, is deferred: left out, still
// queued, neither run nor ejected. The rest are ordered ancestors first, then FIFO.
func FormBatch(entries []Entry, buildsOn map[string][]string, landed map[string]bool) Batch {
	b, pos := Batch{}, map[string]int{}
	for i, e := range entries {
		pos[e.ID] = i
	}
	for live := entries; ; {
		b.ancestors = map[string][]string{}
		in := map[string]bool{}
		for _, e := range live {
			in[e.ID] = true
		}
		var kept []Entry
		for _, e := range live {
			anc, cyclic, blocked := reach(e.ID, buildsOn, in, landed)
			if cyclic {
				b.cyclic = append(b.cyclic, e.ID)
			}
			if cyclic || blocked {
				continue
			}
			slices.SortFunc(anc, func(x, y string) int { return pos[x] - pos[y] })
			b.ancestors[e.ID] = anc
			kept = append(kept, e)
		}
		if len(kept) < len(live) {
			live = kept
			continue
		}
		done := map[string]bool{}
		for len(b.entries) < len(kept) { // Kahn: the ready entry first in FIFO order
			e := kept[slices.IndexFunc(kept, func(e Entry) bool {
				return !done[e.ID] && !slices.ContainsFunc(b.ancestors[e.ID], func(a string) bool { return !done[a] })
			})]
			done[e.ID], b.entries = true, append(b.entries, e)
		}
		return b
	}
}

// reach walks the ancestors of id; only an unlanded node outside the batch blocks.
func reach(id string, buildsOn map[string][]string, in, landed map[string]bool) (anc []string, cyclic, blocked bool) {
	seen := map[string]bool{}
	for todo := slices.Clone(buildsOn[id]); len(todo) > 0; todo = todo[1:] {
		switch p := todo[0]; {
		case p == id:
			cyclic = true
		case seen[p]:
		default:
			seen[p] = true
			if in[p] {
				anc = append(anc, p)
			} else if !landed[p] {
				blocked = true
			}
			todo = append(todo, buildsOn[p]...)
		}
	}
	return
}
