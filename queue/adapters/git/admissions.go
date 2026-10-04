package git

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
)

var (
	_      core.Admissions = (*Admissions)(nil)
	safeID                 = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,99}$`)
)

// Admissions reads the changes pushed to <Prefix><id> on the Lander's remote.
type Admissions struct {
	Lander *Lander
	Prefix string
}

// SafeID refuses an ID that is not one plain ref component of letters,
// digits, '_' and '-', starting with a letter or digit.
func SafeID(id string) error {
	if !safeID.MatchString(id) {
		return fmt.Errorf("change id %q is unsafe: use letters, digits, '_' and '-'", id)
	}
	return nil
}

// Admit pushes sha (any commit-ish), from the repo at dir, to <admission.prefix><id> on repository.uri.
func Admit(ctx context.Context, c config.Config, dir, id, sha string) error {
	if err := SafeID(id); err != nil {
		return err
	}
	_, err := (&Lander{dir: dir}).git(ctx, "push", "-q", "--end-of-options", c.Repository.URI, sha+":"+c.Admission.Prefix+id)
	return err
}

// Pending lists every ref under the prefix, refusing an unsafe ID or a
// non-commit, each building on the queued and pending changes it descends from.
func (a *Admissions) Pending(ctx context.Context, queued []core.Entry) ([]core.Pending, error) {
	out, err := a.Lander.git(ctx, "ls-remote", a.Lander.remote, a.Prefix+"*")
	var ps []core.Pending
	fetch, pending := []string{"fetch", "-q", "--no-tags", a.Lander.remote}, map[string]bool{}
	taken := map[string]bool{} // a pending change under a queued ID is refused, so no change builds on it
	for _, e := range queued {
		taken[e.ID] = true
	}
	for f := strings.Fields(out); len(f) >= 2; f = f[2:] {
		if id, ok := strings.CutPrefix(f[1], a.Prefix); ok {
			ps, fetch = append(ps, core.Pending{ID: id, Commit: f[0]}), append(fetch, f[0])
			if !taken[id] {
				queued, pending[id] = append(queued, core.Entry{ID: id, Commit: f[0]}), true
			}
		}
	}
	if err != nil || len(ps) == 0 {
		return nil, err
	}
	if _, err := a.Lander.git(ctx, fetch...); err != nil {
		return nil, err
	}
	for i, p := range ps {
		if _, err := a.Lander.git(ctx, "rev-parse", "-q", "--verify", p.Commit+"^{commit}"); err != nil {
			ps[i].Why = p.Commit + " is not a commit"
		}
		if err := SafeID(p.ID); err != nil {
			ps[i].Why = err.Error()
		}
		for _, o := range queued { // a queued commit not fetched with p's history is not its ancestor
			if ok, _ := a.Lander.holds(ctx, "merge-base", "--is-ancestor", o.Commit, p.Commit); ok && o.Commit != p.Commit && ps[i].Why == "" {
				ps[i].BuildsOn = append(ps[i].BuildsOn, o.ID)
			}
		}
	}
	// By ID, but after every pending change it builds on: each of those builds on fewer.
	depth := func(p core.Pending) int {
		return len(slices.DeleteFunc(slices.Clone(p.BuildsOn), func(id string) bool { return !pending[id] }))
	}
	slices.SortFunc(ps, func(x, y core.Pending) int { return cmp.Or(depth(x)-depth(y), strings.Compare(x.ID, y.ID)) })
	return ps, nil
}

// Done deletes the ref of id, only if it still points at sha.
func (a *Admissions) Done(ctx context.Context, id, sha string) error {
	_, err := a.Lander.git(ctx, "push", "-q", "--force-with-lease="+a.Prefix+id+":"+sha, a.Lander.remote, ":"+a.Prefix+id)
	return err
}
