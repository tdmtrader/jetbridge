module AgentTicketPageTests exposing (all)

import Application.Application as Application
import Common
import Concourse.AgentTicket as AgentTicket
import Data
import Dict
import Expect
import Html.Attributes
import Json.Decode
import Message.Callback as Callback
import Message.Effects as Effects
import Message.Message
import Message.Subscription exposing (Delivery(..), Interval(..))
import Message.TopLevelMessage as Msgs
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (attribute, class, containing, id, tag, text)
import Time
import Url


sampleDetailJson : String
sampleDetailJson =
    """
    { "ticket":
        { "id": 12, "title": "ship fly archives", "state": "needs_review"
        , "workflow_name": "develop", "body": "do the thing", "budget_usd": 5.0
        , "created_at": 200
        , "repo": "tdmtrader/jetbridge", "target_branch": "main", "branch": "agent/ticket-12"
        }
    , "spec": { "title": "spec title", "body": "spec body", "acceptance_criteria": ["crit a"] }
    , "tasks": [ { "ordering": 1, "title": "first task", "status": "done" } ]
    }
    """


queuedDetailJson : String
queuedDetailJson =
    """
    { "ticket": { "id": 9, "title": "queued work", "state": "queued", "workflow_name": "develop", "created_at": 50 }
    , "spec": null
    , "tasks": []
    }
    """


runningDetailJson : String
runningDetailJson =
    """
    { "ticket": { "id": 9, "title": "queued work", "state": "running", "workflow_name": "develop", "created_at": 50 }
    , "spec": null
    , "tasks": []
    }
    """


mergedDetailJson : String
mergedDetailJson =
    """
    { "ticket":
        { "id": 12, "title": "ship fly archives", "state": "merged"
        , "workflow_name": "develop", "body": "do the thing", "budget_usd": 5.0
        , "created_at": 200
        }
    , "spec": null
    , "tasks": []
    }
    """


erroredDetailJson : String
erroredDetailJson =
    """
    { "ticket":
        { "id": 12, "title": "ship fly archives", "state": "errored"
        , "workflow_name": "develop", "body": "do the thing"
        , "created_at": 200, "completed_at": 300
        , "error_detail": "runner crashed: boom"
        }
    , "spec": null
    , "tasks": []
    }
    """


withDetail : String -> (AgentTicket.Detail -> Expect.Expectation) -> Expect.Expectation
withDetail json f =
    case Json.Decode.decodeString AgentTicket.decodeDetail json of
        Ok d ->
            f d

        Err e ->
            Expect.fail (Json.Decode.errorToString e)


initDetail : ( Application.Model, List Effects.Effect )
initDetail =
    Application.init Data.flags
        { protocol = Url.Http
        , host = ""
        , port_ = Nothing
        , path = "/agent-tickets/12"
        , query = Nothing
        , fragment = Nothing
        }


renderWith path callback =
    Common.init path
        |> Application.handleCallback callback
        |> Tuple.first
        |> Common.queryView


