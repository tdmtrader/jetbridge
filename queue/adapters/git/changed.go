package git

import (
	"context"
	"os"
	"strings"

	"github.com/concourse/concourse/queue/core"
)

// Changed lists the files each entry changed since it forked from main, by
// ID, for suspect hints only. It fetches into a scratch repository and never
// pushes.
func (c Composer) Changed(ctx context.Context, entries []core.Entry) (map[string][]string, error) {
	dir, err := os.MkdirTemp("", "queue-changed-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	git := func(args ...string) (string, error) { return c.composeGit(ctx, dir, args...) }
	fetch := []string{"fetch", "-q", "--no-tags", c.Remote, "+" + c.Main + ":refs/changed/main"}
	for _, e := range entries {
		fetch = append(fetch, e.Commit)
	}
	for _, args := range [][]string{{"init", "-q"}, fetch} {
		if _, err := git(args...); err != nil {
			return nil, err
		}
	}
	out := map[string][]string{}
	for _, e := range entries {
		files, err := git("diff", "--name-only", "-z", "--no-renames", "refs/changed/main..."+e.Commit)
		if err != nil {
			return nil, err
		}
		out[e.ID] = strings.FieldsFunc(files, func(r rune) bool { return r == 0 })
	}
	return out, nil
}

var _ core.ChangeLister = Composer{}
