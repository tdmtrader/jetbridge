module AgentStepDagTests exposing (all)

import AgentBadge
import AgentTickets.StepDag as StepDag
import Concourse.Agent as Agent
import Dict
import Expect
import Html.Attributes
import Json.Decode
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (attribute, class, containing, id, tag, text)


harvestRowJson : String
harvestRowJson =
    """
    { "build_id": 561978, "step_name": "harvest", "status": "ok"
    , "build_status": "succeeded", "outcome": "ok", "summary": "pushed agent/ticket-12"
    , "cost_usd": 0.4, "wall_time_seconds": 12, "created_at": 300
    , "results":
        { "schema_version": "harvest/1", "status": "pass", "summary": "ok", "artifacts": []
        , "metadata":
            { "pushed_branch": "agent/ticket-12"
            , "gates":
                [ { "gate": "build", "status": "ok", "duration_seconds": 3.2, "flaky": false }
                , { "gate": "test", "status": "failed", "duration_seconds": 9.0 }
                ]
            , "judge": { "rubric_hash": "abc", "total": 8.0, "max_total": 10.0, "pass": true }
            }
        }
    }
    """


agentRowJson : String
agentRowJson =
    """
    { "build_id": 561978, "step_name": "implement", "status": "ok"
    , "build_status": "succeeded", "outcome": "ok", "summary": "done"
    , "cost_usd": 0.21, "wall_time_seconds": 77, "created_at": 100 }
    """


row : Int -> String -> String -> String -> Float -> Int -> Agent.RunMetric
row buildId stepName status buildStatus cost created =
    { ticketId = Just 12
    , pipelineRunId = Just 1
    , buildId = buildId
    , planId = "p"
    , stepName = stepName
    , workflowName = "develop"
    , workflowVersion = Just 1
    , status = status
    , buildStatus = buildStatus
    , outcome = ""
    , summary =
        if status == "ok" then
            "did it"

        else
            ""
    , model = ""
    , usage = { inputTokens = 0, outputTokens = 0, cacheReadInputTokens = 0, cacheCreationInputTokens = 0 }
    , turns = 1
    , wallTimeSeconds = 5
    , costUsd = cost
    , eventCounts = Dict.empty
    , createdAt = created
    , results = Agent.emptyStepResults
    }


withResults : Agent.StepResults -> Agent.RunMetric -> Agent.RunMetric
withResults res rm =
    { rm | results = res }


byBuild : List Agent.RunMetric -> Dict.Dict Int (List Agent.RunMetric)
byBuild rows =
    List.foldl
        (\m -> Dict.update m.buildId (\r -> Just (Maybe.withDefault [] r ++ [ m ])))
        Dict.empty
        rows


