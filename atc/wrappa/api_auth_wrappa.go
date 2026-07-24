package wrappa

import (
	"fmt"

	"github.com/concourse/concourse/agent/api/principals"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/tedsuo/rata"
)

type APIAuthWrappa struct {
	checkPipelineAccessHandlerFactory   auth.CheckPipelineAccessHandlerFactory
	checkBuildReadAccessHandlerFactory  auth.CheckBuildReadAccessHandlerFactory
	checkBuildWriteAccessHandlerFactory auth.CheckBuildWriteAccessHandlerFactory
	checkWorkerTeamAccessHandlerFactory auth.CheckWorkerTeamAccessHandlerFactory
	checkAgentPrincipalHandlerFactory   auth.CheckAgentPrincipalHandlerFactory
}

func NewAPIAuthWrappa(
	checkPipelineAccessHandlerFactory auth.CheckPipelineAccessHandlerFactory,
	checkBuildReadAccessHandlerFactory auth.CheckBuildReadAccessHandlerFactory,
	checkBuildWriteAccessHandlerFactory auth.CheckBuildWriteAccessHandlerFactory,
	checkWorkerTeamAccessHandlerFactory auth.CheckWorkerTeamAccessHandlerFactory,
	checkAgentPrincipalHandlerFactory auth.CheckAgentPrincipalHandlerFactory,
) *APIAuthWrappa {
	return &APIAuthWrappa{
		checkPipelineAccessHandlerFactory:   checkPipelineAccessHandlerFactory,
		checkBuildReadAccessHandlerFactory:  checkBuildReadAccessHandlerFactory,
		checkBuildWriteAccessHandlerFactory: checkBuildWriteAccessHandlerFactory,
		checkWorkerTeamAccessHandlerFactory: checkWorkerTeamAccessHandlerFactory,
		checkAgentPrincipalHandlerFactory:   checkAgentPrincipalHandlerFactory,
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
			atc.GetAgentPlatformInfo,
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

		// principal(reviews:write) — 00-shared-contracts.md §4.1.
		case atc.SubmitAgentReview:
			newHandler = wrappa.checkAgentPrincipalHandlerFactory.HandlerFor(
				handler, rejector, principals.ScopeReviewsWrite)

		// principal(costs:write) — 00-shared-contracts.md §4.1.
		case atc.SubmitAgentCostRecord:
			newHandler = wrappa.checkAgentPrincipalHandlerFactory.HandlerFor(
				handler, rejector, principals.ScopeCostsWrite)

		// principal(metrics:write) — 00-shared-contracts.md §4.1/§4.2.
		// Metrics ingest never had a legacy static token, so this is the
		// strict tier: cap1 principal token (or admin user) only.
		case atc.SubmitAgentRunMetrics:
			newHandler = wrappa.checkAgentPrincipalHandlerFactory.HandlerFor(
				handler, rejector, principals.ScopeMetricsWrite)

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
			atc.CreateAgentPrincipal,
			atc.ListAgentPrincipals,
			atc.RevokeAgentPrincipal,
			// SetAgentDispatcher changes cluster-wide autonomous behavior — same
			// admin tier as minting principals. Reads (GetAgentDispatcher) are
			// merely authenticated (block above).
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
			atc.GetAgentFeedback,
			atc.GetAgentFeedbackSummary,
			atc.ClassifyAgentVerdict,
			atc.GetAgentReviewFindings,
			atc.GetAgentSnapshotReview,
			atc.ListAgentWorkflowRunReviews,
			atc.ListAgentRunMetrics,
			atc.ListRecentAgentRunMetrics,
			atc.ListAgentWorkflows,
			atc.ListAgentWorkflowVersions,
			atc.GetAgentWorkflowVersion,
			atc.CreateAgentWorkflowVersion,
			atc.PromoteAgentWorkflowVersion,
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
			// DispatchAgentTicket is deliberately human-only (no principal
			// tier): the manual trigger IS the budget gate while budget
			// admission is deferred (manual-dispatch slice, 2026-07-17).
			atc.DispatchAgentTicket,
			// SetAgentTicketDisposition is deliberately human-only too
			// (§4.2: authorized member, NO principal path): a principal
			// tier would let an agent dispose its own ticket past the
			// human review gate (delivery-outcomes decision D-3).
			// GetAgentTicketOutcome and GetAgentTicketDiff are plain
			// authorized viewer.
			atc.SetAgentTicketDisposition,
			atc.GetAgentTicketOutcome,
			atc.GetAgentTicketDiff:
			newHandler = auth.CheckAgentAuthorizationHandler(handler, rejector)

		// combined tier: agent principal (tickets:write) OR authorized
		// main-team member — 00-shared-contracts.md §4.2 + ticket-core addendum
		case atc.CreateAgentTicket,
			atc.TransitionAgentTicket,
			atc.SubmitAgentTicketSpec,
			atc.SubmitAgentTicketPlan:
			newHandler = auth.AgentPrincipalOrMainTeamHandler(
				wrappa.checkAgentPrincipalHandlerFactory.HandlerFor(handler, rejector, principals.ScopeTicketsWrite),
				auth.CheckAgentAuthorizationHandler(handler, rejector),
			)

		// combined tier: agent principal (tickets:read) OR authorized
		// main-team viewer
		case atc.GetAgentTicket:
			newHandler = auth.AgentPrincipalOrMainTeamHandler(
				wrappa.checkAgentPrincipalHandlerFactory.HandlerFor(handler, rejector, principals.ScopeTicketsRead),
				auth.CheckAgentAuthorizationHandler(handler, rejector),
			)

		// principal-only: task status updates require tickets:write
		case atc.UpdateAgentTicketTask:
			newHandler = wrappa.checkAgentPrincipalHandlerFactory.HandlerFor(handler, rejector, principals.ScopeTicketsWrite)

		// think about it!
		default:
			panic(fmt.Sprintf("you missed a spot: %q", name))
		}

		wrapped[name] = newHandler
	}

	return wrapped
}
