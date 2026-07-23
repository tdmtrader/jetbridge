module AgentExperiments.AgentExperiments exposing
    ( Model
    , documentTitle
    , handleCallback
    , handleDelivery
    , init
    , subscriptions
    , tooltip
    , update
    , view
    )

import AgentPage.Chrome as Chrome
import Application.Models exposing (Session)
import Concourse.Experiment as Experiment
import Dict exposing (Dict)
import EffectTransformer exposing (ET)
import Html exposing (Html)
import Html.Attributes exposing (class, href, style)
import Login.Login as Login
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message)
import Message.Subscription exposing (Delivery, Interval(..), Subscription)
import Polling
import Routes
import Tooltip


type alias Model =
    Login.Model
        { experiments : Maybe (List Experiment.Experiment)
        , cells : Dict String (List Experiment.StoredCell)
        , loadError : Bool
        }


init : ( Model, List Effect )
init =
    ( { experiments = Nothing
      , cells = Dict.empty
      , loadError = False
      , isUserMenuExpanded = False
      }
    , [ FetchAgentExperiments ]
    )


documentTitle : String
documentTitle =
    "Workflow experiments"


handleCallback : Callback -> ET Model
handleCallback callback ( model, effects ) =
    case callback of
        AgentExperimentsFetched (Ok experiments) ->
            ( { model | experiments = Just experiments, loadError = False }
            , effects ++ List.map (FetchAgentExperimentCells << .id) experiments
            )

        AgentExperimentsFetched (Err _) ->
            ( { model | loadError = True }, effects )

        AgentExperimentCellsFetched experimentId (Ok cells) ->
            ( { model | cells = Dict.insert experimentId cells model.cells }, effects )

        AgentExperimentCellsFetched _ (Err _) ->
            ( { model | loadError = True }, effects )

        _ ->
            ( model, effects )


update : Message -> ET Model
update _ =
    identity


polls : List (Polling.Poll Model)
polls =
    [ { interval = OneMinute, fetch = \_ -> [ FetchAgentExperiments ] } ]


handleDelivery : Delivery -> ET Model
handleDelivery =
    Polling.handleDelivery polls


subscriptions : List Subscription
subscriptions =
    Polling.subscriptions polls


tooltip : Model -> a -> Maybe Tooltip.Tooltip
tooltip _ _ =
    Nothing


view : Session -> Model -> Html Message
view session model =
    Chrome.view session
        model
        Routes.AgentExperiments
        "Experiment laboratory"
        "controlled comparisons over frozen workflow definitions and snapshot fixtures"
        [ if model.loadError then
            Html.p [ class "agent-page-error", style "color" "#e0a44e" ]
                [ Html.text "Some experiment state could not be refreshed; the last durable records are shown." ]

          else
            Html.text ""
        , case model.experiments of
            Nothing ->
                muted "loading experiment matrix…"

            Just [] ->
                Html.div [ class "agent-experiments-empty" ]
                    [ Html.p [] [ Html.text "No workflow experiments yet." ]
                    , Html.p [ style "color" "#8a8a8a" ]
                        [ Html.text "Create and freeze one with fly agent experiments; operational workflows remain independent." ]
                    ]

            Just experiments ->
                Html.div [ class "agent-experiments" ]
                    (Html.p [ class "agent-experiments-history-scope", style "color" "#8a8a8a" ]
                        [ Html.text "Showing up to the newest 100 experiments; use fly or the paginated API for complete history." ]
                        :: List.map (experimentRow model) experiments
                    )
        ]


experimentRow : Model -> Experiment.Experiment -> Html Message
experimentRow model experiment =
    let
        definition =
            experiment.definition

        cells =
            Dict.get experiment.id model.cells |> Maybe.withDefault []

        expected =
            List.length definition.variants
                * List.length definition.fixtures
                * definition.repetitions

        terminal =
            List.length (List.filter (isTerminal << .status) cells)

        attention =
            definition.state
                == "failed"
                || List.any (isFailure << .status) cells

        control =
            definition.variants
                |> List.filter .control
                |> List.head
                |> Maybe.map .label
                |> Maybe.withDefault "missing control"

        workflows =
            definition.variants
                |> List.map (.target >> .workflowName)
                |> unique
                |> String.join ", "

        negativeControls =
            definition.fixtures
                |> List.filter (\fixture -> fixture.role == "negative_control")
                |> List.length
    in
    Html.article
        [ class "agent-experiment-row"
        , style "border" "1px solid #3d3c3c"
        , style "padding" "12px 14px"
        , style "margin-bottom" "12px"
        ]
        [ Html.div [ style "display" "flex", style "align-items" "baseline", style "gap" "10px" ]
            [ Html.a
                [ class "agent-experiment-link"
                , href (Routes.toString (Routes.AgentExperiment { id = experiment.id }))
                , style "font-weight" "700"
                ]
                [ Html.text definition.name ]
            , badge "agent-experiment-state" definition.state
            , if attention then
                badge "agent-experiment-needs-attention" "needs attention"

              else
                Html.text ""
            ]
        , Html.p [ style "font-family" "monospace", style "font-size" "12px" ]
            [ Html.text
                (String.fromInt (List.length definition.variants)
                    ++ " variants × "
                    ++ String.fromInt (List.length definition.fixtures)
                    ++ " fixtures × "
                    ++ String.fromInt definition.repetitions
                    ++ " repetitions · "
                    ++ String.fromInt terminal
                    ++ "/"
                    ++ String.fromInt expected
                    ++ " terminal"
                )
            ]
        , Html.p [ style "color" "#8a8a8a", style "margin" "4px 0" ]
            [ Html.text
                ("workflow: "
                    ++ workflows
                    ++ " · control: "
                    ++ control
                    ++ " · negative controls: "
                    ++ String.fromInt negativeControls
                )
            ]
        ]


isTerminal : String -> Bool
isTerminal status =
    not (List.member status [ "pending", "running" ])


isFailure : String -> Bool
isFailure status =
    String.contains "failure" status


unique : List String -> List String
unique values =
    List.foldl
        (\value result ->
            if List.member value result then
                result

            else
                result ++ [ value ]
        )
        []
        values


badge : String -> String -> Html Message
badge className label =
    Html.span
        [ class className
        , style "padding" "2px 7px"
        , style "background" "#302f2f"
        , style "font-size" "11px"
        ]
        [ Html.text label ]


muted : String -> Html Message
muted message =
    Html.p [ style "color" "#8a8a8a", style "font-family" "monospace" ] [ Html.text message ]
