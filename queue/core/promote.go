package core

import "context"

// PromoteRequest asks to move queued change ID into the urgent lane; SHA is what the request holds.
type PromoteRequest struct{ ID, SHA string }

// Promotes is where an operator's promote requests wait: Pending lists them;
// Done removes one only if it still holds the same SHA.
type Promotes interface {
	Pending(ctx context.Context) ([]PromoteRequest, error)
	Done(ctx context.Context, r PromoteRequest) error
}

// PromotedEvent is a change moved into the urgent lane.
const PromotedEvent EventKind = "promoted"

// promoteRequested promotes each requested change, records it or its refusal
// in the settle records, saves, then deletes the request.
func (d *Driver) promoteRequested(ctx context.Context) error {
	if d.Promotes == nil {
		return nil
	}
	reqs, err := d.Promotes.Pending(ctx)
	if err != nil {
		d.logf("promote request: %v", err)
	}
	for _, r := range reqs {
		ev := Event{Kind: PromotedEvent, Entries: []Entry{{ID: r.ID, Commit: d.s.Commits[r.ID]}}, Why: "promoted " + r.ID + " to the urgent lane", At: d.now()}
		if err := d.q.Promote(r.ID); err != nil {
			ev.Kind, ev.Why = RefusedEvent, "promote of "+r.ID+" refused: "+err.Error()
		}
		d.settled(ev)
		if err := d.save(ctx); err != nil {
			return err
		}
		if err := d.Promotes.Done(ctx, r); err != nil {
			d.logf("promote request: done: %v", err)
		}
	}
	return nil
}
