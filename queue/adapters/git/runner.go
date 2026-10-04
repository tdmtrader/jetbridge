package git

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/concourse/concourse/queue/core"
)

var _ core.Runner = (*Runner)(nil)

// Runner keeps every run and its result as refs on Remote and nothing in
// memory, so a fresh process sees the same runs. refs/mq/runs/<id> is a tag of
// the candidate, its tagger date when the run started; refs/mq/verdicts/<id>
// is a parentless commit whose message is "candidate <sha>\nverdict <v>",
// written once by RecordVerdict. A verdict counts only for the run's own
// candidate; none within WaitCap is None, never red.
type Runner struct {
	Remote  string
	WaitCap time.Duration
	Now     func() time.Time
}

func NewRunner(remote string, waitCap time.Duration) *Runner {
	return &Runner{remote, waitCap, time.Now}
}

func runRef(id string) string     { return "refs/mq/runs/" + id }
func verdictRef(id string) string { return "refs/mq/verdicts/" + id }

// Start tags candidate as the run. The same candidate again is a no-op, so the
// start time stands; another replaces the run and drops its verdict, in one
// atomic push leased to the refs as read.
func (r *Runner) Start(ctx context.Context, run core.Run, candidate string) error {
	if !fullSHA.MatchString(candidate) {
		return fmt.Errorf("candidate %q is not a full commit sha", candidate)
	}
	return runnerDo(ctx, func(dir string) error {
		tag, verdict, err := r.refs(ctx, dir, run.ID)
		if err != nil {
			return err
		}
		if tag != "" {
			if old, _, err := r.readRun(ctx, dir, tag); err != nil || old == candidate {
				return err
			}
		}
		if _, err := storeGit(ctx, dir, "", "fetch", "-q", "--no-tags", "--depth=1", r.Remote, candidate); err != nil {
			return err
		}
		body := fmt.Sprintf("object %s\ntype commit\ntag run\ntagger queue <queue@localhost> %d +0000\n\nrun %s\n",
			candidate, r.Now().Unix(), run.ID)
		obj, err := storeGit(ctx, dir, body, "mktag")
		if err != nil {
			return err
		}
		args := []string{"push", "-q", "--atomic", "--force-with-lease=" + runRef(run.ID) + ":" + tag}
		specs := []string{obj + ":" + runRef(run.ID)}
		if verdict != "" {
			args = append(args, "--force-with-lease="+verdictRef(run.ID)+":"+verdict)
			specs = append(specs, ":"+verdictRef(run.ID))
		}
		_, err = storeGit(ctx, dir, "", append(append(args, r.Remote), specs...)...)
		return err
	})
}

// Poll reports the run's verdict once one is recorded for its candidate;
// until then it is pending, and past WaitCap it is done with None.
func (r *Runner) Poll(ctx context.Context, id string) (v core.Verdict, done bool, err error) {
	v, done = core.None, true
	err = runnerDo(ctx, func(dir string) error {
		tag, verdict, err := r.refs(ctx, dir, id)
		if err != nil || tag == "" {
			return cmp.Or(err, fmt.Errorf("run %q was not started", id))
		}
		candidate, started, err := r.readRun(ctx, dir, tag)
		if err != nil {
			return err
		}
		if verdict != "" {
			if _, err := storeGit(ctx, dir, "", "fetch", "-q", "--no-tags", r.Remote, verdict); err != nil {
				return err
			}
			msg, err := storeGit(ctx, dir, "", "show", "-s", "--format=%B", verdict)
			if err != nil {
				return err
			}
			if got, ok := parseVerdict(msg); ok && strings.HasPrefix(msg, "candidate "+candidate+"\n") {
				v = got
				return nil
			}
		}
		done = r.Now().Sub(started) > r.WaitCap
		return nil
	})
	if err != nil {
		return core.None, true, err
	}
	return v, done, nil
}

