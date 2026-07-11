// Package e2e builds the real ci-agent/cmd/dev-mcp binary and proves the
// whole stack: binary + config against the contract-test kit, and the Go
// client path (the exact call path harvest-step will use).
//
// NOTE: this package has no Ginkgo suite, so `ginkgo -r` (make test-unit)
// skips it — run it via `make test-dev-mcp` or `go test ./agent/devmcp/...`.
package e2e_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/devmcp"
	"github.com/concourse/concourse/agent/devmcp/contracttest"
)

var devMCPBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "devmcp-e2e")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	devMCPBin = filepath.Join(dir, "dev-mcp")
	build := exec.Command("go", "build", "-o", devMCPBin, "./cmd/dev-mcp")
	build.Dir = filepath.Join(mustRepoRoot(), "ci-agent")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building dev-mcp: %s\n%s", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func mustRepoRoot() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		panic("runtime.Caller failed")
	}
	// agent/devmcp/e2e/e2e_test.go → repo root is four dirs up from the file
	return filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(thisFile))))
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// copyFixture copies the fixture repo into a temp dir so command logs
// (.dev-mcp/logs) never pollute testdata.
func copyFixture(t *testing.T) string {
	t.Helper()
	src := filepath.Join(mustRepoRoot(), "agent", "devmcp", "contracttest", "testdata", "fixture-repo")
	dst := t.TempDir()
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	return dst
}

func startDevMCP(t *testing.T, workdir, config, heartbeat string) string {
	t.Helper()
	addr := fmt.Sprintf("127.0.0.1:%d", freePort(t))

	cmd := exec.Command(devMCPBin, "--config", config, "--workdir", workdir)
	cmd.Env = append(os.Environ(),
		"MCP_LISTEN_ADDR="+addr,
		"DEV_MCP_PROGRESS_INTERVAL="+heartbeat,
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
	})

	healthz := fmt.Sprintf("http://%s/healthz", addr)
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get(healthz)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("dev-mcp at %s never became healthy: %v", addr, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Sprintf("http://%s/mcp", addr)
}

func TestFixtureRepoContract(t *testing.T) {
	fixture := copyFixture(t)
	endpoint := startDevMCP(t, fixture, filepath.Join(fixture, "dev-mcp.yml"), "200ms")

	contracttest.RunWithOptions(t, endpoint, contracttest.Options{
		ExerciseComponent:    "app",
		FailingLintComponent: "app",
		SlowTestComponent:    "app",
		AffectedPath:         "src/app.sh",
		ExpectAffected:       []string{"app"},
		Timeout:              time.Minute,
	})
}

func TestConcourseRepoContract(t *testing.T) {
	root := mustRepoRoot()
	endpoint := startDevMCP(t, root, filepath.Join(root, "dev-mcp.yml"), "15s")

	// Universal checks only: exercising this repo's build/test takes minutes
	// and belongs to the theborg CI job (TestLiveImageContract below).
	contracttest.Run(t, endpoint)
}

// TestGoClientRunTestsEndToEnd drives run_tests(component) through the Go
// client with progress — the exact call path harvest-step will use.
func TestGoClientRunTestsEndToEnd(t *testing.T) {
	fixture := copyFixture(t)
	endpoint := startDevMCP(t, fixture, filepath.Join(fixture, "dev-mcp.yml"), "200ms")

	var mu sync.Mutex
	var progress []string
	client := devmcp.NewClient(endpoint, devmcp.WithProgress(func(tool, msg string) {
		mu.Lock()
		progress = append(progress, tool+": "+msg)
		mu.Unlock()
	}))

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	res, err := client.RunTests(ctx, "app", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != devmcp.StatusOK {
		t.Fatalf("status %q, want ok (summary %q)", res.Status, res.Summary)
	}
	if res.DurationSeconds <= 0 {
		t.Fatalf("duration_seconds = %v, want > 0", res.DurationSeconds)
	}
	if res.LogPath == "" {
		t.Fatal("log_path is empty")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(progress) == 0 {
		t.Fatal("no progress notifications observed")
	}
}

// TestLiveImageContract exercises a running mcp-dev-concourse container.
// It is driven by the build-mcp-dev-image CI job (deploy/concourse-pipeline.yml)
// and skipped everywhere else.
func TestLiveImageContract(t *testing.T) {
	endpoint := os.Getenv("DEV_MCP_ENDPOINT")
	if endpoint == "" {
		t.Skip("DEV_MCP_ENDPOINT not set; this test runs in the build-mcp-dev-image CI job")
	}
	contracttest.RunWithOptions(t, endpoint, contracttest.Options{
		ExerciseComponent: "ci-agent",
		AffectedPath:      "atc/api/handler.go",
		ExpectAffected:    []string{"atc"},
		Timeout:           20 * time.Minute,
	})
}
