package git

import (
	"context"
	"strings"

	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
)

var _ core.Promotes = (*Promotes)(nil)

// Promotes reads the promote requests: the refs <Prefix><id> on the Lander's remote.
type Promotes struct {
	Lander *Lander
	Prefix string
}

// Promote pushes the current main sha, fetched into dir, to the promote ref of id.
func Promote(ctx context.Context, c config.Config, dir, id string) error {
	if err := SafeID(id); err != nil {
		return err
	}
	l := &Lander{dir: dir}
	if _, err := l.git(ctx, "fetch", "-q", "--no-tags", "--end-of-options", c.Repository.URI, branch(c.Repository.Main)); err != nil {
		return err
	}
	_, err := l.git(ctx, "push", "-q", "--force", "--end-of-options", c.Repository.URI, "FETCH_HEAD:"+c.Admission.ControlPrefix+"promote/"+id)
	return err
}

// Pending lists the requests; a ref whose name after the prefix is not a safe id is no request.
func (p *Promotes) Pending(ctx context.Context) ([]core.PromoteRequest, error) {
	out, err := p.Lander.git(ctx, "ls-remote", p.Lander.remote, p.Prefix+"*")
	var reqs []core.PromoteRequest
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || !strings.HasPrefix(f[1], p.Prefix) {
			continue
		}
		if id := f[1][len(p.Prefix):]; SafeID(id) == nil {
			reqs = append(reqs, core.PromoteRequest{ID: id, SHA: f[0]})
		}
	}
	return reqs, err
}

// Done deletes the request, only if it still points at its sha.
func (p *Promotes) Done(ctx context.Context, r core.PromoteRequest) error {
	_, err := p.Lander.git(ctx, "push", "-q", "--force-with-lease="+p.Prefix+r.ID+":"+r.SHA, p.Lander.remote, ":"+p.Prefix+r.ID)
	return err
}
