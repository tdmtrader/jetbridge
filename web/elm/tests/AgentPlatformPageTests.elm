module AgentPlatformPageTests exposing (all)

import Application.Application as Application
import Common
import Data
import Dict
import Expect
import Html.Attributes as Attr
import Http
import Message.Callback as Callback
import Message.Effects as Effects
import Message.Message
import Message.Subscription as Subscription exposing (Delivery(..), Interval(..))
import Message.TopLevelMessage as Msgs
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (attribute, class, containing, style, tag, text)
import Time


sampleWorkflow :
    { name : String
    , description : String
    , annotation : String
    , hidden : Bool
    , latestVersion : Int
    , schemaVersion : Int
    , signatureVersion : Int
    , contentHash : String
    , liveVersion : Int
    , createdAt : Time.Posix
    }
sampleWorkflow =
    { name = "standard-dev"
    , description = "the five-phase dev flow"
    , annotation = ""
    , hidden = False
    , latestVersion = 2
    , schemaVersion = 3
    , signatureVersion = 1
    , contentHash = "abcdef0123456789cafe"
    , liveVersion = 1
    , createdAt = Time.millisToPosix 0
    }


sampleWorkflowRun =
    { id = "9007199254740993"
    , pipelineRunId = Just 37
    , workflowName = "standard-dev"
    , workflowVersion = 2
    , schemaVersion = 3
    , signatureVersion = 1
    , definitionContentHash = "abcdef0123456789cafe"
    , functionId = Nothing
    , status = "failed"
    , executionStatus = Just "failed"
    , originKind = "manual"
    , originReference = ""
    , ticketId = Nothing
    , ticketReference = ""
    , createdBy = "alice"
    , retryOf = Nothing
    , createdAt = "2026-07-22T12:00:00Z"
    , updatedAt = "2026-07-22T12:01:00Z"
    , startedAt = Just "2026-07-22T12:00:05Z"
    , completedAt = Just "2026-07-22T12:01:00Z"
    , parameterizedConfigHash = "parameterized"
    , instanceConfigHash = Just "instance"
    , actualPlanHash = Just "plan"
    , plannedBuildId = Just 42
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


sampleCredential :
    { kind : String
    , expiresAt : Maybe Time.Posix
    }
sampleCredential =
    { kind = "anthropic_oauth"
    , expiresAt = Just (Time.millisToPosix 0)
    }


sampleRun :
    { workflowRunId : Maybe String
    , functionId : String
    , buildId : Int
    , planId : String
    , stepName : String
    , workflowName : String
    , workflowVersion : Maybe Int
    , status : String
    , buildStatus : String
    , outcome : String
    , summary : String
    , model : String
    , usage :
        { inputTokens : Int
        , outputTokens : Int
        , cacheReadInputTokens : Int
        , cacheCreationInputTokens : Int
        }
    , turns : Int
    , wallTimeSeconds : Int
    , costUsd : Float
    , eventCounts : Dict.Dict String Int
    , createdAt : Int
    }
sampleRun =
    { workflowRunId = Just "42"
    , functionId = "review"
    , buildId = 100
    , planId = "plan-abc"
    , stepName = "review-diff"
    , workflowName = "standard-dev"
    , workflowVersion = Just 2
    , status = "failed"
    , buildStatus = "failed"
    , outcome = "failed"
    , summary = "one finding"
    , model = "claude"
    , usage =
        { inputTokens = 1000
        , outputTokens = 500
        , cacheReadInputTokens = 0
        , cacheCreationInputTokens = 0
        }
    , turns = 3
    , wallTimeSeconds = 12
    , costUsd = 1.25
    , eventCounts = Dict.empty
    , createdAt = 0
    }


all : Test
all =
    describe "agent platform operations console"
        [ test "/agent still resolves to the operations console after the rename" <|
            \_ ->
                -- The module moved (Agent.Agent → AgentPlatform.AgentPlatform)
                -- so the ops console stops claiming the whole agent namespace;
                -- the route it answers is deliberately unchanged.
                Common.init "/agent"
                    |> Common.queryView
                    |> Query.has [ text "Agent workflows" ]
        , test "renders a recent run row with its step name and status badge" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentRunMetricsFetched (Ok [ sampleRun ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-run-row" ]
                    |> Query.has
                        [ containing [ text "review-diff" ]
                        , containing [ class "agent-badge", text "Failed" ]
                        ]
        , test "run rows show created-at in the app-wide date format" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentRunMetricsFetched
                            (Ok [ { sampleRun | createdAt = 1784385000 } ])
                        )
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-run-row" ]
                    |> Query.has [ containing [ text "Jul 18, 2026 14:30" ] ]
        , test "a runs poll failure after a load shows a stale-data warning and keeps the data" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentRunMetricsFetched (Ok [ sampleRun ]))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentRunMetricsFetched (Err Http.NetworkError))
                    |> Tuple.first
                    |> Common.queryView
                    |> Expect.all
                        [ Query.has
                            [ class "agent-section-stale"
                            , text "refresh failed — showing stale data: couldn't load runs"
                            ]
                        , Query.has [ class "agent-run-row", containing [ text "review-diff" ] ]
                        ]
        , test "links a run back to its durable workflow run with the function label" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentRunMetricsFetched (Ok [ sampleRun ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-run-row" ]
                    |> Query.has
                        [ tag "a"
                        , attribute (Attr.href "/agent/workflows/standard-dev/runs/42")
                        , containing [ text "#42 review" ]
                        ]
        , test "renders an unbound CI invocation when there is no workflow run" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentRunMetricsFetched
                            (Ok [ { sampleRun | workflowRunId = Nothing, functionId = "" } ])
                        )
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-run-row" ]
                    |> Query.has [ text "CI" ]
        , test "a collapsed ledger row shows the truncated one-line summary" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentRunMetricsFetched (Ok [ sampleRun ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-run-row" ]
                    |> Query.has [ class "agent-run-summary", containing [ text "one finding" ] ]
        , test "expanding a ledger row reveals the full run summary" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentRunMetricsFetched (Ok [ sampleRun ]))
                    |> Tuple.first
                    |> Application.update
                        -- keyed by build id + plan id (stable across the 5s
                        -- refetch, unique across sibling step rows of a build)
                        (Msgs.Update (Message.Message.AgentRunExpandToggled "100:plan-abc"))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-run-row" ]
                    |> Query.has [ class "agent-run-summary-full", containing [ text "one finding" ] ]
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
        , test "renders a deprecated workflow indicator and operator note" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentWorkflowsFetched
                            (Ok
                                [ { sampleWorkflow
                                    | annotation = "migrate to standard-dev"
                                    , hidden = True
                                  }
                                ]
                            )
                        )
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-workflow-row" ]
                    |> Query.has
                        [ containing [ class "agent-workflow-deprecated", text "deprecated" ]
                        , containing [ class "agent-workflow-annotation", text "migrate to standard-dev" ]
                        ]
        , test "fetches each workflow's durable runs and exact operational counts after loading definitions" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentWorkflowsFetched (Ok [ sampleWorkflow ]))
                    |> Tuple.second
                    |> Expect.all
                        [ Common.contains (Effects.FetchAgentWorkflowRuns "standard-dev")
                        , Common.contains (Effects.FetchAgentWorkflowRunOperationalStatusCounts "standard-dev")
                        ]
        , test "does not present missing workflow summaries as authoritative zeroes while they load" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentWorkflowsFetched (Ok [ sampleWorkflow ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-workflow-row" ]
                    |> Expect.all
                        [ Query.has [ text "latest operational: loading…" ]
                        , Query.has [ text "status counts loading…" ]
                        , Query.has [ text "experiments: loading…" ]
                        , Query.has [ text "cost loading…" ]
                        , Query.hasNot [ text "0 queued" ]
                        , Query.hasNot [ text "$0.00" ]
                        ]
        , test "a different workflow's run success cannot hide this workflow's failed summary" <|
            \_ ->
                let
                    healthyWorkflow =
                        { sampleWorkflow | name = "healthy" }
                in
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentWorkflowsFetched (Ok [ sampleWorkflow, healthyWorkflow ]))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentWorkflowRunsFetched "standard-dev" [] (Err Http.NetworkError))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentWorkflowRunsFetched "healthy" [] (Ok []))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.findAll [ class "agent-workflow-row" ]
                    |> Query.index 0
                    |> Query.has [ text "latest operational: unavailable" ]
        , test "the platform ignores filtered run callbacks left behind by a workflow page" <|
            \_ ->
                let
                    workflowPageQuery =
                        [ ( "window", "7d" ), ( "scope", "operational" ), ( "lens", "attention" ) ]
                in
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentWorkflowsFetched (Ok [ sampleWorkflow ]))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentWorkflowRunsFetched
                            "standard-dev"
                            workflowPageQuery
                            (Ok [ sampleWorkflowRun ])
                        )
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentWorkflowRunsFetched
                            "standard-dev"
                            workflowPageQuery
                            (Err Http.NetworkError)
                        )
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-workflow-row" ]
                    |> Query.has [ text "latest operational: loading…" ]
        , test "a different workflow's status success cannot turn this workflow's failed counts into zeroes" <|
            \_ ->
                let
                    healthyWorkflow =
                        { sampleWorkflow | name = "healthy" }
                in
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentWorkflowsFetched (Ok [ sampleWorkflow, healthyWorkflow ]))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentWorkflowRunOperationalStatusCountsFetched "standard-dev" (Err Http.NetworkError))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentWorkflowRunOperationalStatusCountsFetched
                            "healthy"
                            (Ok { workflowName = "healthy", counts = Dict.empty })
                        )
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.findAll [ class "agent-workflow-row" ]
                    |> Query.index 0
                    |> Expect.all
                        [ Query.has [ text "status counts unavailable" ]
                        , Query.hasNot [ text "0 queued" ]
                        ]
        , test "an experiments failure does not masquerade as no experiments" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentWorkflowsFetched (Ok [ sampleWorkflow ]))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentExperimentsFetched (Err Http.NetworkError))
                    |> Tuple.first
                    |> Common.queryView
                    |> Expect.all
                        [ Query.find [ class "agent-workflow-row" ]
                            >> Query.has [ text "experiments: unavailable" ]
                        , Query.hasNot [ class "agent-section-stale" ]
                        ]
        , test "an experiments refresh failure is stale only after data was loaded" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentWorkflowsFetched (Ok [ sampleWorkflow ]))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentExperimentsFetched (Ok []))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentExperimentsFetched (Err Http.NetworkError))
                    |> Tuple.first
                    |> Common.queryView
                    |> Expect.all
                        [ Query.has [ class "agent-section-stale" ]
                        , Query.find [ class "agent-workflow-row" ]
                            >> Query.has [ text "experiments: no experiments" ]
                        ]
        , test "a daily cost success cannot hide a failed workflow-cost summary" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentWorkflowsFetched (Ok [ sampleWorkflow ]))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentWorkflowCostsFetched (Err Http.NetworkError))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentCostRollupFetched (Ok sampleRollup))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-workflow-row" ]
                    |> Query.has [ text "cost unavailable" ]
        , test "a successful workflow-cost response renders the workflow's actual spend" <|
            \_ ->
                let
                    workflowCostRows =
                        sampleRollup.rows
                            |> List.map (\row -> { row | key = "standard-dev" })
                in
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentWorkflowsFetched (Ok [ sampleWorkflow ]))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentWorkflowCostsFetched
                            (Ok
                                { sampleRollup
                                    | groupBy = "workflow"
                                    , rows = workflowCostRows
                                }
                            )
                        )
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-workflow-row" ]
                    |> Query.has [ text "cost $3.50" ]
        , test "workflow cards link to detail and summarize operational attention" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentWorkflowsFetched (Ok [ sampleWorkflow ]))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentWorkflowRunsFetched "standard-dev" [] (Ok [ sampleWorkflowRun ]))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentWorkflowRunOperationalStatusCountsFetched
                            "standard-dev"
                            (Ok
                                { workflowName = "standard-dev"
                                , counts = Dict.fromList [ ( "failed", 1 ) ]
                                }
                            )
                        )
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-workflow-row" ]
                    |> Query.has
                        [ class "agent-workflow-link"
                        , attribute (Attr.href "/agent/workflows/standard-dev")
                        , containing [ class "agent-workflow-signature", text "schema v3 · signature v1" ]
                        , containing [ class "agent-workflow-operational-state", text "latest operational: failed · 0 queued · 0 running · 1 attention" ]
                        , containing [ class "agent-workflow-needs-attention", text "needs attention" ]
                        ]
        , test "workflow counts come from the exact operational aggregate, not capped history" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentWorkflowsFetched (Ok [ sampleWorkflow ]))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentWorkflowRunsFetched "standard-dev" [] (Ok [ sampleWorkflowRun ]))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentWorkflowRunOperationalStatusCountsFetched
                            "standard-dev"
                            (Ok
                                { workflowName = "standard-dev"
                                , counts =
                                    Dict.fromList
                                        [ ( "admitting", 2 )
                                        , ( "running", 1 )
                                        , ( "canceling", 1 )
                                        , ( "failed", 0 )
                                        , ( "errored", 0 )
                                        ]
                                }
                            )
                        )
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-workflow-row" ]
                    |> Expect.all
                        [ Query.has
                            [ containing
                                [ class "agent-workflow-operational-state"
                                , text "latest operational: failed · 2 queued · 2 running · 0 attention"
                                ]
                            ]
                        , Query.hasNot [ class "agent-workflow-needs-attention" ]
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
                    |> Query.has [ text "today (UTC day): $12.34 spent / $20.00 cap ($7.66 left)" ]
        , test "renders a daily-cap gauge when a cap is configured" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentCostRollupFetched (Ok sampleRollup))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has [ class "agent-daily-cap-gauge" ]
        , test "states that spend is unbounded when no daily cap is set" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentCostRollupFetched
                            (Ok
                                { sampleRollup
                                    | summary =
                                        { dailyCapUsd = 0
                                        , dailySpentUsd = 12.34
                                        , dailyRemainingUsd = 0
                                        , dailyExhausted = False
                                        }
                                }
                            )
                        )
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has [ class "agent-daily-cap-none" ]
        , test "an unrecognized group_by rollup does not clobber the by-day cost table" <|
            \_ ->
                -- The by-day and by-workflow rollups share one callback and are
                -- told apart by group_by. The by-ticket dimension is gone from
                -- the server; a stale response carrying it must not blank the
                -- table the page is showing.
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentCostRollupFetched (Ok sampleRollup))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentWorkflowCostsFetched
                            (Ok { sampleRollup | groupBy = "workflow", rows = [] })
                        )
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-cost-row" ]
                    |> Query.has [ containing [ text "$3.50" ] ]
        , test "shows the platform credential dispatched runs authenticate with" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentPlatformCredentialsFetched
                            (Ok
                                [ { kind = "anthropic_oauth"
                                  , expiresAt = Nothing
                                  }
                                ]
                            )
                        )
                    |> Tuple.first
                    |> Common.queryView
                    |> Expect.all
                        [ Query.has [ text "Platform credential (used by dispatched runs)" ]
                        , Query.find [ class "agent-platform-credential" ]
                            >> Query.has [ text "anthropic_oauth" ]
                        ]
        , test "shows platform credential loading until the admin status request resolves" <|
            \_ ->
                Common.init "/agent"
                    |> Common.queryView
                    |> Query.find [ attribute (Attr.id "agent-credentials") ]
                    |> Query.has
                        [ text "Platform credential (used by dispatched runs)"
                        , text "loading…"
                        ]
        , test "shows that a successful empty platform credential slot blocks dispatched auth" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentPlatformCredentialsFetched (Ok []))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ attribute (Attr.id "agent-credentials") ]
                    |> Query.has
                        [ text "no platform credential stored — dispatched runs cannot authenticate" ]
        , test "shows a platform credential status failure instead of hiding the slot" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentPlatformCredentialsFetched (Err Http.NetworkError))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ attribute (Attr.id "agent-credentials") ]
                    |> Query.has [ text "couldn't load platform credentials" ]
        , test "hides the admin-only platform credential slot only after a forbidden response" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentPlatformCredentialsFetched Data.httpForbidden)
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.hasNot [ text "Platform credential (used by dispatched runs)" ]
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
        , test "renders a stored credential's kind" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentCredentialsFetched (Ok [ sampleCredential ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-credential-row" ]
                    |> Query.has [ text "anthropic_oauth" ]
        , test "a workflows poll failure after a load shows a stale-data warning and keeps the data" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentWorkflowsFetched (Ok [ sampleWorkflow ]))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentWorkflowsFetched (Err Http.NetworkError))
                    |> Tuple.first
                    |> Common.queryView
                    |> Expect.all
                        [ Query.has
                            [ class "agent-section-stale"
                            , text "refresh failed — showing stale data: couldn't load workflows"
                            ]
                        , Query.has [ class "agent-workflow-row", containing [ text "standard-dev" ] ]
                        ]
        , test "a costs poll failure after a load shows a stale-data warning and keeps the data" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentCostRollupFetched (Ok sampleRollup))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentCostRollupFetched (Err Http.NetworkError))
                    |> Tuple.first
                    |> Common.queryView
                    |> Expect.all
                        [ Query.has
                            [ class "agent-section-stale"
                            , text "refresh failed — showing stale data: couldn't load costs"
                            ]
                        , Query.has [ class "agent-cost-row", containing [ text "$3.50" ] ]
                        ]
        , test "a credentials poll failure after a load shows a stale-data warning and keeps the data" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentCredentialsFetched (Ok [ sampleCredential ]))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentCredentialsFetched (Err Http.NetworkError))
                    |> Tuple.first
                    |> Common.queryView
                    |> Expect.all
                        [ Query.has
                            [ class "agent-section-stale"
                            , text "refresh failed — showing stale data: couldn't load credentials"
                            ]
                        , Query.has [ class "agent-credential-row", containing [ text "anthropic_oauth" ] ]
                        ]
        , test "a successful refetch clears the stale-data warning" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentWorkflowsFetched (Ok [ sampleWorkflow ]))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentWorkflowsFetched (Err Http.NetworkError))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentWorkflowsFetched (Ok [ sampleWorkflow ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.hasNot [ class "agent-section-stale" ]
        , test "subscribes to the one minute clock" <|
            \_ ->
                Common.init "/agent"
                    |> Application.subscriptions
                    |> Common.contains (Subscription.OnClockTick OneMinute)
        , test "on one minute timer, refetches workflows, costs, and credentials" <|
            \_ ->
                Common.init "/agent"
                    |> Application.update
                        (Msgs.DeliveryReceived
                            (ClockTicked OneMinute <| Time.millisToPosix 0)
                        )
                    |> Tuple.second
                    |> Expect.all
                        [ Common.contains Effects.FetchAgentRunMetrics
                        , Common.contains Effects.FetchAgentWorkflows
                        , Common.contains Effects.FetchAgentCostRollup
                        , Common.contains Effects.FetchAgentCredentials
                        ]
        , test "on five second timer, does not refetch agent data" <|
            \_ ->
                Common.init "/agent"
                    |> Application.update
                        (Msgs.DeliveryReceived
                            (ClockTicked FiveSeconds <| Time.millisToPosix 0)
                        )
                    |> Tuple.second
                    |> Expect.all
                        [ Common.notContains Effects.FetchAgentRunMetrics
                        , Common.notContains Effects.FetchAgentWorkflows
                        , Common.notContains Effects.FetchAgentCostRollup
                        , Common.notContains Effects.FetchAgentCredentials
                        ]
        , test "does not render retired principal credential controls" <|
            \_ ->
                Common.init "/agent"
                    |> Common.queryView
                    |> Query.hasNot [ class "agent-mint-form" ]
        ]
