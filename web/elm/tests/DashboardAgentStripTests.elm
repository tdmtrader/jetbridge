module DashboardAgentStripTests exposing (all)

import Application.Application as Application
import Common
import Concourse.AgentTicket as AgentTicket
import DashboardTests exposing (whenOnDashboard)
import Data
import Expect
import Html.Attributes as Attr
import Json.Decode
import Message.Callback as Callback
import Message.Effects as Effects
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (attribute, containing, id, tag, text)
import Url


ticketsFrom : String -> List AgentTicket.Ticket
ticketsFrom json =
    Json.Decode.decodeString (Json.Decode.list AgentTicket.decodeTicket) json
        |> Result.withDefault []


needsReviewTickets : List AgentTicket.Ticket
needsReviewTickets =
    ticketsFrom """[ { "id": 12, "title": "ship fly archives", "state": "needs_review", "created_at": 200 } ]"""


mergedTickets : List AgentTicket.Ticket
mergedTickets =
    ticketsFrom """[ { "id": 5, "title": "already done", "state": "merged", "created_at": 1 } ]"""


erroredTickets : List AgentTicket.Ticket
erroredTickets =
    ticketsFrom """[ { "id": 8, "title": "blew up", "state": "errored", "created_at": 300 } ]"""


costRollup : Callback.Callback
costRollup =
    Callback.AgentCostRollupFetched
        (Ok
            { groupBy = "ticket"
            , summary = { dailyCapUsd = 0, dailySpentUsd = 0, dailyRemainingUsd = 0, dailyExhausted = False }
            , rows = [ { key = "12", entries = 1, inputTokens = 0, outputTokens = 0, turns = 0, costUsd = 0.18 } ]
            }
        )


load : Application.Model
load =
    whenOnDashboard { highDensity = False }
        |> Application.handleCallback (Callback.AllPipelinesFetched (Ok []))
        |> Tuple.first
        |> Common.givenDataUnauthenticated []
        |> Tuple.first


initDashboard : ( Application.Model, List Effects.Effect )
initDashboard =
    Application.init Data.flags
        { protocol = Url.Http
        , host = ""
        , port_ = Nothing
        , path = "/"
        , query = Nothing
        , fragment = Nothing
        }


all : Test
all =
    describe "dashboard agent-ticket strip"
        [ test "fetches agent tickets and costs on load" <|
            \_ ->
                initDashboard
                    |> Tuple.second
                    |> Expect.all
                        [ Common.contains Effects.FetchAgentTickets
                        , Common.contains Effects.FetchAgentTicketCosts
                        ]
        , test "surfaces a needs_review ticket as a chip linking to its detail page" <|
            \_ ->
                load
                    |> Application.handleCallback (Callback.AgentTicketsFetched (Ok needsReviewTickets))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ id "agent-ticket-strip" ]
                    |> Query.has
                        [ tag "a"
                        , attribute (Attr.href "/agent/tickets/12")
                        , containing [ text "#12 ship fly archives" ]
                        ]
        , test "joins per-ticket cost into the chip" <|
            \_ ->
                load
                    |> Application.handleCallback (Callback.AgentTicketsFetched (Ok needsReviewTickets))
                    |> Tuple.first
                    |> Application.handleCallback costRollup
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ id "agent-ticket-strip" ]
                    |> Query.has [ containing [ text "$0.18" ] ]
        , test "renders no strip when no tickets need attention" <|
            \_ ->
                load
                    |> Application.handleCallback (Callback.AgentTicketsFetched (Ok mergedTickets))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.hasNot [ id "agent-ticket-strip" ]
        , test "surfaces an errored ticket as a chip (U9)" <|
            \_ ->
                load
                    |> Application.handleCallback (Callback.AgentTicketsFetched (Ok erroredTickets))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ id "agent-ticket-strip" ]
                    |> Query.has
                        [ tag "a"
                        , attribute (Attr.href "/agent/tickets/8")
                        , containing [ text "#8 blew up" ]
                        ]
        ]
