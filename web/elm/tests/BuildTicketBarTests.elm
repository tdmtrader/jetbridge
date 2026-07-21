module BuildTicketBarTests exposing (all)

import Application.Application as Application
import Build.StepTree.Models as STModels
import Common
import Concourse
import Concourse.Agent
import Concourse.BuildStatus exposing (BuildStatus(..))
import Data
import Dict
import Html.Attributes as Attr
import Message.Callback as Callback
import Message.Effects as Effects
import Message.Subscription exposing (Delivery(..))
import Routes
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (attribute, containing, id, tag, text)
import Time


agentBuild : Concourse.Build
agentBuild =
    Data.jobBuild BuildStatusSucceeded
        |> Data.withJob (Just (Data.jobId |> Data.withPipelineName "agent-ticket-12"))


runMetric : Float -> Concourse.Agent.RunMetric
runMetric =
    runMetricFor 1


runMetricFor : Int -> Float -> Concourse.Agent.RunMetric
runMetricFor buildId cost =
    { ticketId = Just 12
    , pipelineRunId = Nothing
    , buildId = buildId
    , planId = "plan"
    , stepName = "implement"
    , workflowName = "standard-dev"
    , workflowVersion = Nothing
    , status = "ok"
    , buildStatus = ""
    , outcome = ""
    , summary = ""
    , model = "claude"
    , usage =
        { inputTokens = 0
        , outputTokens = 0
        , cacheReadInputTokens = 0
        , cacheCreationInputTokens = 0
        }
    , turns = 1
    , wallTimeSeconds = 1
    , costUsd = cost
    , eventCounts = Dict.empty
    , createdAt = 0
    }


all : Test
all =
    describe "build ticket-context bar"
        [ test "links an agent-ticket build back to its ticket" <|
            \_ ->
                Common.init "/builds/1"
                    |> Application.handleCallback (Callback.BuildFetched (Ok agentBuild))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ id "build-ticket-context" ]
                    |> Query.has
                        [ tag "a"
                        , attribute (Attr.href "/agent/tickets/12")
                        , containing [ text "agent ticket #12" ]
                        ]
        , test "shows no context bar for an ordinary build" <|
            \_ ->
                Common.init "/builds/1"
                    |> Application.handleCallback
                        (Callback.BuildFetched (Ok (Data.jobBuild BuildStatusSucceeded)))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.hasNot [ id "build-ticket-context" ]
        , test "fetches agent metrics when the build is first fetched" <|
            \_ ->
                Common.init "/builds/1"
                    |> Application.handleCallback (Callback.BuildFetched (Ok agentBuild))
                    |> Tuple.second
                    |> Common.contains (Effects.FetchBuildAgentMetrics 1)
        , test "sums run costs into a chip on the ticket-context bar" <|
            \_ ->
                Common.init "/builds/1"
                    |> Application.handleCallback (Callback.BuildFetched (Ok agentBuild))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.BuildAgentMetricsFetched (Ok [ runMetric 0.5, runMetric 0.25 ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ id "build-agent-cost" ]
                    |> Query.has [ text "$0.75" ]
        , test "shows the cost chip even for a build outside any ticket" <|
            \_ ->
                Common.init "/builds/1"
                    |> Application.handleCallback
                        (Callback.BuildFetched (Ok (Data.jobBuild BuildStatusSucceeded)))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.BuildAgentMetricsFetched (Ok [ runMetric 1.25 ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ id "build-agent-cost" ]
                    |> Query.has [ text "$1.25" ]
        , test "shows no cost chip when the build has no agent runs" <|
            \_ ->
                Common.init "/builds/1"
                    |> Application.handleCallback (Callback.BuildFetched (Ok agentBuild))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.BuildAgentMetricsFetched (Ok []))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.hasNot [ id "build-agent-cost" ]
        , test "refetches agent metrics when switching builds through history" <|
            \_ ->
                switchToBuildTwo
                    |> Tuple.second
                    |> Common.contains (Effects.FetchBuildAgentMetrics 2)
        , test "clears the previous build's spend when switching builds" <|
            \_ ->
                switchToBuildTwo
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.hasNot [ id "build-agent-cost" ]
        , test "drops a late metrics response that belongs to the previous build" <|
            \_ ->
                switchToBuildTwo
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.BuildAgentMetricsFetched (Ok [ runMetricFor 1 0.5 ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.hasNot [ id "build-agent-cost" ]
        , test "refetches agent metrics when a live-watched build finishes" <|
            \_ ->
                Common.init "/teams/t/pipelines/p/jobs/j/builds/1"
                    |> Application.handleCallback
                        (Callback.BuildFetched (Ok (Data.jobBuild BuildStatusStarted)))
                    |> Tuple.first
                    |> Application.handleDelivery
                        (EventsReceived
                            (Ok
                                [ { url = "http://localhost:8080/api/v1/builds/1/events"
                                  , data = STModels.BuildStatus BuildStatusSucceeded (Time.millisToPosix 0)
                                  }
                                ]
                            )
                        )
                    |> Tuple.second
                    |> Common.contains (Effects.FetchBuildAgentMetrics 1)
        ]


{-| Load build 1 with recorded spend, then switch to build 2 in-app the way
users do: through the fetched history strip (Header.changeToBuild stamps
model.id from history before build 2's BuildFetched arrives).
-}
switchToBuildTwo : ( Application.Model, List Effects.Effect )
switchToBuildTwo =
    let
        buildTwo =
            Data.jobBuild BuildStatusSucceeded
                |> (\b -> { b | id = 2, name = "2" })
    in
    Common.init "/teams/t/pipelines/p/jobs/j/builds/1"
        |> Application.handleCallback
            (Callback.BuildFetched (Ok (Data.jobBuild BuildStatusSucceeded)))
        |> Tuple.first
        |> Application.handleCallback
            (Callback.BuildAgentMetricsFetched (Ok [ runMetricFor 1 0.5 ]))
        |> Tuple.first
        |> Application.handleCallback
            (Callback.BuildHistoryFetched
                (Ok
                    { pagination = { previousPage = Nothing, nextPage = Nothing }
                    , content = [ Data.jobBuild BuildStatusSucceeded, buildTwo ]
                    }
                )
            )
        |> Tuple.first
        |> Application.handleDelivery
            (RouteChanged
                (Routes.Build
                    { id = Data.jobBuildId |> (\i -> { i | buildName = "2" })
                    , highlight = Routes.HighlightNothing
                    , groups = []
                    }
                )
            )
        |> Tuple.first
        |> Application.handleCallback (Callback.BuildFetched (Ok buildTwo))
