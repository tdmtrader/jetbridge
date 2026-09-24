// Package auth contains the API's authorization handlers.
package auth

import "github.com/concourse/concourse/atc"

// AuthorizationKind identifies the actual API wrapper, not a role or an MCP scope.
// Public read kinds still perform target checks; Delegated leaves visibility to
// the handler. Unknown actions must never become executable by default.
type AuthorizationKind uint8

const (
	AuthorizationUnknown AuthorizationKind = iota
	AuthorizationBuildRead
	AuthorizationBuildOutput
	AuthorizationBuildWrite
	AuthorizationPipelineRead
	AuthorizationAuthenticated
	AuthorizationDelegated
	AuthorizationAdmin
	AuthorizationTeam
)

// AuthorizationKindForAction is shared by API wrapping and target-free capability
// filtering. Roles, policy, and target authorization remain enforced per request.
func AuthorizationKindForAction(action string) (AuthorizationKind, bool) {
	switch atc.CanonicalAction(action) {
	case atc.GetBuild,
		atc.BuildResources:
		return AuthorizationBuildRead, true
	case atc.GetBuildPreparation,
		atc.BuildEvents,
		atc.GetBuildPlan,
		atc.ListBuildArtifacts:
		return AuthorizationBuildOutput, true
	case atc.AbortBuild,
		atc.SetBuildComment:
		return AuthorizationBuildWrite, true
	case atc.GetPipeline,
		atc.ListPipelineRuns,
		atc.GetPipelineRun,
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
		return AuthorizationPipelineRead, true
	case atc.ListWorkers,
		atc.RegisterWorker,
		atc.DeleteWorker,
		atc.ListTeamBuilds,
		atc.GetUser:
		return AuthorizationAuthenticated, true
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
		return AuthorizationDelegated, true
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
		atc.ListSharedForResourceType:
		return AuthorizationAdmin, true
	case atc.GetTeam,
		atc.GetPipelineRunResult,
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
		atc.CreatePipelineRunV2,
		atc.UploadPipelineRunInput,
		atc.HandoffPipelineRunCredentials,
		atc.GetPipelineRunCredentialSession,
		atc.CancelPipelineRun,
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
		atc.CopyResourceVersions,
		atc.ListDeprecatedScopes:
		return AuthorizationTeam, true
	default:
		return AuthorizationUnknown, false
	}
}
