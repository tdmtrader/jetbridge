package review

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/capture"
)

// remoteFixture is a working clone whose origin is a local bare repository.
// Commits a, b and c are a line of history; origin's core names b, and the
// clone's HEAD is c, one commit past it.
type remoteFixture struct {
	origin, work, a, b, c string
}

func newRemoteFixture(t *testing.T) remoteFixture {
	t.Helper()
	// Fetch and push inherit the caller's environment, so keep the
	// developer's global and system git config out of the fixture.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	f := remoteFixture{origin: filepath.Join(root, "origin.git"), work: filepath.Join(root, "work")}
	gitTest(t, root, "init", "-q", "--bare", f.origin)
	gitTest(t, root, "init", "-q", f.work)
	gitTest(t, f.work, "config", "user.email", "land@example.test")
	gitTest(t, f.work, "config", "user.name", "Land fixture")
	gitTest(t, f.work, "remote", "add", "origin", f.origin)
	for i, name := range []*string{&f.a, &f.b, &f.c} {
		writeTest(t, filepath.Join(f.work, "parser.go"), "package parser\nconst Version = "+string(rune('1'+i))+"\n")
		gitTest(t, f.work, "add", ".")
		gitTest(t, f.work, "commit", "-qm", "commit")
		*name = gitTest(t, f.work, "rev-parse", "HEAD")
	}
	f.setCore(t, f.origin, f.b)
	return f
}

// setCore moves, creates or (with an empty commit) deletes core in a bare
// repository directly, the way another client's push would.
func (f remoteFixture) setCore(t *testing.T, repo, commit string) {
	t.Helper()
	if commit == "" {
		gitTest(t, repo, "update-ref", "-d", "refs/heads/core")
		return
	}
	gitTest(t, f.work, "push", "-q", "--force", repo, commit+":refs/heads/core")
}

