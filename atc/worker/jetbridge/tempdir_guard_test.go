package jetbridge

// The guard, guarded. The same two questions as atc/hangaroutput's, asked of
// this package's copy, because a guard that DELETES is infrastructure and the
// only thing between it and a neighbour's work is what it believes it can
// attribute -- and here there is a second neighbour, this suite's own sibling
// `--procs` processes.
//
// These run the guard's real body over a FABRICATED temp directory: the body,
// the attribution and the removal are the production ones, and only `$TMPDIR`
// is stood in for. Proving what the guard leaves alone against the real one
// would mean leaving a foreign directory in the user's temp directory after the
// run to show it survived, which is the leak this file exists to stop.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A `go build` belonging to somebody else, created while this package runs.
//
// CI's unit tier runs the packages in parallel and two siblings shell out to
// `go build` from inside their own tests, so a `go-build*` work directory
// appearing in `$TMPDIR` halfway through this package's run is ordinary. It
// carries no pid of ours. The guard may not remove it, and may not fail this
// package for it: a compile losing its work directory mid-flight is the
// `fork/exec ...: no such file or directory` failure this tree already warns
// about, and it would be reported as a leak of ours.
func TestTheTempGuardLeavesADirectoryItCannotAttributeAlone(t *testing.T) {
	dir := t.TempDir()
	pid := os.Getpid()

	root := makeTempDir(t, dir, fmt.Sprintf("%s%d-root", tempRootPrefix, pid))
	before := tempSuspectsIn(dir, pid)

	// Everything below appears DURING the run, which is what makes it a
	// candidate at all: only the first is ours.
	leaked := makeTempDir(t, dir, fmt.Sprintf("%s%d-leaked", tempRootPrefix, pid))
	writeTempFile(t, leaked, "hangar-output-daemon", 4096)

	foreign := makeTempDir(t, dir, "go-build-r3foreign-live2")
	writeTempFile(t, foreign, "b001-compile.o", 128)

	// A sibling Ginkgo process of this very suite, still running.
	sibling := makeTempDir(t, dir, fmt.Sprintf("%s999999-live", tempRootPrefix))
	stranger := makeTempDir(t, dir, "TestSomebodyElse2751953891")

	leaks := tempLeaksIn(dir, root, before, pid)

	for _, survivor := range []string{foreign, sibling, stranger} {
		if _, err := os.Stat(survivor); err != nil {
			t.Errorf("the guard removed %s, which carries no pid of this process: %v",
				filepath.Base(survivor), err)
		}
	}
	if _, err := os.Stat(filepath.Join(foreign, "b001-compile.o")); err != nil {
		t.Errorf("the guard emptied a foreign `go build` work directory: %v", err)
	}

	// And it did not fail the package for any of them.
	for _, leak := range leaks {
		for _, name := range []string{"go-build-r3foreign-live2", "999999", "TestSomebodyElse"} {
			if strings.Contains(leak, name) {
				t.Errorf("the guard reported an unattributable directory as this package's "+
					"leak: %s", leak)
			}
		}
	}

	// What it does do, unchanged: its own root goes, and its own leavings are
	// removed AND reported, with their size, because a report without the bytes
	// gone is the leak all over again.
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("the package temp root survived the guard: %v", err)
	}
	if _, err := os.Stat(leaked); !os.IsNotExist(err) {
		t.Errorf("a directory carrying this process's pid survived the guard: %v", err)
	}
	if len(leaks) != 1 || !strings.Contains(leaks[0], filepath.Base(leaked)) {
		t.Fatalf("the guard's report is %v; it owes exactly one sentence, about %s",
			leaks, filepath.Base(leaked))
	}
	if !strings.Contains(leaks[0], "(4096 bytes)") {
		t.Errorf("the leak was reported without its size: %s", leaks[0])
	}
}

