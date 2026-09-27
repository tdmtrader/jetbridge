//go:build linux

package implement

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/session"
)

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
	output := filepath.Join(t.TempDir(), "change")
	_, err = RunWorker(context.Background(), WorkerOptions{Input: s.Dir, Output: output, RuntimeDir: dir, Codex: "/bin/false", ToolsCommand: "/missing-tools", Model: "test", Auth: io.NopCloser(strings.NewReader(`{"auth_mode":"chatgpt","tokens":{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","id_token":"synthetic-id"}}`)), Timeout: time.Second})
	if err == nil {
		t.Fatal("failing provider succeeded")
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("credentials or workspace survived provider failure: %v %v", files, err)
	}
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		t.Fatal("a failed session published a change")
	}
}
