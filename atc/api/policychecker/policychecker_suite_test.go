package policychecker_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/atc/policy/opa"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"testing"
)

func TestPolicyChecker(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "API PolicyChecker Suite")
}

var (
	testLogger = lagertest.NewTestLogger("test")

	opaConfig *opa.OpaConfig
	opaServer *recordingOpa
)

// recordingOpa answers like a real OPA server would, and keeps the request
// bodies so specs can assert on what the agent actually sent over the wire.
type recordingOpa struct {
	mu       sync.Mutex
	requests [][]byte
	response string
	status   int
}

func (o *recordingOpa) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	o.mu.Lock()
	o.requests = append(o.requests, body)
	response, status := o.response, o.status
	o.mu.Unlock()

	if status != http.StatusOK {
		w.WriteHeader(status)
		return
	}

	fmt.Fprint(w, response)
}

func (o *recordingOpa) Reset(response string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.requests = nil
	o.response = response
	o.status = http.StatusOK
}

func (o *recordingOpa) RespondWithStatus(status int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.status = status
}

func (o *recordingOpa) Requests() [][]byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.requests
}

func initializedChecker(filter policy.Filter) policy.Checker {
	GinkgoHelper()

	checker, err := policy.Initialize(testLogger, "some-cluster", "some-version", filter)
	Expect(err).ToNot(HaveOccurred())
	Expect(checker).ToNot(BeNil())
	return checker
}

var _ = BeforeSuite(func() {
	opaServer = new(recordingOpa)

	server := httptest.NewServer(opaServer)
	DeferCleanup(server.Close)

	opaConfig = &opa.OpaConfig{
		URL:                  server.URL,
		Timeout:              5 * time.Second,
		ResultAllowedKey:     "result.allowed",
		ResultShouldBlockKey: "result.block",
		ResultMessagesKey:    "result.reasons",
	}
	policy.RegisterAgent(opaConfig)
})

var _ = BeforeEach(func() {
	opaServer.Reset(`{"result": {"allowed": true}}`)
})
