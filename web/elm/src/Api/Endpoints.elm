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
    | AgentFeedback Concourse.TeamName
    | AgentMetrics
    | AgentWorkflowsList
    | AgentCostRollup
    | AgentDispatcher
    | AgentCredentialsStatus
    | AgentTicketsList
    | AgentTicket Int
    | AgentTicketState Int
    | AgentTicketDispatch Int
    | AgentTicketRuns Int
    | AgentWorkflowVersions String
    | AgentWorkflowVersionLive String Int
    | AgentWorkflowRuns String
    | AgentWorkflowRunsFiltered String (List ( String, String ))
    | AgentWorkflowOverview String (List ( String, String ))
    | AgentWorkflowRunGraph String String
    | AgentWorkflowRunOperationalStatusCounts String
    | AgentWorkflowRun String String
    | AgentWorkflowRunCancel String String
    | AgentWorkflowRunRetry String String
    | AgentWorkflowRunWaits String String
    | AgentWorkflowWaitResolve String String String
    | AgentWorkflowRunOutcomes String String
    | AgentWorkflowRunMetrics String String
    | AgentWorkflowRunTranscripts String String
    | AgentWorkflowRunTranscript String String String
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


{-| An endpoint that carries its own filter query, following the
`PipelineRunsList` precedent. The pairs come from
`AgentWorkflow.Filters`, which is the single place that decides which
parameter names each agent endpoint actually accepts.
-}
pairs : List ( String, String ) -> List Url.Builder.QueryParameter
pairs query =
    List.map (\( key, value ) -> Url.Builder.string key value) query


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

        AgentFeedback teamName ->
            base |> appendPath [ "teams", teamName, "agent", "feedback" ]

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

        AgentTicketsList ->
            base |> appendPath [ "agent", "tickets" ]

        AgentTicket ticketId ->
            base |> appendPath [ "agent", "tickets", String.fromInt ticketId ]

        AgentTicketState ticketId ->
            base |> appendPath [ "agent", "tickets", String.fromInt ticketId, "state" ]

        AgentTicketDispatch ticketId ->
            base |> appendPath [ "agent", "tickets", String.fromInt ticketId, "dispatch" ]

        AgentTicketRuns ticketId ->
            -- The journal is the whole associated history in occurrence order.
            -- It takes no query fields; the endpoint rejects anything it does
            -- not know about.
            base |> appendPath [ "agent", "tickets", String.fromInt ticketId, "runs" ]

        AgentWorkflowVersions workflowName ->
            base |> appendPath [ "agent", "workflows", Url.percentEncode workflowName, "versions" ]

        AgentWorkflowVersionLive workflowName version ->
            base |> appendPath [ "agent", "workflows", Url.percentEncode workflowName, "versions", String.fromInt version, "live" ]

        AgentWorkflowRuns workflowName ->
            -- The run list API defaults to the attention lens, which is right for
            -- the workflow page's "is anything unresolved?" question and wrong
            -- for a plain history read. The unfiltered caller says so.
            base
                |> appendPath [ "agent", "workflows", Url.percentEncode workflowName, "runs" ]
                |> appendQuery (pairs [ ( "lens", "all" ) ])

        AgentWorkflowRunsFiltered workflowName query ->
            base
                |> appendPath [ "agent", "workflows", Url.percentEncode workflowName, "runs" ]
                |> appendQuery (pairs query)

        AgentWorkflowOverview workflowName query ->
            base
                |> appendPath [ "agent", "workflows", Url.percentEncode workflowName, "overview" ]
                |> appendQuery (pairs query)

        AgentWorkflowRunGraph workflowName workflowRunId ->
            base
                |> appendPath
                    [ "agent", "workflows", Url.percentEncode workflowName, "runs", workflowRunId, "graph" ]

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

        AgentWorkflowRunTranscripts workflowName workflowRunId ->
            base |> appendPath [ "agent", "workflows", Url.percentEncode workflowName, "runs", workflowRunId, "transcripts" ]

        AgentWorkflowRunTranscript workflowName workflowRunId planId ->
            base |> appendPath [ "agent", "workflows", Url.percentEncode workflowName, "runs", workflowRunId, "transcripts", Url.percentEncode planId ]

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
