module AgentWorkflowRun.NodeDetail exposing
    ( Attempt
    , Binding
    , Detail
    , Output
    , Publication
    , Wait
    , view
    )

{-| Durable evidence for the node selected on a run's DAG.

This is a **relocation**, not a rewrite. The run page used to be a stack of
flat cards that each described the whole run — bindings, waits, transcripts,
metrics — leaving the reader to work out which step any of it belonged to. The
cards render what they always rendered; they now render under the node they are
about.

Two things are deliberately NOT here, because they are genuinely run-level and
would be a lie if attributed to a node: output dispositions (outcomes) and the
run's overall invocation telemetry. Those stay below the graph.

The type is a plain record rather than the page's model. That keeps this module
testable on its own and, more importantly, keeps it from acquiring a second
copy of the graph: the caller already has the run's occurrences and hands over
the slice that belongs to one node.

`Html msg` slots carry the two projections whose markup lives elsewhere — the
repository-change projection and the transcript disclosure — so relocating them
cannot accidentally change what they draw.

-}

import Html exposing (Html)
import Html.Attributes exposing (class, href, style)
import Routes


type alias Attempt =
    { attempt : Int

    -- retryAttempt is which copy of an authored retry closure this is;
    -- attempt is which recovery attempt of that copy. They come from different
    -- records and answer different questions, so they are reported separately
    -- here for the same reason the projection stores them separately.
    , retryAttempt : Int
    , status : String
    , startedAt : Maybe String
    , completedAt : Maybe String
    , durationSeconds : Int
    , costUsd : Float
    }


type alias Binding =
    { portName : String
    , snapshotId : String
    , typeRef : String
    }


type alias Output msg =
    { portName : String
    , snapshotId : String
    , typeRef : String
    , contentState : String
    , projection : Maybe (Html msg)
    }


type alias Wait msg =
    { questionName : String
    , prompt : String
    , context : String
    , status : String
    , expectedType : String
    , deadline : String
    , answerSnapshotId : Maybe String
    , resolvedByDisplayName : String

    -- resolution carries the answer controls for an unresolved wait, moved
    -- across unchanged so the interaction is identical to the flat card's.
    , resolution : Maybe (Html msg)
    }


type alias Publication =
    { status : String
    , reference : String
    }


type alias Detail msg =
    { nodeId : String
    , kind : String
    , displayName : String
    , optional : Bool
    , decorations : List String
    , attempts : List Attempt
    , inputs : List Binding
    , outputs : List (Output msg)
    , waits : List (Wait msg)
    , publication : Maybe Publication
    , transcripts : List (Html msg)
    , turns : Int
    , tokens : Int
    }


{-| An unselected canvas gets an invitation, not a blank pane. A reader who
sees an empty region below a graph concludes the run has no detail.
-}
view : Maybe (Detail msg) -> Html msg
view selected =
    case selected of
        Nothing ->
            Html.section [ class "agent-node-detail", cardStyle ]
                [ Html.div [ class "agent-node-detail-empty" ]
                    [ Html.p [ style "color" "#b5b5b5" ]
                        [ Html.text "Select a node on the graph to see what it did in this run." ]
                    ]
                ]

        Just detail ->
            Html.section [ class "agent-node-detail", cardStyle ]
                (identity_ detail
                    :: attempts detail
                    :: bindings detail
                    :: waits detail
                    ++ [ publication detail
                       , transcripts detail
                       , telemetry detail
                       ]
                )


identity_ : Detail msg -> Html msg
identity_ detail =
    Html.div [ class "agent-node-detail-identity" ]
        [ Html.h2 [ style "margin" "0 0 4px" ] [ Html.text detail.nodeId ]
        , Html.p
            [ style "margin" "0 0 12px", style "color" "#b5b5b5", style "font-size" "12px" ]
            [ Html.text
                (detail.kind
                    ++ (if detail.displayName == "" || detail.displayName == detail.nodeId then
                            ""

                        else
                            " · " ++ detail.displayName
                       )
                    ++ (if detail.optional then
                            " · optional"

                        else
                            ""
                       )
                    ++ (if List.isEmpty detail.decorations then
                            ""

                        else
                            " · " ++ String.join ", " detail.decorations
                       )
                )
            ]
        ]