// RecordVerdict writes the run's verdict for candidate, refusing if another is
// already recorded; the same one again is accepted, as a retried put.
func (r *Runner) RecordVerdict(ctx context.Context, id, candidate string, v core.Verdict) error {
	if !fullSHA.MatchString(candidate) || (v != core.Pass && v != core.Fail) {
		return fmt.Errorf("verdict %q for %q: want pass or fail for a full commit sha", v, candidate)
	}
	return runnerDo(ctx, func(dir string) error {
		if same, err := r.recorded(ctx, dir, id, candidate, v); err != nil || same {
			return err
		}
		tree, err := storeGit(ctx, dir, "", "mktree")
		if err != nil {
			return err
		}
		obj, err := storeGit(ctx, dir, "", "commit-tree", "--no-gpg-sign", tree, "-m", "candidate "+candidate+"\nverdict "+string(v))
		if err != nil {
			return err
		}
		if _, err = storeGit(ctx, dir, "", "push", "-q", "--force-with-lease="+verdictRef(id)+":", r.Remote, obj+":"+verdictRef(id)); err != nil {
			return fmt.Errorf("a verdict for run %q is already recorded, or the push failed: %w", id, err)
		}
		return nil
	})
}

// recorded reports whether the run already holds verdict v for candidate,
// refusing if it holds any other; false when none is recorded.
func (r *Runner) recorded(ctx context.Context, dir, id, candidate string, v core.Verdict) (bool, error) {
	_, verdict, err := r.refs(ctx, dir, id)
	if err != nil || verdict == "" {
		return false, err
	}
	if _, err := storeGit(ctx, dir, "", "fetch", "-q", "--no-tags", r.Remote, verdict); err != nil {
		return false, err
	}
	msg, err := storeGit(ctx, dir, "", "show", "-s", "--format=%B", verdict)
	if err != nil {
		return false, err
	}
	if msg != "candidate "+candidate+"\nverdict "+string(v) {
		return false, fmt.Errorf("another verdict for run %q is already recorded", id)
	}
	return true, nil
}

// refs reads the run's tag and verdict ids on the remote; "" for one absent.
func (r *Runner) refs(ctx context.Context, dir, id string) (tag, verdict string, err error) {
	out, err := storeGit(ctx, dir, "", "ls-remote", r.Remote, runRef(id), verdictRef(id))
	for line := range strings.SplitSeq(out, "\n") { // ls-remote suffix-matches, so names are compared whole
		switch f := strings.Fields(line); {
		case len(f) == 2 && f[1] == runRef(id):
			tag = f[0]
		case len(f) == 2 && f[1] == verdictRef(id):
			verdict = f[0]
		}
	}
	return tag, verdict, err
}

// readRun fetches the run's tag, one commit deep, and reads its candidate and start time.
func (r *Runner) readRun(ctx context.Context, dir, tag string) (candidate string, started time.Time, err error) {
	if _, err = storeGit(ctx, dir, "", "fetch", "-q", "--no-tags", "--depth=1", r.Remote, tag); err != nil {
		return "", started, err
	}
	body, err := storeGit(ctx, dir, "", "cat-file", "tag", tag)
	if err != nil {
		return "", started, err
	}
	var unix int64 = -1
	for line := range strings.SplitSeq(body, "\n") {
		if c, ok := strings.CutPrefix(line, "object "); ok {
			candidate = c
		} else if t, ok := strings.CutPrefix(line, "tagger "); ok {
			if f := strings.Fields(t); len(f) >= 2 {
				unix, _ = strconv.ParseInt(f[len(f)-2], 10, 64)
			}
		}
	}
	if candidate == "" || unix < 0 {
		return "", started, fmt.Errorf("run tag %s holds no candidate and start time", tag)
	}
	return candidate, time.Unix(unix, 0), nil
}

func parseVerdict(msg string) (core.Verdict, bool) {
	_, last, _ := strings.Cut(msg, "\nverdict ")
	switch v := core.Verdict(strings.TrimSpace(last)); v {
	case core.Pass, core.Fail:
		return v, true
	}
	return core.None, false
}

func runnerDo(ctx context.Context, f func(dir string) error) error {
	dir, err := os.MkdirTemp("", "queue-runner-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if _, err := storeGit(ctx, dir, "", "init", "-q", "--bare"); err != nil {
		return err
	}
	return f(dir)
}
