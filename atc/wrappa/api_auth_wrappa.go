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
			atc.BuildResources,
			atc.GetBuildAgentReviews:
			newHandler = wrappa.checkBuildReadAccessHandlerFactory.AnyJobHandler(handler, rejector)

		// pipeline and job are public or authorized
		case atc.GetBuildPreparation,
			atc.BuildEvents,
			atc.GetBuildPlan,
			atc.ListBuildArtifacts:
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

		// principal(reviews:write) — 00-shared-contracts.md §4.1. The
		// legacy static publish token is still accepted inside the
		// handler during the dual-accept window, so requests without a
		// cap1 token bypass to the delegate instead of being rejected
		// (agent/api/reviews.Handler.SubmitReview,
		// agent/api/costs.Handler.SubmitRecord validate it themselves;
		// contract addendum 2026-07-08).
		case atc.SubmitAgentReview:
			newHandler = wrappa.checkAgentPrincipalHandlerFactory.HandlerForWithLegacyBypass(
				handler, rejector, principals.ScopeReviewsWrite)

		// principal(costs:write) — same dual-accept recipe as
		// SubmitAgentReview above.
		case atc.SubmitAgentCostRecord:
			newHandler = wrappa.checkAgentPrincipalHandlerFactory.HandlerForWithLegacyBypass(
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
			atc.RevokeAgentPrincipal:
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
			atc.ListAgentRunMetrics,
			atc.ListRecentAgentRunMetrics,
			atc.ListAgentWorkflows,
			atc.ListAgentWorkflowVersions,
			atc.GetAgentWorkflowVersion,
			atc.CreateAgentWorkflowVersion,
			atc.PromoteAgentWorkflowVersion,
			atc.GetAgentCostRollup:
			newHandler = auth.CheckAgentAuthorizationHandler(handler, rejector)

		// think about it!
		default:
			panic(fmt.Sprintf("you missed a spot: %q", name))
		}

		wrapped[name] = newHandler
	}

	return wrapped
}
