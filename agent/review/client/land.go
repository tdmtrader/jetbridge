package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/concourse/concourse/agent/review"
	"github.com/concourse/concourse/agent/runclient"
)

// A landing always targets the fork's trunk on the repository's own origin.
const (
	landRemote = "origin"
	landBranch = "core"
)

// landPollInterval spaces Run status reads while a review is running. Tests
// shorten it.
var landPollInterval = 10 * time.Second

// landPushTimeout bounds the push. The push runs outside the landing's own
// deadline and cancellation, so a timeout or interrupt arriving mid-push
// cannot leave it unknown whether core moved.
const landPushTimeout = 2 * time.Minute

// LandOptions selects the change, the review destination and the threshold.
// StateDir holds one directory per base/head pair with the captured bundle and
// its invocation receipt; it must be outside the repository. Landing the same
// pair again with the same StateDir resumes that pair's Run.
type LandOptions struct {
	Repo, Head, Team, Template, AuthFile, StateDir string
	Policy                                         review.Policy
}

// LandOutcome is the one machine-readable line a landing prints. A refusal is
// returned together with an error carrying its reason. A failure after
// submission leaves Outcome empty but keeps the Run number, and Message
// carries the error.
type LandOutcome struct {
	Outcome    string   `json:"outcome"` // "landed" | "refused" | "" when no decision was reached
	Reason     string   `json:"reason,omitempty"`
	RunNumber  int      `json:"run_number,omitempty"`
	BaseCommit string   `json:"base_commit"`
	HeadCommit string   `json:"head_commit"`
	Verdict    string   `json:"verdict,omitempty"`
	Blocking   []string `json:"blocking"`
	Message    string   `json:"message,omitempty"`
}

func (o LandOutcome) refuse(reason string) (LandOutcome, error) {
	o.Outcome, o.Reason = "refused", reason
	return o, errors.New(reason)
}

// Land reviews head against a freshly fetched origin/core and pushes exactly
// the reviewed head to core when the report passes the policy, on condition
// that core still names the reviewed base. Errors with an empty Outcome are
// failures to reach a decision; a refused Outcome is a decision. It submits
// through the same Run client path as `jb review submit` and review_submit.
func Land(ctx context.Context, c *Client, opts LandOptions) (LandOutcome, error) {
	outcome := LandOutcome{Blocking: []string{}}
	if opts.Repo == "" || opts.Team == "" || opts.Template == "" || opts.StateDir == "" {
		return outcome, errors.New("repo, team, template and state directory are required")
	}
	if opts.Head == "" {
		opts.Head = "HEAD"
	}
	if err := opts.Policy.Validate(); err != nil {
		return outcome, err
	}

	base, err := review.FetchBranch(ctx, opts.Repo, landRemote, landBranch)
	if err != nil {
		return outcome, err
	}
	head, err := review.ResolveCommit(ctx, opts.Repo, opts.Head)
	if err != nil {
		return outcome, err
	}
	outcome.BaseCommit, outcome.HeadCommit = base, head
	if head == base {
		// A landing whose review passed and whose push completed after its
		// caller stopped waiting, or whose output was lost, is reported as
		// landed when run again. Only its pass marker proves the review passed.
		if passed, err := landedBefore(opts.StateDir, head); err != nil {
			return outcome, err
		} else if passed != nil {
			outcome.Outcome, outcome.Reason = "landed", landBranch+" already names head"
			outcome.BaseCommit, outcome.RunNumber = passed.BaseCommit, passed.RunNumber
			return outcome, nil
		}
		return outcome.refuse(landBranch + " already points at head; nothing to land")
	}
	descends, err := review.IsAncestor(ctx, opts.Repo, base, head)
	if err != nil {
		return outcome, err
	}
	if !descends {
		return outcome.refuse("head does not descend from " + landRemote + "/" + landBranch + "; rebase and re-review")
	}

	dir := filepath.Join(opts.StateDir, base[:12]+"-"+head[:12])
	if err := os.MkdirAll(dir, 0700); err != nil {
		return outcome, err
	}
	if dir, err = filepath.EvalSymlinks(dir); err != nil {
		return outcome, err
	}
	bundle, err := landingBundle(ctx, opts.Repo, dir, base, head)
	if err != nil {
		return outcome, err
	}

	// The invocation receipt beside the bundle makes a rerun resume this
	// pair's Run instead of admitting another.
	sub, err := c.Submit(ctx, SubmitOptions{Team: opts.Team, Template: opts.Template, Input: bundle.Dir, Receipt: filepath.Join(dir, "receipt.json"), AuthFile: opts.AuthFile})
	outcome.RunNumber = sub.Handle.Number
	if err != nil {
		return outcome, err
	}
	if !sub.Ready {
		return outcome, fmt.Errorf("review Run %d did not accept credentials (state %q)", sub.Handle.Number, sub.State)
	}

	run, err := c.Wait(ctx, sub.Handle, landPollInterval)
	if err != nil {
		return outcome, err
	}
	if !run.Succeeded() {
		return outcome.refuse(fmt.Sprintf("review Run %s", run.Settled()))
	}

	published, err := c.Result(ctx, sub.Handle, FindingsResult)
	if err != nil {
		return outcome, err
	}
	// The Run client binds the report to its Run; binding it to the bundle
	// captured here proves it reviewed exactly this base and head.
	data, err := json.Marshal(published)
	if err != nil {
		return outcome, err
	}
	report, err := review.ParseReport(data, bundle)
	if err != nil {
		return outcome.refuse("review report does not match the captured change: " + err.Error())
	}
	outcome.Verdict = report.Verdict

	decision, err := review.Evaluate(report, opts.Policy)
	if err != nil {
		return outcome, err
	}
	outcome.Blocking = decision.Blocking
	if !decision.Pass {
		return outcome.refuse(decision.Reason)
	}

	// The commits pushed are the ones resolved here and bound to the report
	// above, never ones the report names. The push gets its own bounded
	// context: once started it finishes, so a rerun can tell whether it did.
	if err := ctx.Err(); err != nil {
		return outcome, err
	}
	if err := markPassed(dir, passMarker{BaseCommit: base, HeadCommit: head, InputDigest: bundle.Digest, RunNumber: sub.Handle.Number}); err != nil {
		return outcome, err
	}
	pushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), landPushTimeout)
	defer cancel()
	err = review.PushBranch(pushCtx, opts.Repo, landRemote, base, head, landBranch)
	if errors.Is(err, review.ErrBranchMoved) {
		return outcome.refuse(landBranch + " moved; rebase and re-review")
	}
	if err != nil {
		return outcome, err
	}
	outcome.Outcome = "landed"
	return outcome, nil
}

