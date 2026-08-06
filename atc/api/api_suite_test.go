package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"code.cloudfoundry.org/clock/fakeclock"
	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/agent/api/dispatcher/dispatchertest"
	experimentsapi "github.com/concourse/concourse/agent/api/experiments"
	"github.com/concourse/concourse/agent/api/feedback"
	"github.com/concourse/concourse/agent/api/metrics/metricstest"
	noderunsapi "github.com/concourse/concourse/agent/api/noderuns"
	nodeupgradesapi "github.com/concourse/concourse/agent/api/nodeupgrades"
	"github.com/concourse/concourse/agent/api/reviews/reviewstest"
	snapshotsapi "github.com/concourse/concourse/agent/api/snapshots"
	"github.com/concourse/concourse/agent/api/ticketjournal"
	"github.com/concourse/concourse/agent/api/tickets/ticketstest"
	workflowoutcomesapi "github.com/concourse/concourse/agent/api/workflowoutcomes"
	workflowoverviewapi "github.com/concourse/concourse/agent/api/workflowoverview"
	workflowrunsapi "github.com/concourse/concourse/agent/api/workflowruns"
	workflowwaitsapi "github.com/concourse/concourse/agent/api/workflowwaits"
	"github.com/concourse/concourse/agent/budget/budgettest"
	"github.com/concourse/concourse/agent/credentials/credentialstest"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflow/workflowtest"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/agent/workflowrun/occurrence"
	"github.com/concourse/concourse/agent/workflowwait/workflowwaittest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/accessor/accessorfakes"
	"github.com/concourse/concourse/atc/api/apifakes"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/containerserver/containerserverfakes"
	"github.com/concourse/concourse/atc/api/policychecker/policycheckerfakes"
	"github.com/concourse/concourse/atc/auditor/auditorfakes"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/credsfakes"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
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

	fakeWorkerPool          *apifakes.FakePool
	fakeVolumeRepository    *dbfakes.FakeVolumeRepository
	dbTeamFactory           *dbfakes.FakeTeamFactory
	dbPipelineFactory       *dbfakes.FakePipelineFactory
	dbJobFactory            *dbfakes.FakeJobFactory
	dbResourceFactory       *dbfakes.FakeResourceFactory
	dbResourceConfigFactory *dbfakes.FakeResourceConfigFactory
	fakePipeline            *dbfakes.FakePipeline
	fakeAccess              *accessorfakes.FakeAccess
	fakeAccessor            *accessorfakes.FakeAccessFactory
	dbWorkerFactory         *dbfakes.FakeWorkerFactory
	dbWorkerTeamFactory     *dbfakes.FakeTeamFactory
	dbWorkerLifecycle       *dbfakes.FakeWorkerLifecycle
	build                   *dbfakes.FakeBuild
	dbBuildFactory          *dbfakes.FakeBuildFactory
	dbUserFactory           *dbfakes.FakeUserFactory
	dbCheckFactory          *dbfakes.FakeCheckFactory
	fakePipelineRunFactory  *dbfakes.FakePipelineRunFactory
	dbTeam                  *dbfakes.FakeTeam
	dbWall                  *dbfakes.FakeWall
	fakeSecretManager       *credsfakes.FakeSecrets
	fakeVarSourcePool       *credsfakes.FakeVarSourcePool
	fakePolicyChecker       *policycheckerfakes.FakePolicyChecker
	credsManagers           creds.Managers
	interceptTimeoutFactory *containerserverfakes.FakeInterceptTimeoutFactory
	interceptTimeout        *containerserverfakes.FakeInterceptTimeout
	isTLSEnabled            bool
	cliDownloadsDir         string
	logger                  *lagertest.TestLogger
	fakeClock               *fakeclock.FakeClock
	dbSigningKeyFactory     *dbfakes.FakeSigningKeyFactory

	fakeAgentRunTranscriptFactory *dbfakes.FakeAgentRunTranscriptFactory
	apiFeedbackStore              *feedback.MemoryStore

	constructedEventHandler *fakeEventHandlerFactory

	apiWorkflowStore *workflowtest.MemoryStore
	apiNodeStore     *workflowtest.MemoryNodeStore

	server *httptest.Server
	client *http.Client
)

type fakeEventHandlerFactory struct {
	build db.BuildForAPI

	lock sync.Mutex
}

type unavailableWorkflowRunBackend struct{}

type allowWorkflowOutcomeAuthorizer struct{}

func (allowWorkflowOutcomeAuthorizer) AuthorizeRun(context.Context, int, string, snapshot.WorkflowRunID) (bool, error) {
	return true, nil
}

