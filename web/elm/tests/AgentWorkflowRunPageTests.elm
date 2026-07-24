module AgentWorkflowRunPageTests exposing (all)

import AgenticData
import Application.Application as Application
import Common
import Concourse.Agent
import Dict
import Expect
import Html.Attributes as Attr
import Message.Callback as Callback
import Message.Effects as Effects
import Message.Message as Message
import Message.Subscription exposing (Delivery(..), Interval(..))
import Message.TopLevelMessage as Msgs
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (attribute, class, containing, text)
import Time


all : Test
all =
    describe "durable workflow run detail"
        [ test "shows immutable hashes, origin, build execution, and snapshot lineage" <|
            \_ ->
                initialized
                    |> Common.queryView
                    |> Query.has
                        [ class "agent-run-identity"
                        , containing [ text "parameterized-hash" ]
                        , containing [ text "instance-hash" ]
                        , containing [ text "actual-plan-hash" ]
                        , class "agent-run-build-link"
                        , attribute (Attr.href "/builds/42")
                        , attribute (Attr.href "/agent/snapshots/9007199254740995")
                        , attribute (Attr.href "/agent/snapshots/9007199254740997")
                        ]
        , test "renders bounded repository-change projection from the server" <|
            \_ ->
                initialized
                    |> Application.handleCallback
                        (Callback.AgentSnapshotRepositoryChangeFetched
                            AgenticData.repositoryChange.snapshotId
                            (Ok AgenticData.repositoryChange)
                        )
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-repository-change" ]
                    |> Query.has
                        [ containing [ text "1 files · +3 −1" ]
                        , containing [ class "agent-unified-diff", text "diff --git a/src/a.go b/src/a.go\n+fixed" ]
                        ]
        , test "resolves a sealed human question without requiring a manual snapshot upload" <|
            \_ ->
                initialized
                    |> Application.handleCallback
                        (Callback.AgentWorkflowWaitsFetched AgenticData.runSummary.id (Ok [ AgenticData.wait ]))
                    |> Tuple.first
                    |> Application.update
                        (Msgs.Update <|
                            Message.AgentWaitAnswerChanged AgenticData.wait.id "approve"
                        )
                    |> Tuple.first
                    |> Application.update
                        (Msgs.Update <| Message.AgentWaitResolveClicked AgenticData.wait.id)
                    |> Tuple.second
                    |> Common.contains
                        (Effects.ResolveAgentWorkflowWait
                            "review-api"
                            AgenticData.runSummary.id
                            AgenticData.wait.id
                            "approve"
                        )
        , test "attributes review feedback to the exact review snapshot" <|
            \_ ->
                initialized
                    |> Application.update
                        (Msgs.Update <|
                            Message.AgentReviewVerdictClicked
                                { reviewSnapshotId = Just "9007199254740997"
                                , repo = "concourse"
                                , commitSha = "abc123"
                                , findingId = "finding-1"
                                , verdict = "accurate"
                                , reviewer = "alice"
                                }
                        )
                    |> Tuple.second
                    |> Common.contains
                        (Effects.SubmitAgentReviewVerdict
                            { reviewSnapshotId = Just "9007199254740997"
                            , repo = "concourse"
                            , commitSha = "abc123"
                            , findingId = "finding-1"
                            , verdict = "accurate"
                            , notes = ""
                            , reviewer = "alice"
                            }
                        )
        , test "refreshes every mutable run projection while the run is active" <|
            \_ ->
                initialized
                    |> Application.update
                        (Msgs.DeliveryReceived (ClockTicked FiveSeconds (Time.millisToPosix 0)))
                    |> Tuple.second
                    |> Expect.all
                        [ Common.contains
                            (Effects.FetchAgentWorkflowRun "review-api" AgenticData.runSummary.id)
                        , Common.contains
                            (Effects.FetchAgentWorkflowWaits "review-api" AgenticData.runSummary.id)
                        , Common.contains
                            (Effects.FetchAgentWorkflowOutcomes "review-api" AgenticData.runSummary.id)
                        , Common.contains
                            (Effects.FetchAgentWorkflowReviews "review-api" AgenticData.runSummary.id)
                        , Common.contains
                            (Effects.FetchAgentWorkflowRunMetrics "review-api" AgenticData.runSummary.id)
                        ]
        , test "keeps refreshing a terminal run while an output projection is pending" <|
            \_ ->
                initialized
                    |> Application.handleCallback
                        (Callback.AgentWorkflowRunFetched
                            AgenticData.runSummary.id
                            (Ok terminalRunDetail)
                        )
                    |> Tuple.first
                    |> Application.update
                        (Msgs.DeliveryReceived (ClockTicked FiveSeconds (Time.millisToPosix 0)))
                    |> Tuple.second
                    |> Common.contains
                        (Effects.FetchAgentWorkflowRun "review-api" AgenticData.runSummary.id)
        , test "removes the run refresh timer once terminal output projections settle" <|
            \_ ->
                initialized
                    |> Application.handleCallback
                        (Callback.AgentWorkflowRunFetched
                            AgenticData.runSummary.id
                            (Ok terminalRunDetail)
                        )
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentSnapshotRepositoryChangeFetched
                            AgenticData.repositoryChange.snapshotId
                            (Ok AgenticData.repositoryChange)
                        )
                    |> Tuple.first
                    |> Application.update
                        (Msgs.DeliveryReceived (ClockTicked FiveSeconds (Time.millisToPosix 0)))
                    |> Tuple.second
                    |> Common.notContains
                        (Effects.FetchAgentWorkflowRun "review-api" AgenticData.runSummary.id)
        , test "populates run telemetry from this run's metrics" <|
            \_ ->
                initialized
                    |> Application.handleCallback
                        (Callback.AgentWorkflowRunMetricsFetched AgenticData.runSummary.id (Ok [ sampleMetric ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-run-telemetry" ]
                    |> Query.has [ text "1 steps" ]
        , test "the run-qualified callback ignores another run's metrics" <|
            \_ ->
                initialized
                    -- results tagged with a different run id must not land here,
                    -- so two open run pages cannot accept each other's metrics
                    |> Application.handleCallback
                        (Callback.AgentWorkflowRunMetricsFetched "9007199254740000" (Ok [ sampleMetric ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-run-telemetry" ]
                    |> Query.has [ text "0 steps" ]
        , test "bounds terminal projection refresh attempts" <|
            \_ ->
                let
                    terminalPending =
                        initialized
                            |> Application.handleCallback
                                (Callback.AgentWorkflowRunFetched
                                    AgenticData.runSummary.id
                                    (Ok terminalRunDetail)
                                )
                            |> Tuple.first

                    exhausted =
                        List.range 1 20
                            |> List.foldl
                                (\_ model ->
                                    model
                                        |> Application.update
                                            (Msgs.DeliveryReceived (ClockTicked FiveSeconds (Time.millisToPosix 0)))
                                        |> Tuple.first
                                )
                                terminalPending
                in
                exhausted
                    |> Application.update
                        (Msgs.DeliveryReceived (ClockTicked FiveSeconds (Time.millisToPosix 0)))
                    |> Tuple.second
                    |> Common.notContains
                        (Effects.FetchAgentWorkflowRun "review-api" AgenticData.runSummary.id)
        ]


initialized : Application.Model
initialized =
    Common.init "/agent/workflows/review-api/runs/9007199254740993"
        |> Application.handleCallback
            (Callback.AgentWorkflowRunFetched AgenticData.runSummary.id (Ok AgenticData.runDetail))
        |> Tuple.first


terminalRunDetail =
    let
        detail =
            AgenticData.runDetail

        summary =
            AgenticData.runSummary
    in
    { detail | summary = { summary | status = "succeeded" } }


{-| A metric bound to this run's workflow and planned build (buildId 42 matches
AgenticData.runSummary.plannedBuildId) so the telemetry card counts it.
-}
sampleMetric : Concourse.Agent.RunMetric
sampleMetric =
    { workflowRunId = Just AgenticData.runSummary.id
    , functionId = "review"
    , buildId = 42
    , planId = "p1"
    , stepName = "review-diff"
    , workflowName = "review-api"
    , workflowVersion = Just 1
    , status = "ok"
    , buildStatus = "succeeded"
    , outcome = "ok"
    , summary = "did it"
    , model = "claude"
    , usage =
        { inputTokens = 100
        , outputTokens = 50
        , cacheReadInputTokens = 0
        , cacheCreationInputTokens = 0
        }
    , turns = 4
    , wallTimeSeconds = 10
    , costUsd = 2
    , eventCounts = Dict.empty
    , createdAt = 0
    }
