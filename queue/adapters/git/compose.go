package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/concourse/concourse/queue/config"
	"github.com/concourse/concourse/queue/core"
)

// Composer is a core.Composer. In a private repository it squashes each entry's
// change, in batch order, onto base as one commit titled land(<id>) naming the
// original, and force-pushes the result to the queue's own scratch ref
// refs/heads/<Candidate>; Compose never pushes any other ref, and never main.
type Composer struct{ Remote, Main, Candidate, Name, Email string }

// NewComposer uses repository.uri, the lander's remote.
func NewComposer(c config.Config) Composer {
	return Composer{c.Repository.URI, c.Repository.Main, c.Repository.Candidate, c.Compose.Committer.Name, c.Compose.Committer.Email}
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
	for i, e := range entries {
		own, err := git("merge-base", e.Commit, "refs/compose/base")
		if err == nil {
			own, err = ownBase(git, own, e, entries[:i])
		}
		if err == nil {
			err = composeOne(git, e, own)
		}
		if err != nil {
			return "", err
		}
	}
	if _, err := git("push", "-q", c.Remote, "+HEAD:refs/heads/"+c.Candidate); err != nil {
		return "", err
	}
	return git("rev-parse", "HEAD")
}

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
func composeOne(git func(...string) (string, error), e core.Entry, own string) error {
	pick, err := git("commit-tree", e.Commit+"^{tree}", "-p", own, "-m", "pick")
	if err != nil {
		return err
	}
	if _, err := git("cherry-pick", "--no-commit", pick); err != nil {
		if unmerged, _ := git("ls-files", "-u"); unmerged == "" {
			return err
		}
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
	_, err = git("commit", "-q", "--author="+author, "-m", "land("+e.ID+"): "+subject, "-m", "original: "+e.Commit)
	return err
}

func (c Composer) composeGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := gitCmd(ctx, append([]string{"-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_COMMITTER_NAME="+c.Name, "GIT_COMMITTER_EMAIL="+c.Email, "GIT_AUTHOR_NAME="+c.Name, "GIT_AUTHOR_EMAIL="+c.Email)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", gitFailed(args[0], err, stderr.String())
	}
	return strings.TrimSpace(string(out)), nil
}
