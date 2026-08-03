module AgentWorkflowRunPageTests exposing (all)

import AgentGraph.Model
import AgenticData
import Application.Application as Application
import Common
import Concourse.Agent
import Concourse.Transcript
import Concourse.WorkflowRun
import Concourse.WorkflowRunGraph
import Data
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
import Test.Html.Selector exposing (attribute, class, containing, tag, text)
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
        , test "renders the pipeline run as an execution diagnostic, not an identity" <|
            \_ ->
                -- pipeline_run_id was decoded and rendered nowhere. It is a
                -- correlation handle for pipeline-side logs, so it renders —
                -- small, and clearly subordinate to the durable run ID.
                initialized
                    |> Common.queryView
                    |> Query.find [ class "agent-run-pipeline-diagnostic" ]
                    |> Query.has [ text "pipeline run 12" ]
        , test "omits the pipeline diagnostic when the run has no pipeline run" <|
            \_ ->
                withSummary (\s -> { s | pipelineRunId = Nothing })
                    |> Common.queryView
                    |> Query.hasNot [ class "agent-run-pipeline-diagnostic" ]
        , test "links a retry back to the run it retried" <|
            \_ ->
                -- Without this a retry was indistinguishable from an original
                -- run, and the failure whose frozen inputs it reuses was
                -- unreachable from it.
                withSummary (\s -> { s | retryOf = Just "9007199254740991" })
                    |> Common.queryView
                    |> Query.find [ class "agent-run-retry-of" ]
                    |> Expect.all
                        [ Query.has [ text "retry of run #9007199254740991" ]
                        , Query.has
                            [ attribute
                                (Attr.href
                                    "/agent/workflows/review-api/runs/9007199254740991"
                                )
                            ]
                        ]
        , test "shows no retry badge on an original run" <|
            \_ ->
                initialized
                    |> Common.queryView
                    |> Query.hasNot [ class "agent-run-retry-of" ]
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
                                { teamName = "research"
                                , reviewSnapshotId = "9007199254740997"
                                , findingId = "finding-1"
                                , verdict = "accurate"
                                , reviewer = "alice"
                                }
                        )
                    |> Tuple.second
                    |> Common.contains
                        (Effects.SubmitAgentReviewVerdict
                            { teamName = "research"
                            , reviewSnapshotId = "9007199254740997"
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
                        , Common.contains
                            (Effects.FetchAgentWorkflowRunTranscripts "review-api" AgenticData.runSummary.id)
                        , Common.contains
                            (Effects.FetchAgentWorkflowRunGraph "review-api" AgenticData.runSummary.id)
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
        , describe "agent transcripts"
            [ test "says so when the run captured no transcript" <|
                \_ ->
                    initialized
                        |> Common.queryView
                        |> Query.find [ class "agent-run-transcripts" ]
                        |> Query.has [ text "no agent transcript captured for this run" ]
            , test "lists the steps of the run that are inspectable" <|
                \_ ->
                    withTranscriptIndex
                        |> Common.queryView
                        |> Query.find [ class "agent-run-transcript" ]
                        |> Query.has [ text "implement · implement · 2 KiB" ]
            , test "the run-qualified callback ignores another run's transcripts" <|
                \_ ->
                    initialized
                        |> Application.handleCallback
                            (Callback.AgentWorkflowRunTranscriptsFetched "9007199254740000"
                                (Ok [ sampleTranscriptRef ])
                            )
                        |> Tuple.first
                        |> Common.queryView
                        |> Query.find [ class "agent-run-transcripts" ]
                        |> Query.hasNot [ class "agent-run-transcript" ]
            , test "fetches the ndjson body only when a step is opened" <|
                \_ ->
                    withTranscriptIndex
                        |> Application.update
                            (Msgs.Update <| Message.AgentTranscriptToggled "p1")
                        |> Tuple.second
                        |> Common.contains
                            (Effects.FetchAgentWorkflowRunTranscript
                                "review-api"
                                AgenticData.runSummary.id
                                "p1"
                            )
            , test "renders the fetched transcript as a labeled conversation" <|
                \_ ->
                    openedTranscript
                        |> Common.queryView
                        |> Query.find [ class "agent-transcript" ]
                        |> Expect.all
                            [ Query.has [ class "assistant", containing [ text "reading the repo" ] ]
                            , Query.has [ class "tool-call", containing [ text "tool · Bash" ] ]
                            ]
            , test "keeps a tool body collapsed until its row is opened" <|
                \_ ->
                    openedTranscript
                        |> Common.queryView
                        |> Query.find [ class "agent-transcript" ]
                        |> Expect.all
                            [ Query.hasNot [ text "git status" ]
                            , always
                                (openedTranscript
                                    |> Application.update
                                        (Msgs.Update <| Message.AgentTranscriptEntryToggled "p1" 2)
                                    |> Tuple.first
                                    |> Common.queryView
                                    |> Query.find [ class "agent-transcript" ]
                                    |> Query.has [ text "git status" ]
                                )
                            ]
            , test "a failed index reads as an error, not as 'nothing captured'" <|
                \_ ->
                    initialized
                        |> Application.handleCallback
                            (Callback.AgentWorkflowRunTranscriptsFetched
                                AgenticData.runSummary.id
                                Data.httpInternalServerError
                            )
                        |> Tuple.first
                        |> Common.queryView
                        |> Query.find [ class "agent-run-transcripts" ]
                        |> Expect.all
                            [ Query.has [ text "index could not be loaded" ]
                            , Query.hasNot [ text "no agent transcript captured" ]
                            ]
            , test "surfaces a transcript that could not be loaded" <|
                \_ ->
                    withTranscriptIndex
                        |> Application.update (Msgs.Update <| Message.AgentTranscriptToggled "p1")
                        |> Tuple.first
                        |> Application.handleCallback
                            (Callback.AgentWorkflowRunTranscriptFetched
                                AgenticData.runSummary.id
                                "p1"
                                Data.httpInternalServerError
                            )
                        |> Tuple.first
                        |> Common.queryView
                        |> Query.find [ class "agent-run-transcript" ]
                        |> Query.has [ text "could not be loaded" ]
            ]
        , describe "the exact run DAG"
            [ test "renders the exact run DAG" <|
                \_ ->
                    initializedWithGraph
                        |> Common.queryView
                        |> Query.has [ class "agent-graph" ]
            , test "draws the run's own revision, not the promoted one" <|
                \_ ->
                    initializedWithGraph
                        |> Common.queryView
                        |> Query.find [ class "agent-run-header" ]
                        |> Query.has [ text "v2" ]
            , test "keeps the header to high-signal identity only" <|
                \_ ->
                    initializedWithGraph
                        |> Common.queryView
                        |> Query.find [ class "agent-run-header" ]
                        |> Expect.all
                            [ Query.has [ text "succeeded" ]
                            , Query.hasNot [ class "agent-run-transcript" ]
                            , Query.hasNot [ class "agent-run-identity" ]
                            ]
            , test "invites a selection before one is made" <|
                \_ ->
                    initializedWithGraph
                        |> Common.queryView
                        |> Query.find [ class "agent-node-detail-empty" ]
                        |> Query.has [ text "Select a node" ]
            , test "reveals node detail on selection" <|
                \_ ->
                    selectedImplement
                        |> Common.queryView
                        |> Query.find [ class "agent-node-detail" ]
                        |> Query.has [ text "implement" ]
            , test "re-homes the node's attempts under the selected node" <|
                \_ ->
                    selectedImplement
                        |> Common.queryView
                        |> Query.find [ class "agent-node-detail-attempts" ]
                        |> Expect.all
                            [ Query.has [ text "attempt 1" ]
                            , Query.has [ text "succeeded" ]
                            ]
            , test "re-homes the node's sealed outputs under the selected node" <|
                \_ ->
                    selectedImplement
                        |> Common.queryView
                        |> Query.find [ class "agent-node-detail-outputs" ]
                        |> Query.has [ text "9007199254740997" ]
            , test "re-homes the node's transcript under the selected node" <|
                \_ ->
                    selectedImplementWithTranscript
                        |> Common.queryView
                        |> Query.find [ class "agent-node-detail-transcripts" ]
                        |> Query.has [ class "agent-run-transcript" ]
            , test "keeps run-level outcomes and telemetry below the graph" <|
                \_ ->
                    initializedWithGraph
                        |> Common.queryView
                        |> Expect.all
                            [ Query.has [ class "agent-run-outcomes" ]
                            , Query.has [ class "agent-run-telemetry" ]
                            , Query.has [ class "agent-run-identity" ]
                            ]
            , test "links the durable ticket reference through its live ticket ID" <|
                \_ ->
                    initializedWithTicket
                        |> Common.queryView
                        |> Query.find [ class "agent-run-header-ticket" ]
                        |> Expect.all
                            [ Query.has [ text "ticket-42" ]
                            , Query.has [ attribute (Attr.href "/agent-tickets/42") ]
                            ]
            , test "shows the inherited ticket on a retry even though its origin names the source run" <|
                \_ ->
                    initializedWithTicketRetry
                        |> Common.queryView
                        |> Query.find [ class "agent-run-header-ticket" ]
                        |> Expect.all
                            [ Query.has [ text "ticket-42" ]
                            , Query.has [ attribute (Attr.href "/agent-tickets/42") ]
                            ]
            , test "keeps a standalone retry free of ticket context" <|
                \_ ->
                    -- A retry's origin reference is the run it retried. Linking
                    -- it to /agent-tickets/<run id> would point at an unrelated
                    -- ticket that happens to share the number.
                    initializedWithRetryOrigin
                        |> Common.queryView
                        |> Query.hasNot [ class "agent-run-header-ticket" ]
            , test "retains deleted-ticket evidence without linking to a ticket that is no longer live" <|
                \_ ->
                    initializedWithDeletedTicket
                        |> Common.queryView
                        |> Query.find [ class "agent-run-header-ticket" ]
                        |> Expect.all
                            [ Query.has [ text "ticket-42" ]
                            , Query.hasNot [ tag "a" ]
                            ]
            , test "does not attribute a run-level binding to a node fed by an intermediate of the same name" <|
                \_ ->
                    -- The edge into `review` carries an intermediate snapshot
                    -- named `repository`. The run's own `repository` input is a
                    -- different snapshot, bound to `implement`, and showing it
                    -- here would attribute evidence to the wrong step.
                    selectedReview
                        |> Common.queryView
                        |> Query.find [ class "agent-node-detail-inputs" ]
                        |> Query.has [ text "no inputs" ]
            , test "falls back to the flat cards when the graph could not be derived" <|
                \_ ->
                    initializedWithUnavailableGraph
                        |> Common.queryView
                        |> Expect.all
                            [ Query.has [ class "agent-graph-unavailable" ]
                            , Query.hasNot [ class "agent-graph" ]
                            , Query.has [ class "agent-run-bindings" ]
                            , Query.has [ class "agent-run-transcripts" ]
                            , Query.has [ class "agent-run-waits" ]
                            ]
            , test "keeps a wait that belongs to no node reachable at run level" <|
                \_ ->
                    initializedWithGraphAndOrphanWait
                        |> Common.queryView
                        |> Query.find [ class "agent-run-waits" ]
                        |> Query.has [ text "Ship this change?" ]
            , test "does not repeat a node-attributed wait at run level" <|
                \_ ->
                    initializedWithGraphAndAttributedWait
                        |> Common.queryView
                        |> Query.hasNot [ class "agent-run-waits" ]
            , test "the run-qualified callback ignores another run's graph" <|
                \_ ->
                    initializedWithGraph
                        |> Application.handleCallback
                            (Callback.AgentWorkflowRunGraphFetched "9007199254740999"
                                (Ok { sampleRunGraph | workflowVersion = 99 })
                            )
                        |> Tuple.first
                        |> Common.queryView
                        |> Query.find [ class "agent-run-header" ]
                        |> Query.has [ text "v2" ]
            ]
        ]


sampleTranscriptRef : Concourse.Transcript.Ref
sampleTranscriptRef =
    { planId = "p1"
    , functionId = "implement"
    , stepName = "implement"
    , buildId = 42
    , byteLen = 2048
    , truncated = False
    }


withTranscriptIndex : Application.Model
withTranscriptIndex =
    initialized
        |> Application.handleCallback
            (Callback.AgentWorkflowRunTranscriptsFetched AgenticData.runSummary.id
                (Ok [ sampleTranscriptRef ])
            )
        |> Tuple.first


openedTranscript : Application.Model
openedTranscript =
    withTranscriptIndex
        |> Application.update (Msgs.Update <| Message.AgentTranscriptToggled "p1")
        |> Tuple.first
        |> Application.handleCallback
            (Callback.AgentWorkflowRunTranscriptFetched AgenticData.runSummary.id
                "p1"
                (Ok sampleTranscriptNDJSON)
            )
        |> Tuple.first


sampleTranscriptNDJSON : String
sampleTranscriptNDJSON =
    String.join "\n"
        [ """{"type":"system","subtype":"init","model":"claude"}"""
        , """{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"reading the repo"},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"git status"}}]}}"""
        , """{"type":"result","subtype":"success","result":"done"}"""
        ]


initialized : Application.Model
initialized =
    Common.init "/agent/workflows/review-api/runs/9007199254740993"
        |> Application.handleCallback
            (Callback.AgentWorkflowRunFetched AgenticData.runSummary.id (Ok AgenticData.runDetail))
        |> Tuple.first


{-| The run page loaded with one field of the summary changed.
-}
withSummary :
    (Concourse.WorkflowRun.Summary -> Concourse.WorkflowRun.Summary)
    -> Application.Model
withSummary f =
    let
        detail =
            AgenticData.runDetail
    in
    Common.init "/agent/workflows/review-api/runs/9007199254740993"
        |> Application.handleCallback
            (Callback.AgentWorkflowRunFetched AgenticData.runSummary.id
                (Ok { detail | summary = f detail.summary })
            )
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


succeededRunDetail : Concourse.WorkflowRun.Detail
succeededRunDetail =
    let
        detail =
            AgenticData.runDetail

        summary =
            detail.summary
    in
    { detail
        | summary =
            { summary
                | status = "succeeded"
                , executionStatus = Just "succeeded"
                , completedAt = Just "2026-07-22T12:04:00Z"
            }
    }


{-| The run pinned revision 2 while the workflow's promoted revision is 3. The
header must say v2, or the page is describing a shape this run never executed.
-}
sampleRunGraph : Concourse.WorkflowRunGraph.RunGraph
sampleRunGraph =
    { workflowRunId = AgenticData.runSummary.id
    , workflowName = "review-api"
    , workflowVersion = 2
    , graph =
        let
            base =
                AgenticData.workflowOverview.graph
        in
        { nodes =
            base.nodes
                ++ [ { id = "review"
                     , kind = AgentGraph.Model.Agent
                     , displayName = "review"
                     , typeRef = ""
                     , optional = False
                     , decorations = []
                     }
                   , { id = "output:change"
                     , kind = AgentGraph.Model.Output
                     , displayName = "change"
                     , typeRef = "repository-change/v1"
                     , optional = False
                     , decorations = []
                     }
                   ]
        , edges =
            base.edges
                ++ [ { from = "implement"
                     , to = "review"
                     , portName = "repository"
                     , typeRef = "repository/v1"
                     , optional = False
                     }
                   , { from = "implement"
                     , to = "output:change"
                     , portName = "change"
                     , typeRef = "repository-change/v1"
                     , optional = False
                     }
                   ]
        }
    , graphUnavailable = False
    , occurrences =
        [ { nodeId = "implement"
          , nodeKind = "agent"
          , retryAttempt = 1
          , attempt = 1
          , status = "succeeded"
          , planId = "p1"
          , waitId = Nothing
          , publicationId = Nothing
          , startedAt = Just "2026-07-22T12:00:05Z"
          , completedAt = Just "2026-07-22T12:04:00Z"
          , durationSeconds = 235
          , costUsd = 1.25
          }
        ]
    }


withRunGraph : Concourse.WorkflowRunGraph.RunGraph -> Application.Model -> Application.Model
withRunGraph runGraph model =
    model
        |> Application.handleCallback
            (Callback.AgentWorkflowRunGraphFetched AgenticData.runSummary.id (Ok runGraph))
        |> Tuple.first


initializedSucceeded : Application.Model
initializedSucceeded =
    Common.init "/agent/workflows/review-api/runs/9007199254740993"
        |> Application.handleCallback
            (Callback.AgentWorkflowRunFetched AgenticData.runSummary.id (Ok succeededRunDetail))
        |> Tuple.first


initializedWithGraph : Application.Model
initializedWithGraph =
    withRunGraph sampleRunGraph initializedSucceeded


selectedReview : Application.Model
selectedReview =
    initializedWithGraph
        |> Application.update (Msgs.Update (Message.AgentWorkflowNodeSelected "review"))
        |> Tuple.first


initializedWithRetryOrigin : Application.Model
initializedWithRetryOrigin =
    let
        detail =
            succeededRunDetail

        summary =
            detail.summary
    in
    Common.init "/agent/workflows/review-api/runs/9007199254740993"
        |> Application.handleCallback
            (Callback.AgentWorkflowRunFetched AgenticData.runSummary.id
                (Ok
                    { detail
                        | summary =
                            { summary
                                | originKind = "retry"
                                , originReference = "9007199254740991"
                                , retryOf = Just "9007199254740991"
                            }
                    }
                )
            )
        |> Tuple.first
        |> withRunGraph sampleRunGraph


selectedImplement : Application.Model
selectedImplement =
    initializedWithGraph
        |> Application.update (Msgs.Update (Message.AgentWorkflowNodeSelected "implement"))
        |> Tuple.first


selectedImplementWithTranscript : Application.Model
selectedImplementWithTranscript =
    selectedImplement
        |> Application.handleCallback
            (Callback.AgentWorkflowRunTranscriptsFetched AgenticData.runSummary.id
                (Ok [ sampleTranscriptRef ])
            )
        |> Tuple.first


initializedWithTicket : Application.Model
initializedWithTicket =
    let
        detail =
            succeededRunDetail

        summary =
            detail.summary
    in
    Common.init "/agent/workflows/review-api/runs/9007199254740993"
        |> Application.handleCallback
            (Callback.AgentWorkflowRunFetched AgenticData.runSummary.id
                (Ok
                    { detail
                        | summary =
                            { summary
                                | originKind = "ticket"
                                , originReference = "42"
                                , ticketId = Just 42
                                , ticketReference = "ticket-42"
                            }
                    }
                )
            )
        |> Tuple.first
        |> withRunGraph sampleRunGraph


initializedWithTicketRetry : Application.Model
initializedWithTicketRetry =
    withSummary
        (\summary ->
            { summary
                | originKind = "retry"
                , originReference = "9007199254740991"
                , retryOf = Just "9007199254740991"
                , ticketId = Just 42
                , ticketReference = "ticket-42"
            }
        )
        |> withRunGraph sampleRunGraph


initializedWithDeletedTicket : Application.Model
initializedWithDeletedTicket =
    withSummary
        (\summary ->
            { summary
                | originKind = "ticket"
                , originReference = "42"
                , ticketId = Nothing
                , ticketReference = "ticket-42"
            }
        )
        |> withRunGraph sampleRunGraph


initializedWithUnavailableGraph : Application.Model
initializedWithUnavailableGraph =
    initializedSucceeded
        |> withRunGraph
            { sampleRunGraph
                | graphUnavailable = True
                , graph = { nodes = [], edges = [] }
                , occurrences = []
            }


{-| A wait whose durable ID matches no occurrence. It must stay reachable: a
wait the graph cannot place is still a human decision this run is holding.
-}
initializedWithGraphAndOrphanWait : Application.Model
initializedWithGraphAndOrphanWait =
    initializedWithGraph
        |> Application.handleCallback
            (Callback.AgentWorkflowWaitsFetched AgenticData.runSummary.id (Ok [ AgenticData.wait ]))
        |> Tuple.first


initializedWithGraphAndAttributedWait : Application.Model
initializedWithGraphAndAttributedWait =
    initializedSucceeded
        |> withRunGraph
            { sampleRunGraph
                | occurrences =
                    List.map (\occurrence -> { occurrence | waitId = Just AgenticData.wait.id })
                        sampleRunGraph.occurrences
            }
        |> Application.handleCallback
            (Callback.AgentWorkflowWaitsFetched AgenticData.runSummary.id (Ok [ AgenticData.wait ]))
        |> Tuple.first
