package policychecker_test

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/atc/policy/opa"
	"github.com/concourse/concourse/atc/postgresrunner"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/tedsuo/ifrit"

	"testing"
)

func TestPolicyChecker(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "API PolicyChecker Suite")
}

// systemClaimKey and systemClaimValue are the defaults atccmd hands the
// accessor, and are what make a request count as self-invoked.
const (
	systemClaimKey   = "aud"
	systemClaimValue = "concourse-worker"
)

var (
	testLogger = lagertest.NewTestLogger("test")

	opaConfig *opa.OpaConfig
	opaServer *recordingOpa

	postgresRunner postgresrunner.Runner
	dbProcess      ifrit.Process
)

// persistTeam stores a team in a clone of its own and hands back the row as
// PostgreSQL gives it up again, so the accessor derives roles from stored auth
// rather than from the value handed to CreateTeam. Only the spec that needs a
// team calls this; the rest never touch the database.
func persistTeam(name string, auth atc.TeamAuth) db.Team {
	GinkgoHelper()

	postgresRunner.CreateTestDBFromTemplate()
	DeferCleanup(postgresRunner.DropTestDB)

	conn := postgresRunner.OpenConn()
	DeferCleanup(func() {
		Expect(conn.Close()).To(Succeed())
	})

	var lockConns [lock.FactoryCount]*sql.DB
	for i := 0; i < lock.FactoryCount; i++ {
		lockConns[i] = postgresRunner.OpenSingleton()
		lockConn := lockConns[i]
		DeferCleanup(func() {
			Expect(lockConn.Close()).To(Succeed())
		})
	}
	ignore := func(lager.Logger, lock.LockID) {}
	teamFactory := db.NewTeamFactory(conn, lock.NewLockFactory(lockConns, ignore, ignore))

	_, err := teamFactory.CreateTeam(atc.Team{Name: name, Auth: auth})
	Expect(err).NotTo(HaveOccurred())

	team, found, err := teamFactory.FindTeam(name)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return team
}

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

	postgresrunner.InitializeRunnerForGinkgo(&postgresRunner, &dbProcess)
})

var _ = AfterSuite(func() {
	postgresrunner.FinalizeRunnerForGinkgo(&postgresRunner, &dbProcess)
})

var _ = BeforeEach(func() {
	opaServer.Reset(`{"result": {"allowed": true}}`)
})
