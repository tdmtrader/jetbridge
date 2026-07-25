module AgentReviewsPageTests exposing (all)

import Application.Application as Application
import Common
import Data
import Expect
import Message.Callback as Callback
import Message.Effects as Effects
import Message.Message
import Message.TopLevelMessage as Msgs
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (class, containing, text)
import Url


sampleSummary =
    { buildId = 42
    , buildName = "3"
    , teamName = "main"
    , pipelineName = "cs"
    , jobName = "ar"
    , repo = "concourse"
    , commitSha = "abc123def456"
    , branch = "jetbridge"
    , score = 7.5
    , maxScore = 10
    , pass = True
    , provenCount = 2
    , observationCount = 3
    , summary = "stuff"
    , createdAt = 0
    , evaluatedCount = 1
    , snapshotId = Nothing
    , workflowRunId = Nothing
    , productionId = Nothing
    }


otherSummary =
    { sampleSummary | buildId = 43, pipelineName = "other", evaluatedCount = 5 }


initTeamAgentReviews : ( Application.Model, List Effects.Effect )
initTeamAgentReviews =
    Application.init Data.flags
        { protocol = Url.Http
        , host = ""
        , port_ = Nothing
        , path = "/teams/main/agent-reviews"
        , query = Nothing
        , fragment = Nothing
        }


all : Test
all =
    describe "agent reviews page"
        [ test "fetches team reviews on load" <|
            \_ ->
                initTeamAgentReviews
                    |> Tuple.second
                    |> Common.contains (Effects.FetchTeamAgentReviews "main")
        , test "renders review rows with score and evaluated count" <|
            \_ ->
                Common.init "/teams/main/agent-reviews"
                    |> Application.handleCallback
                        (Callback.TeamAgentReviewsFetched (Ok [ sampleSummary ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-review-row" ]
                    |> Query.has
                        [ containing [ text "7.5" ]
                        , containing [ text "cs / ar" ]
                        , containing [ text "your feedback: 1 of 5 verdicts" ]
                        ]
        , test "renders an error notice when reviews fail to load" <|
            \_ ->
                Common.init "/teams/main/agent-reviews"
                    |> Application.handleCallback
                        (Callback.TeamAgentReviewsFetched Data.httpUnauthorized)
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has [ containing [ text "Couldn't load agent reviews." ] ]
        , test "renders an empty notice when there are no reviews" <|
            \_ ->
                Common.init "/teams/main/agent-reviews"
                    |> Application.handleCallback
                        (Callback.TeamAgentReviewsFetched (Ok []))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has [ containing [ text "No agent reviews yet." ] ]
        , test "renders the filter controls when reviews are present" <|
            \_ ->
                Common.init "/teams/main/agent-reviews"
                    |> Application.handleCallback
                        (Callback.TeamAgentReviewsFetched (Ok [ sampleSummary ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has [ class "agent-reviews-pipeline-filter" ]
        , test "the pipeline filter narrows the visible rows" <|
            \_ ->
                Common.init "/teams/main/agent-reviews"
                    |> Application.handleCallback
                        (Callback.TeamAgentReviewsFetched (Ok [ sampleSummary, otherSummary ]))
                    |> Tuple.first
                    |> Application.update
                        (Msgs.Update <| Message.Message.AgentReviewsPipelineFilterChanged "cs")
                    |> Tuple.first
                    |> Common.queryView
                    |> Expect.all
                        [ Query.has [ containing [ text "cs / ar" ] ]
                        , Query.hasNot [ containing [ text "other / ar" ] ]
                        ]
        , test "distinguishes rows with the same workflow but different instance identity" <|
            \_ ->
                let
                    firstInstance =
                        { sampleSummary | buildId = 100 }

                    secondInstance =
                        { sampleSummary | buildId = 200 }
                in
                Common.init "/teams/main/agent-reviews"
                    |> Application.handleCallback
                        (Callback.TeamAgentReviewsFetched (Ok [ firstInstance, secondInstance ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Expect.all
                        [ Query.has [ containing [ text "build 100" ] ]
                        , Query.has [ containing [ text "build 200" ] ]
                        ]
        , test "shows a no-match notice when the filter excludes everything" <|
            \_ ->
                Common.init "/teams/main/agent-reviews"
                    |> Application.handleCallback
                        (Callback.TeamAgentReviewsFetched (Ok [ sampleSummary ]))
                    |> Tuple.first
                    |> Application.update
                        (Msgs.Update <| Message.Message.AgentReviewsPipelineFilterChanged "zzz")
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has [ containing [ text "No reviews match this filter." ] ]
        ]
