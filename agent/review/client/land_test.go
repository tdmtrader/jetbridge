package client

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/review"
)

// landFixture is a working clone whose origin is a local bare repository. core
// on origin names base; the clone's checked-out branch is one commit past it.
type landFixture struct {
	origin, work, base, head string
}

func newLandFixture(t *testing.T) landFixture {
	t.Helper()
	// The landing inherits the environment for its fetch and push; keep the
	// developer's global and system git config out of the test.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	f := landFixture{origin: filepath.Join(root, "origin.git"), work: filepath.Join(root, "work")}
	landGit(t, root, "init", "-q", "--bare", f.origin)
	landGit(t, root, "init", "-q", f.work)
	landGit(t, f.work, "config", "user.email", "land@example.test")
	landGit(t, f.work, "config", "user.name", "Land fixture")
	landGit(t, f.work, "remote", "add", "origin", f.origin)
	landWrite(t, filepath.Join(f.work, "parser.go"), "package parser\nfunc First(s string) byte { return s[0] }\n")
	landGit(t, f.work, "add", ".")
	landGit(t, f.work, "commit", "-qm", "base")
	f.base = landGit(t, f.work, "rev-parse", "HEAD")
	landGit(t, f.work, "push", "-q", "origin", f.base+":refs/heads/core")
	landGit(t, f.work, "checkout", "-q", "-b", "change")
	landWrite(t, filepath.Join(f.work, "parser.go"), "package parser\nfunc First(s string) byte { return s[1] }\n")
	landGit(t, f.work, "commit", "-qam", "head")
	f.head = landGit(t, f.work, "rev-parse", "HEAD")
	return f
}

func landGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir, "-c", "core.hooksPath=" + os.DevNull, "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, stderr.Bytes())
	}
	return strings.TrimSpace(string(out))
}

