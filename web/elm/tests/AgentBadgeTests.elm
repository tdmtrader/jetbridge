module AgentBadgeTests exposing (all)

import AgentBadge exposing (Status(..), Tone(..), description, displayOutcome, fromApiToken, fromOutcomeToken, fromRunStatus, label, runOutcome, tone)
import Expect
import Test exposing (Test, describe, test)


allStatuses : List Status
allStatuses =
    [ Draft
    , Queued
    , Running Nothing
    , Running (Just "step")
    , NeedsReview
    , Closed
    , Failed
    , Errored
    , Aborted
    , Succeeded
    , NoOutput
    , Unrecorded
    ]


wireTokens : List String
wireTokens =
    [ "draft"
    , "queued"
    , "running"
    , "needs_review"
    , "closed"
    , "failed"
    , "errored"
    ]


{-| The v2 ticket dispositions were deleted with the per-ticket outcome mirror:
whether work merged, was sent back, or was dropped is the workflow run's
outcome, and the badge must not resurrect a second vocabulary for it.
`awaiting_human` went the same way with PARK-V2: a v3 human wait is a row on
the durable run, never a step exit status the badge can be asked to render.
-}
retiredTokens : List String
retiredTokens =
    [ "merged", "merged_with_fixes", "sent_back", "concluded", "abandoned", "awaiting_human" ]


