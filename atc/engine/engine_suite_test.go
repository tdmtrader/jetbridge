package engine_test

import (
	"testing"
	"time"

	"code.cloudfoundry.org/clock"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/time/rate"
)

func init() {
	util.PanicSink = GinkgoWriter
}

func TestEngine(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Engine Suite")
}

type engineDBFixture = engine.EngineDBFixture

var enginePostgresRunner = &engine.EnginePostgresRunner
var useEngineDB = engine.UseEngineDB
var closedEngineCloneConn = engine.ClosedEngineCloneConn
var createEngineJobBuild = engine.CreateEngineJobBuild
var consumeEngineBuildEvent = engine.ConsumeEngineBuildEvent

var noopStepper exec.Stepper = func(atc.Plan) exec.Step {
	Fail("cannot create substep")
	return nil
}

// newCheckRateLimiter builds the rate limiter the ATC wires into the engine,
// admitting every check immediately.
func newCheckRateLimiter() *db.ResourceCheckRateLimiter {
	return db.NewResourceCheckRateLimiter(rate.Inf, 0, time.Minute, nil, time.Minute, clock.NewClock())
}
