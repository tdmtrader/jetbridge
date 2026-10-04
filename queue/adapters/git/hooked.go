package git

import (
	"bytes"
	"context"
	"errors"
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
