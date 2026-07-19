module AgentTicketsPageTests exposing (all)

import Application.Application as Application
import Common
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
        , path = "/agent-tickets"
        , query = Nothing
        , fragment = Nothing
        }


all : Test
all =
    describe "ticket queue page"
        [ test "decodes the sample ticket fixtures" <|
            \_ ->
                List.length sampleTickets
                    |> Expect.equal 2
        , test "fetches tickets and costs on load" <|
            \_ ->
                initAgentTickets
                    |> Tuple.second
                    |> Expect.all
                        [ Common.contains Effects.FetchAgentTickets
                        , Common.contains Effects.FetchAgentTicketCosts
                        ]
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
        , test "joins per-ticket cost into the matching row" <|
            \_ ->
                Common.init "/agent-tickets"
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
        , test "shows the branch on rows that have a harvest branch" <|
            \_ ->
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
                    |> Query.find [ class "agent-ticket-branch" ]
                    |> Query.has [ text "agent/ticket-12" ]
        , test "live-updates: refetches tickets and costs on the five second tick" <|
            \_ ->
                Common.init "/agent-tickets"
                    |> Application.update
                        (Msgs.DeliveryReceived (ClockTicked FiveSeconds <| Time.millisToPosix 0))
                    |> Tuple.second
                    |> Expect.all
                        [ Common.contains Effects.FetchAgentTickets
                        , Common.contains Effects.FetchAgentTicketCosts
                        ]
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
        , test "surfaces unattributed spend as a footer line" <|
            \_ ->
                Common.init "/agent-tickets"
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
        ]
