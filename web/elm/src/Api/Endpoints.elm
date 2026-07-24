module Api.Endpoints exposing
    ( BuildEndpoint(..)
    , Endpoint(..)
    , InstanceGroupEndpoint(..)
    , JobEndpoint(..)
    , PipelineEndpoint(..)
    , ResourceEndpoint(..)
    , ResourceVersionEndpoint(..)
    , TeamEndpoint(..)
    , toString
    )

import Concourse
import RouteBuilder exposing (RouteBuilder, append, appendPath, appendQuery)
import Url
import Url.Builder


type Endpoint
    = PipelinesList
    | Pipeline Concourse.PipelineIdentifier PipelineEndpoint
    | JobsList
    | Job Concourse.JobIdentifier JobEndpoint
    | JobBuild Concourse.JobBuildIdentifier
    | Build Concourse.BuildId BuildEndpoint
    | ResourcesList
    | Resource Concourse.ResourceIdentifier ResourceEndpoint
    | ResourceVersion Concourse.VersionedResourceIdentifier ResourceVersionEndpoint
    | TeamsList
    | Team Concourse.TeamName TeamEndpoint
    | ClusterInfo
    | Wall
    | Cli
    | UserInfo
    | Logout
    | InstanceGroup Concourse.InstanceGroupIdentifier InstanceGroupEndpoint
    | BuildAgentReviews Concourse.BuildId
    | BuildAgentMetrics Concourse.BuildId
    | TeamAgentReviews Concourse.TeamName
    | AgentFeedback
    | AgentMetrics
    | AgentWorkflowsList
    | AgentCostRollup
    | AgentDispatcher
    | AgentCredentialsStatus
    | AgentPrincipalsList
    | AgentPrincipal Int
    | AgentTicketsList
    | AgentTicket Int
    | AgentTicketState Int
    | AgentTicketDispatch Int
    | AgentTicketTask Int Int
    | AgentTicketMetrics Int
    | AgentWorkflowVersions String
    | AgentWorkflowVersionLive String Int
    | AgentWorkflowRuns String
    | AgentWorkflowRunOperationalStatusCounts String
    | AgentWorkflowRun String String
    | AgentWorkflowRunCancel String String
    | AgentWorkflowRunRetry String String
    | AgentWorkflowRunWaits String String
    | AgentWorkflowWaitResolve String String String
    | AgentWorkflowRunOutcomes String String
    | AgentWorkflowRunMetrics String String
    | AgentWorkflowRunReviews String String
    | AgentSnapshot String String
    | AgentSnapshotContent String String
    | AgentSnapshotPin String String
    | AgentSnapshotRepositoryChange String String
    | AgentSnapshotReview String
    | AgentExperimentsList
    | AgentExperiment String
    | AgentExperimentCells String
    | AgentExperimentScorecard String
    | AgentExperimentCancel String


type PipelineEndpoint
    = BasePipeline
    | PausePipeline
    | UnpausePipeline
    | ExposePipeline
    | HidePipeline
    | PipelineJobsList
    | PipelineResourcesList
    | PipelineRunsList


type JobEndpoint
    = BaseJob
    | PauseJob
    | UnpauseJob
    | JobBuildsList


type BuildEndpoint
    = BaseBuild
    | BuildPlan
    | BuildPrep
    | AbortBuild
    | BuildResourcesList
    | BuildEventStream
    | SetComment


type ResourceEndpoint
    = BaseResource
    | ResourceVersionsList
    | UnpinResource
    | CheckResource
    | PinResourceComment


type ResourceVersionEndpoint
    = BaseResourceVersion
    | ResourceVersionInputTo
    | DownstreamCausality
    | ResourceVersionOutputOf
    | UpstreamCasuality
    | PinResourceVersion
    | EnableResourceVersion
    | DisableResourceVersion


type TeamEndpoint
    = TeamPipelinesList
    | OrderTeamPipelines


type InstanceGroupEndpoint
    = OrderInstanceGroupPipelines


base : RouteBuilder
base =
    ( [ "api", "v1" ], [] )


baseSky : RouteBuilder
baseSky =
    ( [ "sky" ], [] )


pipeline :
    { r
        | pipelineName : String
        , pipelineInstanceVars : Concourse.InstanceVars
        , teamName : String
    }
    -> RouteBuilder
pipeline id =
    base |> append (RouteBuilder.pipeline id)


resource :
    { r
        | pipelineName : String
        , pipelineInstanceVars : Concourse.InstanceVars
        , teamName : String
        , resourceName : String
    }
    -> RouteBuilder
resource id =
    pipeline id |> appendPath [ "resources", id.resourceName ]


toString : List Url.Builder.QueryParameter -> Endpoint -> String
toString query endpoint =
    builder endpoint
        |> appendQuery query
        |> RouteBuilder.build


