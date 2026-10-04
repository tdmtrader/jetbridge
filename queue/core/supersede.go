package core

import (
	"context"
	"fmt"
	"maps"
	"slices"
)

// SupersededEvent: a queued change was replaced, in place, by a new commit of the same id.
const SupersededEvent EventKind = "superseded"

// Supersede replaces the commit of a queued entry, which keeps its place.
func (q *Queue) Supersede(id, commit string) error {
	i := slices.IndexFunc(q.order, func(e Entry) bool { return e.ID == id })
	if i < 0 || q.states[id] != Queued {
		return &RefusedError{ID: id, State: q.states[id]}
	}
	q.order[i].Commit = commit
	return nil
}

// divergent refuses id building on two queued changes, neither built on the other.
func (d *Driver) divergent(id string, buildsOn []string) error {
	b, queued := maps.Clone(d.s.BuildsOn), map[string]bool{}
	b[id] = buildsOn
	for q, s := range d.q.states {
		queued[q] = s == Queued
	}
	up := func(id string) []string { a, _, _ := reach(id, b, queued, nil); return a }
	anc := up(id)
	slices.Sort(anc)
	for i, x := range anc {
		for _, y := range anc[i+1:] {
			if !slices.Contains(up(x), y) && !slices.Contains(up(y), x) {
				return &DivergentError{id, x, y}
			}
		}
	}
	return nil
}

// Supersede replaces queued entry id with commit, on the queued changes it now
// builds on, and recomposes whatever was in flight. It refuses when other queued
// changes build on id, or the new commit is divergent.
func (d *Driver) Supersede(ctx context.Context, id, commit string, buildsOn []string) error {
	if err := d.hold(ctx); err != nil {
		return err
	}
	e, err := d.queuedAt(id, "")
	if err != nil {
		return err
	}
	if deps := d.dependents(id); len(deps) > 0 {
		return &DependentsError{id, deps}
	}
	buildsOn = slices.DeleteFunc(slices.Clone(buildsOn), func(o string) bool { return o == id || d.q.states[o] != Queued })
	if err := d.divergent(id, buildsOn); err != nil {
		return err
	}
	if err := d.q.Supersede(id, commit); err != nil {
		return err
	}
	old := d.s.Commits[id]
	d.s.Commits[id] = commit
	if delete(d.s.BuildsOn, id); len(buildsOn) > 0 {
		d.s.BuildsOn[id] = buildsOn
	}
	d.replan()
	e.Commit = commit
	ev := Event{Kind: SupersededEvent, Entries: []Entry{e}, Why: fmt.Sprintf("replaced %.7s", old), At: d.now()}
	d.settled(ev)
	if err := d.save(ctx); err != nil {
		return err
	}
	d.notify(ctx, ev)
	return nil
}
