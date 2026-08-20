package api_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"code.cloudfoundry.org/clock"
	"code.cloudfoundry.org/clock/fakeclock"
	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/accessor/accessorfakes"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/containerserver"
	"github.com/concourse/concourse/atc/api/policychecker"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/noop"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/atc/wrappa"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	sink *lager.ReconfigurableSink

	externalURL      = "https://example.com"
	clusterName      = "Test Cluster"
	featureFlagsJson = ` {
	"build_rerun": false,
	"global_resources": false,
	"resource_causality": false
}`

	workerRuntime    *apiWorkerRuntime
	fakeAccess       *accessorfakes.FakeAccess
	fakeAccessor     *accessorfakes.FakeAccessFactory
	secretManager    creds.Secrets
	varSourcePool    creds.VarSourcePool
	credsManagers    creds.Managers
	interceptTimeout *observingInterceptTimeout
	isTLSEnabled     bool
	cliDownloadsDir  string
	logger           *lagertest.TestLogger
	fakeClock        *fakeclock.FakeClock

	constructedEventHandler *fakeEventHandlerFactory

	server *httptest.Server
	client *http.Client
)

type fakeEventHandlerFactory struct {
	build db.BuildForAPI

	lock sync.Mutex
}

func (f *fakeEventHandlerFactory) Construct(
	logger lager.Logger,
	build db.BuildForAPI,
) http.Handler {
	f.lock.Lock()
	f.build = build
	f.lock.Unlock()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("fake event handler factory was here"))
	})
}

// observingInterceptTimeout counts resets of the real idle timeout of a
// hijacked container, and lets a spec expire it on demand. Reset and Error run
// the production code; only the expiry channel belongs to the spec, since the
// real one fires on wall-clock time alone.
type observingInterceptTimeout struct {
	containerserver.InterceptTimeout

	channel chan time.Time

	lock   sync.Mutex
	resets int
}

func (t *observingInterceptTimeout) NewInterceptTimeout() containerserver.InterceptTimeout {
	return t
}

func (t *observingInterceptTimeout) Reset() {
	t.lock.Lock()
	t.resets++
	t.lock.Unlock()
	t.InterceptTimeout.Reset()
}

func (t *observingInterceptTimeout) Channel() <-chan time.Time {
	return t.channel
}

func (t *observingInterceptTimeout) resetCount() int {
	t.lock.Lock()
	defer t.lock.Unlock()
	return t.resets
}

func (t *observingInterceptTimeout) expire() {
	t.channel <- time.Time{}
}

var _ = BeforeEach(func() {
	interceptTimeout = &observingInterceptTimeout{
		InterceptTimeout: containerserver.NewInterceptTimeoutFactory(time.Hour).NewInterceptTimeout(),
		channel:          make(chan time.Time),
	}

	fakeAccess = new(accessorfakes.FakeAccess)
	fakeAccessor = new(accessorfakes.FakeAccessFactory)
	fakeAccessor.CreateReturns(fakeAccess, nil)

	workerRuntime = newAPIWorkerRuntime()

	fakeClock = fakeclock.NewFakeClock(time.Unix(123, 456))

	var err error
	cliDownloadsDir, err = os.MkdirTemp("", "cli-downloads")
	Expect(err).NotTo(HaveOccurred())

	constructedEventHandler = &fakeEventHandlerFactory{}

	logger = lagertest.NewTestLogger("api")

	secretManager = noop.Noop{}
	varSourcePool = creds.NewVarSourcePool(
		logger.Session("var-source-pool"),
		creds.CredentialManagementConfig{},
		5*time.Minute,
		time.Minute,
		clock.NewClock(),
	)
	DeferCleanup(varSourcePool.Close)
	credsManagers = make(creds.Managers)

	sink = lager.NewReconfigurableSink(lager.NewPrettySink(GinkgoWriter, lager.DEBUG), lager.DEBUG)

	isTLSEnabled = false

	server = newAPIServer(apiDBDeps{})

	client = &http.Client{
		Transport: &http.Transport{},
	}
})

// apiDBDeps is every database-backed collaborator the API handler is built
// from. A Describe that exercises one passes real factories -- see useRealDB in
// real_db_test.go. The suite default leaves them all nil, which is safe because
// the only endpoints reached without useRealDB (info, cli, log level) never
// touch the database.
//
// Collecting them in a struct is what lets a single Describe swap one out
// without disturbing the rest of the package.
type apiDBDeps struct {
	teamFactory           db.TeamFactory
	pipelineFactory       db.PipelineFactory
	pipelineRunFactory    db.PipelineRunFactory
	jobFactory            db.JobFactory
	resourceFactory       db.ResourceFactory
	workerFactory         db.WorkerFactory
	workerTeamFactory     db.TeamFactory
	volumeRepository      db.VolumeRepository
	buildFactory          db.BuildFactory
	checkFactory          db.CheckFactory
	resourceConfigFactory db.ResourceConfigFactory
	userFactory           db.UserFactory

	wall              db.Wall
	signingKeyFactory db.SigningKeyFactory
}

