package core

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
)

// WithdrawnEvent: a queued change was removed by its author; never a blame.
const WithdrawnEvent EventKind = "withdrawn"

// LifecycleRequest asks to act on change ID at Commit, the one the requester saw
// queued, so it never acts on a later change under the same id. Kind names the
// act; SHA is what the request holds.
type LifecycleRequest struct {
	Kind       EventKind
	ID, Commit string
	SHA        string
}

// Lifecycle is where an author's requests wait: Pending lists them; Done removes
// one only if it still holds the same SHA.
type Lifecycle interface {
	Pending(ctx context.Context) ([]LifecycleRequest, error)
	Done(ctx context.Context, r LifecycleRequest) error
}

// DependentsError: other queued changes build on the entry, so it cannot leave.
type DependentsError struct {
	ID         string
	Dependents []string
}

func (e *DependentsError) Error() string {
	return fmt.Sprintf("entry %q refused: queued %s build on it; act on them first", e.ID, strings.Join(e.Dependents, ", "))
}

// StaleRequestError: the request names a commit the entry is no longer at.
type StaleRequestError struct{ ID, Commit string }

func (e *StaleRequestError) Error() string {
	return fmt.Sprintf("request for %s at %.7s ignored: the entry is not at that commit", e.ID, e.Commit)
}

// Withdraw removes a queued entry from the Queue, so its id is unseen again.
func (q *Queue) Withdraw(id string) error {
	if q.states[id] != Queued {
		return &RefusedError{ID: id, State: q.states[id]}
	}
	q.order = slices.DeleteFunc(q.order, func(e Entry) bool { return e.ID == id })
	delete(q.states, id)
	return nil
}

// queuedAt finds the queued entry id, which must be at commit when commit is set.
func (d *Driver) queuedAt(id, commit string) (Entry, error) {
	i := slices.IndexFunc(d.q.order, func(e Entry) bool { return e.ID == id })
	if i < 0 || d.q.states[id] != Queued {
		return Entry{}, &RefusedError{ID: id, State: d.q.states[id]}
	}
	e := d.q.order[i]
	if commit != "" && e.Commit != commit {
		return Entry{}, &StaleRequestError{id, commit}
	}
	return e, nil
}

// dependents are the queued entries that build directly on id.
func (d *Driver) dependents(id string) []string {
	var out []string
	for _, e := range d.q.SelectBatch(math.MaxInt) {
		if slices.Contains(d.s.BuildsOn[e.ID], id) {
			out = append(out, e.ID)
		}
	}
	return out
}

// replan drops every run in flight and the Strategy's plan, so the next Step
// plans again from the queue as it now is. The runs it forgets are never polled.
func (d *Driver) replan() {
	d.s.InFlight, d.st = nil, d.NewStrategy()
	d.prefix = fmt.Sprintf("t%d.%d-", d.lease.Token, uint32(d.fence()))
}

// Withdraw removes queued entry id, at commit if given, and recomposes whatever
// was in flight without it. It is announced and kept as a settle record, but
// the entry is neither ejected nor blamed.
func (d *Driver) Withdraw(ctx context.Context, id, commit string) error {
	if err := d.hold(ctx); err != nil {
		return err
	}
	e, err := d.queuedAt(id, commit)
	if err != nil {
		return err
	}
	if deps := d.dependents(id); len(deps) > 0 {
		return &DependentsError{id, deps}
	}
	if err := d.q.Withdraw(id); err != nil {
		return err
	}
	delete(d.s.BuildsOn, id)
	d.replan()
	ev := Event{Kind: WithdrawnEvent, Entries: []Entry{e}, Why: "withdrawn by its author", At: d.now()}
	d.settled(ev)
	if err := d.save(ctx); err != nil {
		return err
	}
	d.notify(ctx, ev)
	return nil
}

// lifecycleRequested acts on each request, then deletes it after the save. A
// request the queue refuses, or that names an earlier commit, is deleted unheeded.
func (d *Driver) lifecycleRequested(ctx context.Context) error {
	if d.Lifecycle == nil {
		return nil
	}
	reqs, err := d.Lifecycle.Pending(ctx)
	if err != nil {
		d.logf("lifecycle request: %v", err)
	}
	for _, r := range reqs {
		switch r.Kind {
		case WithdrawnEvent:
			err = d.Withdraw(ctx, r.ID, r.Commit)
		default:
			err = &StaleRequestError{r.ID, r.Commit}
		}
		if ignored(err) {
			d.logf("%s request ignored: %v", r.Kind, err)
		} else if err != nil {
			return err
		}
		if err := d.Lifecycle.Done(ctx, r); err != nil {
			d.logf("lifecycle request: done: %v", err)
		}
		if err := d.load(ctx); err != nil {
			return err
		}
	}
	return nil
}

func ignored(err error) bool {
	var deps *DependentsError
	var stale *StaleRequestError
	var refused *RefusedError
	return errors.As(err, &deps) || errors.As(err, &stale) || errors.As(err, &refused)
}