all : Test
all =
    describe "ticket detail page"
        [ test "decodes a detail fixture with a present spec" <|
            \_ ->
                withDetail sampleDetailJson (\d -> Expect.equal d.ticket.title "ship fly archives")
        , test "decodes a detail fixture with a null spec" <|
            \_ ->
                withDetail queuedDetailJson (\d -> Expect.equal d.spec Nothing)
        , test "fetches the ticket and its run metrics on load" <|
            \_ ->
                initDetail
                    |> Tuple.second
                    |> Expect.all
                        [ Common.contains (Effects.FetchAgentTicket 12)
                        , Common.contains (Effects.FetchAgentTicketMetrics 12)
                        ]
        , test "renders the ticket header, tabs and spec body" <|
            \_ ->
                withDetail sampleDetailJson
                    (\d ->
                        renderWith "/agent-tickets/12" (Callback.AgentTicketFetched (Ok d))
                            |> Query.has
                                [ containing [ text "ship fly archives" ]
                                , containing [ text "Spec" ]
                                , containing [ text "Plan" ]
                                , containing [ text "spec body" ]
                                ]
                    )
        , test "shows the created timestamp in the app-wide date format" <|
            \_ ->
                withDetail sampleDetailJson
                    (\d ->
                        let
                            ticket =
                                d.ticket
                        in
                        renderWith "/agent-tickets/12"
                            (Callback.AgentTicketFetched
                                (Ok { d | ticket = { ticket | createdAt = 1784385000 } })
                            )
                            |> Query.find [ id "ticket-timestamps" ]
                            |> Query.has [ text "created Jul 18, 2026 14:30" ]
                    )
        , test "offers merge / send-back transitions for a needs_review ticket" <|
            \_ ->
                withDetail sampleDetailJson
                    (\d ->
                        renderWith "/agent-tickets/12" (Callback.AgentTicketFetched (Ok d))
                            |> Query.has
                                [ containing [ text "Merge" ]
                                , containing [ text "Send back" ]
                                ]
                    )
        , test "offers a dispatch button for a queued ticket" <|
            \_ ->
                withDetail queuedDetailJson
                    (\d ->
                        renderWith "/agent-tickets/12" (Callback.AgentTicketFetched (Ok d))
                            |> Query.has [ containing [ text "Dispatch run" ] ]
                    )
        , test "shows an error notice when the ticket fails to load" <|
            \_ ->
                renderWith "/agent-tickets/12" (Callback.AgentTicketFetched Data.httpUnauthorized)
                    |> Query.has [ text "Couldn't load ticket." ]
        , test "links the harvest-branch diff for a needs_review ticket" <|
            \_ ->
                withDetail sampleDetailJson
                    (\d ->
                        renderWith "/agent-tickets/12" (Callback.AgentTicketFetched (Ok d))
                            |> Query.find [ class "agent-ticket-compare-link" ]
                            |> Query.has
                                [ attribute
                                    (Html.Attributes.href
                                        "https://github.com/tdmtrader/jetbridge/compare/main...agent/ticket-12"
                                    )
                                ]
                    )
        , test "omits the compare link when there is no harvest branch" <|
            \_ ->
                withDetail queuedDetailJson
                    (\d ->
                        renderWith "/agent-tickets/12" (Callback.AgentTicketFetched (Ok d))
                            |> Query.hasNot [ class "agent-ticket-compare-link" ]
                    )
        , test "run history rows link to their build" <|
            \_ ->
                withDetail sampleDetailJson
                    (\d ->
                        Common.init "/agent-tickets/12"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                            |> Tuple.first
                            |> Application.handleCallback
                                (Callback.AgentTicketMetricsFetched 12
                                    (Ok
                                        [ { ticketId = Just 12
                                          , pipelineRunId = Just 2
                                          , buildId = 561978
                                          , planId = "plan-xyz"
                                          , stepName = "implement"
                                          , workflowName = "develop"
                                          , workflowVersion = Just 1
                                          , status = "ok"
                                          , buildStatus = "succeeded"
                                          , outcome = "no_output"
                                          , summary = ""
                                          , model = ""
                                          , usage =
                                                { inputTokens = 19
                                                , outputTokens = 2480
                                                , cacheReadInputTokens = 0
                                                , cacheCreationInputTokens = 0
                                                }
                                          , turns = 25
                                          , wallTimeSeconds = 77
                                          , costUsd = 0.21
                                          , eventCounts = Dict.empty
                                          , createdAt = 100
                                          }
                                        ]
                                    )
                                )
                            |> Tuple.first
                            |> Common.queryView
                            |> Query.find [ class "agent-ticket-run-row" ]
                            |> Query.has
                                [ tag "a"
                                , attribute (Html.Attributes.href "/builds/561978")
                                ]
                    )
        , test "renders the in-app unified diff when the diff endpoint returns one" <|
            \_ ->
                withDetail sampleDetailJson
                    (\d ->
                        Common.init "/agent-tickets/12"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                            |> Tuple.first
                            |> Application.handleCallback
                                (Callback.AgentTicketDiffFetched
                                    (Ok
                                        { files =
                                            [ { path = "atc/foo.go"
                                              , patch = "@@ -1 +1 @@\n-old line\n+new line\n"
                                              , truncated = False
                                              }
                                            ]
                                        , offset = 0
                                        , limit = 50
                                        , totalFiles = 1
                                        , hasMore = False
                                        }
                                    )
                                )
                            |> Tuple.first
                            |> Common.queryView
                            |> Query.find [ id "ticket-diff" ]
                            |> Query.has
                                [ containing [ text "atc/foo.go" ]
                                , containing [ text "+new line" ]
                                , containing [ text "-old line" ]
                                ]
                    )
        , test "run history shows one row per build, newest first, with per-build cost and final summary" <|
            \_ ->
                let
                    metric buildId planId cost summary =
                        { ticketId = Just 12
                        , pipelineRunId = Just 2
                        , buildId = buildId
                        , planId = planId
                        , stepName = "implement"
                        , workflowName = "develop"
                        , workflowVersion = Just 1
                        , status = "ok"
                        , buildStatus = "succeeded"
                        , outcome = ""
                        , summary = summary
                        , model = ""
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
                        , createdAt = 100
                        }
                in
                withDetail sampleDetailJson
                    (\d ->
                        Common.init "/agent-tickets/12"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                            |> Tuple.first
                            |> Application.handleCallback
                                (Callback.AgentTicketMetricsFetched 12
                                    (Ok
                                        -- rows arrive created_at ASC: build 100's
                                        -- two steps, then build 200's single step
                                        [ metric 100 "p1" 0.25 "agent self-report"
                                        , metric 100 "p2" 0.25 "harvest verdict"
                                        , metric 200 "p3" 1.5 "newest run verdict"
                                        ]
                                    )
                                )
                            |> Tuple.first
                            |> Common.queryView
                            |> Query.findAll [ class "agent-ticket-run-row" ]
                            |> Expect.all
                                [ Query.count (Expect.equal 2)
                                , Query.index 0
                                    >> Expect.all
                                        -- newest build renders first: attempt 2,
                                        -- linking to build 200
                                        [ Query.has [ text "attempt 2" ]
                                        , Query.has [ attribute (Html.Attributes.href "/builds/200") ]
                                        , Query.has [ text "newest run verdict" ]
                                        , Query.has [ text "$1.50" ]
                                        , Query.hasNot [ text "build 200" ]
                                        ]
                                , Query.index 1
                                    >> Expect.all
                                        [ Query.has [ text "attempt 1" ]
                                        , Query.has [ attribute (Html.Attributes.href "/builds/100") ]
                                        , Query.has [ text "harvest verdict" ]
                                        , Query.has [ text "$0.50" ]
                                        ]
                                ]
                    )
        , test "a run row shows an attempt number and links to its build page (build number is not a visible label)" <|
            \_ ->
                withDetail sampleDetailJson
                    (\d ->
                        Common.init "/agent-tickets/12"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                            |> Tuple.first
                            |> Application.handleCallback
                                (Callback.AgentTicketMetricsFetched 12
                                    (Ok
                                        [ { ticketId = Just 12
                                          , pipelineRunId = Just 2
                                          , buildId = 561978
                                          , planId = "plan-xyz"
                                          , stepName = "implement"
                                          , workflowName = "develop"
                                          , workflowVersion = Just 1
                                          , status = "ok"
                                          , buildStatus = "succeeded"
                                          , outcome = ""
                                          , summary = "did the work"
                                          , model = ""
                                          , usage =
                                                { inputTokens = 0
                                                , outputTokens = 0
                                                , cacheReadInputTokens = 0
                                                , cacheCreationInputTokens = 0
                                                }
                                          , turns = 1
                                          , wallTimeSeconds = 1
                                          , costUsd = 0.21
                                          , eventCounts = Dict.empty
                                          , createdAt = 100
                                          }
                                        ]
                                    )
                                )
                            |> Tuple.first
                            |> Common.queryView
                            |> Query.find [ class "agent-ticket-run-row" ]
                            |> Expect.all
                                [ Query.has [ text "attempt 1" ]
                                , Query.has [ attribute (Html.Attributes.href "/builds/561978") ]
                                , Query.hasNot [ text "build 561978" ]
                                ]
                    )
        , test "a periodic self-heal refetch does not clobber an open edit form" <|
            \_ ->
                withDetail sampleDetailJson
                    (\d ->
                        Common.init "/agent-tickets/12"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                            |> Tuple.first
                            |> Application.update (Msgs.Update Message.Message.ClickAgentTicketEdit)
                            |> Tuple.first
                            |> Application.update (Msgs.Update (Message.Message.AgentTicketBodyChanged "MY UNSAVED EDIT"))
                            |> Tuple.first
                            -- the 5s refetch lands while the user is still editing:
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                            |> Tuple.first
                            |> Common.queryView
                            |> Query.has
                                [ tag "textarea"
                                , attribute (Html.Attributes.value "MY UNSAVED EDIT")
                                ]
                    )
        , test "saving after a mid-edit refetch posts the typed values, not the server's" <|
            \_ ->
                withDetail sampleDetailJson
                    (\d ->
                        Common.init "/agent-tickets/12"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                            |> Tuple.first
                            |> Application.update (Msgs.Update Message.Message.ClickAgentTicketEdit)
                            |> Tuple.first
                            |> Application.update (Msgs.Update (Message.Message.AgentTicketBodyChanged "MY UNSAVED EDIT"))
                            |> Tuple.first
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                            |> Tuple.first
                            |> Application.update (Msgs.Update Message.Message.ClickAgentTicketSave)
                            |> Tuple.second
                            |> Common.contains
                                (Effects.SaveAgentTicket
                                    { id = 12
                                    , title = "ship fly archives"
                                    , body = "MY UNSAVED EDIT"
                                    , budgetUsd = Just 5
                                    }
                                )
                    )
        , test "clicking Edit opens the form seeded with the current ticket values" <|
            \_ ->
                withDetail sampleDetailJson
                    (\d ->
                        Common.init "/agent-tickets/12"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                            |> Tuple.first
                            |> Application.update (Msgs.Update Message.Message.ClickAgentTicketEdit)
                            |> Tuple.first
                            |> Common.queryView
                            |> Expect.all
                                [ Query.has [ tag "input", attribute (Html.Attributes.value "ship fly archives") ]
                                , Query.has [ tag "textarea", attribute (Html.Attributes.value "do the thing") ]
                                , Query.has [ tag "input", attribute (Html.Attributes.value "5") ]
                                ]
                    )
        , test "a ticket going terminal mid-edit closes the form and says why" <|
            \_ ->
                withDetail sampleDetailJson
                    (\d ->
                        withDetail mergedDetailJson
                            (\merged ->
                                Common.init "/agent-tickets/12"
                                    |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                                    |> Tuple.first
                                    |> Application.update (Msgs.Update Message.Message.ClickAgentTicketEdit)
                                    |> Tuple.first
                                    |> Application.update (Msgs.Update (Message.Message.AgentTicketBodyChanged "MY UNSAVED EDIT"))
                                    |> Tuple.first
                                    |> Application.handleCallback (Callback.AgentTicketFetched (Ok merged))
                                    |> Tuple.first
                                    |> Common.queryView
                                    |> Expect.all
                                        [ Query.hasNot [ tag "textarea" ]
                                        , Query.has [ text "unsaved changes were discarded" ]
                                        ]
                            )
                    )
        , test "a state change under an armed transition disarms the confirm" <|
            \_ ->
                withDetail queuedDetailJson
                    (\queued ->
                        withDetail runningDetailJson
                            (\running ->
                                Common.init "/agent-tickets/12"
                                    |> Application.handleCallback (Callback.AgentTicketFetched (Ok queued))
                                    |> Tuple.first
                                    |> Application.update (Msgs.Update (Message.Message.ClickAgentTicketTransition "abandoned"))
                                    |> Tuple.first
                                    |> Application.handleCallback (Callback.AgentTicketFetched (Ok running))
                                    |> Tuple.first
                                    |> Common.queryView
                                    |> Query.hasNot [ text "Confirm abandon" ]
                            )
                    )
        , test "a same-state refetch leaves an armed transition armed" <|
            \_ ->
                withDetail queuedDetailJson
                    (\queued ->
                        Common.init "/agent-tickets/12"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok queued))
                            |> Tuple.first
                            |> Application.update (Msgs.Update (Message.Message.ClickAgentTicketTransition "abandoned"))
                            |> Tuple.first
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok queued))
                            |> Tuple.first
                            |> Common.queryView
                            |> Query.has [ text "Confirm abandon" ]
                    )
        , test "the 5s tick refetches the ticket and metrics while it can still change" <|
            \_ ->
                withDetail sampleDetailJson
                    (\d ->
                        Common.init "/agent-tickets/12"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                            |> Tuple.first
                            |> Application.update
                                (Msgs.DeliveryReceived (ClockTicked FiveSeconds <| Time.millisToPosix 0))
                            |> Tuple.second
                            |> Expect.all
                                [ Common.contains (Effects.FetchAgentTicket 12)
                                , Common.contains (Effects.FetchAgentTicketMetrics 12)
                                ]
                    )
        , test "the 5s tick keeps polling while the ticket hasn't loaded yet" <|
            \_ ->
                Common.init "/agent-tickets/12"
                    |> Application.update
                        (Msgs.DeliveryReceived (ClockTicked FiveSeconds <| Time.millisToPosix 0))
                    |> Tuple.second
                    |> Common.contains (Effects.FetchAgentTicket 12)
        , test "the errored run-error box links to the failing run's build page" <|
            \_ ->
                withDetail erroredDetailJson
                    (\d ->
                        Common.init "/agent-tickets/12"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                            |> Tuple.first
                            |> Application.handleCallback
                                (Callback.AgentTicketMetricsFetched 12
                                    (Ok
                                        [ { ticketId = Just 12
                                          , pipelineRunId = Just 2
                                          , buildId = 700
                                          , planId = "plan-xyz"
                                          , stepName = "implement"
                                          , workflowName = "develop"
                                          , workflowVersion = Just 1
                                          , status = "errored"
                                          , buildStatus = "errored"
                                          , outcome = ""
                                          , summary = ""
                                          , model = ""
                                          , usage =
                                                { inputTokens = 0
                                                , outputTokens = 0
                                                , cacheReadInputTokens = 0
                                                , cacheCreationInputTokens = 0
                                                }
                                          , turns = 1
                                          , wallTimeSeconds = 1
                                          , costUsd = 0.1
                                          , eventCounts = Dict.empty
                                          , createdAt = 100
                                          }
                                        ]
                                    )
                                )
                            |> Tuple.first
                            |> Common.queryView
                            |> Query.find [ id "ticket-error-detail" ]
                            |> Query.find [ class "agent-ticket-error-build-link" ]
                            |> Query.has
                                [ tag "a"
                                , attribute (Html.Attributes.href "/builds/700")
                                , text "Run error"
                                ]
                    )
        , test "an errored ticket's finish timestamp reads 'ended', not 'completed'" <|
            \_ ->
                withDetail erroredDetailJson
                    (\d ->
                        renderWith "/agent-tickets/12" (Callback.AgentTicketFetched (Ok d))
                            |> Query.find [ id "ticket-timestamps" ]
                            |> Query.has [ text "created Jan 1, 1970 00:03 · ended Jan 1, 1970 00:05" ]
                    )
        , test "the 5s tick stops refetching once the ticket is terminal" <|
            \_ ->
                withDetail mergedDetailJson
                    (\merged ->
                        Common.init "/agent-tickets/12"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok merged))
                            |> Tuple.first
                            |> Application.update
                                (Msgs.DeliveryReceived (ClockTicked FiveSeconds <| Time.millisToPosix 0))
                            |> Tuple.second
                            |> Expect.all
                                [ Common.notContains (Effects.FetchAgentTicket 12)
                                , Common.notContains (Effects.FetchAgentTicketMetrics 12)
                                ]
                    )
        , activeAttemptTest
        ]


