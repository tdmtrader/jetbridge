module AgentReviews.AgentReviews exposing
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
import Concourse.AgentReview as AgentReview
import EffectTransformer exposing (ET)
import Html exposing (Html)
import Html.Attributes exposing (class, href, id, style)
import Login.Login as Login
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message(..))
import Message.Subscription exposing (Subscription)
import Routes
import SideBar.SideBar as SideBar
import Tooltip
import Views.Styles
import Views.TopBar as TopBar


type alias Model =
    Login.Model
        { teamName : String
        , reviews : List AgentReview.Summary
        , loaded : Bool
        , loadError : Bool
        }


init : { teamName : String } -> ( Model, List Effect )
init { teamName } =
    ( { teamName = teamName
      , reviews = []
      , loaded = False
      , loadError = False
      , isUserMenuExpanded = False
      }
    , [ FetchTeamAgentReviews teamName ]
    )


documentTitle : String
documentTitle =
    "Agent reviews"


handleCallback : Callback -> ET Model
handleCallback callback ( model, effects ) =
    case callback of
        TeamAgentReviewsFetched (Ok reviews) ->
            ( { model | reviews = reviews, loaded = True }, effects )

        TeamAgentReviewsFetched (Err _) ->
            ( { model | loaded = True, loadError = True }, effects )

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
            Routes.AgentReviews { teamName = model.teamName }
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
                [ Html.h1 [ style "font-size" "18px" ]
                    [ Html.text ("Agent reviews — " ++ model.teamName) ]
                , if model.loadError then
                    Html.p [ style "color" "#f0a0a0" ] [ Html.text "Couldn't load agent reviews." ]

                  else if model.loaded && List.isEmpty model.reviews then
                    Html.p [ style "color" "#b0b0b0" ] [ Html.text "No agent reviews yet." ]

                  else
                    Html.div [] (List.map reviewRow model.reviews)
                ]
            ]
        ]


reviewRow : AgentReview.Summary -> Html Message
reviewRow s =
    Html.a
        [ class "agent-review-row"
        , href ("/builds/" ++ String.fromInt s.buildId)
        , style "display" "flex"
        , style "align-items" "center"
        , style "gap" "12px"
        , style "padding" "8px 12px"
        , style "border-bottom" "1px solid #3d3c3c"
        , style "color" "inherit"
        , style "text-decoration" "none"
        ]
        [ Html.span
            [ style "padding" "2px 8px"
            , style "font-weight" "700"
            , style "background"
                (if s.pass then
                    "#2e4f2e"

                 else
                    "#5c2626"
                )
            , style "color"
                (if s.pass then
                    "#9fdf9f"

                 else
                    "#f0a0a0"
                )
            ]
            [ Html.text (String.fromFloat s.score) ]
        , Html.div []
            [ Html.div [] [ Html.text (s.pipelineName ++ " / " ++ s.jobName ++ " #" ++ s.buildName) ]
            , Html.div
                [ style "font-family" "monospace", style "font-size" "12px", style "color" "#7a7a7a" ]
                [ Html.text
                    (s.branch
                        ++ " @ "
                        ++ String.left 7 s.commitSha
                        ++ " · "
                        ++ String.fromInt s.provenCount
                        ++ " issues · "
                        ++ String.fromInt s.observationCount
                        ++ " obs"
                    )
                ]
            ]
        , Html.span [ style "margin-left" "auto", style "color" "#b0b0b0" ]
            [ Html.text
                ("evaluated "
                    ++ String.fromInt s.evaluatedCount
                    ++ "/"
                    ++ String.fromInt (s.provenCount + s.observationCount)
                )
            ]
        ]
