package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/capture"
	"github.com/concourse/concourse/agent/implement"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/hangar"
)

const (
	cliRunID  = 41
	cliNumber = 3
	cliToken  = "cli-test-access"
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

// cliPlatform serves the public Run routes of one implement Run, requiring
// the saved login's bearer on every request.
type cliPlatform struct {
	server *httptest.Server
	mu     sync.Mutex
	run    atc.PipelineRun
	// archives are the Run's published results by name.
	archives map[string][]byte
	handoff  []byte
	// credential is the state the credential session reports until handoff.
	credential string
}

func (p *cliPlatform) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+cliToken {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	v1 := "/api/v1/teams/main/pipelines/implement"
	v2 := "/api/v2/teams/main/pipelines/implement"
	run := "/runs/" + strconv.Itoa(cliNumber)
	reply := func(status int, value any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(value)
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == v1+"/run-inputs/snapshot":
		_, _ = io.Copy(io.Discard, r.Body)
		reply(http.StatusCreated, atc.RunInputSource{SourceID: "input-v1-" + strings.Repeat("ab", 32), Bearer: "transient-bearer"})
	case r.Method == http.MethodPost && r.URL.Path == v2+"/runs":
		reply(http.StatusCreated, p.run)
	case r.URL.Path == v2+run+"/credentials/change":
		if r.Method == http.MethodPost {
			p.handoff, _ = io.ReadAll(r.Body)
			p.credential = "ready"
		}
		reply(http.StatusOK, atc.RunCredentialSession{RunID: cliRunID, Result: "change", Status: p.credential})
	case r.Method == http.MethodGet && r.URL.Path == v1+run:
		reply(http.StatusOK, p.run)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, v1+run+"/results/") && p.archives[strings.TrimPrefix(r.URL.Path, v1+run+"/results/")] != nil:
		archive := p.archives[strings.TrimPrefix(r.URL.Path, v1+run+"/results/")]
		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("Content-Length", strconv.Itoa(len(archive)))
		_, _ = w.Write(archive)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// complete publishes the worker's change and the template's validation of
// it as the Run's two bound results.
func (p *cliPlatform) complete(t *testing.T, s *implement.Snapshot, base string) {
	t.Helper()
	before := implement.Tree{"parser.go": {Data: []byte(base), Mode: "100644"}}
	after := implement.Tree{"parser.go": {Data: []byte(strings.Replace(base, "s[1]", "s[0]", 1)), Mode: "100644"}}
	patch, changes, err := implement.Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	run := cliRunID
	summary, err := implement.BuildSummary(s, patch, changes, []byte(`{"summary":"Return the first byte.","complete":true,"limitations":[]}`),
		implement.Metadata{RunID: &run, ModelRequested: "test-model", CodexVersion: "codex-cli test", Profile: implement.DefaultProfile()})
	if err != nil {
		t.Fatal(err)
	}
	summaryJSON, _ := json.Marshal(summary)
	failed := 1
	validation, _ := json.Marshal(implement.Validation{
		SchemaVersion: implement.ValidationVersion, RunID: cliRunID, InputDigest: s.Digest, PatchDigest: capture.Digest(patch),
		Applied: true, Command: "go test ./...", ExitCode: &failed, Outcome: implement.ValidationFailed, LogTail: "FAIL\n",
	})
	bindings := map[string]atc.RunResultBinding{}
	p.archives = map[string][]byte{}
	for name, files := range map[string]map[string][]byte{
		"change":     {implement.SummaryFile: summaryJSON, implement.PatchFile: patch},
		"validation": {implement.ValidationFile: validation},
	} {
		var raw bytes.Buffer
		tw := tar.NewWriter(&raw)
		for file, data := range files {
			_ = tw.WriteHeader(&tar.Header{Name: file, Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg})
			_, _ = tw.Write(data)
		}
		_ = tw.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		tree, err := (hangar.Canonicalizer{MaxContentBytes: 1 << 20, MaxEntries: 32}).Capture(ctx, &raw)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		archive, err := os.ReadFile(tree.ArchivePath)
		tree.Close()
		if err != nil {
			t.Fatal(err)
		}
		p.mu.Lock()
		p.archives[name] = archive
		p.mu.Unlock()
		bindings[name] = atc.RunResultBinding{Ref: hangar.TreeRef{Digest: tree.Digest}}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.run.Status = atc.RunStatusSucceeded
	p.run.Terminal = &atc.RunTerminalResult{Status: atc.RunStatusSucceeded, Results: bindings}
}

func jb(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, stderr bytes.Buffer
	err := run(context.Background(), args, &out, &stderr)
	return out.String(), err
}

// The whole detached path through the command: capture, submit with a
// credential handoff, status, result (JSON, Markdown, written to disk) and
// apply --run onto a new branch.
func TestImplementCommandsDriveADetachedRun(t *testing.T) {
	platform := &cliPlatform{credential: "available"}
	platform.run = atc.PipelineRun{ID: cliRunID, Number: cliNumber, ContractVersion: atc.RunContractV2, ActivationEpoch: 1, Status: atc.RunStatusRunning}
	platform.server = httptest.NewServer(http.HandlerFunc(platform.serve))
	defer platform.server.Close()
	t.Setenv("FLY_HOME", t.TempDir())
	if err := rc.SaveTarget("jb-test", platform.server.URL, false, "main", &rc.TargetToken{Type: "Bearer", Value: cliToken}, "", "", ""); err != nil {
		t.Fatal(err)
	}

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

	destination := []string{"--target", "jb-test", "--run", strconv.Itoa(cliNumber)}
	out, err := jb(t, "implement", "submit", "--target", "jb-test", "--input", snapshot, "--receipt", filepath.Join(root, "request.json"), "--auth-file", auth)
	if err != nil {
		t.Fatal(err)
	}
	var submitted struct {
		RunID int  `json:"run_id"`
		Ready bool `json:"ready"`
	}
	if err := json.Unmarshal([]byte(out), &submitted); err != nil || submitted.RunID != cliRunID || !submitted.Ready {
		t.Fatalf("submission did not report the ready Run: %s %v", out, err)
	}
	if string(platform.handoff) != `{"synthetic":"owner-auth"}` {
		t.Fatalf("the change producer did not receive the owner's auth file: %q", platform.handoff)
	}

	if _, err := jb(t, append([]string{"implement", "result"}, destination...)...); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("a running Run returned a change: %v", err)
	}
	platform.complete(t, loaded, base)

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
	if err := json.Unmarshal([]byte(out), &change); err != nil || change.Summary.RunID == nil || *change.Summary.RunID != cliRunID || !strings.Contains(change.Patch, "+func First(s string) byte { return s[0] }") {
		t.Fatalf("result did not return the verified change: %s %v", out, err)
	}
	if _, _, err := implement.ReadResult(retrieved); err != nil {
		t.Fatalf("result --output did not write an appliable change: %v", err)
	}
	out, err = jb(t, append([]string{"implement", "result", "--format", "markdown"}, destination...)...)
	if err != nil || !strings.Contains(out, "```diff\n") || !strings.Contains(out, "Run: 41") || !strings.Contains(out, "## Validation: failed") {
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
	if err := json.Unmarshal([]byte(out), &applied); err != nil || applied.Branch != "impl/run-41" || applied.BaseCommit != baseCommit {
		t.Fatalf("apply --run: %s %v", out, err)
	}
	if got := cliGit(t, repo, "rev-parse", "HEAD^"); got != baseCommit {
		t.Fatalf("applied commit is not on the base: %s", got)
	}
	if message := cliGit(t, repo, "log", "-1", "--format=%B"); !strings.Contains(message, "JetBridge-Run: 41") || !strings.Contains(message, "JetBridge-Input: "+loaded.Digest) {
		t.Fatalf("commit lacks provenance: %s", message)
	}
	// Nothing the command wrote holds the owner's credentials.
	receipt, err := os.ReadFile(filepath.Join(root, "request.json"))
	if err != nil || bytes.Contains(receipt, []byte("owner-auth")) || bytes.Contains(receipt, []byte("transient-bearer")) {
		t.Fatalf("receipt retained a secret: %s %v", receipt, err)
	}
}
