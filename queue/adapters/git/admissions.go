package git

import (
	"cmp"
	"context"
	"errors"
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
	// Legacy, if set, admits the rows of the existing request refs too; see legacy.go.
	Legacy *Legacy
	refs   map[string][]string // "<id> <sha>" -> the refs it was read from, by arrival
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
	var lv *legacyView
	var lerr error // a row not retired is retried next step; the admissions go on
	if a.Legacy != nil {
		lv, lerr = a.Legacy.view(ctx)
		if lv != nil { // one the old queue withdrew is no ancestor: it leaves in this step
			gone := lv.leaving()
			queued = slices.DeleteFunc(slices.Clone(queued), func(e core.Entry) bool { _, ok := gone[e.ID]; return ok })
		}
	}
	var ps []core.Pending
	at, kept := map[string]int64{}, map[string]string{}
	a.refs = map[string][]string{}
	fetch := []string{"fetch", "-q", "--no-tags", a.Lander.remote}
	taken := map[string]bool{} // a pending change under a queued ID is its supersede, judged by the driver
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
	existing := map[string]bool{} // the refs read from the existing request refs: fetched with them, never signed
	if lv != nil {
		for rid, r := range lv.rows {
			if !lv.held(rid) {
				rs, existing[a.Legacy.Prefix+rid] = append(rs, read{r.at(), rid, r.commit, a.Legacy.Prefix + rid}), true
			}
		}
	}
	var unsigned []bool
	slices.SortStableFunc(rs, func(x, y read) int { return cmp.Compare(x.at, y.at) })
	for _, r := range rs { // one live admit per id, the oldest; the driver judges a repeat of a queued id
		a.refs[r.id+" "+r.sha] = append(a.refs[r.id+" "+r.sha], r.ref)
		p := core.Pending{ID: r.id, Commit: r.sha}
		if old, dup := kept[r.id]; dup && !taken[r.id] {
			p.Why = fmt.Sprintf("id %s already has an admit waiting at %.7s; admit the new commit under a new id", r.id, old)
		} else if !dup {
			at[r.id], kept[r.id] = r.at, r.sha
		}
		if ps, unsigned = append(ps, p), append(unsigned, existing[r.ref]); !existing[r.ref] {
			fetch = append(fetch, r.sha)
		}
	}
	if err != nil || len(ps) == 0 {
		return nil, errors.Join(err, lerr)
	}
	if len(fetch) > 4 {
		if _, err := a.Lander.git(ctx, fetch...); err != nil {
			return nil, err
		}
	}
	for i, p := range ps {
		if unsigned[i] {
			ps[i].Owner = a.owner(ctx, p.Commit)
			continue
		}
		if _, err := a.Lander.git(ctx, "rev-parse", "-q", "--verify", p.Commit+"^{commit}"); err != nil {
			ps[i].Why = p.Commit + " is not a commit"
		}
		if err := SafeID(p.ID); err != nil {
			ps[i].Why = err.Error()
		}
		if ps[i].Why == "" {
			if err := (operators{a.Lander, a.Operators}).verify(ctx, p.Commit, ""); err != nil {
				ps[i].Why = err.Error()
			}
		}
		ps[i].Owner = a.owner(ctx, p.Commit)
	}
	for _, p := range ps { // only an accepted change is an ancestor candidate; one under a queued id at its new commit too
		if p.Why == "" {
			queued = append(queued, core.Entry{ID: p.ID, Commit: p.Commit})
		}
	}
	for i, p := range ps {
		for _, o := range ps {
			if ok, _ := a.Lander.holds(ctx, "merge-base", "--is-ancestor", o.Commit, p.Commit); ok && o.Commit != p.Commit && !slices.Contains(ps[i].Ancestors, o.Commit) {
				ps[i].Ancestors = append(ps[i].Ancestors, o.Commit)
			}
		}
		for _, o := range queued { // a queued commit not fetched with p's history is not its ancestor
			if ok, _ := a.Lander.holds(ctx, "merge-base", "--is-ancestor", o.Commit, p.Commit); ok && o.Commit != p.Commit && o.ID != p.ID && ps[i].Why == "" && !slices.Contains(ps[i].BuildsOn, o.ID) {
				ps[i].BuildsOn = append(ps[i].BuildsOn, o.ID)
			}
		}
	}
	// By arrival, but after every pending commit in its history: each of those has fewer.
	// A refused repeat of an id comes before its kept admit, so it is announced before the id is queued.
	depth := func(p core.Pending) int { return len(p.Ancestors) }
	refused := func(p core.Pending) int { return min(len(p.Why), 1) }
	slices.SortFunc(ps, func(x, y core.Pending) int {
		return cmp.Or(depth(x)-depth(y), cmp.Compare(at[x.ID], at[y.ID]), strings.Compare(x.ID, y.ID), refused(y)-refused(x))
	})
	return ps, lerr
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
	if a.Legacy != nil && strings.HasPrefix(ref, a.Legacy.Prefix) {
		return nil // kept where the old queue reads it until the entry settles
	}
	err := a.Lander.deleteRef(ctx, ref, sha)
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
	// Operators, if set, is a git allowed-signers file: a request not signed by one of its keys is refused.
	Operators string
}

// Resume reads the pause number from the saved state (Load only, no lease),
// then pushes the current main sha, fetched into dir, to the resume ref of that
// pause, replacing an earlier request for it.
func Resume(ctx context.Context, c config.Config, dir string) error {
	snap, err := NewStore(c).Load(ctx)
	if err != nil {
		return err
	}
	return pushRequest(ctx, c, dir, c.Admission.ControlPrefix+"resume-"+strconv.FormatUint(snap.PauseSeq, 10), requestMessage("resume", strconv.FormatUint(snap.PauseSeq, 10)), true)
}

// Pending lists the requests; a ref whose name after the prefix is not a plain number is no request.
func (r *Resumes) Pending(ctx context.Context) ([]core.ResumeRequest, error) {
	rs, err := r.Lander.requests(ctx, r.Prefix)
	var reqs []core.ResumeRequest
	var shas []string
	for _, q := range rs {
		if n, e := strconv.ParseUint(q.name, 10, 64); e == nil && q.name == strconv.FormatUint(n, 10) {
			reqs, shas = append(reqs, core.ResumeRequest{Seq: n, SHA: q.sha}), append(shas, q.sha)
		}
	}
	if err != nil {
		return reqs, err
	}
	if err := (operators{r.Lander, r.Operators}).verifyAll(ctx, shas, func(i int) string { return requestMessage("resume", strconv.FormatUint(reqs[i].Seq, 10)) }, func(i int, err error) { reqs[i].Why = "resume request " + err.Error() }); err != nil {
		return nil, err
	}
	return reqs, nil
}

// Done deletes the request, only if it still points at its sha.
func (r *Resumes) Done(ctx context.Context, q core.ResumeRequest) error {
	return r.Lander.deleteRef(ctx, r.Prefix+strconv.FormatUint(q.Seq, 10), q.SHA)
}