func (allowWorkflowOutcomeAuthorizer) AuthorizeOutput(context.Context, int, snapshot.WorkflowRunID, snapshot.SnapshotID) (bool, error) {
	return true, nil
}

func (allowWorkflowOutcomeAuthorizer) AuthorizeModification(
	context.Context,
	int,
	snapshot.WorkflowRunID,
	snapshot.SnapshotID,
	snapshot.SnapshotID,
) (bool, error) {
	return true, nil
}

func (unavailableWorkflowRunBackend) BindAndCreate(context.Context, workflowrun.AdmissionContext, workflowrun.BindRequest) (workflowrun.BindResult, error) {
	return workflowrun.BindResult{}, errors.New("workflow-run backend is unavailable in the API suite")
}

func (unavailableWorkflowRunBackend) Get(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
	return db.AgentWorkflowRun{}, false, nil
}

func (unavailableWorkflowRunBackend) GetKind(context.Context, int, workflow.DefinitionKind, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
	return db.AgentWorkflowRun{}, false, nil
}

func (unavailableWorkflowRunBackend) List(context.Context, db.AgentWorkflowRunListFilter) ([]db.AgentWorkflowRun, error) {
	return nil, nil
}

func (unavailableWorkflowRunBackend) ListKind(context.Context, workflow.DefinitionKind, db.AgentWorkflowRunListFilter) ([]db.AgentWorkflowRun, error) {
	return nil, nil
}

func (unavailableWorkflowRunBackend) CountByStatus(context.Context, db.AgentWorkflowRunCountFilter) (map[db.AgentWorkflowRunStatus]int64, error) {
	return map[db.AgentWorkflowRunStatus]int64{}, nil
}

func (unavailableWorkflowRunBackend) Snapshots(context.Context, snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error) {
	return nil, nil
}

func (unavailableWorkflowRunBackend) Cancel(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
	return db.AgentWorkflowRun{}, false, nil
}

func (unavailableWorkflowRunBackend) GetAuthorized(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
	return snapshot.Snapshot{}, false, nil
}

func (unavailableWorkflowRunBackend) Upgrade(context.Context, workflow.NodeUpgradeRequest) (workflow.NodeUpgradeResult, error) {
	return workflow.NodeUpgradeResult{}, errors.New("node-upgrade backend is unavailable in the API suite")
}

func (unavailableWorkflowRunBackend) OccurrencesForRun(context.Context, db.AgentWorkflowRun) ([]occurrence.NodeOccurrence, error) {
	return nil, nil
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

// apiDBDeps is every db-typed collaborator the API handler is built from. The
// suite's default is all fakes; a converted Describe passes real ones.
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
	pipelineRunFactory    db.PipelineRunFactory
	resourceConfigFactory db.ResourceConfigFactory
	userFactory           db.UserFactory

	wall              db.Wall
	signingKeyFactory db.SigningKeyFactory
	transcripts       db.AgentRunTranscriptFactory
	workflowRuns      db.AgentWorkflowRunsFactory
	experiments       experimentsapi.Store

	// The agent handlers are configured with a trusted team. The suite
	// hard-codes 1, which is safe only while nothing resolves it against a row.
	trustedTeamID   int
	trustedTeamName string
}

func fakeDBDeps() apiDBDeps {
	return apiDBDeps{
		teamFactory: dbTeamFactory, pipelineFactory: dbPipelineFactory,
		jobFactory: dbJobFactory, resourceFactory: dbResourceFactory,
		workerFactory: dbWorkerFactory, workerTeamFactory: dbWorkerTeamFactory,
		volumeRepository: fakeVolumeRepository, buildFactory: dbBuildFactory,
		checkFactory: dbCheckFactory, pipelineRunFactory: fakePipelineRunFactory,
		resourceConfigFactory: dbResourceConfigFactory, userFactory: dbUserFactory,
		wall: dbWall, signingKeyFactory: dbSigningKeyFactory,
		transcripts:   fakeAgentRunTranscriptFactory,
		workflowRuns:  new(dbfakes.FakeAgentWorkflowRunsFactory),
		experiments:   new(dbfakes.FakeAgentExperimentsFactory),
		trustedTeamID: 1, trustedTeamName: atc.DefaultTeamName,
	}
}

