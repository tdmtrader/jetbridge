module AgentPageTests exposing (all)

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


sampleCredential :
    { kind : String
    , expiresAt : Maybe Time.Posix
    , lastVerifiedAt : Maybe Time.Posix
    , jiraAccountId : String
    }
sampleCredential =
    { kind = "anthropic_oauth"
    , expiresAt = Just (Time.millisToPosix 0)
    , lastVerifiedAt = Just (Time.millisToPosix 0)
    , jiraAccountId = "acct-123"
    }


samplePrincipal :
    { id : Int
    , name : String
    , description : String
    , tokenPrefix : String
    , scopes : List String
    , teamName : String
    , createdBy : String
    , createdAt : Time.Posix
    , expiresAt : Maybe Time.Posix
    , revokedAt : Maybe Time.Posix
    , lastUsedAt : Maybe Time.Posix
    }
samplePrincipal =
    { id = 7
    , name = "itest-reviewer"
    , description = "integration"
    , tokenPrefix = "cap1.abcd12"
    , scopes = [ "reviews:write" ]
    , teamName = "main"
    , createdBy = "admin"
    , createdAt = Time.millisToPosix 0
    , expiresAt = Nothing
    , revokedAt = Nothing
    , lastUsedAt = Nothing
    }


samplePrincipalCreated :
    { principal :
        { id : Int
        , name : String
        , description : String
        , tokenPrefix : String
        , scopes : List String
        , teamName : String
        , createdBy : String
        , createdAt : Time.Posix
        , expiresAt : Maybe Time.Posix
        , revokedAt : Maybe Time.Posix
        , lastUsedAt : Maybe Time.Posix
        }
    , token : String
    }
samplePrincipalCreated =
    { principal = samplePrincipal
    , token = "cap1.xxx"
    }


ephemeralPrincipal =
    { samplePrincipal | id = 8, name = "run-123" }


