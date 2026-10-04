package git

import (
	"context"

	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
)

var _ core.Promotes = (*Promotes)(nil)

// Promotes reads the promote requests: the refs <Prefix><id> on the Lander's remote.
type Promotes struct {
	Lander    *Lander
	Prefix    string
	Operators string // if set, a request not signed by one of its keys is refused
}

// Promote pushes the current main sha, fetched into dir, to the promote ref of id.
func Promote(ctx context.Context, c config.Config, dir, id string) error {
	if err := SafeID(id); err != nil {
		return err
	}
	return pushRequest(ctx, c, dir, c.Admission.ControlPrefix+"promote/"+id, "promote "+id, true)
}

// Pending lists the requests; a ref whose name after the prefix is not a safe id is no request.
func (p *Promotes) Pending(ctx context.Context) ([]core.PromoteRequest, error) {
	rs, err := p.Lander.requests(ctx, p.Prefix)
	var reqs []core.PromoteRequest
	var shas []string
	for _, r := range rs {
		if SafeID(r.name) == nil {
			reqs, shas = append(reqs, core.PromoteRequest{ID: r.name, SHA: r.sha}), append(shas, r.sha)
		}
	}
	if err != nil {
		return reqs, err
	}
	if err := (operators{p.Lander, p.Operators}).verifyAll(ctx, shas, func(i int, err error) { reqs[i].Why = "promote request " + err.Error() }); err != nil {
		return nil, err
	}
	return reqs, nil
}

// Done deletes the request, only if it still points at its sha.
func (p *Promotes) Done(ctx context.Context, r core.PromoteRequest) error {
	return p.Lander.deleteRef(ctx, p.Prefix+r.ID, r.SHA)
}
