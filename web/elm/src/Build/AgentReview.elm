module Build.AgentReview exposing (view)

import Concourse.AgentReview as AgentReview exposing (BuildReview, Finding)
import Dict exposing (Dict)
import Html exposing (Html)
import Html.Attributes exposing (attribute, class, href, id, placeholder, rel, style, target, type_, value)
import Html.Events exposing (onClick, onInput)
import Message.Message exposing (Message(..))
import Set exposing (Set)


type alias PanelState a =
    { a
        | agentReviews : List BuildReview
        , agentReviewLoadError : Bool
        , agentReviewPanelExpanded : Bool
        , expandedFindings : Set String
        , showObservations : Maybe Bool
        , agentReviewNotes : Dict String String
        , verdictErrors : Set String
        , expandedDescriptions : Set String
    }


{-| Reset a `<button>`'s user-agent chrome so it can carry arbitrary layout
while staying a real, keyboard-focusable, screen-reader-announced control.
Deliberately does NOT set `outline: none` — the focus ring must remain.
-}
buttonReset : List (Html.Attribute Message)
buttonReset =
    [ type_ "button"
    , style "background" "transparent"
    , style "border" "none"
    , style "margin" "0"
    , style "font-family" "inherit"
    , style "font-size" "inherit"
    , style "color" "inherit"
    , style "text-align" "left"
    ]


boolAttr : Bool -> String
boolAttr b =
    if b then
        "true"

    else
        "false"


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
    Html.button
        (buttonReset
            ++ [ class "agent-review-summary"
               , attribute "aria-expanded" (boolAttr expanded)
               , style "display" "flex"
               , style "align-items" "center"
               , style "gap" "12px"
               , style "padding" "8px 12px"
               , style "width" "100%"
               , style "cursor" "pointer"
               , onClick ToggleAgentReviewPanel
               ]
        )
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
                ("your feedback: "
                    ++ String.fromInt s.evaluatedCount
                    ++ " of "
                    ++ String.fromInt review.findingCount
                    ++ " verdicts"
                )
            ]
        , Html.span []
            [ Html.text
                (if expanded then
                    "▾"

                 else
                    "▸"
                )
            ]
        ]


scoreBadge : { a | score : Float, maxScore : Float, pass : Bool } -> Html Message
scoreBadge s =
    Html.span
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
    let
        count =
            List.length review.observations
    in
    if count == 0 then
        []

    else
        let
            -- A short list is worth reading, so it opens by default; a long
            -- one stays folded. A user click records an ABSOLUTE choice
            -- (Just open/closed) rather than "flip the default": reviews
            -- refetch while the page is open, and a count crossing the
            -- threshold must not invert what the user chose.
            open =
                model.showObservations |> Maybe.withDefault (count <= 5)
        in
        Html.button
            (buttonReset
                ++ [ class "agent-review-observations-toggle"
                   , attribute "aria-expanded" (boolAttr open)
                   , style "display" "flex"
                   , style "align-items" "center"
                   , style "gap" "8px"
                   , style "padding" "8px 0"
                   , style "width" "100%"
                   , style "cursor" "pointer"
                   , style "color" "#b0b0b0"
                   , onClick (ToggleAgentReviewObservations (not open))
                   ]
            )
            [ Html.span [ style "font-size" "12px" ]
                [ Html.text
                    (if open then
                        "▾"

                     else
                        "▸"
                    )
                ]
            , Html.span [ style "font-weight" "700" ]
                [ Html.text
                    (String.fromInt count
                        ++ (if count == 1 then
                                " observation"

                            else
                                " observations"
                           )
                    )
                ]
            , Html.span [ style "color" "#7a7a7a", style "font-size" "12px" ]
                [ Html.text "advisory — no failing test" ]
            ]
            :: (if open then
                    review.observations |> List.map (findingCard reviewer review False model)

                else
                    []
               )


findingCard : String -> BuildReview -> Bool -> PanelState a -> Finding -> Html Message
findingCard reviewer review isProven model finding =
    let
        -- A blank id can't key interactive state without colliding with every
        -- other blank-id finding, so such findings are rendered read-only: no
        -- expand toggle, no note box, no verdict buttons. `findingAnchor`
        -- already guards the deep-link anchor on the same condition.
        interactive =
            finding.id /= ""

        -- Non-interactive findings have no toggle to reveal their body, so show
        -- it read-only rather than hiding it. Proven issues are always open.
        expanded =
            isProven || not interactive || Set.member finding.id model.expandedFindings

        recorded =
            Dict.get finding.id review.feedback
    in
    Html.div
        (findingAnchor finding
            ++ [ class "agent-review-finding"
               , style "border" "1px solid #3d3c3c"
               , style "margin-bottom" "8px"
               , style "padding" "8px 12px"
               ]
        )
        ([ Html.div
            [ style "display" "flex"
            , style "align-items" "center"
            , style "gap" "8px"
            ]
            [ findingHeader interactive expanded finding
            , fileRef review.info finding
            ]
         ]
            ++ (if expanded then
                    descriptionBlock interactive model finding
                        ++ testEvidence finding
                        ++ (if interactive then
                                [ verdictRow reviewer review finding recorded model ]

                            else
                                []
                           )

                else
                    []
               )
        )


