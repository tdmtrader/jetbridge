module AgentWorkflow.RunList exposing (Config, Row, view)

{-| The chronological run list coupled to the graph.

Row content is deliberately minimal: effective-state glyph, run ID, relative
time, duration, ticket reference when there is one, and an attention cue when
there is one. Cost and outcome live behind selection, because a list that
shows everything competes with the graph instead of supporting it.

Clicking a row navigates straight to the run page. The row is an anchor rather
than a click handler so that middle-click, copy-link, and keyboard activation
all work — there is deliberately no inline preview to intercept the click.

The row never derives an aggregate of its own. `status` is the effective state
the caller resolved; the list renders it as a glyph *and* a word so colour is
only ever reinforcement.

-}

import Duration
import Html exposing (Html)
import Html.Attributes exposing (class, href)
import Time


type alias Row =
    { id : String
    , url : String
    , status : String
    , workflowVersion : Int
    , startedAt : Maybe Time.Posix
    , completedAt : Maybe Time.Posix
    , ticketReference : String
    , attentionCue : String
    }


{-| `emptyMessage` is the caller's because "no runs at all" and "no runs match
this lens" are different facts, and a list that renders one message for both
teaches the reader that the workflow is idle when it is not.
-}
type alias Config =
    { now : Time.Posix
    , emptyMessage : String
    }


view : Config -> List Row -> Html msg
view config rows =
    if List.isEmpty rows then
        Html.div [ class "agent-run-list-empty" ] [ Html.text config.emptyMessage ]

    else
        Html.ul [ class "agent-run-list" ]
            (rows |> withRevisionBoundaries |> List.map (viewRow config))


{-| A revision marker belongs on the row where the revision changes, not on
every row: repeating it would turn a subtle boundary cue into noise. The first
row always carries one, because it opens whatever revision the list starts in
and an unlabelled first block would leave the reader guessing.
-}
withRevisionBoundaries : List Row -> List ( Row, Bool )
withRevisionBoundaries rows =
    let
        step row ( previous, acc ) =
            ( Just row.workflowVersion, ( row, previous /= Just row.workflowVersion ) :: acc )
    in
    rows |> List.foldl step ( Nothing, [] ) |> Tuple.second |> List.reverse


viewRow : Config -> ( Row, Bool ) -> Html msg
viewRow config ( row, isBoundary ) =
    Html.li [ class "agent-run-row" ]
        (List.filterMap identity
            [ if isBoundary then
                Just
                    (Html.span [ class "agent-run-row-revision-boundary" ]
                        [ Html.text ("v" ++ String.fromInt row.workflowVersion) ]
                    )

              else
                Nothing
            , Just
                (Html.a
                    [ class "agent-run-row-link"
                    , href row.url
                    , Html.Attributes.attribute "aria-label" (accessibleName config row)
                    ]
                    (List.filterMap identity
                        [ Just
                            (Html.span [ class "agent-run-row-status" ]
                                [ Html.text (glyphFor row.status ++ " " ++ row.status) ]
                            )
                        , Just
                            (Html.span [ class "agent-run-row-id" ] [ Html.text row.id ])
                        , Just
                            (Html.span [ class "agent-run-row-time" ]
                                [ Html.text (relativeTime config.now row.startedAt) ]
                            )
                        , Just
                            (Html.span [ class "agent-run-row-duration" ]
                                [ Html.text (duration config row) ]
                            )
                        , present "agent-run-row-ticket" row.ticketReference
                        , present "agent-run-row-attention" row.attentionCue
                        ]
                    )
                )
            ]
        )


present : String -> String -> Maybe (Html msg)
present className value =
    if value == "" then
        Nothing

    else
        Just (Html.span [ class className ] [ Html.text value ])


{-| The glyphs repeat the word rather than replacing it, so the list survives
greyscale, a screen reader, and a reader who has never seen this page before.
-}
glyphFor : String -> String
glyphFor status =
    case status of
        "succeeded" ->
            "\u{2713}"

        "failed" ->
            "\u{2715}"

        "errored" ->
            "\u{2715}"

        "aborted" ->
            "\u{2298}"

        "canceling" ->
            "\u{2298}"

        "running" ->
            "\u{25B6}"

        _ ->
            "\u{00B7}"


{-| A run still executing is timed against now, not left blank: "how long has
this been going" is the question an unfinished run raises.
-}
duration : Config -> Row -> String
duration config row =
    case row.startedAt of
        Nothing ->
            "not started"

        Just startedAt ->
            Duration.format
                (Duration.between startedAt (Maybe.withDefault config.now row.completedAt))


relativeTime : Time.Posix -> Maybe Time.Posix -> String
relativeTime now startedAt =
    case startedAt of
        Nothing ->
            "not started"

        Just posix ->
            let
                seconds =
                    Duration.between posix now // 1000
            in
            if seconds < 0 then
                "just now"

            else if seconds < 60 then
                "just now"

            else if seconds < 3600 then
                String.fromInt (seconds // 60) ++ "m ago"

            else if seconds < 86400 then
                String.fromInt (seconds // 3600) ++ "h ago"

            else
                String.fromInt (seconds // 86400) ++ "d ago"


accessibleName : Config -> Row -> String
accessibleName config row =
    [ Just ("run " ++ row.id)
    , Just row.status
    , Just (relativeTime config.now row.startedAt)
    , if row.ticketReference == "" then
        Nothing

      else
        Just row.ticketReference
    , if row.attentionCue == "" then
        Nothing

      else
        Just row.attentionCue
    ]
        |> List.filterMap identity
        |> String.join ", "
