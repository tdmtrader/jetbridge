package implement

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const syntheticAuth = `{"auth_mode":"chatgpt","tokens":{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","id_token":"synthetic-id"}}`

// buildProvider compiles the fake Codex used by the Brine features: a real
// subprocess that edits its working directory and reports the edits.
func buildProvider(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	out := filepath.Join(t.TempDir(), "provider")
	c := exec.Command("go", "build", "-o", out, "github.com/concourse/concourse/agent/review/testdata/provider")
	if b, err := c.CombinedOutput(); err != nil {
		t.Fatalf("build fake provider: %v: %s", err, b)
	}
	return out
}

// runInRuntime is the worker without its tmpfs gate, so the session, trace,
// diff and publication run on any host. The gate itself is covered by
// TestWorkerRequiresMemoryRuntime and the Linux-only tests.
func TestWorkerSessionWithFakeProvider(t *testing.T) {
	provider := buildProvider(t)
	for _, mode := range []string{"edit", "edit-outside-scratch", "forbidden-shell", "malformed-edit"} {
		t.Run(mode, func(t *testing.T) {
			repo, s := snapshotFixture(t)
			runtimeDir := t.TempDir()
			output := filepath.Join(t.TempDir(), "change")
			summary, err := runInRuntime(context.Background(), WorkerOptions{
				Input: s.Dir, Output: output, RuntimeDir: runtimeDir, Codex: provider, ToolsCommand: "/unused/workspace-tools",
				Model: mode, Auth: io.NopCloser(strings.NewReader(syntheticAuth)), Timeout: 10 * time.Second,
			})
			if entries, _ := os.ReadDir(runtimeDir); len(entries) != 0 {
				t.Fatalf("session survived: %v", entries)
			}
			if mode != "edit" {
				want := map[string]string{"edit-outside-scratch": "outside the workspace", "forbidden-shell": "outside the edit-only policy", "malformed-edit": "malformed Codex file change"}[mode]
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("want refusal naming %q, got %v", want, err)
				}
				if _, statErr := os.Lstat(output); !os.IsNotExist(statErr) {
					t.Fatal("a refused session published output")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{PatchFile, SummaryFile} {
				b, err := os.ReadFile(filepath.Join(output, name))
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(b), "synthetic-") {
					t.Fatal("credential reached the published change")
				}
			}
			if summary.Provenance.CodexVersion == "" || summary.Provenance.ModelRequested != "edit" || !summary.Complete {
				t.Fatalf("summary %+v", summary)
			}
			// The published change is immutable.
			if _, err := runInRuntime(context.Background(), WorkerOptions{
				Input: s.Dir, Output: output, RuntimeDir: runtimeDir, Codex: provider, ToolsCommand: "/unused/workspace-tools",
				Model: mode, Auth: io.NopCloser(strings.NewReader(syntheticAuth)), Timeout: 10 * time.Second,
			}); err == nil {
				t.Fatal("overwrote a published change")
			}
			applied, err := Apply(context.Background(), ApplyOptions{Repo: repo, ResultDir: output})
			if err != nil {
				t.Fatal(err)
			}
			if got := gitTest(t, repo, "diff", "--name-status", s.Manifest.BaseCommit, applied.Commit); got != "D\tdeleted.txt\nM\tparser.go\nA\tparser_test.go" {
				t.Fatalf("applied change:\n%s", got)
			}
		})
	}
}