all : Test
all =
    describe "AgentBadge"
        [ test "every status has a non-empty, underscore-free label" <|
            \_ ->
                allStatuses
                    |> List.map label
                    |> List.all (\l -> not (String.isEmpty l) && not (String.contains "_" l))
                    |> Expect.equal True
        , test "every status has a non-empty description" <|
            \_ ->
                allStatuses
                    |> List.map description
                    |> List.all (not << String.isEmpty)
                    |> Expect.equal True
        , test "NeedsReview maps to Attention" <|
            \_ ->
                tone NeedsReview
                    |> Expect.equal Attention
        , test "fromApiToken returns Just for every wire token" <|
            \_ ->
                wireTokens
                    |> List.map fromApiToken
                    |> List.all (\m -> m /= Nothing)
                    |> Expect.equal True
        , test "fromApiToken returns Nothing for a bogus token" <|
            \_ ->
                fromApiToken "bogus"
                    |> Expect.equal Nothing
        , test "fromApiToken refuses the retired ticket dispositions" <|
            \_ ->
                retiredTokens
                    |> List.map fromApiToken
                    |> List.all (\m -> m == Nothing)
                    |> Expect.equal True
        , test "fromRunStatus maps every agent_run_metrics status to a badge" <|
            \_ ->
                [ "ok", "failed", "error" ]
                    |> List.map fromRunStatus
                    |> Expect.equal
                        [ Just Succeeded, Just Failed, Just Errored ]
        , test "fromRunStatus refuses the retired parked status" <|
            \_ ->
                fromRunStatus "parked"
                    |> Expect.equal Nothing
        , test "fromRunStatus returns Nothing for an unknown status" <|
            \_ ->
                fromRunStatus "bogus"
                    |> Expect.equal Nothing
        , test "a successful run badge is Good-toned" <|
            \_ ->
                tone Succeeded
                    |> Expect.equal Good
        , describe "runOutcome — build truth wins over step status (U3)"
            [ test "a step that exited ok inside a FAILED build shows Failed, not OK" <|
                \_ ->
                    runOutcome { buildStatus = "failed", runStatus = "ok", hasResult = True }
                        |> Expect.equal (Just Failed)
            , test "an errored build shows Errored regardless of step status" <|
                \_ ->
                    runOutcome { buildStatus = "errored", runStatus = "ok", hasResult = True }
                        |> Expect.equal (Just Errored)
            , test "an aborted build shows Aborted" <|
                \_ ->
                    runOutcome { buildStatus = "aborted", runStatus = "ok", hasResult = True }
                        |> Expect.equal (Just Aborted)
            , test "a succeeded build that delivered a result is OK" <|
                \_ ->
                    runOutcome { buildStatus = "succeeded", runStatus = "ok", hasResult = True }
                        |> Expect.equal (Just Succeeded)
            , test "a succeeded build with NO result is No output, never a green OK" <|
                \_ ->
                    runOutcome { buildStatus = "succeeded", runStatus = "ok", hasResult = False }
                        |> Expect.equal (Just NoOutput)
            , test "No output is Warn-toned, not Good" <|
                \_ ->
                    tone NoOutput
                        |> Expect.notEqual Good
            , test "falls back to the step status when the build status is absent" <|
                \_ ->
                    runOutcome { buildStatus = "", runStatus = "failed", hasResult = True }
                        |> Expect.equal (Just Failed)
            , test "a still-running build shows Running" <|
                \_ ->
                    runOutcome { buildStatus = "started", runStatus = "ok", hasResult = False }
                        |> Expect.equal (Just (Running Nothing))
            , test "the retired parked status under an open build derives nothing" <|
                \_ ->
                    -- PARK-V2 is gone; 'parked' is now an unknown step status,
                    -- and an unknown status under a build that says nothing
                    -- useful must not be invented into a verdict.
                    runOutcome { buildStatus = "", runStatus = "parked", hasResult = True }
                        |> Expect.equal Nothing
            , test "a step that reported error inside a SUCCEEDED build is Errored, never a green OK" <|
                \_ ->
                    -- attempts:/try can fail an agent step inside a build that
                    -- still succeeds; the run's own failure must not be masked.
                    runOutcome { buildStatus = "succeeded", runStatus = "error", hasResult = True }
                        |> Expect.equal (Just Errored)
            , test "a step that reported failed inside a SUCCEEDED build is Failed" <|
                \_ ->
                    runOutcome { buildStatus = "succeeded", runStatus = "failed", hasResult = True }
                        |> Expect.equal (Just Failed)
            , test "a step that reported error while its build is still open is Errored, not Running" <|
                \_ ->
                    -- the metric row lands at step end; hooks or a wedged build
                    -- can hold the build open long after the run died.
                    runOutcome { buildStatus = "started", runStatus = "error", hasResult = False }
                        |> Expect.equal (Just Errored)
            ]
        , describe "displayOutcome — the server-derived outcome wins when present"
            [ test "fromOutcomeToken maps every server outcome token to its badge" <|
                \_ ->
                    [ "ok", "no_output", "running", "failed", "errored", "aborted" ]
                        |> List.map fromOutcomeToken
                        |> Expect.equal
                            [ Just Succeeded
                            , Just NoOutput
                            , Just (Running Nothing)
                            , Just Failed
                            , Just Errored
                            , Just Aborted
                            ]
            , test "prefers the server outcome over the locally-fused fields" <|
                \_ ->
                    -- deliberately contradictory fallback fields prove the
                    -- token was used: the local fusion would say Succeeded
                    displayOutcome { outcome = "failed", buildStatus = "succeeded", runStatus = "ok", hasResult = True }
                        |> Expect.equal (Just Failed)
            , test "falls back to the local fusion when the server sent no outcome (old server)" <|
                \_ ->
                    displayOutcome { outcome = "", buildStatus = "failed", runStatus = "ok", hasResult = True }
                        |> Expect.equal (Just Failed)
            , test "falls back to the local fusion on an unknown token (newer server)" <|
                \_ ->
                    displayOutcome { outcome = "quantum_flux", buildStatus = "succeeded", runStatus = "ok", hasResult = True }
                        |> Expect.equal (Just Succeeded)
            ]
        , describe "W-14 — status vocabulary through the single display map"
            [ test "the ok outcome displays as 'succeeded', not 'OK'" <|
                \_ ->
                    label Succeeded
                        |> Expect.equal "succeeded"
            , test "fromOutcomeToken 'ok' still decodes to Succeeded (token unchanged)" <|
                \_ ->
                    fromOutcomeToken "ok"
                        |> Expect.equal (Just Succeeded)
            , test "fromOutcomeToken 'unrecorded' (#41) decodes to the Unrecorded badge" <|
                \_ ->
                    fromOutcomeToken "unrecorded"
                        |> Expect.equal (Just Unrecorded)
            , test "the unrecorded badge is amber (Warn), the no_output colour family — not red" <|
                \_ ->
                    tone Unrecorded
                        |> Expect.equal Warn
            , test "runStatus 'incomplete' with no fused outcome degrades to amber, not red or empty" <|
                \_ ->
                    -- #41: a green build whose runner wrote no flight recording.
                    runOutcome { buildStatus = "succeeded", runStatus = "incomplete", hasResult = False }
                        |> Expect.equal (Just Unrecorded)
            , test "an incomplete run on a server without any build status still shows amber, never empty" <|
                \_ ->
                    runOutcome { buildStatus = "", runStatus = "incomplete", hasResult = False }
                        |> Expect.equal (Just Unrecorded)
            , test "an incomplete run on a still-open build shows Running, mirroring DeriveOutcome (not amber)" <|
                \_ ->
                    -- the server's DeriveOutcome maps incomplete+started/pending
                    -- to running; the local fusion must agree so an in-flight run
                    -- does not flash amber before its recording lands.
                    [ runOutcome { buildStatus = "started", runStatus = "incomplete", hasResult = False }
                    , runOutcome { buildStatus = "pending", runStatus = "incomplete", hasResult = False }
                    ]
                        |> Expect.equal [ Just (Running Nothing), Just (Running Nothing) ]
            , test "displayOutcome routes the 'unrecorded' token to the amber badge" <|
                \_ ->
                    displayOutcome { outcome = "unrecorded", buildStatus = "succeeded", runStatus = "incomplete", hasResult = False }
                        |> Expect.equal (Just Unrecorded)
            , test "toneColor returns the good hex for a Good status" <|
                \_ ->
                    AgentBadge.toneColor (AgentBadge.tone AgentBadge.Succeeded)
                        |> Expect.equal "#11c560"
            , test "toneColor returns the bad hex for a Failed status" <|
                \_ ->
                    AgentBadge.toneColor (AgentBadge.tone AgentBadge.Failed)
                        |> Expect.equal "#ed4b35"
            , test "toneColor covers every tone (no empty string)" <|
                \_ ->
                    [ AgentBadge.Neutral, AgentBadge.Info, AgentBadge.Active, AgentBadge.Attention, AgentBadge.Good, AgentBadge.Warn, AgentBadge.Bad, AgentBadge.Error ]
                        |> List.map AgentBadge.toneColor
                        |> List.all (\c -> String.startsWith "#" c)
                        |> Expect.equal True
            ]
        ]