activeAttemptTest : Test
activeAttemptTest =
    test "shows an active-attempt strip while running" <|
        \_ ->
            withDetail runningDetailJson
                (\d ->
                    Common.init "/agent-tickets/9"
                        |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                        |> Tuple.first
                        |> Application.handleCallback
                            (Callback.AgentTicketMetricsFetched 9
                                (Ok
                                    [ { ticketId = Just 9
                                      , pipelineRunId = Just 2
                                      , buildId = 500
                                      , planId = "plan-run"
                                      , stepName = "implement"
                                      , workflowName = "develop"
                                      , workflowVersion = Just 1
                                      , status = "ok"
                                      , buildStatus = "started"
                                      , outcome = ""
                                      , summary = ""
                                      , model = ""
                                      , usage =
                                            { inputTokens = 0
                                            , outputTokens = 0
                                            , cacheReadInputTokens = 0
                                            , cacheCreationInputTokens = 0
                                            }
                                      , turns = 1
                                      , wallTimeSeconds = 1
                                      , costUsd = 0.0
                                      , eventCounts = Dict.empty
                                      , createdAt = 100
                                      }
                                    ]
                                )
                            )
                        |> Tuple.first
                        |> Common.queryView
                        |> Query.has [ class "agent-ticket-active-attempt" ]
                )