// passedFile is the pass marker a landing writes into its pair's directory
// once the review passed its floor, immediately before it pushes. A saved
// bundle proves only that a landing started; the marker proves the review of
// exactly that bundle passed, which is what may land head.
const passedFile = "passed.json"

type passMarker struct {
	BaseCommit  string `json:"base_commit"`
	HeadCommit  string `json:"head_commit"`
	InputDigest string `json:"input_digest"`
	RunNumber   int    `json:"run_number"`
}

// markPassed writes the pair's pass marker atomically: a private temporary
// file in the same directory, synced, then renamed over the marker.
func markPassed(dir string, m passMarker) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".passed-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err = temp.Write(data); err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temp.Name(), filepath.Join(dir, passedFile))
}

// landedBefore returns the pass marker of a landing of head kept under
// stateDir, or nil when there is none. A pair counts only when its saved
// bundle verifies, was captured for exactly head from the base its directory
// names, and its marker names that base, head and bundle digest. The caller
// has already seen that core names head.
func landedBefore(stateDir, head string) (*passMarker, error) {
	dirs, err := filepath.Glob(filepath.Join(stateDir, "*-"+head[:12]))
	if err != nil {
		return nil, err
	}
	for _, dir := range dirs {
		bundle, err := review.LoadBundle(filepath.Join(dir, "bundle"))
		if err != nil {
			continue
		}
		base := bundle.Manifest.BaseCommit
		if bundle.Manifest.HeadCommit != head || filepath.Base(dir) != base[:12]+"-"+head[:12] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, passedFile))
		if err != nil {
			continue
		}
		var m passMarker
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		if d.Decode(&m) != nil || d.More() {
			continue
		}
		if m.BaseCommit == base && m.HeadCommit == head && m.InputDigest == bundle.Digest && m.RunNumber > 0 {
			return &m, nil
		}
	}
	return nil, nil
}

// landingBundle returns the pair's captured bundle, capturing it on first use.
// A lock beside it keeps two landings of one pair from capturing at once; the
// invocation receipt's own lock then serializes their submissions.
func landingBundle(ctx context.Context, repo, dir, base, head string) (*review.Bundle, error) {
	unlock, err := runclient.LockFile(ctx, filepath.Join(dir, "bundle.lock"))
	if err != nil {
		return nil, err
	}
	defer unlock()

	path := filepath.Join(dir, "bundle")
	var bundle *review.Bundle
	if _, err := os.Lstat(path); err == nil {
		bundle, err = review.LoadBundle(path)
		if err != nil {
			return nil, fmt.Errorf("saved landing bundle: %w", err)
		}
	} else if errors.Is(err, os.ErrNotExist) {
		bundle, err = review.Capture(ctx, review.CaptureOptions{Repo: repo, Base: base, Head: head, Output: path})
		if err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	if bundle.Manifest.BaseCommit != base || bundle.Manifest.HeadCommit != head {
		return nil, errors.New("landing bundle does not match the requested base and head")
	}
	return bundle, nil
}