// A root left behind by a process that is GONE.
//
// A suite that panics, or is killed, or fails in TestMain before the run
// starts, takes no directory with it -- and no later run swept one, because the
// guard trusts only its own pid. Liveness is the attribution that closes it:
// a root whose owner is not running belongs to nobody and is removed at init,
// and a root whose owner IS running is a sibling `--procs` process's live work
// and is not touched by anybody.
//
// The dead pid here is a real one: a process that ran and exited, not a number
// picked for being large.
func TestAStaleRootWhoseProcessIsGoneIsSwept(t *testing.T) {
	dir := t.TempDir()

	dead := exitedProcessPID(t)
	if processAlive(dead) {
		t.Fatalf("pid %d is still alive after being waited for; the sweep cannot be asserted "+
			"against it", dead)
	}
	if !processAlive(os.Getpid()) {
		t.Fatal("processAlive says this very process is not running")
	}

	stale := makeTempDir(t, dir, fmt.Sprintf("%s%d-stale", tempRootPrefix, dead))
	writeTempFile(t, stale, "hangar-output-daemon", 64)
	live := makeTempDir(t, dir, fmt.Sprintf("%s%d-live", tempRootPrefix, os.Getpid()))
	foreign := makeTempDir(t, dir, "go-build-r3foreign-live2")
	other := makeTempDir(t, dir, fmt.Sprintf("some-other-suite-%d-live", dead))

	swept := sweepStaleRoots(dir, os.Getpid())

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("the root of a process that has exited survived the sweep: %v", err)
	}
	if len(swept) != 1 || !strings.Contains(swept[0], filepath.Base(stale)) {
		t.Errorf("the sweep removed %v; it owes exactly %s", swept, filepath.Base(stale))
	}
	for _, survivor := range []string{live, foreign, other} {
		if _, err := os.Stat(survivor); err != nil {
			t.Errorf("the sweep removed %s, which belongs to a live process or to another "+
				"package entirely: %v", filepath.Base(survivor), err)
		}
	}
}

// exitedProcessPID runs a process, waits for it, and returns its pid.
func exitedProcessPID(t *testing.T) int {
	t.Helper()

	command := exec.Command("/bin/sh", "-c", "exit 0")
	if err := command.Run(); err != nil {
		t.Fatalf("running a process to exit: %v", err)
	}

	return command.ProcessState.Pid()
}

func makeTempDir(t *testing.T, parent, name string) string {
	t.Helper()

	path := filepath.Join(parent, name)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("creating %s: %v", name, err)
	}

	return path
}

func writeTempFile(t *testing.T, dir, name string, size int) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

// The process takes its root with it under ANY `-run` filter.
//
// This is the one thing the fabricated-directory specs above cannot say, and it
// is the finding itself: the root is created at package INIT, so what owns it
// is the process, and a guard hanging off the Ginkgo entry point does not run
// when `-run` selects one of the 260 plain Go tests beside it. So this runs a
// real test binary of this very package with a filter that matches NOTHING --
// no test executes, the root is still created -- and asserts the temp directory
// it was pointed at is empty afterwards.
//
// `TMPDIR` is redirected into the spec's own scratch, which keeps the child's
// build work and its root out of the user's temp directory, and incidentally
// puts a live `go-build*` under the same roof the child's guard is sweeping.
func TestTheProcessTakesItsTempRootWithItUnderAnyRunFilter(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain on PATH to run a second test binary with")
	}

	scratch := t.TempDir()

	child := exec.Command("go", "test", "-count=1", "-run", "^$",
		"github.com/concourse/concourse/atc/worker/jetbridge")
	child.Env = append(os.Environ(), "TMPDIR="+scratch)
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("running a filtered test binary of this package: %v\n%s", err, out)
	}

	entries, err := os.ReadDir(scratch)
	if err != nil {
		t.Fatalf("reading the child's temp directory: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), tempRootPrefix) {
			t.Errorf("a test binary of this package that ran NO tests left %s behind; the temp "+
				"root is created at package init, so removing it belongs to TestMain and not to "+
				"the suite's entry point", entry.Name())
		}
	}
}
