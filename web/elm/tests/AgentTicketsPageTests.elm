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


initAgentTickets : ( Application.Model, List Effects.Effect )
initAgentTickets =
    Application.init Data.flags
        { protocol = Url.Http
        , host = ""
        , port_ = Nothing
        , path = "/agent-tickets"
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
        , test "fetches tickets and dispatcher status on load — and no per-ticket cost rollup" <|
            \_ ->
                -- Cost belongs to the workflow run; the ticket-keyed rollup the
                -- queue page used to poll is not a server dimension any more.
                initAgentTickets
                    |> Tuple.second
                    |> Expect.all
                        [ Common.contains Effects.FetchAgentTickets
                        , Common.contains Effects.FetchAgentDispatcher
                        , Common.notContains Effects.FetchAgentCostRollup
                        , Common.notContains Effects.FetchAgentWorkflowCosts
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
                Common.init "/agent-tickets"
                    |> Application.handleCallback (dispatcherStatus "active")
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ Test.Html.Selector.id "dispatcher-status-pill" ]
                    |> Query.has [ text "Auto-dispatch: active" ]
        , test "shows the pause banner when auto-dispatch is not active" <|
            \_ ->
                Common.init "/agent-tickets"
                    |> Application.handleCallback (dispatcherStatus "paused")
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ Test.Html.Selector.id "dispatcher-banner" ]
                    |> Query.has [ text "manually" ]
        , test "shows no dispatcher banner when auto-dispatch is active" <|
            \_ ->
                Common.init "/agent-tickets"
                    |> Application.handleCallback (dispatcherStatus "active")
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.findAll [ Test.Html.Selector.id "dispatcher-banner" ]
                    |> Query.count (Expect.equal 0)
        , test "renders a ticket row with id, title and workflow" <|
            \_ ->
                Common.init "/agent-tickets"
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
        , test "shows an empty-state notice when there are no tickets" <|
            \_ ->
                Common.init "/agent-tickets"
                    |> Application.handleCallback (Callback.AgentTicketsFetched (Ok []))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has [ text "No tickets yet." ]
        , test "shows an error notice when tickets fail to load" <|
            \_ ->
                Common.init "/agent-tickets"
                    |> Application.handleCallback (Callback.AgentTicketsFetched Data.httpUnauthorized)
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has [ text "Couldn't load tickets." ]
        , test "never renders a delivered branch: the ticket does not carry one" <|
            \_ ->
                -- agent_tickets.branch was written by harvest, which is gone.
                -- A server that still sends the key must not resurrect the
                -- column on the queue row; the delivered branch lives on the
                -- durable workflow run.
                Common.init "/agent-tickets"
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
                    |> Query.hasNot [ text "agent/ticket-12" ]
        , test "live-updates: refetches tickets on the five second tick" <|
            \_ ->
                Common.init "/agent-tickets"
                    |> Application.update
                        (Msgs.DeliveryReceived (ClockTicked FiveSeconds <| Time.millisToPosix 0))
                    |> Tuple.second
                    |> Common.contains Effects.FetchAgentTickets
        , test "client-side filter narrows the visible rows by title" <|
            \_ ->
                Common.init "/agent-tickets"
                    |> Application.handleCallback (Callback.AgentTicketsFetched (Ok sampleTickets))
                    |> Tuple.first
                    |> Application.update (Msgs.Update (Message.AgentTicketsFilterChanged "gap"))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.findAll [ class "agent-ticket-row" ]
                    |> Query.count (Expect.equal 1)
        , test "enriches a row with author, attempt count and workflow version" <|
            \_ ->
                Common.init "/agent-tickets"
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
        , test "renders the needs-review section above the draft section" <|
            \_ ->
                Common.init "/agent-tickets"
                    |> Application.handleCallback
                        (Callback.AgentTicketsFetched
                            (Ok
                                (ticketsFrom
                                    """
                                    [ { "id": 5, "title": "draft one", "state": "draft", "workflow_name": "develop", "created_at": 100 }
                                    , { "id": 6, "title": "waiting on you", "state": "needs_review", "workflow_name": "develop", "created_at": 200 }
                                    ]
                                    """
                                )
                            )
                        )
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.findAll [ Test.Html.Selector.tag "h2" ]
                    |> Expect.all
                        [ Query.index 0 >> Query.has [ text "Needs your review" ]
                        , Query.index 1 >> Query.has [ text "Draft" ]
                        ]
        , test "section header carries a count, and closed is the one terminal section" <|
            \_ ->
                Common.init "/agent-tickets"
                    |> Application.handleCallback
                        (Callback.AgentTicketsFetched
                            (Ok
                                (ticketsFrom
                                    """
                                    [ { "id": 5, "title": "one", "state": "closed", "workflow_name": "develop", "created_at": 100 }
                                    , { "id": 6, "title": "two", "state": "closed", "workflow_name": "develop", "created_at": 200 }
                                    ]
                                    """
                                )
                            )
                        )
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.findAll [ Test.Html.Selector.tag "h2" ]
                    |> Query.first
                    |> Query.has [ text "Closed (2)" ]
        ]
