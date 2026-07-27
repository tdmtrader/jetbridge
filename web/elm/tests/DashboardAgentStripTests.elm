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
import Views.Truncate


ticketsFrom : String -> List AgentTicket.Ticket
ticketsFrom json =
    Json.Decode.decodeString (Json.Decode.list AgentTicket.decodeTicket) json
        |> Result.withDefault []


needsReviewTickets : List AgentTicket.Ticket
needsReviewTickets =
    ticketsFrom """[ { "id": 12, "title": "ship fly archives", "state": "needs_review", "created_at": 200 } ]"""


closedTickets : List AgentTicket.Ticket
closedTickets =
    ticketsFrom """[ { "id": 5, "title": "already done", "state": "closed", "created_at": 1 } ]"""


longTailTitle : String
longTailTitle =
    "recorder plus evidence harvester rewrite pass two (T9 only)"


longTitleTickets : List AgentTicket.Ticket
longTitleTickets =
    ticketsFrom
        ("""[ { "id": 42, "title": \""""
            ++ longTailTitle
            ++ """", "state": "needs_review", "created_at": 400 } ]"""
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
        [ test "fetches agent tickets on load, and no per-ticket cost rollup" <|
            \_ ->
                -- The strip shows queue state only: cost is the workflow run's,
                -- and the ticket-keyed rollup it used to join is gone.
                initDashboard
                    |> Tuple.second
                    |> Expect.all
                        [ Common.contains Effects.FetchAgentTickets
                        , Common.notContains Effects.FetchAgentCostRollup
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
                        , attribute (Attr.href "/agent-tickets/12")
                        , containing [ text "#12 ship fly archives" ]
                        ]
        , test "renders no strip when every ticket is closed" <|
            \_ ->
                load
                    |> Application.handleCallback (Callback.AgentTicketsFetched (Ok closedTickets))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.hasNot [ id "agent-ticket-strip" ]
        , test "middle-truncates a long chip label so the distinguishing tail survives (W-10)" <|
            \_ ->
                load
                    |> Application.handleCallback (Callback.AgentTicketsFetched (Ok longTitleTickets))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ id "agent-ticket-strip" ]
                    |> Query.has
                        [ containing
                            [ text (Views.Truncate.middle 48 ("#42 " ++ longTailTitle)) ]
                        ]
        , test "keeps the full untruncated title on the chip tooltip (W-10)" <|
            \_ ->
                load
                    |> Application.handleCallback (Callback.AgentTicketsFetched (Ok longTitleTickets))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ id "agent-ticket-strip" ]
                    |> Query.has [ attribute (Attr.title longTailTitle) ]
        ]
