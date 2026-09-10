package steps

// The guard, guarded.
//
// A temp guard is infrastructure that DELETES, and the only thing standing
// between it and a neighbour's work is what it believes it can attribute. That
// belief is worth a spec of its own: the Phase 5 round that added the Go
// harnesses' guard shipped one that treated every `go-build*` directory
// appearing during the run as its own leak, removed a live compile's work
// directory and failed the package for it.
//
// These run the guard's real body over a FABRICATED temp directory. That is not
// a stand-in for the thing under test -- the body, the attribution and the
// removal are the production ones -- it is a stand-in for `$TMPDIR`, and it is
// the only way to assert the half that matters: what the guard leaves alone.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain sweeps after a `go test` run, for the same reason main does after an
// adapter run.
//
// The root is created at package INIT, so every binary that links this package
// has one -- including this test binary, which has no exitAfterSweep to call.
// Without this the steps suite left one root per run, and the only thing
// removing it was the stale-root sweep of whichever adapter or test process ran
// next. That safety net is real and it worked, but "somebody else will clean up
// after me eventually" is the rule this file exists to replace.
func TestMain(m *testing.M) {
	code := m.Run()

	for _, leak := range SweepAdapterDaemonRoots() {
		fmt.Fprintln(os.Stderr, "temp leak:", leak)
		if code == 0 {
			code = 1
		}
	}

	os.Exit(code)
}

func TestTheAdapterSweepLeavesADirectoryItCannotAttributeAlone(t *testing.T) {
	dir := t.TempDir()
	pid := os.Getpid()

	root := makeAdapterDir(t, dir, fmt.Sprintf("%s%d-root", daemonRootPrefix, pid))
	leaked := makeAdapterDir(t, dir, fmt.Sprintf("brine-hangar-output-daemon-%d-leaked", pid))
	writeAdapterFile(t, leaked, "hangar-output-daemon", 4096)

	// A LIVE sibling adapter, under a parallel brine run. Its pid is not ours,
	// and its bytes are its own.
	sibling := makeAdapterDir(t, dir, fmt.Sprintf("%s%d-live", daemonRootPrefix, pid+1))
	writeAdapterFile(t, sibling, "artifact-daemon", 128)

	// Directories this adapter never composed at all.
	foreign := makeAdapterDir(t, dir, "go-build-r3foreign-live2")
	stranger := makeAdapterDir(t, dir, fmt.Sprintf("brine-identity-%d-somebody", pid))

	leaks := sweepAdapterRootsIn(dir, root, pid)

	for _, survivor := range []string{sibling, foreign, stranger} {
		if _, err := os.Stat(survivor); err != nil {
			t.Errorf("the sweep removed %s, which is not this process's daemon root: %v",
				filepath.Base(survivor), err)
		}
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("the adapter's own daemon root survived the sweep: %v", err)
	}
	if _, err := os.Stat(leaked); !os.IsNotExist(err) {
		t.Errorf("a daemon root carrying this process's pid survived the sweep: %v", err)
	}
	if len(leaks) != 1 || !strings.Contains(leaks[0], filepath.Base(leaked)) {
		t.Fatalf("the sweep's report is %v; it owes exactly one sentence, about %s",
			leaks, filepath.Base(leaked))
	}
	if !strings.Contains(leaks[0], "(4096 bytes)") {
		t.Errorf("the leak was reported without its size: %s", leaks[0])
	}
}

// A root left behind by a process that is GONE.
//
// An adapter that panics, or is killed by the runner's SIGTERM drain timing
// out, takes no directory with it -- and no later run swept one, because the
// sweep at exit trusts only its own pid. Liveness is the attribution that
// closes it: a root whose owner is not running belongs to nobody, and a root
// whose owner IS running is a parallel run's live work.
func TestAStaleAdapterRootWhoseProcessIsGoneIsSwept(t *testing.T) {
	dir := t.TempDir()

	dead := exitedAdapterPID(t)
	if adapterProcessAlive(dead) {
		t.Fatalf("pid %d is still alive after being waited for", dead)
	}
	if !adapterProcessAlive(os.Getpid()) {
		t.Fatal("adapterProcessAlive says this very process is not running")
	}

	stale := makeAdapterDir(t, dir, fmt.Sprintf("%s%d-stale", daemonRootPrefix, dead))
	live := makeAdapterDir(t, dir, fmt.Sprintf("%s%d-live", daemonRootPrefix, os.Getpid()))
	unnamed := makeAdapterDir(t, dir, "brine-daemon-root-2751953891")

	swept := sweepStaleAdapterRoots(dir, os.Getpid())

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("a root whose owning process is gone survived: %v", err)
	}
	for _, survivor := range []string{live, unnamed} {
		if _, err := os.Stat(survivor); err != nil {
			t.Errorf("the sweep removed %s: %v", filepath.Base(survivor), err)
		}
	}
	if len(swept) != 1 || !strings.Contains(swept[0], filepath.Base(stale)) {
		t.Errorf("the sweep removed %v; it owes exactly %s", swept, filepath.Base(stale))
	}
}

// Every temp directory a daemon fixture makes goes under the adapter's root.
//
// The sweep can only remove what it can attribute, and it attributes by the pid
// in the name -- which it can only put there for the directories it creates
// itself. One `os.MkdirTemp("", ...)` added back to realdaemon.go is a
// directory nothing removes and nothing reports, which is exactly how 42 GB
// accumulated. The rule is read out of the source rather than remembered.
func TestEveryDaemonFixtureTempDirIsUnderTheAdapterRoot(t *testing.T) {
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, "realdaemon.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing realdaemon.go: %v", err)
	}

	found := 0
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "MkdirTemp" {
			return true
		}
		package_, ok := selector.X.(*ast.Ident)
		if !ok || package_.Name != "os" {
			return true
		}
		found++
		if len(call.Args) == 0 {
			return true
		}
		literal, ok := call.Args[0].(*ast.BasicLit)
		if ok && literal.Value == `""` {
			t.Errorf("steps/realdaemon.go:%d calls os.MkdirTemp with an empty directory, so it "+
				"puts a daemon's bytes straight into the user's temp directory under a name "+
				"nothing can attribute and nothing sweeps. Call daemonTempDir instead.",
				fileSet.Position(call.Pos()).Line)
		}

		return true
	})

	if found != 0 {
		t.Errorf("realdaemon.go calls os.MkdirTemp %d time(s) directly; every daemon fixture "+
			"directory goes through daemonTempDir, which is what puts this process's pid in the "+
			"name", found)
	}
}

func makeAdapterDir(t *testing.T, parent, name string) string {
	t.Helper()

	path := filepath.Join(parent, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("making %s: %v", name, err)
	}

	return path
}

func writeAdapterFile(t *testing.T, dir, name string, size int) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

// exitedAdapterPID is a real pid that ran and exited, rather than a number
// picked for being large.
func exitedAdapterPID(t *testing.T) int {
	t.Helper()

	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatalf("running a process to exit: %v", err)
	}

	return cmd.Process.Pid
}