{-| Every attempt, in order, with its own effective state.

A node that failed and was recovered has two attempts and both are true; the
run page's job is to show what happened, not to summarize it away.
-}
attempts : Detail msg -> Html msg
attempts detail =
    Html.div [ class "agent-node-detail-attempts" ]
        [ heading "Attempts"
        , if List.isEmpty detail.attempts then
            note "this node was never reached in this run"

          else
            Html.div [] (List.map attemptRow detail.attempts)
        , Html.p
            [ class "agent-node-detail-cost", style "font-family" "monospace", style "margin" "8px 0 0" ]
            [ Html.text
                (money (List.sum (List.map .costUsd detail.attempts))
                    ++ " · "
                    ++ duration (List.sum (List.map .durationSeconds detail.attempts))
                )
            ]
        ]


attemptRow : Attempt -> Html msg
attemptRow item =
    Html.div
        [ class "agent-node-detail-attempt"
        , style "padding" "6px 0"
        , style "border-top" "1px solid #302f2f"
        ]
        [ Html.strong [] [ Html.text ("attempt " ++ String.fromInt item.attempt) ]
        , Html.text
            ((if item.retryAttempt > 1 then
                " · retry copy " ++ String.fromInt item.retryAttempt

              else
                ""
             )
                ++ " · "
                ++ item.status
                ++ " · "
                ++ duration item.durationSeconds
                ++ " · "
                ++ money item.costUsd
            )
        , case ( item.startedAt, item.completedAt ) of
            ( Just startedAt, Just completedAt ) ->
                Html.p
                    [ style "margin" "2px 0 0", style "color" "#8a8a8a", style "font-size" "11px" ]
                    [ Html.text (startedAt ++ " → " ++ completedAt) ]

            ( Just startedAt, Nothing ) ->
                Html.p
                    [ style "margin" "2px 0 0", style "color" "#8a8a8a", style "font-size" "11px" ]
                    [ Html.text ("started " ++ startedAt) ]

            _ ->
                Html.text ""
        ]


bindings : Detail msg -> Html msg
bindings detail =
    Html.div
        [ style "display" "grid", style "grid-template-columns" "1fr 1fr", style "gap" "20px" ]
        [ Html.div [ class "agent-node-detail-inputs" ]
            [ heading "Inputs"
            , if List.isEmpty detail.inputs then
                note "no inputs"

              else
                Html.ul [] (List.map inputRow detail.inputs)
            ]
        , Html.div [ class "agent-node-detail-outputs" ]
            [ heading "Sealed outputs"
            , if List.isEmpty detail.outputs then
                note "outputs have not materialized"

              else
                Html.div [] (List.map outputRow detail.outputs)
            ]
        ]


inputRow : Binding -> Html msg
inputRow binding =
    Html.li [] [ Html.text (binding.portName ++ " ← "), snapshotRef binding ]


outputRow : Output msg -> Html msg
outputRow output =
    Html.div [ class "agent-run-output", style "margin-bottom" "12px" ]
        [ Html.div []
            [ Html.text (output.portName ++ " → ")
            , snapshotRef { snapshotId = output.snapshotId, typeRef = output.typeRef }
            , Html.span [ style "color" "#8a8a8a" ] [ Html.text (" · " ++ output.contentState) ]
            ]
        , Maybe.withDefault (Html.text "") output.projection
        ]


snapshotRef : { a | snapshotId : String, typeRef : String } -> Html msg
snapshotRef ref =
    Html.a
        [ class "agent-snapshot-link"
        , href (Routes.toString (Routes.AgentSnapshot { id = ref.snapshotId }))
        ]
        [ Html.text (ref.typeRef ++ " #" ++ ref.snapshotId) ]


