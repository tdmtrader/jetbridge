module BuildAgentReviewTests exposing (all)

import Application.Application as Application
import Common
import Concourse.AgentReview as AgentReview
import Concourse.BuildStatus exposing (BuildStatus(..))
import Data
import Dict
import Expect
import Message.Callback as Callback
import Message.Effects as Effects
import Message.Message
import Message.TopLevelMessage as Msgs
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (class, containing, id, text)


withBuildLoaded : Application.Model -> Application.Model
withBuildLoaded =
    Application.handleCallback
        (Callback.BuildFetched (Ok (Data.jobBuild BuildStatusSucceeded)))
        >> Tuple.first


sampleReview : AgentReview.BuildReview
sampleReview =
    { info =
        { buildId = 1, buildName = "1", teamName = "t", pipelineName = "p", jobName = "j"
        , repo = "concourse", commitSha = "abc123def", branch = "jetbridge"
        , score = 7.5, maxScore = 10, pass = True
        , provenCount = 1, observationCount = 1, summary = "one bug"
        , createdAt = 0, evaluatedCount = 0
        }
    , provenIssues =
        [ { id = "PI-1", severity = "high", title = "nil deref", description = "boom"
          , file = "a.go", line = 10, category = "correctness"
          , testName = "TestNil", testOutput = "FAIL"
          }
        ]
    , observations = []
    , feedback = Dict.empty
    , findingCount = 2
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
                                { repo = "concourse"
                                , commitSha = "abc123def"
                                , findingId = "PI-1"
                                , verdict = "accurate"
                                , reviewer = "anonymous"
                                }
                        )
                    |> Tuple.second
                    |> Common.contains
                        (Effects.SubmitAgentReviewVerdict
                            { repo = "concourse"
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
                                    { repo = "concourse"
                                    , commitSha = "abc123def"
                                    , findingId = "PI-1"
                                    , verdict = "accurate"
                                    , reviewer = "anonymous"
                                    }
                            )

                    submit =
                        Effects.SubmitAgentReviewVerdict
                            { repo = "concourse"
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
        ]
