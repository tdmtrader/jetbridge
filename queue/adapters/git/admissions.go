package git

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

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
	refs   map[string]string // "<id> <sha>" -> the ref it was read from
}

// stamp is the arrival clock of Admit: nanoseconds, never repeated or going back.
var stamp struct {
	sync.Mutex
	last int64
}

func nextStamp() int64 {
	stamp.Lock()
	defer stamp.Unlock()
	stamp.last = max(time.Now().UnixNano(), stamp.last+1)
	return stamp.last
}

// splitRef reads <Prefix><19 digit stamp>.<id> as (stamp, id); a ref pushed by hand, <Prefix><id>, has stamp 0.
func splitRef(name string) (int64, string) {
	if n, id, ok := strings.Cut(name, "."); ok && len(n) == 19 {
		if at, err := strconv.ParseInt(n, 10, 64); err == nil {
			return at, id
		}
	}
	return 0, name
}

// SafeID refuses an ID that is not one plain ref component of letters,
// digits, '_' and '-', starting with a letter or digit.
func SafeID(id string) error {
	if !safeID.MatchString(id) {
		return fmt.Errorf("change id %q is unsafe: use letters, digits, '_' and '-'", id)
	}
	return nil
}

// Admit pushes sha (any commit-ish), from the repo at dir, to <admission.prefix><stamp>.<id> on repository.uri;
// the stamp keeps arrival order, however many changes are admitted in one second.
func Admit(ctx context.Context, c config.Config, dir, id, sha string) error {
	if err := SafeID(id); err != nil {
		return err
	}
	_, err := (&Lander{dir: dir}).git(ctx, "push", "-q", "--end-of-options", c.Repository.URI, sha+":"+fmt.Sprintf("%s%019d.%s", c.Admission.Prefix, nextStamp(), id))
	return err
}

// Pending lists every ref under the prefix, refusing an unsafe ID or a
// non-commit, each building on the queued and pending changes it descends from.
func (a *Admissions) Pending(ctx context.Context, queued []core.Entry) ([]core.Pending, error) {
	out, err := a.Lander.git(ctx, "ls-remote", a.Lander.remote, a.Prefix+"*")
	var ps []core.Pending
	at := map[string]int64{}
	a.refs = map[string]string{}
	fetch, pending := []string{"fetch", "-q", "--no-tags", a.Lander.remote}, map[string]bool{}
	taken := map[string]bool{} // a pending change under a queued ID is refused, so no change builds on it
	for _, e := range queued {
		taken[e.ID] = true
	}
	for f := strings.Fields(out); len(f) >= 2; f = f[2:] {
		if name, ok := strings.CutPrefix(f[1], a.Prefix); ok {
			stampAt, id := splitRef(name)
			at[id], a.refs[id+" "+f[0]] = stampAt, f[1]
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
	// By arrival, but after every pending change it builds on: each of those builds on fewer.
	depth := func(p core.Pending) int {
		return len(slices.DeleteFunc(slices.Clone(p.BuildsOn), func(id string) bool { return !pending[id] }))
	}
	slices.SortFunc(ps, func(x, y core.Pending) int {
		return cmp.Or(depth(x)-depth(y), cmp.Compare(at[x.ID], at[y.ID]), strings.Compare(x.ID, y.ID))
	})
	return ps, nil
}

// Done deletes the ref of id, only if it still points at sha. A ref that is
// already gone is a repeat, and nothing to do; one moved to another commit stays.
func (a *Admissions) Done(ctx context.Context, id, sha string) error {
	ref, ok := a.refs[id+" "+sha]
	if !ok {
		return fmt.Errorf("no admit ref of %s was read at %.7s", id, sha)
	}
	_, err := a.Lander.git(ctx, "push", "-q", "--force-with-lease="+ref+":"+sha, a.Lander.remote, ":"+ref)
	if err != nil {
		if out, e := a.Lander.git(ctx, "ls-remote", a.Lander.remote, ref); e == nil && out == "" {
			return nil
		}
	}
	return err
}

// Resumes reads the resume requests: the refs <Prefix><seq> on the Lander's remote.
type Resumes struct {
	Lander *Lander
	Prefix string
}

// Resume reads the pause number from the saved state (Load only, no lease),
// then pushes the current main sha, fetched into dir, to the resume ref of that
// pause, replacing an earlier request for it.
func Resume(ctx context.Context, c config.Config, dir string) error {
	snap, err := NewStore(c).Load(ctx)
	if err != nil {
		return err
	}
	l := &Lander{dir: dir}
	if _, err := l.git(ctx, "fetch", "-q", "--no-tags", "--end-of-options", c.Repository.URI, branch(c.Repository.Main)); err != nil {
		return err
	}
	_, err = l.git(ctx, "push", "-q", "--force", "--end-of-options", c.Repository.URI, "FETCH_HEAD:"+c.Admission.ControlPrefix+"resume-"+strconv.FormatUint(snap.PauseSeq, 10))
	return err
}

// Pending lists the requests; a ref whose name after the prefix is not a plain number is no request.
func (r *Resumes) Pending(ctx context.Context) ([]core.ResumeRequest, error) {
	out, err := r.Lander.git(ctx, "ls-remote", r.Lander.remote, r.Prefix+"*")
	var reqs []core.ResumeRequest
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || !strings.HasPrefix(f[1], r.Prefix) {
			continue
		}
		if n, e := strconv.ParseUint(f[1][len(r.Prefix):], 10, 64); e == nil && f[1] == r.ref(n) {
			reqs = append(reqs, core.ResumeRequest{Seq: n, SHA: f[0]})
		}
	}
	return reqs, err
}

// Done deletes the request, only if it still points at its sha.
func (r *Resumes) Done(ctx context.Context, q core.ResumeRequest) error {
	_, err := r.Lander.git(ctx, "push", "-q", "--force-with-lease="+r.ref(q.Seq)+":"+q.SHA, r.Lander.remote, ":"+r.ref(q.Seq))
	return err
}

func (r *Resumes) ref(seq uint64) string { return r.Prefix + strconv.FormatUint(seq, 10) }
