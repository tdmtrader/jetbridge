module AgentBadgeTests exposing (all)

import AgentBadge exposing (Status(..), Tone(..), fromApiToken, fromRunStatus, label, runOutcome, tone)
import Expect
import Test exposing (Test, describe, test)


allStatuses : List Status
allStatuses =
    [ Draft
    , Queued
    , Running Nothing
    , Running (Just "step")
    , AwaitingHuman
    , NeedsReview
    , Merged
    , MergedWithFixes
    , SentBack
    , Concluded
    , Abandoned
    , Failed
    , Errored
    , Aborted
    , Succeeded
    , NoOutput
    ]


wireTokens : List String
wireTokens =
    [ "draft"
    , "queued"
    , "running"
    , "awaiting_human"
    , "needs_review"
    , "merged"
    , "merged_with_fixes"
    , "sent_back"
    , "concluded"
    , "abandoned"
    , "failed"
    , "errored"
    ]


all : Test
all =
    describe "AgentBadge"
        [ test "every status has a non-empty, underscore-free label" <|
            \_ ->
                allStatuses
                    |> List.map label
                    |> List.all (\l -> not (String.isEmpty l) && not (String.contains "_" l))
                    |> Expect.equal True
        , test "NeedsReview and AwaitingHuman both map to Attention" <|
            \_ ->
                ( tone NeedsReview, tone AwaitingHuman )
                    |> Expect.equal ( Attention, Attention )
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
        , test "fromRunStatus maps every agent_run_metrics status to a badge" <|
            \_ ->
                [ "ok", "failed", "parked", "error" ]
                    |> List.map fromRunStatus
                    |> Expect.equal
                        [ Just Succeeded, Just Failed, Just AwaitingHuman, Just Errored ]
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
            ]
        ]