func landWrite(t *testing.T, name, text string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLandingBundleResumesItsPair(t *testing.T) {
	fix := newLandFixture(t)
	ctx := context.Background()
	// Land resolves its state directory before using it, as done here.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, err := landingBundle(ctx, fix.work, dir, fix.base, fix.head)
	if err != nil {
		t.Fatal(err)
	}
	// Capture refuses a dirty worktree and an existing output, so a second
	// call that succeeds here has reused the saved bundle.
	landWrite(t, filepath.Join(fix.work, "parser.go"), "uncommitted\n")
	again, err := landingBundle(ctx, fix.work, dir, fix.base, fix.head)
	if err != nil {
		t.Fatalf("rerun could not resume the saved bundle: %v", err)
	}
	if again.Digest != first.Digest || again.Dir != first.Dir {
		t.Fatalf("rerun used %s at %s, want %s at %s", again.Digest, again.Dir, first.Digest, first.Dir)
	}

	// A bundle saved under this directory for another pair is never reused.
	if _, err := landingBundle(ctx, fix.work, dir, fix.base, fix.base); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("reused a bundle for another pair: %v", err)
	}

	// A saved bundle whose files changed after capture fails its own digest.
	if err := os.WriteFile(filepath.Join(first.Dir, "change.diff"), []byte("tampered\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := landingBundle(ctx, fix.work, dir, fix.base, fix.head); err == nil || !strings.Contains(err.Error(), "saved landing bundle") {
		t.Fatalf("reused a modified bundle: %v", err)
	}
}

func TestLandRefusesBeforeAnyRequest(t *testing.T) {
	// These refusals are decided from git alone. Nothing listens at this
	// address, so a landing that went on to submit would fail instead of
	// refusing.
	unreachable, err := New("http://127.0.0.1:1", &http.Client{})
	if err != nil {
		t.Fatal(err)
	}
	land := func(t *testing.T, fix landFixture, head string) (LandOutcome, error, string) {
		t.Helper()
		state := filepath.Join(t.TempDir(), "state")
		outcome, err := Land(context.Background(), unreachable, LandOptions{
			Repo: fix.work, Head: head, Team: "main", Template: "review", StateDir: state,
			Policy: review.Policy{BlockAt: "medium"},
		})
		return outcome, err, state
	}
	requireRefused := func(t *testing.T, fix landFixture, outcome LandOutcome, err error, state, reason string) {
		t.Helper()
		if err == nil || outcome.Outcome != "refused" || !strings.Contains(outcome.Reason, reason) || err.Error() != outcome.Reason || outcome.RunNumber != 0 {
			t.Fatalf("want refusal %q, got %+v, %v", reason, outcome, err)
		}
		if _, err := os.Stat(state); !os.IsNotExist(err) {
			t.Fatalf("refusal before review captured state: %v", err)
		}
		if got := landGit(t, fix.origin, "rev-parse", "refs/heads/core"); got != outcome.BaseCommit {
			t.Fatalf("refusal moved core to %s", got)
		}
	}

	t.Run("head is core", func(t *testing.T) {
		fix := newLandFixture(t)
		outcome, err, state := land(t, fix, fix.base)
		requireRefused(t, fix, outcome, err, state, "nothing to land")
		if outcome.BaseCommit != fix.base || outcome.HeadCommit != fix.base {
			t.Fatalf("unexpected commits %+v", outcome)
		}
	})

	t.Run("head does not descend from core", func(t *testing.T) {
		fix := newLandFixture(t)
		// Another commit lands on core first; head no longer contains it.
		other := filepath.Join(t.TempDir(), "other")
		landGit(t, filepath.Dir(other), "clone", "-q", "--branch", "core", fix.origin, other)
		landGit(t, other, "config", "user.email", "land@example.test")
		landGit(t, other, "config", "user.name", "Land fixture")
		landWrite(t, filepath.Join(other, "README"), "moved\n")
		landGit(t, other, "add", ".")
		landGit(t, other, "commit", "-qm", "moved")
		landGit(t, other, "push", "-q", "origin", "HEAD:refs/heads/core")
		moved := landGit(t, other, "rev-parse", "HEAD")

		outcome, err, state := land(t, fix, "HEAD")
		requireRefused(t, fix, outcome, err, state, "rebase and re-review")
		if outcome.BaseCommit != moved || outcome.HeadCommit != fix.head {
			t.Fatalf("want a refusal of %s against %s, got %+v", fix.head, moved, outcome)
		}
	})

	t.Run("unknown floor", func(t *testing.T) {
		fix := newLandFixture(t)
		_, err := Land(context.Background(), unreachable, LandOptions{Repo: fix.work, Team: "main", Template: "review", StateDir: t.TempDir(), Policy: review.Policy{BlockAt: "critical"}})
		if err == nil || !strings.Contains(err.Error(), "unknown severity floor") {
			t.Fatalf("accepted an unknown floor: %v", err)
		}
	})
}

// A push that completed after its landing stopped waiting -- a timeout or an
// interrupt -- leaves core on head. Landing the pair again reports it landed
// rather than refusing, from git and the pair's pass marker alone; a saved
// bundle without one proves only that a landing started.
func TestLandReportsAnEarlierLandingOfHead(t *testing.T) {
	unreachable, err := New("http://127.0.0.1:1", &http.Client{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	fix := newLandFixture(t)
	state := t.TempDir()
	dir, err := filepath.EvalSymlinks(state)
	if err != nil {
		t.Fatal(err)
	}
	dir = filepath.Join(dir, fix.base[:12]+"-"+fix.head[:12])
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// A landing that captured its pair and stopped before any Run, say at
	// submission; head then reaches core by hand.
	bundle, err := landingBundle(ctx, fix.work, dir, fix.base, fix.head)
	if err != nil {
		t.Fatal(err)
	}
	landGit(t, fix.work, "push", "-q", "origin", fix.head+":refs/heads/core")
	options := LandOptions{Repo: fix.work, Team: "main", Template: "review", StateDir: state, Policy: review.Policy{BlockAt: "medium"}}
	requireNothingToLand := func(t *testing.T, why string) {
		t.Helper()
		outcome, err := Land(ctx, unreachable, options)
		if err == nil || outcome.Outcome != "refused" || !strings.Contains(outcome.Reason, "nothing to land") {
			t.Fatalf("head was reported landed %s: %+v, %v", why, outcome, err)
		}
	}
	requireNothingToLand(t, "by a landing that was never submitted")

	// Markers that do not name this pair's bundle are no evidence either.
	passed := passMarker{BaseCommit: fix.base, HeadCommit: fix.head, InputDigest: bundle.Digest, RunNumber: 7}
	for name, marker := range map[string]passMarker{
		"for another input": {BaseCommit: fix.base, HeadCommit: fix.head, InputDigest: strings.Repeat("0", 64), RunNumber: 7},
		"for another base":  {BaseCommit: fix.head, HeadCommit: fix.head, InputDigest: bundle.Digest, RunNumber: 7},
		"with no Run":       {BaseCommit: fix.base, HeadCommit: fix.head, InputDigest: bundle.Digest},
	} {
		if err := markPassed(dir, marker); err != nil {
			t.Fatal(err)
		}
		requireNothingToLand(t, "from a marker "+name)
	}

	// The landing's review passed: it wrote its marker, and its push landed.
	if err := markPassed(dir, passed); err != nil {
		t.Fatal(err)
	}
	outcome, err := Land(ctx, unreachable, options)
	if err != nil || outcome.Outcome != "landed" || outcome.BaseCommit != fix.base || outcome.HeadCommit != fix.head || outcome.RunNumber != 7 {
		t.Fatalf("an earlier landing of head was not reported: %+v, %v", outcome, err)
	}

	// The saved state of another pair is no evidence that head landed.
	if err := os.Rename(dir, filepath.Join(filepath.Dir(dir), fix.base[:12]+"-"+fix.base[:12])); err != nil {
		t.Fatal(err)
	}
	requireNothingToLand(t, "without its own state")
}
