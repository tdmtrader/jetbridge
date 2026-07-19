# Ticket-page Step DAG (S-1, audit Proposal A) Implementation Plan

> For agentic workers: REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax.

**Goal:** Render each ticket ATTEMPT (one pipeline build) on the ticket detail page as a horizontal chain of pipeline-grammar step boxes (ticket → agent steps → gate:build → judge → push → review → merge), each box colored by state, annotated with cost and duration, and click-through to its build.

**Architecture:** The existing `GET /api/v1/agent/tickets/:ticket_id/metrics` endpoint already returns, per agent step, the `step_name`, server-derived `outcome`, `cost_usd`, `wall_time_seconds`, and (on the terminal `harvest` row) a `results` payload whose `metadata.gates` / `metadata.judge` / `metadata.pushed_branch` carry the gate outcomes, judge verdict, and push branch (written by `agent/harvest/runner.go`, scanned back with `results` intact by `atc/db/agent_run_metrics_factory.go`). So Part 1 (server) needs **no new endpoint** — the DAG composes entirely client-side from data already on the wire. The only server-adjacent change is extending the Elm `RunMetric` decoder to surface the currently-dropped `results` sub-fields. Part 2 (Elm) adds a new pure `AgentTickets.StepDag` module that groups metrics by build into `Attempt`s of `StepBox`es and renders them, reusing the app's existing status-color grammar (`AgentBadge` tones — the classic pipeline graph is a JS `renderPipeline` port and cannot be reused as Elm; the badge/step-tree color vocabulary is the reusable Elm status language).

**Tech Stack:** Elm 0.19.1 (web UI), `elm-test` (unit tests, `Test.Html`), `hack/build-web.sh` (bundle rebuild). No Go changes. No migration.

---

## Why no new server endpoint (Part 1 justification)

Grounded reads (all confirmed against HEAD `6188b2a8c1`):

- Route + handler: `atc/routes.go:315` (`ListAgentRunMetrics` → `GET /api/v1/agent/tickets/:ticket_id/metrics`); handler `agent/api/metrics/handler.go:80` `ListByTicket` JSON-encodes the full `[]schema.RunMetrics` slice.
- The wire row `schema.RunMetrics` (`agent/schema/metrics.go:18`) has `StepName`, `WallTimeSeconds`, `CostUSD`, server-derived `Outcome`/`BuildStatus`, `CreatedAt`, and `Results json.RawMessage` (`json:"results,omitempty"`).
- `Results` is the full `agent/schema/results.go` `Results{...Metadata map[string]interface{}}`. On the terminal `harvest` step (`agent/dispatch/render.go:248` sets `Name: "harvest"`), `agent/harvest/runner.go:240` writes `m["gates"] = f.Gates` (`[]harvest.GateOutcome`, shape at `agent/harvest/gates.go:17`: `gate`, `status` = `ok|failed|error`, `flaky`, `duration_seconds`), `runner.go:244` writes `m["judge"]` (`rubric_hash`/`total`/`max_total`/`pass`), and `runner.go:237` writes `m["pushed_branch"]`.
- The **DB read path preserves `results`**: `atc/db/agent_run_metrics_factory.go:285-286` scans `resultsPayload` into `rm.Results`, then re-derives `Outcome` on read (`:289`). So every ticket-metrics list already carries gates + judge + push for the harvest row.

The Elm side (`web/elm/src/Concourse/Agent.elm:169` `decodeRunMetric`) simply **drops** the `results` field today. Surfacing it is a decoder-only change — additive and back-compatible (absent `results` → empty). Building a bespoke attempt-summary endpoint would duplicate composition the client can do from data it already fetches every 5s, and would add a six-touchpoint route for no new data. **Recommendation: compose client-side; add no endpoint.** (This is revisited in Open Decisions if a server-side rollup is later wanted for `/agent` or fly parity.)

---

## File Structure