{-| The human question, its answer, and who answered it.

The resolution audit is the point: a wait whose answer has no attributable
human is indistinguishable from one the system answered for itself, and those
are very different facts.
-}
waits : Detail msg -> List (Html msg)
waits detail =
    List.map waitRow detail.waits


waitRow : Wait msg -> Html msg
waitRow wait =
    Html.div [ class "agent-node-detail-wait", style "padding" "10px 0" ]
        [ heading "Human wait"
        , Html.div []
            [ Html.strong [] [ Html.text wait.questionName ]
            , Html.text
                (" · "
                    ++ wait.status
                    ++ " · expects "
                    ++ wait.expectedType
                    ++ " · deadline "
                    ++ wait.deadline
                )
            ]
        , Html.p [ class "agent-run-wait-prompt", style "margin" "8px 0 4px" ]
            [ Html.text wait.prompt ]
        , Html.p
            [ class "agent-run-wait-context", style "margin" "0 0 8px", style "color" "#b5b5b5" ]
            [ Html.text wait.context ]
        , case wait.answerSnapshotId of
            Just snapshotId ->
                Html.div []
                    [ Html.text "answer: "
                    , snapshotRef { snapshotId = snapshotId, typeRef = "answer" }
                    , if wait.resolvedByDisplayName == "" then
                        Html.text " · resolved without an attributable human"

                      else
                        Html.text (" · resolved by " ++ wait.resolvedByDisplayName)
                    ]

            Nothing ->
                Html.text ""
        , Maybe.withDefault (Html.text "") wait.resolution
        ]


publication : Detail msg -> Html msg
publication detail =
    case detail.publication of
        Nothing ->
            Html.text ""

        Just result ->
            Html.div [ class "agent-node-detail-publication", style "padding" "10px 0" ]
                [ heading "Publish result"
                , Html.p [ style "margin" "0" ]
                    [ Html.text
                        (result.status
                            ++ (if result.reference == "" then
                                    ""

                                else
                                    " · " ++ result.reference
                               )
                        )
                    ]
                ]


transcripts : Detail msg -> Html msg
transcripts detail =
    if List.isEmpty detail.transcripts then
        Html.text ""

    else
        Html.div [ class "agent-node-detail-transcripts", style "padding" "10px 0" ]
            (heading "Transcript" :: detail.transcripts)


telemetry : Detail msg -> Html msg
telemetry detail =
    if detail.turns == 0 && detail.tokens == 0 then
        Html.text ""

    else
        Html.p
            [ class "agent-node-detail-telemetry"
            , style "font-family" "monospace"
            , style "margin" "8px 0 0"
            ]
            [ Html.text
                (String.fromInt detail.turns
                    ++ " turns · "
                    ++ String.fromInt detail.tokens
                    ++ " tokens"
                )
            ]


{-| Two fixed decimal places. `String.fromFloat` renders 1 as "1", which reads
as a whole dollar amount that was never stated.
-}
money : Float -> String
money value =
    let
        cents =
            round (value * 100)

        sign =
            if cents < 0 then
                "-"

            else
                ""

        magnitude =
            abs cents
    in
    sign
        ++ "$"
        ++ String.fromInt (magnitude // 100)
        ++ "."
        ++ String.padLeft 2 '0' (String.fromInt (modBy 100 magnitude))


duration : Int -> String
duration seconds =
    if seconds < 60 then
        String.fromInt seconds ++ "s"

    else
        String.fromInt (seconds // 60) ++ "m " ++ String.fromInt (modBy 60 seconds) ++ "s"


heading : String -> Html msg
heading text =
    Html.h3 [ style "margin" "0 0 6px", style "font-size" "13px" ] [ Html.text text ]


note : String -> Html msg
note text =
    Html.p [ style "color" "#8a8a8a", style "margin" "0" ] [ Html.text text ]


cardStyle : Html.Attribute msg
cardStyle =
    style "margin-bottom" "20px"
