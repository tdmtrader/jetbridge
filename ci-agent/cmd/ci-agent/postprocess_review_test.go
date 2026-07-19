package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/concourse/ci-agent/phaseconfig"
)

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	run("remote", "add", "origin", "https://github.com/tdmtrader/jetbridge.git")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")
	return dir
}

func reviewConfig() *phaseconfig.Config {
	return &phaseconfig.Config{
		Name: "review",
		Steps: []phaseconfig.Step{
			{
				Name: "review-code",
				Artifacts: []phaseconfig.Artifact{
					{Name: "review", Path: "review.json"},
				},
			},
		},
	}
}

func TestFillReviewMetadataBackfillsEmptyFields(t *testing.T) {
	repoDir := initGitRepo(t)
	outputDir := t.TempDir()
	cfg := reviewConfig()

	raw := `{"schema_version":"1.0.0","proven_issues":[],"observations":[],"score":{"value":9,"max":10,"pass":true},"summary":"clean"}`
	if err := os.WriteFile(filepath.Join(outputDir, "review.json"), []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}

	if err := fillReviewMetadata(cfg, outputDir, repoDir, "claude", "claude-sonnet-5", 3*time.Second); err != nil {
		t.Fatalf("fillReviewMetadata: %v", err)
	}

	patched, err := os.ReadFile(filepath.Join(outputDir, "review.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(patched, &got); err != nil {
		t.Fatal(err)
	}
	metadata := got["metadata"].(map[string]any)

	if metadata["repo"] != "jetbridge" {
		t.Errorf("repo = %v, want jetbridge", metadata["repo"])
	}
	if commit, _ := metadata["commit"].(string); len(commit) != 40 {
		t.Errorf("commit = %q, want a 40-char sha", commit)
	}
	if metadata["agent_cli"] != "claude" {
		t.Errorf("agent_cli = %v, want claude", metadata["agent_cli"])
	}
	if metadata["agent_model"] != "claude-sonnet-5" {
		t.Errorf("agent_model = %v, want claude-sonnet-5", metadata["agent_model"])
	}
	if metadata["duration_seconds"].(float64) != 3 {
		t.Errorf("duration_seconds = %v, want 3", metadata["duration_seconds"])
	}
	if metadata["timestamp"] == "" || metadata["timestamp"] == nil {
		t.Error("timestamp not set")
	}

	// The schema-level Validate() only requires schema_version/summary, but
	// the ATC's ingest handler additionally requires repo/commit — confirm
	// those are non-empty after patching, since that's the whole point.
	if got["proven_issues"] == nil || got["observations"] == nil {
		t.Error("proven_issues/observations should survive the round-trip")
	}
}

func TestFillReviewMetadataSynthesizesIdsForIdlessFindings(t *testing.T) {
	repoDir := initGitRepo(t)
	outputDir := t.TempDir()
	cfg := reviewConfig()

	// Two proven issues and one observation with no id. The review UI keys
	// per-finding triage state (expansion, note, verdict) on the id, so two
	// id-less findings would collide onto "" and misattribute human feedback.
	// Postprocess must give each finding a distinct, non-empty id.
	raw := `{"schema_version":"1.0.0","metadata":{"repo":"r","commit":"c"},` +
		`"proven_issues":[` +
		`{"severity":"high","title":"first","file":"a.go","line":1,"test_file":"a_test.go","test_name":"T1","category":"correctness"},` +
		`{"severity":"high","title":"second","file":"b.go","line":2,"test_file":"b_test.go","test_name":"T2","category":"correctness"}` +
		`],"observations":[` +
		`{"title":"obs","file":"c.go","line":3,"category":"testing"}` +
		`],"score":{"value":9,"max":10,"pass":true},"summary":"has findings"}`
	if err := os.WriteFile(filepath.Join(outputDir, "review.json"), []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}

	if err := fillReviewMetadata(cfg, outputDir, repoDir, "claude", "claude-sonnet-5", time.Second); err != nil {
		t.Fatalf("fillReviewMetadata: %v", err)
	}

	patched, _ := os.ReadFile(filepath.Join(outputDir, "review.json"))
	var got map[string]any
	if err := json.Unmarshal(patched, &got); err != nil {
		t.Fatal(err)
	}

	ids := map[string]bool{}
	collect := func(key string) {
		for _, f := range got[key].([]any) {
			id, _ := f.(map[string]any)["id"].(string)
			if id == "" {
				t.Errorf("%s finding still has an empty id after postprocess", key)
			}
			if ids[id] {
				t.Errorf("duplicate synthesized id %q across findings", id)
			}
			ids[id] = true
		}
	}
	collect("proven_issues")
	collect("observations")

	if len(ids) != 3 {
		t.Errorf("expected 3 distinct finding ids, got %d: %v", len(ids), ids)
	}
}

func TestFillReviewMetadataKeepsExistingFindingIds(t *testing.T) {
	repoDir := initGitRepo(t)
	outputDir := t.TempDir()
	cfg := reviewConfig()

	raw := `{"schema_version":"1.0.0","metadata":{"repo":"r","commit":"c"},` +
		`"proven_issues":[{"id":"PI-7","severity":"high","title":"t","file":"a.go","line":1,"test_file":"a_test.go","test_name":"T","category":"correctness"}],` +
		`"observations":[],"score":{"value":9,"max":10,"pass":true},"summary":"one"}`
	if err := os.WriteFile(filepath.Join(outputDir, "review.json"), []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}

	if err := fillReviewMetadata(cfg, outputDir, repoDir, "claude", "", time.Second); err != nil {
		t.Fatalf("fillReviewMetadata: %v", err)
	}

	patched, _ := os.ReadFile(filepath.Join(outputDir, "review.json"))
	var got map[string]any
	json.Unmarshal(patched, &got)
	first := got["proven_issues"].([]any)[0].(map[string]any)
	if first["id"] != "PI-7" {
		t.Errorf("id = %v, want PI-7 (existing ids must be preserved)", first["id"])
	}
}

func TestFillReviewMetadataDoesNotOverwriteExisting(t *testing.T) {
	repoDir := initGitRepo(t)
	outputDir := t.TempDir()
	cfg := reviewConfig()

	raw := `{"schema_version":"1.0.0","metadata":{"repo":"already-set","commit":"deadbeef"},"proven_issues":[],"observations":[],"score":{"value":9,"max":10,"pass":true},"summary":"clean"}`
	if err := os.WriteFile(filepath.Join(outputDir, "review.json"), []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}

	if err := fillReviewMetadata(cfg, outputDir, repoDir, "claude", "claude-sonnet-5", time.Second); err != nil {
		t.Fatalf("fillReviewMetadata: %v", err)
	}

	patched, _ := os.ReadFile(filepath.Join(outputDir, "review.json"))
	var got map[string]any
	json.Unmarshal(patched, &got)
	metadata := got["metadata"].(map[string]any)

	if metadata["repo"] != "already-set" {
		t.Errorf("repo = %v, want already-set (should not overwrite)", metadata["repo"])
	}
	if metadata["commit"] != "deadbeef" {
		t.Errorf("commit = %v, want deadbeef (should not overwrite)", metadata["commit"])
	}
}

func TestFillReviewMetadataNoopsForNonReviewPhase(t *testing.T) {
	cfg := &phaseconfig.Config{Name: "qa"}
	if err := fillReviewMetadata(cfg, t.TempDir(), t.TempDir(), "claude", "", time.Second); err != nil {
		t.Fatalf("expected no error for a phase with no review artifact, got %v", err)
	}
}

func TestRepoNameFromGitRemote(t *testing.T) {
	repoDir := initGitRepo(t)
	if got := repoNameFromGitRemote(repoDir); got != "jetbridge" {
		t.Errorf("repoNameFromGitRemote = %q, want jetbridge", got)
	}
}
