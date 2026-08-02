package api

import (
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"code.cloudfoundry.org/clock"
	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/agent/api/costs"
	dispatcherapi "github.com/concourse/concourse/agent/api/dispatcher"
	experimentsapi "github.com/concourse/concourse/agent/api/experiments"
	"github.com/concourse/concourse/agent/api/feedback"
	metricsapi "github.com/concourse/concourse/agent/api/metrics"
	noderunsapi "github.com/concourse/concourse/agent/api/noderuns"
	nodesapi "github.com/concourse/concourse/agent/api/nodes"
	nodeupgradesapi "github.com/concourse/concourse/agent/api/nodeupgrades"
	reviewsapi "github.com/concourse/concourse/agent/api/reviews"
	snapshotsapi "github.com/concourse/concourse/agent/api/snapshots"
	ticketjournalapi "github.com/concourse/concourse/agent/api/ticketjournal"
	ticketsapi "github.com/concourse/concourse/agent/api/tickets"
	workflowoutcomesapi "github.com/concourse/concourse/agent/api/workflowoutcomes"
	workflowoverviewapi "github.com/concourse/concourse/agent/api/workflowoverview"
	workflowrunsapi "github.com/concourse/concourse/agent/api/workflowruns"
	workflowsapi "github.com/concourse/concourse/agent/api/workflows"
	workflowwaitsapi "github.com/concourse/concourse/agent/api/workflowwaits"
	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/credentials"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/agentchildexecutions"
	"github.com/concourse/concourse/atc/api/artifactserver"
	"github.com/concourse/concourse/atc/api/buildserver"
	"github.com/concourse/concourse/atc/api/ccserver"
	"github.com/concourse/concourse/atc/api/cliserver"
	"github.com/concourse/concourse/atc/api/configserver"
	"github.com/concourse/concourse/atc/api/containerserver"
	"github.com/concourse/concourse/atc/api/idtokenserver"
	"github.com/concourse/concourse/atc/api/infoserver"
	"github.com/concourse/concourse/atc/api/jobserver"
	"github.com/concourse/concourse/atc/api/loglevelserver"
	"github.com/concourse/concourse/atc/api/mcpserver"
	"github.com/concourse/concourse/atc/api/pipelineserver"
	"github.com/concourse/concourse/atc/api/resourceserver"
	"github.com/concourse/concourse/atc/api/resourceserver/versionserver"
	"github.com/concourse/concourse/atc/api/runserver"
	"github.com/concourse/concourse/atc/api/teamserver"
	"github.com/concourse/concourse/atc/api/transcriptserver"
	"github.com/concourse/concourse/atc/api/usersserver"
	"github.com/concourse/concourse/atc/api/volumeserver"
	"github.com/concourse/concourse/atc/api/wallserver"
	"github.com/concourse/concourse/atc/api/workerserver"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/mainredirect"
	"github.com/concourse/concourse/atc/wrappa"
	"github.com/tedsuo/rata"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

//counterfeiter:generate . Pool
type Pool interface {
	artifactserver.Pool
	containerserver.Pool
}

// AgentChildExecutionHandlers is optional while the broker is disabled. When
// supplied, authority endpoints remain execution-capability authenticated by
// their own handler and inspection is scoped through ATC's ordinary team
// wrapper.
type AgentChildExecutionHandlers struct {
	Authority http.Handler
	Store     agentchildexecutions.ExecutionStore
}

func NewHandler(
	logger lager.Logger,

	externalURL string,
	oidcIssuer string,
	clusterName string,

	wrapper wrappa.Wrappa,

	dbTeamFactory db.TeamFactory,
	dbPipelineFactory db.PipelineFactory,
	dbJobFactory db.JobFactory,
	dbResourceFactory db.ResourceFactory,
	dbWorkerFactory db.WorkerFactory,
	workerTeamFactory db.TeamFactory,
	volumeRepository db.VolumeRepository,
	dbBuildFactory db.BuildFactory,
	dbCheckFactory db.CheckFactory,
	dbPipelineRunFactory db.PipelineRunFactory,
	dbResourceConfigFactory db.ResourceConfigFactory,
	dbUserFactory db.UserFactory,

	eventHandlerFactory buildserver.EventHandlerFactory,

	workerPool Pool,

	sink *lager.ReconfigurableSink,

	isTLSEnabled bool,

	cliDownloadsDir string,
	version string,
	workerVersion string,
	jetBridgeVersion string,
	concourseVersion string,
	secretManager creds.Secrets,
	varSourcePool creds.VarSourcePool,
	credsManagers creds.Managers,
	interceptTimeoutFactory containerserver.InterceptTimeoutFactory,
	interceptUpdateInterval time.Duration,
	dbWall db.Wall,
	clock clock.Clock,
	dbSigningKeyFactory db.SigningKeyFactory,
	dbPinger infoserver.DBPinger,
	feedbackStore feedback.Store,
	reviewsStore reviewsapi.Store,
	metricsStore metricsapi.Store,
	ticketsStore ticketsapi.Store,
	// ticketRunJournal serves the cross-workflow ticket journal. A ticket may
	// drive many runs across many workflows, so the journal reads the durable
	// runs directly rather than anything stored on the ticket.
	ticketRunJournal ticketjournalapi.RunStore,
	ticketJournalTeam ticketjournalapi.TrustedTeam,
	credentialsBackend credentials.Backend,
	costLedger budget.Ledger,
	agentDailyBudgetUSD float64,
	// agentRunTranscriptStore backs GetAgentWorkflowRunTranscript: the raw
	// tool-call transcript the agent step persisted during flight ingestion.
	agentRunTranscriptStore transcriptserver.Store,
	workflowStore workflow.Store,
	nodeStore workflow.NodeStore,
	// agentDispatchHandler serves DispatchAgentTicket (built in
	// atccmd/command.go from dispatch.Deps; a stub in the test suite).
	agentDispatchHandler http.Handler,
	// agentSettingsStore backs the dispatcher runtime-control routes
	// (Get/SetAgentDispatcher). The seeded agent_settings row is the only
	// authority on the dispatcher mode — there is no boot flag behind it.
	agentSettingsStore dispatcherapi.Store,
	snapshotHandlers *snapshotsapi.HandlerFactory,
	resourceCapturer snapshotsapi.ResourceCapturer,
	workflowRunHandlers *workflowrunsapi.Handler,
	workflowOverviewHandlers *workflowoverviewapi.Handler,
	nodeRunHandlers *noderunsapi.Handler,
	nodeUpgradeHandlers *nodeupgradesapi.Handler,
	workflowWaitHandlers *workflowwaitsapi.Handler,
	workflowOutcomeHandlers *workflowoutcomesapi.Handler,
	experimentHandlers *experimentsapi.Handler,
	agentChildHandlers ...AgentChildExecutionHandlers,
) (http.Handler, error) {
	if workflowRunHandlers == nil {
		return nil, fmt.Errorf("workflow-run API handlers are required")
	}
	if workflowOverviewHandlers == nil {
		return nil, fmt.Errorf("workflow-overview API handlers are required")
	}
	if nodeRunHandlers == nil {
		return nil, fmt.Errorf("node-run API handlers are required")
	}
	if nodeUpgradeHandlers == nil {
		return nil, fmt.Errorf("node-upgrade API handlers are required")
	}
	if workflowOutcomeHandlers == nil {
		return nil, fmt.Errorf("workflow-outcome API handlers are required")
	}
	if workflowWaitHandlers == nil {
		return nil, fmt.Errorf("workflow-wait API handlers are required")
	}
	if experimentHandlers == nil {
		return nil, fmt.Errorf("experiment API handlers are required")
	}
	if len(agentChildHandlers) > 1 {
		return nil, fmt.Errorf("at most one agent child execution handler set is allowed")
	}

	absCLIDownloadsDir, err := filepath.Abs(cliDownloadsDir)
	if err != nil {
		return nil, err
	}

	pipelineHandlerFactory := pipelineserver.NewScopedHandlerFactory(dbTeamFactory)
	buildHandlerFactory := buildserver.NewScopedHandlerFactory(logger)
	teamHandlerFactory := NewTeamScopedHandlerFactory(logger, dbTeamFactory)

	buildServer := buildserver.NewServer(logger, externalURL, dbTeamFactory, dbBuildFactory, eventHandlerFactory)
	jobServer := jobserver.NewServer(logger, externalURL, secretManager, dbJobFactory, dbCheckFactory)
	resourceServer := resourceserver.NewServer(logger, secretManager, varSourcePool, dbCheckFactory, dbResourceFactory, dbResourceConfigFactory)

	versionServer := versionserver.NewServer(logger, externalURL)
	pipelineServer := pipelineserver.NewServer(logger, dbTeamFactory, dbPipelineFactory, externalURL)
	runServer := runserver.NewServer(logger, dbPipelineRunFactory)
	configServer := configserver.NewServer(logger, dbTeamFactory, secretManager)
	ccServer := ccserver.NewServer(logger, dbTeamFactory, externalURL)
	workerServer := workerserver.NewServer(logger, workerTeamFactory, dbWorkerFactory)
	logLevelServer := loglevelserver.NewServer(logger, sink)
	cliServer := cliserver.NewServer(logger, absCLIDownloadsDir)
	containerServer := containerserver.NewServer(logger, workerPool, interceptTimeoutFactory, interceptUpdateInterval, clock)
	volumesServer := volumeserver.NewServer(logger, volumeRepository)
	teamServer := teamserver.NewServer(logger, dbTeamFactory, externalURL)
	infoServer := infoserver.NewServer(logger, version, workerVersion, externalURL, clusterName, credsManagers, jetBridgeVersion, concourseVersion, dbPinger, dbWorkerFactory)
	artifactServer := artifactserver.NewServer(logger, workerPool)
	usersServer := usersserver.NewServer(logger, dbUserFactory)
	wallServer := wallserver.NewServer(dbWall, logger)
	feedbackServer := feedback.NewHandler(
		feedbackStore,
		feedback.WithSnapshotTeam(atc.DefaultTeamName),
		feedback.WithIdentity(func(r *http.Request) string {
			claims := accessor.GetAccessor(r).Claims()
			if claims.PreferredUsername != "" {
				return claims.PreferredUsername
			}
			return claims.UserName
		}),
	)
	reviewsServer := reviewsapi.NewHandler(reviewsStore, feedbackStore, atc.DefaultTeamName)
	metricsServer := metricsapi.NewHandler(metricsStore)
	ticketsServer := ticketsapi.NewHandler(ticketsStore, func(r *http.Request) string {
		return accessor.GetAccessor(r).Claims().UserName
	})
	ticketJournalServer := ticketjournalapi.NewHandler(ticketsStore, ticketRunJournal, ticketJournalTeam)
	transcriptServer := transcriptserver.NewServer(logger, agentRunTranscriptStore)
	workflowsServer := workflowsapi.NewHandler(workflowStore, metricsStore)
	nodesServer := nodesapi.NewHandler(nodeStore)
	dispatcherServer := dispatcherapi.NewHandler(
		agentSettingsStore,
		func(r *http.Request) string {
			return accessor.GetAccessor(r).Claims().UserName
		},
	)
	snapshotTeamHandler := func(handler func(snapshotsapi.TrustedTeam) http.Handler) func(db.Team) http.Handler {
		return func(team db.Team) http.Handler {
			return handler(snapshotsapi.TrustedTeam{ID: team.ID(), Name: team.Name()})
		}
	}
	credentialsServer := credentials.NewHandler(credentialsBackend, func(r *http.Request) (string, string, bool, bool) {
		acc := accessor.GetAccessor(r)
		claims := acc.Claims()
		name := claims.PreferredUsername
		if name == "" {
			name = claims.UserName
		}
		return claims.Sub, name, acc.IsAdmin(), claims.Sub != ""
	})
	costChecker := budget.NewChecker(costLedger, budget.Config{
		GlobalDailyCapUSD: agentDailyBudgetUSD,
	})
	costsServer := costs.NewHandler(costLedger, costChecker)
	if oidcIssuer == "" {
		oidcIssuer = externalURL
	}
	idTokenServer := idtokenserver.NewServer(logger, oidcIssuer, dbSigningKeyFactory)

	mcpServer := mcpserver.NewServer()
	mcpserver.RegisterTools(mcpServer, dbTeamFactory, dbBuildFactory, workflowStore, costLedger, dbPipelineRunFactory, externalURL, version)
	authorityHandler := http.NotFoundHandler()
	inspectionHandler := http.NotFoundHandler()
	if len(agentChildHandlers) == 1 {
		configured := agentChildHandlers[0]
		if configured.Authority != nil && configured.Store != nil {
			authorityHandler = configured.Authority
			inspectionHandler = teamHandlerFactory.HandlerFor(agentchildexecutions.NewTeamInspectionHandlerFactory(configured.Store))
		}
	}

	handlers := map[string]http.Handler{
		atc.GetConfig:  http.HandlerFunc(configServer.GetConfig),
		atc.SaveConfig: http.HandlerFunc(configServer.SaveConfig),

		atc.GetCC: http.HandlerFunc(ccServer.GetCC),

		atc.ListBuilds:          http.HandlerFunc(buildServer.ListBuilds),
		atc.CreateBuild:         teamHandlerFactory.HandlerFor(buildServer.CreateBuild),
		atc.GetBuild:            buildHandlerFactory.HandlerFor(buildServer.GetBuild),
		atc.BuildResources:      buildHandlerFactory.HandlerFor(buildServer.BuildResources),
		atc.AbortBuild:          buildHandlerFactory.HandlerFor(buildServer.AbortBuild),
		atc.GetBuildPlan:        buildHandlerFactory.HandlerFor(buildServer.GetBuildPlan),
		atc.GetBuildPreparation: buildHandlerFactory.HandlerFor(buildServer.GetBuildPreparation),
		atc.BuildEvents:         buildHandlerFactory.HandlerFor(buildServer.BuildEvents),
		atc.ListBuildArtifacts:  buildHandlerFactory.HandlerFor(buildServer.GetBuildArtifacts),
		atc.SetBuildComment:     buildHandlerFactory.HandlerFor(buildServer.SetBuildComment),

		atc.ListAllJobs:    http.HandlerFunc(jobServer.ListAllJobs),
		atc.ListJobs:       pipelineHandlerFactory.HandlerFor(jobServer.ListJobs),
		atc.GetJob:         pipelineHandlerFactory.HandlerFor(jobServer.GetJob),
		atc.ListJobBuilds:  pipelineHandlerFactory.HandlerFor(jobServer.ListJobBuilds),
		atc.ListJobInputs:  pipelineHandlerFactory.HandlerFor(jobServer.ListJobInputs),
		atc.GetJobBuild:    pipelineHandlerFactory.HandlerFor(jobServer.GetJobBuild),
		atc.CreateJobBuild: pipelineHandlerFactory.HandlerFor(jobServer.CreateJobBuild),
		atc.RerunJobBuild:  pipelineHandlerFactory.HandlerFor(jobServer.RerunJobBuild),
		atc.PauseJob:       pipelineHandlerFactory.HandlerFor(jobServer.PauseJob),
		atc.UnpauseJob:     pipelineHandlerFactory.HandlerFor(jobServer.UnpauseJob),
		atc.ScheduleJob:    pipelineHandlerFactory.HandlerFor(jobServer.ScheduleJob),
		atc.JobBadge:       pipelineHandlerFactory.HandlerFor(jobServer.JobBadge),
		atc.MainJobBadge: mainredirect.Handler{
			Routes: atc.Routes,
			Route:  atc.JobBadge,
		},

		atc.ClearTaskCache: pipelineHandlerFactory.HandlerFor(jobServer.ClearTaskCache),

		atc.ListAllPipelines:          http.HandlerFunc(pipelineServer.ListAllPipelines),
		atc.ListPipelines:             http.HandlerFunc(pipelineServer.ListPipelines),
		atc.GetPipeline:               pipelineHandlerFactory.HandlerFor(pipelineServer.GetPipeline),
		atc.DeletePipeline:            pipelineHandlerFactory.HandlerFor(pipelineServer.DeletePipeline),
		atc.OrderPipelines:            teamHandlerFactory.HandlerFor(pipelineServer.OrderPipelines),
		atc.OrderPipelinesWithinGroup: teamHandlerFactory.HandlerFor(pipelineServer.OrderPipelinesWithinGroup),
		atc.PausePipeline:             pipelineHandlerFactory.HandlerFor(pipelineServer.PausePipeline),
		atc.ArchivePipeline:           pipelineHandlerFactory.HandlerFor(pipelineServer.ArchivePipeline),
		atc.UnpausePipeline:           pipelineHandlerFactory.HandlerFor(pipelineServer.UnpausePipeline),
		atc.ExposePipeline:            pipelineHandlerFactory.HandlerFor(pipelineServer.ExposePipeline),
		atc.HidePipeline:              pipelineHandlerFactory.HandlerFor(pipelineServer.HidePipeline),
		atc.GetVersionsDB:             pipelineHandlerFactory.HandlerFor(pipelineServer.GetVersionsDB),
		atc.RenamePipeline:            teamHandlerFactory.HandlerFor(pipelineServer.RenamePipeline),
		atc.ListPipelineBuilds:        pipelineHandlerFactory.HandlerFor(pipelineServer.ListPipelineBuilds),
		atc.CreatePipelineBuild:       pipelineHandlerFactory.HandlerFor(pipelineServer.CreateBuild),
		atc.PipelineBadge:             pipelineHandlerFactory.HandlerFor(pipelineServer.PipelineBadge),

		atc.CreatePipelineRun: pipelineHandlerFactory.HandlerFor(runServer.CreateRun),
		atc.ListPipelineRuns:  pipelineHandlerFactory.HandlerFor(runServer.ListRuns),
		atc.GetPipelineRun:    pipelineHandlerFactory.HandlerFor(runServer.GetRun),

		atc.ListAllResources:          http.HandlerFunc(resourceServer.ListAllResources),
		atc.ListSharedForResource:     pipelineHandlerFactory.HandlerFor(resourceServer.ListSharedForResource),
		atc.ListSharedForResourceType: pipelineHandlerFactory.HandlerFor(resourceServer.ListSharedForResourceType),
		atc.ListResources:             pipelineHandlerFactory.HandlerFor(resourceServer.ListResources),
		atc.ListResourceTypes:         pipelineHandlerFactory.HandlerFor(resourceServer.ListResourceTypes),
		atc.GetResource:               pipelineHandlerFactory.HandlerFor(resourceServer.GetResource),
		atc.UnpinResource:             pipelineHandlerFactory.HandlerFor(resourceServer.UnpinResource),
		atc.SetPinCommentOnResource:   pipelineHandlerFactory.HandlerFor(resourceServer.SetPinCommentOnResource),
		atc.CheckResource:             pipelineHandlerFactory.HandlerFor(resourceServer.CheckResource),
		atc.CheckResourceWebHook:      pipelineHandlerFactory.HandlerFor(resourceServer.CheckResourceWebHook),
		atc.CheckResourceType:         pipelineHandlerFactory.HandlerFor(resourceServer.CheckResourceType),
		atc.CheckPrototype:            pipelineHandlerFactory.HandlerFor(resourceServer.CheckPrototype),
		atc.ClearResourceCache:        pipelineHandlerFactory.HandlerFor(resourceServer.ClearResourceCache),

		atc.ListResourceVersions:           pipelineHandlerFactory.HandlerFor(versionServer.ListResourceVersions),
		atc.ClearResourceVersions:          pipelineHandlerFactory.HandlerFor(versionServer.ClearResourceVersions),
		atc.CopyResourceVersions:           pipelineHandlerFactory.HandlerFor(versionServer.CopyResourceVersions),
		atc.ListDeprecatedScopes:           pipelineHandlerFactory.HandlerFor(versionServer.ListDeprecatedScopes),
		atc.ClearResourceTypeVersions:      pipelineHandlerFactory.HandlerFor(versionServer.ClearResourceTypeVersions),
		atc.GetResourceVersion:             pipelineHandlerFactory.HandlerFor(versionServer.GetResourceVersion),
		atc.EnableResourceVersion:          pipelineHandlerFactory.HandlerFor(versionServer.EnableResourceVersion),
		atc.DisableResourceVersion:         pipelineHandlerFactory.HandlerFor(versionServer.DisableResourceVersion),
		atc.PinResourceVersion:             pipelineHandlerFactory.HandlerFor(versionServer.PinResourceVersion),
		atc.ListBuildsWithVersionAsInput:   pipelineHandlerFactory.HandlerFor(versionServer.ListBuildsWithVersionAsInput),
		atc.ListBuildsWithVersionAsOutput:  pipelineHandlerFactory.HandlerFor(versionServer.ListBuildsWithVersionAsOutput),
		atc.GetDownstreamResourceCausality: pipelineHandlerFactory.HandlerFor(versionServer.GetDownstreamResourceCausality),
		atc.GetUpstreamResourceCausality:   pipelineHandlerFactory.HandlerFor(versionServer.GetUpstreamResourceCausality),

		atc.ListWorkers:    http.HandlerFunc(workerServer.ListWorkers),
		atc.RegisterWorker: http.HandlerFunc(workerServer.RegisterWorker),
		atc.DeleteWorker:   http.HandlerFunc(workerServer.DeleteWorker),

		atc.SetLogLevel: http.HandlerFunc(logLevelServer.SetMinLevel),
		atc.GetLogLevel: http.HandlerFunc(logLevelServer.GetMinLevel),

		atc.DownloadCLI:  http.HandlerFunc(cliServer.Download),
		atc.GetInfo:      http.HandlerFunc(infoServer.Info),
		atc.GetInfoCreds: http.HandlerFunc(infoServer.Creds),
		atc.GetHealth:    http.HandlerFunc(infoServer.Health),

		atc.GetUser:              http.HandlerFunc(usersServer.GetUser),
		atc.ListActiveUsersSince: http.HandlerFunc(usersServer.GetUsersSince),

		atc.ListContainers:  teamHandlerFactory.HandlerFor(containerServer.ListContainers),
		atc.GetContainer:    teamHandlerFactory.HandlerFor(containerServer.GetContainer),
		atc.HijackContainer: teamHandlerFactory.HandlerFor(containerServer.HijackContainer),

		atc.ListVolumes: teamHandlerFactory.HandlerFor(volumesServer.ListVolumes),

		atc.ListTeams:      http.HandlerFunc(teamServer.ListTeams),
		atc.GetTeam:        teamHandlerFactory.HandlerFor(teamServer.GetTeam),
		atc.SetTeam:        http.HandlerFunc(teamServer.SetTeam),
		atc.RenameTeam:     teamHandlerFactory.HandlerFor(teamServer.RenameTeam),
		atc.DestroyTeam:    teamHandlerFactory.HandlerFor(teamServer.DestroyTeam),
		atc.ListTeamBuilds: teamHandlerFactory.HandlerFor(teamServer.ListTeamBuilds),

		atc.CreateArtifact: teamHandlerFactory.HandlerFor(artifactServer.CreateArtifact),
		atc.GetArtifact:    teamHandlerFactory.HandlerFor(artifactServer.GetArtifact),

		atc.GetWall:   http.HandlerFunc(wallServer.GetWall),
		atc.SetWall:   http.HandlerFunc(wallServer.SetWall),
		atc.ClearWall: http.HandlerFunc(wallServer.ClearWall),

		atc.MCPEndpoint: mcpServer,

		atc.GetOpenIDConfiguration: http.HandlerFunc(idTokenServer.OpenIDConfiguration),
		atc.GetSigningKeys:         http.HandlerFunc(idTokenServer.SigningKeys),

		atc.SubmitAgentFeedback: http.HandlerFunc(feedbackServer.SubmitFeedback),

		atc.GetBuildAgentReviews:        http.HandlerFunc(reviewsServer.GetByBuild),
		atc.ListTeamAgentReviews:        http.HandlerFunc(reviewsServer.ListByTeam),
		atc.GetAgentSnapshotReview:      http.HandlerFunc(reviewsServer.GetBySnapshot),
		atc.ListAgentWorkflowRunReviews: http.HandlerFunc(reviewsServer.ListByWorkflowRun),

		atc.ListRecentAgentRunMetrics: http.HandlerFunc(metricsServer.ListRecent),
		atc.ListBuildAgentRunMetrics:  http.HandlerFunc(metricsServer.ListByBuild),

		atc.ListAgentTickets:      http.HandlerFunc(ticketsServer.ListTickets),
		atc.CreateAgentTicket:     http.HandlerFunc(ticketsServer.CreateTicket),
		atc.GetAgentTicket:        http.HandlerFunc(ticketsServer.GetTicket),
		atc.UpdateAgentTicket:     http.HandlerFunc(ticketsServer.UpdateTicket),
		atc.TransitionAgentTicket: http.HandlerFunc(ticketsServer.TransitionTicket),
		atc.DispatchAgentTicket:   agentDispatchHandler,
		atc.ListAgentTicketRuns:   http.HandlerFunc(ticketJournalServer.ListRuns),

		atc.SetAgentUserCredential:                     http.HandlerFunc(credentialsServer.Set),
		atc.GetAgentUserCredentialStatus:               http.HandlerFunc(credentialsServer.Status),
		atc.DeleteAgentUserCredential:                  http.HandlerFunc(credentialsServer.Delete),
		atc.GetAgentCostRollup:                         http.HandlerFunc(costsServer.GetRollup),
		atc.ListAgentWorkflows:                         http.HandlerFunc(workflowsServer.List),
		atc.ListAgentWorkflowVersions:                  http.HandlerFunc(workflowsServer.Versions),
		atc.GetAgentWorkflowVersion:                    http.HandlerFunc(workflowsServer.Get),
		atc.CreateAgentWorkflowVersion:                 http.HandlerFunc(workflowsServer.Import),
		atc.PromoteAgentWorkflowVersion:                http.HandlerFunc(workflowsServer.Promote),
		atc.GetAgentWorkflowStats:                      http.HandlerFunc(workflowsServer.Stats),
		atc.GetAgentWorkflowOverview:                   http.HandlerFunc(workflowOverviewHandlers.Overview),
		atc.UpdateAgentWorkflow:                        http.HandlerFunc(workflowsServer.Update),
		atc.CreateAgentWorkflowRun:                     http.HandlerFunc(workflowRunHandlers.Create),
		atc.ListAgentWorkflowRuns:                      http.HandlerFunc(workflowRunHandlers.List),
		atc.GetAgentWorkflowRunOperationalStatusCounts: http.HandlerFunc(workflowRunHandlers.OperationalStatusCounts),
		atc.GetAgentWorkflowRun:                        http.HandlerFunc(workflowRunHandlers.Get),
		atc.CancelAgentWorkflowRun:                     http.HandlerFunc(workflowRunHandlers.Cancel),
		atc.RetryAgentWorkflowRun:                      http.HandlerFunc(workflowRunHandlers.Retry),
		atc.GetAgentWorkflowRunOutputs:                 http.HandlerFunc(workflowRunHandlers.Outputs),
		atc.GetAgentWorkflowRunGraph:                   http.HandlerFunc(workflowRunHandlers.Graph),
		atc.ListAgentWorkflowRunWaits:                  http.HandlerFunc(workflowWaitHandlers.List),
		atc.ResolveAgentWorkflowRunWait:                http.HandlerFunc(workflowWaitHandlers.Resolve),
		atc.ListAgentWorkflowRunOutcomes:               http.HandlerFunc(workflowOutcomeHandlers.List),
		atc.SetAgentWorkflowRunOutputOutcome:           http.HandlerFunc(workflowOutcomeHandlers.Record),
		atc.ListAgentWorkflowRunMetrics:                http.HandlerFunc(metricsServer.ListByWorkflowRun),
		atc.ListAgentWorkflowRunTranscripts:            http.HandlerFunc(transcriptServer.ListTranscripts),
		atc.GetAgentWorkflowRunTranscript:              http.HandlerFunc(transcriptServer.GetTranscript),
		atc.ListAgentNodes:                             http.HandlerFunc(nodesServer.List),
		atc.ListAgentNodeVersions:                      http.HandlerFunc(nodesServer.Versions),
		atc.GetAgentNodeVersion:                        http.HandlerFunc(nodesServer.Get),
		atc.CreateAgentNodeVersion:                     http.HandlerFunc(nodesServer.Import),
		atc.ReleaseAgentNodeVersion:                    http.HandlerFunc(nodesServer.Release),
		atc.DeprecateAgentNodeVersion:                  http.HandlerFunc(nodesServer.Deprecate),
		atc.CreateAgentNodeRun:                         http.HandlerFunc(nodeRunHandlers.Create),
		atc.ListAgentNodeRuns:                          http.HandlerFunc(nodeRunHandlers.List),
		atc.GetAgentNodeRun:                            http.HandlerFunc(nodeRunHandlers.Get),
		atc.CancelAgentNodeRun:                         http.HandlerFunc(nodeRunHandlers.Cancel),
		atc.ListAgentNodeConsumers:                     http.HandlerFunc(nodeUpgradeHandlers.Consumers),
		atc.UpgradeAgentNodeConsumers:                  http.HandlerFunc(nodeUpgradeHandlers.Upgrade),
		atc.CreateAgentExperiment:                      http.HandlerFunc(experimentHandlers.Create),
		atc.ListAgentExperiments:                       http.HandlerFunc(experimentHandlers.List),
		atc.GetAgentExperiment:                         http.HandlerFunc(experimentHandlers.Get),
		atc.UpdateAgentExperiment:                      http.HandlerFunc(experimentHandlers.Update),
		atc.ValidateAgentExperiment:                    http.HandlerFunc(experimentHandlers.Validate),
		atc.StartAgentExperiment:                       http.HandlerFunc(experimentHandlers.Start),
		atc.CancelAgentExperiment:                      http.HandlerFunc(experimentHandlers.Cancel),
		atc.ListAgentExperimentCells:                   http.HandlerFunc(experimentHandlers.ListCells),
		atc.GetAgentExperimentCell:                     http.HandlerFunc(experimentHandlers.GetCell),
		atc.GetAgentExperimentScorecard:                http.HandlerFunc(experimentHandlers.Scorecard),

		atc.GetAgentDispatcher: http.HandlerFunc(dispatcherServer.Get),
		atc.SetAgentDispatcher: http.HandlerFunc(dispatcherServer.Set),

		atc.CreateAgentSnapshot: teamHandlerFactory.HandlerFor(snapshotTeamHandler(snapshotHandlers.Create)),
		atc.CaptureAgentResourceSnapshot: teamHandlerFactory.HandlerFor(snapshotTeamHandler(func(team snapshotsapi.TrustedTeam) http.Handler {
			return snapshotHandlers.CaptureResource(team, resourceCapturer)
		})),
		atc.ListAgentSnapshots:                 teamHandlerFactory.HandlerFor(snapshotTeamHandler(snapshotHandlers.List)),
		atc.GetAgentSnapshot:                   teamHandlerFactory.HandlerFor(snapshotTeamHandler(snapshotHandlers.Show)),
		atc.GetAgentRepositoryChangeProjection: teamHandlerFactory.HandlerFor(snapshotTeamHandler(snapshotHandlers.RepositoryChangeProjection)),
		atc.DownloadAgentSnapshot:              teamHandlerFactory.HandlerFor(snapshotTeamHandler(snapshotHandlers.Content)),
		atc.PinAgentSnapshot:                   teamHandlerFactory.HandlerFor(snapshotTeamHandler(snapshotHandlers.Pin)),
		atc.UnpinAgentSnapshot:                 teamHandlerFactory.HandlerFor(snapshotTeamHandler(snapshotHandlers.Unpin)),
		atc.AdmitAgentChildExecution:           authorityHandler,
		atc.PhaseAgentChildExecution:           authorityHandler,
		atc.UpdateAgentChildExecution:          authorityHandler,
		atc.TerminalAgentChildExecution:        authorityHandler,
		atc.SealAgentChildExecution:            authorityHandler,
		atc.GetAgentChildExecution:             inspectionHandler,
	}

	return rata.NewRouter(atc.Routes, wrapper.Wrap(handlers))
}
