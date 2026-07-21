module AgentTicketsPageTests exposing (all)

import Application.Application as Application
import Common
import Concourse.AgentDispatcher as AgentDispatcher
import Concourse.AgentTicket as AgentTicket
import Data
import Expect
import Json.Decode
import Message.Callback as Callback
import Message.Effects as Effects
import Message.Message as Message
import Message.Subscription exposing (Delivery(..), Interval(..))
import Message.TopLevelMessage as Msgs
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (class, containing, text)
import Time
import Url


ticketsFrom : String -> List AgentTicket.Ticket
ticketsFrom json =
    Json.Decode.decodeString (Json.Decode.list AgentTicket.decodeTicket) json
        |> Result.withDefault []


sampleTickets : List AgentTicket.Ticket
sampleTickets =
    ticketsFrom
        """
        [ { "id": 12, "title": "ship fly archives", "state": "needs_review", "workflow_name": "develop", "created_at": 200 }
        , { "id": 7, "title": "gap analysis", "state": "running", "workflow_name": "analyze", "created_at": 100 }
        ]
        """


costRollup : Callback.Callback
costRollup =
    Callback.AgentCostRollupFetched
        (Ok
            { groupBy = "ticket"
            , summary =
                { dailyCapUsd = 0
                , dailySpentUsd = 0
                , dailyRemainingUsd = 0
                , dailyExhausted = False
                }
            , rows =
                [ { key = "12", entries = 1, inputTokens = 0, outputTokens = 0, turns = 0, costUsd = 0.18 } ]
            }
        )


initAgentTickets : ( Application.Model, List Effects.Effect )
initAgentTickets =
    Application.init Data.flags
        { protocol = Url.Http
        , host = ""
        , port_ = Nothing
        , path = "/agent/tickets"
        , query = Nothing
        , fragment = Nothing
        }


dispatcherStatus : String -> Callback.Callback
dispatcherStatus modeToken =
    Callback.AgentDispatcherFetched
        (Ok
            { mode = AgentDispatcher.modeFromString modeToken
            , source = "setting"
            , updatedAt = Just "2026-07-19T12:00:00Z"
            , updatedBy = Just "operator"
            , bootDefault = AgentDispatcher.Off
            }
        )


