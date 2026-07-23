module AgentWorkflowPageTests exposing (all)

import AgenticData
import Application.Application as Application
import Common
import Expect
import Html.Attributes as Attr
import Message.Callback as Callback
import Message.Effects as Effects
import Message.Message as Message
import Message.Subscription exposing (Delivery(..), Interval(..))
import Message.TopLevelMessage as Msgs
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (attribute, class, containing, id, text)
import Time


all : Test
all =
    describe "workflow detail"
        [ test "shows the frozen typed signature and source hash" <|
            \_ ->
                initialized
                    |> Common.queryView
                    |> Query.find [ class "agent-workflow-signature-detail" ]
                    |> Query.has
                        [ containing [ text "repository : repository/v1" ]
                        , containing [ text "review : review/v1" ]
                        , containing [ text "schema v3 · signature v1 · #abcdef0123456789" ]
                        ]
        , test "starts a run from validated snapshot bindings and a pinned definition version" <|
            \_ ->
                initialized
                    |> Application.update
                        (Msgs.Update <|
                            Message.AgentWorkflowInputChanged "repository" "9007199254740995"
                        )
                    |> Tuple.first
                    |> Application.update (Msgs.Update Message.AgentWorkflowStartClicked)
                    |> Tuple.second
                    |> Common.contains
                        (Effects.CreateAgentWorkflowRun
                            { workflowName = "review-api"
                            , version = Just 3
                            , inputs = [ ( "repository", "9007199254740995" ) ]
                            }
                        )
        , test "links exact durable run IDs and labels operational state" <|
            \_ ->
                initialized
                    |> Application.handleCallback
                        (Callback.AgentWorkflowRunsFetched "review-api" (Ok [ AgenticData.runSummary ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-workflow-run-row" ]
                    |> Query.has
                        [ attribute (Attr.href "/agent/workflows/review-api/runs/9007199254740993")
                        , containing [ text "running" ]
                        , containing [ text "manual" ]
                        ]
        , test "promotion is explicit and version-scoped" <|
            \_ ->
                initialized
                    |> Application.update
                        (Msgs.Update <| Message.AgentWorkflowPromoteClicked 3)
                    |> Tuple.second
                    |> Common.contains (Effects.PromoteAgentWorkflowVersion "review-api" 3)
        , test "keeps the agent product identity in the global breadcrumb" <|
            \_ ->
                initialized
                    |> Common.queryView
                    |> Query.find [ id "breadcrumbs" ]
                    |> Query.has
                        [ containing [ text "agent" ]
                        , containing [ text "review-api" ]
                        ]
        , test "refreshes a workflow with active runs on the bounded five second cadence" <|
            \_ ->
                initialized
                    |> Application.handleCallback
                        (Callback.AgentWorkflowRunsFetched "review-api" (Ok [ AgenticData.runSummary ]))
                    |> Tuple.first
                    |> Application.update
                        (Msgs.DeliveryReceived (ClockTicked FiveSeconds (Time.millisToPosix 0)))
                    |> Tuple.second
                    |> Expect.all
                        [ Common.contains (Effects.FetchAgentWorkflowVersions "review-api")
                        , Common.contains (Effects.FetchAgentWorkflowRuns "review-api")
                        , Common.contains Effects.FetchAgentExperiments
                        ]
        , test "removes the workflow refresh timer after all visible runs settle" <|
            \_ ->
                initialized
                    |> Application.handleCallback
                        (Callback.AgentWorkflowRunsFetched
                            "review-api"
                            (Ok [ terminalRunSummary ])
                        )
                    |> Tuple.first
                    |> Application.update
                        (Msgs.DeliveryReceived (ClockTicked FiveSeconds (Time.millisToPosix 0)))
                    |> Tuple.second
                    |> Expect.all
                        [ Common.notContains (Effects.FetchAgentWorkflowVersions "review-api")
                        , Common.notContains (Effects.FetchAgentWorkflowRuns "review-api")
                        ]
        ]


initialized : Application.Model
initialized =
    Common.init "/agent/workflows/review-api"
        |> Application.handleCallback
            (Callback.AgentWorkflowVersionsFetched "review-api" (Ok [ AgenticData.workflowVersion ]))
        |> Tuple.first


terminalRunSummary =
    let
        summary =
            AgenticData.runSummary
    in
    { summary | status = "succeeded" }
