package review

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The review-input/v1 digest is a published contract: receipts, reports and
// the worker's before/after check all compare it. These goldens were recorded
// before the capture helpers moved to agent/capture and must never change.
// Commit identities are made deterministic by fixing author, committer and
// dates, so the goldens cover both the Git capture path and the manifest
// canonicalization.
const (
	goldenBundleDigest     = "85008027d88d87725173e3223551a76469c4898585446e23a9c1dc63c1472102"
	goldenPlanBundleDigest = "c21301fc785d8e4c29a3cb73bbd572e5a5d04b08089fc56c297ea1d08d08bfef"
)

func deterministicGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", repo, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false",
		"-c", "user.name=Review fixture", "-c", "user.email=review@example.test", "-c", "init.defaultBranch=main"}, args...)...)
	c.Env = append(gitEnvironment(), "GIT_AUTHOR_DATE=2026-09-12T00:00:00Z", "GIT_COMMITTER_DATE=2026-09-12T00:00:00Z")
	b, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, b)
	}
	return strings.TrimSpace(string(b))
}

func deterministicFixture(t *testing.T) (string, string, string) {
	t.Helper()
	repo := t.TempDir()
	deterministicGit(t, repo, "init", "-q")
	writeTest(t, filepath.Join(repo, "parser.go"), "package parser\nfunc First(s string) byte { return s[0] }\n")
	writeTest(t, filepath.Join(repo, "deleted.txt"), "removed line\n")
	writeTest(t, filepath.Join(repo, ".gitattributes"), "parser.go export-ignore\n")
	writeTest(t, filepath.Join(repo, "nested", "tool.sh"), "#!/bin/sh\necho tool\n")
	if err := os.Chmod(filepath.Join(repo, "nested", "tool.sh"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("parser.go", filepath.Join(repo, "link")); err != nil {
		t.Fatal(err)
	}
	deterministicGit(t, repo, "add", ".")
	deterministicGit(t, repo, "commit", "-qm", "base")
	base := deterministicGit(t, repo, "rev-parse", "HEAD")
	if err := os.Remove(filepath.Join(repo, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	writeTest(t, filepath.Join(repo, "parser.go"), "package parser\nfunc First(s string) byte { return s[1] }\n")
	writeTest(t, filepath.Join(repo, "added.txt"), "new line\n")
	deterministicGit(t, repo, "add", ".")
	deterministicGit(t, repo, "commit", "-qm", "head")
	return repo, base, deterministicGit(t, repo, "rev-parse", "HEAD")
}

func TestReviewInputDigestIsStable(t *testing.T) {
	repo, base, head := deterministicFixture(t)
	plan := filepath.Join(t.TempDir(), "plan.md")
	writeTest(t, plan, "Return the first byte.\n")
	for _, c := range []struct {
		name, plan, want string
	}{{"no plan", "", goldenBundleDigest}, {"plan", plan, goldenPlanBundleDigest}} {
		t.Run(c.name, func(t *testing.T) {
			b, err := Capture(context.Background(), CaptureOptions{Repo: repo, Base: base, Head: head, Plan: c.plan, Output: filepath.Join(t.TempDir(), "bundle")})
			if err != nil {
				t.Fatal(err)
			}
			if b.Digest != c.want {
				t.Fatalf("review-input/v1 digest changed: got %s, want %s", b.Digest, c.want)
			}
			loaded, err := LoadBundle(b.Dir)
			if err != nil || loaded.Digest != c.want {
				t.Fatalf("reloaded digest differs: %v", err)
			}
		})
	}
}
