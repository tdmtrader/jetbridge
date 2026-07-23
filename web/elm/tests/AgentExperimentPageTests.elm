module AgentExperimentPageTests exposing (all)

import AgenticData
import Application.Application as Application
import Common
import Dict
import Expect
import Html.Attributes as Attr
import Http
import Message.Callback as Callback
import Message.Effects as Effects
import Message.Message as Message
import Message.Subscription exposing (Delivery(..), Interval(..))
import Message.TopLevelMessage as Msgs
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (attribute, class, containing, text)
import Time


all : Test
all =
    describe "experiment detail"
        [ test "shows frozen variants, fixtures, evaluator, and ordinary run links" <|
            \_ ->
                initialized
                    |> Common.queryView
                    |> Query.has
                        [ class "agent-experiment-signature"
                        , containing [ text "repository : repository/v1" ]
                        , class "agent-experiment-variants"
                        , containing [ text "control · control" ]
                        , class "agent-experiment-fixtures"
                        , containing [ text "negative · negative_control" ]
                        , class "agent-experiment-evaluator"
                        , containing [ text "review-evaluator@3 · measurements → measurements" ]
                        , attribute (Attr.href "/agent/workflows/review-api/runs/9007199254740993")
                        , class "agent-experiment-promotion-link"
                        ]
        , test "shows failed recommendation conditions and raw cells together" <|
            \_ ->
                initialized
                    |> Common.queryView
                    |> Query.find [ class "agent-experiment-scorecard" ]
                    |> Query.has
                        [ class "agent-scorecard-insufficient"
                        , containing [ text "at least five pairs; 80% coverage" ]
                        , class "agent-scorecard-raw-cells"
                        , containing [ text "9007199254741013" ]
                        ]
        , test "cancellation carries the optimistic revision" <|
            \_ ->
                initialized
                    |> Application.update (Msgs.Update Message.AgentExperimentCancelClicked)
                    |> Tuple.second
                    |> Common.contains
                        (Effects.CancelAgentExperiment AgenticData.experiment.id AgenticData.experiment.revision)
        , test "refreshes the definition, matrix, and scorecard while the experiment is active" <|
            \_ ->
                initialized
                    |> Application.update
                        (Msgs.DeliveryReceived (ClockTicked FiveSeconds (Time.millisToPosix 0)))
                    |> Tuple.second
                    |> Expect.all
                        [ Common.contains (Effects.FetchAgentExperiment AgenticData.experiment.id)
                        , Common.contains (Effects.FetchAgentExperimentCells AgenticData.experiment.id)
                        , Common.contains (Effects.FetchAgentExperimentScorecard AgenticData.experiment.id)
                        ]
        , test "removes the experiment refresh timer after a terminal transition" <|
            \_ ->
                initialized
                    |> Application.handleCallback
                        (Callback.AgentExperimentFetched
                            AgenticData.experiment.id
                            (Ok terminalExperiment)
                        )
                    |> Tuple.first
                    |> Application.update
                        (Msgs.DeliveryReceived (ClockTicked FiveSeconds (Time.millisToPosix 0)))
                    |> Tuple.second
                    |> Common.notContains (Effects.FetchAgentExperiment AgenticData.experiment.id)
        , test "distinguishes an expected nonterminal scorecard from a service failure" <|
            \_ ->
                Expect.all
                    [ \_ ->
                        scorecardErrorModel 409
                            |> Common.queryView
                            |> Query.has [ class "agent-scorecard-unavailable", containing [ text "unavailable until" ] ]
                    , \_ ->
                        scorecardErrorModel 500
                            |> Common.queryView
                            |> Expect.all
                                [ Query.has [ class "agent-page-error" ]
                                , Query.hasNot [ class "agent-scorecard-unavailable" ]
                                ]
                    ]
                    ()
        ]


initialized : Application.Model
initialized =
    Common.init "/agent/experiments/9007199254741011"
        |> Application.handleCallback
            (Callback.AgentExperimentFetched AgenticData.experiment.id (Ok AgenticData.experiment))
        |> Tuple.first
        |> Application.handleCallback
            (Callback.AgentExperimentCellsFetched AgenticData.experiment.id (Ok [ AgenticData.storedCell ]))
        |> Tuple.first
        |> Application.handleCallback
            (Callback.AgentExperimentScorecardFetched AgenticData.experiment.id (Ok AgenticData.scorecard))
        |> Tuple.first


terminalExperiment =
    let
        experiment =
            AgenticData.experiment

        definition =
            experiment.definition
    in
    { experiment | definition = { definition | state = "completed" } }


scorecardErrorModel : Int -> Application.Model
scorecardErrorModel statusCode =
    Common.init "/agent/experiments/9007199254741011"
        |> Application.handleCallback
            (Callback.AgentExperimentFetched AgenticData.experiment.id (Ok AgenticData.experiment))
        |> Tuple.first
        |> Application.handleCallback
            (Callback.AgentExperimentCellsFetched AgenticData.experiment.id (Ok [ AgenticData.storedCell ]))
        |> Tuple.first
        |> Application.handleCallback
            (Callback.AgentExperimentScorecardFetched AgenticData.experiment.id
                (Err
                    (Http.BadStatus
                        { url = "http://example.com"
                        , status = { code = statusCode, message = "" }
                        , headers = Dict.empty
                        , body = ""
                        }
                    )
                )
            )
        |> Tuple.first
