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

	dispatcherapi "github.com/concourse/concourse/agent/api/dispatcher"
	experimentsapi "github.com/concourse/concourse/agent/api/experiments"
	"github.com/concourse/concourse/agent/api/feedback"
	"github.com/concourse/concourse/agent/api/metrics"
	"github.com/concourse/concourse/agent/api/principals"
	"github.com/concourse/concourse/agent/api/reviews"
	snapshotsapi "github.com/concourse/concourse/agent/api/snapshots"
	"github.com/concourse/concourse/agent/api/tickets"
	workflowoutcomesapi "github.com/concourse/concourse/agent/api/workflowoutcomes"
	workflowrunsapi "github.com/concourse/concourse/agent/api/workflowruns"
	workflowwaitsapi "github.com/concourse/concourse/agent/api/workflowwaits"
	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/credentials"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/agent/workflowwait"
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

	constructedEventHandler *fakeEventHandlerFactory

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

func (unavailableWorkflowRunBackend) List(context.Context, db.AgentWorkflowRunListFilter) ([]db.AgentWorkflowRun, error) {
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

	interceptTimeoutFactory = new(containerserverfakes.FakeInterceptTimeoutFactory)
	interceptTimeout = new(containerserverfakes.FakeInterceptTimeout)
	interceptTimeoutFactory.NewInterceptTimeoutReturns(interceptTimeout)

	dbTeam = new(dbfakes.FakeTeam)
	dbTeam.IDReturns(734)
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

	checkPipelineAccessHandlerFactory := auth.NewCheckPipelineAccessHandlerFactory(dbTeamFactory)

	checkBuildReadAccessHandlerFactory := auth.NewCheckBuildReadAccessHandlerFactory(dbBuildFactory)

	checkBuildWriteAccessHandlerFactory := auth.NewCheckBuildWriteAccessHandlerFactory(dbBuildFactory)

	checkWorkerTeamAccessHandlerFactory := auth.NewCheckWorkerTeamAccessHandlerFactory(dbWorkerFactory)

	fakePolicyChecker = new(policycheckerfakes.FakePolicyChecker)
	fakePolicyChecker.CheckReturns(policy.PassedPolicyCheck(), nil)

	principalsStore := principals.NewMemoryStore()
	checkAgentPrincipalHandlerFactory := auth.NewCheckAgentPrincipalHandlerFactory(principals.NewVerifier(principalsStore))

	apiWrapper := wrappa.MultiWrappa{
		wrappa.NewPolicyCheckWrappa(logger, fakePolicyChecker),
		wrappa.NewAPIAuthWrappa(
			checkPipelineAccessHandlerFactory,
			checkBuildReadAccessHandlerFactory,
			checkBuildWriteAccessHandlerFactory,
			checkWorkerTeamAccessHandlerFactory,
			checkAgentPrincipalHandlerFactory,
		),
	}

	snapshotHandlers, err := snapshotsapi.NewHandlerFactory(snapshotsapi.Config{Enabled: false})
	Expect(err).NotTo(HaveOccurred())
	workflowRunBackend := unavailableWorkflowRunBackend{}
	workflowRunHandlers, err := workflowrunsapi.NewHandler(workflowrunsapi.Config{
		Team:     workflowrunsapi.TrustedTeam{ID: 1, Name: atc.DefaultTeamName},
		Identity: func(*http.Request) (string, error) { return "api-suite", nil },
		Binder:   workflowRunBackend, Runs: workflowRunBackend,
		Canceler: workflowRunBackend, Manifests: workflowRunBackend,
	})
	Expect(err).NotTo(HaveOccurred())
	workflowOutcomeHandlers, err := workflowoutcomesapi.NewHandler(workflowoutcomesapi.HandlerConfig{
		TeamID: 1, TeamName: atc.DefaultTeamName,
		Identity:   func(*http.Request) (string, error) { return "api-suite", nil },
		Store:      workflowoutcomesapi.NewMemoryStore(time.Now),
		Authorizer: allowWorkflowOutcomeAuthorizer{},
	})
	Expect(err).NotTo(HaveOccurred())
	workflowWaitHandlers, err := workflowwaitsapi.NewHandler(workflowwaitsapi.Config{
		Team: workflowwaitsapi.TrustedTeam{ID: 1, Name: atc.DefaultTeamName},
		Identity: func(*http.Request) (workflowwaitsapi.RequestIdentity, error) {
			return workflowwaitsapi.RequestIdentity{Actor: "subject:sha256:api-suite", DisplayName: "api-suite"}, nil
		},
		Runs: workflowRunBackend, Waits: workflowwait.NewMemoryStore(time.Now), Manifests: workflowRunBackend,
	})
	Expect(err).NotTo(HaveOccurred())
	experimentHandlers, err := experimentsapi.NewHandler(experimentsapi.Config{
		TeamID: 1, TeamName: atc.DefaultTeamName,
		Identity: func(*http.Request) (string, error) { return "api-suite", nil },
		Store:    new(dbfakes.FakeAgentExperimentsFactory), RunnerAvailable: true,
	})
	Expect(err).NotTo(HaveOccurred())
	handler, err := api.NewHandler(
		logger,

		externalURL,
		"",
		clusterName,

		apiWrapper,

		dbTeamFactory,
		dbPipelineFactory,
		dbJobFactory,
		dbResourceFactory,
		dbWorkerFactory,
		dbWorkerTeamFactory,
		fakeVolumeRepository,
		dbBuildFactory,
		dbCheckFactory,
		fakePipelineRunFactory,
		dbResourceConfigFactory,
		dbUserFactory,

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
		dbWall,
		fakeClock,
		dbSigningKeyFactory,
		nil,
		feedback.NewMemoryStore(),
		reviews.NewMemoryStore(),
		metrics.NewMemoryStore(),
		tickets.NewMemoryStore(),
		principalsStore,
		credentials.NewMemoryBackend(),
		// lags the suite's "1.2.3" version param — the platform-info
		// endpoint must report the skew (#45)
		"registry.example/agent-runner:v1.2.2",
		budget.NewMemoryLedger(),
		0,
		budget.NoTicketBudgets{},
		workflow.NewMemoryStore(),
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotImplemented) // dispatch handler stub
		}),
		dispatcherapi.NewMemoryStore(),
		false, // agent dispatcher boot default (flag off)
		snapshotHandlers,
		nil, // resource capture disabled with the snapshot service in this suite
		workflowRunHandlers,
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

	server = httptest.NewServer(handler)

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
