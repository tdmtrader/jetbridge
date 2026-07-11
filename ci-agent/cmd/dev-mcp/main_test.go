package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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

// TestMainDrainsInFlightRequestsOnSIGTERM: SIGTERM must gracefully drain —
// main must not exit until http.Server.Shutdown returns, so an in-flight
// tools/call completes (and an SSE stream gets its final frame) instead of
// dying with the connection.
func TestMainDrainsInFlightRequestsOnSIGTERM(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "dev-mcp")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v: %s", err, out)
	}

	workdir := t.TempDir()
	// the build command drops a sentinel so the test knows the call is
	// in flight, then holds the request open long enough to race SIGTERM.
	config := filepath.Join(workdir, "dev-mcp.yml")
	if err := os.WriteFile(config, []byte(`schema_version: 1
components:
  - id: slow
    description: slow build for drain testing
    paths: ["slow/"]
    kind: other
    build: { cmd: ["sh", "-c", "touch in-flight; sleep 1"] }
`), 0o644); err != nil {
		t.Fatal(err)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	lis.Close()

	cmd := exec.Command(bin, "--config", config, "--workdir", workdir)
	cmd.Env = append(os.Environ(), "MCP_LISTEN_ADDR="+addr)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()

	waitFor(t, "healthz", func() bool {
		resp, err := http.Get(fmt.Sprintf("http://%s/healthz", addr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})

	type callResult struct {
		body []byte
		err  error
	}
	callDone := make(chan callResult, 1)
	go func() {
		resp, err := http.Post(fmt.Sprintf("http://%s/mcp", addr), "application/json",
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"build","arguments":{"component":"slow"}}}`))
		if err != nil {
			callDone <- callResult{err: err}
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		callDone <- callResult{body: body, err: err}
	}()

	waitFor(t, "tool call in flight", func() bool {
		_, err := os.Stat(filepath.Join(workdir, "in-flight"))
		return err == nil
	})

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	select {
	case res := <-callDone:
		if res.err != nil {
			t.Fatalf("in-flight tools/call did not survive SIGTERM: %v", res.err)
		}
		// the tool payload is JSON-escaped inside the text content block
		if !strings.Contains(string(res.body), `\"status\":\"ok\"`) {
			t.Fatalf("in-flight tools/call got a non-ok result after SIGTERM: %s", res.body)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("in-flight tools/call never completed after SIGTERM")
	}

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("process exited non-zero after drain: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("process never exited after the drain completed")
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
