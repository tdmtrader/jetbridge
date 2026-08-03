package atc

import "github.com/tedsuo/rata"

const (
	SaveConfig = "SaveConfig"
	GetConfig  = "GetConfig"

	GetBuild            = "GetBuild"
	GetBuildPlan        = "GetBuildPlan"
	CreateBuild         = "CreateBuild"
	ListBuilds          = "ListBuilds"
	BuildEvents         = "BuildEvents"
	BuildResources      = "BuildResources"
	AbortBuild          = "AbortBuild"
	GetBuildPreparation = "GetBuildPreparation"
	SetBuildComment     = "SetBuildComment"

	GetJob         = "GetJob"
	CreateJobBuild = "CreateJobBuild"
	RerunJobBuild  = "RerunJobBuild"
	ListAllJobs    = "ListAllJobs"
	ListJobs       = "ListJobs"
	ListJobBuilds  = "ListJobBuilds"
	ListJobInputs  = "ListJobInputs"
	GetJobBuild    = "GetJobBuild"
	PauseJob       = "PauseJob"
	UnpauseJob     = "UnpauseJob"
	ScheduleJob    = "ScheduleJob"
	GetVersionsDB  = "GetVersionsDB"
	JobBadge       = "JobBadge"
	MainJobBadge   = "MainJobBadge"

	ClearTaskCache = "ClearTaskCache"

	ListAllResources          = "ListAllResources"
	ListSharedForResource     = "ListSharedForResource"
	ListSharedForResourceType = "ListSharedForResourceType"
	ListResources             = "ListResources"
	ListResourceTypes         = "ListResourceTypes"
	GetResource               = "GetResource"
	CheckResource             = "CheckResource"
	CheckResourceWebHook      = "CheckResourceWebHook"
	CheckResourceType         = "CheckResourceType"
	CheckPrototype            = "CheckPrototype"

	ListResourceVersions           = "ListResourceVersions"
	ClearResourceVersions          = "ClearResourceVersions"
	CopyResourceVersions           = "CopyResourceVersions"
	ListDeprecatedScopes           = "ListDeprecatedScopes"
	ClearResourceTypeVersions      = "ClearResourceTypeVersions"
	GetResourceVersion             = "GetResourceVersion"
	EnableResourceVersion          = "EnableResourceVersion"
	DisableResourceVersion         = "DisableResourceVersion"
	PinResourceVersion             = "PinResourceVersion"
	UnpinResource                  = "UnpinResource"
	SetPinCommentOnResource        = "SetPinCommentOnResource"
	ListBuildsWithVersionAsInput   = "ListBuildsWithVersionAsInput"
	ListBuildsWithVersionAsOutput  = "ListBuildsWithVersionAsOutput"
	ClearResourceCache             = "ClearResourceCache"
	GetDownstreamResourceCausality = "GetDownstreamResourceCausality"
	GetUpstreamResourceCausality   = "GetUpstreamResourceCausality"

	GetCC = "GetCC"

	ListAllPipelines          = "ListAllPipelines"
	ListPipelines             = "ListPipelines"
	GetPipeline               = "GetPipeline"
	DeletePipeline            = "DeletePipeline"
	OrderPipelines            = "OrderPipelines"
	OrderPipelinesWithinGroup = "OrderPipelinesWithinGroup"
	PausePipeline             = "PausePipeline"
	ArchivePipeline           = "ArchivePipeline"
	UnpausePipeline           = "UnpausePipeline"
	ExposePipeline            = "ExposePipeline"
	HidePipeline              = "HidePipeline"
	RenamePipeline            = "RenamePipeline"
	ListPipelineBuilds        = "ListPipelineBuilds"
	CreatePipelineBuild       = "CreatePipelineBuild"
	PipelineBadge             = "PipelineBadge"

	CreatePipelineRun = "CreatePipelineRun"
	ListPipelineRuns  = "ListPipelineRuns"
	GetPipelineRun    = "GetPipelineRun"

	RegisterWorker = "RegisterWorker"
	ListWorkers    = "ListWorkers"
	DeleteWorker   = "DeleteWorker"

	SetLogLevel = "SetLogLevel"
	GetLogLevel = "GetLogLevel"

	DownloadCLI  = "DownloadCLI"
	GetInfo      = "GetInfo"
	GetInfoCreds = "GetInfoCreds"
	GetHealth    = "GetHealth"

	ListContainers  = "ListContainers"
	GetContainer    = "GetContainer"
	HijackContainer = "HijackContainer"

	ListVolumes = "ListVolumes"

	ListTeams      = "ListTeams"
	GetTeam        = "GetTeam"
	SetTeam        = "SetTeam"
	RenameTeam     = "RenameTeam"
	DestroyTeam    = "DestroyTeam"
	ListTeamBuilds = "ListTeamBuilds"

	CreateArtifact     = "CreateArtifact"
	GetArtifact        = "GetArtifact"
	ListBuildArtifacts = "ListBuildArtifacts"

	GetUser              = "GetUser"
	ListActiveUsersSince = "ListActiveUsersSince"

	SetWall   = "SetWall"
	GetWall   = "GetWall"
	ClearWall = "ClearWall"

	GetOpenIDConfiguration = "GetOpenIDConfiguration"
	GetSigningKeys         = "GetSigningKeys"

	SubmitAgentFeedback = "SubmitAgentFeedback"

	SetAgentUserCredential       = "SetAgentUserCredential"
	GetAgentUserCredentialStatus = "GetAgentUserCredentialStatus"
	DeleteAgentUserCredential    = "DeleteAgentUserCredential"
	GetAgentCostRollup           = "GetAgentCostRollup"

	GetBuildAgentReviews        = "GetBuildAgentReviews"
	ListTeamAgentReviews        = "ListTeamAgentReviews"
	GetAgentSnapshotReview      = "GetAgentSnapshotReview"
	ListAgentWorkflowRunReviews = "ListAgentWorkflowRunReviews"

	ListRecentAgentRunMetrics = "ListRecentAgentRunMetrics"
	ListBuildAgentRunMetrics  = "ListBuildAgentRunMetrics"

	ListAgentTickets      = "ListAgentTickets"
	CreateAgentTicket     = "CreateAgentTicket"
	GetAgentTicket        = "GetAgentTicket"
	UpdateAgentTicket     = "UpdateAgentTicket"
	TransitionAgentTicket = "TransitionAgentTicket"
	DispatchAgentTicket   = "DispatchAgentTicket"
	ListAgentTicketRuns   = "ListAgentTicketRuns"

	ListAgentWorkflows                         = "ListAgentWorkflows"
	ListAgentWorkflowVersions                  = "ListAgentWorkflowVersions"
	GetAgentWorkflowVersion                    = "GetAgentWorkflowVersion"
	CreateAgentWorkflowVersion                 = "CreateAgentWorkflowVersion"
	PromoteAgentWorkflowVersion                = "PromoteAgentWorkflowVersion"
	GetAgentWorkflowStats                      = "GetAgentWorkflowStats"
	GetAgentWorkflowOverview                   = "GetAgentWorkflowOverview"
	UpdateAgentWorkflow                        = "UpdateAgentWorkflow"
	CreateAgentWorkflowRun                     = "CreateAgentWorkflowRun"
	ListAgentWorkflowRuns                      = "ListAgentWorkflowRuns"
	GetAgentWorkflowRunOperationalStatusCounts = "GetAgentWorkflowRunOperationalStatusCounts"
	GetAgentWorkflowRun                        = "GetAgentWorkflowRun"
	CancelAgentWorkflowRun                     = "CancelAgentWorkflowRun"
	RetryAgentWorkflowRun                      = "RetryAgentWorkflowRun"
	GetAgentWorkflowRunOutputs                 = "GetAgentWorkflowRunOutputs"
	GetAgentWorkflowRunGraph                   = "GetAgentWorkflowRunGraph"
	ListAgentWorkflowRunWaits                  = "ListAgentWorkflowRunWaits"
	ResolveAgentWorkflowRunWait                = "ResolveAgentWorkflowRunWait"
	ListAgentWorkflowRunOutcomes               = "ListAgentWorkflowRunOutcomes"
	SetAgentWorkflowRunOutputOutcome           = "SetAgentWorkflowRunOutputOutcome"
	ListAgentWorkflowRunMetrics                = "ListAgentWorkflowRunMetrics"
	ListAgentWorkflowRunTranscripts            = "ListAgentWorkflowRunTranscripts"
	GetAgentWorkflowRunTranscript              = "GetAgentWorkflowRunTranscript"
	ListAgentNodes                             = "ListAgentNodes"
	ListAgentNodeVersions                      = "ListAgentNodeVersions"
	GetAgentNodeVersion                        = "GetAgentNodeVersion"
	CreateAgentNodeVersion                     = "CreateAgentNodeVersion"
	ReleaseAgentNodeVersion                    = "ReleaseAgentNodeVersion"
	DeprecateAgentNodeVersion                  = "DeprecateAgentNodeVersion"
	CreateAgentNodeRun                         = "CreateAgentNodeRun"
	ListAgentNodeRuns                          = "ListAgentNodeRuns"
	GetAgentNodeRun                            = "GetAgentNodeRun"
	CancelAgentNodeRun                         = "CancelAgentNodeRun"
	ListAgentNodeConsumers                     = "ListAgentNodeConsumers"
	UpgradeAgentNodeConsumers                  = "UpgradeAgentNodeConsumers"

	CreateAgentExperiment       = "CreateAgentExperiment"
	ListAgentExperiments        = "ListAgentExperiments"
	GetAgentExperiment          = "GetAgentExperiment"
	UpdateAgentExperiment       = "UpdateAgentExperiment"
	ValidateAgentExperiment     = "ValidateAgentExperiment"
	StartAgentExperiment        = "StartAgentExperiment"
	CancelAgentExperiment       = "CancelAgentExperiment"
	ListAgentExperimentCells    = "ListAgentExperimentCells"
	GetAgentExperimentCell      = "GetAgentExperimentCell"
	GetAgentExperimentScorecard = "GetAgentExperimentScorecard"

	CreateAgentSnapshot                = "CreateAgentSnapshot"
	CaptureAgentResourceSnapshot       = "CaptureAgentResourceSnapshot"
	ListAgentSnapshots                 = "ListAgentSnapshots"
	GetAgentSnapshot                   = "GetAgentSnapshot"
	GetAgentRepositoryChangeProjection = "GetAgentRepositoryChangeProjection"
	DownloadAgentSnapshot              = "DownloadAgentSnapshot"
	PinAgentSnapshot                   = "PinAgentSnapshot"
	UnpinAgentSnapshot                 = "UnpinAgentSnapshot"

	AdmitAgentChildExecution                   = "AdmitAgentChildExecution"
	PhaseAgentChildExecution                   = "PhaseAgentChildExecution"
	UpdateAgentChildExecution                  = "UpdateAgentChildExecution"
	TerminalAgentChildExecution                = "TerminalAgentChildExecution"
	SealAgentChildExecution                    = "SealAgentChildExecution"
	CaptureWorkspaceAgentChildExecution        = "CaptureWorkspaceAgentChildExecution"
	CaptureWorkspaceFailureAgentChildExecution = "CaptureWorkspaceFailureAgentChildExecution"
	GetAgentChildExecution                     = "GetAgentChildExecution"

	GetAgentDispatcher = "GetAgentDispatcher"
	SetAgentDispatcher = "SetAgentDispatcher"

	MCPEndpoint = "MCPEndpoint"
)

