package hangaroutput_test

// Where this package is allowed to put a file, and what happens if it forgets
// to take it away.
//
// A harness that starts a real daemon needs a real directory: a built binary, a
// control ledger, a steps root, a scratch root, key material. Those went into
// the user's temp directory and nothing removed them, so a suite that is run a
// hundred times over a week leaves a hundred copies of a 150 MB binary behind.
// Fifty-one gigabytes of them were found on this machine, and a full root
// volume does not fail one suite -- it reddens EVERY suite, and imitates the
// both-red evidence a mutation battery is read from.
//
// The rule is one directory per process, everything inside it, and the process
// takes it with it. `go build`'s own work directory is included by pointing the
// build's TMPDIR inside ours: the go tool removes it on success and leaves it
// on a signal, and on a signal is exactly when it used to be left in the user's
// temp directory forever.
//
// ATTRIBUTION IS THE WHOLE RULE, and it is a rule about deleting as much as
// about reporting. The guard reports and removes entries carrying THIS
// process's pid, and nothing else -- not a `go-build*` directory, not a
// neighbour's scratch, not anything that merely appeared while this package
// ran. A plain before/after count under os.TempDir() reports a neighbouring
// package's live work as this package's leak; a guard that also DELETES what it
// cannot attribute takes a concurrent `go build`'s work directory out from
// under it mid-compile, which is the `fork/exec ...: no such file or directory`
// shape this tree already warns about elsewhere, and then reddens this package
// for a leak that was never its own. CI's unit tier runs the packages in
// parallel and two siblings build binaries into TMPDIR from inside their tests,
// so the neighbour is not hypothetical. A guard that cries wolf is a guard
// somebody deletes; a guard that eats the sheep is worse.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// tempRootPrefix names this package's roots, and the pid follows it. Both the
// guard and the stale-root sweep read the pid back out of the name.
const tempRootPrefix = "hangaroutput-daemon-"

// tempRoot is the one directory this package creates outside its own tree.
//
// The pid is in the name so the guard can tell its own leavings from another
// test process's live work, and so a human reading `ls` can tell a leak from
// something still running.
var tempRoot = func() string {
	root, err := os.MkdirTemp("", fmt.Sprintf("%s%d-*", tempRootPrefix, os.Getpid()))
	if err != nil {
		panic("hangaroutput harness: creating the package temp root: " + err.Error())
	}

	return root
}()

// tempLeaks removes this process's temp root and reports whatever it could not
// account for, as sentences.
//
// It is called from TestMain after the run, and a non-empty answer fails the
// package. Removing first and reporting second is deliberate: the report is
// about the RULE, and leaving the bytes behind to prove they were there would
// be the leak all over again.
func tempLeaks(before map[string]bool) []string {
	return tempLeaksIn(os.TempDir(), tempRoot, before, os.Getpid())
}

// tempLeaksIn is tempLeaks with the directory and the pid named, so the guard
// can be run over a fabricated temp directory and asserted on -- including the
// half that is about what it must NOT touch, which is unassertable against the
// real one without leaving a foreign directory behind to prove it.
func tempLeaksIn(dir, root string, before map[string]bool, pid int) []string {
	var leaks []string

	if err := os.RemoveAll(root); err != nil {
		leaks = append(leaks, fmt.Sprintf("the package temp root %s could not be removed: %v",
			filepath.Base(root), err))
	}
	// The root is created at package init, so the "before" snapshot taken in
	// TestMain already has it. Forgetting that would make the guard vacuous:
	// the one directory it exists to chase would be exempt from it.
	delete(before, filepath.Base(root))

	for name := range tempSuspectsIn(dir, pid) {
		if before[name] {
			continue
		}
		path := filepath.Join(dir, name)
		size := treeBytes(path)
		_ = os.RemoveAll(path)
		leaks = append(leaks, fmt.Sprintf("%s (%d bytes) survived this package's run; every "+
			"directory a harness makes outside its own tree belongs under the package temp root, "+
			"and a `go build` this package drives runs with TMPDIR pointed inside it", name, size))
	}

	return leaks
}

// tempSuspects is the set of entries under os.TempDir() this process is
// responsible for: the ones carrying its own pid, and only those.
func tempSuspects() map[string]bool {
	return tempSuspectsIn(os.TempDir(), os.Getpid())
}

func tempSuspectsIn(dir string, pid int) map[string]bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	mine := fmt.Sprintf("-%d-", pid)
	suspects := map[string]bool{}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), mine) {
			suspects[entry.Name()] = true
		}
	}

	return suspects
}

func treeBytes(root string) int64 {
	var total int64
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			total += info.Size()
		}

		return nil
	})

	return total
}
