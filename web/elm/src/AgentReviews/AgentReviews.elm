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
import Html.Attributes exposing (checked, class, href, id, placeholder, style, type_, value)
import Html.Events exposing (onCheck, onInput)
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
        , unevaluatedFirst : Bool
        , pipelineFilter : String
        }


init : { teamName : String } -> ( Model, List Effect )
init { teamName } =
    ( { teamName = teamName
      , reviews = []
      , loaded = False
      , loadError = False
      , unevaluatedFirst = False
      , pipelineFilter = ""
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
update msg ( model, effects ) =
    case msg of
        AgentReviewsUnevaluatedToggled on ->
            ( { model | unevaluatedFirst = on }, effects )

        AgentReviewsPipelineFilterChanged f ->
            ( { model | pipelineFilter = f }, effects )

        _ ->
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
                , content model
                ]
            ]
        ]


content : Model -> Html Message
content model =
    if model.loadError then
        Html.p [ style "color" "#f0a0a0" ] [ Html.text "Couldn't load agent reviews." ]

    else if model.loaded && List.isEmpty model.reviews then
        Html.p [ style "color" "#b0b0b0" ] [ Html.text "No agent reviews yet." ]

    else
        let
            visible =
                visibleReviews model
        in
        Html.div []
            [ filterBar model
            , if List.isEmpty visible then
                Html.p [ style "color" "#b0b0b0" ] [ Html.text "No reviews match this filter." ]

              else
                Html.div [] (List.map reviewRow visible)
            ]


{-| Client-side filters over the already-fetched reviews: an optional
pipeline-name substring filter, then (when toggled) a stable sort that floats
reviews with unevaluated findings to the top. No server round-trip — all the
data is already in `model.reviews`.
-}
visibleReviews : Model -> List AgentReview.Summary
visibleReviews model =
    model.reviews
        |> List.filter (pipelineMatches model.pipelineFilter)
        |> (if model.unevaluatedFirst then
                List.sortBy
                    (\s ->
                        if isUnevaluated s then
                            0

                        else
                            1
                    )

            else
                identity
           )


pipelineMatches : String -> AgentReview.Summary -> Bool
pipelineMatches filter s =
    (filter == "")
        || String.contains (String.toLower filter) (String.toLower s.pipelineName)


isUnevaluated : AgentReview.Summary -> Bool
isUnevaluated s =
    s.evaluatedCount < s.provenCount + s.observationCount


filterBar : Model -> Html Message
filterBar model =
    Html.div
        [ style "display" "flex"
        , style "align-items" "center"
        , style "gap" "16px"
        , style "margin" "8px 0 12px"
        ]
        [ Html.label
            [ style "display" "flex"
            , style "align-items" "center"
            , style "gap" "6px"
            , style "color" "#b0b0b0"
            , style "font-size" "13px"
            , style "cursor" "pointer"
            ]
            [ Html.input
                [ type_ "checkbox"
                , checked model.unevaluatedFirst
                , onCheck AgentReviewsUnevaluatedToggled
                ]
                []
            , Html.text "Unevaluated first"
            ]
        , Html.input
            [ type_ "text"
            , class "agent-reviews-pipeline-filter"
            , placeholder "filter by pipeline"
            , value model.pipelineFilter
            , onInput AgentReviewsPipelineFilterChanged
            , style "background" "#141313"
            , style "color" "#e0e0e0"
            , style "border" "1px solid #3d3c3c"
            , style "padding" "4px 8px"
            ]
            []
        ]


reviewRow : AgentReview.Summary -> Html Message
reviewRow s =
    Html.a
        [ class "agent-review-row"
        , href (Routes.toString (Routes.OneOffBuild { id = s.buildId, highlight = Routes.HighlightNothing }))
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
                ("your feedback: "
                    ++ String.fromInt s.evaluatedCount
                    ++ " of "
                    ++ String.fromInt (s.provenCount + s.observationCount)
                    ++ " verdicts"
                )
            ]
        ]
