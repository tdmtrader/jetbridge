//go:build linux

package implement

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

// The worker as deployed: its session on a tmpfs runtime and the pinned
// Codex. A session the backend refuses leaves nothing of the credential or
// the workspace on the runtime, and publishes nothing.
func TestLinuxMemoryCredentialCleanup(t *testing.T) {
	if err := session.RequireMemoryRuntime("/dev/shm"); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("/dev/shm", "implement-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	_, s := snapshotFixture(t)
	m := codextest.NewModel(t, codextest.Steps(codextest.Fail("usage_not_included", "The plan does not include this model."))...)
	output := filepath.Join(t.TempDir(), "change")
	_, err = RunWorker(context.Background(), WorkerOptions{Input: s.Dir, Output: output, RuntimeDir: dir, Codex: codextest.Launcher(t, m),
		ToolsCommand: codextest.Worker(t), Model: model, Auth: io.NopCloser(bytes.NewReader(codextest.Auth())), Timeout: time.Minute})
	if err == nil {
		t.Fatal("a refused session succeeded")
	}
	// The refusal came from the backend, not from a session that never
	// started Codex.
	if len(m.Requests()) == 0 {
		t.Fatalf("the session failed before it reached the model: %v", err)
	}
	if files, err := os.ReadDir(dir); err != nil || len(files) != 0 {
		t.Fatalf("the credential or workspace survived the session: %v %v", files, err)
	}
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		t.Fatal("a failed session published a change")
	}
}