| File | Create/Modify | Responsibility |
|---|---|---|
| `web/elm/src/Concourse/Agent.elm` | Modify | Add `StepResults`/`GateResult`/`JudgeResult` types, `emptyStepResults`, their decoders; add `results : StepResults` field to `RunMetric` and decode it tolerantly from `results.metadata`. |
| `web/elm/src/AgentBadge.elm` | Modify | Add `toneColor : Tone -> String` (the single source of the status hex palette, mirroring `agent-badge--*` in `main.css`). |
| `web/elm/src/AgentTickets/StepDag.elm` | Create | Pure composition (`attempts : String -> Dict Int (List RunMetric) -> List Attempt`) + `view` rendering each attempt as a horizontal box chain. No `Message` dependency (`Html msg`). |
| `web/elm/src/AgentTickets/AgentTicket.elm` | Modify | Insert `StepDag.view ticket.state model.runMetricsByBuild` as a new `#ticket-step-dag` section in `rest`, above the existing `runHistory`. Nothing else touched (run rows stay — W-2's surface). |
| `web/elm/tests/AgentBadgeTests.elm` | Modify | Cover `toneColor`. |
| `web/elm/tests/AgentStepDagTests.elm` | Create | Pure composition tests + `Test.Html` box-rendering tests + `results` decoder test. |
| `web/elm/tests/AgentTicketPageTests.elm` | Modify | Add a page test that the DAG renders; add `results` to the two existing `RunMetric` literals. |
| `web/elm/tests/AgentPageTests.elm` | Modify | Add `results` to its one `RunMetric` literal. |
| `web/elm/tests/BuildTicketBarTests.elm` | Modify | Add `results` to its one `RunMetric` literal. |
| `web/public/elm.min.js` | Modify (generated) | Rebuilt from source via `hack/build-web.sh` — the served bundle. |

---

## Coordination notes (read before starting)

- **No `render.go` edits.** This plan does not touch `agent/dispatch/render.go` at all.
- **No migration.** Pure Elm + generated bundle. The head migration stays `1773106066` (next free `1773106067`).
- **Do not touch `runRow` / `runHistory` / their tests.** W-2 reworks the run-row identity. This plan ADDS the DAG above them and leaves the flat rows intact. Consolidation is an Open Decision.
- **L-1 coupling (recording-incomplete tier):** the DAG derives each box's tone/warn from `AgentBadge` status. When L-1 adds a `delivered-unrecorded`/`incomplete` amber token to `AgentBadge.fromOutcomeToken`, add one case to `StepDag.boxDisplay` mapping it to `(GoodMuted, warn=True)` — a green box with a ⚠, never red. Until L-1 lands, the same green-box-with-warning path is exercised by today's `NoOutput` (see Task 3). Search anchor comment `-- L-1 coupling` is placed in the code.
- **W-2 coupling (attempt naming):** the DAG numbers attempts 1..N by ascending build id and labels each header `attempt N`. When W-2 lands its own attempt-numbering helper, both should share one function; until then this plan owns it. Anchor comment `-- W-2 coupling`.
- **Elm bundle:** there is **no local elm-build gate today** (WF-2 adds one). Task 6 rebuilds and commits `web/public/elm.min.js`; skipping it serves the stale bundle.

---

## Task 1 — `AgentBadge.toneColor`: one source for the status palette

**Files**
- Modify: `web/elm/src/AgentBadge.elm` (exposing list + new function)
- Test: `web/elm/tests/AgentBadgeTests.elm`

**Steps**

- [ ] Write the failing test. Append to the `describe` list in `web/elm/tests/AgentBadgeTests.elm`:

```elm
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
                [ AgentBadge.Neutral, AgentBadge.Info, AgentBadge.Active, AgentBadge.Attention, AgentBadge.Good, AgentBadge.GoodMuted, AgentBadge.Warn, AgentBadge.Calm, AgentBadge.Bad, AgentBadge.Error ]
                    |> List.map AgentBadge.toneColor
                    |> List.all (\c -> String.startsWith "#" c)
                    |> Expect.equal True
```

Ensure the test module imports `AgentBadge` and exposes `Tone(..)`/`Status(..)` usage (it already imports `AgentBadge`; `Tone` and `Status` become reachable once exported in the next step).

- [ ] Run it, expect FAIL:

```
cd web/elm && elm-test tests/AgentBadgeTests.elm
```

Expected: a compile error `NAMING ERROR ... toneColor ... does not expose` (and `Tone` not exposed). This is the red state.

- [ ] Minimal implementation. In `web/elm/src/AgentBadge.elm`, add `Tone(..)` and `toneColor` to the `exposing (...)` block (it already exposes `Tone(..)` and `Status(..)`; if `Tone(..)` is not exposed, change `Tone` → `Tone(..)`), then add:

```elm
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

        GoodMuted ->
            "#419867"

        Warn ->
            "#ed4b35"

        Calm ->
            "#2d76cc"

        Bad ->
            "#ed4b35"

        Error ->
            "#d58808"
```

- [ ] Run it, expect PASS:

```
cd web/elm && elm-test tests/AgentBadgeTests.elm
```

Expected: `TEST RUN PASSED`.

- [ ] Commit:

```
git add web/elm/src/AgentBadge.elm web/elm/tests/AgentBadgeTests.elm
git commit -m "feat(web): AgentBadge.toneColor — one source for the status palette"
```

---

## Task 2 — Surface `results` (gates/judge/push) on the `RunMetric` decoder

**Files**
- Modify: `web/elm/src/Concourse/Agent.elm` (types, `emptyStepResults`, decoders, `RunMetric` field, exposing list)
- Test: `web/elm/tests/AgentStepDagTests.elm` (new file — decoder section)
- Modify: `web/elm/tests/AgentTicketPageTests.elm`, `web/elm/tests/AgentPageTests.elm`, `web/elm/tests/BuildTicketBarTests.elm` (add the new field to existing `RunMetric` literals so they still compile)

**Steps**

- [ ] Write the failing test. Create `web/elm/tests/AgentStepDagTests.elm` with just the decoder section for now:

```elm
module AgentStepDagTests exposing (all)

import Concourse.Agent as Agent
import Expect
import Json.Decode
import Test exposing (Test, describe, test)


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
        ]
```

- [ ] Run it, expect FAIL:

```
cd web/elm && elm-test tests/AgentStepDagTests.elm
```

Expected: compile error — `Agent` does not expose `emptyStepResults`, and `RunMetric` has no `results` field.

- [ ] Minimal implementation in `web/elm/src/Concourse/Agent.elm`.

Add to the `exposing (...)` block: `GateResult`, `JudgeResult`, `StepResults`, `emptyStepResults`.

Add the types (near the `Usage`/`RunMetric` block):

```elm
type alias GateResult =
    { gate : String
    , status : String
    , flaky : Bool
    , durationSeconds : Float
    }


type alias JudgeResult =
    { total : Float
    , maxTotal : Float
    , pass : Bool
    }


{-| The DAG-relevant slice of a step's results.json: the harvest step's gate
outcomes, judge verdict and pushed branch (metadata keys written by
agent/harvest/runner.go). Empty for agent steps, which carry no results.
-}
type alias StepResults =
    { gates : List GateResult
    , judge : Maybe JudgeResult
    , pushedBranch : String
    }


emptyStepResults : StepResults
emptyStepResults =
    { gates = [], judge = Nothing, pushedBranch = "" }
```

Add the new field to `RunMetric` (append after `createdAt` so record order is stable):

```elm
    , createdAt : Int
    , results : StepResults
    }
```

Add the decoders:

```elm
decodeGateResult : Json.Decode.Decoder GateResult
decodeGateResult =
    Json.Decode.succeed GateResult
        |> andMap (defaultTo "" <| Json.Decode.field "gate" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "status" Json.Decode.string)
        |> andMap (defaultTo False <| Json.Decode.field "flaky" Json.Decode.bool)
        |> andMap (defaultTo 0 <| Json.Decode.field "duration_seconds" Json.Decode.float)


decodeJudgeResult : Json.Decode.Decoder JudgeResult
decodeJudgeResult =
    Json.Decode.succeed JudgeResult
        |> andMap (defaultTo 0 <| Json.Decode.field "total" Json.Decode.float)
        |> andMap (defaultTo 0 <| Json.Decode.field "max_total" Json.Decode.float)
        |> andMap (defaultTo False <| Json.Decode.field "pass" Json.Decode.bool)


{-| Decode the DAG slice out of a row's `results.metadata`. Absent/partial
results (agent steps, older servers) fall back to emptyStepResults, so this is
fully back-compatible.
-}
decodeStepResults : Json.Decode.Decoder StepResults
decodeStepResults =
    let
        atMeta name dec =
            defaultTo (dec |> alwaysEmpty)
                (Json.Decode.at [ "results", "metadata", name ] dec)
    in
    Json.Decode.succeed StepResults
        |> andMap (defaultTo [] (Json.Decode.at [ "results", "metadata", "gates" ] (Json.Decode.list decodeGateResult)))
        |> andMap (Json.Decode.maybe (Json.Decode.at [ "results", "metadata", "judge" ] decodeJudgeResult))
        |> andMap (defaultTo "" (Json.Decode.at [ "results", "metadata", "pushed_branch" ] Json.Decode.string))
```

Remove the unused `atMeta`/`alwaysEmpty` scaffolding — the three `andMap` lines above are the whole decoder; delete the `let ... in` block so the final `decodeStepResults` is exactly:

```elm
decodeStepResults : Json.Decode.Decoder StepResults
decodeStepResults =
    Json.Decode.succeed StepResults
        |> andMap (defaultTo [] (Json.Decode.at [ "results", "metadata", "gates" ] (Json.Decode.list decodeGateResult)))
        |> andMap (Json.Decode.maybe (Json.Decode.at [ "results", "metadata", "judge" ] decodeJudgeResult))
        |> andMap (defaultTo "" (Json.Decode.at [ "results", "metadata", "pushed_branch" ] Json.Decode.string))
```

Wire it into `decodeRunMetric` — add one `andMap` line at the very end (after the `created_at` line), decoding from the whole row object (not a sub-field), so it reads the row's own `results`:

```elm
        |> andMap (defaultTo 0 <| Json.Decode.field "created_at" Json.Decode.int)
        |> andMap (defaultTo emptyStepResults decodeStepResults)
```

Note: `decodeStepResults` reads `at ["results","metadata",...]` from the same row object `decodeRunMetric` is decoding, so it is applied to the whole value (no `field "results"` wrapper needed). `defaultTo emptyStepResults` guards a row with no `results` at all.

- [ ] Run the decoder test, expect PASS:

```
cd web/elm && elm-test tests/AgentStepDagTests.elm
```

Expected: `TEST RUN PASSED` (2 decoder tests).

- [ ] Fix the existing `RunMetric` literals (they now fail to compile — missing `results`). Add the field to each. In `web/elm/tests/AgentTicketPageTests.elm`, both literals — the inline one (currently ending `, createdAt = 100`, around line 218) and the `metric` helper (currently ending `, createdAt = 100`, around line 257) — append:

```elm
                                          , createdAt = 100
                                          , results = { gates = [], judge = Nothing, pushedBranch = "" }
```

(match each literal's indentation; the second literal ends `, createdAt = 100\n , results = { gates = [], judge = Nothing, pushedBranch = "" }`).

In `web/elm/tests/AgentPageTests.elm`, the single `RunMetric` literal (step `"review-diff"`) — add `, results = { gates = [], judge = Nothing, pushedBranch = "" }` after its `createdAt` line.

In `web/elm/tests/BuildTicketBarTests.elm`, the single `RunMetric` literal (step `"implement"`) — add `, results = { gates = [], judge = Nothing, pushedBranch = "" }` after its `createdAt` line.

- [ ] Run the whole suite to confirm nothing else broke:

```
cd web/elm && elm-test
```

Expected: `TEST RUN PASSED`.

- [ ] Commit:

```
git add web/elm/src/Concourse/Agent.elm web/elm/tests/AgentStepDagTests.elm web/elm/tests/AgentTicketPageTests.elm web/elm/tests/AgentPageTests.elm web/elm/tests/BuildTicketBarTests.elm
git commit -m "feat(web): decode step results (gates/judge/push) on the RunMetric wire"
```

---

## Task 3 — `AgentTickets.StepDag` pure composition: builds → attempts → boxes

**Files**
- Create: `web/elm/src/AgentTickets/StepDag.elm` (composition half only in this task)
- Test: `web/elm/tests/AgentStepDagTests.elm` (composition section)

**Steps**

- [ ] Write the failing tests. Add a `composition` describe block and a fixture builder to `web/elm/tests/AgentStepDagTests.elm`. Insert these top-level helpers below the JSON fixtures:

```elm
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
```

Add `import Dict` and `import AgentTickets.StepDag as StepDag` and `import AgentBadge` to the test module. Then add to the `all` describe list:

```elm
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
```

- [ ] Run it, expect FAIL:

```
cd web/elm && elm-test tests/AgentStepDagTests.elm
```

Expected: compile error — module `AgentTickets.StepDag` not found.

- [ ] Minimal implementation. Create `web/elm/src/AgentTickets/StepDag.elm` with the composition half (the `view` comes in Task 4, but include a stub `view` so the module type-checks and Task 5 can import it):

```elm
module AgentTickets.StepDag exposing
    ( Attempt
    , BoxKind(..)
    , StepBox
    , attempts
    , view
    )

{-| The ticket-page step DAG (audit Proposal A / S-1). Composes the per-step
run metrics of one ticket into a per-attempt (per-build) horizontal chain of
pipeline-grammar boxes — ticket → agent steps → gate/judge/push (expanded from
the harvest row's results metadata) → review/merge (from the ticket state, on
the latest attempt only). Boxes are colored by the shared AgentBadge status
palette (the classic pipeline graph is a JS render port, so its Elm-reusable
grammar is the badge/step-tree status color language, reused here via
AgentBadge.tone/toneColor).
-}

import AgentBadge
import Concourse.Agent as Agent
import Dict exposing (Dict)
import Html exposing (Html)
import Html.Attributes exposing (class, href, id, style, title)
import Routes


type BoxKind
    = TicketBox
    | AgentStep
    | GateBox
    | JudgeBox
    | PushBox
    | ReviewBox
    | MergeBox


type alias StepBox =
    { label : String
    , kind : BoxKind
    , tone : AgentBadge.Tone
    , warn : Bool
    , costUsd : Float
    , durationSeconds : Int
    , buildId : Int
    }


type alias Attempt =
    { buildId : Int
    , index : Int
    , outcome : Maybe AgentBadge.Status
    , costUsd : Float
    , createdAt : Int
    , boxes : List StepBox
    }


{-| Group the ticket's builds into attempts. `byBuild` is keyed by build id
with each build's rows already in created_at ASC order (as
AgentTicket.groupMetricsByBuild produces). Ascending build id = chronological
attempt order; the last attempt is the live one and receives the terminal
review/merge boxes derived from the ticket state.
-}
attempts : String -> Dict Int (List Agent.RunMetric) -> List Attempt
attempts ticketState byBuild =
    let
        builds =
            Dict.toList byBuild

        count =
            List.length builds
    in
    builds
        |> List.indexedMap
            (\i ( buildId, rows ) ->
                buildAttempt ticketState (i + 1) (i == count - 1) buildId rows
            )


buildAttempt : String -> Int -> Bool -> Int -> List Agent.RunMetric -> Attempt
buildAttempt ticketState index isLatest buildId rows =
    let
        cost =
            rows |> List.map .costUsd |> List.sum

        createdAt =
            rows |> List.map .createdAt |> List.minimum |> Maybe.withDefault 0

        ticketBox =
            { label = "ticket", kind = TicketBox, tone = AgentBadge.Info, warn = False, costUsd = 0, durationSeconds = 0, buildId = buildId }

        stepBoxes =
            List.concatMap (stepBoxesFor buildId) rows

        tail =
            if isLatest then
                terminalBoxes ticketState buildId

            else
                []
    in
    { buildId = buildId
    , index = index
    , outcome = attemptOutcome rows
    , costUsd = cost
    , createdAt = createdAt
    , boxes = ticketBox :: stepBoxes ++ tail
    }


{-| The harvest step (plan Name "harvest", agent/dispatch/render.go) expands
into its gate/judge/push facts; every other step is a single agent box.
-}
stepBoxesFor : Int -> Agent.RunMetric -> List StepBox
stepBoxesFor buildId rm =
    if rm.stepName == "harvest" then
        harvestBoxes buildId rm

    else
        [ agentStepBox buildId rm ]


agentStepBox : Int -> Agent.RunMetric -> StepBox
agentStepBox buildId rm =
    let
        status =
            AgentBadge.displayOutcome
                { outcome = rm.outcome
                , buildStatus = rm.buildStatus
                , runStatus = rm.status
                , hasResult = rm.summary /= ""
                }

        disp =
            boxDisplay status
    in
    { label = rm.stepName
    , kind = AgentStep
    , tone = disp.tone
    , warn = disp.warn
    , costUsd = rm.costUsd
    , durationSeconds = rm.wallTimeSeconds
    , buildId = buildId
    }


{-| Tone + warn for an agent step's fused status. NoOutput — a green build that
delivered nothing — is rendered as a green (GoodMuted) box with a ⚠, honoring
the audit rule that a recording gap is a warning, never a red failure.

-- L-1 coupling: when AgentBadge gains a delivered-unrecorded/incomplete amber
status token, add a case here mapping it to { tone = GoodMuted, warn = True }.
-}
boxDisplay : Maybe AgentBadge.Status -> { tone : AgentBadge.Tone, warn : Bool }
boxDisplay status =
    case status of
        Just AgentBadge.NoOutput ->
            { tone = AgentBadge.GoodMuted, warn = True }

        Just s ->
            { tone = AgentBadge.tone s, warn = False }

        Nothing ->
            { tone = AgentBadge.Neutral, warn = False }


harvestBoxes : Int -> Agent.RunMetric -> List StepBox
harvestBoxes buildId rm =
    let
        gateBoxes =
            List.map (gateBox buildId) rm.results.gates

        judgeBoxes =
            case rm.results.judge of
                Just j ->
                    [ judgeBox buildId rm.costUsd j ]

                Nothing ->
                    []

        pushBoxes =
            if rm.results.pushedBranch /= "" then
                [ { label = "push", kind = PushBox, tone = AgentBadge.Good, warn = False, costUsd = 0, durationSeconds = 0, buildId = buildId } ]

            else
                []
    in
    gateBoxes ++ judgeBoxes ++ pushBoxes


gateBox : Int -> Agent.GateResult -> StepBox
gateBox buildId g =
    let
        ( tone, warn ) =
            case g.status of
                "ok" ->
                    if g.flaky then
                        ( AgentBadge.GoodMuted, True )

                    else
                        ( AgentBadge.Good, False )

                "failed" ->
                    ( AgentBadge.Bad, False )

                "error" ->
                    ( AgentBadge.Error, False )

                _ ->
                    ( AgentBadge.Neutral, False )
    in
    { label = "gate: " ++ g.gate
    , kind = GateBox
    , tone = tone
    , warn = warn
    , costUsd = 0
    , durationSeconds = round g.durationSeconds
    , buildId = buildId
    }


{-| The judge box carries the harvest row's cost — the judge LLM call is the
priced work in the harvest step; the gate/push boxes are unpriced shell work.
-}
judgeBox : Int -> Float -> Agent.JudgeResult -> StepBox
judgeBox buildId cost j =
    { label = "judge " ++ scoreText j.total ++ "/" ++ scoreText j.maxTotal
    , kind = JudgeBox
    , tone =
        if j.pass then
            AgentBadge.Good

        else
            AgentBadge.Bad
    , warn = False
    , costUsd = cost
    , durationSeconds = 0
    , buildId = buildId
    }


{-| Terminal ticket-lifecycle boxes appended to the LATEST attempt only.
Mirrors the human-visible endpoints of the ticket state machine.

-- W-2 coupling: attempt numbering here (1..N by build id) is the same numbering
W-2 introduces for the run-row identity; share one helper once W-2 lands.
-}
terminalBoxes : String -> Int -> List StepBox
terminalBoxes ticketState buildId =
    let
        box label kind tone =
            { label = label, kind = kind, tone = tone, warn = False, costUsd = 0, durationSeconds = 0, buildId = buildId }
    in
    case ticketState of
        "needs_review" ->
            [ box "review" ReviewBox AgentBadge.Attention ]

        "merged" ->
            [ box "review" ReviewBox AgentBadge.Good, box "merge" MergeBox AgentBadge.Good ]

        "merged_with_fixes" ->
            [ box "review" ReviewBox AgentBadge.Good, box "merge" MergeBox AgentBadge.GoodMuted ]

        "concluded" ->
            [ box "review" ReviewBox AgentBadge.Calm, box "concluded" MergeBox AgentBadge.Calm ]

        "abandoned" ->
            [ box "abandoned" MergeBox AgentBadge.Neutral ]

        _ ->
            []


{-| The attempt-level verdict: the same worst-truth-wins fusion the run rows
use (parked-anywhere, else the last step's status, joined with the build
status and whether anything was delivered). Mirrors AgentTicket.runRow.
-}
attemptOutcome : List Agent.RunMetric -> Maybe AgentBadge.Status
attemptOutcome rows =
    let
        runStatus =
            case List.filter (\m -> m.status == "parked") rows of
                parked :: _ ->
                    parked.status

                [] ->
                    rows |> List.reverse |> List.head |> Maybe.map .status |> Maybe.withDefault ""

        buildStatus =
            rows |> List.head |> Maybe.map .buildStatus |> Maybe.withDefault ""

        hasResult =
            (rows |> List.filterMap (\m -> nonEmpty m.summary) |> List.reverse |> List.head |> Maybe.withDefault "") /= ""
    in
    AgentBadge.runOutcome { buildStatus = buildStatus, runStatus = runStatus, hasResult = hasResult }


nonEmpty : String -> Maybe String
nonEmpty s =
    if s == "" then
        Nothing

    else
        Just s


scoreText : Float -> String
scoreText f =
    let
        n =
            round f
    in
    if toFloat n == f then
        String.fromInt n

    else
        String.fromFloat f


{-| Placeholder view — implemented in Task 4. Kept minimal so the module
type-checks after Task 3.
-}
view : String -> Dict Int (List Agent.RunMetric) -> Html msg
view _ _ =
    Html.text ""
```

- [ ] Run it, expect PASS:

```
cd web/elm && elm-test tests/AgentStepDagTests.elm
```

Expected: `TEST RUN PASSED` (decoder + composition sections). If `Routes`, `class`, `href`, `id`, `title` are reported as unused, that is a warning only (elm-test still passes); they are used in Task 4.

- [ ] Commit:

```
git add web/elm/src/AgentTickets/StepDag.elm web/elm/tests/AgentStepDagTests.elm
git commit -m "feat(web): StepDag composition — builds into attempts of colored step boxes"
```

---

## Task 4 — `StepDag.view`: render attempts as horizontal box chains

**Files**
- Modify: `web/elm/src/AgentTickets/StepDag.elm` (replace the `view` stub)
- Test: `web/elm/tests/AgentStepDagTests.elm` (rendering section)

**Steps**

- [ ] Write the failing tests. Add a `rendering` describe block to `all` in `web/elm/tests/AgentStepDagTests.elm`. Add these imports to the test module: `import Html.Attributes` `import Test.Html.Query as Query` `import Test.Html.Selector exposing (attribute, class, containing, id, tag, text)`.

```elm
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
                        |> Query.find [ id "ticket-step-dag" ]
                        |> Expect.all
                            [ Query.has [ text "attempt 1" ]
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
```

- [ ] Run it, expect FAIL:

```
cd web/elm && elm-test tests/AgentStepDagTests.elm
```

Expected: the rendering tests fail — `id "ticket-step-dag"` never found (the stub renders `Html.text ""`).

- [ ] Minimal implementation. Replace the `view` stub in `web/elm/src/AgentTickets/StepDag.elm` with:

```elm
{-| Render the ticket's attempts, newest first, each as a labeled row with a
horizontal chain of connector-separated step boxes.
-}
view : String -> Dict Int (List Agent.RunMetric) -> Html msg
view ticketState byBuild =
    case attempts ticketState byBuild of
        [] ->
            Html.text ""

        atts ->
            Html.div
                [ id "ticket-step-dag", style "margin" "12px 0" ]
                (sectionLabel "attempts"
                    :: (atts |> List.reverse |> List.map attemptView)
                )


attemptView : Attempt -> Html msg
attemptView att =
    Html.div
        [ class "agent-attempt"
        , style "margin" "10px 0"
        , style "border" "1px solid #2a2929"
        , style "padding" "8px"
        ]
        [ attemptHeader att
        , Html.div
            [ class "agent-step-dag-row"
            , style "display" "flex"
            , style "flex-wrap" "wrap"
            , style "align-items" "stretch"
            , style "gap" "0"
            , style "margin-top" "6px"
            ]
            (att.boxes
                |> List.indexedMap
                    (\i b ->
                        if i == 0 then
                            [ boxView b ]

                        else
                            [ connector, boxView b ]
                    )
                |> List.concat
            )
        ]


attemptHeader : Attempt -> Html msg
attemptHeader att =
    Html.div
        [ style "display" "flex", style "align-items" "center", style "gap" "8px", style "font-size" "12px" ]
        [ Html.span [ style "color" "#d0d0d0" ] [ Html.text ("attempt " ++ String.fromInt att.index) ]
        , case att.outcome of
            Just s ->
                AgentBadge.view s

            Nothing ->
                Html.text ""
        , Html.a
            [ href (buildHref att.buildId)
            , style "font-family" "monospace"
            , style "color" "#7aa37a"
            , style "text-decoration" "none"
            ]
            [ Html.text ("build " ++ String.fromInt att.buildId) ]
        , Html.span [ style "flex" "1" ] []
        , Html.span [ style "font-family" "monospace", style "color" "#b0b0b0" ] [ Html.text ("$" ++ usd att.costUsd) ]
        ]


boxView : StepBox -> Html msg
boxView b =
    let
        color =
            AgentBadge.toneColor b.tone
    in
    Html.a
        [ class "agent-step-box"
        , href (buildHref b.buildId)
        , title b.label
        , style "display" "inline-flex"
        , style "flex-direction" "column"
        , style "justify-content" "center"
        , style "border" ("1px solid " ++ color)
        , style "border-left" ("3px solid " ++ color)
        , style "background" "#1b1b1b"
        , style "padding" "4px 8px"
        , style "text-decoration" "none"
        ]
        [ Html.span
            [ style "display" "flex", style "align-items" "center", style "gap" "4px" ]
            ((if b.warn then
                [ Html.span
                    [ class "agent-step-box-warn"
                    , title "recording incomplete — delivered, but the run record is partial"
                    , style "color" (AgentBadge.toneColor AgentBadge.Attention)
                    ]
                    [ Html.text "⚠" ]
                ]

              else
                []
             )
                ++ [ Html.span [ style "color" color, style "font-size" "12px" ] [ Html.text b.label ] ]
            )
        , boxSublabel b
        ]


boxSublabel : StepBox -> Html msg
boxSublabel b =
    let
        parts =
            List.filterMap identity
                [ if b.costUsd > 0 then
                    Just ("$" ++ usd b.costUsd)

                  else
                    Nothing
                , if b.durationSeconds > 0 then
                    Just (duration b.durationSeconds)

                  else
                    Nothing
                ]
    in
    if List.isEmpty parts then
        Html.text ""

    else
        Html.span
            [ style "font-family" "monospace", style "color" "#7a7a7a", style "font-size" "10px", style "margin-top" "2px" ]
            [ Html.text (String.join " · " parts) ]


connector : Html msg
connector =
    Html.span
        [ style "align-self" "center", style "color" "#5a5a5a", style "padding" "0 4px" ]
        [ Html.text "→" ]


sectionLabel : String -> Html msg
sectionLabel txt =
    Html.div
        [ style "font-size" "11px", style "text-transform" "uppercase", style "letter-spacing" "0.08em", style "color" "#9aa39b", style "margin" "8px 0 4px" ]
        [ Html.text txt ]


buildHref : Int -> String
buildHref buildId =
    Routes.toString (Routes.OneOffBuild { id = buildId, highlight = Routes.HighlightNothing })


{-| Compact duration: "77s" → "1m 17s", "5s" → "5s".
-}
duration : Int -> String
duration secs =
    if secs < 60 then
        String.fromInt secs ++ "s"

    else
        String.fromInt (secs // 60) ++ "m " ++ String.fromInt (modBy 60 secs) ++ "s"


{-| Two-decimal USD, matching AgentTicket.formatUsd.
-}
usd : Float -> String
usd amount =
    let
        cents =
            round (amount * 100)

        absCents =
            abs cents

        dollars =
            absCents // 100

        rem =
            modBy 100 absCents

        frac =
            if rem < 10 then
                "0" ++ String.fromInt rem

            else
                String.fromInt rem
    in
    String.fromInt dollars ++ "." ++ frac
```

- [ ] Run it, expect PASS:

```
cd web/elm && elm-test tests/AgentStepDagTests.elm
```

Expected: `TEST RUN PASSED` (decoder + composition + rendering).

- [ ] Commit:

```
git add web/elm/src/AgentTickets/StepDag.elm web/elm/tests/AgentStepDagTests.elm
git commit -m "feat(web): StepDag view — horizontal box chain per attempt with cost/duration/warn"
```

---

## Task 5 — Wire the DAG into the ticket detail page

**Files**
- Modify: `web/elm/src/AgentTickets/AgentTicket.elm` (import + one line in `rest`)
- Test: `web/elm/tests/AgentTicketPageTests.elm` (page test)

**Steps**

- [ ] Write the failing test. Add to the `all` list in `web/elm/tests/AgentTicketPageTests.elm`:

```elm
        , test "renders the step DAG with a box per step for a fetched build" <|
            \_ ->
                withDetail sampleDetailJson
                    (\d ->
                        Common.init "/agent-tickets/12"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                            |> Tuple.first
                            |> Application.handleCallback
                                (Callback.AgentTicketMetricsFetched 12
                                    (Ok
                                        [ { ticketId = Just 12
                                          , pipelineRunId = Just 2
                                          , buildId = 561978
                                          , planId = "plan-xyz"
                                          , stepName = "implement"
                                          , workflowName = "develop"
                                          , workflowVersion = Just 1
                                          , status = "ok"
                                          , buildStatus = "succeeded"
                                          , outcome = "ok"
                                          , summary = "did the thing"
                                          , model = ""
                                          , usage = { inputTokens = 0, outputTokens = 0, cacheReadInputTokens = 0, cacheCreationInputTokens = 0 }
                                          , turns = 1
                                          , wallTimeSeconds = 77
                                          , costUsd = 0.21
                                          , eventCounts = Dict.empty
                                          , createdAt = 100
                                          , results = { gates = [], judge = Nothing, pushedBranch = "" }
                                          }
                                        ]
                                    )
                                )
                            |> Tuple.first
                            |> Common.queryView
                            |> Query.find [ id "ticket-step-dag" ]
                            |> Expect.all
                                [ Query.has [ text "attempt 1" ]
                                , Query.has [ class "agent-step-box", containing [ text "implement" ] ]
                                , Query.has [ class "agent-step-box", containing [ text "review" ] ]
                                ]
                    )
```

- [ ] Run it, expect FAIL:

```
cd web/elm && elm-test tests/AgentTicketPageTests.elm
```

Expected: `Query.find [ id "ticket-step-dag" ]` fails — the section is not rendered yet.

- [ ] Minimal implementation in `web/elm/src/AgentTickets/AgentTicket.elm`.

Add the import (alphabetical, after `import AgentBadge`):

```elm
import AgentTickets.StepDag as StepDag
```

In `content`, insert the DAG into `rest` immediately above `runHistory`. The current `rest` ends:

```elm
                        , Html.Lazy.lazy taskList detail.tasks
                        , Html.Lazy.lazy runHistory model.runMetricsByBuild
                        ]
```

Change it to:

```elm
                        , Html.Lazy.lazy taskList detail.tasks
                        , Html.Lazy.lazy2 StepDag.view ticket.state model.runMetricsByBuild
                        , Html.Lazy.lazy runHistory model.runMetricsByBuild
                        ]
```

(`ticket` is already bound in the `Just detail ->` branch of `content`; `ticket.state` is a `String` and `model.runMetricsByBuild` is reference-stable across a no-change refetch, so `lazy2` is safe — consistent with the existing lazy usage documented in `handleCallback`.)

- [ ] Run the page tests, expect PASS:

```
cd web/elm && elm-test tests/AgentTicketPageTests.elm
```

Expected: `TEST RUN PASSED`. (The pre-existing `runHistory` tests still pass — the flat rows are untouched.)

- [ ] Run the full suite:

```
cd web/elm && elm-test
```

Expected: `TEST RUN PASSED`.

- [ ] Commit:

```
git add web/elm/src/AgentTickets/AgentTicket.elm web/elm/tests/AgentTicketPageTests.elm
git commit -m "feat(web): render the step DAG on the ticket detail page"
```

---

## Task 6 — Rebuild and commit the Elm bundle (MANDATORY)

**Files**
- Modify (generated): `web/public/elm.min.js`

There is no local elm-build gate today (WF-2 adds one). The served UI is `elm.min.js`; skipping this leaves the deployed web on the OLD bundle — a known stale-bundle trap.

**Steps**

- [ ] Verify the toolchain is present:

```
elm --version && uglifyjs --version
```

Expected: `0.19.1` and a uglify-js version. If `uglifyjs` is missing: `npm i -g uglify-js`.

- [ ] Rebuild the bundle:

```
bash hack/build-web.sh
```

Expected final line: `built web/public/elm.min.js (<N> bytes)` with no `elm make` errors.

- [ ] Sanity-check the new symbol landed in the bundle (proves the source made it in):

```
grep -c "ticket-step-dag" web/public/elm.min.js
```

Expected: `1` or more (non-zero).

- [ ] Commit the regenerated bundle on its own:

```
git add web/public/elm.min.js
git commit -m "build(web): rebuild elm.min.js for the ticket step DAG"
```

---

## Self-Review

**Spec coverage (S-1 requirements):**
- "Render each attempt as a horizontal chain of step boxes" → `StepDag.attempts` (per-build) + `attemptView` horizontal `flex` row (Tasks 3, 4).
- "ticket → implement → gate:build → judge → push → review → merge" → `ticketBox` + agent boxes + `harvestBoxes` (gate/judge/push from results metadata) + `terminalBoxes` (review/merge from ticket state). The chain is data-derived (actual steps), not a hardcoded ideal — verified by the "gate: build / gate: test / judge 8/10 / push" composition test.
- "colored by state" → `AgentBadge.tone`/`toneColor`, reusing the existing status palette (single source, Task 1).
- "annotated with cost and duration" → `boxSublabel` (`$cost`, `Ns`/`Nm Ns`); gates carry `duration_seconds`, agent steps `wall_time_seconds`; harvest cost attributed to the judge box (documented).
- "click-through to the build/step" → every box and the attempt header link to `/builds/<id>` (`buildHref`). Step-level deep-link deferred (Open Decision 3).
- "recording-incomplete renders as a WARNING on a green box, NEVER red" → `boxDisplay` maps `NoOutput` (today) to `(GoodMuted, warn=True)`; L-1 coupling anchor documents the extension point for the amber token. Verified by the "no-output step … green (GoodMuted) with a warn" test.
- Part 1 server decision (compose vs. new endpoint) → justified from grounded reads; compose client-side, add no endpoint.

**Placeholder scan:** No `TODO`/`TBD`/`similar to`/"add error handling" left. The Task 3 `view` stub is explicitly a placeholder replaced in Task 4 (each shown in full). The Task 2 decoder shows both a wrong first draft and the exact final form to keep — the final block is the one to commit.

**Type consistency:**
- `RunMetric` gains exactly one field `results : StepResults`; all four existing test literals are enumerated for update (Task 2).
- `StepDag.view : String -> Dict Int (List Agent.RunMetric) -> Html msg` — generic `msg`, unified to the page's `Message` at the `Html.Lazy.lazy2` call site (Task 5).
- `AgentBadge.toneColor : Tone -> String` total over all 10 `Tone` constructors (Task 1 test asserts totality).
- `boxDisplay : Maybe AgentBadge.Status -> { tone, warn }` handles `NoOutput`, any other `Just`, and `Nothing`.

---

## Open Decisions

1. **DAG vs. flat run rows — consolidation (owner: web/UX + W-2 owner).** This plan ADDS the DAG above the existing flat `runHistory` rows to avoid colliding with W-2 (which reworks the run-row identity). That is momentarily redundant. **Recommendation:** once W-2 lands, remove the flat `runRow`/`runHistory` and let the DAG's `attemptHeader` (which already carries attempt-N · outcome badge · build link · $cost) own the section — the header was designed to be the W-2 row. Do it in the W-2 branch or a fast follow, not here, to keep this change regression-free.

2. **Harvest-step cost attribution (owner: web/UX).** The harvest metrics row has one `cost_usd`; the plan attributes it to the judge box (the judge LLM call is the priced work; gate/push are shell steps). **Recommendation:** keep judge-attribution; if a harvest row ever prices non-judge work, add a synthetic "harvest" grouping box instead of smearing. Low stakes — harvest cost is typically small.

3. **Step-level click-through (owner: web/UX).** Boxes link to `/builds/<id>` (proven, matches run rows). A per-box deep-link to the specific step would need a step anchor; `Routes.Highlight` today is `HighlightLine StepID Int` (line-based, not step-header), and it is unconfirmed that a metrics `plan_id` equals the build page's `StepID`. **Recommendation:** ship build-level links now; treat step-anchor deep-linking as a separate spike that first verifies the `plan_id` ↔ build-page `StepID` mapping.

4. **Server-side attempt-summary rollup for `/agent` + fly parity (owner: platform).** This plan composes the DAG client-side. If `/agent` (S-3) or `fly agent tickets show` later wants the same attempt→step structure, a server rollup would avoid re-deriving it per client. **Recommendation:** do NOT build it speculatively; if S-3/fly need it, add it then via the #36 six-touchpoint pattern, reusing this module's composition rules as the spec.

5. **Attempt-numbering ownership with W-2 (owner: whoever lands second).** Both this plan and W-2 number attempts 1..N by ascending build id. **Recommendation:** the second-to-land PR extracts one shared helper (in `AgentTicket.elm` or `StepDag.elm`) and deletes the duplicate; the `-- W-2 coupling` anchor marks the site.
