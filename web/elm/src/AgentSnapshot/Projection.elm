module AgentSnapshot.Projection exposing (view)

import AgentSnapshot.RepositoryChange as RepositoryChange
import Build.AgentReview as AgentReview
import Concourse.AgentReview exposing (BuildReview)
import Concourse.WorkflowRun exposing (RepositoryChange)
import Dict exposing (Dict)
import Html exposing (Html)
import Html.Attributes exposing (class, style)
import Message.Message exposing (Message)
import Set exposing (Set)


type alias ProjectionState model =
    { model
        | agentReviews : List BuildReview
        , agentReviewLoadError : Bool
        , agentReviewPanelExpanded : Bool
        , expandedFindings : Set String
        , showObservations : Maybe Bool
        , agentReviewNotes : Dict String String
        , verdictErrors : Set String
        , expandedDescriptions : Set String
    }


view : String -> String -> Maybe RepositoryChange -> ProjectionState model -> Html Message
view reviewer typeRef repositoryChange model =
    case typeRef of
        "repository-change/v1" ->
            case repositoryChange of
                Just projection ->
                    RepositoryChange.view projection

                Nothing ->
                    projectionPending "repository-change projection"

        "review/v1" ->
            if List.isEmpty model.agentReviews && not model.agentReviewLoadError then
                projectionPending "review projection"

            else
                AgentReview.view reviewer model

        _ ->
            Html.div
                [ class "agent-projection-fallback"
                , style "margin-top" "12px"
                , style "padding" "10px 12px"
                , style "border" "1px solid #3d3c3c"
                , style "color" "#8a8a8a"
                ]
                [ Html.text
                    ("No specialized renderer for "
                        ++ typeRef
                        ++ ". The immutable manifest and content download remain available."
                    )
                ]


projectionPending : String -> Html Message
projectionPending label =
    Html.p
        [ class "agent-projection-loading"
        , style "color" "#8a8a8a"
        , style "font-family" "monospace"
        , style "font-size" "12px"
        ]
        [ Html.text ("loading " ++ label ++ "…") ]
