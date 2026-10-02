package bootstrap_test

import (
	"testing"

	"github.com/concourse/concourse/atc/postgresrunner"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var postgresRunner postgresrunner.Runner
var _ = postgresrunner.GinkgoRunner(&postgresRunner)

// TestBootstrap runs the database step's specs over a real PostgreSQL; the
// inventory reconcile's tests beside it are plain Go tests.
func TestBootstrap(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Hangar bootstrap")
}
