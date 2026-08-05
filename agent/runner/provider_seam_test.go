package runner_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/agent/provider"
	"github.com/concourse/concourse/agent/runner"
)

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
