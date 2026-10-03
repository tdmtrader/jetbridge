package review

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/concourse/concourse/agent/capture"
)

// Landing talks to a remote, so unlike capture it cannot run hermetically:
// fetch and push need the user's credential helper, URL rewrites and transport
// config. It still never inherits a caller's repository selection, always runs
// against the repository top level, and always names the remote and a full
// refspec, never a configured push default or upstream. Reading the local
// repository (resolving refs, ancestry) stays on capture's hermetic git.

var repositorySelection = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_NAMESPACE",
}

func landingEnvironment() []string {
	env := []string{}
next:
	for _, entry := range os.Environ() {
		for _, name := range repositorySelection {
			if strings.HasPrefix(entry, name+"=") {
				continue next
			}
		}
		env = append(env, entry)
	}
	return env
}

// landingOutputBytes bounds what a fetch or push may print.
const landingOutputBytes = 1 << 20

type boundedBuffer struct{ bytes.Buffer }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > landingOutputBytes {
		return 0, errors.New("git output exceeds 1 MiB")
	}
	return b.Buffer.Write(p)
}

// landingGit runs one git command in the repository's top level. Stderr is
// kept, bounded, for the error: a refused push must say why.
func landingGit(ctx context.Context, repo string, args ...string) ([]byte, error) {
	top, err := capture.Repository(ctx, repo)
	if err != nil {
		return nil, err
	}
	c := exec.CommandContext(ctx, "git", append([]string{"-C", top}, args...)...)
	c.Env = landingEnvironment()
	var stdout, stderr boundedBuffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		return stdout.Bytes(), gitFailure(args[0], err, stderr.Bytes())
	}
	return stdout.Bytes(), nil
}

func gitFailure(command string, err error, stderr []byte) error {
	const keep = 1024
	text := strings.TrimSpace(string(stderr))
	if len(text) > keep {
		text = "…" + text[len(text)-keep:]
	}
	if text == "" {
		return fmt.Errorf("git %s failed: %w", command, err)
	}
	return fmt.Errorf("git %s failed: %w: %s", command, err, text)
}

// ResolveCommit resolves a local ref to a full commit ID the way Capture
// resolves its base and head, so a landing can name its state before capturing.
func ResolveCommit(ctx context.Context, repo, ref string) (string, error) {
	if repo == "" {
		return "", errors.New("a repository is required")
	}
	top, err := capture.Repository(ctx, repo)
	if err != nil {
		return "", err
	}
	return capture.ResolveCommit(ctx, top, ref)
}

// IsAncestor reports whether ancestor is reachable from descendant, so a head
// that cannot fast-forward the branch is refused before it is reviewed.
func IsAncestor(ctx context.Context, repo, ancestor, descendant string) (bool, error) {
	if !capture.CommitPattern.MatchString(ancestor) || !capture.CommitPattern.MatchString(descendant) {
		return false, errors.New("ancestry requires full commit IDs")
	}
	top, err := capture.Repository(ctx, repo)
	if err != nil {
		return false, err
	}
	err = capture.GitCommand(ctx, top, "merge-base", "--is-ancestor", ancestor, descendant).Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return false, nil
	default:
		return false, fmt.Errorf("git merge-base failed: %w", err)
	}
}

func checkRemoteAndBranch(ctx context.Context, repo, remote, branch string) error {
	if remote == "" || strings.HasPrefix(remote, "-") || strings.ContainsAny(remote, " \t\r\n\x00") {
		return fmt.Errorf("invalid remote name %q", remote)
	}
	if branch == "" || strings.HasPrefix(branch, "-") {
		return fmt.Errorf("invalid branch name %q", branch)
	}
	if _, err := capture.GitOutput(ctx, repo, "check-ref-format", "refs/heads/"+branch); err != nil {
		return fmt.Errorf("invalid branch name %q", branch)
	}
	return nil
}

// FetchBranch fetches one branch from remote and returns the commit it named.
// It reads FETCH_HEAD, which a concurrent fetch in the same repository can
// rewrite; the push's lease is what binds a landing to the commit reviewed.
func FetchBranch(ctx context.Context, repo, remote, branch string) (string, error) {
	if err := checkRemoteAndBranch(ctx, repo, remote, branch); err != nil {
		return "", err
	}
	if _, err := landingGit(ctx, repo, "fetch", "--quiet", "--no-tags", "--no-recurse-submodules", remote, "refs/heads/"+branch); err != nil {
		return "", err
	}
	return ResolveCommit(ctx, repo, "FETCH_HEAD")
}

// ErrBranchMoved is a push refused because the branch no longer names the
// commit the change was reviewed against.
var ErrBranchMoved = errors.New("branch no longer names the reviewed base")

// PushBranch moves branch on remote from base to head in one conditional
// update: the lease makes the receiving repository refuse unless the branch
// still names base, so a branch moved, rolled back or deleted since the review
// is never overwritten, and the check is made where the push lands, whatever
// URL the remote fetches from. Tags and submodules are never pushed along with
// it, whatever the user's config says.
func PushBranch(ctx context.Context, repo, remote, base, head, branch string) error {
	if err := checkRemoteAndBranch(ctx, repo, remote, branch); err != nil {
		return err
	}
	if !capture.CommitPattern.MatchString(base) || !capture.CommitPattern.MatchString(head) {
		return errors.New("push requires full commit IDs")
	}
	ref := "refs/heads/" + branch
	status, err := landingGit(ctx, repo, "push", "--quiet", "--porcelain", "--no-follow-tags", "--recurse-submodules=no", "--force-with-lease="+ref+":"+base, remote, head+":"+ref)
	// The porcelain status line names a failed lease "stale info".
	if err != nil && strings.Contains(string(status), "[rejected] (stale info)") {
		return fmt.Errorf("%w: %s", ErrBranchMoved, ref)
	}
	return err
}
