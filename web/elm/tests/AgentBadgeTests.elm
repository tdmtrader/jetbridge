module AgentBadgeTests exposing (all)

import AgentBadge exposing (Status(..), Tone(..), fromApiToken, label, tone)
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
        ]
