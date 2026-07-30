package wrappa

import (
	"fmt"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/tedsuo/rata"
)

type APIAuthWrappa struct {
	checkPipelineAccessHandlerFactory   auth.CheckPipelineAccessHandlerFactory
	checkBuildReadAccessHandlerFactory  auth.CheckBuildReadAccessHandlerFactory
	checkBuildWriteAccessHandlerFactory auth.CheckBuildWriteAccessHandlerFactory
	checkWorkerTeamAccessHandlerFactory auth.CheckWorkerTeamAccessHandlerFactory
}

func NewAPIAuthWrappa(
	checkPipelineAccessHandlerFactory auth.CheckPipelineAccessHandlerFactory,
	checkBuildReadAccessHandlerFactory auth.CheckBuildReadAccessHandlerFactory,
	checkBuildWriteAccessHandlerFactory auth.CheckBuildWriteAccessHandlerFactory,
	checkWorkerTeamAccessHandlerFactory auth.CheckWorkerTeamAccessHandlerFactory,
) *APIAuthWrappa {
	return &APIAuthWrappa{
		checkPipelineAccessHandlerFactory:   checkPipelineAccessHandlerFactory,
		checkBuildReadAccessHandlerFactory:  checkBuildReadAccessHandlerFactory,
		checkBuildWriteAccessHandlerFactory: checkBuildWriteAccessHandlerFactory,
		checkWorkerTeamAccessHandlerFactory: checkWorkerTeamAccessHandlerFactory,
	}
}

