//go:build linux

package review

import (
	"context"
	"github.com/concourse/concourse/agent/session"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLinuxMemoryCredentialCleanup(t *testing.T) {
	if err := session.RequireMemoryRuntime("/dev/shm"); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("/dev/shm", "review-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	b := captureTest(t)
	_, err = RunWorker(context.Background(), WorkerOptions{Input: b.Dir, Output: filepath.Join(t.TempDir(), "report"), RuntimeDir: dir, Codex: "/bin/false", ReaderCommand: "/missing-reader", Model: "test", Auth: io.NopCloser(strings.NewReader(`{"auth_mode":"chatgpt","tokens":{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","id_token":"synthetic-id"}}`)), Timeout: time.Second})
	if err == nil {
		t.Fatal("failing provider succeeded")
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("credentials survived provider failure: %v %v", files, err)
	}
}
