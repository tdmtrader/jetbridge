package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/concourse/concourse/queue/config"
)

// ownedOn reads script from the commit at ref, never from a queued change, and
// asks it "owned": the files it regenerates, one per line. found is false, and
// nothing is owned, when ref has no such script. A script that cannot answer
// is a plain error: no verdict.
func ownedOn(ctx context.Context, git func(...string) (string, error), ref, script string, limit time.Duration) (owned []string, found bool, err error) {
	ent, err := git("ls-tree", ref, "--", script)
	if err != nil || ent == "" {
		return nil, false, err
	}
	if !strings.HasPrefix(ent, "100644 blob ") && !strings.HasPrefix(ent, "100755 blob ") {
		return nil, true, errors.New("compose hook script on main is not a regular file")
	}
	body, err := git("cat-file", "blob", ref+":"+script)
	if err != nil {
		return nil, true, err
	}
	tmp, err := os.MkdirTemp("", "queue-hook-")
	if err != nil {
		return nil, true, err
	}
	defer os.RemoveAll(tmp)
	path, empty := filepath.Join(tmp, "hook"), filepath.Join(tmp, "empty")
	if err := errors.Join(os.WriteFile(path, []byte(body+"\n"), 0o755), os.Mkdir(empty, 0o755)); err != nil {
		return nil, true, err
	}
	var out bytes.Buffer
	if err := hookExec(ctx, limit, empty, &out, path, "owned"); err != nil {
		return nil, true, err
	}
	owned = strings.Fields(out.String())
	if slices.ContainsFunc(owned, func(p string) bool { return !config.RepoPath(p) }) {
		return nil, true, errors.New("compose hook owned a path outside the repository")
	}
	return owned, true, nil
}

// keepOurs resolves each unmerged path, all of them in keep, to HEAD's side.
func keepOurs(git func(...string) (string, error), unmerged, keep []string) error {
	for _, p := range unmerged {
		if !slices.Contains(keep, p) {
			return errors.New("a conflict outside the hook's files")
		}
	}
	for _, p := range unmerged {
		args := []string{"checkout", "HEAD", "--", p}
		if _, err := git("cat-file", "-e", "HEAD:"+p); err != nil {
			args = []string{"rm", "-q", "-f", "--", p}
		}
		if _, err := git(args...); err != nil {
			return err
		}
	}
	_, _ = git("cherry-pick", "--quit")
	return nil
}

// HookedRef is where the test job publishes the hook's commit on candidate.
func HookedRef(candidate string) string { return "refs/mq/hooked/" + candidate }

// hooked is the commit that lands for candidate. With the hook script on main
// it is the one at HookedRef: exactly one commit on candidate, changing only
// files main's script owns. Missing or otherwise, nothing lands: a land error,
// never an eject. Without the script on main it is candidate.
func (l *Lander) hooked(ctx context.Context, mainOID, candidate string) (string, error) {
	if l.hookScript == "" {
		return candidate, nil
	}
	git := func(args ...string) (string, error) { return l.git(ctx, args...) }
	owned, found, err := ownedOn(ctx, git, mainOID, l.hookScript, l.hookTimeout)
	if err != nil || !found {
		return candidate, err
	}
	ref := HookedRef(candidate)
	out, err := git("ls-remote", l.remote, ref)
	if err != nil {
		return "", err
	}
	h := ""
	for line := range strings.SplitSeq(out, "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == ref && fullSHA.MatchString(f[0]) {
			h = f[0]
		}
	}
	if h == "" {
		return "", fmt.Errorf("candidate %s has no hooked commit at %s: main's hook must run before it lands", candidate, ref)
	}
	if _, err := git("fetch", "-q", "--no-tags", l.remote, h); err != nil {
		return "", err
	}
	if parents, err := git("rev-list", "--parents", "-n", "1", h); err != nil || parents != h+" "+candidate {
		return "", errors.Join(err, fmt.Errorf("the hooked commit %s is not one commit on the candidate %s", h, candidate))
	}
	changed, err := git("diff-tree", "-r", "-z", "--name-only", "--no-renames", candidate, h)
	if err != nil {
		return "", err
	}
	outside := 0
	for _, p := range strings.Split(changed, "\x00") {
		if p != "" && !slices.Contains(owned, p) {
			outside++
		}
	}
	if outside > 0 {
		return "", fmt.Errorf("the hooked commit %s changes %d file(s) the hook does not own", h, outside)
	}
	return h, nil
}
