package runner_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/provider"
	"github.com/concourse/concourse/agent/runner"
)

func TestRunPassesBoundaryControlOnlyToSafeAdapter(t *testing.T) {
	for _, safe := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsafe", true: "safe"}[safe], func(t *testing.T) {
			var got provider.BoundaryControl
			adapter := &provider.FakeAdapter{
				IdentityValue:     provider.Identity{Name: "test", Version: "v1"},
				CapabilitiesValue: provider.Capabilities{SafeBoundary: safe},
				StartFunc: func(_ context.Context, _ provider.StartRequest, control provider.BoundaryControl) (provider.RunningSession, error) {
					got = control
					return providerSessionFunc(func(context.Context) (provider.Result, error) {
						return provider.Result{Stream: []byte(`{"type":"result","subtype":"success","result":"\"done\"","is_error":false}` + "\n")}, nil
					}), nil
				},
			}
			dir := t.TempDir()
			exit, err := runner.Run(context.Background(), runner.Config{Prompt: "p", FlightDir: filepath.Join(dir, "flight"), WorkDir: dir, Adapter: adapter, Stdout: io.Discard, Stderr: io.Discard, BoundaryStop: func() error { t.Fatal("unexpected stop"); return nil }})
			if err != nil || exit != 0 {
				t.Fatalf("Run() = %d, %v", exit, err)
			}
			if (got != nil) != safe {
				t.Fatalf("control present = %t, safe = %t", got != nil, safe)
			}
		})
	}
}

func TestRunValidatesReservedSessionDirectoryWithoutRedirectingClaudeConfig(t *testing.T) {
	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	envPath := filepath.Join(dir, "env")
	claude := filepath.Join(dir, "claude")
	if err := os.WriteFile(claude, []byte("#!/bin/sh\nenv > '"+envPath+"'\necho '"+`{"type":"result","subtype":"success","result":"\"done\"","is_error":false}`+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(dir, ".concourse", "session")
	t.Setenv("HOME", "/home/unchanged")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	exit, err := runner.Run(context.Background(), runner.Config{Prompt: "p", FlightDir: flight, WorkDir: dir, SessionDir: configDir, ClaudePath: claude, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || exit != 0 {
		t.Fatalf("Run() = %d, %v", exit, err)
	}
	env, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(env, []byte("CLAUDE_CONFIG_DIR="+configDir)) || !bytes.Contains(env, []byte("HOME=/home/unchanged")) {
		t.Fatalf("legacy Claude env unexpectedly redirected: %s", env)
	}
	if _, err := runner.Run(context.Background(), runner.Config{Prompt: "p", FlightDir: flight, WorkDir: dir, SessionDir: filepath.Join(dir, "other"), ClaudePath: claude}); err == nil {
		t.Fatal("accepted non-reserved session directory")
	}
}

func TestRunUsesTerminalBoundaryForUnsafeProviderCompletion(t *testing.T) {
	var mu sync.Mutex
	stops := 0
	adapter := &provider.FakeAdapter{
		IdentityValue: provider.Identity{Name: "unsafe", Version: "v1"},
		StartFunc: func(_ context.Context, _ provider.StartRequest, control provider.BoundaryControl) (provider.RunningSession, error) {
			if control != nil {
				t.Fatal("unsafe adapter received boundary control")
			}
			if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
				t.Fatal(err)
			}
			return providerSessionFunc(func(context.Context) (provider.Result, error) {
				time.Sleep(20 * time.Millisecond)
				return provider.Result{Stream: []byte(`{"type":"system","subtype":"init","session_id":"terminal-session"}` + "\n" + `{"type":"result","subtype":"success","result":"\"done\"","is_error":false}` + "\n")}, nil
			}), nil
		},
	}
	dir := t.TempDir()
	exit, err := runner.Run(context.Background(), runner.Config{Prompt: "p", FlightDir: filepath.Join(dir, "flight"), WorkDir: dir, Adapter: adapter, Stdout: io.Discard, Stderr: io.Discard, BoundaryStop: func() error { mu.Lock(); stops++; mu.Unlock(); return nil }})
	if err != nil || exit != 0 {
		t.Fatalf("Run() = %d, %v", exit, err)
	}
	mu.Lock()
	got := stops
	mu.Unlock()
	if got != 1 {
		t.Fatalf("terminal boundary stop count = %d", got)
	}
}

func TestFromEnvReadsReservedSessionDirectory(t *testing.T) {
	workdir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(workdir, ".concourse", "session")
	t.Setenv("AGENT_SESSION_DIR", want)
	if got := runner.FromEnv().SessionDir; got != want {
		t.Fatalf("SessionDir = %q, want %q", got, want)
	}
}

type providerSessionFunc func(context.Context) (provider.Result, error)

func (fn providerSessionFunc) Wait(ctx context.Context) (provider.Result, error) { return fn(ctx) }