builder : Endpoint -> RouteBuilder
builder endpoint =
    case endpoint of
        PipelinesList ->
            base |> appendPath [ "pipelines" ]

        Pipeline id subEndpoint ->
            pipeline id |> append (pipelineEndpoint subEndpoint)

        JobsList ->
            base |> appendPath [ "jobs" ]

        Job id subEndpoint ->
            pipeline id
                |> appendPath [ "jobs", id.jobName ]
                |> append (jobEndpoint subEndpoint)

        JobBuild id ->
            pipeline id |> appendPath [ "jobs", id.jobName, "builds", id.buildName ]

        Build id subEndpoint ->
            base
                |> appendPath [ "builds", String.fromInt id ]
                |> append (buildEndpoint subEndpoint)

        ResourcesList ->
            base |> appendPath [ "resources" ]

        Resource id subEndpoint ->
            resource id |> append (resourceEndpoint subEndpoint)

        ResourceVersion id subEndpoint ->
            resource id
                |> appendPath [ "versions", String.fromInt id.versionID ]
                |> append (resourceVersionEndpoint subEndpoint)

        TeamsList ->
            base |> appendPath [ "teams" ]

        Team teamName subEndpoint ->
            base
                |> appendPath [ "teams", teamName ]
                |> append (teamEndpoint subEndpoint)

        ClusterInfo ->
            base |> appendPath [ "info" ]

        Wall ->
            base |> appendPath [ "wall" ]

        Cli ->
            base |> appendPath [ "cli" ]

        UserInfo ->
            base |> appendPath [ "user" ]

        Logout ->
            baseSky |> appendPath [ "logout" ]

        InstanceGroup { teamName, name } subEndpoint ->
            base
                |> appendPath [ "teams", teamName ]
                |> appendPath [ "pipelines", name ]
                |> append (instanceGroupEndpoint subEndpoint)

        BuildAgentReviews buildId ->
            base |> appendPath [ "builds", String.fromInt buildId, "agent-reviews" ]

        BuildAgentMetrics buildId ->
            base |> appendPath [ "builds", String.fromInt buildId, "agent-metrics" ]

        TeamAgentReviews teamName ->
            base |> appendPath [ "teams", teamName, "agent-reviews" ]

        AgentFeedback ->
            base |> appendPath [ "agent", "feedback" ]

        AgentMetrics ->
            base |> appendPath [ "agent", "metrics" ]

        AgentWorkflowsList ->
            base |> appendPath [ "agent", "workflows" ]

        AgentCostRollup ->
            base |> appendPath [ "agent", "costs" ]

        AgentDispatcher ->
            base |> appendPath [ "agent", "dispatcher" ]

        AgentCredentialsStatus ->
            base |> appendPath [ "agent", "user-credentials" ]

        AgentPrincipalsList ->
            base |> appendPath [ "agent", "principals" ]

        AgentPrincipal principalId ->
            base |> appendPath [ "agent", "principals", String.fromInt principalId ]

        AgentTicketsList ->
            base |> appendPath [ "agent", "tickets" ]

        AgentTicket ticketId ->
            base |> appendPath [ "agent", "tickets", String.fromInt ticketId ]

        AgentTicketState ticketId ->
            base |> appendPath [ "agent", "tickets", String.fromInt ticketId, "state" ]

        AgentTicketDispatch ticketId ->
            base |> appendPath [ "agent", "tickets", String.fromInt ticketId, "dispatch" ]

        AgentTicketTask ticketId ordering ->
            base |> appendPath [ "agent", "tickets", String.fromInt ticketId, "tasks", String.fromInt ordering ]

        AgentTicketMetrics ticketId ->
            base |> appendPath [ "agent", "tickets", String.fromInt ticketId, "metrics" ]

        AgentWorkflowVersions workflowName ->
            base |> appendPath [ "agent", "workflows", Url.percentEncode workflowName, "versions" ]

        AgentWorkflowVersionLive workflowName version ->
            base |> appendPath [ "agent", "workflows", Url.percentEncode workflowName, "versions", String.fromInt version, "live" ]

        AgentWorkflowRuns workflowName ->
            base |> appendPath [ "agent", "workflows", Url.percentEncode workflowName, "runs" ]

        AgentWorkflowRunOperationalStatusCounts workflowName ->
            base |> appendPath [ "agent", "workflows", Url.percentEncode workflowName, "runs", "operational-status-counts" ]

        AgentWorkflowRun workflowName workflowRunId ->
            base |> appendPath [ "agent", "workflows", Url.percentEncode workflowName, "runs", workflowRunId ]

        AgentWorkflowRunCancel workflowName workflowRunId ->
            base |> appendPath [ "agent", "workflows", Url.percentEncode workflowName, "runs", workflowRunId, "cancel" ]

        AgentWorkflowRunRetry workflowName workflowRunId ->
            base |> appendPath [ "agent", "workflows", Url.percentEncode workflowName, "runs", workflowRunId, "retry" ]

        AgentWorkflowRunWaits workflowName workflowRunId ->
            base |> appendPath [ "agent", "workflows", Url.percentEncode workflowName, "runs", workflowRunId, "waits" ]

        AgentWorkflowWaitResolve workflowName workflowRunId waitId ->
            base |> appendPath [ "agent", "workflows", Url.percentEncode workflowName, "runs", workflowRunId, "waits", waitId, "resolve" ]

        AgentWorkflowRunOutcomes workflowName workflowRunId ->
            base |> appendPath [ "agent", "workflows", Url.percentEncode workflowName, "runs", workflowRunId, "outcomes" ]

        AgentWorkflowRunMetrics workflowName workflowRunId ->
            base |> appendPath [ "agent", "workflows", Url.percentEncode workflowName, "runs", workflowRunId, "metrics" ]

        AgentWorkflowRunReviews workflowName workflowRunId ->
            base |> appendPath [ "agent", "workflows", Url.percentEncode workflowName, "runs", workflowRunId, "reviews" ]

        AgentSnapshot teamName snapshotId ->
            base |> appendPath [ "teams", teamName, "agent", "snapshots", snapshotId ]

        AgentSnapshotContent teamName snapshotId ->
            base |> appendPath [ "teams", teamName, "agent", "snapshots", snapshotId, "content" ]

        AgentSnapshotPin teamName snapshotId ->
            base |> appendPath [ "teams", teamName, "agent", "snapshots", snapshotId, "pin" ]

        AgentSnapshotRepositoryChange teamName snapshotId ->
            base |> appendPath [ "teams", teamName, "agent", "snapshots", snapshotId, "projections", "repository-change" ]

        AgentSnapshotReview snapshotId ->
            base |> appendPath [ "agent", "snapshots", snapshotId, "projections", "review" ]

        AgentExperimentsList ->
            base |> appendPath [ "agent", "experiments" ]

        AgentExperiment experimentId ->
            base |> appendPath [ "agent", "experiments", experimentId ]

        AgentExperimentCells experimentId ->
            base |> appendPath [ "agent", "experiments", experimentId, "cells" ]

        AgentExperimentScorecard experimentId ->
            base |> appendPath [ "agent", "experiments", experimentId, "scorecard" ]

        AgentExperimentCancel experimentId ->
            base |> appendPath [ "agent", "experiments", experimentId, "cancel" ]


