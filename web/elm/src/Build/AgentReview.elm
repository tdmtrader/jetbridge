module Build.AgentReview exposing (view)

import Concourse.AgentReview as AgentReview exposing (BuildReview, Finding)
import Dict exposing (Dict)
import Html exposing (Html)
import Html.Attributes exposing (class, id, placeholder, style, value)
import Html.Events exposing (onClick, onInput)
import Message.Message exposing (Message(..))
import Set exposing (Set)


type alias PanelState a =
    { a
        | agentReviews : List BuildReview
        , agentReviewLoadError : Bool
        , agentReviewPanelExpanded : Bool
        , expandedFindings : Set String
        , showObservations : Bool
        , agentReviewNotes : Dict String String
        , verdictErrors : Set String
    }


view : String -> PanelState a -> Html Message
view reviewer model =
    case model.agentReviews of
        [] ->
            if model.agentReviewLoadError then
                Html.p
                    [ style "margin" "8px 12px", style "color" "#7a7a7a", style "font-size" "12px" ]
                    [ Html.text "Couldn't load agent review." ]

            else
                Html.text ""

        review :: _ ->
            Html.div
                [ id "agent-review-panel"
                , style "margin" "8px"
                , style "border" "1px solid #3d3c3c"
                , style "background" "#1e1d1d"
                ]
                (summaryBar review model.agentReviewPanelExpanded
                    :: (if model.agentReviewPanelExpanded then
                            [ panelBody reviewer review model ]

                        else
                            []
                       )
                )


summaryBar : BuildReview -> Bool -> Html Message
summaryBar review expanded =
    let
        s =
            review.info
    in
    Html.div
        [ class "agent-review-summary"
        , style "display" "flex"
        , style "align-items" "center"
        , style "gap" "12px"
        , style "padding" "8px 12px"
        , style "cursor" "pointer"
        , onClick ToggleAgentReviewPanel
        ]
        [ Html.span [ style "font-weight" "700" ] [ Html.text "agent review" ]
        , scoreBadge s
        , Html.span [ style "color" "#b0b0b0" ]
            [ Html.text
                (String.fromInt s.provenCount
                    ++ " proven · "
                    ++ String.fromInt s.observationCount
                    ++ " observations"
                )
            ]
        , Html.span [ style "margin-left" "auto", style "color" "#7a7a7a" ]
            [ Html.text
                ("evaluated "
                    ++ String.fromInt s.evaluatedCount
                    ++ " of "
                    ++ String.fromInt review.findingCount
                )
            ]
        , Html.span [] [ Html.text (if expanded then "▾" else "▸") ]
        ]


scoreBadge : { a | score : Float, maxScore : Float, pass : Bool } -> Html Message
scoreBadge s =
    Html.span
        [ style "padding" "2px 8px"
        , style "font-weight" "700"
        , style "background" (if s.pass then "#2e4f2e" else "#5c2626")
        , style "color" (if s.pass then "#9fdf9f" else "#f0a0a0")
        ]
        [ Html.text (String.fromFloat s.score ++ " / " ++ String.fromFloat s.maxScore) ]


panelBody : String -> BuildReview -> PanelState a -> Html Message
panelBody reviewer review model =
    Html.div [ style "padding" "8px 12px" ]
        ((review.provenIssues
            |> List.map (findingCard reviewer review True model)
         )
            ++ observationsSection reviewer review model
        )


observationsSection : String -> BuildReview -> PanelState a -> List (Html Message)
observationsSection reviewer review model =
    if List.isEmpty review.observations then
        []

    else
        Html.div
            [ class "agent-review-observations-toggle"
            , style "padding" "8px 0"
            , style "cursor" "pointer"
            , style "color" "#b0b0b0"
            , onClick ToggleAgentReviewObservations
            ]
            [ Html.text
                ("observations ("
                    ++ String.fromInt (List.length review.observations)
                    ++ ") — advisory, no failing test "
                    ++ (if model.showObservations then "▾" else "▸")
                )
            ]
            :: (if model.showObservations then
                    review.observations |> List.map (findingCard reviewer review False model)

                else
                    []
               )


