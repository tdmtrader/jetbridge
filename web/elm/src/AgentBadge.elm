module AgentBadge exposing
    ( Status(..)
    , Tone(..)
    , fromApiToken
    , fromRunStatus
    , label
    , tone
    , view
    )

import Html exposing (Html)
import Html.Attributes exposing (class)


type Status
    = Draft
    | Queued
    | Running (Maybe String)
    | AwaitingHuman
    | NeedsReview
    | Merged
    | MergedWithFixes
    | SentBack
    | Concluded
    | Abandoned
    | Failed
    | Errored
    | Succeeded


type Tone
    = Neutral
    | Info
    | Active
    | Attention
    | Good
    | GoodMuted
    | Warn
    | Calm
    | Bad
    | Error


label : Status -> String
label status =
    case status of
        Draft ->
            "Draft"

        Queued ->
            "Queued"

        Running (Just s) ->
            "Running · " ++ s

        Running Nothing ->
            "Running"

        AwaitingHuman ->
            "Waiting on you"

        NeedsReview ->
            "Needs your review"

        Merged ->
            "Merged"

        MergedWithFixes ->
            "Merged with fixes"

        SentBack ->
            "Sent back"

        Concluded ->
            "Concluded"

        Abandoned ->
            "Abandoned"

        Failed ->
            "Failed"

        Errored ->
            "Errored"

        Succeeded ->
            "OK"


tone : Status -> Tone
tone status =
    case status of
        Draft ->
            Neutral

        Queued ->
            Info

        Running _ ->
            Active

        AwaitingHuman ->
            Attention

        NeedsReview ->
            Attention

        Merged ->
            Good

        MergedWithFixes ->
            GoodMuted

        SentBack ->
            Warn

        Concluded ->
            Calm

        Abandoned ->
            Neutral

        Failed ->
            Bad

        Errored ->
            Error

        Succeeded ->
            Good


fromApiToken : String -> Maybe Status
fromApiToken token =
    case token of
        "draft" ->
            Just Draft

        "queued" ->
            Just Queued

        "running" ->
            Just (Running Nothing)

        "awaiting_human" ->
            Just AwaitingHuman

        "needs_review" ->
            Just NeedsReview

        "merged" ->
            Just Merged

        "merged_with_fixes" ->
            Just MergedWithFixes

        "sent_back" ->
            Just SentBack

        "concluded" ->
            Just Concluded

        "abandoned" ->
            Just Abandoned

        "failed" ->
            Just Failed

        "errored" ->
            Just Errored

        _ ->
            Nothing


{-| fromRunStatus maps an agent_run_metrics status ("ok"/"failed"/"parked"/
"error") to a badge. A parked run is awaiting a human, so it reuses that tone.
-}
fromRunStatus : String -> Maybe Status
fromRunStatus status =
    case status of
        "ok" ->
            Just Succeeded

        "failed" ->
            Just Failed

        "parked" ->
            Just AwaitingHuman

        "error" ->
            Just Errored

        _ ->
            Nothing


toneClass : Tone -> String
toneClass t =
    case t of
        Neutral ->
            "agent-badge--neutral"

        Info ->
            "agent-badge--info"

        Active ->
            "agent-badge--active"

        Attention ->
            "agent-badge--attention"

        Good ->
            "agent-badge--good"

        GoodMuted ->
            "agent-badge--good-muted"

        Warn ->
            "agent-badge--warn"

        Calm ->
            "agent-badge--calm"

        Bad ->
            "agent-badge--bad"

        Error ->
            "agent-badge--error"


pulses : Status -> Bool
pulses status =
    case status of
        Running _ ->
            True

        AwaitingHuman ->
            True

        _ ->
            False


view : Status -> Html msg
view status =
    let
        classes =
            "agent-badge"
                :: toneClass (tone status)
                :: (if pulses status then
                        [ "agent-badge--pulse" ]

                    else
                        []
                   )
    in
    Html.span
        (List.map class classes)
        [ Html.span [ class "agent-badge__dot" ] []
        , Html.text (label status)
        ]
