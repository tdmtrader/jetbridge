module AgentExperimentsPageTests exposing (all)

import AgenticData
import Application.Application as Application
import Common
import Html.Attributes as Attr
import Message.Callback as Callback
import Message.Effects as Effects
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (attribute, class, containing, text)


all : Test
all =
    describe "experiment laboratory list"
        [ test "loads cells for progress after durable experiment definitions" <|
            \_ ->
                Common.init "/agent/experiments"
                    |> Application.handleCallback
                        (Callback.AgentExperimentsFetched (Ok [ AgenticData.experiment ]))
                    |> Tuple.second
                    |> Common.contains (Effects.FetchAgentExperimentCells AgenticData.experiment.id)
        , test "shows matrix progress, control, workflow, and attention" <|
            \_ ->
                initialized
                    |> Common.queryView
                    |> Query.find [ class "agent-experiment-row" ]
                    |> Query.has
                        [ attribute (Attr.href "/agent/experiments/9007199254741011")
                        , containing [ text "2 variants × 2 fixtures × 5 repetitions · 1/20 terminal" ]
                        , containing [ text "workflow: review-api · control: control · negative controls: 1" ]
                        , containing [ class "agent-experiment-needs-attention", text "needs attention" ]
                        ]
        , test "labels the bounded browser history page" <|
            \_ ->
                initialized
                    |> Common.queryView
                    |> Query.find [ class "agent-experiments-history-scope" ]
                    |> Query.has [ text "Showing up to the newest 100 experiments" ]
        ]


initialized : Application.Model
initialized =
    Common.init "/agent/experiments"
        |> Application.handleCallback
            (Callback.AgentExperimentsFetched (Ok [ AgenticData.experiment ]))
        |> Tuple.first
        |> Application.handleCallback
            (Callback.AgentExperimentCellsFetched AgenticData.experiment.id (Ok [ AgenticData.storedCell ]))
        |> Tuple.first