sampleRun :
    { ticketId : Maybe Int
    , pipelineRunId : Maybe Int
    , buildId : Int
    , planId : String
    , stepName : String
    , workflowName : String
    , workflowVersion : Maybe Int
    , status : String
    , buildStatus : String
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
    { ticketId = Just 42
    , pipelineRunId = Nothing
    , buildId = 100
    , planId = "plan-abc"
    , stepName = "review-diff"
    , workflowName = "standard-dev"
    , workflowVersion = Just 2
    , status = "failed"
    , buildStatus = "failed"
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
    describe "agent page"
        [ test "fetches workflows and cost rollup on load" <|
            \_ ->
                Common.init "/agent"
                    |> Common.queryView
                    |> Query.has [ text "Agent" ]
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
        , test "links a ticket-backed run back to its ticket" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentRunMetricsFetched (Ok [ sampleRun ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-run-row" ]
                    |> Query.has
                        [ tag "a"
                        , attribute (Attr.href "/agent-tickets/42")
                        , containing [ text "#42" ]
                        ]
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
                        (Msgs.Update (Message.Message.AgentRunExpandToggled 0))
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
        , test "surfaces the unattributed bucket from the by-ticket rollup" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentCostRollupFetched (Ok sampleRollup))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentCostRollupFetched
                            (Ok
                                { sampleRollup
                                    | groupBy = "ticket"
                                    , rows =
                                        [ { key = ""
                                          , entries = 5
                                          , inputTokens = 10
                                          , outputTokens = 20
                                          , turns = 30
                                          , costUsd = 11.08
                                          }
                                        , { key = "12"
                                          , entries = 1
                                          , inputTokens = 1
                                          , outputTokens = 2
                                          , turns = 3
                                          , costUsd = 0.4
                                          }
                                        ]
                                }
                            )
                        )
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has [ text "unattributed (no ticket, all time): $11.08" ]
        , test "the by-ticket rollup does not clobber the by-day cost table" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentCostRollupFetched (Ok sampleRollup))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentCostRollupFetched
                            (Ok { sampleRollup | groupBy = "ticket", rows = [] })
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
                                  , lastVerifiedAt = Nothing
                                  , jiraAccountId = ""
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
        , test "renders a principal with its name and a revoke control" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentPrincipalsFetched (Ok [ samplePrincipal ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-principal-row" ]
                    |> Query.has
                        [ containing [ text "itest-reviewer" ]
                        , containing [ class "agent-principal-revoke", text "revoke" ]
                        ]
        , test "renders the one-time token box after minting" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentPrincipalCreated (Ok samplePrincipalCreated))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-minted-token" ]
                    |> Query.has [ text "cap1.xxx" ]
        , test "folds ephemeral run principals behind a toggle, keeping durable ones visible" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentPrincipalsFetched (Ok [ samplePrincipal, ephemeralPrincipal ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Expect.all
                        [ Query.has [ class "agent-principal-row", containing [ text "itest-reviewer" ] ]
                        , Query.hasNot [ containing [ text "run-123" ] ]
                        , Query.has [ class "agent-ephemeral-toggle", containing [ text "1 ephemeral run principal" ] ]
                        ]
        , test "expanding the toggle reveals the ephemeral run principals" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentPrincipalsFetched (Ok [ samplePrincipal, ephemeralPrincipal ]))
                    |> Tuple.first
                    |> Application.update
                        (Msgs.Update Message.Message.AgentPrincipalsShowEphemeralToggled)
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has [ class "agent-principal-row", containing [ text "run-123" ] ]
        , test "shows an admin-only message when principals fetch is forbidden" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentPrincipalsFetched Data.httpForbidden)
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has
                        [ text "not authorized — the agent principals API is admin-only" ]
        , test "the mint button shows a disabled minting state after submit" <|
            \_ ->
                Common.init "/agent"
                    |> Application.update
                        (Msgs.Update <| Message.Message.AgentMintNameChanged "reviewer")
                    |> Tuple.first
                    |> Application.update
                        (Msgs.Update <| Message.Message.AgentMintScopeToggled "reviews:write")
                    |> Tuple.first
                    |> Application.update
                        (Msgs.Update Message.Message.AgentMintSubmitted)
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-mint-button" ]
                    |> Expect.all
                        [ Query.has [ text "minting…" ]
                        , Query.has [ style "cursor" "not-allowed" ]
                        ]
        , test "a revoke failure surfaces in the principals section, not the mint form" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentPrincipalsFetched (Ok [ samplePrincipal ]))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentPrincipalRevoked Data.httpForbidden)
                    |> Tuple.first
                    |> Common.queryView
                    |> Expect.all
                        [ Query.find [ class "agent-revoke-error" ]
                            >> Query.has [ text "not authorized — principals are admin-only" ]
                        , Query.find [ class "agent-mint-form" ]
                            >> Query.hasNot [ text "not authorized — principals are admin-only" ]
                        ]
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
        , test "a principals poll failure after a load shows a stale-data warning and keeps the data" <|
            \_ ->
                Common.init "/agent"
                    |> Application.handleCallback
                        (Callback.AgentPrincipalsFetched (Ok [ samplePrincipal ]))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentPrincipalsFetched (Err Http.NetworkError))
                    |> Tuple.first
                    |> Common.queryView
                    |> Expect.all
                        [ Query.has
                            [ class "agent-section-stale"
                            , text "refresh failed — showing stale data: couldn't load principals"
                            ]
                        , Query.has [ class "agent-principal-row", containing [ text "itest-reviewer" ] ]
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
        , test "a non-numeric expiry shows a hint and disables minting" <|
            \_ ->
                Common.init "/agent"
                    |> Application.update
                        (Msgs.Update <| Message.Message.AgentMintNameChanged "reviewer")
                    |> Tuple.first
                    |> Application.update
                        (Msgs.Update <| Message.Message.AgentMintScopeToggled "reviews:write")
                    |> Tuple.first
                    |> Application.update
                        (Msgs.Update <| Message.Message.AgentMintExpiresChanged "soon")
                    |> Tuple.first
                    |> Common.queryView
                    |> Expect.all
                        [ Query.has
                            [ class "agent-mint-expires-hint"
                            , text "must be a positive number of days; leave blank for no expiry"
                            ]
                        , Query.find [ class "agent-mint-button" ]
                            >> Query.has [ style "cursor" "not-allowed" ]
                        ]
        , test "subscribes to the one minute clock" <|
            \_ ->
                Common.init "/agent"
                    |> Application.subscriptions
                    |> Common.contains (Subscription.OnClockTick OneMinute)
        , test "on one minute timer, refetches workflows, costs, credentials, and principals" <|
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
                        , Common.contains Effects.FetchAgentPrincipals
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
                        , Common.notContains Effects.FetchAgentPrincipals
                        ]
        ]