all : Test
all =
    describe "ticket queue page"
        [ test "decodes the sample ticket fixtures" <|
            \_ ->
                List.length sampleTickets
                    |> Expect.equal 2
        , test "fetches tickets, costs and dispatcher status on load" <|
            \_ ->
                initAgentTickets
                    |> Tuple.second
                    |> Expect.all
                        [ Common.contains Effects.FetchAgentTickets
                        , Common.contains Effects.FetchAgentTicketCosts
                        , Common.contains Effects.FetchAgentDispatcher
                        ]
        , test "decodes the dispatcher status wire shape" <|
            \_ ->
                Json.Decode.decodeString AgentDispatcher.decodeStatus
                    """
                    { "mode": "paused", "source": "setting"
                    , "updated_at": "2026-07-19T12:00:00Z", "updated_by": "operator"
                    , "boot_default": "off" }
                    """
                    |> Result.map .mode
                    |> Expect.equal (Ok AgentDispatcher.Paused)
        , test "tolerates an unknown mode token without crashing" <|
            \_ ->
                Json.Decode.decodeString AgentDispatcher.decodeStatus
                    """{ "mode": "hibernating", "source": "setting", "boot_default": "active" }"""
                    |> Result.map .mode
                    |> Expect.equal (Ok (AgentDispatcher.Unknown "hibernating"))
        , test "renders the auto-dispatch status pill from the fetched status" <|
            \_ ->
                Common.init "/agent/tickets"
                    |> Application.handleCallback (dispatcherStatus "active")
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ Test.Html.Selector.id "dispatcher-status-pill" ]
                    |> Query.has [ text "Auto-dispatch: active" ]
        , test "shows the pause banner when auto-dispatch is not active" <|
            \_ ->
                Common.init "/agent/tickets"
                    |> Application.handleCallback (dispatcherStatus "paused")
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ Test.Html.Selector.id "dispatcher-banner" ]
                    |> Query.has [ text "manually" ]
        , test "shows no dispatcher banner when auto-dispatch is active" <|
            \_ ->
                Common.init "/agent/tickets"
                    |> Application.handleCallback (dispatcherStatus "active")
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.findAll [ Test.Html.Selector.id "dispatcher-banner" ]
                    |> Query.count (Expect.equal 0)
        , test "renders a ticket row with id, title and workflow" <|
            \_ ->
                Common.init "/agent/tickets"
                    |> Application.handleCallback (Callback.AgentTicketsFetched (Ok sampleTickets))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.findAll [ class "agent-ticket-row" ]
                    |> Query.first
                    |> Query.has
                        [ containing [ text "#12" ]
                        , containing [ text "ship fly archives" ]
                        , containing [ text "develop" ]
                        ]
        , test "joins per-ticket cost into the matching row" <|
            \_ ->
                Common.init "/agent/tickets"
                    |> Application.handleCallback (Callback.AgentTicketsFetched (Ok sampleTickets))
                    |> Tuple.first
                    |> Application.handleCallback costRollup
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.findAll [ class "agent-ticket-row" ]
                    |> Query.first
                    |> Query.has [ containing [ text "$0.18" ] ]
        , test "shows an empty-state notice when there are no tickets" <|
            \_ ->
                Common.init "/agent/tickets"
                    |> Application.handleCallback (Callback.AgentTicketsFetched (Ok []))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has [ text "No tickets yet." ]
        , test "shows an error notice when tickets fail to load" <|
            \_ ->
                Common.init "/agent/tickets"
                    |> Application.handleCallback (Callback.AgentTicketsFetched Data.httpUnauthorized)
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has [ text "Couldn't load tickets." ]
        , test "shows the branch on rows that have a harvest branch" <|
            \_ ->
                Common.init "/agent/tickets"
                    |> Application.handleCallback
                        (Callback.AgentTicketsFetched
                            (Ok
                                (ticketsFrom
                                    """
                                    [ { "id": 12, "title": "ship fly archives", "state": "needs_review"
                                      , "workflow_name": "develop", "created_at": 200, "branch": "agent/ticket-12" }
                                    ]
                                    """
                                )
                            )
                        )
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-ticket-branch" ]
                    |> Query.has [ text "agent/ticket-12" ]
        , test "live-updates: refetches tickets on the five second tick, but not the heavy cost rollup" <|
            \_ ->
                Common.init "/agent/tickets"
                    |> Application.update
                        (Msgs.DeliveryReceived (ClockTicked FiveSeconds <| Time.millisToPosix 0))
                    |> Tuple.second
                    |> Expect.all
                        [ Common.contains Effects.FetchAgentTickets
                        , Common.notContains Effects.FetchAgentTicketCosts
                        ]
        , test "live-updates: refreshes the cost rollup on the minute tick" <|
            \_ ->
                -- the rollup is a whole-window ledger aggregation; 5s polling
                -- would run it 720x/hour per open tab for run-granularity data
                Common.init "/agent/tickets"
                    |> Application.update
                        (Msgs.DeliveryReceived (ClockTicked OneMinute <| Time.millisToPosix 0))
                    |> Tuple.second
                    |> Common.contains Effects.FetchAgentTicketCosts
        , test "client-side filter narrows the visible rows by title" <|
            \_ ->
                Common.init "/agent/tickets"
                    |> Application.handleCallback (Callback.AgentTicketsFetched (Ok sampleTickets))
                    |> Tuple.first
                    |> Application.update (Msgs.Update (Message.AgentTicketsFilterChanged "gap"))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.findAll [ class "agent-ticket-row" ]
                    |> Query.count (Expect.equal 1)
        , test "enriches a row with author, attempt count and workflow version" <|
            \_ ->
                Common.init "/agent/tickets"
                    |> Application.handleCallback
                        (Callback.AgentTicketsFetched
                            (Ok
                                (ticketsFrom
                                    """
                                    [ { "id": 3, "title": "ret, again", "state": "running"
                                      , "workflow_name": "develop", "workflow_version": 2
                                      , "user_name": "alice", "attempt_count": 2, "created_at": 100 }
                                    ]
                                    """
                                )
                            )
                        )
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-ticket-row" ]
                    |> Query.has
                        [ containing [ text "alice" ]
                        , containing [ text "attempt 2" ]
                        , containing [ text "develop v2" ]
                        ]
        , test "surfaces unattributed spend as a footer line" <|
            \_ ->
                Common.init "/agent/tickets"
                    |> Application.handleCallback (Callback.AgentTicketsFetched (Ok sampleTickets))
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentCostRollupFetched
                            (Ok
                                { groupBy = "ticket"
                                , summary =
                                    { dailyCapUsd = 0
                                    , dailySpentUsd = 0
                                    , dailyRemainingUsd = 0
                                    , dailyExhausted = False
                                    }
                                , rows =
                                    [ { key = "", entries = 5, inputTokens = 0, outputTokens = 0, turns = 0, costUsd = 11.08 }
                                    , { key = "12", entries = 1, inputTokens = 0, outputTokens = 0, turns = 0, costUsd = 0.18 }
                                    ]
                                }
                            )
                        )
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ Test.Html.Selector.id "unattributed-cost" ]
                    |> Query.has [ containing [ text "$11.08" ] ]
        , test "renders the errored section above the draft section" <|
            \_ ->
                Common.init "/agent/tickets"
                    |> Application.handleCallback
                        (Callback.AgentTicketsFetched
                            (Ok
                                (ticketsFrom
                                    """
                                    [ { "id": 5, "title": "draft one", "state": "draft", "workflow_name": "develop", "created_at": 100 }
                                    , { "id": 6, "title": "errored one", "state": "errored", "workflow_name": "develop", "created_at": 200 }
                                    ]
                                    """
                                )
                            )
                        )
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.findAll [ Test.Html.Selector.tag "h2" ]
                    |> Expect.all
                        [ Query.index 0 >> Query.has [ text "Errored" ]
                        , Query.index 1 >> Query.has [ text "Draft" ]
                        ]
        , test "section header carries a count and spend rollup" <|
            \_ ->
                Common.init "/agent/tickets"
                    |> Application.handleCallback
                        (Callback.AgentTicketsFetched
                            (Ok
                                (ticketsFrom
                                    """
                                    [ { "id": 5, "title": "one", "state": "merged", "workflow_name": "develop", "created_at": 100 }
                                    , { "id": 6, "title": "two", "state": "merged", "workflow_name": "develop", "created_at": 200 }
                                    ]
                                    """
                                )
                            )
                        )
                    |> Tuple.first
                    |> Application.handleCallback
                        (Callback.AgentCostRollupFetched
                            (Ok
                                { groupBy = "ticket"
                                , summary =
                                    { dailyCapUsd = 0
                                    , dailySpentUsd = 0
                                    , dailyRemainingUsd = 0
                                    , dailyExhausted = False
                                    }
                                , rows =
                                    [ { key = "5", entries = 1, inputTokens = 0, outputTokens = 0, turns = 0, costUsd = 40.0 }
                                    , { key = "6", entries = 1, inputTokens = 0, outputTokens = 0, turns = 0, costUsd = 3.1 }
                                    ]
                                }
                            )
                        )
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.findAll [ Test.Html.Selector.tag "h2" ]
                    |> Query.first
                    |> Query.has [ text "Merged (2) · $43.10" ]
        , plumbingTest
        , openFormTest
        , submitTest
        ]