findingCard : String -> BuildReview -> Bool -> PanelState a -> Finding -> Html Message
findingCard reviewer review isProven model finding =
    let
        expanded =
            isProven || Set.member finding.id model.expandedFindings

        recorded =
            Dict.get finding.id review.feedback
    in
    Html.div
        [ class "agent-review-finding"
        , style "border" "1px solid #3d3c3c"
        , style "margin-bottom" "8px"
        , style "padding" "8px 12px"
        ]
        ([ Html.div
            [ style "display" "flex"
            , style "align-items" "center"
            , style "gap" "8px"
            , style "cursor" "pointer"
            , onClick (ToggleAgentReviewFinding finding.id)
            ]
            [ severityBadge finding.severity
            , Html.span [ style "font-weight" "700" ] [ Html.text finding.title ]
            , Html.span
                [ style "margin-left" "auto", style "font-family" "monospace", style "color" "#7a7a7a" ]
                [ Html.text (finding.file ++ ":" ++ String.fromInt finding.line) ]
            ]
         ]
            ++ (if expanded then
                    [ Html.p [ style "color" "#b0b0b0", style "margin" "8px 0" ]
                        [ Html.text finding.description ]
                    ]
                        ++ testEvidence finding
                        ++ [ verdictRow reviewer review finding recorded model ]

                else
                    []
               )
        )


severityBadge : String -> Html Message
severityBadge severity =
    let
        ( bg, fg ) =
            case severity of
                "critical" ->
                    ( "#5c2626", "#f0a0a0" )

                "high" ->
                    ( "#5c2626", "#f0a0a0" )

                "medium" ->
                    ( "#5c4a26", "#f0d0a0" )

                _ ->
                    ( "#3d3c3c", "#b0b0b0" )
    in
    if severity == "" then
        Html.text ""

    else
        Html.span
            [ style "background" bg, style "color" fg, style "padding" "1px 6px", style "font-size" "12px" ]
            [ Html.text severity ]


testEvidence : Finding -> List (Html Message)
testEvidence finding =
    if finding.testOutput == "" then
        []

    else
        [ Html.pre
            [ style "background" "#141313"
            , style "padding" "8px"
            , style "font-size" "12px"
            , style "overflow-x" "auto"
            ]
            [ Html.text finding.testOutput ]
        ]


verdictRow :
    String
    -> BuildReview
    -> Finding
    -> Maybe AgentReview.FindingFeedback
    -> PanelState a
    -> Html Message
verdictRow reviewer review finding recorded model =
    Html.div []
        [ Html.div
            [ class "agent-review-verdicts"
            , style "display" "flex"
            , style "align-items" "center"
            , style "gap" "0"
            , style "margin-top" "8px"
            , style "border" "1px solid #555"
            , style "width" "fit-content"
            ]
            (AgentReview.allVerdicts
                |> List.map
                    (\verdict ->
                        let
                            selected =
                                recorded |> Maybe.map (.verdict >> (==) verdict) |> Maybe.withDefault False
                        in
                        Html.span
                            [ style "padding" "4px 10px"
                            , style "font-size" "12px"
                            , style "cursor" "pointer"
                            , style "border-right" "1px solid #555"
                            , style "background" (if selected then "#e0e0e0" else "transparent")
                            , style "color" (if selected then "#141313" else "#b0b0b0")
                            , onClick
                                (AgentReviewVerdictClicked
                                    { repo = review.info.repo
                                    , commitSha = review.info.commitSha
                                    , findingId = finding.id
                                    , verdict = verdict
                                    , reviewer = reviewer
                                    }
                                )
                            ]
                            [ Html.text (AgentReview.verdictLabel verdict) ]
                    )
            )
        , Html.input
            [ placeholder "Add a note about this verdict"
            , value (Dict.get finding.id model.agentReviewNotes |> Maybe.withDefault "")
            , onInput (AgentReviewNoteChanged finding.id)
            , style "width" "100%"
            , style "margin-top" "6px"
            , style "background" "#141313"
            , style "color" "#e0e0e0"
            , style "border" "1px solid #3d3c3c"
            , style "padding" "4px 8px"
            ]
            []
        , if Set.member finding.id model.verdictErrors then
            Html.p [ style "color" "#f0a0a0", style "font-size" "12px", style "margin" "4px 0 0" ]
                [ Html.text "Couldn't save verdict. Click a verdict to retry." ]

          else
            Html.text ""
        ]
