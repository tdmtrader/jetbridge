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
// The guard reports only entries carrying THIS process's pid, plus any
// `go-build*` that escaped the redirect. Attribution is what makes it usable
// here at all: this is a Ginkgo suite, several processes of it run at once
// under `--procs`, and a plain before/after count under os.TempDir() would
// report a sibling process's live work as this one's leak.
//
// The two entry points are exported because the suite's own entry point,
// TestJetbridge, lives in the external `jetbridge_test` package while the
// harness that starts the daemon lives here -- and a test binary may hold only
// one TestMain, which the `live` build tag has already spent.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tempRoot is the one directory this package creates outside its own
// tree. The pid is in the name so the guard can tell its own leavings from a
// sibling Ginkgo process's live work.
var tempRoot = func() string {
	root, err := os.MkdirTemp("", fmt.Sprintf("jetbridge-output-daemon-%d-*", os.Getpid()))
	if err != nil {
		panic("jetbridge harness: creating the package temp root: " + err.Error())
	}

	return root
}()

// AssertNoTempSurvives removes this process's temp root and fails the package
// for anything attributable it could not account for.
//
// It runs after RunSpecs returns, in each Ginkgo process, because this suite
// has no TestMain of its own in the untagged build and the one under the `live`
// tag belongs to a different set of tests. Exported for the reason in the file
// comment; it exists only in the test binary.
func AssertNoTempSurvives(t *testing.T, before map[string]bool) {
	if err := os.RemoveAll(tempRoot); err != nil {
		t.Errorf("the package temp root %s could not be removed: %v",
			filepath.Base(tempRoot), err)
	}
	// Created at package init, so the "before" snapshot already has it.
	// Forgetting that would exempt the one directory this guard exists for.
	delete(before, filepath.Base(tempRoot))

	for name := range TempSuspects() {
		if before[name] {
			continue
		}
		path := filepath.Join(os.TempDir(), name)
		_ = os.RemoveAll(path)
		t.Errorf("%s survived this package's run; every directory a harness makes outside its "+
			"own tree belongs under the package temp root, and a `go build` this package drives "+
			"runs with TMPDIR pointed inside it", name)
	}
}

// TempSuspects is the set of entries under os.TempDir() this process could be
// responsible for: its own pid-stamped root, and any go-build work directory,
// which no test in this tree should be producing there any more. Exported for
// the reason in the file comment; it exists only in the test binary.
func TempSuspects() map[string]bool {
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
