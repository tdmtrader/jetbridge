package runner

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/provider"
)

func TestRunDefaultsToImageOwnedClaudeInsteadOfWorkflowPATH(t *testing.T) {
	dir := t.TempDir()
	attackerBin := filepath.Join(dir, "attacker-bin")
	if err := os.MkdirAll(attackerBin, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeClaude := filepath.Join(attackerBin, "claude")
	if err := os.WriteFile(fakeClaude, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", attackerBin)
	resolvedFake, err := exec.LookPath("claude")
	if err != nil || resolvedFake != fakeClaude {
		t.Fatalf("test setup did not make workflow PATH resolve counterfeit Claude: path=%q err=%v", resolvedFake, err)
	}

	var selectedPath string
	oldFactory := newLegacyClaudeAdapter
	newLegacyClaudeAdapter = func(path string, _ []string, _ []string, _ io.Writer, _ time.Duration) provider.Adapter {
		selectedPath = path
		return &provider.FakeAdapter{
			IdentityValue: provider.Identity{Name: "path-test", Version: "v1"},
			StartFunc: func(context.Context, provider.StartRequest, provider.BoundaryControl) (provider.RunningSession, error) {
				return pathSelectionSession{}, nil
			},
		}
	}
	t.Cleanup(func() { newLegacyClaudeAdapter = oldFactory })

	exit, err := Run(context.Background(), Config{
		Prompt: "do it", FlightDir: filepath.Join(dir, "flight"), WorkDir: dir, StepName: "path-safe",
		Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil || exit != 0 {
		t.Fatalf("Run() = %d, %v", exit, err)
	}
	if selectedPath != "/usr/local/bin/claude" {
		t.Fatalf("default Claude executable = %q, want image-owned /usr/local/bin/claude", selectedPath)
	}
	if !filepath.IsAbs(selectedPath) || selectedPath == resolvedFake {
		t.Fatalf("default Claude executable must ignore workflow PATH: selected=%q counterfeit=%q", selectedPath, resolvedFake)
	}
}

type pathSelectionSession struct{}

func (pathSelectionSession) Wait(context.Context) (provider.Result, error) {
	return provider.Result{Stream: []byte(`{"type":"result","subtype":"success","result":"\"done\"","is_error":false}` + "\n")}, nil
}
