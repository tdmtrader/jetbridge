//go:build !live

package jetbridge

// The untagged build's entry point, and it exists for one reason: the temp
// root is created at package INIT, for every test binary of this package, and
// the guard used to hang off `TestJetbridge`.
//
// That made the guard a property of one test rather than of the process.
// `go test ./atc/worker/jetbridge/ -run <a single Go test>` is how a developer
// runs one of the 260 plain `Test*` functions beside the Ginkgo suite, and
// under `-run` the suite does not execute, so nothing ever removed the root --
// deterministically, every invocation, and 47 MB of it whenever the test
// started a real output daemon. A guard that only runs when the whole suite
// runs cannot say "this process takes its directory with it"; only TestMain
// can, because only TestMain is the process.
//
// The `live` build has a TestMain of its own in the external test package and a
// binary may hold only one, hence the constraint -- and that one calls the same
// two functions, so both builds are guarded.

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// What was already in the user's temp directory before this process ran.
	// Anything attributable to it that is still there afterwards is a leak, and
	// it fails the package rather than accumulating a copy of a 150 MB daemon
	// binary per run until the volume is full.
	before := TempSuspects()

	code := m.Run()

	if leaks := TempLeaks(before); len(leaks) != 0 {
		for _, leak := range leaks {
			fmt.Fprintln(os.Stderr, "temp leak:", leak)
		}
		if code == 0 {
			code = 1
		}
	}

	os.Exit(code)
}
