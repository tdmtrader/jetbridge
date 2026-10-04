package git

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
)

var _ core.Lifecycle = (*Lifecycle)(nil)

// requestKinds maps the start of a request ref's name to the act it asks for.
var requestKinds = map[string]core.EventKind{"withdraw-": core.WithdrawnEvent, "resolve-": core.ResolvedEvent}

// Lifecycle reads the requests under the control prefix: <Prefix>withdraw-<id>.<commit> or
// <Prefix>resolve-<id>.<commit>, where commit is the one the requester saw queued or ejected.
type Lifecycle struct {
	Lander    *Lander
	Prefix    string
	Operators string // if set, a request not signed by one of its keys is refused
}

// Request pushes the current main sha, fetched into dir, to the request ref for
// id at commit, replacing an earlier request.
func Request(ctx context.Context, c config.Config, dir, kind, id, commit string) error {
	if err := SafeID(id); err != nil {
		return err
	}
	return pushRequest(ctx, c, dir, c.Admission.ControlPrefix+kind+id+"."+commit, requestMessage(strings.TrimSuffix(kind, "-"), id, commit), true)
}

// Withdraw asks the runner to remove queued change id, the commit saved for it,
// and deletes any admit ref still waiting under that id so nothing re-queues it.
func Withdraw(ctx context.Context, c config.Config, dir, id string) error {
	snap, err := NewStore(c).Load(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(snap.Queued, func(e core.Entry) bool { return e.ID == id })
	if i < 0 {
		return fmt.Errorf("change %q is not queued", id)
	}
	l := &Lander{dir: dir}
	out, err := l.git(ctx, "ls-remote", c.Repository.URI, c.Admission.Prefix+"*")
	if err != nil {
		return err
	}
	for f := strings.Fields(out); len(f) >= 2; f = f[2:] {
		if _, got := splitRef(strings.TrimPrefix(f[1], c.Admission.Prefix)); got == id {
			if _, err := l.git(ctx, "push", "-q", "--end-of-options", c.Repository.URI, ":"+f[1]); err != nil {
				return err
			}
		}
	}
	return Request(ctx, c, dir, "withdraw-", id, snap.Queued[i].Commit)
}

// Resolve asks the runner to clear the eject of change id, at the commit saved for it.
func Resolve(ctx context.Context, c config.Config, dir, id string) error {
	snap, err := NewStore(c).Load(ctx)
	if err != nil {
		return err
	}
	if !snap.Ejected[id] {
		return fmt.Errorf("change %q is not ejected", id)
	}
	return Request(ctx, c, dir, "resolve-", id, snap.Commits[id])
}

// Pending lists the requests; a ref that is not a plain kind, id and commit is no request.
func (r *Lifecycle) Pending(ctx context.Context) ([]core.LifecycleRequest, error) {
	rs, err := r.Lander.requests(ctx, r.Prefix)
	var reqs []core.LifecycleRequest
	for _, q := range rs {
		for pre, kind := range requestKinds {
			if rest, ok := strings.CutPrefix(q.name, pre); ok {
				if id, commit, ok := strings.Cut(rest, "."); ok && SafeID(id) == nil && commit != "" {
					reqs = append(reqs, core.LifecycleRequest{Kind: kind, ID: id, Commit: commit, SHA: q.sha})
				}
			}
		}
	}
	if err != nil || r.Operators == "" || len(reqs) == 0 {
		return reqs, err
	}
	fetch := []string{"fetch", "-q", "--no-tags", r.Lander.remote}
	for _, q := range reqs {
		fetch = append(fetch, q.SHA)
	}
	if _, err := r.Lander.git(ctx, fetch...); err != nil {
		return nil, err
	}
	for i, q := range reqs {
		if err := (operators{r.Lander, r.Operators}).verify(ctx, q.SHA, requestMessage(strings.TrimSuffix(prefixOf(q.Kind), "-"), q.ID, q.Commit)); err != nil {
			reqs[i].Why = strings.TrimSuffix(prefixOf(q.Kind), "-") + " request " + err.Error()
		}
	}
	return reqs, nil
}

// Done deletes the request, only if it still points at its sha.
func (r *Lifecycle) Done(ctx context.Context, q core.LifecycleRequest) error {
	return r.Lander.deleteRef(ctx, r.Prefix+prefixOf(q.Kind)+q.ID+"."+q.Commit, q.SHA)
}

func prefixOf(k core.EventKind) string {
	for pre, kind := range requestKinds {
		if kind == k {
			return pre
		}
	}
	return ""
}
