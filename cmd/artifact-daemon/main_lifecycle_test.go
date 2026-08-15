package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	artifactDaemonHelperEnv     = "ARTIFACT_DAEMON_TEST_HELPER"
	artifactDaemonHelperArgsEnv = "ARTIFACT_DAEMON_TEST_ARGS"
)

// TestArtifactDaemonLifecycle exercises the process boundary that Kubernetes
// actually runs: flag parsing, component wiring, the HTTP listener, background
// maintenance, signal handling, and graceful shutdown. The child is this test
// executable, so the test adds no go-build subprocess and an instrumented test
// binary keeps writing to the same Go coverage directory.
func TestArtifactDaemonLifecycle(t *testing.T) {
	if os.Getenv(artifactDaemonHelperEnv) == "1" {
		runArtifactDaemonTestProcess(t)
		return
	}

	storagePath := t.TempDir()
	durablePath := t.TempDir()
	expiredObject := filepath.Join(durablePath, "resource-caches", "expired")
	if err := os.MkdirAll(filepath.Dir(expiredObject), 0o755); err != nil {
		t.Fatalf("create durable retention class: %v", err)
	}
	if err := os.WriteFile(expiredObject, []byte("expired cache"), 0o644); err != nil {
		t.Fatalf("seed durable store: %v", err)
	}
	expiredAt := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(expiredObject, expiredAt, expiredAt); err != nil {
		t.Fatalf("backdate durable object: %v", err)
	}

	port := availableTCPPort(t)
	args := []string{
		"--port=" + strconv.Itoa(port),
		"--storage-path=" + storagePath,
		"--ttl=1m",
		"--durable-store=filesystem",
		"--durable-path=" + durablePath,
		"--durable-timeout=1s",
		"--durable-maintenance-interval=2ms",
		"--durable-retention=resource-caches=1h",
		"--preemption-watch",
	}
	encodedArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("encode daemon arguments: %v", err)
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test executable: %v", err)
	}
	childArgs := []string{"-test.run=^TestArtifactDaemonLifecycle$"}
	if coverDir := currentTestCoverDir(); coverDir != "" {
		childArgs = append(childArgs, "-test.gocoverdir="+coverDir)
	}
	cmd := exec.Command(executable, childArgs...)
	cmd.Env = append(os.Environ(),
		artifactDaemonHelperEnv+"=1",
		artifactDaemonHelperArgsEnv+"="+string(encodedArgs),
	)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start artifact daemon: %v", err)
	}
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitForDaemonHTTP(t, baseURL+"/healthz", http.StatusOK, 3*time.Second)
	waitForPathAbsent(t, expiredObject, 3*time.Second)

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("interrupt artifact daemon: %v", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case err := <-waited:
		stopped = true
		if err != nil {
			t.Fatalf("artifact daemon did not stop cleanly: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-waited
		stopped = true
		t.Fatalf("artifact daemon did not stop within 5s\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}

	logs := stdout.String()
	for _, event := range []string{
		`"message":"artifact-daemon.durable-store-enabled"`,
		`"message":"artifact-daemon.durable-retention"`,
		`"message":"artifact-daemon.preemption-watch-disabled"`,
		`"message":"artifact-daemon.starting"`,
		`"message":"artifact-daemon.shutting-down"`,
		`"message":"artifact-daemon.stopped"`,
	} {
		if !strings.Contains(logs, event) {
			t.Errorf("daemon log did not contain %s\nstdout:\n%s", event, logs)
		}
	}
}

func runArtifactDaemonTestProcess(t *testing.T) {
	t.Helper()

	var args []string
	if err := json.Unmarshal([]byte(os.Getenv(artifactDaemonHelperArgsEnv)), &args); err != nil {
		t.Fatalf("decode daemon arguments: %v", err)
	}
	flag.CommandLine = flag.NewFlagSet("artifact-daemon", flag.ExitOnError)
	os.Args = append([]string{"artifact-daemon"}, args...)
	main()
}

func currentTestCoverDir() string {
	if f := flag.Lookup("test.gocoverdir"); f != nil {
		return f.Value.String()
	}
	return ""
}

func availableTCPPort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate daemon port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release daemon port: %v", err)
	}
	return port
}

func waitForDaemonHTTP(t *testing.T, url string, wantStatus int, timeout time.Duration) {
	t.Helper()

	client := &http.Client{Timeout: 100 * time.Millisecond}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == wantStatus {
				return
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s did not return status %d within %s: %v", url, wantStatus, timeout, lastErr)
}

func waitForPathAbsent(t *testing.T, path string, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s still exists after %s", path, timeout)
}