plumbingTest : Test
plumbingTest =
    test "CreateAgentTicket effect and AgentTicketCreated callback are wired" <|
        \_ ->
            -- Compile-level guard: these constructors must exist and typecheck.
            let
                effect =
                    Effects.CreateAgentTicket
                        { title = "t"
                        , body = ""
                        , repo = "o/n"
                        , targetBranch = ""
                        , workflowName = ""
                        , workflowVersion = Nothing
                        , budgetUsd = Nothing
                        }

                callback =
                    Callback.AgentTicketCreated Data.httpUnauthorized
            in
            Expect.all
                [ \_ -> Expect.equal effect effect
                , \_ -> Expect.equal callback callback
                ]
                ()


openFormTest : Test
openFormTest =
    describe "new-ticket form"
        [ test "New ticket button is present" <|
            \_ ->
                Common.init "/agent/tickets"
                    |> Common.queryView
                    |> Query.find [ class "agent-new-ticket-open" ]
                    |> Query.has [ text "New ticket" ]
        , test "clicking New ticket reveals the form and fetches workflows" <|
            \_ ->
                let
                    ( _, effects ) =
                        Common.init "/agent/tickets"
                            |> Application.update
                                (Msgs.Update Message.ClickNewAgentTicket)
                in
                Expect.equal True (List.member Effects.FetchAgentWorkflows effects)
        ]


