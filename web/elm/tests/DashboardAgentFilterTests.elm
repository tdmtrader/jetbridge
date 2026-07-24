module DashboardAgentFilterTests exposing (all)

import Application.Application as Application
import Common
import DashboardTests exposing (whenOnDashboard)
import Data
import Expect
import Message.Callback as Callback
import Message.Message as Msgs
import Message.TopLevelMessage as ApplicationMsgs
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (class, containing, text)


load : Application.Model
load =
    whenOnDashboard { highDensity = False }
        |> Application.handleCallback
            (Callback.AllPipelinesFetched
                (Ok
                    [ Data.pipeline "team" 0 |> Data.withName "agent-ticket-12"
                    , Data.pipeline "team" 1 |> Data.withName "my-service"
                    ]
                )
            )
        |> Tuple.first
        |> Common.givenDataUnauthenticated []
        |> Tuple.first


applyFilter : String -> Application.Model -> Application.Model
applyFilter q =
    Application.update (ApplicationMsgs.Update (Msgs.FilterMsg q)) >> Tuple.first


all : Test
all =
    describe "dashboard pipeline filtering"
        [ test "shows both pipelines with no filter" <|
            \_ ->
                load
                    |> Common.queryView
                    |> Expect.all
                        [ Query.has [ class "card", containing [ text "agent-ticket-12" ] ]
                        , Query.has [ class "card", containing [ text "my-service" ] ]
                        ]
        , test "finds an agent-ticket-prefixed pipeline as an ordinary name" <|
            \_ ->
                load
                    |> applyFilter "agent-ticket-12"
                    |> Common.queryView
                    |> Expect.all
                        [ Query.has [ class "card", containing [ text "agent-ticket-12" ] ]
                        , Query.hasNot [ class "card", containing [ text "my-service" ] ]
                        ]
        , test "is:agent has no special ownership meaning" <|
            \_ ->
                load
                    |> applyFilter "is:agent"
                    |> Common.queryView
                    |> Expect.all
                        [ Query.hasNot [ class "card", containing [ text "agent-ticket-12" ] ]
                        , Query.hasNot [ class "card", containing [ text "my-service" ] ]
                        ]
        , test "-is:agent does not hide pipelines by agent-ticket prefix" <|
            \_ ->
                load
                    |> applyFilter "-is:agent"
                    |> Common.queryView
                    |> Expect.all
                        [ Query.has [ class "card", containing [ text "my-service" ] ]
                        , Query.has [ class "card", containing [ text "agent-ticket-12" ] ]
                        ]
        ]
