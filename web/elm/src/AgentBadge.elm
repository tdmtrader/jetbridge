module AgentBadge exposing
    ( Status(..)
    , Tone(..)
    , description
    , displayOutcome
    , fromApiToken
    , fromOutcomeToken
    , fromRunStatus
    , label
    , runOutcome
    , tone
    , toneColor
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
    | Closed
    | Failed
    | Errored
    | Aborted
    | Succeeded
    | NoOutput
    | Unrecorded


type Tone
    = Neutral
    | Info
    | Active
    | Attention
    | Good
    | Warn
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

        Closed ->
            "Closed"

        Failed ->
            "Failed"

        Errored ->
            "Errored"

        Aborted ->
            "Aborted"

        Succeeded ->
            "succeeded"

        NoOutput ->
            "No output"

        Unrecorded ->
            "unrecorded"


{-| A one-line, plain-English gloss for each status, surfaced as the badge's
hover `title` so the terminal state carries its meaning in the UI instead of
only in docs.
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
            "The run finished — it is waiting on your decision"

        Closed ->
            "You are done with this work item — how the run turned out is on the run"

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

        Unrecorded ->
            "Delivered, but the runner wrote no flight recording"


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

        Closed ->
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

        Unrecorded ->
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

        "closed" ->
            Just Closed

        "failed" ->
            Just Failed

        "errored" ->
            Just Errored

        _ ->
            Nothing


{-| fromRunStatus maps an agent\_run\_metrics status ("ok"/"failed"/"parked"/
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


{-| runOutcome derives the DISPLAY truth for a run, fusing the server-derived
pipeline build status with the agent step's own status (U3). The precedence is
"worst truth wins":

1.  A terminally-bad BUILD is final — failed/errored/aborted render as such
    even if the metric row still says "parked" (an abort-while-parked leaves
    the parked row behind forever; it must not pulse "Waiting on you" for a
    dead run).
2.  Otherwise parked beats everything: the build deliberately stays "started"
    while a HITL checkpoint waits, and a merely-open (or even succeeded)
    build must not hide that the operator is needed.
3.  Otherwise a step-reported failure ("error"/"failed") is never masked — not
    by a succeeded build (attempts/try can fail an agent step inside a green
    build) and not by a still-open one (the row lands at step end, so a dead
    run would otherwise show Running until the build closes, indefinitely for
    wedged builds).
4.  Only then does the build status speak: succeeded is a green OK only with a
    result in hand (else NoOutput — never a green OK on a run that did not
    deliver), started/pending render Running, and an absent build status
    falls back to the step status alone.

-}
runOutcome : { buildStatus : String, runStatus : String, hasResult : Bool } -> Maybe Status
runOutcome { buildStatus, runStatus, hasResult } =
    case buildStatus of
        "failed" ->
            Just Failed

        "errored" ->
            Just Errored

        "aborted" ->
            Just Aborted

        _ ->
            if runStatus == "parked" then
                Just AwaitingHuman

            else if runStatus == "error" then
                Just Errored

            else if runStatus == "failed" then
                Just Failed

            else if runStatus == "incomplete" then
                -- #41: the runner wrote no flight recording. Mirror the server's
                -- DeriveOutcome exactly (agent/schema metrics.go): a terminally
                -- bad build already returned red above; a still-open build is
                -- Running; a succeeded/unknown build degrades to amber Unrecorded
                -- (never red, never empty).
                if buildStatus == "started" || buildStatus == "pending" then
                    Just (Running Nothing)

                else
                    Just Unrecorded

            else
                case buildStatus of
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


{-| fromOutcomeToken maps the server-derived `outcome` wire field (the same
U3 fusion, computed once in agent/schema DeriveOutcome) to a badge. Unknown
tokens — an older server that omits the field, or a newer one with new
vocabulary — yield Nothing so the caller can fall back to the local fusion.
-}
fromOutcomeToken : String -> Maybe Status
fromOutcomeToken token =
    case token of
        "ok" ->
            Just Succeeded

        "no_output" ->
            Just NoOutput

        "unrecorded" ->
            Just Unrecorded

        "running" ->
            Just (Running Nothing)

        "parked" ->
            Just AwaitingHuman

        "failed" ->
            Just Failed

        "errored" ->
            Just Errored

        "aborted" ->
            Just Aborted

        _ ->
            Nothing


{-| displayOutcome renders a run's display truth: the server-derived outcome
when present (the rule then lives in exactly one place, agent/schema
DeriveOutcome), falling back to the local runOutcome fusion for servers that
predate the field.
-}
displayOutcome : { outcome : String, buildStatus : String, runStatus : String, hasResult : Bool } -> Maybe Status
displayOutcome { outcome, buildStatus, runStatus, hasResult } =
    case fromOutcomeToken outcome of
        Just status ->
            Just status

        Nothing ->
            runOutcome { buildStatus = buildStatus, runStatus = runStatus, hasResult = hasResult }


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

        Warn ->
            "agent-badge--warn"

        Bad ->
            "agent-badge--bad"

        Error ->
            "agent-badge--error"


{-| The hex color for a tone — THE single source of the status palette,
mirroring the `agent-badge--*` rules in web/public/main.css so the step-DAG
boxes and the badges can never drift apart.
-}
toneColor : Tone -> String
toneColor t =
    case t of
        Neutral ->
            "#9b9b9b"

        Info ->
            "#4a90e2"

        Active ->
            "#f1c40f"

        Attention ->
            "#f5a623"

        Good ->
            "#11c560"

        Warn ->
            "#ed4b35"

        Bad ->
            "#ed4b35"

        Error ->
            "#d58808"


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