func coreIn(t *testing.T, repo string) string {
	t.Helper()
	c := capture.GitCommand(context.Background(), repo, "rev-parse", "--verify", "-q", "refs/heads/core")
	out, err := c.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func TestFetchBranchNamesTheRemoteCommit(t *testing.T) {
	f := newRemoteFixture(t)
	ctx := context.Background()
	got, err := FetchBranch(ctx, f.work, "origin", "core")
	if err != nil || got != f.b {
		t.Fatalf("fetched %s, %v; want origin's core %s", got, err, f.b)
	}
	// A fetch reports the remote as it is now, not a stale local view of it.
	f.setCore(t, f.origin, f.a)
	if got, err = FetchBranch(ctx, f.work, "origin", "core"); err != nil || got != f.a {
		t.Fatalf("fetched %s, %v after core moved; want %s", got, err, f.a)
	}
	f.setCore(t, f.origin, "")
	if _, err = FetchBranch(ctx, f.work, "origin", "core"); err == nil {
		t.Fatal("fetched a branch the remote does not have")
	}
	for _, bad := range [][2]string{{"-origin", "core"}, {"origin", "-core"}, {"origin", "a..b"}, {"", "core"}} {
		if _, err := FetchBranch(ctx, f.work, bad[0], bad[1]); err == nil {
			t.Fatalf("accepted remote %q branch %q", bad[0], bad[1])
		}
	}
}

func TestPushBranchMovesOnlyTheReviewedBase(t *testing.T) {
	for _, tc := range []struct {
		name string
		core string // what origin's core names when the push arrives
	}{
		{"core moved on", "c"},
		// A plain fast-forward push accepts both of these; only the
		// lease refuses them.
		{"core rolled back to an ancestor", "a"},
		{"core deleted", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRemoteFixture(t)
			now := map[string]string{"a": f.a, "c": f.c, "": ""}[tc.core]
			if tc.core == "c" {
				// Someone else's commit on top of the reviewed base.
				gitTest(t, f.work, "checkout", "-q", "-b", "other", f.b)
				writeTest(t, filepath.Join(f.work, "other.go"), "package parser\n")
				gitTest(t, f.work, "add", ".")
				gitTest(t, f.work, "commit", "-qm", "other")
				now = gitTest(t, f.work, "rev-parse", "HEAD")
			}
			f.setCore(t, f.origin, now)
			err := PushBranch(context.Background(), f.work, "origin", f.b, f.c, "core")
			if !errors.Is(err, ErrBranchMoved) {
				t.Fatalf("want ErrBranchMoved, got %v", err)
			}
			if got := coreIn(t, f.origin); got != now {
				t.Fatalf("refused push changed core from %q to %q", now, got)
			}
		})
	}

	t.Run("core still names the base", func(t *testing.T) {
		f := newRemoteFixture(t)
		if err := PushBranch(context.Background(), f.work, "origin", f.b, f.c, "core"); err != nil {
			t.Fatal(err)
		}
		if got := coreIn(t, f.origin); got != f.c {
			t.Fatalf("core is %s, want the pushed head %s", got, f.c)
		}
	})

	t.Run("the push URL is not the fetch URL", func(t *testing.T) {
		f := newRemoteFixture(t)
		// Fetches read origin, where core names b; pushes go to a mirror
		// whose core is still a. The lease is checked where the push lands.
		mirror := filepath.Join(t.TempDir(), "mirror.git")
		gitTest(t, f.work, "init", "-q", "--bare", mirror)
		f.setCore(t, mirror, f.a)
		gitTest(t, f.work, "remote", "set-url", "--push", "origin", mirror)
		base, err := FetchBranch(context.Background(), f.work, "origin", "core")
		if err != nil || base != f.b {
			t.Fatalf("fetched %s, %v; want %s", base, err, f.b)
		}
		if err := PushBranch(context.Background(), f.work, "origin", base, f.c, "core"); !errors.Is(err, ErrBranchMoved) {
			t.Fatalf("want ErrBranchMoved, got %v", err)
		}
		if got := coreIn(t, mirror); got != f.a {
			t.Fatalf("push destination moved to %s", got)
		}
	})

	t.Run("other push failures are not a moved branch", func(t *testing.T) {
		f := newRemoteFixture(t)
		gitTest(t, f.work, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
		err := PushBranch(context.Background(), f.work, "origin", f.b, f.c, "core")
		if err == nil || errors.Is(err, ErrBranchMoved) {
			t.Fatalf("want a plain push failure, got %v", err)
		}
	})
}

func TestPushBranchRefusesAbbreviatedCommits(t *testing.T) {
	f := newRemoteFixture(t)
	if err := PushBranch(context.Background(), f.work, "origin", f.b[:12], f.c, "core"); err == nil {
		t.Fatal("pushed against an abbreviated base")
	}
	if err := PushBranch(context.Background(), f.work, "origin", f.b, "HEAD", "core"); err == nil {
		t.Fatal("pushed a symbolic head")
	}
	if got := coreIn(t, f.origin); got != f.b {
		t.Fatalf("core moved to %s", got)
	}
}

func TestResolveAndAncestry(t *testing.T) {
	f := newRemoteFixture(t)
	ctx := context.Background()
	if got, err := ResolveCommit(ctx, filepath.Join(f.work, "."), "HEAD~1"); err != nil || got != f.b {
		t.Fatalf("resolved %s, %v; want %s", got, err, f.b)
	}
	if _, err := ResolveCommit(ctx, f.work, "no-such-ref"); err == nil {
		t.Fatal("resolved a missing ref")
	}
	for _, tc := range []struct {
		ancestor, descendant string
		want                 bool
	}{{f.a, f.c, true}, {f.b, f.b, true}, {f.c, f.a, false}} {
		got, err := IsAncestor(ctx, f.work, tc.ancestor, tc.descendant)
		if err != nil || got != tc.want {
			t.Fatalf("IsAncestor(%s, %s) = %v, %v; want %v", tc.ancestor[:7], tc.descendant[:7], got, err, tc.want)
		}
	}
	if _, err := IsAncestor(ctx, f.work, "HEAD", f.c); err == nil {
		t.Fatal("accepted a symbolic ancestor")
	}
}
