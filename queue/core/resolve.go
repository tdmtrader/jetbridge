package core

import (
	"context"
	"slices"
)

// ResolvedEvent: an eject was cleared, so the id may be admitted again.
const ResolvedEvent EventKind = "resolved"

// Resolve forgets an ejected entry, so its id is unseen again.
func (q *Queue) Resolve(id string) error {
	if q.states[id] != Ejected {
		return &RefusedError{ID: id, State: q.states[id]}
	}
	q.order = slices.DeleteFunc(q.order, func(e Entry) bool { return e.ID == id })
	delete(q.states, id)
	return nil
}

// Resolve clears the eject of id, at commit if given. The id can be admitted
// again; changes ejected because they built on it stay ejected until resolved.
func (d *Driver) Resolve(ctx context.Context, id, commit string) error {
	if err := d.hold(ctx); err != nil {
		return err
	}
	if old := d.s.Commits[id]; commit != "" && old != commit && d.q.states[id] == Ejected {
		return &StaleRequestError{id, commit}
	}
	if err := d.q.Resolve(id); err != nil {
		return err
	}
	delete(d.s.Ejected, id)
	ev := Event{Kind: ResolvedEvent, Entries: []Entry{{ID: id, Commit: d.s.Commits[id]}}, Why: "eject resolved; the id can be admitted again", At: d.now()}
	d.settled(ev)
	if err := d.save(ctx); err != nil {
		return err
	}
	d.notify(ctx, ev)
	return nil
}
