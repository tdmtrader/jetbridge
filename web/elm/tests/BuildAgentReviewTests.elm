module BuildAgentReviewTests exposing (all)

import Application.Application as Application
import Common
import Concourse.AgentReview as AgentReview
import Concourse.BuildStatus exposing (BuildStatus(..))
import Data
import Dict
import Expect
import Html.Attributes as Attr
import Message.Callback as Callback
import Message.Effects as Effects
import Message.Message
import Message.TopLevelMessage as Msgs
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (attribute, class, containing, id, tag, text)


withBuildLoaded : Application.Model -> Application.Model
withBuildLoaded =
    Application.handleCallback
        (Callback.BuildFetched (Ok (Data.jobBuild BuildStatusSucceeded)))
        >> Tuple.first


sampleReview : AgentReview.BuildReview
sampleReview =
    { info =
        { buildId = 1
        , buildName = "1"
        , teamName = "t"
        , pipelineName = "p"
        , jobName = "j"
        , repo = "concourse"
        , commitSha = "abc123def"
        , branch = "jetbridge"
        , score = 7.5
        , maxScore = 10
        , pass = True
        , provenCount = 1
        , observationCount = 1
        , summary = "one bug"
        , createdAt = 0
        , evaluatedCount = 0
        , snapshotId = Nothing
        , workflowRunId = Nothing
        , productionId = Nothing
        }
    , provenIssues =
        [ { id = "PI-1"
          , severity = "high"
          , title = "nil deref"
          , description = "boom"
          , file = "a.go"
          , line = 10
          , category = "correctness"
          , testName = "TestNil"
          , testOutput = "FAIL"
          }
        ]
    , observations = []
    , feedback = Dict.empty
    , findingCount = 2
    }


linkedReview : AgentReview.BuildReview
linkedReview =
    { sampleReview
        | info =
            let
                info =
                    sampleReview.info
            in
            { info | repo = "https://github.com/org/repo.git", commitSha = "deadbeef" }
    }


idlessFinding : String -> AgentReview.Finding
idlessFinding title =
    { id = ""
    , severity = "high"
    , title = title
    , description = "boom"
    , file = "a.go"
    , line = 10
    , category = "correctness"
    , testName = "TestNil"
    , testOutput = "FAIL"
    }


{-| Two findings with no id — the read path tolerates partial decodes, so id can
be "". If the card kept interactive state keyed on the (shared) empty id, a
verdict click would post findingId="" and two distinct human verdicts would
collapse to one record. Render such findings read-only instead.
-}
idlessReview : AgentReview.BuildReview
idlessReview =
    { sampleReview
        | provenIssues = [ idlessFinding "first idless bug", idlessFinding "second idless bug" ]
        , findingCount = 2
    }


idlessObservationsReview : AgentReview.BuildReview
idlessObservationsReview =
    { sampleReview
        | provenIssues = []
        , observations =
            [ { id = "", severity = "", title = "idless obs", description = "advisory prose", file = "c.go", line = 3, category = "testing", testName = "", testOutput = "" } ]
        , findingCount = 1
    }


