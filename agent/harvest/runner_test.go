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

func TestRunRefusesNonFullScopeGatesAndJudgeInV0(t *testing.T) {
	// v0.5 boundary (loud, never silent): the in-pod gate engine only
	// enforces scope "full" — affected/affected_then_full still error
	// (dev-mcp, the wave-3 executor, isn't wired yet) — and judge is
	// still fully refused (out of scope for this slice).
	workspace, _ := workspaceWithRemote(t)

	gated := harvest.Config{
		StepName: "h", Workspace: "workspace", Repo: "r", TicketID: 1, Branch: "agent/ticket-1",
		GatePolicy: harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "test", Scope: "affected"}}, OnGateFailure: "needs_review"},
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

// seedGoModule commits a throwaway Go module's worth of files into an
// already-cloned workspace so full-scope gates (the fixed v0.5
// build|test|lint command map) have something real to run.
func seedGoModule(t *testing.T, workspace string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	git(t, workspace, "add", ".")
	git(t, workspace, "commit", "-m", "seed go module for gates")
}

const gateFixtureGoMod = "module fixture\n\ngo 1.21\n"
const gateFixtureMain = "package p\n\nfunc F() int { return 1 }\n"

func TestRunGatesPassPushesTheBranch(t *testing.T) {
	workspace, remote := workspaceWithRemote(t)
	seedGoModule(t, workspace, map[string]string{
		"go.mod":    gateFixtureGoMod,
		"main.go":   gateFixtureMain,
		"f_test.go": "package p\n\nimport \"testing\"\n\nfunc TestF(t *testing.T) {\n\tif F() != 1 {\n\t\tt.Fatal(\"wrong\")\n\t}\n}\n",
	})
	head := git(t, workspace, "rev-parse", "HEAD")

	cfg := harvest.Config{
		StepName: "h", Workspace: "workspace", Repo: "r", TicketID: 1, Branch: "agent/ticket-1", Push: true,
		GatePolicy: harvest.GatePolicy{
			Gates:         []harvest.Gate{{Gate: "build", Scope: "full"}, {Gate: "test", Scope: "full"}},
			OnGateFailure: "needs_review",
		},
	}
	code, res, raw := runHarvest(t, cfg, workspace)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (gates pass): %s", code, raw)
	}
	if res.Status != "pass" || res.Metadata.PushedBranch != "agent/ticket-1" {
		t.Errorf("results = %+v", res)
	}
	if len(res.Metadata.Gates) != 2 {
		t.Fatalf("gate outcomes = %+v, want 2", res.Metadata.Gates)
	}
	for _, g := range res.Metadata.Gates {
		if g.Status != "ok" {
			t.Errorf("gate %s: status = %q, want ok", g.Gate, g.Status)
		}
	}

	remoteSHA := git(t, remote, "rev-parse", "refs/heads/agent/ticket-1")
	if remoteSHA != head {
		t.Errorf("remote branch = %s, want %s (gates-pass must push)", remoteSHA, head)
	}
}

func TestRunGatesFailBlocksThePush(t *testing.T) {
	workspace, remote := workspaceWithRemote(t)
	seedGoModule(t, workspace, map[string]string{
		"go.mod":    gateFixtureGoMod,
		"main.go":   gateFixtureMain,
		"f_test.go": "package p\n\nimport \"testing\"\n\nfunc TestF(t *testing.T) {\n\tt.Fatal(\"always fails\")\n}\n",
	})

	cfg := harvest.Config{
		StepName: "h", Workspace: "workspace", Repo: "r", TicketID: 1, Branch: "agent/ticket-1", Push: true,
		GatePolicy: harvest.GatePolicy{
			Gates:         []harvest.Gate{{Gate: "test", Scope: "full"}},
			OnGateFailure: "needs_review",
		},
	}
	code, res, raw := runHarvest(t, cfg, workspace)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (gate failed): %s", code, raw)
	}
	if res.Status != "fail" {
		t.Errorf("results = %+v", res)
	}
	if len(res.Metadata.Gates) != 1 || res.Metadata.Gates[0].Status != "failed" {
		t.Errorf("gate outcomes = %+v, want one failed gate", res.Metadata.Gates)
	}
	if res.Metadata.PushedBranch != "" {
		t.Errorf("pushed branch = %q, want empty — a failed gate must not push", res.Metadata.PushedBranch)
	}

	cmd := exec.Command("git", "rev-parse", "refs/heads/agent/ticket-1")
	cmd.Dir = remote
	if err := cmd.Run(); err == nil {
		t.Error("a failed gate must not push — remote branch must not exist")
	}
}

func TestRunGateErrorExitsPlatformError(t *testing.T) {
	workspace, remote := workspaceWithRemote(t)

	cfg := harvest.Config{
		StepName: "h", Workspace: "workspace", Repo: "r", TicketID: 1, Branch: "agent/ticket-1", Push: true,
		GatePolicy: harvest.GatePolicy{
			Gates:         []harvest.Gate{{Gate: "build", Scope: "affected"}},
			OnGateFailure: "needs_review",
		},
	}
	code, res, raw := runHarvest(t, cfg, workspace)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (gate errored): %s", code, raw)
	}
	if res.Status != "error" {
		t.Errorf("results = %+v", res)
	}
	if len(res.Metadata.Gates) != 1 || res.Metadata.Gates[0].Status != "error" {
		t.Errorf("gate outcomes = %+v, want one errored gate", res.Metadata.Gates)
	}
	if res.Metadata.PushedBranch != "" {
		t.Errorf("pushed branch = %q, want empty — an errored gate must not push", res.Metadata.PushedBranch)
	}

	cmd := exec.Command("git", "rev-parse", "refs/heads/agent/ticket-1")
	cmd.Dir = remote
	if err := cmd.Run(); err == nil {
		t.Error("an errored gate must not push — remote branch must not exist")
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