pipelineEndpoint : PipelineEndpoint -> RouteBuilder
pipelineEndpoint endpoint =
    case endpoint of
        BasePipeline ->
            ( [], [] )

        PausePipeline ->
            ( [ "pause" ], [] )

        UnpausePipeline ->
            ( [ "unpause" ], [] )

        ExposePipeline ->
            ( [ "expose" ], [] )

        HidePipeline ->
            ( [ "hide" ], [] )

        PipelineJobsList ->
            ( [ "jobs" ], [] )

        PipelineResourcesList ->
            ( [ "resources" ], [] )

        PipelineRunsList ->
            -- template runs are refetched every 5s alongside the pipeline and
            -- each row ships its full params JSONB, so cap the page size
            -- instead of taking the server default of 100
            ( [ "runs" ], [ Url.Builder.int "limit" 25 ] )


jobEndpoint : JobEndpoint -> RouteBuilder
jobEndpoint endpoint =
    ( case endpoint of
        BaseJob ->
            []

        PauseJob ->
            [ "pause" ]

        UnpauseJob ->
            [ "unpause" ]

        JobBuildsList ->
            [ "builds" ]
    , []
    )


buildEndpoint : BuildEndpoint -> RouteBuilder
buildEndpoint endpoint =
    ( case endpoint of
        BaseBuild ->
            []

        BuildPlan ->
            [ "plan" ]

        BuildPrep ->
            [ "preparation" ]

        AbortBuild ->
            [ "abort" ]

        BuildResourcesList ->
            [ "resources" ]

        BuildEventStream ->
            [ "events" ]

        SetComment ->
            [ "comment" ]
    , []
    )


resourceEndpoint : ResourceEndpoint -> RouteBuilder
resourceEndpoint endpoint =
    ( case endpoint of
        BaseResource ->
            []

        ResourceVersionsList ->
            [ "versions" ]

        UnpinResource ->
            [ "unpin" ]

        CheckResource ->
            [ "check" ]

        PinResourceComment ->
            [ "pin_comment" ]
    , []
    )


resourceVersionEndpoint : ResourceVersionEndpoint -> RouteBuilder
resourceVersionEndpoint endpoint =
    ( case endpoint of
        BaseResourceVersion ->
            []

        ResourceVersionInputTo ->
            [ "input_to" ]

        ResourceVersionOutputOf ->
            [ "output_of" ]

        PinResourceVersion ->
            [ "pin" ]

        EnableResourceVersion ->
            [ "enable" ]

        DisableResourceVersion ->
            [ "disable" ]

        DownstreamCausality ->
            [ "downstream" ]

        UpstreamCasuality ->
            [ "upstream" ]
    , []
    )


teamEndpoint : TeamEndpoint -> RouteBuilder
teamEndpoint endpoint =
    ( case endpoint of
        TeamPipelinesList ->
            [ "pipelines" ]

        OrderTeamPipelines ->
            [ "pipelines", "ordering" ]
    , []
    )


instanceGroupEndpoint : InstanceGroupEndpoint -> RouteBuilder
instanceGroupEndpoint endpoint =
    ( case endpoint of
        OrderInstanceGroupPipelines ->
            [ "ordering" ]
    , []
    )
