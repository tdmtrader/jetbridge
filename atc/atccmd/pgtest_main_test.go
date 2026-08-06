package atccmd

import (
	"os"
	"testing"

	"github.com/concourse/concourse/atc/db/pgtest"
)

// The composition tests need a genuine db.DbConn: composeAgentCheckpoints and
// composeAgentSnapshots reject a nil connection outright (command.go:2223), so
// a real one is the only honest way to satisfy them. pgtest runs its own
// postmaster, migrates a template once for this binary, and hands out a
// database per test that asks; tests that never call OpenTestDB pay nothing.
//
// Main is mandatory -- without it the postmaster outlives the test binary.
func TestMain(m *testing.M) {
	os.Exit(pgtest.Main(m))
}
