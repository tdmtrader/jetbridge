package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// This is our engine-to-adapter boundary, not upstream's release matrix.
// The real daemon root must survive holding and disappear on release,
// independently of what the persisted events claim.
func TestEngineReleaseDrainsRealDaemon(t *testing.T) {
	// The engine runs the adapter from its temporary manifest directory, outside
	// the repository. Resolve the daemon's owning module and build there, using
	// its dependency set and the same prebuilt-binary path as CI.
	daemon := filepath.Join(t.TempDir(), "artifact-daemon")
	module, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/concourse/concourse").Output()
	if err != nil {
		t.Fatalf("locate daemon module: %v", err)
	}
	build := exec.Command("go", "build", "-mod=readonly", "-o", daemon, "./cmd/artifact-daemon")
	build.Dir = strings.TrimSpace(string(module))
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build engine test daemon: %v\n%s", err, out)
	}
	t.Setenv("BRINE_ARTIFACT_DAEMON_BINARY", daemon)
	dir, _ := documentForFeature(t, "run", "Feature: engine-held daemon\n  Scenario: held artifact\n    Given a real artifact daemon\n    And a step wrote \"held-by-engine\" into its output \"build-held/out/answer.txt\"\n    When the ATC asks it for the artifact \"build-held/out\"\n    Then the file at \"answer.txt\" reads \"held-by-engine\"\n")
	scratch := ownedResourceScratch(t)
	cli, err := exec.LookPath("brine")
	if err != nil {
		t.Fatal(err)
	}
	cli, err = filepath.Abs(cli)
	if err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "BRINE_HOME="+filepath.Join(dir, "machine"),
		"BRINE_ENGINE_MACHINE=0", "BRINE_ENGINE_PERSIST=1", "BRINE_BINARY="+cli,
		"BRINE_ENGINE_IDLE_TIMEOUT_SECS=0", "BRINE_ENGINE_GRACE_SECS=15", "TMPDIR="+scratch)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	engine := exec.CommandContext(ctx, "brine-engine", "--port", "0")
	engine.Dir, engine.Env = dir, env
	engine.Cancel = func() error { return engine.Process.Signal(syscall.SIGTERM) }
	engine.WaitDelay = 15 * time.Second
	log, err := os.Create(filepath.Join(dir, "engine.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	engine.Stderr = log
	stdout, err := engine.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- engine.Wait() }()
	t.Cleanup(func() {
		_ = engine.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("owned engine shutdown: %v", err)
			}
		case <-time.After(20 * time.Second):
			_ = engine.Process.Kill()
			<-done
			t.Error("owned engine did not shut down within 20s")
		}
	})
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatalf("engine did not announce its address: %v", scanner.Err())
	}
	address := strings.TrimPrefix(scanner.Text(), "brine-engine listening on ")
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" || port == "0" {
		t.Fatalf("unexpected engine address %q: %v", address, err)
	}
	env = append(env, "BRINE_ENGINE_PORT="+port)
	run := func(args ...string) ([]byte, string, int) {
		t.Helper()
		command := exec.CommandContext(ctx, cli, args...)
		command.Dir, command.Env = dir, env
		var out, stderr bytes.Buffer
		command.Stdout, command.Stderr = &out, &stderr
		err := command.Run()
		if ctx.Err() != nil {
			t.Fatalf("engine command timed out: %v", args)
		}
		if err == nil {
			return out.Bytes(), stderr.String(), 0
		}
		if exit, ok := err.(*exec.ExitError); ok {
			return out.Bytes(), stderr.String(), exit.ExitCode()
		}
		t.Fatalf("engine command %v: %v", args, err)
		return nil, "", -1
	}
	out, stderr, code := run("stage", dir, "--scenario", "held artifact", "--json")
	if code != 0 {
		t.Fatalf("stage: exit %d, stdout=%s stderr=%s", code, out, stderr)
	}
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	var held map[string]any
	if err := json.Unmarshal(lines[len(lines)-1], &held); err != nil {
		t.Fatal(err)
	}
	runID, _ := held["run_id"].(string)
	if held["status"] != "held" || runID == "" {
		t.Fatalf("stage did not hold: %s", out)
	}
	// The daemon's root lives under the adapter's own attributed temp root
	// (steps/temproot.go): <TMPDIR>/brine-adapter-daemon-<pid>-*/root-*.
	roots, err := filepath.Glob(filepath.Join(scratch, "brine-adapter-daemon-*", "root-*"))
	if err != nil || len(roots) != 1 {
		t.Fatalf("expected one held daemon root, got %v: %v", roots, err)
	}
	content, err := os.ReadFile(filepath.Join(roots[0], "steps", "build-held", "out", "answer.txt"))
	if err != nil || string(content) != "held-by-engine" {
		t.Fatalf("held resource was not preserved: %q %v", content, err)
	}
	partial := func() map[string]any {
		t.Helper()
		out, stderr, code := run("engine", "partial-run", runID, "--json")
		if code != 0 {
			t.Fatalf("partial run: %d %s %s", code, out, stderr)
		}
		var state map[string]any
		if err := json.Unmarshal(out, &state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	if state := partial(); state["complete"] != false || state["released"] != false {
		t.Fatalf("held run already ended: %v", state)
	}
	out, stderr, code = run("release", runID)
	if code != 0 {
		t.Fatalf("release: exit %d, stdout=%s stderr=%s", code, out, stderr)
	}
	if _, err := os.Stat(roots[0]); !os.IsNotExist(err) {
		t.Fatalf("release did not remove the real daemon root: %v", err)
	}
	if state := partial(); state["complete"] != true || state["released"] != true || state["release_refused"] != nil {
		t.Fatalf("release was not persisted: %v", state)
	}
	record, err := os.ReadFile(filepath.Join(dir, ".brine-runs", runID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	next := 0
	order := []string{"hold_ready", "recorder_drain_started", "recorder_drain_completed", "run_released"}
	for _, event := range decodeEvents(t, record) {
		kind, _ := event["type"].(string)
		if strings.HasPrefix(kind, "recorder_drain_") && next == 0 {
			t.Fatalf("recorder drained before holding: %s", record)
		}
		if kind == "recorder_drain_started" && event["disposer_count"] != float64(1) {
			t.Fatalf("held daemon disposer missing: %v", event)
		}
		if kind == "recorder_drain_completed" && (event["partial"] != false || event["disposers_drained"] != float64(1)) {
			t.Fatalf("held daemon drain incomplete: %v", event)
		}
		if kind == "run_release_refused" {
			t.Fatalf("record refused a clean release: %s", record)
		}
		if next < len(order) && kind == order[next] {
			next++
		}
	}
	if next != len(order) {
		t.Fatalf("missing ordered hold/drain/release sequence (%d/%d): %s", next, len(order), record)
	}
	out, stderr, code = run("release", runID)
	if code != 0 || !strings.Contains(string(out)+stderr, "already released") {
		t.Fatalf("second release: %d %s %s", code, out, stderr)
	}
	t.Logf("real daemon preserved during hold, removed on release; run %s", runID)
}
