package git

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
)

// The existing request refs are the layout the old queue writes and reads,
// under Prefix (say refs/queue/): a row is an annotated tag at <Prefix><rid> on
// the row's commit; an eject is a tag at refs/queue-out/<rid>; a withdraw or
// supersede is a tag at refs/queue-state/resolved/<rid>. Anyone who may push
// those refs may admit, as with the old queue: they are never signed.
var (
	legacyRID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	legacySHA = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

const legacyTagger = "JetBridge queue <jbq@invalid>"

// admitOnly are the admit lines an eject record does not copy.
var admitOnly = []string{"rid", "sha", "at", "at-ns", "priority", "level", "supersedes", "promoted", "promoted-ns", "requeued", "requeued-ns"}

// Legacy admits the rows of the existing request refs and leaves each where
// the old queue reads it until the queue settles it. Then it retires the row
// as the old queue does: a landed one's ref deleted (main's land commit names
// it, so the old queue counts it landed anyway); an ejected or refused one
// deleted with an eject record written in the same push; one withdrawn here
// deleted with a withdraw record. So the old queue, run again, never lands a
// row twice. It writes nothing else under those refs.
type Legacy struct {
	Lander *Lander
	Prefix string
	Store  interface {
		Load(ctx context.Context) (core.Snapshot, error)
	}
	last *legacyView // read by the drain, used once by the lifecycle step after it
}

type legacyRow struct{ tag, commit, msg string }

type legacyView struct {
	rows     map[string]legacyRow
	refs     map[string]string    // every ref read, full remote name -> object
	resolved map[string]legacyRow // retirement records: tag, sha, message
	snap     core.Snapshot
}

func field(msg, key string) string {
	for ln := range strings.SplitSeq(msg, "\n") {
		if v, ok := strings.CutPrefix(ln, key+": "); ok {
			return v
		}
	}
	return ""
}

// read fetches the existing request refs into the private repo and the queue's saved state.
func (l *Legacy) read(ctx context.Context) (*legacyView, error) {
	ps := config.LegacyPrefixes(l.Prefix)
	if _, err := l.Lander.git(ctx, "fetch", "-q", "--no-tags", "--prune", l.Lander.remote,
		"+"+ps[0]+"*:refs/legacy/rows/*", "+"+ps[1]+"*:refs/legacy/out/*", "+"+ps[2]+"resolved/*:refs/legacy/resolved/*"); err != nil {
		return nil, err
	}
	out, err := l.Lander.git(ctx, "for-each-ref", "--format=%(refname)%1f%(objectname)%1f%(objecttype)%1f%(*objectname)%1f%(contents)%1e", "refs/legacy/")
	if err != nil {
		return nil, err
	}
	v := &legacyView{rows: map[string]legacyRow{}, refs: map[string]string{}, resolved: map[string]legacyRow{}}
	remote := map[string]string{"rows": ps[0], "out": ps[1], "resolved": ps[2] + "resolved/"}
	for rec := range strings.SplitSeq(out, "\x1e") {
		f := strings.SplitN(strings.TrimLeft(rec, "\n"), "\x1f", 5)
		if len(f) != 5 {
			continue
		}
		kind, rid, _ := strings.Cut(strings.TrimPrefix(f[0], "refs/legacy/"), "/")
		v.refs[remote[kind]+rid] = f[1]
		if f[2] != "tag" || !legacyRID.MatchString(rid) {
			continue
		}
		row := legacyRow{f[1], f[3], f[4]}
		switch { // as the old queue's Load reads them
		case kind == "rows" && strings.HasPrefix(row.msg, "jbq-admit v1\n") && field(row.msg, "rid") == rid && field(row.msg, "sha") == row.commit && legacySHA.MatchString(row.commit):
			v.rows[rid] = row
		case kind == "resolved" && strings.HasPrefix(row.msg, "jbq-resolved v1\n") && field(row.msg, "rid") == rid:
			v.resolved[rid] = legacyRow{row.tag, field(row.msg, "sha"), row.msg}
		}
	}
	if v.snap, err = l.Store.Load(ctx); err != nil {
		return nil, err
	}
	return v, nil
}

// view reads, retires every row the saved state has settled, and keeps the result for requests.
func (l *Legacy) view(ctx context.Context) (*legacyView, error) {
	v, err := l.read(ctx)
	if err != nil {
		return nil, err
	}
	queued := map[string]string{}
	for _, e := range v.snap.Queued {
		queued[e.ID] = e.Commit
	}
	var errs []error
	for rid, r := range v.rows {
		if _, ok := queued[rid]; ok {
			continue
		}
		ref, body := l.settled(v.snap, rid, r)
		if body == "" && ref == "" {
			continue
		}
		if err := l.retire(ctx, v, rid, r, ref, body); err != nil {
			errs = append(errs, fmt.Errorf("retire %s: %w", rid, err))
			continue
		}
		delete(v.rows, rid)
	}
	l.last = v
	return v, errors.Join(errs...)
}

// settled says how the old queue would retire row rid, if the saved state settled it at its commit:
// the record ref and body to write with the delete, or ref "-" for a delete alone.
func (l *Legacy) settled(s core.Snapshot, rid string, r legacyRow) (ref, body string) {
	ps := config.LegacyPrefixes(l.Prefix)
	at := s.Commits[rid] == r.commit
	switch {
	case s.Landed[rid] && at:
		return "-", ""
	case s.Ejected[rid] && at:
		why, cause, base := "ejected", "", ""
		for _, sr := range s.Settled {
			if sr.ID == rid && sr.Commit == r.commit && sr.Kind == core.EjectedEvent {
				why, cause, base = sr.Why, sr.Cause, sr.Base
			}
		}
		return ps[1] + rid, ejectBody(rid, r, why, cause, base)
	}
	for _, f := range slices.Backward(s.Settled) {
		if f.ID == rid && f.Commit == r.commit && f.Kind == core.WithdrawnEvent && !s.Landed[rid] && !s.Ejected[rid] {
			return ps[2] + "resolved/" + rid, fmt.Sprintf("jbq-resolved v1\nrid: %s\nat: %d\nkind: withdrawn\nsha: %s\nwas: queued\nreason: %s", rid, time.Now().Unix(), r.commit, clean(f.Why))
		}
	}
	for _, f := range s.Refused {
		if f.ID == rid && f.Commit == r.commit && s.Commits[rid] != r.commit {
			return ps[1] + rid, ejectBody(rid, r, "refused at admission: "+f.Why, "", "")
		}
	}
	return "", ""
}

// ejectBody is the old queue's eject record of row r, with the shipper's lines copied from its admit.
func ejectBody(rid string, r legacyRow, why, cause, base string) string {
	switch cause {
	case core.Culprit:
		cause = "real"
	case core.ParentEjected:
		cause = "stacked"
	default:
		cause = "other"
	}
	b := fmt.Sprintf("jbq-eject v1\nrid: %s\nsha: %s\nat: %d\ntip: %s\nreason: %s\ncause: %s", rid, r.commit, time.Now().Unix(), base, clean(why), cause)
	for ln := range strings.SplitSeq(r.msg, "\n") { // the notify, shipper, proof and seat lines, as the old eject copies them
		if k, _, ok := strings.Cut(ln, ": "); ok && !slices.Contains(admitOnly, k) {
			b += "\n" + ln
		}
	}
	if at := field(r.msg, "at"); at != "" {
		b += "\nadmitted: " + at
	}
	return b
}

func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, core.Redact(s))
	return s[:min(len(s), 2600)]
}

