package jetbridge

// Where this package is allowed to put a file, and what happens if it forgets
// to take it away.
//
// The same rule and the same reason as atc/hangaroutput's: a harness that
// starts a real output daemon needs a built binary, a control ledger, a steps
// root, a scratch root and key material, and none of it was ever removed. A
// suite run repeatedly over a week leaves a copy of a 150 MB binary each time.
// A full root volume does not fail one suite -- it reddens every suite, and
// imitates the both-red evidence a mutation battery is read from.
//
// One directory per process, everything inside it, and the process takes it
// with it. `go build`'s own work directory is included by pointing the build's
// TMPDIR inside ours.
//
// ATTRIBUTION IS THE WHOLE RULE, and it is a rule about deleting as much as
// about reporting. The guard reports and removes entries carrying THIS
// process's pid, and nothing else. This is a Ginkgo suite: several processes of
// it run at once under `--procs`, so a plain before/after count under
// os.TempDir() would report a sibling's live work as this one's leak -- and a
// guard that also DELETED what it cannot attribute would take a neighbouring
// package's `go build` work directory out from under it mid-compile, which is
// the `fork/exec ...: no such file or directory` shape this tree already warns
// about, and then redden this package for a leak that was never its own.
//
// The exception, and it is still attribution: a root whose owning process is
// GONE. A run that panics, or is killed, or dies in TestMain before the run
// starts takes nothing with it, and no later run swept those because the guard
// trusted only its own pid. Liveness answers it -- a pid that is not running
// cannot be using its directory, and one that is running must not have it taken
// away.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// tempRootPrefix names this package's roots, and the pid follows it. Both the
// guard and the stale-root sweep read the pid back out of the name.
const tempRootPrefix = "jetbridge-output-daemon-"

// tempRoot is the one directory this package creates outside its own
// tree. The pid is in the name so the guard can tell its own leavings from a
// sibling Ginkgo process's live work.
var tempRoot = func() string {
	// Before this process's own root, the ones nobody owns any more.
	sweepStaleRoots(os.TempDir(), os.Getpid())

	root, err := os.MkdirTemp("", fmt.Sprintf("%s%d-*", tempRootPrefix, os.Getpid()))
	if err != nil {
		panic("jetbridge harness: creating the package temp root: " + err.Error())
	}

	return root
}()

// TempLeaks removes this process's temp root and reports whatever attributable
// thing it could not account for, as sentences.
//
// It is called after the run, and a non-empty answer fails the package.
// Removing first and reporting second is deliberate: the report is about the
// RULE, and leaving the bytes behind to prove they were there would be the leak
// all over again.
//
// Exported because the suite's own entry point lives in the external
// `jetbridge_test` package; it exists only in the test binary.
func TempLeaks(before map[string]bool) []string {
	return tempLeaksIn(os.TempDir(), tempRoot, before, os.Getpid())
}

// tempLeaksIn is TempLeaks with the directory and the pid named, so the guard
// can be run over a fabricated temp directory and asserted on -- including the
// half that is about what it must NOT touch, which is unassertable against the
// real one without leaving a foreign directory behind to prove it.
func tempLeaksIn(dir, root string, before map[string]bool, pid int) []string {
	var leaks []string

	if err := os.RemoveAll(root); err != nil {
		leaks = append(leaks, fmt.Sprintf("the package temp root %s could not be removed: %v",
			filepath.Base(root), err))
	}
	// Created at package init, so the "before" snapshot already has it.
	// Forgetting that would exempt the one directory this guard exists for.
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

// TempSuspects is the set of entries under os.TempDir() this process is
// responsible for: the ones carrying its own pid, and only those. Exported for
// the reason above; it exists only in the test binary.
func TempSuspects() map[string]bool {
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

// sweepStaleRoots removes this package's roots whose owning process is gone,
// and reports what it removed.
//
// Only this package's own prefix, and only a pid that is not running: a live
// sibling under `--procs` owns its directory, and a directory belonging to
// another package belongs to that package's guard. A pid this process cannot
// answer for -- an error that is not "no such process" -- counts as ALIVE, so
// the sweep errs towards leaving bytes on the disk rather than towards deleting
// somebody's live work.
func sweepStaleRoots(dir string, self int) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var swept []string
	for _, entry := range entries {
		name := entry.Name()
		pid, ok := rootPID(name)
		if !ok || pid == self || processAlive(pid) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, name)); err == nil {
			swept = append(swept, name)
		}
	}

	return swept
}

// rootPID reads the pid back out of a root's name, and answers no for a name
// this package did not compose.
func rootPID(name string) (int, bool) {
	if !strings.HasPrefix(name, tempRootPrefix) {
		return 0, false
	}

	rest := strings.TrimPrefix(name, tempRootPrefix)
	digits, _, found := strings.Cut(rest, "-")
	if !found || digits == "" {
		return 0, false
	}

	var pid int
	if _, err := fmt.Sscanf(digits, "%d", &pid); err != nil || pid <= 0 {
		return 0, false
	}

	return pid, true
}

// processAlive answers whether a pid is running, and answers YES when it cannot
// tell.
//
// Signal 0 delivers nothing and reports only whether it could have. "No such
// process" is the one answer that permits a removal, in its two spellings --
// the errno, and the ErrProcessDone the standard library substitutes for a
// child this process has already reaped. A permission error, somebody else's
// process, is not one of them, and neither is anything unrecognised: the sweep
// errs towards leaving bytes on the disk rather than towards deleting live work.
func processAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		// Unreachable on Unix, and it still answers the contract's way: an
		// error here is "cannot tell", and cannot-tell is ALIVE. Answering no
		// would make the one unrecognised case the one that permits a removal.
		return true
	}

	err = process.Signal(syscall.Signal(0))

	return !errors.Is(err, syscall.ESRCH) && !errors.Is(err, os.ErrProcessDone)
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
