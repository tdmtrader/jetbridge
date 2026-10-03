//go:build linux

package session_test

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/session"
	"github.com/concourse/concourse/agent/session/codextest"
)

// memoryRuntime is a private runtime directory on /dev/shm, the tmpfs a
// credential handoff requires.
func memoryRuntime(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/dev/shm", "session-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

type handoff struct {
	runtime, socket string
	auth            io.ReadCloser
	sent            chan error
}

// listen opens the worker's side and starts the sender with credential,
// both for real over the private socket.
func listen(t *testing.T, workerRun, senderRun int, credential []byte, timeout time.Duration) *handoff {
	t.Helper()
	h := &handoff{runtime: memoryRuntime(t), sent: make(chan error, 1)}
	h.socket = filepath.Join(h.runtime, "auth.sock")
	auth, err := session.ListenCredentialHandoff(context.Background(), h.runtime, h.socket, workerRun, timeout)
	if err != nil {
		t.Fatal(err)
	}
	h.auth = auth
	t.Cleanup(func() { auth.Close() })
	if credential != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			h.sent <- session.SendCredentialHandoff(ctx, h.socket, senderRun, io.NopCloser(strings.NewReader(string(credential))))
		}()
	}
	return h
}

func (h *handoff) open(t *testing.T, codex string) (*session.Session, error) {
	t.Helper()
	s, err := session.Open(context.Background(), session.Options{RuntimeDir: h.runtime, Provider: session.Codex{}, Executable: codex, Auth: h.auth})
	if err == nil {
		t.Cleanup(func() { s.Close() })
	}
	return s, err
}

// The sender is acknowledged only once the credential is staged and the
// pinned Codex has answered for itself; the socket takes no second sender.
func TestHandoffAcknowledgesAStagedCredential(t *testing.T) {
	r := newRun(t)
	h := listen(t, 7, 7, codextest.Auth(), 20*time.Second)
	s, err := h.open(t, r.codex)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-h.sent; err != nil {
		t.Fatalf("the sender was not acknowledged: %v", err)
	}
	if staged, _ := os.ReadFile(filepath.Join(s.Home, "auth.json")); string(staged) != string(codextest.Auth()) {
		t.Fatal("the handed-off credential was not staged")
	}
	if conn, err := net.Dial("unix", h.socket); err == nil {
		conn.Close()
		t.Fatal("the socket accepted a second sender")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	empty(t, h.runtime)
}

// A handoff that fails is never acknowledged, and nothing of the session
// remains.
func TestHandoffIsNotAcknowledgedWhenTheSessionIsRefused(t *testing.T) {
	r := newRun(t)
	for name, tc := range map[string]struct {
		senderRun int
		codex     string
		reason    string
	}{
		"another Run":     {senderRun: 8, codex: r.codex, reason: "valid Codex subscription auth.json required"},
		"another release": {senderRun: 7, codex: "/bin/echo", reason: "worker requires codex-cli"},
	} {
		t.Run(name, func(t *testing.T) {
			h := listen(t, 7, tc.senderRun, codextest.Auth(), 20*time.Second)
			if _, err := h.open(t, tc.codex); err == nil || !strings.HasPrefix(err.Error(), tc.reason) {
				t.Fatalf("got %v, want %q", err, tc.reason)
			}
			h.auth.Close()
			if err := <-h.sent; err == nil {
				t.Fatal("a refused session acknowledged the sender")
			}
			empty(t, h.runtime)
		})
	}
}

// A worker whose sender never arrives gives up at the handoff timeout.
func TestHandoffTimesOutWithoutASender(t *testing.T) {
	r := newRun(t)
	h := listen(t, 7, 7, nil, 300*time.Millisecond)
	if _, err := h.open(t, r.codex); !errors.Is(err, session.ErrCredentialHandoffTimeout) {
		t.Fatalf("got %v", err)
	}
	empty(t, h.runtime)
}

// The socket lives directly in a private tmpfs runtime and nowhere else.
func TestListenCredentialHandoffRequiresAPrivateMemoryRuntime(t *testing.T) {
	runtime := memoryRuntime(t)
	nested := filepath.Join(runtime, "nested")
	os.Mkdir(nested, 0o700)
	open := memoryRuntime(t)
	os.Chmod(open, 0o755)
	for name, tc := range map[string]struct{ runtime, socket string }{
		"not tmpfs":        {t.TempDir(), filepath.Join(t.TempDir(), "auth.sock")},
		"socket elsewhere": {runtime, filepath.Join(nested, "auth.sock")},
		"relative socket":  {runtime, "auth.sock"},
		"shared runtime":   {open, filepath.Join(open, "auth.sock")},
	} {
		if _, err := session.ListenCredentialHandoff(context.Background(), tc.runtime, tc.socket, 1, time.Second); err == nil {
			t.Errorf("%s: listened", name)
		}
	}
}
