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
var requestKinds = map[string]core.EventKind{"withdraw-": core.WithdrawnEvent}

// Lifecycle reads the requests under the control prefix: <Prefix>withdraw-<id>.<commit>,
// where commit is the one the author saw queued.
type Lifecycle struct {
	Lander *Lander
	Prefix string
}

// Request pushes the current main sha, fetched into dir, to the request ref for
// id at commit, replacing an earlier request.
func Request(ctx context.Context, c config.Config, dir, kind, id, commit string) error {
	if err := SafeID(id); err != nil {
		return err
	}
	l := &Lander{dir: dir}
	if _, err := l.git(ctx, "fetch", "-q", "--no-tags", "--end-of-options", c.Repository.URI, branch(c.Repository.Main)); err != nil {
		return err
	}
	_, err := l.git(ctx, "push", "-q", "--force", "--end-of-options", c.Repository.URI, "FETCH_HEAD:"+c.Admission.ControlPrefix+kind+id+"."+commit)
	return err
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

// Pending lists the requests; a ref that is not a plain kind, id and commit is no request.
func (r *Lifecycle) Pending(ctx context.Context) ([]core.LifecycleRequest, error) {
	out, err := r.Lander.git(ctx, "ls-remote", r.Lander.remote, r.Prefix+"*")
	var reqs []core.LifecycleRequest
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || !strings.HasPrefix(f[1], r.Prefix) {
			continue
		}
		name := f[1][len(r.Prefix):]
		for pre, kind := range requestKinds {
			if rest, ok := strings.CutPrefix(name, pre); ok {
				if id, commit, ok := strings.Cut(rest, "."); ok && SafeID(id) == nil && commit != "" {
					reqs = append(reqs, core.LifecycleRequest{Kind: kind, ID: id, Commit: commit, SHA: f[0]})
				}
			}
		}
	}
	return reqs, err
}

// Done deletes the request, only if it still points at its sha.
func (r *Lifecycle) Done(ctx context.Context, q core.LifecycleRequest) error {
	ref := r.Prefix + prefixOf(q.Kind) + q.ID + "." + q.Commit
	_, err := r.Lander.git(ctx, "push", "-q", "--force-with-lease="+ref+":"+q.SHA, r.Lander.remote, ":"+ref)
	return err
}

func prefixOf(k core.EventKind) string {
	for pre, kind := range requestKinds {
		if kind == k {
			return pre
		}
	}
	return ""
}
