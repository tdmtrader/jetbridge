package git

import (
	"context"
	"strings"

	"github.com/concourse/concourse/queue/config"
)

// pushRequest pushes the current main sha, fetched into dir, to ref on the
// remote, replacing an earlier request there.
func pushRequest(ctx context.Context, c config.Config, dir, ref string) error {
	l := &Lander{dir: dir}
	if _, err := l.git(ctx, "fetch", "-q", "--no-tags", "--end-of-options", c.Repository.URI, branch(c.Repository.Main)); err != nil {
		return err
	}
	_, err := l.git(ctx, "push", "-q", "--force", "--end-of-options", c.Repository.URI, "FETCH_HEAD:"+ref)
	return err
}

// request is one ref under a request prefix: its name after the prefix, and its sha.
type request struct{ name, sha string }

// requests lists the refs under prefix on the remote.
func (l *Lander) requests(ctx context.Context, prefix string) ([]request, error) {
	out, err := l.git(ctx, "ls-remote", l.remote, prefix+"*")
	var rs []request
	for line := range strings.SplitSeq(out, "\n") {
		if f := strings.Fields(line); len(f) >= 2 && strings.HasPrefix(f[1], prefix) {
			rs = append(rs, request{f[1][len(prefix):], f[0]})
		}
	}
	return rs, err
}

// deleteRef deletes ref on the remote, only if it still points at sha.
func (l *Lander) deleteRef(ctx context.Context, ref, sha string) error {
	_, err := l.git(ctx, "push", "-q", "--force-with-lease="+ref+":"+sha, l.remote, ":"+ref)
	return err
}
