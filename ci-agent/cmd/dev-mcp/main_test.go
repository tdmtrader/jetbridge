package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMainFailsFastOnBadProgressInterval (F13 SSE seam delta, 00 §3.1
// 2026-07-09; mirrored from 08 Task 13 / 10 Task 2): a
// DEV_MCP_PROGRESS_INTERVAL that is invalid, <= 0, or > 30s must be a
// FATAL startup error — never clamped silently to DefaultHeartbeat.
func TestMainFailsFastOnBadProgressInterval(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "dev-mcp")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v: %s", err, out)
	}
	smoke, err := filepath.Abs(filepath.Join("..", "..", "devmcp", "testdata", "smoke.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"bogus", "0s", "-5s", "45s"} {
		cmd := exec.Command(bin, "--config", smoke, "--workdir", t.TempDir())
		cmd.Env = append(os.Environ(), "DEV_MCP_PROGRESS_INTERVAL="+bad)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("DEV_MCP_PROGRESS_INTERVAL=%q: expected non-zero exit, got clean start", bad)
		}
		// the config is valid (Task 8 smoke.yml), so the fatal must be the
		// heartbeat's — assert the message names the env var, ruling out a
		// config-load failure masquerading as a pass.
		if !strings.Contains(string(out), "DEV_MCP_PROGRESS_INTERVAL") {
			t.Fatalf("DEV_MCP_PROGRESS_INTERVAL=%q: fatal exit, but not for the heartbeat: %s", bad, out)
		}
	}
}
