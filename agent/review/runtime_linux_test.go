//go:build linux

package review

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/session"
	"github.com/concourse/concourse/agent/session/codextest"
)

// The worker as deployed: its session on a tmpfs runtime, the pinned Codex,
// a published report on success, and nothing of the credential left on the
// runtime whichever way the session ends.
func TestLinuxMemoryCredentialCleanup(t *testing.T) {
	if err := session.RequireMemoryRuntime("/dev/shm"); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		turns     []codextest.Turn
		published bool
	}{
		"published":       {codextest.Steps(codextest.Say(assessment(t, true))), true},
		"refused request": {codextest.Steps(codextest.Fail("usage_not_included", "The plan does not include this model.")), false},
	} {
		t.Run(name, func(t *testing.T) {
			dir, err := os.MkdirTemp("/dev/shm", "review-test-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			b := captureTest(t)
			m := codextest.NewModel(t, tc.turns...)
			output := filepath.Join(t.TempDir(), "report")
			_, err = RunWorker(context.Background(), WorkerOptions{Input: b.Dir, Output: output, RuntimeDir: dir, Codex: codextest.Launcher(t, m),
				ReaderCommand: codextest.Worker(t), Model: model, Auth: io.NopCloser(bytes.NewReader(codextest.Auth())), Timeout: time.Minute})
			if (err == nil) != tc.published {
				t.Fatalf("published=%v: %v", tc.published, err)
			}
			if len(m.Requests()) == 0 {
				t.Fatalf("the session ended before it reached the model: %v", err)
			}
			if files, err := os.ReadDir(dir); err != nil || len(files) != 0 {
				t.Fatalf("the credential survived the session: %v %v", files, err)
			}
			if _, err := os.Lstat(output); os.IsNotExist(err) == tc.published {
				t.Fatalf("published=%v, output: %v", tc.published, err)
			}
		})
	}
}
