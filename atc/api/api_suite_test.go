package api_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"code.cloudfoundry.org/clock/fakeclock"
	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/atc/api"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/accessor/accessorfakes"
	"github.com/concourse/concourse/atc/api/apifakes"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/containerserver"
	"github.com/concourse/concourse/atc/api/policychecker"
	"github.com/concourse/concourse/atc/auditor/auditorfakes"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/credsfakes"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/policy"
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

	fakeWorkerPool    *apifakes.FakePool
	fakeAccess        *accessorfakes.FakeAccess
	fakeAccessor      *accessorfakes.FakeAccessFactory
	fakeSecretManager *credsfakes.FakeSecrets
	fakeVarSourcePool *credsfakes.FakeVarSourcePool
	credsManagers     creds.Managers
	interceptTimeout  *observingInterceptTimeout
	isTLSEnabled      bool
	cliDownloadsDir   string
	logger            *lagertest.TestLogger
	fakeClock         *fakeclock.FakeClock

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

	fakeWorkerPool = new(apifakes.FakePool)

	fakeSecretManager = new(credsfakes.FakeSecrets)
	fakeVarSourcePool = new(credsfakes.FakeVarSourcePool)
	credsManagers = make(creds.Managers)

	fakeClock = fakeclock.NewFakeClock(time.Unix(123, 456))

	var err error
	cliDownloadsDir, err = os.MkdirTemp("", "cli-downloads")
	Expect(err).NotTo(HaveOccurred())

	constructedEventHandler = &fakeEventHandlerFactory{}

	logger = lagertest.NewTestLogger("api")

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

// newAPIServer builds the full API handler over deps and serves it. The
// accessor, auditor, policy checker and event handler stay fake: none of them
// is a database seam.
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

		constructedEventHandler.Construct,

		fakeWorkerPool,

		sink,

		isTLSEnabled,

		cliDownloadsDir,
		"1.2.3",
		"4.5.6",
		"0.1.0-test",
		"8.0.1-test",
		fakeSecretManager,
		fakeVarSourcePool,
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
		"some-action",
		handler,
		fakeAccessor,
		new(auditorfakes.FakeAuditor),
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
