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
// The guard reports only entries carrying THIS process's pid, plus any
// `go-build*` that escaped the redirect. Attribution is the point: a plain
// before/after count under os.TempDir() reports a neighbouring package's live
// work as this package's leak, and a guard that cries wolf is a guard somebody
// deletes.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// tempRoot is the one directory this package creates outside its own tree.
//
// The pid is in the name so the guard can tell its own leavings from another
// test process's live work, and so a human reading `ls` can tell a leak from
// something still running.
var tempRoot = func() string {
	root, err := os.MkdirTemp("", fmt.Sprintf("hangaroutput-daemon-%d-*", os.Getpid()))
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
	var leaks []string

	if err := os.RemoveAll(tempRoot); err != nil {
		leaks = append(leaks, fmt.Sprintf("the package temp root %s could not be removed: %v",
			filepath.Base(tempRoot), err))
	}
	// The root is created at package init, so the "before" snapshot taken in
	// TestMain already has it. Forgetting that would make the guard vacuous:
	// the one directory it exists to chase would be exempt from it.
	delete(before, filepath.Base(tempRoot))

	for name := range tempSuspects() {
		if before[name] {
			continue
		}
		path := filepath.Join(os.TempDir(), name)
		size := treeBytes(path)
		_ = os.RemoveAll(path)
		leaks = append(leaks, fmt.Sprintf("%s (%d bytes) survived this package's run; every "+
			"directory a harness makes outside its own tree belongs under the package temp root, "+
			"and a `go build` this package drives runs with TMPDIR pointed inside it", name, size))
	}

	return leaks
}

// tempSuspects is the set of entries under os.TempDir() this process could be
// responsible for: its own pid-stamped roots, and any go-build work directory,
// which no test in this tree should be producing there any more.
func tempSuspects() map[string]bool {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return nil
	}

	mine := fmt.Sprintf("-%d-", os.Getpid())
	suspects := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "go-build") || strings.Contains(name, mine) {
			suspects[name] = true
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
