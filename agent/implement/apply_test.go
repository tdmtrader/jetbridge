package implement

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyCommitsOnANewBranchAtBase(t *testing.T) {
	repo, s := snapshotFixture(t)
	base := s.Manifest.BaseCommit
	// The developer has moved on since capture; the branch still starts at base.
	writeTest(t, filepath.Join(repo, "later.txt"), "later\n")
	gitTest(t, repo, "add", ".")
	gitTest(t, repo, "commit", "-qm", "later")
	run := 7
	dir, summary, _ := publish(t, s, &run)
	applied, err := Apply(context.Background(), ApplyOptions{Repo: repo, ResultDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if applied.Branch != "impl/run-7" || applied.BaseCommit != base {
		t.Fatalf("applied %+v", applied)
	}
	if got := gitTest(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); got != "impl/run-7" {
		t.Fatalf("checked out %s", got)
	}
	if got := gitTest(t, repo, "rev-parse", "HEAD^"); got != base {
		t.Fatalf("parent %s, want base %s", got, base)
	}
	if got := gitTest(t, repo, "rev-parse", "HEAD"); got != applied.Commit {
		t.Fatalf("head %s, applied %s", got, applied.Commit)
	}
	message := gitTest(t, repo, "log", "-1", "--format=%B")
	if !strings.HasPrefix(message, summary.Summary) || !strings.Contains(message, "JetBridge-Run: 7") || !strings.Contains(message, "JetBridge-Input: "+s.Digest) {
		t.Fatalf("commit message lacks provenance:\n%s", message)
	}
	if got := gitTest(t, repo, "diff", "--name-status", base, "HEAD"); got != "D\tdeleted.txt\nM\tparser.go\nA\tparser_test.go" {
		t.Fatalf("committed change:\n%s", got)
	}
	if got := gitTest(t, repo, "status", "--porcelain"); got != "" {
		t.Fatalf("worktree not clean after apply:\n%s", got)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "parser.go")); !strings.Contains(string(b), "s[0]") {
		t.Fatal("worktree does not hold the change")
	}
	if _, err := Apply(context.Background(), ApplyOptions{Repo: repo, ResultDir: dir}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("reapplied over an existing branch: %v", err)
	}
}

func TestApplyWithoutRunUsesALocalBranch(t *testing.T) {
	repo, s := snapshotFixture(t)
	dir, summary, _ := publish(t, s, nil)
	applied, err := Apply(context.Background(), ApplyOptions{Repo: repo, ResultDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if applied.Branch != "impl/local-"+summary.PatchDigest[:12] || applied.RunID != nil {
		t.Fatalf("applied %+v", applied)
	}
	if strings.Contains(gitTest(t, repo, "log", "-1", "--format=%B"), "JetBridge-Run") {
		t.Fatal("a local change claimed a Run")
	}
}

func TestApplyRefusesWithoutSideEffects(t *testing.T) {
	for _, c := range []string{"dirty", "missing-base", "digest", "branch-name", "diverged-head"} {
		t.Run(c, func(t *testing.T) {
			repo, s := snapshotFixture(t)
			dir, _, patch := publish(t, s, nil)
			head := gitTest(t, repo, "rev-parse", "HEAD")
			opts := ApplyOptions{Repo: repo, ResultDir: dir}
			want := ""
			switch c {
			case "dirty":
				writeTest(t, filepath.Join(repo, "parser.go"), "dirty\n")
				want = "uncommitted"
			case "missing-base":
				// A repository that never had the base commit.
				repo2 := t.TempDir()
				gitTest(t, repo2, "init", "-q")
				gitTest(t, repo2, "config", "user.name", "Other")
				gitTest(t, repo2, "config", "user.email", "other@example.test")
				writeTest(t, filepath.Join(repo2, "other"), "unrelated history\n")
				gitTest(t, repo2, "add", ".")
				gitTest(t, repo2, "commit", "-qm", "other history")
				opts.Repo, repo, head = repo2, repo2, gitTest(t, repo2, "rev-parse", "HEAD")
				want = "not in this repository"
			case "digest":
				writeTest(t, filepath.Join(dir, PatchFile), strings.Replace(string(patch), "s[0]", "s[2]", 1))
				want = "patch digest"
			case "branch-name":
				opts.Branch = "bad..name"
				want = "invalid branch"
			case "diverged-head":
				// Not a refusal: a change applies at its recorded base,
				// never at whatever HEAD has become.
				opts.Branch = "impl/x"
				gitTest(t, repo, "checkout", "-q", "-b", "diverged")
				writeTest(t, filepath.Join(repo, "parser.go"), "changed\n")
				gitTest(t, repo, "commit", "-qam", "diverge")
				head = gitTest(t, repo, "rev-parse", "HEAD")
				applied, err := Apply(context.Background(), opts)
				if err != nil {
					t.Fatal(err)
				}
				if gitTest(t, repo, "rev-parse", applied.Commit+"^") != s.Manifest.BaseCommit {
					t.Fatal("change applied somewhere other than its base")
				}
				return
			}
			_, err := Apply(context.Background(), opts)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("want error naming %q, got %v", want, err)
			}
			if got := gitTest(t, repo, "rev-parse", "HEAD"); got != head {
				t.Fatal("a refused apply moved HEAD")
			}
			if branches := gitTest(t, repo, "branch", "--list", "impl/*"); branches != "" {
				t.Fatalf("a refused apply left a branch: %s", branches)
			}
		})
	}
}
