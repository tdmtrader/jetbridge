module BuildTicketBarTests exposing (all)

import Application.Application as Application
import Common
import Concourse
import Concourse.Agent
import Concourse.BuildStatus exposing (BuildStatus(..))
import Data
import Dict
import Html.Attributes as Attr
import Message.Callback as Callback
import Message.Effects as Effects
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (attribute, containing, id, tag, text)


agentBuild : Concourse.Build
agentBuild =
    Data.jobBuild BuildStatusSucceeded
        |> Data.withJob (Just (Data.jobId |> Data.withPipelineName "agent-ticket-12"))


runMetric : Float -> Concourse.Agent.RunMetric
runMetric cost =
    { ticketId = Just 12
    , pipelineRunId = Nothing
    , buildId = 1
    , planId = "plan"
    , stepName = "implement"
    , workflowName = "standard-dev"
    , workflowVersion = Nothing
    , status = "ok"
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
                        , attribute (Attr.href "/agent-tickets/12")
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
        ]
