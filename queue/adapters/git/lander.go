// Package git is the core's Lander, done with the git CLI. It only
// fast-forwards main, and fences every call: it writes a fresh "fence <n>"
// commit to a lease ref on the remote, in one atomic push with main, each ref
// leased to the value it was read at. Authentication is git's own config.

package git

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
)

var _ core.Lander = (*Lander)(nil)
var fullSHA = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// Lander works in a private bare repo, which Close removes.
type Lander struct {
	remote, leaseRef, dir, emptyTree string
	beforePush                       func()
}

// New takes a config.Parse-checked config; its private repo goes in lander.scratch or os.TempDir.
func New(c config.Config) (*Lander, error) {
	scratch, err := filepath.Abs(cmp.Or(c.Lander.Scratch, os.TempDir()))
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(scratch, "lander-")
	if err != nil {
		return nil, err
	}
	l := &Lander{remote: c.Repository.URI, leaseRef: c.Lander.LeaseRef, dir: dir, beforePush: func() {}}
	if _, err = l.git(context.Background(), "init", "-q", "--bare"); err == nil {
		l.emptyTree, err = l.git(context.Background(), "mktree")
	}
	if err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

func (l *Lander) Close() error { return os.RemoveAll(l.dir) }

// Land fast-forwards main to candidate, moving the lease ref to fence in the
// same atomic push; it refuses if either ref moved since it was read.
func (l *Lander) Land(ctx context.Context, main, candidate string, fence uint64) error {
	if !fullSHA.MatchString(candidate) {
		return fmt.Errorf("candidate %q is not a full commit sha", candidate)
	}
	mainRef := branch(main)
	mainOID, leaseOID, err := l.observe(ctx, mainRef, fence, candidate)
	if err != nil {
		return err
	}
	if ok, err := l.holds(ctx, "merge-base", "--is-ancestor", mainOID, candidate); err != nil {
		return errors.Join(err, fmt.Errorf("candidate %s is not ahead of main %q", candidate, mainOID))
	} else if !ok {
		return &core.MainMovedError{Main: mainRef, Candidate: candidate, Head: mainOID}
	}
	return l.push(ctx, fence, leaseOID, mainRef, mainOID, candidate+":"+mainRef)
}

// Contains first moves the lease ref to fence, so no earlier Land can move
// main after it, then fetches main: a candidate not here now is not on it.
func (l *Lander) Contains(ctx context.Context, main, candidate string, fence uint64) (bool, error) {
	mainRef := branch(main)
	mainOID, leaseOID, err := l.observe(ctx, mainRef, fence)
	if err == nil {
		err = l.push(ctx, fence, leaseOID, mainRef, mainOID)
	}
	if err == nil {
		_, err = l.git(ctx, "fetch", "-q", "--no-tags", l.remote, "+"+mainRef+":refs/seen/main")
	}
	if err != nil {
		return false, err
	}
	if ok, err := l.holds(ctx, "cat-file", "-e", candidate); err != nil || !ok {
		return false, err
	}
	return l.holds(ctx, "merge-base", "--is-ancestor", candidate, "refs/seen/main")
}

// observe reads and fetches main and the lease ref, and refuses unless fence is
// above the lease ref's fence (none means 0).
func (l *Lander) observe(ctx context.Context, mainRef string, fence uint64, want ...string) (mainOID, leaseOID string, err error) {
	out, err := l.git(ctx, "ls-remote", l.remote, mainRef, l.leaseRef)
	if err != nil {
		return "", "", err
	}
	refs := map[string]string{}
	for f := strings.Fields(out); len(f) >= 2; f = f[2:] {
		refs[f[1]] = f[0]
	}
	mainOID, leaseOID = refs[mainRef], refs[l.leaseRef]
	want = slices.DeleteFunc(append(want, mainOID, leaseOID), func(s string) bool { return s == "" })
	if _, err := l.git(ctx, append([]string{"fetch", "-q", "--no-tags", l.remote}, want...)...); err != nil {
		return "", "", err
	}
	var (
		seen uint64
		perr error
	)
	if leaseOID != "" {
		msg, err := l.git(ctx, "show", "-s", "--format=%B%x00", leaseOID)
		n, ok := strings.CutPrefix(strings.TrimSuffix(strings.TrimSuffix(msg, "\x00"), "\n"), "fence ")
		seen, perr = strconv.ParseUint(n, 10, 64) // the NUL marks where git's output ended
		if err != nil || !ok || perr != nil {
			return "", "", errors.New(core.Redact(fmt.Sprintf("%s does not hold a fence (%q): %v", l.leaseRef, msg, errors.Join(err, perr))))
		}
	}
	if seen >= fence {
		return "", "", fmt.Errorf("fence %d refused: the remote's fence is %d", fence, seen)
	}
	return mainOID, leaseOID, nil
}

func (l *Lander) push(ctx context.Context, fence uint64, leaseOID, mainRef, mainOID string, refspecs ...string) error {
	obj, err := l.git(ctx, "commit-tree", l.emptyTree, "-m", fmt.Sprintf("fence %d", fence))
	if err != nil {
		return err
	}
	l.beforePush()
	_, err = l.git(ctx, append([]string{"push", "-q", "--atomic", "--force-with-lease=" + l.leaseRef + ":" + leaseOID,
		"--force-with-lease=" + mainRef + ":" + mainOID, l.remote, obj + ":" + l.leaseRef}, refspecs...)...)
	return err
}

// holds reads a git test command's exit 0 as true and exit 1 as false; any other failure is an error.
func (l *Lander) holds(ctx context.Context, args ...string) (bool, error) {
	_, err := l.git(ctx, args...)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return err == nil, err
}

func (l *Lander) git(ctx context.Context, args ...string) (string, error) {
	return runGit(ctx, "", []string{"-C", l.dir, "-c", "user.name=queue", "-c", "user.email=queue@localhost"}, args...)
}

// runGit runs one git child with its own ssh control directory, removed when
// it ends, so concurrent calls never share a connection.
func runGit(ctx context.Context, stdin string, global []string, args ...string) (string, error) {
	dir, err := os.MkdirTemp("", "queue-ssh-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	cmd := gitCmd(ctx, append(global, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if os.Getenv("GIT_SSH") == "" || os.Getenv("GIT_SSH_COMMAND") != "" { // a GIT_SSH program is left alone
		base := cmp.Or(os.Getenv("GIT_SSH_COMMAND"), sshConfigured(ctx), "ssh")
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND="+base+" -o ControlMaster=no -o ControlPath="+strings.ReplaceAll(dir, " ", "\\ ")+"/s")
	}
	var stderr bytes.Buffer
	cmd.Stdin, cmd.Stderr = strings.NewReader(stdin), &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", gitFailed(args[0], err, stderr.String())
	}
	return strings.TrimSpace(string(out)), nil
}

// sshConfigured is git's own core.sshCommand, kept as the base of the ssh command.
func sshConfigured(ctx context.Context) string {
	out, _ := gitCmd(ctx, "config", "--get", "core.sshCommand").Output()
	return strings.TrimSpace(string(out))
}

// gitCmd is every git child: once ctx ends it is killed, and a grandchild
// holding its pipes is cut loose after waitDelay, so no call outlives ctx long.
func gitCmd(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.WaitDelay = waitDelay
	cmd.Cancel = func() error { return cmd.Process.Kill() }
	return cmd
}

// gitFailed is the error of a git child; its stderr may name a URL with a password, so it is redacted.
func gitFailed(sub string, err error, stderr string) error {
	return fmt.Errorf("git %s: %w: %s", sub, err, core.Redact(strings.TrimSpace(stderr)))
}

const waitDelay = 5 * time.Second

func branch(main string) string { return "refs/heads/" + strings.TrimPrefix(main, "refs/heads/") }
