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
	// Operators, if set, is a git allowed-signers file: a change not signed by one of its keys is refused.
	Operators string
	refs      map[string][]string // "<id> <sha>" -> the refs it was read from, by arrival
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

// Pending lists every ref under the prefix, refusing an unsafe ID, a repeat or a
// non-commit, each building on the queued and pending changes it descends from.
func (a *Admissions) Pending(ctx context.Context, queued []core.Entry) ([]core.Pending, error) {
	out, err := a.Lander.git(ctx, "ls-remote", a.Lander.remote, a.Prefix+"*")
	var ps []core.Pending
	at, kept := map[string]int64{}, map[string]string{}
	a.refs = map[string][]string{}
	fetch, pending := []string{"fetch", "-q", "--no-tags", a.Lander.remote}, map[string]bool{}
	taken := map[string]bool{} // a pending change under a queued ID is refused, so no change builds on it
	for _, e := range queued {
		taken[e.ID] = true
	}
	type read struct {
		at           int64
		id, sha, ref string
	}
	var rs []read
	for f := strings.Fields(out); len(f) >= 2; f = f[2:] {
		if name, ok := strings.CutPrefix(f[1], a.Prefix); ok {
			stampAt, id := splitRef(name)
			rs = append(rs, read{stampAt, id, f[0], f[1]})
		}
	}
	slices.SortStableFunc(rs, func(x, y read) int { return cmp.Compare(x.at, y.at) })
	for _, r := range rs { // one live admit per id, the oldest; the driver judges a repeat of a queued id
		a.refs[r.id+" "+r.sha] = append(a.refs[r.id+" "+r.sha], r.ref)
		p := core.Pending{ID: r.id, Commit: r.sha}
		if old, dup := kept[r.id]; dup && !taken[r.id] {
			p.Why = fmt.Sprintf("id %s already has an admit waiting at %.7s; admit the new commit under a new id", r.id, old)
		} else if !dup {
			at[r.id], kept[r.id] = r.at, r.sha
		}
		ps, fetch = append(ps, p), append(fetch, r.sha)
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
		if a.Operators != "" && ps[i].Why == "" {
			if _, err := a.Lander.git(ctx, "-c", "gpg.format=ssh", "-c", "gpg.ssh.allowedSignersFile="+a.Operators, "verify-commit", p.Commit); err != nil {
				ps[i].Why = fmt.Sprintf("%.7s is not signed by an operator", p.Commit) // git's own message may quote key material
			}
		}
		ps[i].Owner = a.owner(ctx, p.Commit)
	}
	for _, p := range ps { // only an accepted change is an ancestor candidate
		if p.Why == "" && !taken[p.ID] {
			queued, pending[p.ID] = append(queued, core.Entry{ID: p.ID, Commit: p.Commit}), true
		}
	}
	for i, p := range ps {
		for _, o := range queued { // a queued commit not fetched with p's history is not its ancestor
			if ok, _ := a.Lander.holds(ctx, "merge-base", "--is-ancestor", o.Commit, p.Commit); ok && o.Commit != p.Commit && ps[i].Why == "" {
				ps[i].BuildsOn = append(ps[i].BuildsOn, o.ID)
			}
		}
	}
	// By arrival, but after every pending change it builds on: each of those builds on fewer.
	// A refused repeat of an id comes before its kept admit, so it is announced before the id is queued.
	depth := func(p core.Pending) int {
		return len(slices.DeleteFunc(slices.Clone(p.BuildsOn), func(id string) bool { return !pending[id] }))
	}
	refused := func(p core.Pending) int { return min(len(p.Why), 1) }
	slices.SortFunc(ps, func(x, y core.Pending) int {
		return cmp.Or(depth(x)-depth(y), cmp.Compare(at[x.ID], at[y.ID]), strings.Compare(x.ID, y.ID), refused(y)-refused(x))
	})
	return ps, nil
}

// Done deletes the ref of id, only if it still points at sha. A ref that is
// already gone is a repeat, and nothing to do; one moved to another commit stays.
func (a *Admissions) Done(ctx context.Context, id, sha string) error {
	rs := a.refs[id+" "+sha] // repeats at one sha are Done refused first, so the latest ref goes first
	if len(rs) == 0 {
		return fmt.Errorf("no admit ref of %s was read at %.7s", id, sha)
	}
	ref := rs[len(rs)-1]
	if len(rs) > 1 {
		a.refs[id+" "+sha] = rs[:len(rs)-1]
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
