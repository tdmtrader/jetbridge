module AgentPageTests exposing (all)

import Application.Application as Application
import Common
import Data
import Http
import Message.Callback as Callback
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (class, containing, text)
import Time


sampleWorkflow :
    { name : String
    , description : String
    , latestVersion : Int
    , contentHash : String
    , liveVersion : Int
    , createdAt : Time.Posix
    }
sampleWorkflow =
    { name = "standard-dev"
    , description = "the five-phase dev flow"
    , latestVersion = 2
    , contentHash = "abcdef0123456789cafe"
    , liveVersion = 1
    , createdAt = Time.millisToPosix 0
    }


sampleRollup :
    { groupBy : String
    , summary :
        { dailyCapUsd : Float
        , dailySpentUsd : Float
        , dailyRemainingUsd : Float
        , dailyExhausted : Bool
        }
    , rows :
        List
            { key : String
            , entries : Int
            , inputTokens : Int
            , outputTokens : Int
            , turns : Int
            , costUsd : Float
            }
    }
sampleRollup =
    { groupBy = "day"
    , summary =
        { dailyCapUsd = 20
        , dailySpentUsd = 12.34
        , dailyRemainingUsd = 7.66
        , dailyExhausted = False
        }
    , rows =
        [ { key = "2026-07-11"
          , entries = 4
          , inputTokens = 1000
          , outputTokens = 2000
          , turns = 6
          , costUsd = 3.5
          }
        ]
    }


all : Test
all =
    describe "agent page"
        [ test "fetches workflows and cost rollup on load" <|
            \_ ->
                Common.init "/agent"
                    |> Common.queryView
                    |> Query.has [ text "Agent" ]
        , test "renders a workflow name with a live indicator" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentWorkflowsFetched (Ok [ sampleWorkflow ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-workflow-row" ]
                    |> Query.has
                        [ containing [ text "standard-dev" ]
                        , containing [ class "agent-workflow-live", text "live" ]
                        , containing [ text "candidate v2" ]
                        ]
        , test "shows an empty state when there are no workflows" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentWorkflowsFetched (Ok []))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has [ text "no workflow definitions — import one with: fly agent workflows import" ]
        , test "renders the cost row with its formatted cost" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentCostRollupFetched (Ok sampleRollup))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-cost-row" ]
                    |> Query.has [ containing [ text "$3.50" ] ]
        , test "renders the daily spend summary line" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentCostRollupFetched (Ok sampleRollup))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has [ text "today: $12.34 spent / $20.00 cap ($7.66 left)" ]
        , test "shows an admin-only message when workflows fetch is forbidden" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentWorkflowsFetched Data.httpForbidden)
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has
                        [ text "not authorized — the agent workflows API is admin-only" ]
        , test "shows a generic error message when costs fetch fails" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentCostRollupFetched (Err Http.NetworkError))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has [ text "couldn't load costs" ]
        ]
