package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
)

// Composer is a core.Composer. In a private repository it squashes each entry's
// change, in batch order, onto base as one commit titled land(<id>) naming the
// original, and force-pushes the result to the queue's own scratch ref
// refs/heads/<Candidate>; Compose never pushes any other ref, and never main.
type Composer struct {
	Remote, Main, Candidate, Name, Email string
	Hook, HookOwned                      []string
	HookTimeout                          time.Duration
	HookScript                           string // its owned files, asked of main's copy, take main's side in a conflict
}

// NewComposer uses repository.uri, the lander's remote.
func NewComposer(c config.Config) Composer {
	return Composer{c.Repository.URI, c.Repository.Main, c.Repository.Candidate, c.Compose.Committer.Name, c.Compose.Committer.Email, c.Compose.Hook, c.Compose.HookOwned, c.Compose.HookTimeout, c.Compose.HookScript}
}

// Compose returns the candidate's full sha, or a core.ConflictError naming
// the entry that does not merge. Any other failure is a plain error.
func (c Composer) Compose(ctx context.Context, base string, entries []core.Entry) (string, error) {
	if c.Candidate == "" || c.Candidate == c.Main {
		return "", fmt.Errorf("refusing to push the candidate to refs/heads/%s, the main branch", c.Main)
	}
	dir, err := os.MkdirTemp("", "queue-compose-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	git := func(args ...string) (string, error) { return c.composeGit(ctx, dir, args...) }
	fetch := []string{"fetch", "-q", "--no-tags", c.Remote, "+" + base + ":refs/compose/base"}
	for _, e := range entries {
		fetch = append(fetch, e.Commit)
	}
	for _, args := range [][]string{{"init", "-q"}, fetch, {"checkout", "-q", "--detach", "refs/compose/base"}} {
		if _, err := git(args...); err != nil {
			return "", err
		}
	}
	var keep, union []string
	if c.HookScript != "" {
		if keep, union, err = hookLists(ctx, git, "refs/compose/base", c.HookScript, c.HookTimeout); err != nil {
			return "", err
		}
	}
	for i, e := range entries {
		own, err := git("merge-base", e.Commit, "refs/compose/base")
		if err == nil {
			own, err = ownBase(git, own, e, entries[:i])
		}
		if err == nil {
			err = composeOne(git, dir, e, own, keep, union)
		}
		if err != nil {
			return "", err
		}
	}
	if err := c.runHook(ctx, dir, git); err != nil {
		return "", err
	}
	if _, err := git("push", "-q", c.Remote, "+HEAD:refs/heads/"+c.Candidate); err != nil {
		return "", err
	}
	return git("rev-parse", "HEAD")
}

// rowsHead heads the block naming a land commit's change, one "<id> <sha>" line,
// in the layout the old queue wrote and its readers still parse.
const rowsHead = "Rows (row id, original sha):"

var originalLine = regexp.MustCompile(`(?m)^original: ([0-9a-f]{40})$`)

// ownBase returns the nearest ancestor of commit among the earlier entries and
// the originals named by land commits on base since fork, else fork. It relies
// on the batch invariant that every ancestor of an entry is landed or in the
// batch. With no single nearest one it is a ConflictError on the entry: squash
// has no one base to replay it on.
func ownBase(git func(...string) (string, error), fork string, e core.Entry, earlier []core.Entry) (string, error) {
	commit := e.Commit
	landed, err := git("log", "--format=%B", fork+"..refs/compose/base")
	candidates, name := []string{}, map[string]string{}
	for _, p := range earlier {
		candidates, name[p.Commit] = append(candidates, p.Commit), p.ID
	}
	for _, m := range originalLine.FindAllStringSubmatch(landed, -1) {
		candidates, name[m[1]] = append(candidates, m[1]), m[1]
	}
	anc := func(a, b string) bool { _, no := git("merge-base", "--is-ancestor", a, b); return no == nil }
	own := ""
	for _, c := range candidates {
		if anc(c, commit) && (own == "" || anc(own, c)) {
			own = c
		}
	}
	for _, c := range candidates {
		if anc(c, commit) && !anc(c, own) {
			return "", fmt.Errorf("squash needs one base: %s builds on a merge of queued changes %s, %s: %w", e.ID, name[own], name[c], core.ConflictError{EntryID: e.ID})
		}
	}
	if own == "" {
		return fork, err
	}
	return own, err
}

// composeOne applies e's diff from own (see ownBase) onto HEAD as one commit; an empty result adds none.
// A list of union both sides changed merges key by key (unionMerge); a conflict only in files of keep takes HEAD's side.
func composeOne(git func(...string) (string, error), dir string, e core.Entry, own string, keep, union []string) error {
	pick, err := git("commit-tree", e.Commit+"^{tree}", "-p", own, "-m", "pick")
	if err != nil {
		return err
	}
	unmerged := ""
	if _, err := git("cherry-pick", "--no-commit", pick); err != nil {
		if unmerged, _ = git("diff", "--name-only", "--diff-filter=U"); unmerged == "" {
			return err
		}
	}
	rest, conflict, err := unionMerge(git, dir, own, pick, strings.Split(unmerged, "\n"), union)
	if err != nil {
		return err
	}
	if conflict || unmerged != "" && keepOurs(git, rest, keep) != nil {
		_, _ = git("reset", "-q", "--hard")
		return core.ConflictError{EntryID: e.ID}
	}
	if _, err := git("diff", "--cached", "--quiet"); err == nil {
		return nil
	}
	info, err := git("log", "-1", "--format=%an <%ae>%n%s", e.Commit)
	if err != nil {
		return err
	}
	author, subject, _ := strings.Cut(info, "\n")
	_, err = git("commit", "-q", "--author="+author, "-m", "land("+e.ID+"): "+subject, "-m", "original: "+e.Commit, "-m", rowsHead+"\n"+e.ID+" "+e.Commit)
	return err
}

func (c Composer) composeGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := gitCmd(ctx, append([]string{"-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(gitEnv(ctx, "-C", dir), "GIT_COMMITTER_NAME="+c.Name, "GIT_COMMITTER_EMAIL="+c.Email, "GIT_AUTHOR_NAME="+c.Name, "GIT_AUTHOR_EMAIL="+c.Email)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", gitFailed(args[0], err, stderr.String())
	}
	return strings.TrimSpace(string(out)), nil
}

// runHook runs the hook, if any, in the composed tree and commits what it
// changed on top of the composition. Its output is dropped, not echoed. A
// failure, a timeout or a change outside HookOwned is a plain error: no verdict.
func (c Composer) runHook(ctx context.Context, dir string, git func(...string) (string, error)) error {
	if len(c.Hook) == 0 {
		return nil
	}
	if err := hookExec(ctx, c.HookTimeout, dir, nil, c.Hook...); err != nil {
		return err
	}
	return c.commitHook(dir, git)
}

// hookExec runs argv in dir as its own process group, all of it killed at limit.
func hookExec(ctx context.Context, limit time.Duration, dir string, stdout io.Writer, argv ...string) error {
	if limit <= 0 {
		limit = 15 * time.Minute
	}
	hctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cmd := exec.CommandContext(hctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.WaitDelay, cmd.Stdout = dir, time.Second, stdout
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // the hook and its children are one group
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	var exit *exec.ExitError
	err := cmd.Run()
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // nothing the hook started outlives it
	}
	if hctx.Err() != nil && ctx.Err() == nil {
		return errors.New("compose hook failed: timeout")
	} else if errors.As(err, &exit) {
		return fmt.Errorf("compose hook failed: exit %d", exit.ExitCode())
	} else if err != nil {
		return errors.New("compose hook failed: could not run")
	}
	return nil
}

// commitHook commits what the hook changed, if it stayed inside HookOwned.
func (c Composer) commitHook(dir string, git func(...string) (string, error)) error {
	if _, err := git("add", "-A"); err != nil {
		return err
	}
	// --no-renames names both sides of a move; ignored files are listed apart, as add skips them.
	changed, err := git("diff", "--cached", "--name-only", "--no-renames", "-z")
	if err != nil {
		return err
	}
	ignored, err := git("ls-files", "-z", "--others", "--ignored", "--exclude-standard")
	if err != nil {
		return err
	}
	split := func(z string) []string {
		return slices.DeleteFunc(strings.Split(z, "\x00"), func(n string) bool { return n == "" })
	}
	outside, force := 0, split(ignored)
	if c.HookOwned == nil {
		force = nil
	}
	for _, n := range append(split(changed), force...) {
		if c.HookOwned != nil && !c.owns(dir, n) {
			outside++
		}
	}
	if outside > 0 {
		return fmt.Errorf("compose hook changed %d path(s) outside hook_owned", outside)
	}
	if len(force) > 0 {
		if _, err := git(append([]string{"add", "-f", "--"}, force...)...); err != nil {
			return err
		}
	} else if changed == "" {
		return nil
	}
	_, err = git("commit", "-q", "-m", "compose hook: regenerate files")
	return err
}

// owns says whether the hook may change n: a clean path inside the tree under
// a HookOwned prefix, and, if it is a symlink, one whose target stays inside the tree.
func (c Composer) owns(dir, n string) bool {
	n = path.Clean(n)
	if path.IsAbs(n) || n == ".." || strings.HasPrefix(n, "../") || !slices.ContainsFunc(c.HookOwned, func(p string) bool { return strings.HasPrefix(n, p) }) {
		return false
	}
	full := filepath.Join(dir, n)
	if fi, err := os.Lstat(full); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return true
	}
	root, err := filepath.EvalSymlinks(dir)
	target, err2 := filepath.EvalSymlinks(full)
	return err == nil && err2 == nil && strings.HasPrefix(target, root+string(filepath.Separator))
}