submitTest : Test
submitTest =
    describe "submitting the new-ticket form"
        [ test "valid submit emits CreateAgentTicket with the entered fields" <|
            \_ ->
                let
                    ( _, effects ) =
                        Common.init "/agent/tickets"
                            |> Application.update (Msgs.Update Message.ClickNewAgentTicket)
                            |> Tuple.first
                            |> Application.update (Msgs.Update (Message.NewAgentTicketTitleChanged "ship it"))
                            |> Tuple.first
                            |> Application.update (Msgs.Update (Message.NewAgentTicketRepoChanged "o/n"))
                            |> Tuple.first
                            |> Application.update (Msgs.Update Message.SubmitNewAgentTicket)
                in
                Expect.equal True
                    (List.member
                        (Effects.CreateAgentTicket
                            { title = "ship it"
                            , body = ""
                            , repo = "o/n"
                            , targetBranch = ""
                            , workflowName = ""
                            , workflowVersion = Nothing
                            , budgetUsd = Nothing
                            }
                        )
                        effects
                    )
        , test "submit with empty repo does not emit CreateAgentTicket" <|
            \_ ->
                let
                    ( _, effects ) =
                        Common.init "/agent/tickets"
                            |> Application.update (Msgs.Update Message.ClickNewAgentTicket)
                            |> Tuple.first
                            |> Application.update (Msgs.Update (Message.NewAgentTicketTitleChanged "no repo"))
                            |> Tuple.first
                            |> Application.update (Msgs.Update Message.SubmitNewAgentTicket)
                in
                Expect.equal False
                    (List.any
                        (\e ->
                            case e of
                                Effects.CreateAgentTicket _ ->
                                    True

                                _ ->
                                    False
                        )
                        effects
                    )
        , test "created ticket navigates to its detail page" <|
            \_ ->
                let
                    created =
                        { id = 99
                        , title = "ship it"
                        , body = ""
                        , state = "draft"
                        , origin = "web"
                        , repo = "o/n"
                        , targetBranch = ""
                        , workflowName = ""
                        , budgetUsd = Nothing
                        , userName = "me"
                        , branch = ""
                        , createdAt = 0
                        , updatedAt = 0
                        , workflowVersion = Nothing
                        , pipelineRunId = Nothing
                        , attemptCount = 0
                        , errorDetail = ""
                        , completedAt = Nothing
                        }

                    ( _, effects ) =
                        Common.init "/agent/tickets"
                            |> Application.handleCallback
                                (Callback.AgentTicketCreated (Ok created))
                in
                Expect.equal True
                    (List.member (Effects.NavigateTo "/agent/tickets/99") effects)
        ]
