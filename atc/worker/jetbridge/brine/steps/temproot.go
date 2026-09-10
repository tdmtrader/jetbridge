package steps

// Where this adapter is allowed to put a daemon's bytes, and what happens if it
// forgets to take them away.
//
// A scenario that runs a REAL daemon needs a real directory twice over: the
// built binary (~150 MB, one per daemon per adapter process) and the node
// storage root the daemon serves from. Both went straight into the user's temp
// directory under names carrying nothing but a random suffix, and nothing swept
// them: 571 `brine-artifact-daemon-*`, `brine-hangar-output-daemon-*` and
// `brine-daemon-root-*` directories were found here, 42 GB, the oldest five
// days old. A full root volume does not redden one suite -- it reddens EVERY
// suite, and imitates the both-red evidence a mutation battery is read from.
//
// The rule is the one Phase 5 settled for the Go harnesses, restated for a
// process that is not a `go test` binary: ONE directory per adapter process,
// everything inside it, the process takes it with it, and the pid is in the
// name so that what is left behind can be attributed.
//
// ATTRIBUTION IS THE WHOLE RULE, and it is a rule about deleting as much as
// about reporting. The sweep removes roots this adapter composed whose owning
// PROCESS IS GONE, and nothing else: not a sibling adapter's live root under a
// parallel brine run, not a directory that merely looks similar, not anything
// that appeared while this process ran. A guard that also deleted what it could
// not attribute would take a live sibling's daemon storage out from under it,
// and the scenario would fail for a reason nobody could find.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// daemonRootPrefix names this adapter's daemon roots, and the pid follows it.
// Both the guard and the stale-root sweep read the pid back out of the name.
//
// It contains both "brine-" and "daemon" on purpose: that is the shape a human
// greps for, and it is the shape the run-level guard reports on.
const daemonRootPrefix = "brine-adapter-daemon-"

// adapterDaemonRoot is the one directory this adapter creates outside its own
// tree. Everything a daemon fixture needs goes under it.
var adapterDaemonRoot = func() string {
	sweepStaleAdapterRoots(os.TempDir(), os.Getpid())

	root, err := os.MkdirTemp("", fmt.Sprintf("%s%d-*", daemonRootPrefix, os.Getpid()))
	if err != nil {
		panic("brine adapter: creating the daemon temp root: " + err.Error())
	}

	return root
}()

// daemonTempDir makes a directory for one purpose UNDER this process's root.
//
// Every caller in realdaemon.go goes through it, and the source guard in
// temproot_test.go is what keeps that true: a single `os.MkdirTemp("", ...)`
// added back is a directory nothing can attribute and nothing will remove.
func daemonTempDir(purpose string) (string, error) {
	return os.MkdirTemp(adapterDaemonRoot, purpose+"-*")
}

// SweepAdapterDaemonRoots removes this process's daemon root and reports, as
// sentences, whatever it could not account for.
//
// It is called from the adapter's main before every exit, and a non-empty
// answer fails the run. Removing first and reporting second is deliberate: the
// report is about the RULE, and leaving the bytes behind to prove they were
// there would be the leak all over again.
func SweepAdapterDaemonRoots() []string {
	return sweepAdapterRootsIn(os.TempDir(), adapterDaemonRoot, os.Getpid())
}

// sweepAdapterRootsIn is SweepAdapterDaemonRoots with the directory and the pid
// named, so the guard can be driven over a fabricated temp directory and
// asserted on -- including the half that is about what it must NOT touch, which
// is unassertable against the real one without leaving a foreign directory
// behind to prove it.
func sweepAdapterRootsIn(dir, root string, pid int) []string {
	var leaks []string

	if root != "" {
		if err := os.RemoveAll(root); err != nil {
			leaks = append(leaks, fmt.Sprintf("the adapter daemon root %s could not be removed: %v",
				filepath.Base(root), err))
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return leaks
	}
	mine := fmt.Sprintf("-%d-", pid)
	for _, entry := range entries {
		name := entry.Name()
		if !looksLikeADaemonRoot(name) || !strings.Contains(name, mine) {
			continue
		}
		if root != "" && name == filepath.Base(root) {
			continue
		}
		path := filepath.Join(dir, name)
		size := treeBytes(path)
		_ = os.RemoveAll(path)
		leaks = append(leaks, fmt.Sprintf("%s (%d bytes) survived this adapter's run; every "+
			"directory a daemon fixture makes belongs under the adapter's own root", name, size))
	}

	return leaks
}

// looksLikeADaemonRoot is the `brine-*daemon*` shape, read as two facts rather
// than as a glob so that a name this adapter never composes cannot match.
func looksLikeADaemonRoot(name string) bool {
	return strings.HasPrefix(name, "brine-") && strings.Contains(name, "daemon")
}

// sweepStaleAdapterRoots removes this adapter's roots whose owning process is
// gone, and reports what it removed.
//
// Only this adapter's own prefix, and only a pid that is not running: a live
// sibling under a parallel brine run owns its directory. A pid this process
// cannot answer for -- an error that is not "no such process" -- counts as
// ALIVE, so the sweep errs towards leaving bytes on the disk rather than
// towards deleting somebody's live work.
func sweepStaleAdapterRoots(dir string, self int) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var swept []string
	for _, entry := range entries {
		name := entry.Name()
		pid, ok := adapterRootPID(name)
		if !ok || pid == self || adapterProcessAlive(pid) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, name)); err == nil {
			swept = append(swept, name)
		}
	}

	return swept
}

// adapterRootPID reads the pid back out of a root's name, and answers no for a
// name this adapter did not compose.
func adapterRootPID(name string) (int, bool) {
	if !strings.HasPrefix(name, daemonRootPrefix) {
		return 0, false
	}

	rest := strings.TrimPrefix(name, daemonRootPrefix)
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

// adapterProcessAlive answers whether a pid is running, and answers YES when it
// cannot tell.
//
// Signal 0 delivers nothing and reports only whether it could have. "No such
// process" is the one answer that permits a removal, in its two spellings --
// the errno, and the ErrProcessDone the standard library substitutes for a
// child this process has already reaped. A permission error, somebody else's
// process, is not one of them, and neither is anything unrecognised.
func adapterProcessAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
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
