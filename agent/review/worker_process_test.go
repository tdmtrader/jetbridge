package review

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

// runInRuntime is the worker without its tmpfs gate, so the review session
// that now runs through agent/session is exercised on any host against the
// same compiled fake Codex the Brine features use.
func TestReviewSessionWithFakeProvider(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	provider := filepath.Join(t.TempDir(), "provider")
	if b, err := exec.Command("go", "build", "-o", provider, "github.com/concourse/concourse/agent/review/testdata/provider").CombinedOutput(); err != nil {
		t.Fatalf("build fake provider: %v: %s", err, b)
	}
	auth := `{"auth_mode":"chatgpt","tokens":{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","id_token":"synthetic-id"}}`
	for mode, verdict := range map[string]string{"valid": "no_findings", "finding": "findings", "partial": "incomplete", "forbidden": "", "nonzero": "", "malformed": "", "missing": "", "foreign-path": ""} {
		t.Run(mode, func(t *testing.T) {
			b := captureTest(t)
			runtimeDir := t.TempDir()
			output := filepath.Join(t.TempDir(), "report")
			report, err := runInRuntime(context.Background(), WorkerOptions{Input: b.Dir, Output: output, RuntimeDir: runtimeDir, Codex: provider,
				ReaderCommand: "/unused/input-tools", Model: mode, Auth: io.NopCloser(strings.NewReader(auth)), Timeout: 10 * time.Second})
			if entries, _ := os.ReadDir(runtimeDir); len(entries) != 0 {
				t.Fatalf("session survived: %v", entries)
			}
			if verdict == "" {
				if err == nil {
					t.Fatal("published an invalid review")
				}
				if _, statErr := os.Lstat(output); !os.IsNotExist(statErr) {
					t.Fatal("a failed review published output")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if report.Verdict != verdict || report.Provenance.CodexVersion != "codex-cli "+CodexVersion() {
				t.Fatalf("report %+v", report)
			}
		})
	}
}
