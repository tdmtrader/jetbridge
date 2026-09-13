package hangaroutput_test

// The guard, guarded.
//
// A temp guard is infrastructure that DELETES, and the only thing standing
// between it and a neighbour's work is what it believes it can attribute. That
// belief is worth a spec of its own: the round that added the guard shipped one
// that treated every `go-build*` directory appearing during the run as its own
// leak, removed a live compile's work directory and failed the package for it.
//
// These run the guard's real body over a FABRICATED temp directory. That is not
// a stand-in for the thing under test -- the body, the attribution and the
// removal are the production ones -- it is a stand-in for `$TMPDIR`, and it is
// the only way to assert the half that matters here: what the guard leaves
// alone. Proving that against the real temp directory would mean leaving a
// foreign directory in the user's `$TMPDIR` after the run to show it survived,
// which is the leak this whole file exists to stop.

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

	root := makeDir(t, dir, fmt.Sprintf("%s%d-root", tempRootPrefix, pid))
	before := tempSuspectsIn(dir, pid)

	// Everything below appears DURING the run, which is what makes it a
	// candidate at all: only the first is ours.
	leaked := makeDir(t, dir, fmt.Sprintf("%s%d-leaked", tempRootPrefix, pid))
	writeFile(t, leaked, "hangar-output-daemon", 4096)

	foreign := makeDir(t, dir, "go-build-r3foreign-live2")
	writeFile(t, foreign, "b001-compile.o", 128)

	neighbour := makeDir(t, dir, "hangaroutput-daemon-999999-live")
	stranger := makeDir(t, dir, "TestSomebodyElse2751953891")

	leaks := tempLeaksIn(dir, root, before, pid)

	for _, survivor := range []string{foreign, neighbour, stranger} {
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

	stale := makeDir(t, dir, fmt.Sprintf("%s%d-stale", tempRootPrefix, dead))
	writeFile(t, stale, "hangar-output-daemon", 64)
	live := makeDir(t, dir, fmt.Sprintf("%s%d-live", tempRootPrefix, os.Getpid()))
	foreign := makeDir(t, dir, "go-build-r3foreign-live2")
	other := makeDir(t, dir, fmt.Sprintf("some-other-suite-%d-live", dead))

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

func makeDir(t *testing.T, parent, name string) string {
	t.Helper()

	path := filepath.Join(parent, name)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("creating %s: %v", name, err)
	}

	return path
}

func writeFile(t *testing.T, dir, name string, size int) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}