const (
	ClearTaskCacheQueryPath = "cache_path"
	SaveConfigCheckCreds    = "check_creds"
)

var Routes = rata.Routes([]rata.Route{
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/config", Method: "PUT", Name: SaveConfig},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/config", Method: "GET", Name: GetConfig},

	{Path: "/api/v1/teams/:team_name/builds", Method: "POST", Name: CreateBuild},

	{Path: "/api/v1/builds", Method: "GET", Name: ListBuilds},
	{Path: "/api/v1/builds/:build_id", Method: "GET", Name: GetBuild},
	{Path: "/api/v1/builds/:build_id/plan", Method: "GET", Name: GetBuildPlan},
	{Path: "/api/v1/builds/:build_id/events", Method: "GET", Name: BuildEvents},
	{Path: "/api/v1/builds/:build_id/resources", Method: "GET", Name: BuildResources},
	{Path: "/api/v1/builds/:build_id/abort", Method: "PUT", Name: AbortBuild},
	{Path: "/api/v1/builds/:build_id/preparation", Method: "GET", Name: GetBuildPreparation},
	{Path: "/api/v1/builds/:build_id/artifacts", Method: "GET", Name: ListBuildArtifacts},
	{Path: "/api/v1/builds/:build_id/comment", Method: "PUT", Name: SetBuildComment},

	{Path: "/api/v1/jobs", Method: "GET", Name: ListAllJobs},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/jobs", Method: "GET", Name: ListJobs},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name", Method: "GET", Name: GetJob},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/builds", Method: "GET", Name: ListJobBuilds},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/builds", Method: "POST", Name: CreateJobBuild},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/builds/:build_name", Method: "POST", Name: RerunJobBuild},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/inputs", Method: "GET", Name: ListJobInputs},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/builds/:build_name", Method: "GET", Name: GetJobBuild},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/pause", Method: "PUT", Name: PauseJob},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/unpause", Method: "PUT", Name: UnpauseJob},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/schedule", Method: "PUT", Name: ScheduleJob},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/badge", Method: "GET", Name: JobBadge},
	{Path: "/api/v1/pipelines/:pipeline_name/jobs/:job_name/badge", Method: "GET", Name: MainJobBadge},

	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/jobs/:job_name/tasks/:step_name/cache", Method: "DELETE", Name: ClearTaskCache},

	{Path: "/api/v1/pipelines", Method: "GET", Name: ListAllPipelines},
	{Path: "/api/v1/teams/:team_name/pipelines", Method: "GET", Name: ListPipelines},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name", Method: "GET", Name: GetPipeline},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name", Method: "DELETE", Name: DeletePipeline},
	{Path: "/api/v1/teams/:team_name/pipelines/ordering", Method: "PUT", Name: OrderPipelines},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/ordering", Method: "PUT", Name: OrderPipelinesWithinGroup},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/pause", Method: "PUT", Name: PausePipeline},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/archive", Method: "PUT", Name: ArchivePipeline},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/unpause", Method: "PUT", Name: UnpausePipeline},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/expose", Method: "PUT", Name: ExposePipeline},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/hide", Method: "PUT", Name: HidePipeline},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/versions-db", Method: "GET", Name: GetVersionsDB},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/rename", Method: "PUT", Name: RenamePipeline},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/builds", Method: "GET", Name: ListPipelineBuilds},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/builds", Method: "POST", Name: CreatePipelineBuild},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/badge", Method: "GET", Name: PipelineBadge},

	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/runs", Method: "POST", Name: CreatePipelineRun},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/runs", Method: "GET", Name: ListPipelineRuns},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/runs/:run_number", Method: "GET", Name: GetPipelineRun},

	{Path: "/api/v1/resources", Method: "GET", Name: ListAllResources},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/resources", Method: "GET", Name: ListResources},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/shared", Method: "GET", Name: ListSharedForResource},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/resource-types/:resource_type_name/shared", Method: "GET", Name: ListSharedForResourceType},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/resource-types", Method: "GET", Name: ListResourceTypes},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name", Method: "GET", Name: GetResource},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/check", Method: "POST", Name: CheckResource},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/check/webhook", Method: "POST", Name: CheckResourceWebHook},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/resource-types/:resource_type_name/check", Method: "POST", Name: CheckResourceType},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/prototypes/:prototype_name/check", Method: "POST", Name: CheckPrototype},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/cache", Method: "DELETE", Name: ClearResourceCache},

	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/versions", Method: "GET", Name: ListResourceVersions},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/versions", Method: "DELETE", Name: ClearResourceVersions},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/copy-versions", Method: "PUT", Name: CopyResourceVersions},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/deprecated-scopes", Method: "GET", Name: ListDeprecatedScopes},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/resource-types/:resource_type_name/versions", Method: "DELETE", Name: ClearResourceTypeVersions},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/versions/:resource_config_version_id", Method: "GET", Name: GetResourceVersion},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/versions/:resource_config_version_id/enable", Method: "PUT", Name: EnableResourceVersion},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/versions/:resource_config_version_id/disable", Method: "PUT", Name: DisableResourceVersion},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/versions/:resource_config_version_id/pin", Method: "PUT", Name: PinResourceVersion},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/unpin", Method: "PUT", Name: UnpinResource},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/pin_comment", Method: "PUT", Name: SetPinCommentOnResource},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/versions/:resource_config_version_id/input_to", Method: "GET", Name: ListBuildsWithVersionAsInput},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/versions/:resource_config_version_id/output_of", Method: "GET", Name: ListBuildsWithVersionAsOutput},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/versions/:resource_config_version_id/downstream", Method: "GET", Name: GetDownstreamResourceCausality},
	{Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/versions/:resource_config_version_id/upstream", Method: "GET", Name: GetUpstreamResourceCausality},
	// {Path: "/api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/causality", Method: "GET", Name: GetResourceCausality},

	{Path: "/api/v1/teams/:team_name/cc.xml", Method: "GET", Name: GetCC},

	{Path: "/api/v1/workers", Method: "GET", Name: ListWorkers},
	{Path: "/api/v1/workers", Method: "POST", Name: RegisterWorker},
	{Path: "/api/v1/workers/:worker_name", Method: "DELETE", Name: DeleteWorker},

	{Path: "/api/v1/log-level", Method: "GET", Name: GetLogLevel},
	{Path: "/api/v1/log-level", Method: "PUT", Name: SetLogLevel},

	{Path: "/api/v1/cli", Method: "GET", Name: DownloadCLI},
	{Path: "/api/v1/info", Method: "GET", Name: GetInfo},
	{Path: "/api/v1/info/creds", Method: "GET", Name: GetInfoCreds},
	{Path: "/api/v1/health", Method: "GET", Name: GetHealth},

	{Path: "/api/v1/user", Method: "GET", Name: GetUser},
	{Path: "/api/v1/users", Method: "GET", Name: ListActiveUsersSince},

	{Path: "/api/v1/teams/:team_name/containers", Method: "GET", Name: ListContainers},
	{Path: "/api/v1/teams/:team_name/containers/:id", Method: "GET", Name: GetContainer},
	{Path: "/api/v1/teams/:team_name/containers/:id/hijack", Method: "GET", Name: HijackContainer},

	{Path: "/api/v1/teams/:team_name/volumes", Method: "GET", Name: ListVolumes},

	{Path: "/api/v1/teams", Method: "GET", Name: ListTeams},
	{Path: "/api/v1/teams/:team_name", Method: "GET", Name: GetTeam},
	{Path: "/api/v1/teams/:team_name", Method: "PUT", Name: SetTeam},
	{Path: "/api/v1/teams/:team_name/rename", Method: "PUT", Name: RenameTeam},
	{Path: "/api/v1/teams/:team_name", Method: "DELETE", Name: DestroyTeam},
	{Path: "/api/v1/teams/:team_name/builds", Method: "GET", Name: ListTeamBuilds},

	{Path: "/api/v1/teams/:team_name/artifacts", Method: "POST", Name: CreateArtifact},
	{Path: "/api/v1/teams/:team_name/artifacts/:artifact_id", Method: "GET", Name: GetArtifact},

	{Path: "/api/v1/wall", Method: "GET", Name: GetWall},
	{Path: "/api/v1/wall", Method: "PUT", Name: SetWall},
	{Path: "/api/v1/wall", Method: "DELETE", Name: ClearWall},

	{Path: "/api/v1/teams/:team_name/agent/feedback", Method: "POST", Name: SubmitAgentFeedback},

	{Path: "/api/v1/agent/user-credentials", Method: "PUT", Name: SetAgentUserCredential},
	{Path: "/api/v1/agent/user-credentials", Method: "GET", Name: GetAgentUserCredentialStatus},
	{Path: "/api/v1/agent/user-credentials/:kind", Method: "DELETE", Name: DeleteAgentUserCredential},
	{Path: "/api/v1/agent/costs", Method: "GET", Name: GetAgentCostRollup},

	{Path: "/api/v1/builds/:build_id/agent-reviews", Method: "GET", Name: GetBuildAgentReviews},
	{Path: "/api/v1/teams/:team_name/agent-reviews", Method: "GET", Name: ListTeamAgentReviews},
	{Path: "/api/v1/agent/snapshots/:snapshot_id/projections/review", Method: "GET", Name: GetAgentSnapshotReview},

	{Path: "/api/v1/agent/metrics", Method: "GET", Name: ListRecentAgentRunMetrics},
	{Path: "/api/v1/builds/:build_id/agent-metrics", Method: "GET", Name: ListBuildAgentRunMetrics},

	{Path: "/api/v1/agent/tickets", Method: "GET", Name: ListAgentTickets},
	{Path: "/api/v1/agent/tickets", Method: "POST", Name: CreateAgentTicket},
	{Path: "/api/v1/agent/tickets/:ticket_id", Method: "GET", Name: GetAgentTicket},
	{Path: "/api/v1/agent/tickets/:ticket_id", Method: "PUT", Name: UpdateAgentTicket},
	{Path: "/api/v1/agent/tickets/:ticket_id/state", Method: "PUT", Name: TransitionAgentTicket},
	{Path: "/api/v1/agent/tickets/:ticket_id/dispatch", Method: "POST", Name: DispatchAgentTicket},
	{Path: "/api/v1/agent/tickets/:ticket_id/runs", Method: "GET", Name: ListAgentTicketRuns},

	{Path: "/api/v1/agent/workflows", Method: "GET", Name: ListAgentWorkflows},
	{Path: "/api/v1/agent/workflows/:workflow_name/versions", Method: "GET", Name: ListAgentWorkflowVersions},
	{Path: "/api/v1/agent/workflows/:workflow_name/versions/:version", Method: "GET", Name: GetAgentWorkflowVersion},
	{Path: "/api/v1/agent/workflows/:workflow_name/versions", Method: "POST", Name: CreateAgentWorkflowVersion},
	{Path: "/api/v1/agent/workflows/:workflow_name/versions/:version/live", Method: "PUT", Name: PromoteAgentWorkflowVersion},
	{Path: "/api/v1/agent/workflows/:workflow_name/stats", Method: "GET", Name: GetAgentWorkflowStats},
	{Path: "/api/v1/agent/workflows/:workflow_name/overview", Method: "GET", Name: GetAgentWorkflowOverview},
	{Path: "/api/v1/agent/workflows/:workflow_name", Method: "PUT", Name: UpdateAgentWorkflow},
	{Path: "/api/v1/agent/workflows/:workflow_name/runs", Method: "POST", Name: CreateAgentWorkflowRun},
	{Path: "/api/v1/agent/workflows/:workflow_name/runs", Method: "GET", Name: ListAgentWorkflowRuns},
	{Path: "/api/v1/agent/workflows/:workflow_name/runs/operational-status-counts", Method: "GET", Name: GetAgentWorkflowRunOperationalStatusCounts},
	{Path: "/api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id", Method: "GET", Name: GetAgentWorkflowRun},
	{Path: "/api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id/cancel", Method: "POST", Name: CancelAgentWorkflowRun},
	{Path: "/api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id/retry", Method: "POST", Name: RetryAgentWorkflowRun},
	{Path: "/api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id/outputs", Method: "GET", Name: GetAgentWorkflowRunOutputs},
	{Path: "/api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id/graph", Method: "GET", Name: GetAgentWorkflowRunGraph},
	{Path: "/api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id/waits", Method: "GET", Name: ListAgentWorkflowRunWaits},
	{Path: "/api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id/waits/:workflow_wait_id/resolve", Method: "PUT", Name: ResolveAgentWorkflowRunWait},
	{Path: "/api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id/outcomes", Method: "GET", Name: ListAgentWorkflowRunOutcomes},
	{Path: "/api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id/outputs/:snapshot_id/outcome", Method: "PUT", Name: SetAgentWorkflowRunOutputOutcome},
	{Path: "/api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id/metrics", Method: "GET", Name: ListAgentWorkflowRunMetrics},
	{Path: "/api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id/transcripts", Method: "GET", Name: ListAgentWorkflowRunTranscripts},
	{Path: "/api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id/transcripts/:plan_id", Method: "GET", Name: GetAgentWorkflowRunTranscript},
	{Path: "/api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id/reviews", Method: "GET", Name: ListAgentWorkflowRunReviews},
	{Path: "/api/v1/agent/nodes", Method: "GET", Name: ListAgentNodes},
	{Path: "/api/v1/agent/nodes/:node_name/versions", Method: "GET", Name: ListAgentNodeVersions},
	{Path: "/api/v1/agent/nodes/:node_name/versions/:version", Method: "GET", Name: GetAgentNodeVersion},
	{Path: "/api/v1/agent/nodes/:node_name/versions", Method: "POST", Name: CreateAgentNodeVersion},
	{Path: "/api/v1/agent/nodes/:node_name/versions/:version/release", Method: "PUT", Name: ReleaseAgentNodeVersion},
	{Path: "/api/v1/agent/nodes/:node_name/versions/:version/deprecation", Method: "PUT", Name: DeprecateAgentNodeVersion},
	{Path: "/api/v1/agent/nodes/:node_name/versions/:version/runs", Method: "POST", Name: CreateAgentNodeRun},
	{Path: "/api/v1/agent/nodes/:node_name/versions/:version/runs", Method: "GET", Name: ListAgentNodeRuns},
	{Path: "/api/v1/agent/nodes/:node_name/runs/:workflow_run_id", Method: "GET", Name: GetAgentNodeRun},
	{Path: "/api/v1/agent/nodes/:node_name/runs/:workflow_run_id/cancel", Method: "POST", Name: CancelAgentNodeRun},
	{Path: "/api/v1/agent/nodes/:node_name/versions/:version/consumers", Method: "GET", Name: ListAgentNodeConsumers},
	{Path: "/api/v1/agent/nodes/:node_name/versions/:version/upgrades", Method: "POST", Name: UpgradeAgentNodeConsumers},

	{Path: "/api/v1/agent/experiments", Method: "POST", Name: CreateAgentExperiment},
	{Path: "/api/v1/agent/experiments", Method: "GET", Name: ListAgentExperiments},
	{Path: "/api/v1/agent/experiments/:experiment_id", Method: "GET", Name: GetAgentExperiment},
	{Path: "/api/v1/agent/experiments/:experiment_id", Method: "PUT", Name: UpdateAgentExperiment},
	{Path: "/api/v1/agent/experiments/:experiment_id/validate", Method: "POST", Name: ValidateAgentExperiment},
	{Path: "/api/v1/agent/experiments/:experiment_id/start", Method: "POST", Name: StartAgentExperiment},
	{Path: "/api/v1/agent/experiments/:experiment_id/cancel", Method: "POST", Name: CancelAgentExperiment},
	{Path: "/api/v1/agent/experiments/:experiment_id/cells", Method: "GET", Name: ListAgentExperimentCells},
	{Path: "/api/v1/agent/experiments/:experiment_id/cells/:cell_id", Method: "GET", Name: GetAgentExperimentCell},
	{Path: "/api/v1/agent/experiments/:experiment_id/scorecard", Method: "GET", Name: GetAgentExperimentScorecard},

	{Path: "/api/v1/teams/:team_name/agent/snapshots", Method: "POST", Name: CreateAgentSnapshot},
	{Path: "/api/v1/teams/:team_name/agent/snapshots/capture-resource", Method: "POST", Name: CaptureAgentResourceSnapshot},
	{Path: "/api/v1/teams/:team_name/agent/snapshots", Method: "GET", Name: ListAgentSnapshots},
	{Path: "/api/v1/teams/:team_name/agent/snapshots/:snapshot_id", Method: "GET", Name: GetAgentSnapshot},
	{Path: "/api/v1/teams/:team_name/agent/snapshots/:snapshot_id/projections/repository-change", Method: "GET", Name: GetAgentRepositoryChangeProjection},
	{Path: "/api/v1/teams/:team_name/agent/snapshots/:snapshot_id/content", Method: "GET", Name: DownloadAgentSnapshot},
	{Path: "/api/v1/teams/:team_name/agent/snapshots/:snapshot_id/pin", Method: "PUT", Name: PinAgentSnapshot},
	{Path: "/api/v1/teams/:team_name/agent/snapshots/:snapshot_id/pin", Method: "DELETE", Name: UnpinAgentSnapshot},

	{Path: "/api/v1/internal/agent-child-executions/admit", Method: "POST", Name: AdmitAgentChildExecution},
	{Path: "/api/v1/internal/agent-child-executions/:execution_id/phase", Method: "POST", Name: PhaseAgentChildExecution},
	{Path: "/api/v1/internal/agent-child-executions/:execution_id/update", Method: "POST", Name: UpdateAgentChildExecution},
	{Path: "/api/v1/internal/agent-child-executions/:execution_id/terminal", Method: "POST", Name: TerminalAgentChildExecution},
	{Path: "/api/v1/internal/agent-child-executions/:execution_id/seal", Method: "POST", Name: SealAgentChildExecution},
	{Path: "/api/v1/internal/agent-child-executions/:execution_id/workspace-capture", Method: "POST", Name: CaptureWorkspaceAgentChildExecution},
	{Path: "/api/v1/internal/agent-child-executions/:execution_id/workspace-capture-failed", Method: "POST", Name: CaptureWorkspaceFailureAgentChildExecution},
	{Path: "/api/v1/teams/:team_name/agent-child-executions/:execution_id", Method: "GET", Name: GetAgentChildExecution},

	{Path: "/api/v1/agent/dispatcher", Method: "GET", Name: GetAgentDispatcher},
	{Path: "/api/v1/agent/dispatcher", Method: "PUT", Name: SetAgentDispatcher},

	{Path: "/api/v1/mcp", Method: "POST", Name: MCPEndpoint},

	{Path: "/.well-known/openid-configuration", Method: "GET", Name: GetOpenIDConfiguration},
	{Path: "/.well-known/jwks.json", Method: "GET", Name: GetSigningKeys},
})
