package engine_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"code.cloudfoundry.org/clock"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/atc/policy/opa"
	"github.com/concourse/concourse/atc/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/time/rate"
)

var opaServer = new(recordingOPA)

func init() {
	util.PanicSink = GinkgoWriter

	// The suite's one BeforeSuite belongs to postgresrunner, and policy.Initialize
	// refuses to run with two configured agent factories, so the agent every
	// checker in this suite talks to is registered once, here.
	server := httptest.NewServer(opaServer)
	policy.RegisterAgent(&opa.OpaConfig{
		URL:                  server.URL,
		Timeout:              5 * time.Second,
		ResultAllowedKey:     "result.allowed",
		ResultShouldBlockKey: "result.block",
		ResultMessagesKey:    "result.reasons",
	})
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

const opaAllowed = `{"result": {"allowed": true}}`

var _ = BeforeEach(func() {
	opaServer.Reset()
})

// recordingOPA answers policy checks the way an OPA deployment does, and keeps
// the requests so specs can assert on what the checker sent over the wire.
type recordingOPA struct {
	mu       sync.Mutex
	requests []policyRequest
	status   int
	response string
}

// policyRequest is one check as OPA received it. The action's data stays
// encoded so each spec can decode it into the shape that action sends.
type policyRequest struct {
	policy.PolicyCheckInput
	Data json.RawMessage `json:"data"`
}

func (o *recordingOPA) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Input policyRequest `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	o.mu.Lock()
	o.requests = append(o.requests, body.Input)
	status, response := o.status, o.response
	o.mu.Unlock()

	w.WriteHeader(status)
	fmt.Fprint(w, response)
}

func (o *recordingOPA) Reset() {
	o.Answers(opaAllowed)
}

func (o *recordingOPA) Answers(response string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.requests = nil
	o.status = http.StatusOK
	o.response = response
}

// Fails answers the way an OPA deployment that is down does.
func (o *recordingOPA) Fails() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.requests = nil
	o.status = http.StatusInternalServerError
	o.response = ""
}

func (o *recordingOPA) Requests() []policyRequest {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.requests
}

// newPolicyChecker builds the agent-backed checker the ATC wires in, filtered
// to the actions the spec wants checked.
func newPolicyChecker(actions ...string) policy.Checker {
	GinkgoHelper()

	checker, err := policy.Initialize(
		lagertest.NewTestLogger("policy"),
		"some-cluster",
		"some-version",
		policy.Filter{Actions: actions},
	)
	Expect(err).NotTo(HaveOccurred())
	return checker
}

// recordingChecker keeps the real checker's decisions while recording the one
// thing it gives no way to observe: the actions a delegate asked about.
type recordingChecker struct {
	policy.Checker

	actions []string
}

func (c *recordingChecker) ShouldCheckAction(action string) bool {
	c.actions = append(c.actions, action)
	return c.Checker.ShouldCheckAction(action)
}
