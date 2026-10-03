package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/capture"
	"github.com/concourse/concourse/agent/implement"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/atctest"
	"github.com/concourse/concourse/fly/rc"
	"sigs.k8s.io/yaml"
)

func cliGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", repo, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false"}, args...)...)
	c.Env = capture.GitEnvironment()
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestMain(m *testing.M) { os.Exit(atctest.Run(m)) }

// cliPlatform is the real platform, seen by one test through its own team,
// with the implement and review templates installed as deploy/ ships them and
// a saved fly login, jb-test, for that team.
type cliPlatform struct {
	*atctest.Platform
	team string
	// grants are the Run input grants the platform issued to jb-test.
	grants func() []string
}

func newCLIPlatform(t *testing.T) *cliPlatform {
	t.Helper()
	p := &cliPlatform{Platform: atctest.Get(t)}
	p.team = p.NewTeam(t)
	for name, vars := range map[string][]string{
		"implement": {"((review_worker_image))", atctest.Pin, "((implement_model))", "some-model",
			"((validate_image))", "registry.example/toolchain@sha256:" + strings.Repeat("fe", 32), "((validate_command))", "go test ./..."},
		"review": {"((review_worker_image))", atctest.Pin, "((review_model))", "review-model"},
	} {
		data, err := os.ReadFile(filepath.Join("..", "..", "deploy", name+"-template.yml"))
		if err != nil {
			t.Fatal(err)
		}
		var config atc.Config
		if err := yaml.Unmarshal([]byte(strings.NewReplacer(vars...).Replace(string(data))), &config); err != nil {
			t.Fatal(err)
		}
		p.Install(t, p.team, name, config)
	}
	t.Setenv("FLY_HOME", t.TempDir())
	// jb-test reaches the platform through a decorator that keeps every
	// input grant issued, so a test can look for the real grant later.
	url, grants := p.RecordInputGrants(t)
	p.grants = grants
	if err := rc.SaveTarget("jb-test", url, false, p.team, &rc.TargetToken{Type: "Bearer", Value: p.Token(t)}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	return p
}

// runID is the ID of Run number of template.
func (p *cliPlatform) runID(t *testing.T, template string, number int) int {
	t.Helper()
	out, err := jb(t, "implement", "status", "--target", "jb-test", "--template", template, "--run", strconv.Itoa(number))
	var run atc.PipelineRun
	if err != nil || json.Unmarshal([]byte(out), &run) != nil {
		t.Fatalf("status of %s Run %d: %s %v", template, number, out, err)
	}
	return run.ID
}

// complete publishes the worker's change and the template's validation of
// it as implement Run number's two results, and finishes the Run.
func (p *cliPlatform) complete(t *testing.T, number int, s *implement.Snapshot, base string) int {
	t.Helper()
	run := p.runID(t, "implement", number)
	before := implement.Tree{"parser.go": {Data: []byte(base), Mode: "100644"}}
	after := implement.Tree{"parser.go": {Data: []byte(strings.Replace(base, "s[1]", "s[0]", 1)), Mode: "100644"}}
	patch, changes, err := implement.Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := implement.BuildSummary(s, patch, changes, []byte(`{"summary":"Return the first byte.","complete":true,"limitations":[]}`),
		implement.Metadata{RunID: &run, ModelRequested: "test-model", CodexVersion: "codex-cli test", Profile: implement.DefaultProfile()})
	if err != nil {
		t.Fatal(err)
	}
	summaryJSON, _ := json.Marshal(summary)
	failed := 1
	validation, _ := json.Marshal(implement.Validation{
		SchemaVersion: implement.ValidationVersion, RunID: run, InputDigest: s.Digest, PatchDigest: capture.Digest(patch),
		Applied: true, Command: "go test ./...", ExitCode: &failed, Outcome: implement.ValidationFailed, LogTail: "FAIL\n",
	})
	p.Succeed(t, p.team, "implement", number, map[string]map[string][]byte{
		"change":     {implement.SummaryFile: summaryJSON, implement.PatchFile: patch},
		"validation": {implement.ValidationFile: validation},
	})
	return run
}

// capturedSnapshot captures a one-file repository's base with a brief, as
// jb implement capture does, and returns the snapshot directory.
func capturedSnapshot(t *testing.T, root string) string {
	t.Helper()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	cliGit(t, repo, "init", "-q")
	cliGit(t, repo, "config", "user.name", "CLI fixture")
	cliGit(t, repo, "config", "user.email", "cli@example.test")
	brief := filepath.Join(root, "brief.md")
	for name, text := range map[string]string{filepath.Join(repo, "parser.go"): "package parser\n", brief: "Return the first byte.\n"} {
		if err := os.WriteFile(name, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cliGit(t, repo, "add", ".")
	cliGit(t, repo, "commit", "-qm", "base")
	snapshot := filepath.Join(root, "snapshot")
	if _, err := jb(t, "implement", "capture", "--repo", repo, "--brief", brief, "--output", snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func jb(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, stderr bytes.Buffer
	err := run(context.Background(), args, &out, &stderr)
	return out.String(), err
}

// The whole submitted path through the command: capture, submit with a
// credential handoff, status, result (JSON, Markdown, written to disk) and
// apply --run onto a new branch.
func TestImplementCommandsDriveARun(t *testing.T) {
	p := newCLIPlatform(t)
	p.Schedule(t, p.team, "implement", "change")

	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	cliGit(t, repo, "init", "-q")
	cliGit(t, repo, "config", "user.name", "CLI fixture")
	cliGit(t, repo, "config", "user.email", "cli@example.test")
	base := "package parser\nfunc First(s string) byte { return s[1] }\n"
	if err := os.WriteFile(filepath.Join(repo, "parser.go"), []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	cliGit(t, repo, "add", ".")
	cliGit(t, repo, "commit", "-qm", "base")
	baseCommit := cliGit(t, repo, "rev-parse", "HEAD")
	brief := filepath.Join(root, "brief.md")
	auth := filepath.Join(root, "auth.json")
	for name, text := range map[string]string{brief: "Return the first byte.\n", auth: `{"synthetic":"owner-auth"}`} {
		if err := os.WriteFile(name, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := filepath.Join(root, "snapshot")
	if _, err := jb(t, "implement", "capture", "--repo", repo, "--brief", brief, "--output", snapshot); err != nil {
		t.Fatal(err)
	}
	loaded, err := implement.LoadSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}

	out, err := jb(t, "implement", "submit", "--target", "jb-test", "--input", snapshot, "--receipt", filepath.Join(root, "request.json"), "--auth-file", auth)
	if err != nil {
		t.Fatal(err)
	}
	var submitted struct {
		RunID  int  `json:"run_id"`
		Ready  bool `json:"ready"`
		Handle struct {
			Number int `json:"number"`
		} `json:"handle"`
	}
	if err := json.Unmarshal([]byte(out), &submitted); err != nil || submitted.RunID < 1 || !submitted.Ready {
		t.Fatalf("submission did not report the ready Run: %s %v", out, err)
	}
	if delivered, _ := p.Delivered(submitted.RunID); string(delivered) != `{"synthetic":"owner-auth"}` {
		t.Fatalf("the change producer did not receive the owner's auth file: %q", delivered)
	}
	destination := []string{"--target", "jb-test", "--run", strconv.Itoa(submitted.Handle.Number)}

	if _, err := jb(t, append([]string{"implement", "result"}, destination...)...); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("a running Run returned a change: %v", err)
	}
	runID := p.complete(t, submitted.Handle.Number, loaded, base)

	out, err = jb(t, append([]string{"implement", "status"}, destination...)...)
	if err != nil || !strings.Contains(out, `"status":"succeeded"`) {
		t.Fatalf("status: %s %v", out, err)
	}
	retrieved := filepath.Join(root, "retrieved")
	out, err = jb(t, append([]string{"implement", "result", "--output", retrieved}, destination...)...)
	if err != nil {
		t.Fatal(err)
	}
	var change struct {
		Summary implement.Summary `json:"summary"`
		Patch   string            `json:"patch"`
	}
	if err := json.Unmarshal([]byte(out), &change); err != nil || change.Summary.RunID == nil || *change.Summary.RunID != runID || !strings.Contains(change.Patch, "+func First(s string) byte { return s[0] }") {
		t.Fatalf("result did not return the verified change: %s %v", out, err)
	}
	if _, _, err := implement.ReadResult(retrieved); err != nil {
		t.Fatalf("result --output did not write an appliable change: %v", err)
	}
	out, err = jb(t, append([]string{"implement", "result", "--format", "markdown"}, destination...)...)
	if err != nil || !strings.Contains(out, "```diff\n") || !strings.Contains(out, fmt.Sprintf("Run: %d", runID)) || !strings.Contains(out, "## Validation: failed") {
		t.Fatalf("markdown result does not show the change with its validation: %s %v", out, err)
	}
	// A failing validation is retrievable beside the change it tested.
	out, err = jb(t, append([]string{"implement", "result", "--result", "validation"}, destination...)...)
	var validation implement.Validation
	if err != nil || json.Unmarshal([]byte(out), &validation) != nil || validation.Outcome != implement.ValidationFailed || validation.PatchDigest != change.Summary.PatchDigest {
		t.Fatalf("validation result: %s %v", out, err)
	}
	out, err = jb(t, append([]string{"implement", "result", "--result", "validation", "--format", "markdown"}, destination...)...)
	if err != nil || !strings.Contains(out, "## Validation: failed") || !strings.Contains(out, "```diff\n") {
		t.Fatalf("validation markdown does not show both results: %s %v", out, err)
	}
	if _, err := jb(t, append([]string{"implement", "result", "--result", "validation", "--output", filepath.Join(root, "validation")}, destination...)...); err == nil {
		t.Fatal("result --output wrote a validation as if it were a change")
	}

	if _, err := jb(t, append([]string{"implement", "apply", "--repo", repo, "--result-dir", retrieved}, destination...)...); err == nil {
		t.Fatal("apply accepted both --run and --result-dir")
	}
	out, err = jb(t, append([]string{"implement", "apply", "--repo", repo}, destination...)...)
	if err != nil {
		t.Fatal(err)
	}
	var applied implement.Applied
	if err := json.Unmarshal([]byte(out), &applied); err != nil || applied.Branch != fmt.Sprintf("impl/run-%d", runID) || applied.BaseCommit != baseCommit {
		t.Fatalf("apply --run: %s %v", out, err)
	}
	if got := cliGit(t, repo, "rev-parse", applied.Branch+"^"); got != baseCommit {
		t.Fatalf("applied commit is not on the base: %s", got)
	}
	if got := cliGit(t, repo, "rev-parse", "HEAD"); got != baseCommit {
		t.Fatalf("apply moved HEAD to %s", got)
	}
	if message := cliGit(t, repo, "log", "-1", "--format=%B", applied.Branch); !strings.Contains(message, fmt.Sprintf("JetBridge-Run: %d", runID)) || !strings.Contains(message, "JetBridge-Input: "+loaded.Digest) {
		t.Fatalf("commit lacks provenance: %s", message)
	}
	// Nothing the command wrote holds the owner's credentials.
	receipt, err := os.ReadFile(filepath.Join(root, "request.json"))
	if err != nil || bytes.Contains(receipt, []byte("owner-auth")) {
		t.Fatalf("receipt retained a secret: %s %v", receipt, err)
	}
	grants := p.grants()
	if len(grants) == 0 {
		t.Fatal("the platform issued no input grant")
	}
	for _, grant := range grants {
		if bytes.Contains(receipt, []byte(grant)) {
			t.Fatalf("receipt retained the input grant: %s", receipt)
		}
	}
}
