package api_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
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
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/containerserver"
	"github.com/concourse/concourse/atc/api/hangarserver"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/noop"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/atc/wrappa"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	externalURL = "https://example.com"
	clusterName = "Test Cluster"

	workerRuntime         *apiWorkerRuntime
	scriptedAccess        *apiAccess
	scriptedAccessFactory *staticAccessFactory
	secretManager         creds.Secrets
	varSourcePool         creds.VarSourcePool
	credsManagers         creds.Managers
	interceptTimeout      *observingInterceptTimeout
	isTLSEnabled          bool
	cliDownloadsDir       string
	logger                *lagertest.TestLogger
	fakeClock             *fakeclock.FakeClock

	constructedEventHandler *fakeEventHandlerFactory

	server *httptest.Server
	client *http.Client
)

// apiAccess is the accessor's answer, set per spec: whether the caller is
// authenticated, which teams it is authorized for, whether it is an admin or
// the system, and what it says about itself.
//
// The handler specs are about what a handler does with that answer; how the
// answer is computed from a token and the teams table is the accessor's own
// contract, pinned by atc/api/accessor's suite over real teams. Several
// hundred specs across this package set the answer, most of them over handlers
// built with no database at all, so the answer is a value here rather than a
// claim set and a team row.
type apiAccess struct {
	authenticated bool
	authorized    bool
	authorize     func(team string) bool
	admin         bool
	system        bool
	teamNames     []string
	userInfo      atc.UserInfo

	mu            sync.Mutex
	authorizedFor []string
}

func (a *apiAccess) HasToken() bool                 { return a.authenticated }
func (a *apiAccess) IsAuthenticated() bool          { return a.authenticated }
func (a *apiAccess) IsAdmin() bool                  { return a.admin }
func (a *apiAccess) IsSystem() bool                 { return a.system }
func (a *apiAccess) TeamNames() []string            { return a.teamNames }
func (a *apiAccess) UserInfo() atc.UserInfo         { return a.userInfo }
func (a *apiAccess) TeamRoles() map[string][]string { return nil }
func (a *apiAccess) Claims() accessor.Claims        { return accessor.Claims{} }

func (a *apiAccess) IsAuthorized(team string) bool {
	a.mu.Lock()
	a.authorizedFor = append(a.authorizedFor, team)
	a.mu.Unlock()
	if a.authorize != nil {
		return a.authorize(team)
	}
	return a.authorized
}

// authorizedTeams is every team a handler asked about, in order.
func (a *apiAccess) authorizedTeams() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.authorizedFor)
}

// staticAccessFactory hands every request the same access.
type staticAccessFactory struct {
	access accessor.Access
}

func (f *staticAccessFactory) Create(*http.Request, string) (accessor.Access, error) {
	return f.access, nil
}

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

	scriptedAccess = new(apiAccess)
	scriptedAccessFactory = &staticAccessFactory{access: scriptedAccess}

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
	volumeRepository      db.VolumeRepository
	buildFactory          db.BuildFactory
	checkFactory          db.CheckFactory
	resourceConfigFactory db.ResourceConfigFactory
	userFactory           db.UserFactory
	landingQueueFactory   db.LandingQueueFactory

	wall db.Wall

	hangarStatus hangarserver.Source
}

// auditedAction is the action the accessor handler labels every request with.
// Production labels each route with its own action; here one handler fronts
// the whole API.
const auditedAction = atc.GetInfo

// newAPIServer builds the full API handler over deps and serves it. The
// accessor's answer and the event handler stay scripted: neither is a
// database seam.
func newAPIServer(deps apiDBDeps) *httptest.Server {
	GinkgoHelper()

	checkPipelineAccessHandlerFactory := auth.NewCheckPipelineAccessHandlerFactory(deps.teamFactory)

	checkBuildReadAccessHandlerFactory := auth.NewCheckBuildReadAccessHandlerFactory(deps.buildFactory)

	checkBuildWriteAccessHandlerFactory := auth.NewCheckBuildWriteAccessHandlerFactory(deps.buildFactory)

	apiWrapper := wrappa.MultiWrappa{
		wrappa.NewAPIAuthWrappa(
			checkPipelineAccessHandlerFactory,
			checkBuildReadAccessHandlerFactory,
			checkBuildWriteAccessHandlerFactory,
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
		clusterName,

		apiWrapper,

		deps.teamFactory,
		deps.pipelineFactory,
		deps.pipelineRunFactory,
		deps.jobFactory,
		deps.resourceFactory,
		deps.workerFactory,
		deps.buildFactory,
		deps.checkFactory,
		deps.resourceConfigFactory,
		deps.userFactory,
		deps.landingQueueFactory,

		constructedEventHandler.Construct,

		workerPool,

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
		nil,
		deps.hangarStatus,
	)

	Expect(err).NotTo(HaveOccurred())

	accessorHandler := accessor.NewHandler(
		logger,
		auditedAction,
		handler,
		scriptedAccessFactory,
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

func cloneAPIPage(page db.Page) db.Page {
	cloned := page
	if page.From != nil {
		cloned.From = db.NewIntPtr(*page.From)
	}
	if page.To != nil {
		cloned.To = db.NewIntPtr(*page.To)
	}
	return cloned
}

func findSuppliedTeam(factory db.TeamFactory, teamName string, team db.Team, name string) (db.Team, bool, error) {
	if name == teamName {
		return team, true, nil
	}
	return factory.FindTeam(name)
}
