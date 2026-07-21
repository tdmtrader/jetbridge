package merge_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/merge"
)

var botEnv = []string{
	"GIT_AUTHOR_NAME=concourse-agent[bot]",
	"GIT_AUTHOR_EMAIL=agent@concourse.invalid",
	"GIT_COMMITTER_NAME=concourse-agent[bot]",
	"GIT_COMMITTER_EMAIL=agent@concourse.invalid",
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), botEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// scenario builds an origin with `main` plus a delivered branch
// `agent/ticket-1`, and returns a working clone. When advanceTarget is
// non-empty, main gains a further commit writing that content to
// target.txt, so the branch is behind.
func scenario(t *testing.T, branchFile, branchBody, advanceTarget string) (ws string) {
	t.Helper()
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "origin.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, bare, "init", "--bare", "--initial-branch=main")

	seed := filepath.Join(tmp, "seed")
	git(t, tmp, "clone", bare, seed)
	write(t, seed, "base.txt", "base\n")
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-m", "base")
	git(t, seed, "push", "origin", "HEAD:main")

	// delivered branch off main
	git(t, seed, "checkout", "-b", "agent/ticket-1")
	write(t, seed, branchFile, branchBody)
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-m", "agent work")
	git(t, seed, "push", "origin", "HEAD:refs/heads/agent/ticket-1")

	if advanceTarget != "" {
		git(t, seed, "checkout", "main")
		write(t, seed, "target.txt", advanceTarget)
		git(t, seed, "add", ".")
		git(t, seed, "commit", "-m", "target moved")
		git(t, seed, "push", "origin", "HEAD:main")
	}

	ws = filepath.Join(tmp, "ws")
	git(t, tmp, "clone", bare, ws)
	git(t, ws, "fetch", "origin", "+refs/heads/*:refs/remotes/origin/*")
	return ws
}

func TestStalenessZeroWhenBranchIsCurrent(t *testing.T) {
	ws := scenario(t, "f.txt", "work\n", "")
	behind, err := merge.Staleness(ws, "origin/agent/ticket-1", "origin/main")
	if err != nil {
		t.Fatal(err)
	}
	if behind != 0 {
		t.Fatalf("expected 0 commits behind, got %d", behind)
	}
}

func TestStalenessCountsCommitsBehind(t *testing.T) {
	ws := scenario(t, "f.txt", "work\n", "moved\n")
	behind, err := merge.Staleness(ws, "origin/agent/ticket-1", "origin/main")
	if err != nil {
		t.Fatal(err)
	}
	if behind != 1 {
		t.Fatalf("expected 1 commit behind, got %d", behind)
	}
}

func TestPrepareCleanMergeProducesResultSha(t *testing.T) {
	ws := scenario(t, "f.txt", "work\n", "moved\n")
	targetBefore := git(t, ws, "rev-parse", "origin/main")

	res, err := merge.Prepare(ws, merge.Plan{
		Branch: "origin/agent/ticket-1", Target: "origin/main",
		Method: merge.MethodMerge, Message: "merge ticket 1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ok || res.Conflict {
		t.Fatalf("expected a clean merge, got %+v", res)
	}
	if res.ResultSha == "" || res.ResultSha == targetBefore {
		t.Fatalf("expected a new result sha, got %q", res.ResultSha)
	}
	if res.Behind != 1 {
		t.Fatalf("expected Behind=1, got %d", res.Behind)
	}
}

func TestPrepareDoesNotMutateTheRemote(t *testing.T) {
	ws := scenario(t, "f.txt", "work\n", "moved\n")
	before := git(t, ws, "rev-parse", "origin/main")

	if _, err := merge.Prepare(ws, merge.Plan{
		Branch: "origin/agent/ticket-1", Target: "origin/main",
		Method: merge.MethodMerge, Message: "merge ticket 1",
	}); err != nil {
		t.Fatal(err)
	}

	git(t, ws, "fetch", "origin")
	if after := git(t, ws, "rev-parse", "origin/main"); after != before {
		t.Fatal("Prepare must be speculative — it must never push to the remote")
	}
}

func TestPrepareReportsConflictAndLeavesRepoClean(t *testing.T) {
	// both sides edit base.txt => conflict
	ws := scenario(t, "base.txt", "agent version\n", "")
	git(t, ws, "checkout", "main")
	write(t, ws, "base.txt", "target version\n")
	git(t, ws, "add", ".")
	git(t, ws, "commit", "-m", "target edits base")
	git(t, ws, "push", "origin", "HEAD:main")
	git(t, ws, "fetch", "origin")

	res, err := merge.Prepare(ws, merge.Plan{
		Branch: "origin/agent/ticket-1", Target: "origin/main",
		Method: merge.MethodMerge, Message: "merge ticket 1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Ok || !res.Conflict {
		t.Fatalf("expected a conflict, got %+v", res)
	}
	if len(res.ConflictPaths) == 0 || res.ConflictPaths[0] != "base.txt" {
		t.Fatalf("expected base.txt in conflict paths, got %v", res.ConflictPaths)
	}
	// the working tree must be usable afterwards: no merge left in progress
	if _, err := os.Stat(filepath.Join(ws, ".git", "MERGE_HEAD")); !os.IsNotExist(err) {
		t.Fatal("a conflicting Prepare must abort the merge, leaving no MERGE_HEAD")
	}
}

func TestPrepareSquashProducesASingleCommit(t *testing.T) {
	ws := scenario(t, "f.txt", "work\n", "moved\n")

	res, err := merge.Prepare(ws, merge.Plan{
		Branch: "origin/agent/ticket-1", Target: "origin/main",
		Method: merge.MethodSquash, Message: "squash ticket 1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ok {
		t.Fatalf("expected a clean squash, got %+v", res)
	}
	// a squash result has exactly one parent (the target head)
	parents := git(t, ws, "rev-list", "--parents", "-n", "1", res.ResultSha)
	if n := len(strings.Fields(parents)) - 1; n != 1 {
		t.Fatalf("expected a squash commit with 1 parent, got %d (%q)", n, parents)
	}
}

func TestPrepareRejectsUnknownMethod(t *testing.T) {
	ws := scenario(t, "f.txt", "work\n", "")
	if _, err := merge.Prepare(ws, merge.Plan{
		Branch: "origin/agent/ticket-1", Target: "origin/main", Method: "yolo",
	}); err == nil {
		t.Fatal("an unknown merge method must be rejected, not silently defaulted")
	}
}
