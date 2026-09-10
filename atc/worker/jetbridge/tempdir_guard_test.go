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
