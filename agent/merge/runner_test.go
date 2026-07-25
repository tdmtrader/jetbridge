package merge_test

import (
	"errors"
	"io"
	"testing"

	"github.com/concourse/concourse/agent/merge"
)

func runCfg() merge.Config {
	return merge.Config{
		Repo: "tdmtrader/jetbridge", TicketID: 1,
		Branch: "agent/ticket-1", TargetBranch: "main",
		Method: merge.MethodMerge, Message: "merge ticket 1", Push: true,
	}
}

func noGates(string) error { return nil }

func TestRunMergesAndPushesTheTarget(t *testing.T) {
	ws := scenario(t, "f.txt", "work\n", "moved\n")

	out, code := merge.Run(runCfg(), ws, noGates, io.Discard)
	if code != 0 {
		t.Fatalf("exit = %d, outcome = %+v", code, out)
	}
	if !out.Pushed || out.ResultSha == "" {
		t.Fatalf("expected a pushed merge, got %+v", out)
	}
	// the remote target must now BE the merge result
	git(t, ws, "fetch", "origin")
	if remote := git(t, ws, "rev-parse", "origin/main"); remote != out.ResultSha {
		t.Fatalf("origin/main = %s, want the merge result %s", remote, out.ResultSha)
	}
}

// The whole point of gating at merge time: gates run against the MERGED
// result, and a failure blocks the push.
func TestRunGateFailureBlocksThePush(t *testing.T) {
	ws := scenario(t, "f.txt", "work\n", "moved\n")
	before := git(t, ws, "rev-parse", "origin/main")

	failing := func(string) error { return errors.New("unit tests failed on the merged tree") }
	out, code := merge.Run(runCfg(), ws, failing, io.Discard)

	if code != 1 {
		t.Fatalf("gate failure must exit 1, got %d (%+v)", code, out)
	}
	if out.Pushed {
		t.Fatal("a failing gate must never push")
	}
	git(t, ws, "fetch", "origin")
	if after := git(t, ws, "rev-parse", "origin/main"); after != before {
		t.Fatal("origin/main moved despite a failing gate")
	}
}

func TestRunConflictBlocksThePushAndReportsPaths(t *testing.T) {
	// both sides edit base.txt => conflict
	ws := scenario(t, "base.txt", "agent version\n", "")
	git(t, ws, "checkout", "main")
	write(t, ws, "base.txt", "target version\n")
	git(t, ws, "add", ".")
	git(t, ws, "commit", "-m", "target edits base")
	git(t, ws, "push", "origin", "HEAD:main")
	before := git(t, ws, "rev-parse", "HEAD")

	out, code := merge.Run(runCfg(), ws, noGates, io.Discard)

	if code != 1 {
		t.Fatalf("a conflict must exit 1, got %d (%+v)", code, out)
	}
	if !out.Conflict || len(out.ConflictPaths) == 0 {
		t.Fatalf("expected reported conflict paths, got %+v", out)
	}
	if out.Pushed {
		t.Fatal("a conflicting merge must never push")
	}
	git(t, ws, "fetch", "origin")
	if after := git(t, ws, "rev-parse", "origin/main"); after != before {
		t.Fatal("origin/main moved despite a conflict")
	}
}

// Push:false is the speculative query — "would this land cleanly and pass?"
// It must answer without mutating the remote.
func TestRunSpeculativeDoesNotPush(t *testing.T) {
	ws := scenario(t, "f.txt", "work\n", "moved\n")
	before := git(t, ws, "rev-parse", "origin/main")

	cfg := runCfg()
	cfg.Push = false
	out, code := merge.Run(cfg, ws, noGates, io.Discard)

	if code != 0 {
		t.Fatalf("a clean speculative merge must exit 0, got %d (%+v)", code, out)
	}
	if out.Pushed {
		t.Fatal("Push:false must not push")
	}
	if out.ResultSha == "" {
		t.Fatal("a speculative run must still report what would land")
	}
	git(t, ws, "fetch", "origin")
	if after := git(t, ws, "rev-parse", "origin/main"); after != before {
		t.Fatal("Push:false mutated the remote")
	}
}

func TestRunRejectsIncompleteConfig(t *testing.T) {
	ws := scenario(t, "f.txt", "work\n", "")
	cfg := runCfg()
	cfg.TargetBranch = ""
	if _, code := merge.Run(cfg, ws, noGates, io.Discard); code != 2 {
		t.Fatalf("a missing target branch is a tooling fault (exit 2), got %d", code)
	}
}
