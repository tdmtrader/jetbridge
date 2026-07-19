module AgentBadge exposing
    ( Status(..)
    , Tone(..)
    , description
    , fromApiToken
    , fromRunStatus
    , label
    , runOutcome
    , tone
    , view
    )

import Html exposing (Html)
import Html.Attributes exposing (class, title)


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
    | Aborted
    | Succeeded
    | NoOutput


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

        Aborted ->
            "Aborted"

        Succeeded ->
            "OK"

        NoOutput ->
            "No output"


{-| A one-line, plain-English gloss for each status, surfaced as the badge's
hover `title` so the terminal states (Merged / Concluded / Abandoned, …) carry
their meaning in the UI instead of only in docs.
-}
description : Status -> String
description status =
    case status of
        Draft ->
            "Not queued yet — still being drafted"

        Queued ->
            "Waiting for an agent to pick it up"

        Running _ ->
            "An agent is working on it now"

        AwaitingHuman ->
            "Paused — waiting on your input"

        NeedsReview ->
            "Work is ready for your review"

        Merged ->
            "Branch merged to the target branch"

        MergedWithFixes ->
            "Merged after manual fixes on top"

        SentBack ->
            "Returned to the agent for changes"

        Concluded ->
            "Closed without merging (e.g. analysis-only)"

        Abandoned ->
            "Dropped without delivery"

        Failed ->
            "The run failed"

        Errored ->
            "The run hit an unexpected error"

        Aborted ->
            "The run was aborted"

        Succeeded ->
            "Completed successfully"

        NoOutput ->
            "Finished but produced no result"


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

        Aborted ->
            Neutral

        Succeeded ->
            Good

        NoOutput ->
            Warn


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


{-| runOutcome derives the DISPLAY truth for a finished run. The pipeline
build status (server-derived) wins over the agent step status (U3): a step
that exited "ok" inside a build that failed is Failed, and a step that exited
"ok" in a build that succeeded but produced no result summary is NoOutput —
never a green OK on a build that did not deliver. Falls back to the step
status when the build status is absent (e.g. a metric that predates the final
build state, or a still-running build).
-}
runOutcome : { buildStatus : String, runStatus : String, hasResult : Bool } -> Maybe Status
runOutcome { buildStatus, runStatus, hasResult } =
    -- A parked run is blocked awaiting a human (HITL checkpoint). Its build
    -- deliberately stays "started" until the run continues, so parked must be
    -- checked BEFORE the build status — otherwise "started" would render it as
    -- Running and hide that it needs the operator's input.
    if runStatus == "parked" then
        Just AwaitingHuman

    else
        case buildStatus of
            "failed" ->
                Just Failed

            "errored" ->
                Just Errored

            "aborted" ->
                Just Aborted

            "succeeded" ->
                if hasResult then
                    Just Succeeded

                else
                    Just NoOutput

            "started" ->
                Just (Running Nothing)

            "pending" ->
                Just (Running Nothing)

            _ ->
                fromRunStatus runStatus


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
        (title (description status) :: List.map class classes)
        [ Html.span [ class "agent-badge__dot" ] []
        , Html.text (label status)
        ]
