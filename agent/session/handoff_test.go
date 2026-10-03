package session_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/session"
	"github.com/concourse/concourse/agent/session/codextest"
)

// The sender refuses before it dials: it needs a deadline, an absolute
// socket, a positive Run ID and a subscription credential, and it closes the
// credential stream whatever happens.
func TestSendCredentialHandoffRefusesAnIncompleteHandoff(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "auth.sock")
	bounded := func() context.Context {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		t.Cleanup(cancel)
		return ctx
	}
	for name, tc := range map[string]struct {
		ctx    context.Context
		path   string
		runID  int
		auth   string
		reason string
	}{
		"no deadline":     {context.Background(), socket, 1, string(codextest.Auth()), "credential handoff requires a deadline"},
		"relative socket": {bounded(), "auth.sock", 1, string(codextest.Auth()), "positive Run ID and absolute credential socket are required"},
		"no Run ID":       {bounded(), socket, 0, string(codextest.Auth()), "positive Run ID and absolute credential socket are required"},
		"API key":         {bounded(), socket, 1, `{"OPENAI_API_KEY":"k"}`, "valid Codex subscription auth.json required"},
	} {
		t.Run(name, func(t *testing.T) {
			auth := &closeRecorder{Reader: strings.NewReader(tc.auth)}
			if err := session.SendCredentialHandoff(tc.ctx, tc.path, tc.runID, auth); err == nil || err.Error() != tc.reason {
				t.Fatalf("got %v, want %q", err, tc.reason)
			}
			if !auth.closed {
				t.Fatal("the credential stream was left open")
			}
		})
	}
	if err := session.SendCredentialHandoff(context.Background(), socket, 1, nil); err == nil {
		t.Fatal("sent without a credential")
	}
}

// A worker that never opens its socket is waited for only until the
// deadline, and the sender says it timed out.
func TestSendCredentialHandoffTimesOutWithoutAWorker(t *testing.T) {
	// A short directory: a socket path is limited to about a hundred bytes.
	dir, err := os.MkdirTemp("/tmp", "handoff-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err = session.SendCredentialHandoff(ctx, filepath.Join(dir, "auth.sock"), 1, io.NopCloser(strings.NewReader(string(codextest.Auth()))))
	if !errors.Is(err, session.ErrCredentialHandoffTimeout) {
		t.Fatalf("got %v", err)
	}
}

func TestListenCredentialHandoffRefusesAnIncompleteRequest(t *testing.T) {
	dir := t.TempDir()
	for name, tc := range map[string]struct {
		runID   int
		timeout time.Duration
	}{"no Run ID": {0, time.Second}, "no timeout": {1, 0}} {
		if _, err := session.ListenCredentialHandoff(context.Background(), dir, filepath.Join(dir, "auth.sock"), tc.runID, tc.timeout); err == nil {
			t.Errorf("%s: listened", name)
		}
	}
}

// closeRecorder is a credential stream that records being closed.
type closeRecorder struct {
	io.Reader
	closed bool
}

func (c *closeRecorder) Close() error { c.closed = true; return nil }
