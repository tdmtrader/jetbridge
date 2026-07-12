module Agent.Agent exposing
    ( Model
    , documentTitle
    , handleCallback
    , init
    , subscriptions
    , tooltip
    , update
    , view
    )

import Application.Models exposing (Session)
import Concourse.Agent as Agent
import EffectTransformer exposing (ET)
import Html exposing (Html)
import Html.Attributes exposing (class, id, style)
import Login.Login as Login
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message)
import Message.Subscription exposing (Subscription)
import Routes
import SideBar.SideBar as SideBar
import Tooltip
import Views.Styles
import Views.TopBar as TopBar


type alias Model =
    Login.Model
        { workflows : Maybe (List Agent.WorkflowSummary)
        , costs : Maybe Agent.CostRollup
        }


init : ( Model, List Effect )
init =
    ( { workflows = Nothing
      , costs = Nothing
      , isUserMenuExpanded = False
      }
    , [ FetchAgentWorkflows, FetchAgentCostRollup ]
    )


documentTitle : String
documentTitle =
    "Agent"


handleCallback : Callback -> ET Model
handleCallback callback ( model, effects ) =
    case callback of
        AgentWorkflowsFetched (Ok workflows) ->
            ( { model | workflows = Just workflows }, effects )

        AgentCostRollupFetched (Ok costs) ->
            ( { model | costs = Just costs }, effects )

        _ ->
            ( model, effects )


update : Message -> ET Model
update _ ( model, effects ) =
    ( model, effects )


tooltip : Model -> a -> Maybe Tooltip.Tooltip
tooltip _ _ =
    Nothing


subscriptions : List Subscription
subscriptions =
    []


view : Session -> Model -> Html Message
view session model =
    let
        route =
            Routes.Agent
    in
    Html.div
        (id "page-including-top-bar" :: Views.Styles.pageIncludingTopBar)
        [ Html.div
            (id "top-bar-app" :: Views.Styles.topBar False)
            [ Html.div
                [ style "display" "flex", style "align-items" "center" ]
                (SideBar.sideBarIcon session
                    :: TopBar.breadcrumbs session route
                )
            , Login.view session.userState model
            ]
        , Html.div
            (id "page-below-top-bar" :: Views.Styles.pageBelowTopBar route)
            [ SideBar.view session Nothing
            , Html.div
                [ style "padding" "16px", style "width" "100%" ]
                [ Html.h1 [ style "font-size" "18px" ] [ Html.text "Agent" ]
                , workflowsSection model
                , costsSection model
                ]
            ]
        ]


workflowsSection : Model -> Html Message
workflowsSection model =
    Html.div [ class "agent-workflows", style "margin-top" "16px" ]
        [ Html.h2 [ style "font-size" "15px" ] [ Html.text "workflows" ]
        , case model.workflows of
            Nothing ->
                Html.p [ style "color" "#b0b0b0" ] [ Html.text "Loading workflows…" ]

            Just [] ->
                Html.p [ style "color" "#b0b0b0" ] [ Html.text "No workflows yet." ]

            Just workflows ->
                Html.div [] (List.map workflowRow workflows)
        ]


workflowRow : Agent.WorkflowSummary -> Html Message
workflowRow w =
    Html.div
        [ style "padding" "6px 0"
        , style "border-bottom" "1px solid #3d3c3c"
        ]
        [ Html.div [] [ Html.text w.name ]
        , Html.div
            [ style "font-family" "monospace", style "font-size" "12px", style "color" "#7a7a7a" ]
            [ Html.text
                ("v"
                    ++ String.fromInt w.latestVersion
                    ++ " · live v"
                    ++ String.fromInt w.liveVersion
                )
            ]
        ]


costsSection : Model -> Html Message
costsSection model =
    Html.div [ class "agent-costs", style "margin-top" "16px" ]
        [ Html.h2 [ style "font-size" "15px" ] [ Html.text "costs" ]
        , case model.costs of
            Nothing ->
                Html.p [ style "color" "#b0b0b0" ] [ Html.text "Loading costs…" ]

            Just rollup ->
                Html.div []
                    (Html.div
                        [ style "font-family" "monospace", style "font-size" "12px", style "color" "#7a7a7a" ]
                        [ Html.text
                            ("spent $"
                                ++ String.fromFloat rollup.summary.dailySpentUsd
                                ++ " / cap $"
                                ++ String.fromFloat rollup.summary.dailyCapUsd
                            )
                        ]
                        :: List.map costRow rollup.rows
                    )
        ]


costRow : Agent.CostRow -> Html Message
costRow r =
    Html.div
        [ style "padding" "6px 0"
        , style "border-bottom" "1px solid #3d3c3c"
        ]
        [ Html.div [] [ Html.text r.key ]
        , Html.div
            [ style "font-family" "monospace", style "font-size" "12px", style "color" "#7a7a7a" ]
            [ Html.text
                (String.fromInt r.entries
                    ++ " entries · $"
                    ++ String.fromFloat r.costUsd
                )
            ]
        ]
