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

	"github.com/concourse/concourse/atc/api"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/buildserver"
	"github.com/concourse/concourse/atc/api/containerserver"
	"github.com/concourse/concourse/atc/api/infoserver"
	"github.com/concourse/concourse/atc/api/policychecker"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/noop"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/atc/wrappa"
	"github.com/concourse/concourse/skymarshal/skycmd"

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
	secretManager    creds.Secrets
	varSourcePool    creds.VarSourcePool
	credsManagers    creds.Managers
	interceptTimeout *observingInterceptTimeout
	isTLSEnabled     bool
	cliDownloadsDir  string
	logger           *lagertest.TestLogger
	fakeClock        *fakeclock.FakeClock

	server *httptest.Server
	client *http.Client

	apiProfileTransport *profileTransport
)

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

	workerRuntime = newAPIWorkerRuntime()

	fakeClock = fakeclock.NewFakeClock(time.Unix(123, 456))

	var err error
	cliDownloadsDir, err = os.MkdirTemp("", "cli-downloads")
	Expect(err).NotTo(HaveOccurred())

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

	postgresRunner.Truncate()
	apiDB = openRealDB()
	createRequestProfiles()

	apiProfileTransport = &profileTransport{base: &http.Transport{}}
	useProfile(anonymousProfile)
	client = &http.Client{Transport: apiProfileTransport}

	server = newAPIServer(apiDB.Deps)
	rootServer := server
	DeferCleanup(rootServer.Close)
})

// apiDBDeps is every database-backed collaborator the API handler is built
// from. The root server receives the full real fixture; individual Describes
// can override one dependency without making the others nil.
type apiDBDeps struct {
	teamFactory           db.TeamFactory
	pipelineFactory       db.PipelineFactory
	jobFactory            db.JobFactory
	resourceFactory       db.ResourceFactory
	workerFactory         db.WorkerFactory
	workerTeamFactory     db.TeamFactory
	volumeRepository      db.VolumeRepository
	buildFactory          db.BuildFactory
	checkFactory          db.CheckFactory
	resourceConfigFactory db.ResourceConfigFactory
	userFactory           db.UserFactory
	accessTokenFactory    db.AccessTokenFactory
	dbPinger              infoserver.DBPinger

	wall              db.Wall
	signingKeyFactory db.SigningKeyFactory
}

func (base apiDBDeps) withOverrides(overrides apiDBDeps) apiDBDeps {
	if overrides.teamFactory != nil {
		base.teamFactory = overrides.teamFactory
	}
	if overrides.pipelineFactory != nil {
		base.pipelineFactory = overrides.pipelineFactory
	}
	if overrides.jobFactory != nil {
		base.jobFactory = overrides.jobFactory
	}
	if overrides.resourceFactory != nil {
		base.resourceFactory = overrides.resourceFactory
	}
	if overrides.workerFactory != nil {
		base.workerFactory = overrides.workerFactory
	}
	if overrides.workerTeamFactory != nil {
		base.workerTeamFactory = overrides.workerTeamFactory
	}
	if overrides.volumeRepository != nil {
		base.volumeRepository = overrides.volumeRepository
	}
	if overrides.buildFactory != nil {
		base.buildFactory = overrides.buildFactory
	}
	if overrides.checkFactory != nil {
		base.checkFactory = overrides.checkFactory
	}
	if overrides.resourceConfigFactory != nil {
		base.resourceConfigFactory = overrides.resourceConfigFactory
	}
	if overrides.userFactory != nil {
		base.userFactory = overrides.userFactory
	}
	if overrides.accessTokenFactory != nil {
		base.accessTokenFactory = overrides.accessTokenFactory
	}
	if overrides.dbPinger != nil {
		base.dbPinger = overrides.dbPinger
	}
	if overrides.wall != nil {
		base.wall = overrides.wall
	}
	if overrides.signingKeyFactory != nil {
		base.signingKeyFactory = overrides.signingKeyFactory
	}
	return base
}

// newAuditor returns the same concrete route auditor production wires in.
func newAuditor() auditor.Auditor {
	return auditor.NewAuditor(false, false, false, false, false, true, false, false, false, logger)
}

// newAPIServer builds the full API handler over deps and serves it. The
// access wrapper resolves real persisted bearer tokens and team roles. The
// policy checker remains a deterministic suite fixture.
func newAPIServer(deps apiDBDeps) *httptest.Server {
	GinkgoHelper()
	Expect(apiDB).NotTo(BeNil())
	deps = apiDB.Deps.withOverrides(deps)

	displayID, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{})
	Expect(err).NotTo(HaveOccurred())
	accessFactory := accessor.NewAccessFactory(
		accessor.NewVerifier(deps.accessTokenFactory, []string{"api-test"}),
		deps.teamFactory,
		"sub",
		[]string{"api-system"},
		displayID,
	)

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
		wrappa.NewAccessorWrappa(logger, accessFactory, newAuditor(), map[string]string{}),
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
		deps.jobFactory,
		deps.resourceFactory,
		deps.workerFactory,
		deps.workerTeamFactory,
		deps.volumeRepository,
		deps.buildFactory,
		deps.checkFactory,
		deps.resourceConfigFactory,
		deps.userFactory,

		buildserver.NewEventHandler,

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
		deps.dbPinger,
	)

	Expect(err).NotTo(HaveOccurred())

	return httptest.NewServer(wrappa.LoggerHandler{
		Logger:  logger,
		Handler: handler,
	})
}

var _ = AfterEach(func() {
	os.Remove(cliDownloadsDir)
})

func TestAPI(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "API Suite")
}
