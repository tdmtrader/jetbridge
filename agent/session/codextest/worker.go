package codextest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

var worker string

// WorkerVariable names an installed jb-review-worker to use instead of
// building one: the worker image's own, when these tests run against it.
const WorkerVariable = "JB_WORKER_EXECUTABLE"

// Main runs a package's tests with cmd/jb-review-worker built once for all
// of them. The worker binary is what Codex starts as every workload's tool
// server (input-tools, workspace-tools), so a session under test serves its
// tools through the production executable.
func Main(m *testing.M) {
	if installed := os.Getenv(WorkerVariable); installed != "" {
		if !filepath.IsAbs(installed) {
			fmt.Fprintf(os.Stderr, "$%s must be an absolute path\n", WorkerVariable)
			os.Exit(1)
		}
		worker = installed
		os.Exit(m.Run())
	}
	dir, err := os.MkdirTemp("", "codextest-worker-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	worker = filepath.Join(dir, "jb-review-worker")
	if out, err := exec.Command("go", "build", "-o", worker, "github.com/concourse/concourse/cmd/jb-review-worker").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build jb-review-worker: %v: %s\n", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// Worker is the jb-review-worker executable Main built.
func Worker(t testing.TB) string {
	t.Helper()
	if worker == "" {
		t.Fatal("codextest.Worker needs the package's TestMain to call codextest.Main")
	}
	return worker
}
