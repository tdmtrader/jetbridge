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

import AgentPage.Chrome as Chrome
import Application.Models exposing (Session)
import Concourse.AgentReview as AgentReview
import EffectTransformer exposing (ET)
import Html exposing (Html)
import Html.Attributes exposing (checked, class, href, placeholder, style, type_, value)
import Html.Events exposing (onCheck, onInput)
import Login.Login as Login
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message(..))
import Message.Subscription exposing (Subscription)
import Routes
import Tooltip


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
    Chrome.view session
        model
        (Routes.AgentReviews { teamName = model.teamName })
        ("Agent reviews — " ++ model.teamName)
        "review projections, keyed by the snapshot each one judged"
        [ content model ]


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
    s.evaluatedCount < AgentReview.findingTotal s


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


{-| Where a row goes when you click it.

The durable workflow run owns the review, so that is the first destination: it
is the page that holds the run's steps, cost, outcome and this very review. A
build link is the fallback for a review produced outside a run, and a review
with neither (an upload occurrence) is not a link at all — a href to
`/builds/0` was a dead end dressed up as navigation.

-}
rowDestination : AgentReview.Summary -> Maybe String
rowDestination s =
    case ( s.workflowRunId, s.workflowName ) of
        ( Just runId, workflowName ) ->
            if workflowName == "" then
                buildDestination s

            else
                Just (Routes.toString (Routes.AgentWorkflowRun { workflowName = workflowName, id = runId }))

        ( Nothing, _ ) ->
            buildDestination s


buildDestination : AgentReview.Summary -> Maybe String
buildDestination s =
    if s.buildId > 0 then
        Just (Routes.toString (Routes.OneOffBuild { id = s.buildId, highlight = Routes.HighlightNothing }))

    else
        Nothing


rowStyles : List (Html.Attribute Message)
rowStyles =
    [ class "agent-review-row"
    , style "display" "flex"
    , style "align-items" "center"
    , style "gap" "12px"
    , style "padding" "8px 12px"
    , style "border-bottom" "1px solid #3d3c3c"
    , style "color" "inherit"
    , style "text-decoration" "none"
    ]


reviewRow : AgentReview.Summary -> Html Message
reviewRow s =
    let
        body =
            rowBody s
    in
    case rowDestination s of
        Just destination ->
            Html.a (href destination :: rowStyles) body

        Nothing ->
            Html.div rowStyles body


rowBody : AgentReview.Summary -> List (Html Message)
rowBody s =
    let
        ( background, foreground ) =
            AgentReview.conclusionTone s.conclusion

        total =
            AgentReview.findingTotal s
    in
    [ Html.span
        [ class "agent-review-conclusion"
        , style "padding" "2px 8px"
        , style "font-weight" "700"
        , style "background" background
        , style "color" foreground
        ]
        [ Html.text (AgentReview.conclusionLabel s.conclusion) ]
    , Html.div []
        [ Html.div [] [ Html.text (rowTitle s) ]
        , Html.div
            [ style "font-family" "monospace", style "font-size" "12px", style "color" "#7a7a7a" ]
            [ Html.text
                (String.fromInt (AgentReview.substantiveCount s)
                    ++ " findings · "
                    ++ String.fromInt (AgentReview.observationCount s)
                    ++ " obs"
                )
            ]
        ]
    , Html.span [ style "margin-left" "auto", style "color" "#b0b0b0" ]
        [ Html.text
            ("your feedback: "
                ++ String.fromInt s.evaluatedCount
                ++ " of "
                ++ String.fromInt total
                ++ " verdicts"
            )
        ]
    ]


{-| Name the occurrence with whatever it actually has. A review produced by a
workflow run is named by the workflow; one produced in a build by its
pipeline/job/build; an upload occurrence by neither, so it says so instead of
rendering `/` separators around empty strings.
-}
rowTitle : AgentReview.Summary -> String
rowTitle s =
    if s.workflowName /= "" then
        s.workflowName
            ++ (case s.workflowRunId of
                    Just runId ->
                        " · run " ++ runId

                    Nothing ->
                        ""
               )

    else if s.buildId > 0 then
        s.pipelineName ++ " / " ++ s.jobName ++ " #" ++ s.buildName ++ " · build " ++ String.fromInt s.buildId

    else
        "uploaded review"