all : Test
all =
    describe "build page agent review panel"
        [ test "requests agent reviews when the build is fetched" <|
            \_ ->
                Common.init "/builds/1"
                    |> Application.handleCallback
                        (Callback.BuildFetched (Ok (Data.jobBuild BuildStatusSucceeded)))
                    |> Tuple.second
                    |> Common.contains (Effects.FetchBuildAgentReviews 1)
        , test "renders no panel when there are no reviews" <|
            \_ ->
                Common.init "/builds/1"
                    |> withBuildLoaded
                    |> Application.handleCallback (Callback.BuildAgentReviewsFetched (Ok []))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.hasNot [ id "agent-review-panel" ]
        , test "renders summary bar with score and counts" <|
            \_ ->
                Common.init "/builds/1"
                    |> withBuildLoaded
                    |> Application.handleCallback (Callback.BuildAgentReviewsFetched (Ok [ sampleReview ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ id "agent-review-panel" ]
                    |> Expect.all
                        [ Query.has [ containing [ text "7.5" ] ]
                        , Query.has [ containing [ text "1 proven" ] ]
                        ]
        , test "shows all six verdicts on an expanded finding" <|
            \_ ->
                Common.init "/builds/1"
                    |> withBuildLoaded
                    |> Application.handleCallback (Callback.BuildAgentReviewsFetched (Ok [ sampleReview ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-review-verdicts" ]
                    |> Expect.all
                        (AgentReview.allVerdicts
                            |> List.map (\v -> Query.has [ containing [ text (AgentReview.verdictLabel v) ] ])
                        )
        , test "renders id-less findings read-only so their interactive state can't collide" <|
            \_ ->
                Common.init "/builds/1"
                    |> withBuildLoaded
                    |> Application.handleCallback (Callback.BuildAgentReviewsFetched (Ok [ idlessReview ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ id "agent-review-panel" ]
                    |> Expect.all
                        [ Query.has [ containing [ text "first idless bug" ] ]
                        , Query.has [ containing [ text "second idless bug" ] ]
                        , Query.hasNot [ class "agent-review-verdicts" ]
                        , Query.hasNot [ tag "input" ]
                        , Query.hasNot [ class "agent-review-finding-toggle" ]
                        ]
        , test "id-less observations show their body read-only, no controls" <|
            \_ ->
                -- a short observation list (≤5) opens by default, so no
                -- toggle is needed to reveal the body
                Common.init "/builds/1"
                    |> withBuildLoaded
                    |> Application.handleCallback (Callback.BuildAgentReviewsFetched (Ok [ idlessObservationsReview ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ id "agent-review-panel" ]
                    |> Expect.all
                        [ Query.has [ containing [ text "idless obs" ] ]
                        , Query.has [ containing [ text "advisory prose" ] ]
                        , Query.hasNot [ class "agent-review-verdicts" ]
                        , Query.hasNot [ class "agent-review-finding-toggle" ]
                        ]
        , test "renders a quiet notice when the agent review fails to load" <|
            \_ ->
                Common.init "/builds/1"
                    |> withBuildLoaded
                    |> Application.handleCallback (Callback.BuildAgentReviewsFetched Data.httpUnauthorized)
                    |> Tuple.first
                    |> Common.queryView
                    |> Expect.all
                        [ Query.hasNot [ id "agent-review-panel" ]
                        , Query.has [ containing [ text "Couldn't load agent review." ] ]
                        ]
        , test "clicking a verdict submits it with the typed note" <|
            \_ ->
                Common.init "/builds/1"
                    |> withBuildLoaded
                    |> Application.handleCallback (Callback.BuildAgentReviewsFetched (Ok [ sampleReview ]))
                    |> Tuple.first
                    |> Application.update
                        (Msgs.Update <|
                            Message.Message.AgentReviewNoteChanged "PI-1" "my note"
                        )
                    |> Tuple.first
                    |> Application.update
                        (Msgs.Update <|
                            Message.Message.AgentReviewVerdictClicked
                                { reviewSnapshotId = Nothing
                                , repo = "concourse"
                                , commitSha = "abc123def"
                                , findingId = "PI-1"
                                , verdict = "accurate"
                                , reviewer = "anonymous"
                                }
                        )
                    |> Tuple.second
                    |> Common.contains
                        (Effects.SubmitAgentReviewVerdict
                            { reviewSnapshotId = Nothing
                            , repo = "concourse"
                            , commitSha = "abc123def"
                            , findingId = "PI-1"
                            , verdict = "accurate"
                            , notes = "my note"
                            , reviewer = "anonymous"
                            }
                        )
        , test "clicking a verdict twice submits twice (no dedupe in v1)" <|
            \_ ->
                let
                    click =
                        Application.update
                            (Msgs.Update <|
                                Message.Message.AgentReviewVerdictClicked
                                    { reviewSnapshotId = Nothing
                                    , repo = "concourse"
                                    , commitSha = "abc123def"
                                    , findingId = "PI-1"
                                    , verdict = "accurate"
                                    , reviewer = "anonymous"
                                    }
                            )

                    submit =
                        Effects.SubmitAgentReviewVerdict
                            { reviewSnapshotId = Nothing
                            , repo = "concourse"
                            , commitSha = "abc123def"
                            , findingId = "PI-1"
                            , verdict = "accurate"
                            , notes = ""
                            , reviewer = "anonymous"
                            }
                in
                Common.init "/builds/1"
                    |> withBuildLoaded
                    |> Application.handleCallback (Callback.BuildAgentReviewsFetched (Ok [ sampleReview ]))
                    |> Tuple.first
                    |> click
                    |> Tuple.first
                    |> click
                    |> Tuple.second
                    |> Common.contains submit
        , test "a verdict click with an empty findingId is dropped, never submitted" <|
            \_ ->
                Common.init "/builds/1"
                    |> withBuildLoaded
                    |> Application.handleCallback (Callback.BuildAgentReviewsFetched (Ok [ idlessReview ]))
                    |> Tuple.first
                    |> Application.update
                        (Msgs.Update <|
                            Message.Message.AgentReviewVerdictClicked
                                { reviewSnapshotId = Nothing
                                , repo = "concourse"
                                , commitSha = "abc123def"
                                , findingId = ""
                                , verdict = "accurate"
                                , reviewer = "anonymous"
                                }
                        )
                    |> Tuple.second
                    |> Common.notContains
                        (Effects.SubmitAgentReviewVerdict
                            { reviewSnapshotId = Nothing
                            , repo = "concourse"
                            , commitSha = "abc123def"
                            , findingId = ""
                            , verdict = "accurate"
                            , notes = ""
                            , reviewer = "anonymous"
                            }
                        )
        , test "the summary bar is a real button exposing its expanded state" <|
            \_ ->
                Common.init "/builds/1"
                    |> withBuildLoaded
                    |> Application.handleCallback (Callback.BuildAgentReviewsFetched (Ok [ sampleReview ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-review-summary" ]
                    |> Query.has [ tag "button", attribute (Attr.attribute "aria-expanded" "true") ]
        , test "links a finding's file:line to the blob at the reviewed sha" <|
            \_ ->
                Common.init "/builds/1"
                    |> withBuildLoaded
                    |> Application.handleCallback (Callback.BuildAgentReviewsFetched (Ok [ linkedReview ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ id "agent-review-panel" ]
                    |> Query.has
                        [ tag "a"
                        , attribute (Attr.href "https://github.com/org/repo/blob/deadbeef/a.go#L10")
                        ]
        , test "a bare (non-URL) repo renders file:line as plain text, not a link" <|
            \_ ->
                Common.init "/builds/1"
                    |> withBuildLoaded
                    |> Application.handleCallback (Callback.BuildAgentReviewsFetched (Ok [ sampleReview ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ id "agent-review-panel" ]
                    |> Query.hasNot [ tag "a" ]
        ]
