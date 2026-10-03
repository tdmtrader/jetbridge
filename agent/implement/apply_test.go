package implement

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/capture"
)

func TestApplyCommitsOnANewBranchAtBase(t *testing.T) {
	repo, s := snapshotFixture(t)
	base := s.Manifest.BaseCommit
	// The developer has moved on since capture; the branch still starts at base.
	writeTest(t, filepath.Join(repo, "later.txt"), "later\n")
	gitTest(t, repo, "add", ".")
	gitTest(t, repo, "commit", "-qm", "later")
	head := gitTest(t, repo, "rev-parse", "HEAD")
	run := 7
	dir, summary, _ := publish(t, s, &run)
	applied, err := Apply(context.Background(), ApplyOptions{Repo: repo, ResultDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if applied.Branch != "impl/run-7" || applied.BaseCommit != base {
		t.Fatalf("applied %+v", applied)
	}
	// The branch is created, never checked out.
	if got := gitTest(t, repo, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD moved to %s", got)
	}
	if got := gitTest(t, repo, "rev-parse", "impl/run-7^"); got != base {
		t.Fatalf("parent %s, want base %s", got, base)
	}
	if got := gitTest(t, repo, "rev-parse", "impl/run-7"); got != applied.Commit {
		t.Fatalf("branch %s, applied %s", got, applied.Commit)
	}
	message := gitTest(t, repo, "log", "-1", "--format=%B", "impl/run-7")
	if !strings.HasPrefix(message, summary.Summary) || !strings.Contains(message, "JetBridge-Run: 7") || !strings.Contains(message, "JetBridge-Input: "+s.Digest) {
		t.Fatalf("commit message lacks provenance:\n%s", message)
	}
	if got := gitTest(t, repo, "diff", "--name-status", base, "impl/run-7"); got != "D\tdeleted.txt\nM\tparser.go\nA\tparser_test.go" {
		t.Fatalf("committed change:\n%s", got)
	}
	if got := gitTest(t, repo, "show", "impl/run-7:parser.go"); !strings.Contains(got, "s[0]") {
		t.Fatalf("the branch does not hold the change: %s", got)
	}
	if got := gitTest(t, repo, "status", "--porcelain"); got != "" {
		t.Fatalf("apply changed the worktree:\n%s", got)
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
	for _, c := range []string{"missing-base", "digest", "branch-name", "diverged-head"} {
		t.Run(c, func(t *testing.T) {
			repo, s := snapshotFixture(t)
			dir, _, patch := publish(t, s, nil)
			head := gitTest(t, repo, "rev-parse", "HEAD")
			opts := ApplyOptions{Repo: repo, ResultDir: dir}
			want := ""
			switch c {
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

// Apply leaves the developer's checkout alone: uncommitted changes, an
// ignored file at a path the change adds, HEAD and the index all stay as
// they were, and a locked index does not matter.
func TestApplyLeavesTheCheckoutAlone(t *testing.T) {
	repo, s := snapshotFixture(t)
	dir, _, _ := publish(t, s, nil)
	head := gitTest(t, repo, "rev-parse", "HEAD")
	// parser_test.go, which the change adds, is an ignored local file here.
	writeTest(t, filepath.Join(repo, ".git", "info", "exclude"), "parser_test.go\n")
	writeTest(t, filepath.Join(repo, "parser_test.go"), "local secret\n")
	writeTest(t, filepath.Join(repo, "parser.go"), "uncommitted\n")
	writeTest(t, filepath.Join(repo, "untracked.txt"), "mine\n")
	writeTest(t, filepath.Join(repo, ".git", "index.lock"), "")
	before := snapshotDir(t, repo)
	applied, err := Apply(context.Background(), ApplyOptions{Repo: repo, ResultDir: dir, Branch: "impl/aside"})
	if err != nil {
		t.Fatal(err)
	}
	if after := snapshotDir(t, repo); !sameOutsideGitObjects(before, after) {
		t.Fatalf("apply changed the checkout:\n%s\n%s", before, after)
	}
	os.Remove(filepath.Join(repo, ".git", "index.lock"))
	if got := gitTest(t, repo, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD moved to %s", got)
	}
	if got := gitTest(t, repo, "show", applied.Commit+":parser_test.go"); got != "package parser" {
		t.Fatalf("the branch holds %q", got)
	}
}

// sameOutsideGitObjects compares two snapshotDir listings, ignoring the
// object store and refs, where Apply writes the commit and its branch.
func sameOutsideGitObjects(before, after string) bool {
	keep := func(s string) string {
		var out []string
		for _, line := range strings.Split(s, "\n") {
			if strings.Contains(line, "/.git/objects") || strings.Contains(line, "/.git/refs") || strings.Contains(line, "/.git/logs") {
				continue
			}
			out = append(out, line)
		}
		return strings.Join(out, "\n")
	}
	return keep(before) == keep(after)
}

// Nothing in the change runs, even where checking it out would: the base's
// .gitattributes routes every file through a configured filter whose script
// the change rewrites, and Apply still runs neither the old script nor the
// new one.
func TestApplyRunsNoFilter(t *testing.T) {
	repo, base := repoFixture(t)
	marker := filepath.Join(t.TempDir(), "ran")
	writeTest(t, filepath.Join(repo, "tools", "filter.sh"), "#!/bin/sh\ntouch "+marker+"\ncat\n")
	writeTest(t, filepath.Join(repo, ".gitattributes"), "* filter=fixture\n")
	gitTest(t, repo, "add", ".")
	gitTest(t, repo, "commit", "-qm", "filtered")
	gitTest(t, repo, "config", "filter.fixture.smudge", "sh "+filepath.Join(repo, "tools", "filter.sh"))
	gitTest(t, repo, "config", "filter.fixture.clean", "sh "+filepath.Join(repo, "tools", "filter.sh"))
	gitTest(t, repo, "config", "filter.fixture.process", "sh "+filepath.Join(repo, "tools", "filter.sh"))
	os.Remove(marker)
	brief := filepath.Join(t.TempDir(), "brief.md")
	writeTest(t, brief, "Return the first byte.\n")
	s, err := CaptureSnapshot(context.Background(), CaptureOptions{Repo: repo, Base: gitTest(t, repo, "rev-parse", "HEAD"), Brief: brief, Output: filepath.Join(t.TempDir(), "snapshot")})
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(marker)
	_ = base
	tree := treeOf(t, s)
	edited := edit(tree)
	edited["tools/filter.sh"] = TreeFile{Data: []byte("#!/bin/sh\ntouch " + marker + "-agent\ncat\n"), Mode: tree["tools/filter.sh"].Mode}
	dir := forge(t, s, edited)
	if _, err := Apply(context.Background(), ApplyOptions{Repo: repo, ResultDir: dir}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{marker, marker + "-agent"} {
		if _, err := os.Lstat(m); !os.IsNotExist(err) {
			t.Errorf("apply ran a filter: %s exists", m)
		}
	}
}

// A hand-made result is held to what the worker could publish, against the
// base's actual files: it cannot retarget a link, add beneath a file or
// link, or apply at a shifted position. A refused apply leaves no branch.
func TestApplyRefusesWhatTheBaseDoesNotAllow(t *testing.T) {
	for name, change := range map[string]func(Tree) Tree{
		"retarget a link": func(base Tree) Tree {
			edited := edit(base)
			edited["link"] = TreeFile{Data: []byte("/etc/passwd"), Mode: "100644"}
			return edited
		},
		"add beneath a link": func(base Tree) Tree {
			edited := edit(base)
			edited["link/inner.go"] = TreeFile{Data: []byte("package inner\n"), Mode: "100644"}
			return edited
		},
		"add beneath a file": func(base Tree) Tree {
			edited := edit(base)
			edited["noeol.txt/inner.go"] = TreeFile{Data: []byte("package inner\n"), Mode: "100644"}
			return edited
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo, s := snapshotFixture(t)
			base := treeOf(t, s)
			// The forged base treats the link as the text file the worker
			// sees, so Diff emits a section that modifies it.
			base["link"] = TreeFile{Data: base["link"].Data, Mode: "100644"}
			dir := forgeFrom(t, s, base, change(base))
			if _, err := Apply(context.Background(), ApplyOptions{Repo: repo, ResultDir: dir}); err == nil {
				t.Fatal("applied")
			}
			if branches := gitTest(t, repo, "branch", "--list", "impl/*"); branches != "" {
				t.Fatalf("a refused apply left a branch: %s", branches)
			}
		})
	}
	t.Run("shifted hunk", func(t *testing.T) {
		repo, s := snapshotFixture(t)
		_, _, patch := publish(t, s, nil)
		// git apply would find the hunk one line off; this applier does not.
		shifted := strings.Replace(string(patch), "@@ -1,2 +1,2 @@\n package parser", "@@ -2,2 +2,2 @@\n package parser", 1)
		if shifted == string(patch) {
			t.Fatalf("no hunk to shift in:\n%s", patch)
		}
		dir := forgePatch(t, s, []byte(shifted))
		if _, err := Apply(context.Background(), ApplyOptions{Repo: repo, ResultDir: dir}); err == nil {
			t.Fatal("applied a shifted hunk")
		}
	})
}

// forge publishes edited, against the snapshot's base as the worker sees it.
func forge(t *testing.T, s *Snapshot, edited Tree) string {
	t.Helper()
	return forgeFrom(t, s, treeOf(t, s), edited)
}

// forgeFrom publishes the change from base to edited as a worker would,
// without the checks the worker makes against the snapshot.
func forgeFrom(t *testing.T, s *Snapshot, base, edited Tree) string {
	t.Helper()
	patch, _, err := Diff(base, edited)
	if err != nil {
		t.Fatal(err)
	}
	return forgePatch(t, s, patch)
}

// forgePatch publishes patch with the summary a worker would stamp, changed
// only to match it: a hand-made result that ReadResult accepts.
func forgePatch(t *testing.T, s *Snapshot, patch []byte) string {
	t.Helper()
	sections, err := ParsePatch(patch)
	if err != nil {
		t.Fatal(err)
	}
	_, summary, _ := publish(t, s, nil)
	summary.PatchDigest, summary.ChangedFiles = capture.Digest(patch), changedFiles(sections)
	dir := filepath.Join(t.TempDir(), "forged")
	data, _ := json.MarshalIndent(summary, "", "  ")
	writeTest(t, filepath.Join(dir, SummaryFile), string(data)+"\n")
	writeTest(t, filepath.Join(dir, PatchFile), string(patch))
	if _, _, err := ReadResult(dir); err != nil {
		t.Fatalf("the forged result is not one ReadResult accepts: %v", err)
	}
	return dir
}