// newAPIServer serves the production router over deps. Extracted verbatim
// from the suite's BeforeEach so a single Describe can shadow `server` with
// one built over real db factories, without disturbing the ~950 specs that
// still run against fakes.
func newAPIServer(deps apiDBDeps) *httptest.Server {
	GinkgoHelper()

	checkPipelineAccessHandlerFactory := auth.NewCheckPipelineAccessHandlerFactory(deps.teamFactory)

	checkBuildReadAccessHandlerFactory := auth.NewCheckBuildReadAccessHandlerFactory(deps.buildFactory)

	checkBuildWriteAccessHandlerFactory := auth.NewCheckBuildWriteAccessHandlerFactory(deps.buildFactory)

	checkWorkerTeamAccessHandlerFactory := auth.NewCheckWorkerTeamAccessHandlerFactory(deps.workerFactory)

	fakePolicyChecker = new(policycheckerfakes.FakePolicyChecker)
	fakePolicyChecker.CheckReturns(policy.PassedPolicyCheck(), nil)

	apiWrapper := wrappa.MultiWrappa{
		wrappa.NewPolicyCheckWrappa(logger, fakePolicyChecker),
		wrappa.NewAPIAuthWrappa(
			checkPipelineAccessHandlerFactory,
			checkBuildReadAccessHandlerFactory,
			checkBuildWriteAccessHandlerFactory,
			checkWorkerTeamAccessHandlerFactory,
		),
	}

	snapshotHandlers, err := snapshotsapi.NewHandlerFactory(snapshotsapi.Config{Enabled: false})
	Expect(err).NotTo(HaveOccurred())
	workflowRunBackend := unavailableWorkflowRunBackend{}
	apiWorkflowStore = workflowtest.NewMemoryStore()
	apiNodeStore = workflowtest.NewMemoryNodeStore()
	workflowRunHandlers, err := workflowrunsapi.NewHandler(workflowrunsapi.Config{
		Team:     workflowrunsapi.TrustedTeam{ID: deps.trustedTeamID, Name: deps.trustedTeamName},
		Identity: func(*http.Request) (string, error) { return "api-suite", nil },
		Binder:   workflowRunBackend, Runs: workflowRunBackend,
		Canceler: workflowRunBackend, Manifests: workflowRunBackend,
		Definitions: apiWorkflowStore, Occurrences: workflowRunBackend,
	})
	Expect(err).NotTo(HaveOccurred())
	nodeUpgradeHandlers, err := nodeupgradesapi.NewHandler(nodeupgradesapi.Config{
		TeamID: deps.trustedTeamID, TeamName: deps.trustedTeamName, Store: apiNodeStore, Upgrader: workflowRunBackend,
		Identity: func(*http.Request) (string, error) { return "api-suite", nil },
	})
	Expect(err).NotTo(HaveOccurred())
	nodeRunHandlers, err := noderunsapi.NewHandler(noderunsapi.Config{
		Team:     workflowrunsapi.TrustedTeam{ID: deps.trustedTeamID, Name: deps.trustedTeamName},
		Identity: func(*http.Request) (string, error) { return "api-suite", nil },
		Binder:   workflowRunBackend, Runs: workflowRunBackend,
		Canceler: workflowRunBackend, Manifests: workflowRunBackend,
	})
	Expect(err).NotTo(HaveOccurred())
	workflowOverviewHandlers, err := workflowoverviewapi.NewHandler(workflowoverviewapi.Config{
		Team:        workflowoverviewapi.TrustedTeam{ID: deps.trustedTeamID, Name: deps.trustedTeamName},
		Definitions: apiWorkflowStore, Runs: workflowRunBackend, Occurrences: workflowRunBackend,
	})
	Expect(err).NotTo(HaveOccurred())
	workflowOutcomeHandlers, err := workflowoutcomesapi.NewHandler(workflowoutcomesapi.HandlerConfig{
		TeamID: deps.trustedTeamID, TeamName: deps.trustedTeamName,
		Identity:   func(*http.Request) (string, error) { return "api-suite", nil },
		Store:      workflowoutcomesapi.NewMemoryStore(time.Now),
		Authorizer: allowWorkflowOutcomeAuthorizer{},
	})
	Expect(err).NotTo(HaveOccurred())
	workflowWaitHandlers, err := workflowwaitsapi.NewHandler(workflowwaitsapi.Config{
		Team: workflowwaitsapi.TrustedTeam{ID: deps.trustedTeamID, Name: deps.trustedTeamName},
		Identity: func(*http.Request) (workflowwaitsapi.RequestIdentity, error) {
			return workflowwaitsapi.RequestIdentity{Actor: "subject:sha256:api-suite", DisplayName: "api-suite"}, nil
		},
		Runs: workflowRunBackend, Waits: workflowwaittest.NewMemoryStore(time.Now), Manifests: workflowRunBackend,
	})
	Expect(err).NotTo(HaveOccurred())
	experimentHandlers, err := experimentsapi.NewHandler(experimentsapi.Config{
		TeamID: deps.trustedTeamID, TeamName: deps.trustedTeamName,
		Identity: func(*http.Request) (string, error) { return "api-suite", nil },
		Store:    deps.experiments, RunnerAvailable: true,
	})
	Expect(err).NotTo(HaveOccurred())
	apiTicketStore := ticketstest.NewMemoryStore()
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
		deps.pipelineRunFactory,
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
		interceptTimeoutFactory,
		time.Second,
		deps.wall,
		fakeClock,
		deps.signingKeyFactory,
		nil,
		apiFeedbackStore,
		reviewstest.NewMemoryStore(),
		metricstest.NewMemoryStore(),
		apiTicketStore,
		deps.workflowRuns,
		ticketjournal.TrustedTeam{ID: deps.trustedTeamID},
		credentialstest.NewMemoryBackend(),
		budgettest.NewMemoryLedger(),
		0,
		deps.transcripts,
		apiWorkflowStore,
		apiNodeStore,
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotImplemented) // dispatch handler stub
		}),
		dispatchertest.NewMemoryStore(),
		snapshotHandlers,
		nil, // resource capture disabled with the snapshot service in this suite
		workflowRunHandlers,
		workflowOverviewHandlers,
		nodeRunHandlers,
		nodeUpgradeHandlers,
		workflowWaitHandlers,
		workflowOutcomeHandlers,
		experimentHandlers,
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

	handler = wrappa.LoggerHandler{
		Logger:  logger,
		Handler: accessorHandler,
	}

	return httptest.NewServer(handler)

}