// retire deletes row rid's ref and, unless ref is "-", writes body as a tag at ref, in one atomic push.
func (l *Legacy) retire(ctx context.Context, v *legacyView, rid string, r legacyRow, ref, body string) error {
	row := l.Prefix + rid
	args := []string{"push", "-q", "--atomic", "--force-with-lease=" + row + ":" + r.tag}
	specs := []string{":" + row}
	if ref != "-" {
		obj, err := runGit(ctx, fmt.Sprintf("object %s\ntype commit\ntag %s\ntagger %s %d +0000\n\n%s\n", r.commit, rid, legacyTagger, time.Now().Unix(), body), []string{"-C", l.Lander.dir}, "mktag")
		if err != nil {
			return err
		}
		args, specs = append(args, "--force-with-lease="+ref+":"+v.refs[ref]), append(specs, obj+":"+ref)
	}
	_, err := l.Lander.git(ctx, append(append(args, l.Lander.remote), specs...)...)
	return err
}

// held says whether row rid waits for its eject to be cleared: the old admit of an ejected row id at a new commit.
func (v *legacyView) held(rid string) bool {
	return v.snap.Ejected[rid] && v.snap.Commits[rid] != v.rows[rid].commit
}

// leaving lists the queued entries the old queue withdrew or superseded: their row is gone and its record names their commit.
func (v *legacyView) leaving() map[string]legacyRow {
	out := map[string]legacyRow{}
	for _, e := range v.snap.Queued {
		rec, ok := v.resolved[e.ID]
		if _, row := v.rows[e.ID]; row || !ok || rec.commit != e.Commit || field(rec.msg, "was") != "queued" {
			continue
		}
		if k := field(rec.msg, "kind"); k == "withdrawn" || k == "superseded" {
			out[e.ID] = rec
		}
	}
	return out
}

// requests are the withdraws and eject clears the existing request refs ask for, from the drain's read when there was one.
func (l *Legacy) requests(ctx context.Context) ([]core.LifecycleRequest, error) {
	v := l.last
	if l.last = nil; v == nil {
		var err error
		if v, err = l.view(ctx); err != nil && v == nil {
			return nil, err
		}
		l.last = nil
	}
	var reqs []core.LifecycleRequest
	for id, rec := range v.leaving() {
		reqs = append(reqs, core.LifecycleRequest{Kind: core.WithdrawnEvent, ID: id, Commit: rec.commit, SHA: rec.tag})
	}
	for id, r := range v.rows {
		if v.held(id) {
			reqs = append(reqs, core.LifecycleRequest{Kind: core.ResolvedEvent, ID: id, Commit: v.snap.Commits[id], SHA: r.tag})
		}
	}
	slices.SortFunc(reqs, func(a, b core.LifecycleRequest) int { return strings.Compare(a.ID, b.ID) })
	return reqs, nil
}

// at is a row's admit time in nanoseconds, as the old queue orders rows.
func (r legacyRow) at() int64 {
	if ns, err := strconv.ParseInt(field(r.msg, "at-ns"), 10, 64); err == nil && ns > 0 {
		return ns
	}
	s, _ := strconv.ParseInt(field(r.msg, "at"), 10, 64)
	return s * int64(time.Second)
}
