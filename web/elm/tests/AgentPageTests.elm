module AgentPageTests exposing (all)

import Application.Application as Application
import Common
import Data
import Expect
import Http
import Message.Callback as Callback
import Message.Message
import Message.TopLevelMessage as Msgs
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (class, containing, style, text)
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
        ]