var _ = BeforeEach(func() {
	dbTeamFactory = new(dbfakes.FakeTeamFactory)
	dbWorkerTeamFactory = new(dbfakes.FakeTeamFactory)
	dbPipelineFactory = new(dbfakes.FakePipelineFactory)
	dbJobFactory = new(dbfakes.FakeJobFactory)
	dbResourceFactory = new(dbfakes.FakeResourceFactory)
	dbResourceConfigFactory = new(dbfakes.FakeResourceConfigFactory)
	dbBuildFactory = new(dbfakes.FakeBuildFactory)
	dbUserFactory = new(dbfakes.FakeUserFactory)
	dbCheckFactory = new(dbfakes.FakeCheckFactory)
	fakePipelineRunFactory = new(dbfakes.FakePipelineRunFactory)
	dbWall = new(dbfakes.FakeWall)
	dbSigningKeyFactory = new(dbfakes.FakeSigningKeyFactory)
	fakeAgentRunTranscriptFactory = new(dbfakes.FakeAgentRunTranscriptFactory)
	apiFeedbackStore = feedback.NewMemoryStore()

	interceptTimeoutFactory = new(containerserverfakes.FakeInterceptTimeoutFactory)
	interceptTimeout = new(containerserverfakes.FakeInterceptTimeout)
	interceptTimeoutFactory.NewInterceptTimeoutReturns(interceptTimeout)

	dbTeam = new(dbfakes.FakeTeam)
	dbTeam.IDReturns(734)
	dbTeam.NameReturns("some-team")
	dbTeamFactory.FindTeamReturns(dbTeam, true, nil)
	dbTeamFactory.GetByIDReturns(dbTeam)
	dbWorkerTeamFactory.FindTeamReturns(dbTeam, true, nil)
	dbWorkerTeamFactory.GetByIDReturns(dbTeam)

	fakeAccess = new(accessorfakes.FakeAccess)
	fakeAccessor = new(accessorfakes.FakeAccessFactory)
	fakeAccessor.CreateReturns(fakeAccess, nil)

	fakePipeline = new(dbfakes.FakePipeline)
	dbTeam.PipelineReturns(fakePipeline, true, nil)

	dbWorkerFactory = new(dbfakes.FakeWorkerFactory)
	dbWorkerLifecycle = new(dbfakes.FakeWorkerLifecycle)

	fakeWorkerPool = new(apifakes.FakePool)

	fakeVolumeRepository = new(dbfakes.FakeVolumeRepository)
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

	build = new(dbfakes.FakeBuild)

	server = newAPIServer(fakeDBDeps())

	client = &http.Client{
		Transport: &http.Transport{},
	}
})

var _ = AfterEach(func() {
	os.Remove(cliDownloadsDir)
	server.Close()
})

func TestAPI(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "API Suite")
}
