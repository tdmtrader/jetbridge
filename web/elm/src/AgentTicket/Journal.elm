module AgentTicket.Journal exposing (Config, Entry, view)

{-| One ticket's cross-workflow history, as a chronological list.

This is deliberately NOT a graph. There is no composed cross-workflow DAG, no
edge element, and no invented causality: entries are ordered by the durable
occurrence time the server sent, and nothing here computes a rank, a parent, or
a path. A ticket that drove `small-fix`, then `pr-create`, then `small-fix`
again reads as three entries, because that is three things that happened.

`retryOf` is offered so a retry can be GROUPED with the run it retried — it is
the run's own durable retry identity, rendered as a label, and no layout
depends on it. Snapshot lineage and downstream-run relationships may enrich an
entry later; they are never required to construct or lay out the journal.

Completed detail collapses to a single line. Anything a human still has to
resolve carries the `--outstanding` modifier and says, in words, what is
outstanding — colour is only ever reinforcement.

-}

import Duration
import Html exposing (Html)
import Html.Attributes exposing (class, href)
import Time


{-| One run occurrence.

`id` and `retryOf` are STRINGS: durable run IDs exceed exact JS number range,
so they ride as quoted decimals from the API and are never parsed to a number
on the way to the screen.

-}
type alias Entry =
    { id : String
    , url : String
    , workflowName : String
    , workflowVersion : Int
    , status : String
    , createdAt : Maybe Time.Posix
    , startedAt : Maybe Time.Posix
    , completedAt : Maybe Time.Posix
    , retryOf : Maybe String
    , outcome : String

    {- Empty when nothing is outstanding. Non-empty is both the elevation
       signal and the words shown, so the two can never disagree.
    -}
    , outstandingAction : String
    }


type alias Config =
    { now : Time.Posix
    , emptyMessage : String
    }


view : Config -> List Entry -> Html msg
view config entries =
    if List.isEmpty entries then
        Html.div [ class "agent-journal-empty" ] [ Html.text config.emptyMessage ]

    else
        Html.ol [ class "agent-journal" ] (List.map (viewEntry config) entries)


viewEntry : Config -> Entry -> Html msg
viewEntry config entry =
    Html.li [ class (entryClasses entry) ]
        [ Html.a
            [ class "agent-journal-entry-link"
            , href entry.url
            , Html.Attributes.attribute "aria-label" (accessibleName config entry)
            ]
            (List.filterMap identity
                [ Just
                    (Html.span [ class "agent-journal-entry-status" ]
                        [ Html.text (glyphFor entry.status ++ " " ++ entry.status) ]
                    )
                , Just
                    (Html.span [ class "agent-journal-entry-workflow" ]
                        [ Html.text entry.workflowName ]
                    )
                , Just
                    (Html.span [ class "agent-journal-entry-revision" ]
                        [ Html.text ("v" ++ String.fromInt entry.workflowVersion) ]
                    )
                , Just (Html.span [ class "agent-journal-entry-id" ] [ Html.text entry.id ])
                , Just
                    (Html.span [ class "agent-journal-entry-time" ]
                        [ Html.text (relativeTime config.now entry.createdAt) ]
                    )
                , Just
                    (Html.span [ class "agent-journal-entry-duration" ]
                        [ Html.text (duration config entry) ]
                    )
                , entry.retryOf
                    |> Maybe.map
                        (\source ->
                            Html.span [ class "agent-journal-entry-retry" ]
                                [ Html.text ("retry of " ++ source) ]
                        )
                , present "agent-journal-entry-outcome" entry.outcome
                , present "agent-journal-entry-outstanding" entry.outstandingAction
                ]
            )
        ]


{-| Modifiers ride on the list item so a reader can find "what still needs me"
and "which of these is a retry" without opening anything.
-}
entryClasses : Entry -> String
entryClasses entry =
    String.join " "
        (List.filterMap identity
            [ Just "agent-journal-entry"
            , if entry.outstandingAction == "" then
                Nothing

              else
                Just "agent-journal-entry--outstanding"
            , entry.retryOf |> Maybe.map (always "agent-journal-entry--retry")
            ]
        )


present : String -> String -> Maybe (Html msg)
present className value =
    if value == "" then
        Nothing

    else
        Just (Html.span [ class className ] [ Html.text value ])


{-| The glyph repeats the word rather than replacing it, so the journal
survives greyscale and a screen reader.
-}
glyphFor : String -> String
glyphFor status =
    case status of
        "succeeded" ->
            "✓"

        "failed" ->
            "✕"

        "errored" ->
            "✕"

        "aborted" ->
            "⊘"

        "canceling" ->
            "⊘"

        "running" ->
            "▶"

        _ ->
            "·"


duration : Config -> Entry -> String
duration config entry =
    case entry.startedAt of
        Nothing ->
            "not started"

        Just startedAt ->
            -- Clamped because `now` is unknown until the first clock tick, and
            -- a negative elapsed time is worse than a momentary zero.
            Duration.format
                (max 0
                    (Duration.between startedAt (Maybe.withDefault config.now entry.completedAt))
                )


relativeTime : Time.Posix -> Maybe Time.Posix -> String
relativeTime now moment =
    case moment of
        Nothing ->
            "not yet"

        Just at ->
            let
                seconds =
                    Duration.between at now // 1000
            in
            if seconds < 3600 then
                String.fromInt (max 0 seconds // 60) ++ "m ago"

            else if seconds < 86400 then
                String.fromInt (seconds // 3600) ++ "h ago"

            else
                String.fromInt (seconds // 86400) ++ "d ago"


accessibleName : Config -> Entry -> String
accessibleName config entry =
    String.join ", "
        (List.filter (\part -> part /= "")
            [ entry.workflowName ++ " v" ++ String.fromInt entry.workflowVersion
            , "run " ++ entry.id
            , entry.status
            , relativeTime config.now entry.createdAt
            , entry.retryOf |> Maybe.map (\source -> "retry of " ++ source) |> Maybe.withDefault ""
            , entry.outstandingAction
            ]
        )
