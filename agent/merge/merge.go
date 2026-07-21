// Package merge is the platform-owned merge engine (design 2026-07-20 §2).
//
// The platform performs the merge itself, so delivery outcomes are RECORDED
// rather than inferred from a git mirror. Prepare is deliberately
// SPECULATIVE — it computes the prospective merge in a scratch branch of a
// working clone and never touches the remote, so a caller can gate the
// result before deciding to land it (design §4.3, Zuul's model).
//
// This library runs POD-SIDE, alongside a working checkout, exactly as
// agent/harvest does. It must never be called from the web node: the web
// process is deliberately stateless with respect to git.
package merge

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Method is how the delivered branch is integrated into the target.
type Method string

const (
	// MethodMerge creates a true merge commit (two parents).
	MethodMerge Method = "merge"
	// MethodSquash collapses the branch into a single commit on the target.
	MethodSquash Method = "squash"
)

// scratchBranch is where a prospective merge is computed. Using a scratch
// branch keeps the caller's checked-out refs untouched.
const scratchBranch = "concourse-merge-scratch"

// BotIdentity is the platform git identity for platform-generated commits.
// It matches outcomes.BotAuthor so a platform merge commit is never counted
// as human touch.
const (
	BotName  = "concourse-agent[bot]"
	BotEmail = "agent@concourse.invalid"
)

// Plan describes one prospective integration.
type Plan struct {
	Branch  string // delivered ref, e.g. "origin/agent/ticket-12"
	Target  string // target ref, e.g. "origin/main"
	Method  Method
	Message string // commit message for the merge/squash commit
}

// Result is what a prospective merge would produce.
type Result struct {
	Ok            bool
	Conflict      bool
	ConflictPaths []string
	ResultSha     string // what the target would point at if landed
	Behind        int    // commits the branch was behind the target
}

// Staleness reports how many commits the target has that the branch does
// not — i.e. how far behind the delivered branch has fallen. This is a
// read-only query; it is the cheap half of freshness (design §4.1), safe to
// surface on the ticket page without mutating anything.
func Staleness(dir, branch, target string) (int, error) {
	out, err := run(dir, "rev-list", "--count", branch+".."+target)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

// Prepare computes the prospective merge WITHOUT pushing. A conflict is a
// reported Result, not an error — errors are reserved for tooling faults.
// On conflict the merge is aborted so the working clone stays usable.
func Prepare(dir string, p Plan) (Result, error) {
	if p.Method != MethodMerge && p.Method != MethodSquash {
		return Result{}, fmt.Errorf("unknown merge method %q", p.Method)
	}

	behind, err := Staleness(dir, p.Branch, p.Target)
	if err != nil {
		return Result{}, err
	}

	// Compute on a scratch branch rooted at the target so the caller's
	// checked-out refs are never disturbed.
	if _, err := run(dir, "checkout", "-B", scratchBranch, p.Target); err != nil {
		return Result{}, err
	}

	var mergeErr error
	switch p.Method {
	case MethodMerge:
		_, mergeErr = run(dir, "merge", "--no-ff", "--no-commit", p.Branch)
	case MethodSquash:
		_, mergeErr = run(dir, "merge", "--squash", p.Branch)
	}

	if mergeErr != nil {
		paths := conflictPaths(dir)
		abort(dir, p.Method)
		if len(paths) == 0 {
			// Not a content conflict — a real tooling fault.
			return Result{}, mergeErr
		}
		return Result{Conflict: true, ConflictPaths: paths, Behind: behind}, nil
	}

	// A squash leaves changes staged; a --no-commit merge leaves them staged
	// with MERGE_HEAD set. Both need an explicit commit.
	if _, err := run(dir, "commit", "-m", p.Message); err != nil {
		abort(dir, p.Method)
		return Result{}, err
	}

	sha, err := run(dir, "rev-parse", "HEAD")
	if err != nil {
		return Result{}, err
	}
	return Result{Ok: true, ResultSha: strings.TrimSpace(sha), Behind: behind}, nil
}

// conflictPaths lists unmerged files. Empty means the failure was not a
// content conflict.
func conflictPaths(dir string) []string {
	out, err := run(dir, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil
	}
	var paths []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			paths = append(paths, l)
		}
	}
	return paths
}

// abort restores a clean working tree. A squash merge sets no MERGE_HEAD, so
// `merge --abort` does not apply to it; a hard reset covers both shapes.
func abort(dir string, m Method) {
	if m == MethodMerge {
		if _, err := run(dir, "merge", "--abort"); err == nil {
			return
		}
	}
	_, _ = run(dir, "reset", "--hard", "HEAD")
	_, _ = run(dir, "clean", "-fd")
}

// run executes git with a deterministic bot identity so results never
// depend on ambient user config.
func run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME="+BotName,
		"GIT_AUTHOR_EMAIL="+BotEmail,
		"GIT_COMMITTER_NAME="+BotName,
		"GIT_COMMITTER_EMAIL="+BotEmail,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return string(out), nil
}
