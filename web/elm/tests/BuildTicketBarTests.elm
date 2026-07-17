module BuildTicketBarTests exposing (all)

import Application.Application as Application
import Common
import Concourse
import Concourse.BuildStatus exposing (BuildStatus(..))
import Data
import Html.Attributes as Attr
import Message.Callback as Callback
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (attribute, containing, id, tag, text)


agentBuild : Concourse.Build
agentBuild =
    Data.jobBuild BuildStatusSucceeded
        |> Data.withJob (Just (Data.jobId |> Data.withPipelineName "agent-ticket-12"))


all : Test
all =
    describe "build ticket-context bar"
        [ test "links an agent-ticket build back to its ticket" <|
            \_ ->
                Common.init "/builds/1"
                    |> Application.handleCallback (Callback.BuildFetched (Ok agentBuild))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ id "build-ticket-context" ]
                    |> Query.has
                        [ tag "a"
                        , attribute (Attr.href "/agent-tickets/12")
                        , containing [ text "agent ticket #12" ]
                        ]
        , test "shows no context bar for an ordinary build" <|
            \_ ->
                Common.init "/builds/1"
                    |> Application.handleCallback
                        (Callback.BuildFetched (Ok (Data.jobBuild BuildStatusSucceeded)))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.hasNot [ id "build-ticket-context" ]
        ]
