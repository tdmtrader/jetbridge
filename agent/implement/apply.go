package implement

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/concourse/concourse/agent/capture"
)

type ApplyOptions struct {
	Repo, ResultDir string
	// Branch defaults to impl/run-<n>, or impl/local-<patch digest prefix>
	// for a change produced outside a Run.
	Branch string
}

type Applied struct {
	Branch     string `json:"branch"`
	Commit     string `json:"commit"`
	BaseCommit string `json:"base_commit"`
	RunID      *int   `json:"run_id"`
}

// Apply turns a verified change into one commit on a new local branch whose
// parent is the change's base commit, then checks that branch out. The
// base..branch range is exactly what `jb review capture` takes.
//
// The commit is built in a private index, so the worktree, the current index
// and every existing ref are untouched unless the whole change applies. Git
// runs with hooks disabled and without global or system configuration, so
// nothing in the agent's change is executed on the developer's machine.
func Apply(ctx context.Context, opts ApplyOptions) (*Applied, error) {
	if opts.Repo == "" || opts.ResultDir == "" {
		return nil, errors.New("repo and result directory are required")
	}
	summary, patch, err := ReadResult(opts.ResultDir)
	if err != nil {
		return nil, err
	}
	if len(summary.ChangedFiles) == 0 {
		return nil, errors.New("the change is empty; there is nothing to apply")
	}
	repo, err := capture.Repository(ctx, opts.Repo)
	if err != nil {
		return nil, err
	}
	if err := capture.RequireClean(ctx, repo); err != nil {
		return nil, errors.New("repository has uncommitted changes; commit or stash them before applying")
	}
	base := summary.Provenance.BaseCommit
	if _, err := capture.GitOutput(ctx, repo, "cat-file", "-e", base+"^{commit}"); err != nil {
		return nil, fmt.Errorf("base commit %s is not in this repository; fetch it first", base)
	}
	branch := opts.Branch
	if branch == "" {
		if summary.RunID != nil {
			branch = fmt.Sprintf("impl/run-%d", *summary.RunID)
		} else {
			branch = "impl/local-" + summary.PatchDigest[:12]
		}
	}
	if _, err := capture.GitOutput(ctx, repo, "check-ref-format", "--branch", branch); err != nil || strings.HasPrefix(branch, "-") {
		return nil, fmt.Errorf("invalid branch name %q", branch)
	}
	if _, err := capture.GitOutput(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		return nil, fmt.Errorf("branch %s already exists", branch)
	}
	ident, err := identity(ctx, repo)
	if err != nil {
		return nil, err
	}
	scratch, err := os.MkdirTemp("", "jb-implement-apply-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	index := "GIT_INDEX_FILE=" + filepath.Join(scratch, "index")
	git := func(stdin []byte, env []string, args ...string) (string, error) {
		c := capture.GitCommand(ctx, repo, args...)
		c.Env = append(c.Env, env...)
		c.Env = append(c.Env, index)
		if stdin != nil {
			c.Stdin = bytes.NewReader(stdin)
		}
		var out, stderr bytes.Buffer
		c.Stdout, c.Stderr = &out, &stderr
		if err := c.Run(); err != nil {
			return "", fmt.Errorf("git %s failed: %s", args[0], strings.TrimSpace(stderr.String()))
		}
		return strings.TrimSpace(out.String()), nil
	}
	if _, err := git(nil, nil, "read-tree", base); err != nil {
		return nil, err
	}
	if _, err := git(patch, nil, "apply", "--cached", "--whitespace=nowarn", "-"); err != nil {
		return nil, fmt.Errorf("change does not apply to its base: %w", err)
	}
	tree, err := git(nil, nil, "write-tree")
	if err != nil {
		return nil, err
	}
	message := strings.TrimSpace(summary.Summary) + "\n\n"
	if summary.RunID != nil {
		message += fmt.Sprintf("JetBridge-Run: %d\n", *summary.RunID)
	}
	message += "JetBridge-Input: " + summary.Provenance.InputDigest + "\n"
	commit, err := git([]byte(message), ident, "commit-tree", tree, "-p", base, "-F", "-")
	if err != nil {
		return nil, err
	}
	if !capture.CommitPattern.MatchString(commit) {
		return nil, errors.New("git produced an invalid commit name")
	}
	// An empty old value creates the branch only if it still does not exist.
	if _, err := git(nil, nil, "update-ref", "-m", "jb implement apply", "refs/heads/"+branch, commit, ""); err != nil {
		return nil, err
	}
	if _, err := capture.GitOutput(ctx, repo, "switch", "--quiet", branch); err != nil {
		return nil, fmt.Errorf("created %s at %s but could not switch to it: %w", branch, commit, err)
	}
	return &Applied{Branch: branch, Commit: commit, BaseCommit: base, RunID: summary.RunID}, nil
}

// identity reads the developer's configured name and email through git's
// normal configuration lookup, which only reads files. The commit itself is
// written with the isolated environment.
func identity(ctx context.Context, repo string) ([]string, error) {
	get := func(key string) (string, error) {
		c := exec.CommandContext(ctx, "git", "-C", repo, "config", "--get", key)
		out, err := c.Output()
		value := strings.TrimSpace(string(out))
		if err != nil || value == "" {
			return "", errors.New("set git user.name and user.email before applying a change")
		}
		return value, nil
	}
	name, err := get("user.name")
	if err != nil {
		return nil, err
	}
	email, err := get("user.email")
	if err != nil {
		return nil, err
	}
	return []string{"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email, "GIT_COMMITTER_NAME=" + name, "GIT_COMMITTER_EMAIL=" + email}, nil
}