{-| The finding's severity + title. When the finding is interactive it is the
expand/collapse toggle button; a blank-id finding renders the same content as
inert text so no interactive state is keyed on "".
-}
findingHeader : Bool -> Bool -> Finding -> Html Message
findingHeader interactive expanded finding =
    let
        content =
            [ severityBadge finding.severity
            , Html.span [ style "font-weight" "700" ] [ Html.text finding.title ]
            ]
    in
    if interactive then
        Html.button
            (buttonReset
                ++ [ class "agent-review-finding-toggle"
                   , attribute "aria-expanded" (boolAttr expanded)
                   , style "display" "flex"
                   , style "align-items" "center"
                   , style "gap" "8px"
                   , style "flex" "1"
                   , style "cursor" "pointer"
                   , onClick (ToggleAgentReviewFinding finding.id)
                   ]
            )
            content

    else
        Html.div
            [ class "agent-review-finding-title"
            , style "display" "flex"
            , style "align-items" "center"
            , style "gap" "8px"
            , style "flex" "1"
            ]
            content


{-| A stable anchor so a review comment can deep-link straight to one finding.
Guarded on a non-empty id — a blank id would collide across findings.
-}
findingAnchor : Finding -> List (Html.Attribute Message)
findingAnchor finding =
    if finding.id == "" then
        []

    else
        [ id ("agent-review-finding-" ++ finding.id) ]


{-| The <file:line> reference, rendered as a real link to the blob at the reviewed
sha when the repo URL is usable, otherwise as plain monospace text. Sibling of —
never nested inside — the header toggle button, so the two are independent
controls for assistive tech.
-}
fileRef : AgentReview.Summary -> Finding -> Html Message
fileRef info finding =
    if finding.file == "" then
        Html.text ""

    else
        let
            label =
                finding.file ++ ":" ++ String.fromInt finding.line
        in
        case AgentReview.repoBlobUrl info.repo info.commitSha finding.file finding.line of
            Just url ->
                Html.a
                    [ href url
                    , target "_blank"
                    , rel "noopener noreferrer"
                    , style "margin-left" "auto"
                    , style "font-family" "monospace"
                    , style "color" "#7a9ac0"
                    , style "text-decoration" "none"
                    ]
                    [ Html.text label ]

            Nothing ->
                Html.span
                    [ style "margin-left" "auto"
                    , style "font-family" "monospace"
                    , style "color" "#7a7a7a"
                    ]
                    [ Html.text label ]


{-| The finding description, line-clamped when long with a show-more/less toggle
so a wall of agent prose never dominates the panel. A blank-id finding is
non-interactive — its clamp state can't be keyed without colliding — so it is
shown in full with no toggle.
-}
descriptionBlock : Bool -> PanelState a -> Finding -> List (Html Message)
descriptionBlock interactive model finding =
    if finding.description == "" then
        []

    else
        let
            expanded =
                not interactive || Set.member finding.id model.expandedDescriptions
        in
        Html.p
            (style "color" "#b0b0b0" :: style "margin" "8px 0" :: clampStyles expanded)
            [ Html.text finding.description ]
            :: (if interactive && isLong finding.description then
                    [ Html.button
                        (buttonReset
                            ++ [ class "agent-review-description-toggle"
                               , attribute "aria-expanded" (boolAttr expanded)
                               , style "color" "#7a7a7a"
                               , style "font-size" "12px"
                               , style "cursor" "pointer"
                               , onClick (ToggleAgentReviewFindingBody finding.id)
                               ]
                        )
                        [ Html.text
                            (if expanded then
                                "show less"

                             else
                                "show more"
                            )
                        ]
                    ]

                else
                    []
               )


isLong : String -> Bool
isLong s =
    String.length s > 220


clampStyles : Bool -> List (Html.Attribute Message)
clampStyles expanded =
    if expanded then
        []

    else
        [ style "display" "-webkit-box"
        , style "-webkit-line-clamp" "4"
        , style "-webkit-box-orient" "vertical"
        , style "overflow" "hidden"
        ]


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
                        Html.button
                            (buttonReset
                                ++ [ attribute "aria-pressed" (boolAttr selected)
                                   , style "padding" "4px 10px"
                                   , style "font-size" "12px"
                                   , style "cursor" "pointer"
                                   , style "border-right" "1px solid #555"
                                   , style "background"
                                        (if selected then
                                            "#e0e0e0"

                                         else
                                            "transparent"
                                        )
                                   , style "color"
                                        (if selected then
                                            "#141313"

                                         else
                                            "#b0b0b0"
                                        )
                                   , onClick
                                        (AgentReviewVerdictClicked
                                            { reviewSnapshotId = review.info.snapshotId
                                            , repo = review.info.repo
                                            , commitSha = review.info.commitSha
                                            , findingId = finding.id
                                            , verdict = verdict
                                            , reviewer = reviewer
                                            }
                                        )
                                   ]
                            )
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
