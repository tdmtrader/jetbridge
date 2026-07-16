module AgentPageTests exposing (all)

import Application.Application as Application
import Common
import Data
import Dict
import Message.Callback as Callback
import Message.Effects as Effects
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (containing, text)
import Url


sampleRun =
    { ticketId = Just 101
    , pipelineRunId = Just 5001
    , buildId = 9002
    , planId = "p2"
    , stepName = "implement"
    , workflowName = "standard-dev"
    , workflowVersion = Just 3
    , status = "ok"
    , summary = "Implemented tasks 1-4"
    , model = "claude-sonnet-4-5"
    , usage =
        { inputTokens = 180000
        , outputTokens = 41000
        , cacheReadInputTokens = 22000
        , cacheCreationInputTokens = 6000
        }
    , turns = 31
    , wallTimeSeconds = 640
    , costUsd = 2.87
    , eventCounts = Dict.fromList [ ( "tool.call", 88 ) ]
    , createdAt = 0
    }


samplePrincipal =
    { id = 3
    , name = "agent-step"
    , description = "Native agent step metrics ingest"
    , tokenPrefix = "agntpk_9c1b83"
    , scopes = [ "metrics:write" ]
    , teamName = "main"
    , createdBy = "test"
    , createdAt = 0
    , expiresAt = Nothing
    , revokedAt = Nothing
    , lastUsedAt = Just 1
    }


initAgent : ( Application.Model, List Effects.Effect )
initAgent =
    Application.init Data.flags
        { protocol = Url.Http
        , host = ""
        , port_ = Nothing
        , path = "/teams/main/agent"
        , query = Nothing
        , fragment = Nothing
        }


all : Test
all =
    describe "agent platform page"
        [ test "fetches runs on load" <|
            \_ ->
                initAgent
                    |> Tuple.second
                    |> Common.contains Effects.FetchAgentRunMetrics
        , test "fetches costs on load" <|
            \_ ->
                initAgent
                    |> Tuple.second
                    |> Common.contains Effects.FetchAgentCosts
        , test "fetches principals on load" <|
            \_ ->
                initAgent
                    |> Tuple.second
                    |> Common.contains Effects.FetchAgentPrincipals
        , test "renders run rows with step, workflow and cost" <|
            \_ ->
                Common.init "/teams/main/agent"
                    |> Application.handleCallback
                        (Callback.AgentRunMetricsFetched (Ok [ sampleRun ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has
                        [ containing [ text "implement" ]
                        , containing [ text "standard-dev@3" ]
                        , containing [ text "$2.87" ]
                        ]
        , test "renders an error notice when runs fail to load" <|
            \_ ->
                Common.init "/teams/main/agent"
                    |> Application.handleCallback
                        (Callback.AgentRunMetricsFetched Data.httpUnauthorized)
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has [ containing [ text "Couldn't load recent agent runs." ] ]
        , test "renders an empty notice when there are no runs" <|
            \_ ->
                Common.init "/teams/main/agent"
                    |> Application.handleCallback
                        (Callback.AgentRunMetricsFetched (Ok []))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has [ containing [ text "No agent runs recorded yet." ] ]
        , test "renders a principal with a revoke action" <|
            \_ ->
                Common.init "/teams/main/agent"
                    |> Application.handleCallback
                        (Callback.AgentPrincipalsFetched (Ok [ samplePrincipal ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has
                        [ containing [ text "agent-step" ]
                        , containing [ text "Revoke" ]
                        ]
        ]
