package git

import (
	"context"
	"fmt"
	"strings"

	"github.com/concourse/concourse/queue/config"
)

// operators checks a commit against a git allowed-signers file (ssh keys); the
// zero file accepts everything. Admits and resume, promote, withdraw and resolve requests share it.
type operators struct {
	l    *Lander
	file string
}

// verify is nil if sha, already fetched into l's repo, is signed by a listed key and, if want
// is set, its message is exactly want, so a signed request cannot be replayed as another. The
// error is never git's own words, which may quote key material.
func (o operators) verify(ctx context.Context, sha, want string) error {
	if o.file == "" {
		return nil
	}
	if _, err := o.l.git(ctx, "-c", "gpg.format=ssh", "-c", "gpg.ssh.allowedSignersFile="+o.file, "verify-commit", sha); err != nil {
		return fmt.Errorf("%.7s is not signed by an operator", sha)
	}
	if msg, err := o.l.git(ctx, "show", "-s", "--format=%B", sha); want != "" && (err != nil || msg != want) {
		return fmt.Errorf("%.7s is not signed for %s", sha, want)
	}
	return nil
}

// verifyAll fetches shas and passes refuse the index and error of each one not
// signed by an operator for want(i); the zero file refuses none and fetches nothing.
func (o operators) verifyAll(ctx context.Context, shas []string, want func(int) string, refuse func(int, error)) error {
	if o.file == "" || len(shas) == 0 {
		return nil
	}
	if _, err := o.l.git(ctx, append([]string{"fetch", "-q", "--no-tags", o.l.remote}, shas...)...); err != nil {
		return err
	}
	for i, sha := range shas {
		if err := o.verify(ctx, sha, want(i)); err != nil {
			refuse(i, err)
		}
	}
	return nil
}

// pushRequest pushes the current main sha, fetched into dir, to ref on the
// remote, replacing an earlier request there. With sign and an operators file
// set, it is first signed, as msg, with the operator's own git signing config.
func pushRequest(ctx context.Context, c config.Config, dir, ref, msg string, sign bool) error {
	l := &Lander{dir: dir}
	if _, err := l.git(ctx, "fetch", "-q", "--no-tags", "--end-of-options", c.Repository.URI, branch(c.Repository.Main)); err != nil {
		return err
	}
	src := "FETCH_HEAD"
	if sign && c.Admission.OperatorsFile != "" {
		var err error
		if src, err = l.git(ctx, "commit-tree", "-S", "FETCH_HEAD^{tree}", "-p", "FETCH_HEAD", "-m", msg); err != nil {
			return err
		}
	}
	_, err := l.git(ctx, "push", "-q", "--force", "--end-of-options", c.Repository.URI, src+":"+ref)
	return err
}

// requestMessage is the message a signed control request carries: "queue <op> <args...>".
func requestMessage(op string, args ...string) string {
	return strings.Join(append([]string{"queue", op}, args...), " ")
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