all : Test
all =
    describe "step DAG"
        [ describe "RunMetric.results decoder"
            [ test "decodes gates, judge and pushed branch from a harvest row" <|
                \_ ->
                    case Json.Decode.decodeString Agent.decodeRunMetric harvestRowJson of
                        Ok rm ->
                            Expect.all
                                [ \r -> Expect.equal (List.length r.results.gates) 2
                                , \r -> Expect.equal (List.map .gate r.results.gates) [ "build", "test" ]
                                , \r -> Expect.equal (List.map .status r.results.gates) [ "ok", "failed" ]
                                , \r -> Expect.equal (Maybe.map .pass r.results.judge) (Just True)
                                , \r -> Expect.equal (Maybe.map .total r.results.judge) (Just 8.0)
                                , \r -> Expect.equal r.results.pushedBranch "agent/ticket-12"
                                ]
                                rm

                        Err e ->
                            Expect.fail (Json.Decode.errorToString e)
            , test "an agent row with no results decodes to empty step results" <|
                \_ ->
                    case Json.Decode.decodeString Agent.decodeRunMetric agentRowJson of
                        Ok rm ->
                            Expect.equal rm.results Agent.emptyStepResults

                        Err e ->
                            Expect.fail (Json.Decode.errorToString e)
            ]
        , describe "attempts composition"
            [ test "one build becomes one attempt, numbered 1, newest-relevant terminal boxes on the latest" <|
                \_ ->
                    StepDag.attempts "queued" (byBuild [ row 100 "implement" "ok" "succeeded" 0.2 100 ])
                        |> List.map .index
                        |> Expect.equal [ 1 ]
            , test "two builds become attempts 1 and 2 by ascending build id" <|
                \_ ->
                    StepDag.attempts "queued"
                        (byBuild
                            [ row 100 "implement" "ok" "succeeded" 0.2 100
                            , row 200 "implement" "ok" "succeeded" 0.3 200
                            ]
                        )
                        |> List.map (\a -> ( a.buildId, a.index ))
                        |> Expect.equal [ ( 100, 1 ), ( 200, 2 ) ]
            , test "an attempt always opens with a ticket box then one box per agent step" <|
                \_ ->
                    StepDag.attempts "queued"
                        (byBuild
                            [ row 100 "implement" "ok" "succeeded" 0.2 100
                            , row 100 "polish" "ok" "succeeded" 0.1 101
                            ]
                        )
                        |> List.head
                        |> Maybe.map (.boxes >> List.map .label)
                        |> Expect.equal (Just [ "ticket", "implement", "polish" ])
            , test "a harvest row expands into gate, judge and push boxes" <|
                \_ ->
                    let
                        harvest =
                            row 100 "harvest" "ok" "succeeded" 0.4 200
                                |> withResults
                                    { gates =
                                        [ { gate = "build", status = "ok", flaky = False, durationSeconds = 3 }
                                        , { gate = "test", status = "failed", flaky = False, durationSeconds = 9 }
                                        ]
                                    , judge = Just { total = 8, maxTotal = 10, pass = True }
                                    , pushedBranch = "agent/ticket-12"
                                    }
                    in
                    StepDag.attempts "queued"
                        (byBuild [ row 100 "implement" "ok" "succeeded" 0.2 100, harvest ])
                        |> List.head
                        |> Maybe.map (.boxes >> List.map .label)
                        |> Expect.equal (Just [ "ticket", "implement", "gate: build", "gate: test", "judge 8/10", "push" ])
            , test "a failed gate box is Bad-toned, a flaky-ok gate is GoodMuted with a warn" <|
                \_ ->
                    let
                        harvest =
                            row 100 "harvest" "ok" "succeeded" 0 200
                                |> withResults
                                    { gates =
                                        [ { gate = "build", status = "ok", flaky = True, durationSeconds = 3 }
                                        , { gate = "test", status = "failed", flaky = False, durationSeconds = 9 }
                                        ]
                                    , judge = Nothing
                                    , pushedBranch = ""
                                    }
                    in
                    StepDag.attempts "queued" (byBuild [ harvest ])
                        |> List.head
                        |> Maybe.map (.boxes >> List.filter (\b -> String.startsWith "gate:" b.label) >> List.map (\b -> ( b.tone, b.warn )))
                        |> Expect.equal (Just [ ( AgentBadge.GoodMuted, True ), ( AgentBadge.Bad, False ) ])
            , test "a harvest that failed before any gate/judge/push still renders exactly one harvest box (never vanishes)" <|
                \_ ->
                    -- S-1 regression: the fail-before-gates harvests (no-op guard
                    -- HEAD==base, workspace dirty, not-a-git-repo, invalid judge)
                    -- carry empty results.metadata, so gate/judge/push expansion
                    -- is empty. The failed harvest must fall back to a single box
                    -- carrying its own failure color — not disappear from the DAG.
                    let
                        harvest =
                            -- default results = Agent.emptyStepResults (no metadata)
                            row 100 "harvest" "failed" "failed" 0.4 200
                    in
                    StepDag.attempts "queued"
                        (byBuild [ row 100 "implement" "ok" "succeeded" 0.2 100, harvest ])
                        |> List.head
                        |> Maybe.map .boxes
                        |> Expect.all
                            [ \mb -> Expect.equal (Maybe.map (List.map .label) mb) (Just [ "ticket", "implement", "harvest" ])
                            , \mb -> Expect.equal (Maybe.map (List.filter (\b -> b.label == "harvest") >> List.length) mb) (Just 1)
                            , \mb -> Expect.equal (Maybe.map (List.filter (\b -> b.label == "harvest") >> List.map .tone) mb) (Just [ AgentBadge.Bad ])
                            ]
            , test "a no-output agent step renders green (GoodMuted) with a warn, never red" <|
                \_ ->
                    -- succeeded build, ok step, but no summary/result → NoOutput.
                    -- The audit's recording-incomplete intent: green box + warning.
                    StepDag.attempts "queued"
                        (byBuild [ row 100 "implement" "ok" "succeeded" 0.2 100 |> (\m -> { m | summary = "" }) ])
                        |> List.head
                        |> Maybe.map (.boxes >> List.filter (\b -> b.label == "implement") >> List.map (\b -> ( b.tone, b.warn )))
                        |> Expect.equal (Just [ ( AgentBadge.GoodMuted, True ) ])
            , test "the latest attempt appends a review box for a needs_review ticket" <|
                \_ ->
                    StepDag.attempts "needs_review" (byBuild [ row 100 "implement" "ok" "succeeded" 0.2 100 ])
                        |> List.head
                        |> Maybe.map (.boxes >> List.map .label)
                        |> Expect.equal (Just [ "ticket", "implement", "review" ])
            , test "only the latest attempt gets terminal boxes; older attempts end at their steps" <|
                \_ ->
                    StepDag.attempts "merged"
                        (byBuild
                            [ row 100 "implement" "ok" "succeeded" 0.2 100
                            , row 200 "implement" "ok" "succeeded" 0.3 200
                            ]
                        )
                        |> List.map (.boxes >> List.map .label)
                        |> Expect.equal
                            [ [ "ticket", "implement" ]
                            , [ "ticket", "implement", "review", "merge" ]
                            ]
            ]
        , describe "rendering"
            [ test "renders nothing when there are no attempts" <|
                \_ ->
                    StepDag.view "queued" Dict.empty
                        |> Query.fromHtml
                        |> Query.hasNot [ id "ticket-step-dag" ]
            , test "renders a step-dag container with an attempt header and step boxes" <|
                \_ ->
                    StepDag.view "needs_review"
                        (byBuild
                            [ row 561978 "implement" "ok" "succeeded" 0.21 100 ]
                        )
                        |> Query.fromHtml
                        |> Expect.all
                            [ Query.has [ id "ticket-step-dag" ]
                            , Query.has [ text "attempt 1" ]
                            , Query.has [ class "agent-step-box", containing [ text "implement" ] ]
                            , Query.has [ class "agent-step-box", containing [ text "review" ] ]
                            ]
            , test "each step box links to its build" <|
                \_ ->
                    StepDag.view "queued"
                        (byBuild [ row 561978 "implement" "ok" "succeeded" 0.21 100 ])
                        |> Query.fromHtml
                        |> Query.findAll [ class "agent-step-box" ]
                        |> Query.each
                            (Query.has
                                [ tag "a"
                                , attribute (Html.Attributes.href "/builds/561978")
                                ]
                            )
            , test "a step box shows its cost and duration" <|
                \_ ->
                    StepDag.view "queued"
                        (byBuild [ row 561978 "implement" "ok" "succeeded" 0.21 100 ])
                        |> Query.fromHtml
                        |> Query.find [ class "agent-step-box", containing [ text "implement" ] ]
                        |> Expect.all
                            [ Query.has [ text "$0.21" ]
                            , Query.has [ text "5s" ]
                            ]
            , test "a no-output step box shows the warn marker" <|
                \_ ->
                    StepDag.view "queued"
                        (byBuild [ row 561978 "implement" "ok" "succeeded" 0.21 100 |> (\m -> { m | summary = "" }) ])
                        |> Query.fromHtml
                        |> Query.find [ class "agent-step-box", containing [ text "implement" ] ]
                        |> Query.has [ class "agent-step-box-warn" ]
            ]
        ]
