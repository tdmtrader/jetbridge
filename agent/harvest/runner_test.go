package harvest_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/harvest"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// workspaceWithRemote builds the harvest input shape: a bare "remote"
// and a workspace clone with one committed change on a work branch's
// content (still on the default branch — harvest pushes by sha, the
// local branch name is irrelevant).
func workspaceWithRemote(t *testing.T) (workspace, remote string) {
	t.Helper()
	root := t.TempDir()
	remote = filepath.Join(root, "remote.git")
	git(t, root, "init", "--bare", "-b", "main", remote)

	seed := filepath.Join(root, "seed")
	git(t, root, "clone", remote, seed)
	os.WriteFile(filepath.Join(seed, "README.md"), []byte("hello\n"), 0644)
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-m", "seed")
	git(t, seed, "push", "origin", "HEAD:main")

	workspace = filepath.Join(root, "workspace")
	git(t, root, "clone", remote, workspace)
	os.WriteFile(filepath.Join(workspace, "report.md"), []byte("the work\n"), 0644)
	git(t, workspace, "add", ".")
	git(t, workspace, "commit", "-m", "agent work for ticket 42")
	return workspace, remote
}

func runHarvest(t *testing.T, cfg harvest.Config, workspace string) (int, harvest.Results, string) {
	t.Helper()
	var out bytes.Buffer
	code := harvest.Run(cfg, workspace, "", &out)
	var res harvest.Results
	if out.Len() > 0 {
		if err := json.Unmarshal(out.Bytes(), &res); err != nil {
			t.Fatalf("results not JSON: %v\n%s", err, out.String())
		}
	}
	return code, res, out.String()
}

func TestRunPushesCommittedWorkBySha(t *testing.T) {
	workspace, remote := workspaceWithRemote(t)
	head := git(t, workspace, "rev-parse", "HEAD")

	cfg := harvest.Config{
		StepName: "harvest", Workspace: "workspace", Repo: "tdmtrader/jetbridge",
		TargetBranch: "main", TicketID: 42, Branch: "agent/ticket-42", Push: true,
	}
	code, res, raw := runHarvest(t, cfg, workspace)
	if code != 0 {
		t.Fatalf("exit = %d, output: %s", code, raw)
	}
	if res.Status != "pass" || res.Metadata.PushedBranch != "agent/ticket-42" || res.Metadata.HeadSHA != head {
		t.Errorf("results = %+v", res)
	}

	remoteSHA := git(t, remote, "rev-parse", "refs/heads/agent/ticket-42")
	if remoteSHA != head {
		t.Errorf("remote branch = %s, want %s", remoteSHA, head)
	}
}

func TestRunRePushUpdatesTheBranch(t *testing.T) {
	workspace, remote := workspaceWithRemote(t)
	cfg := harvest.Config{
		StepName: "harvest", Workspace: "workspace", Repo: "r",
		TicketID: 42, Branch: "agent/ticket-42", Push: true,
	}
	if code, _, raw := runHarvest(t, cfg, workspace); code != 0 {
		t.Fatalf("first push exit = %d: %s", code, raw)
	}

	// attempt 2 of the rework loop: new commit, same stable branch
	os.WriteFile(filepath.Join(workspace, "report.md"), []byte("more work\n"), 0644)
	git(t, workspace, "add", ".")
	git(t, workspace, "commit", "-m", "rework")
	head2 := git(t, workspace, "rev-parse", "HEAD")

	if code, _, raw := runHarvest(t, cfg, workspace); code != 0 {
		t.Fatalf("re-push exit = %d: %s", code, raw)
	}
	if remoteSHA := git(t, remote, "rev-parse", "refs/heads/agent/ticket-42"); remoteSHA != head2 {
		t.Errorf("remote branch = %s, want %s", remoteSHA, head2)
	}
}

func TestRunDirtyWorktreeFailsWithoutPushing(t *testing.T) {
	workspace, remote := workspaceWithRemote(t)
	os.WriteFile(filepath.Join(workspace, "uncommitted.txt"), []byte("oops\n"), 0644)

	cfg := harvest.Config{
		StepName: "harvest", Workspace: "workspace", Repo: "r",
		TicketID: 42, Branch: "agent/ticket-42", Push: true,
	}
	code, res, _ := runHarvest(t, cfg, workspace)
	if code != 1 {
		t.Fatalf("dirty tree exit = %d, want 1 (F33: agent failure, never auto-discarded)", code)
	}
	if res.Status != "fail" || !strings.Contains(res.Metadata.Detail, "workspace-dirty") {
		t.Errorf("results = %+v", res)
	}

	cmd := exec.Command("git", "rev-parse", "refs/heads/agent/ticket-42")
	cmd.Dir = remote
	if err := cmd.Run(); err == nil {
		t.Error("dirty tree must not push")
	}
}

func TestRunNonRepoWorkspaceFails(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "report.md"), []byte("x\n"), 0644)

	code, res, _ := runHarvest(t, harvest.Config{StepName: "h", Workspace: "workspace", Repo: "r", TicketID: 1, Branch: "agent/ticket-1", Push: true}, dir)
	if code != 1 {
		t.Fatalf("non-repo exit = %d, want 1", code)
	}
	if res.Status != "fail" {
		t.Errorf("results = %+v", res)
	}
}

func TestRunRefusesGatesAndJudgeInV0(t *testing.T) {
	workspace, _ := workspaceWithRemote(t)

	gated := harvest.Config{
		StepName: "h", Workspace: "workspace", Repo: "r", TicketID: 1, Branch: "agent/ticket-1",
		GatePolicy: harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "test", Scope: "affected"}}},
	}
	if code, res, _ := runHarvest(t, gated, workspace); code != 2 || res.Status != "error" {
		t.Errorf("gates: exit %d results %+v, want 2/error (loud refusal, not a silent skip)", code, res)
	}

	judged := harvest.Config{
		StepName: "h", Workspace: "workspace", Repo: "r", TicketID: 1, Branch: "agent/ticket-1",
		Judge: &harvest.JudgeConfig{Rubric: []harvest.RubricDimension{{Name: "c", Weight: 1}}, PassThreshold: 6},
	}
	if code, res, _ := runHarvest(t, judged, workspace); code != 2 || res.Status != "error" {
		t.Errorf("judge: exit %d results %+v, want 2/error", code, res)
	}
}

func TestRunPushFalseVerifiesOnly(t *testing.T) {
	workspace, remote := workspaceWithRemote(t)
	cfg := harvest.Config{StepName: "h", Workspace: "workspace", Repo: "r", TicketID: 7}
	code, res, _ := runHarvest(t, cfg, workspace)
	if code != 0 || res.Status != "pass" || res.Metadata.PushedBranch != "" {
		t.Errorf("verify-only: exit %d results %+v", code, res)
	}
	cmd := exec.Command("git", "rev-parse", "refs/heads/agent/ticket-7")
	cmd.Dir = remote
	if err := cmd.Run(); err == nil {
		t.Error("push=false must not push")
	}
}