func (wrappa *APIAuthWrappa) Wrap(handlers rata.Handlers) rata.Handlers {
	wrapped := rata.Handlers{}

	rejector := auth.UnauthorizedRejector{}

	for name, handler := range handlers {
		newHandler := handler

		switch name {
		// pipeline is public or authorized
		case atc.GetBuild,
			atc.BuildResources:
			newHandler = wrappa.checkBuildReadAccessHandlerFactory.AnyJobHandler(handler, rejector)

		// pipeline and job are public or authorized — agent reviews and
		// run metrics carry content-bearing output (findings, run
		// Summary/Results), so they follow the BuildEvents tier rather
		// than the pipeline-only tier
		case atc.GetBuildPreparation,
			atc.BuildEvents,
			atc.GetBuildPlan,
			atc.ListBuildArtifacts,
			atc.GetBuildAgentReviews,
			atc.ListBuildAgentRunMetrics:
			newHandler = wrappa.checkBuildReadAccessHandlerFactory.CheckIfPrivateJobHandler(handler, rejector)

			// resource belongs to authorized team
		case atc.AbortBuild,
			atc.SetBuildComment:
			newHandler = wrappa.checkBuildWriteAccessHandlerFactory.HandlerFor(handler, rejector)

		// pipeline is public or authorized
		case atc.GetPipeline,
			atc.GetJobBuild,
			atc.PipelineBadge,
			atc.JobBadge,
			atc.ListJobs,
			atc.GetJob,
			atc.ListJobBuilds,
			atc.ListPipelineBuilds,
			atc.GetResource,
			atc.ListBuildsWithVersionAsInput,
			atc.ListBuildsWithVersionAsOutput,
			atc.GetDownstreamResourceCausality,
			atc.GetUpstreamResourceCausality,
			atc.GetResourceVersion,
			atc.ListResources,
			atc.ListResourceTypes,
			atc.ListResourceVersions:
			newHandler = wrappa.checkPipelineAccessHandlerFactory.HandlerFor(handler, rejector)

		// authenticated
		case atc.ListWorkers,
			atc.RegisterWorker,
			atc.DeleteWorker,
			atc.ListTeamBuilds,
			atc.GetUser,
			atc.SetAgentUserCredential,
			atc.GetAgentUserCredentialStatus,
			atc.DeleteAgentUserCredential,
			// GetAgentDispatcher: any authenticated user may READ the
			// dispatcher status. Mutating it (SetAgentDispatcher) is admin-only,
			// pinned in the CheckAdminHandler block below.
			atc.GetAgentDispatcher,
			atc.MCPEndpoint:
			newHandler = auth.CheckAuthenticationHandler(handler, rejector)

		// unauthenticated / delegating to handler (validate token if provided)
		case atc.DownloadCLI,
			atc.CheckResourceWebHook,
			atc.GetInfo,
			atc.GetHealth,
			atc.GetCC,
			atc.ListTeams,
			atc.ListAllPipelines,
			atc.ListPipelines,
			atc.ListAllJobs,
			atc.ListAllResources,
			atc.ListBuilds,
			atc.MainJobBadge,
			atc.GetWall,
			atc.GetOpenIDConfiguration,
			atc.GetSigningKeys:
			newHandler = auth.CheckAuthenticationIfProvidedHandler(handler, rejector)

		// admin
		case atc.GetLogLevel,
			atc.DestroyTeam,
			atc.ListActiveUsersSince,
			atc.SetLogLevel,
			atc.GetInfoCreds,
			atc.SetWall,
			atc.ClearWall,
			atc.ClearResourceVersions,
			atc.ClearResourceTypeVersions,
			atc.ListSharedForResource,
			atc.ListSharedForResourceType,
			// SetAgentDispatcher changes cluster-wide autonomous behavior. Reads
			// (GetAgentDispatcher) are merely authenticated (block above).
			atc.SetAgentDispatcher:
			newHandler = auth.CheckAdminHandler(handler, rejector)

		// authorized (requested team matches resource team and has required role, or is admin)
		case atc.GetTeam,
			atc.SetTeam,
			atc.RenameTeam,
			atc.ListContainers,
			atc.GetContainer,
			atc.HijackContainer,
			atc.ListVolumes,
			atc.CreateBuild,
			atc.CheckResource,
			atc.CheckResourceType,
			atc.CheckPrototype,
			atc.CreateJobBuild,
			atc.RerunJobBuild,
			atc.CreatePipelineBuild,
			atc.DeletePipeline,
			atc.DisableResourceVersion,
			atc.EnableResourceVersion,
			atc.PinResourceVersion,
			atc.UnpinResource,
			atc.SetPinCommentOnResource,
			atc.GetConfig,
			atc.GetVersionsDB,
			atc.ListJobInputs,
			atc.OrderPipelines,
			atc.OrderPipelinesWithinGroup,
			atc.PauseJob,
			atc.UnpauseJob,
			atc.PausePipeline,
			atc.UnpausePipeline,
			atc.RenamePipeline,
			atc.ExposePipeline,
			atc.HidePipeline,
			atc.SaveConfig,
			atc.ArchivePipeline,
			atc.ClearTaskCache,
			atc.ClearResourceCache,
			atc.CreateArtifact,
			atc.ScheduleJob,
			atc.GetArtifact,
			atc.ListTeamAgentReviews,
			atc.CreateAgentSnapshot,
			atc.CaptureAgentResourceSnapshot,
			atc.ListAgentSnapshots,
			atc.GetAgentSnapshot,
			atc.GetAgentRepositoryChangeProjection,
			atc.DownloadAgentSnapshot,
			atc.PinAgentSnapshot,
			atc.UnpinAgentSnapshot,
			atc.CopyResourceVersions,
			atc.CreatePipelineRun,
			atc.ListPipelineRuns,
			atc.GetPipelineRun,
			atc.ListDeprecatedScopes:
			newHandler = auth.CheckAuthorizationHandler(handler, rejector)

		// team-less /api/v1/agent/* routes: authorized against the main
		// team via their accessor DefaultRoles entries (decision 21 in
		// docs/superpowers/plans/agentic-platform/00-shared-contracts.md)
		case atc.SubmitAgentFeedback,
			atc.GetAgentSnapshotReview,
			atc.ListAgentWorkflowRunReviews,
			atc.ListRecentAgentRunMetrics,
			atc.ListAgentWorkflows,
			atc.ListAgentWorkflowVersions,
			atc.GetAgentWorkflowVersion,
			atc.CreateAgentWorkflowVersion,
			atc.PromoteAgentWorkflowVersion,
			atc.GetAgentWorkflowStats,
			// UpdateAgentWorkflow (annotate/deprecate) is deliberately human-only:
			// deprecating a workflow is an operator decision.
			atc.UpdateAgentWorkflow,
			atc.CreateAgentWorkflowRun,
			atc.ListAgentWorkflowRuns,
			atc.GetAgentWorkflowRunOperationalStatusCounts,
			atc.GetAgentWorkflowRun,
			atc.CancelAgentWorkflowRun,
			atc.RetryAgentWorkflowRun,
			atc.GetAgentWorkflowRunOutputs,
			atc.ListAgentWorkflowRunWaits,
			atc.ResolveAgentWorkflowRunWait,
			atc.ListAgentWorkflowRunOutcomes,
			atc.SetAgentWorkflowRunOutputOutcome,
			atc.ListAgentWorkflowRunMetrics,
			atc.ListAgentWorkflowRunTranscripts,
			atc.GetAgentWorkflowRunTranscript,
			atc.ListAgentNodes,
			atc.ListAgentNodeVersions,
			atc.GetAgentNodeVersion,
			atc.CreateAgentNodeVersion,
			atc.ReleaseAgentNodeVersion,
			atc.DeprecateAgentNodeVersion,
			atc.CreateAgentNodeRun,
			atc.ListAgentNodeRuns,
			atc.GetAgentNodeRun,
			atc.ListAgentNodeConsumers,
			atc.UpgradeAgentNodeConsumers,
			atc.CreateAgentExperiment,
			atc.ListAgentExperiments,
			atc.GetAgentExperiment,
			atc.UpdateAgentExperiment,
			atc.ValidateAgentExperiment,
			atc.StartAgentExperiment,
			atc.CancelAgentExperiment,
			atc.ListAgentExperimentCells,
			atc.GetAgentExperimentCell,
			atc.GetAgentExperimentScorecard,
			atc.GetAgentCostRollup,
			atc.ListAgentTickets,
			atc.UpdateAgentTicket,
			// DispatchAgentTicket is deliberately human-only: the manual trigger
			// is the budget gate while budget admission is deferred.
			atc.DispatchAgentTicket,
			atc.CreateAgentTicket,
			atc.TransitionAgentTicket,
			atc.GetAgentTicket:
			newHandler = auth.CheckAgentAuthorizationHandler(handler, rejector)

		// think about it!
		default:
			panic(fmt.Sprintf("you missed a spot: %q", name))
		}

		wrapped[name] = newHandler
	}

	return wrapped
}
