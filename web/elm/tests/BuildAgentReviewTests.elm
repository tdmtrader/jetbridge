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
import Test.Html.Event as Event
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
        , workflowName = "code-review"
        , conclusion = "changes-required"
        , summary = "one bug"
        , severityCounts = Dict.fromList [ ( "critical", 1 ), ( "observation", 1 ) ]
        , createdAt = 0
        , evaluatedCount = 0
        , snapshotId = Just "9007199254740993"
        , workflowRunId = Just "9007199254740995"
        , productionId = Just "9007199254740997"
        }
    , provenIssues =
        [ { id = "PI-1"
          , severity = "critical"
          , blocking = True
          , title = "nil deref"
          , description = "boom"
          , file = "a.go"
          , line = 10
          , category = "correctness"
          }
        ]
    , observations = []
    , feedback = Dict.empty
    , findingCount = 2
    }


idlessFinding : String -> AgentReview.Finding
idlessFinding title =
    { id = ""
    , severity = "critical"
    , blocking = True
    , title = title
    , description = "boom"
    , file = "a.go"
    , line = 10
    , category = "correctness"
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
            [ { id = ""
              , severity = "observation"
              , blocking = False
              , title = "idless obs"
              , description = "advisory prose"
              , file = "c.go"
              , line = 3
              , category = "testing"
              }
            ]
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
        , test "renders the summary bar with the conclusion and counts, never a score" <|
            \_ ->
                Common.init "/builds/1"
                    |> withBuildLoaded
                    |> Application.handleCallback (Callback.BuildAgentReviewsFetched (Ok [ sampleReview ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ id "agent-review-panel" ]
                    |> Expect.all
                        [ Query.has [ class "agent-review-conclusion", containing [ text "changes required" ] ]
                        , Query.has [ containing [ text "1 findings · 1 observations" ] ]
                        , Query.hasNot [ containing [ text "/ 10" ] ]
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
        , test "offers no verdict controls when the review has no snapshot identity" <|
            \_ ->
                let
                    info =
                        sampleReview.info
                in
                Common.init "/builds/1"
                    |> withBuildLoaded
                    |> Application.handleCallback
                        (Callback.BuildAgentReviewsFetched
                            (Ok [ { sampleReview | info = { info | snapshotId = Nothing } } ])
                        )
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ id "agent-review-panel" ]
                    |> Query.hasNot [ class "agent-review-verdicts" ]
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
        , test "clicking a verdict submits it against the review snapshot with the typed note" <|
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
                                { teamName = "t"
                                , reviewSnapshotId = "9007199254740993"
                                , findingId = "PI-1"
                                , verdict = "accurate"
                                , reviewer = "anonymous"
                                }
                        )
                    |> Tuple.second
                    |> Common.contains
                        (Effects.SubmitAgentReviewVerdict
                            { teamName = "t"
                            , reviewSnapshotId = "9007199254740993"
                            , findingId = "PI-1"
                            , verdict = "accurate"
                            , notes = "my note"
                            , reviewer = "anonymous"
                            }
                        )
        , test "the verdict control carries the team from the rendered review" <|
            \_ ->
                Common.init "/builds/1"
                    |> withBuildLoaded
                    |> Application.handleCallback (Callback.BuildAgentReviewsFetched (Ok [ sampleReview ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-review-verdicts" ]
                    |> Query.find [ tag "button", containing [ text "accurate" ] ]
                    |> Event.simulate Event.click
                    |> Event.expect
                        (Msgs.Update <|
                            Message.Message.AgentReviewVerdictClicked
                                { teamName = "t"
                                , reviewSnapshotId = "9007199254740993"
                                , findingId = "PI-1"
                                , verdict = "accurate"
                                , reviewer = "anonymous"
                                }
                        )
        , test "a verdict click with an empty findingId is dropped, never submitted" <|
            \_ ->
                Common.init "/builds/1"
                    |> withBuildLoaded
                    |> Application.handleCallback (Callback.BuildAgentReviewsFetched (Ok [ idlessReview ]))
                    |> Tuple.first
                    |> Application.update
                        (Msgs.Update <|
                            Message.Message.AgentReviewVerdictClicked
                                { teamName = "t"
                                , reviewSnapshotId = "9007199254740993"
                                , findingId = ""
                                , verdict = "accurate"
                                , reviewer = "anonymous"
                                }
                        )
                    |> Tuple.second
                    |> Common.notContains
                        (Effects.SubmitAgentReviewVerdict
                            { teamName = "t"
                            , reviewSnapshotId = "9007199254740993"
                            , findingId = ""
                            , verdict = "accurate"
                            , notes = ""
                            , reviewer = "anonymous"
                            }
                        )
        , test "a verdict click with no review snapshot names no review, so it is dropped" <|
            \_ ->
                Common.init "/builds/1"
                    |> withBuildLoaded
                    |> Application.handleCallback (Callback.BuildAgentReviewsFetched (Ok [ sampleReview ]))
                    |> Tuple.first
                    |> Application.update
                        (Msgs.Update <|
                            Message.Message.AgentReviewVerdictClicked
                                { teamName = "t"
                                , reviewSnapshotId = ""
                                , findingId = "PI-1"
                                , verdict = "accurate"
                                , reviewer = "anonymous"
                                }
                        )
                    |> Tuple.second
                    |> Common.notContains
                        (Effects.SubmitAgentReviewVerdict
                            { teamName = "t"
                            , reviewSnapshotId = ""
                            , findingId = "PI-1"
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
        , test "a finding's file:line is plain text — a subject digest is not a blob URL" <|
            \_ ->
                Common.init "/builds/1"
                    |> withBuildLoaded
                    |> Application.handleCallback (Callback.BuildAgentReviewsFetched (Ok [ sampleReview ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ id "agent-review-panel" ]
                    |> Expect.all
                        [ Query.has [ class "agent-review-finding-location", containing [ text "a.go:10" ] ]
                        , Query.hasNot [ tag "a" ]
                        ]
        ]