// auditedAction is the action the accessor handler labels every request with.
// Production labels each route with its own action; here one handler fronts
// the whole API, so it has to be an action the auditor routes -- the auditor
// panics on one it does not know -- and a system action is the category the
// audit flag below turns on.
const auditedAction = atc.GetInfo

// newAuditor returns the auditor production wires in, with the one category
// auditedAction falls into switched on so that Audit actually logs.
func newAuditor() auditor.Auditor {
	return auditingACopy{
		Auditor: auditor.NewAuditor(false, false, false, false, false, true, false, false, false, logger),
	}
}

// auditingACopy hands the auditor its own copy of the request. Auditing parses
// the request form and net/http caches that parse; production audits from
// inside the router, after the router has appended the route's parameters to
// the query, while the accessor handler here fronts the router. Auditing the
// original would freeze a parameterless parse that every routed handler would
// then read `:build_id` and friends out of.
type auditingACopy struct {
	auditor.Auditor
}

func (a auditingACopy) Audit(action string, userName string, r *http.Request) {
	a.Auditor.Audit(action, userName, r.Clone(r.Context()))
}

// newAPIServer builds the full API handler over deps and serves it. The
// accessor, policy checker and event handler stay fake: none of them is a
// database seam.
func newAPIServer(deps apiDBDeps) *httptest.Server {
	GinkgoHelper()

	checkPipelineAccessHandlerFactory := auth.NewCheckPipelineAccessHandlerFactory(deps.teamFactory)

	checkBuildReadAccessHandlerFactory := auth.NewCheckBuildReadAccessHandlerFactory(deps.buildFactory)

	checkBuildWriteAccessHandlerFactory := auth.NewCheckBuildWriteAccessHandlerFactory(deps.buildFactory)

	checkWorkerTeamAccessHandlerFactory := auth.NewCheckWorkerTeamAccessHandlerFactory(deps.workerFactory)

	apiWrapper := wrappa.MultiWrappa{
		wrappa.NewPolicyCheckWrappa(logger, policychecker.NewApiPolicyChecker(policy.NoopChecker{})),
		wrappa.NewAPIAuthWrappa(
			checkPipelineAccessHandlerFactory,
			checkBuildReadAccessHandlerFactory,
			checkBuildWriteAccessHandlerFactory,
			checkWorkerTeamAccessHandlerFactory,
		),
	}

	workerPool := worker.NewPool(
		poolWorkerFactory{runtime: workerRuntime},
		worker.DB{
			WorkerFactory: deps.workerFactory,
			TeamFactory:   deps.teamFactory,
			VolumeRepo:    deps.volumeRepository,
		},
	)

	handler, err := api.NewHandler(
		logger,

		externalURL,
		"",
		clusterName,

		apiWrapper,

		deps.teamFactory,
		deps.pipelineFactory,
		deps.pipelineRunFactory,
		deps.jobFactory,
		deps.resourceFactory,
		deps.workerFactory,
		deps.workerTeamFactory,
		deps.volumeRepository,
		deps.buildFactory,
		deps.checkFactory,
		deps.resourceConfigFactory,
		deps.userFactory,

		constructedEventHandler.Construct,

		workerPool,

		sink,

		isTLSEnabled,

		cliDownloadsDir,
		"1.2.3",
		"4.5.6",
		"0.1.0-test",
		"8.0.1-test",
		secretManager,
		varSourcePool,
		credsManagers,
		interceptTimeout,
		time.Second,
		deps.wall,
		fakeClock,
		deps.signingKeyFactory,
		nil,
	)

	Expect(err).NotTo(HaveOccurred())

	accessorHandler := accessor.NewHandler(
		logger,
		auditedAction,
		handler,
		fakeAccessor,
		newAuditor(),
		map[string]string{},
	)

	return httptest.NewServer(wrappa.LoggerHandler{
		Logger:  logger,
		Handler: accessorHandler,
	})
}

var _ = AfterEach(func() {
	os.Remove(cliDownloadsDir)
	server.Close()
})

func TestAPI(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "API Suite")
}
