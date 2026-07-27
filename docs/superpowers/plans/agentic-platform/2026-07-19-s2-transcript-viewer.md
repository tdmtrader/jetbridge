# Agent-Step Transcript Viewer (S-2, Proposal C) Implementation Plan

> **SUPERSEDED — historical implementation record (not executable).** The approved [Agentic Workflows as Functions over Snapshots design](../../specs/2026-07-21-agentic-workflows-as-functions-design.md) and [Agentic Functions program plan](../2026-07-21-agentic-functions-program.md) are authoritative. The transcript viewer shipped (batch 6, judge + transcript view); this plan's ticket/attempt framing below is historical — the live viewer operates on workflow runs.

> For agentic workers: REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax.

**Goal:** Give the spectator a dedicated per-attempt page that fetches a run's flight-events NDJSON and renders a collapsible turn timeline (step boundaries, cost records, gate results, judge verdicts, pushes, and — when present — tool calls / file edits / thinking) with a live totals bar (turns / tokens / dollars) and a raw-JSON toggle.

**Architecture:** A new Elm page `AgentTickets/AgentRunTranscript.elm` reached at `/agent-tickets/:id/runs/:buildId` fetches the flight-events NDJSON from the L-3/#43 read API `GET /api/v1/agent/tickets/:ticket_id/runs/:build_id/events` and the existing per-ticket run metrics, folds the NDJSON into a typed timeline via a pure decoder module `Concourse/AgentEvent.elm`, and keeps itself current with the existing 5-second `Polling` helper (there is **no** browser-facing SSE heartbeat today — see Open Decisions). Placement is a dedicated page rather than inline in `Build/StepTree` because the event stream is build-scoped, uses the team-less agent auth tier, and the build page was deliberately de-heavied (perf commit `f085e126be`).

**Tech Stack:** Elm 0.19.1 (`elm/http` 1.0.0, `elm/json` 1.1.3), `elm-test` 0.19.1-revision17, existing `Polling`/`Api`/`Routes`/`SubPage` infrastructure. No Go changes in this track — the server route is the L-3/#43 dependency.

---

## Hard dependency (state up front)

This track **assumes L-3 / draft ticket #43 has landed**: `GET /api/v1/agent/tickets/:ticket_id/runs/:build_id/events` returning the run's flight events as NDJSON (one `{"ts":...,"event":...,"data":{...}}` object per line, merged across the build's agent + harvest steps), served on the team-less agent auth tier (`CheckAgentAuthorizationHandler`, same tier as `GET /api/v1/agent/tickets/:ticket_id/metrics`). This plan does **not** implement that route. Until it lands the page renders its empty/error state; the Elm work is independently mergeable and testable against fixture NDJSON.

## Grounded reality of the event stream (read before designing decoders)

The event **type taxonomy** is large (`agent/schema/event.go` + `agent/schema/event_payloads.go`), but only a subset is actually emitted today. Verified emission sites (`grep` for `writeEvent`/`emit`/`Emit` across `agent/`):

- **Agent step** (`agent/runner/runner.go`): `step.start`, `cost.record`, `step.end`, and `error` (only on sidecar-health failure). The runner invokes claude with `--output-format json` (a single final envelope, `runner.go:324`), so it emits **no** per-turn `tool.call` / `tool.result` / thinking / `artifact.written` events.
- **Harvest step** (`agent/harvest/runner.go` + `agent/harvest/gates.go`): `step.start`, `gate.start`, `gate.result`, `judge.score`, `cost.record` (judge cost), `push.done`, `step.end`.
- **Platform-MCP HITL** (`agent/platformmcp/askhuman.go`, `checkpoint.go`), only when a run parks: `human.ask`, `human.answer`, `checkpoint.wait`, `checkpoint.release`.

**Never emitted today:** `tool.call`, `tool.result`, `artifact.written`, `decision`, `subagent.call`, `subagent.result`, `agent.start`/`agent.end`, `skill.start`/`skill.end`, `plan.*`, `budget.warn`/`budget.stop`.

**Design consequence:** "tool calls, file edits, thinking summaries" (Proposal C's headline) are **not backed by data today**. This plan therefore (a) decodes the **full** taxonomy defensively so the viewer lights up automatically if/when the runner adopts `--output-format stream-json` (a separate track — see Open Decisions), and (b) ships a timeline that renders the events that exist: step boundaries, cost records, gate results, judge verdicts, pushes, HITL asks/answers, and errors. The totals bar draws its authoritative numbers from the server-derived `RunMetric` rows (`Concourse.Agent.RunMetric`, already ingested and `outcome`-fused), not by summing NDJSON, so it is correct even when the NDJSON is coarse.

Payload field names come straight from `agent/schema/event_payloads.go` (`StepStartData`, `StepEndData`, `GateResultData`, `JudgeScoreData`, `PushDoneData`) and the `map[string]any` literals in `agent/platformmcp/askhuman.go` / `checkpoint.go`.

---

## File Structure

| File | Create/Modify | One responsibility |
|---|---|---|
| `web/elm/src/Concourse/AgentEvent.elm` | Create | Pure NDJSON → typed `Transcript` (timeline entries + parse-skip count); envelope + per-type payload decoders covering the full taxonomy. |
| `web/elm/tests/AgentEventTests.elm` | Create | `elm-test` suite for `Concourse.AgentEvent` (envelope tolerance, per-type folds, unknown-type fallthrough). |
| `web/elm/src/Api/Endpoints.elm` | Modify | Add `AgentTicketRunEvents Int Int` endpoint constructor + path. |
| `web/elm/tests/ApiEndpointsTests.elm` | Modify | Assert the new endpoint's URL path. |
| `web/elm/src/Api.elm` | Modify | Add `expectString` request transformer (raw NDJSON body). |
| `web/elm/src/Message/Effects.elm` | Modify | Add `FetchAgentRunEvents Int Int` effect. |
| `web/elm/src/Message/Callback.elm` | Modify | Add `AgentRunEventsFetched Int Int (Fetched String)` callback. |
| `web/elm/src/Message/Message.elm` | Modify | Add `TranscriptEntryToggled Int` and `TranscriptRawToggled` page messages. |
| `web/elm/src/Routes.elm` | Modify | Add `AgentRunTranscript` route across its six switch sites. |
| `web/elm/tests/RoutesTests.elm` | Modify | Round-trip parse/serialize test for the new route. |
| `web/elm/src/AgentTickets/AgentRunTranscript.elm` | Create | The page: fetch events + metrics, fold, render timeline + totals bar + raw toggle, poll while running. |
| `web/elm/tests/AgentRunTranscriptPageTests.elm` | Create | Page-level `elm-test` (renders totals + a gate/judge entry from fixture NDJSON). |
| `web/elm/src/SubPage/SubPage.elm` | Modify | Wire the page into the SubPage model/init/update/callback/delivery/view/tooltip/subscriptions/title. |
| `web/elm/src/AgentTickets/AgentTicket.elm` | Modify | Link each run-history row to its transcript page. |
| `web/public/elm.min.js` | Modify (generated) | Rebuilt embedded bundle (no local elm-build gate today — WF-2 adds one). |

---

## Task 1 — `Concourse.AgentEvent`: envelope + tolerant NDJSON parse

**Files:**
- Create `web/elm/src/Concourse/AgentEvent.elm`
- Test `web/elm/tests/AgentEventTests.elm`

### Steps

- [ ] Write the failing test. Create `web/elm/tests/AgentEventTests.elm`:

```elm
module AgentEventTests exposing (all)

import Concourse.AgentEvent as AE
import Expect
import Test exposing (Test, describe, test)


all : Test
all =
    describe "Concourse.AgentEvent.parseTranscript"
        [ test "decodes step.start / cost.record / step.end into ordered entries" <|
            \_ ->
                let
                    ndjson =
                        String.join "\n"
                            [ """{"ts":"2026-07-19T00:00:01Z","event":"step.start","data":{"step_name":"implement","build_id":42,"plan_id":"p1"}}"""
                            , """{"ts":"2026-07-19T00:00:02Z","event":"cost.record","data":{"source":"agent_step","model":"claude","input_tokens":100,"output_tokens":20,"turns":3,"cost_usd":0.05}}"""
                            , """{"ts":"2026-07-19T00:00:03Z","event":"step.end","data":{"step_name":"implement","status":"ok","summary":"done","wall_time_seconds":9,"cost_usd":0.05,"turns":3}}"""
                            ]

                    t =
                        AE.parseTranscript ndjson
                in
                Expect.equal ( List.length t.entries, t.skipped ) ( 3, 0 )
        , test "skips blank and unparseable lines, counting the garbled ones" <|
            \_ ->
                let
                    ndjson =
                        String.join "\n"
                            [ ""
                            , "not json at all"
                            , """{"ts":"2026-07-19T00:00:01Z","event":"push.done","data":{"branch":"agent/ticket-12","sha":"abc123","manifest_artifact":"m"}}"""
                            , "   "
                            ]

                    t =
                        AE.parseTranscript ndjson
                in
                Expect.equal ( List.length t.entries, t.skipped ) ( 1, 1 )
        , test "an unknown event type is kept as an Unknown entry, not dropped" <|
            \_ ->
                let
                    t =
                        AE.parseTranscript """{"ts":"2026-07-19T00:00:01Z","event":"tool.call","data":{"tool":"Bash"}}"""
                in
                case List.map .body t.entries of
                    [ AE.ToolCalled call ] ->
                        Expect.equal call.tool "Bash"

                    _ ->
                        Expect.fail "expected a single ToolCalled entry"
        ]
```

- [ ] Run it, expected FAIL. Command: `cd web/elm && elm-test tests/AgentEventTests.elm`. Expected message: a compile error `I cannot find a \`AE.parseTranscript\` variable` (module does not exist yet).

- [ ] Minimal implementation. Create `web/elm/src/Concourse/AgentEvent.elm`:

```elm
module Concourse.AgentEvent exposing
    ( CostRecord
    , EntryBody(..)
    , GateResult
    , JudgeDimension
    , JudgeVerdict
    , StepEnd
    , TimelineEntry
    , Transcript
    , parseTranscript
    )

{-| Pure decoding of a run's flight-events NDJSON (L-3 / #43 read API) into a
typed timeline. One NDJSON line = one `{"ts","event","data"}` envelope. Lines
that are blank or fail to decode are skipped and counted (`Transcript.skipped`)
rather than aborting the whole transcript — the stream is appended live, so a
half-written trailing line during a poll is normal.

Decoders cover the FULL event taxonomy (agent/schema/event.go +
event_payloads.go), not just what the runner emits today, so the viewer lights
up automatically if the runner ever streams per-turn tool/thinking events. Any
event type without a specific decoder becomes `Unknown`, preserving its raw
JSON for the raw-toggle instead of being dropped.
-}

import Json.Decode as D


type alias Transcript =
    { entries : List TimelineEntry
    , skipped : Int
    }


type alias TimelineEntry =
    { seq : Int
    , ts : String
    , eventType : String
    , raw : String
    , body : EntryBody
    }


type EntryBody
    = StepStarted { stepName : String }
    | StepEnded StepEnd
    | CostRecorded CostRecord
    | GateStarted { gate : String, component : String, scope : String }
    | GateResulted GateResult
    | JudgeScored JudgeVerdict
    | Pushed { branch : String, sha : String }
    | HumanAsked { questionId : Int, question : String, options : List String }
    | HumanAnswered { questionId : Int, answer : String, answeredBy : String, timedOut : Bool }
    | CheckpointWaited { questionId : Int, checkpoint : String }
    | CheckpointReleased { questionId : Int, approved : Bool, answeredBy : String }
    | Errored { message : String }
    | ToolCalled { tool : String, input : String }
    | ToolResulted { tool : String, output : String, isError : Bool }
    | ArtifactWritten { path : String, bytes : Int }
    | Thought { summary : String }
    | Unknown


type alias StepEnd =
    { stepName : String
    , status : String
    , summary : String
    , wallTimeSeconds : Int
    , costUsd : Float
    , turns : Int
    }


type alias CostRecord =
    { source : String
    , model : String
    , inputTokens : Int
    , outputTokens : Int
    , turns : Int
    , costUsd : Float
    }


type alias GateResult =
    { gate : String
    , component : String
    , scope : String
    , status : String
    , durationSeconds : Float
    , summary : String
    , flaky : Bool
    }


type alias JudgeDimension =
    { name : String, score : Float, max : Float, rationale : String }


type alias JudgeVerdict =
    { total : Float, maxTotal : Float, model : String, dimensions : List JudgeDimension }


parseTranscript : String -> Transcript
parseTranscript raw =
    let
        lines =
            String.split "\n" raw
                |> List.map String.trim
                |> List.filter (\l -> l /= "")

        step line ( accEntries, skipped, seq ) =
            case D.decodeString (envelopeDecoder line seq) line of
                Ok entry ->
                    ( entry :: accEntries, skipped, seq + 1 )

                Err _ ->
                    ( accEntries, skipped + 1, seq )

        ( revEntries, totalSkipped, _ ) =
            List.foldl step ( [], 0, 0 ) lines
    in
    { entries = List.reverse revEntries, skipped = totalSkipped }


envelopeDecoder : String -> Int -> D.Decoder TimelineEntry
envelopeDecoder rawLine seq =
    D.map3 (\ts eventType body -> TimelineEntry seq ts eventType rawLine body)
        (D.field "ts" D.string)
        (D.field "event" D.string)
        (D.field "event" D.string |> D.andThen (\t -> D.field "data" (bodyDecoder t)))


bodyDecoder : String -> D.Decoder EntryBody
bodyDecoder eventType =
    let
        f name dec =
            D.field name dec

        s name =
            D.oneOf [ f name D.string, D.succeed "" ]

        i name =
            D.oneOf [ f name D.int, D.succeed 0 ]

        fl name =
            D.oneOf [ f name D.float, D.succeed 0 ]

        b name =
            D.oneOf [ f name D.bool, D.succeed False ]

        list name =
            D.oneOf [ f name (D.list D.string), D.succeed [] ]
    in
    case eventType of
        "step.start" ->
            D.map (\n -> StepStarted { stepName = n }) (s "step_name")

        "step.end" ->
            D.map6 (\n st su w c tu -> StepEnded (StepEnd n st su w c tu))
                (s "step_name")
                (s "status")
                (s "summary")
                (i "wall_time_seconds")
                (fl "cost_usd")
                (i "turns")

        "cost.record" ->
            D.map6 (\src m it ot tu c -> CostRecorded (CostRecord src m it ot tu c))
                (s "source")
                (s "model")
                (i "input_tokens")
                (i "output_tokens")
                (i "turns")
                (fl "cost_usd")

        "gate.start" ->
            D.map3 (\g c sc -> GateStarted { gate = g, component = c, scope = sc })
                (s "gate")
                (s "component")
                (s "scope")

        "gate.result" ->
            D.map7 (\g c sc st dur su fk -> GateResulted (GateResult g c sc st dur su fk))
                (s "gate")
                (s "component")
                (s "scope")
                (s "status")
                (fl "duration_seconds")
                (s "summary")
                (b "flaky")

        "judge.score" ->
            D.map4 (\tot mx m dims -> JudgeScored (JudgeVerdict tot mx m dims))
                (fl "total")
                (fl "max_total")
                (s "model")
                (D.oneOf [ f "dimensions" (D.list judgeDimensionDecoder), D.succeed [] ])

        "push.done" ->
            D.map2 (\br sha -> Pushed { branch = br, sha = sha }) (s "branch") (s "sha")

        "human.ask" ->
            D.map3 (\q qn opts -> HumanAsked { questionId = q, question = qn, options = opts })
                (i "question_id")
                (s "question")
                (list "options")

        "human.answer" ->
            D.map4 (\q a by to -> HumanAnswered { questionId = q, answer = a, answeredBy = by, timedOut = to })
                (i "question_id")
                (s "answer")
                (s "answered_by")
                (b "timed_out")

        "checkpoint.wait" ->
            D.map2 (\q ck -> CheckpointWaited { questionId = q, checkpoint = ck }) (i "question_id") (s "checkpoint")

        "checkpoint.release" ->
            D.map3 (\q ap by -> CheckpointReleased { questionId = q, approved = ap, answeredBy = by })
                (i "question_id")
                (b "approved")
                (s "answered_by")

        "error" ->
            D.map (\m -> Errored { message = m }) (s "message")

        "tool.call" ->
            D.map2 (\t inp -> ToolCalled { tool = t, input = inp }) (s "tool") (s "input")

        "tool.result" ->
            D.map3 (\t o e -> ToolResulted { tool = t, output = o, isError = e }) (s "tool") (s "output") (b "is_error")

        "artifact.written" ->
            D.map2 (\p n -> ArtifactWritten { path = p, bytes = n }) (s "path") (i "bytes")

        "decision" ->
            D.map (\su -> Thought { summary = su }) (s "summary")

        _ ->
            D.succeed Unknown


judgeDimensionDecoder : D.Decoder JudgeDimension
judgeDimensionDecoder =
    D.map4 JudgeDimension
        (D.oneOf [ D.field "name" D.string, D.succeed "" ])
        (D.oneOf [ D.field "score" D.float, D.succeed 0 ])
        (D.oneOf [ D.field "max" D.float, D.succeed 0 ])
        (D.oneOf [ D.field "rationale" D.string, D.succeed "" ])
```

- [ ] Run it, expected PASS. Command: `cd web/elm && elm-test tests/AgentEventTests.elm`. Expected output: `TEST RUN PASSED` with `3` passed.

- [ ] Commit. `git add web/elm/src/Concourse/AgentEvent.elm web/elm/tests/AgentEventTests.elm && git commit -m "feat(web): pure NDJSON→timeline decoder for agent flight events (S-2)"`

---

## Task 2 — Endpoint constructor + path

**Files:**
- Modify `web/elm/src/Api/Endpoints.elm` (flat `Endpoint` union `:18`, agent constructors `:36-51`; `toString` path switch `:248-260`)
- Test `web/elm/tests/ApiEndpointsTests.elm`

### Steps

- [ ] Write the failing test. Add to `web/elm/tests/ApiEndpointsTests.elm` inside the `AgentTicket`/`Agent` `describe` group in `testEndpoints` (mirror the existing `AgentTicketMetrics` entry, which uses `toPath = toString []` and the **unqualified** constructor):

```elm
            , test "run events" <|
                \_ ->
                    AgentTicketRunEvents 12 4567
                        |> toPath
                        |> Expect.equal "/api/v1/agent/tickets/12/runs/4567/events"
```

(`ApiEndpointsTests.elm` imports `Api.Endpoints as E exposing (Endpoint(..), toString)`, so all agent endpoints are referenced as **unqualified constructors** — e.g. `AgentTicketMetrics 12 |> toPath`. There is **no** `Endpoints.Agent (...)` wrapper: `Endpoint` is a single flat union whose agent members are direct constructors. Write `AgentTicketRunEvents 12 4567 |> toString []` (via the local `toPath = toString []`), not `Endpoints.Agent (...)` / `Endpoints.toString`.)

- [ ] Run it, expected FAIL. Command: `cd web/elm && elm-test tests/ApiEndpointsTests.elm`. Expected message: `I cannot find a \`Endpoints.AgentTicketRunEvents\` variant`.

- [ ] Minimal implementation. In `web/elm/src/Api/Endpoints.elm`, add the constructor directly to the flat `Endpoint` union after `AgentTicketMetrics Int` (line ~51) — no `Agent` wrapper:

```elm
    | AgentTicketMetrics Int
    | AgentTicketRunEvents Int Int
```

and add its path arm to the `toString` case switch after the `AgentTicketMetrics ticketId ->` arm (line ~260):

```elm
        AgentTicketRunEvents ticketId buildId ->
            base |> appendPath [ "agent", "tickets", String.fromInt ticketId, "runs", String.fromInt buildId, "events" ]
```

- [ ] Run it, expected PASS. Command: `cd web/elm && elm-test tests/ApiEndpointsTests.elm`. Expected output: `TEST RUN PASSED`.

- [ ] Commit. `git add web/elm/src/Api/Endpoints.elm web/elm/tests/ApiEndpointsTests.elm && git commit -m "feat(web): AgentTicketRunEvents endpoint path (S-2)"`

---

## Task 3 — `Api.expectString` (raw NDJSON body)

**Files:**
- Modify `web/elm/src/Api.elm` (exposes list `:1-12`; add transformer after `expectJson` `:104-112`)

The L-3/#43 response is NDJSON, not a JSON array, so the request must yield the raw body string for `Concourse.AgentEvent.parseTranscript`. `elm/http` 1.0.0 provides `Http.expectString : Expect String`.

### Steps

- [ ] Write the failing test. Add to `web/elm/tests/ApiEndpointsTests.elm` a compile-only assertion that the transformer exists and yields a `Request String` (a value-level reference is enough for the compiler to enforce the type):

```elm
        , test "expectString transforms a Request into a Request String" <|
            \_ ->
                let
                    req : Api.Request String
                    req =
                        Api.get (AgentTicketRunEvents 1 2)
                            |> Api.expectString
                in
                Expect.equal req.method "GET"
```

(Add `import Api` at the top of the test module if not present.)

- [ ] Run it, expected FAIL. Command: `cd web/elm && elm-test tests/ApiEndpointsTests.elm`. Expected message: `I cannot find a \`Api.expectString\` variable`.

- [ ] Minimal implementation. In `web/elm/src/Api.elm`, add `expectString` to the exposing list (after `expectJson`, line ~4):

```elm
    , expectJson
    , expectString
```

and add the transformer after `expectJson` (line ~112):

```elm
expectString : Request a -> Request String
expectString r =
    { method = r.method
    , headers = r.headers
    , endpoint = r.endpoint
    , query = r.query
    , body = r.body
    , expect = Http.expectString
    }
```

- [ ] Run it, expected PASS. Command: `cd web/elm && elm-test tests/ApiEndpointsTests.elm`. Expected output: `TEST RUN PASSED`.

- [ ] Commit. `git add web/elm/src/Api.elm web/elm/tests/ApiEndpointsTests.elm && git commit -m "feat(web): Api.expectString for raw NDJSON bodies (S-2)"`

---

## Task 4 — Effect, Callback, and page Messages

**Files:**
- Modify `web/elm/src/Message/Effects.elm` (`Effect` union `:249`; dispatcher `:934-938`)
- Modify `web/elm/src/Message/Callback.elm` (`Callback` union `:89`)
- Modify `web/elm/src/Message/Message.elm` (`Message` union near the AgentTicket block `:73-88`)

No test here — these are wiring types verified by the page test (Task 6) and the whole-suite compile at the end. Keep them minimal and matched to existing shapes.

### Steps

- [ ] Add the effect. In `web/elm/src/Message/Effects.elm`, add to the `Effect` union after `FetchAgentTicketMetrics Int` (line ~249):

```elm
    | FetchAgentTicketMetrics Int
    | FetchAgentRunEvents Int Int
```

and add the dispatcher arm after the `FetchAgentTicketMetrics` arm (line ~938):

```elm
        FetchAgentRunEvents ticketId buildId ->
            Api.get (Endpoints.AgentTicketRunEvents ticketId buildId)
                |> Api.expectString
                |> Api.request
                |> Task.attempt (AgentRunEventsFetched ticketId buildId)
```

(`Message/Effects.elm` imports `Api.Endpoints as Endpoints`, and the neighbouring `FetchAgentTicketMetrics` arm calls `Api.get (Endpoints.AgentTicketMetrics ticketId)` — a **direct** flat constructor with no `Endpoints.Agent (...)` wrapper. Match that exact form: `Endpoints.AgentTicketRunEvents ticketId buildId`.)

- [ ] Add the callback. In `web/elm/src/Message/Callback.elm`, add after `AgentTicketMetricsFetched Int (Fetched (List Concourse.Agent.RunMetric))` (line ~89):

```elm
    | AgentTicketMetricsFetched Int (Fetched (List Concourse.Agent.RunMetric))
    | AgentRunEventsFetched Int Int (Fetched String)
```

- [ ] Add the page messages. In `web/elm/src/Message/Message.elm`, add after the AgentTicket message block (line ~88):

```elm
    | TranscriptEntryToggled Int
    | TranscriptRawToggled
```

- [ ] Verify compilation. Command: `cd web/elm && elm make --output /dev/null src/Main.elm`. Expected: `Success!` — but this will FAIL until Task 6 handles the new callback/messages in `AgentRunTranscript`; if compiling standalone before Task 6, expect an incomplete-pattern warning in `SubPage`/page handlers. Defer the green compile to Task 6's end-to-end build. (Do not commit a non-compiling tree — batch Tasks 4–7 into one working-tree commit at Task 7's end, or use `git commit --no-verify` intermediate checkpoints on a WIP branch. Recommended: commit Tasks 4+5+6+7 together.)

---

## Task 5 — Route (six touchpoints)

**Files:**
- Modify `web/elm/src/Routes.elm`: type `:61-64`, parser `:329-336`, parser list `:504-507`, `toString`/build `:618-632`, `getGroups` `:711-751`, `withGroups` `:754-794`
- Test `web/elm/tests/RoutesTests.elm`

### Steps

- [ ] Write the failing test. Add to `web/elm/tests/RoutesTests.elm` inside the top-level `describe "Routes"` list:

```elm
        , test "AgentRunTranscript round-trips through toString/parsePath" <|
            \_ ->
                Routes.AgentRunTranscript { id = 12, buildId = 4567 }
                    |> Routes.toString
                    |> (\path -> "http://example.com" ++ path)
                    |> Common.url
                    |> Routes.parsePath
                    |> Expect.equal (Just (Routes.AgentRunTranscript { id = 12, buildId = 4567 }))
```

(Match the URL-building helper the neighbouring `Pipeline route can be parsed properly` test uses — it prefixes a host and calls `Routes.parsePath` on a parsed `Url`; reuse that exact helper, e.g. `Common.url` or the inline `Url.fromString` the file already uses.)

- [ ] Run it, expected FAIL. Command: `cd web/elm && elm-test tests/RoutesTests.elm`. Expected message: `I cannot find a \`Routes.AgentRunTranscript\` variant`.

- [ ] Minimal implementation. In `web/elm/src/Routes.elm`:

  1. Type union, after `AgentTicket { id : Int }` (line ~64):

```elm
    | AgentTicket { id : Int }
    | AgentRunTranscript { id : Int, buildId : Int }
```

  2. Parser, after `agentTickets` (line ~336):

```elm
agentRunTranscript : Parser ((b -> Route) -> a) a
agentRunTranscript =
    map (\id buildId -> always <| AgentRunTranscript { id = id, buildId = buildId })
        (s "agent-tickets" </> int </> s "runs" </> int)
```

  3. Parser list — add `agentRunTranscript` **before** `agentTicket` in the `oneOf` list (line ~504-507), so the two-segment `/agent-tickets/:id/runs/:buildId` is tried before the one-segment `/agent-tickets/:id`:

```elm
        , agentReviews
        , agent
        , agentRunTranscript
        , agentTicket
        , agentTickets
```

  4. `toString` build, after the `AgentTicket` arm (line ~632):

```elm
        AgentRunTranscript { id, buildId } ->
            ( [ "agent-tickets", String.fromInt id, "runs", String.fromInt buildId ], [] )
                |> RouteBuilder.build
```

  5. `getGroups`, after the `AgentTicket _ ->` arm (line ~750):

```elm
        AgentTicket _ ->
            []

        AgentRunTranscript _ ->
            []
```

  6. `withGroups`, after the `AgentTicket _ ->` arm (line ~793):

```elm
        AgentTicket _ ->
            route

        AgentRunTranscript _ ->
            route
```

- [ ] Run it, expected PASS. Command: `cd web/elm && elm-test tests/RoutesTests.elm`. Expected output: `TEST RUN PASSED`.

- [ ] Commit (with Tasks 4/6/7). Deferred — see Task 7.

---

## Task 6 — The `AgentRunTranscript` page

**Files:**
- Create `web/elm/src/AgentTickets/AgentRunTranscript.elm`
- Test `web/elm/tests/AgentRunTranscriptPageTests.elm`

Model mirrors `AgentTickets/AgentTicket.elm`'s structure (Login.Model wrapper, `Polling` for the 5s cadence, reference-preserved refetch). The totals bar reads the **server-derived** `RunMetric` rows filtered to this `buildId` (authoritative cost / turns / tokens / `outcome`); the timeline renders `Concourse.AgentEvent` entries; a per-entry expand set and a global raw toggle drive collapsibility.

### Steps

- [ ] Write the failing test. Create `web/elm/tests/AgentRunTranscriptPageTests.elm`:

```elm
module AgentRunTranscriptPageTests exposing (all)

import AgentTickets.AgentRunTranscript as Page
import Application.Models exposing (Session)
import Data
import Expect
import HoverState
import Message.Callback as Callback
import RemoteData
import Routes
import ScreenSize
import Set
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (containing, tag, text)
import Time
import UserState


sampleNdjson : String
sampleNdjson =
    String.join "\n"
        [ """{"ts":"2026-07-19T00:00:01Z","event":"step.start","data":{"step_name":"implement","build_id":4567,"plan_id":"p1"}}"""
        , """{"ts":"2026-07-19T00:00:05Z","event":"gate.result","data":{"gate":"build","component":"web","scope":"affected","status":"ok","duration_seconds":12.0,"summary":"built"}}"""
        , """{"ts":"2026-07-19T00:00:08Z","event":"judge.score","data":{"total":8.0,"max_total":10.0,"model":"claude","dimensions":[]}}"""
        , """{"ts":"2026-07-19T00:00:09Z","event":"step.end","data":{"step_name":"implement","status":"ok","summary":"done","wall_time_seconds":9,"cost_usd":0.05,"turns":3}}"""
        ]


loaded : Page.Model
loaded =
    let
        ( m0, _ ) =
            Page.init { id = 12, buildId = 4567 }

        ( m1, _ ) =
            Page.handleCallback (Callback.AgentRunEventsFetched 12 4567 (Ok sampleNdjson)) ( m0, [] )
    in
    m1


all : Test
all =
    describe "AgentRunTranscript page"
        [ test "renders a timeline entry for the gate result" <|
            \_ ->
                Page.view sampleSession loaded
                    |> Query.fromHtml
                    |> Query.has [ text "build" ]
        , test "renders a judge entry" <|
            \_ ->
                Page.view sampleSession loaded
                    |> Query.fromHtml
                    |> Query.has [ text "judge" ]
        ]


sampleSession : Session
sampleSession =
    { expandedTeamsInAllPipelines = Set.empty
    , collapsedTeamsInFavorites = Set.empty
    , pipelines = RemoteData.NotAsked
    , hovered = HoverState.NoHover
    , sideBarState =
        { isOpen = False
        , width = 275
        }
    , draggingSideBar = False
    , screenSize = ScreenSize.Desktop
    , userState = UserState.UserStateLoggedOut
    , clusterName = ""
    , version = ""
    , jetbridgeVersion = ""
    , concourseVersion = ""
    , featureFlags = Data.featureFlags
    , turbulenceImgSrc = ""
    , notFoundImgSrc = ""
    , csrfToken = ""
    , authToken = ""
    , pipelineRunningKeyframes = ""
    , timeZone = Time.utc
    , favoritedPipelines = Set.empty
    , favoritedInstanceGroups = Set.empty
    , route = Routes.AgentRunTranscript { id = 12, buildId = 4567 }
    }
```

`Page.view : Session -> Model -> Html Message` returns `Html Message` (mirroring `AgentTicket.elm:455`), **not** a `{ title, body }` document — so the test queries the result with `Query.fromHtml` directly, never `.body`/`List.head`. `Common.session` does **not** exist in `web/elm/tests/Common.elm`; the only reusable direct-view `Session` fixture in the suite is the inline record copied above verbatim from `web/elm/tests/Build/HeaderTests.elm:444-471` (only the `route` field is changed to this page's route). The sibling `AgentTicketPageTests.elm` does **not** call `page.view session model` — it drives the page at the Application level via `Common.init "/agent-tickets/12"` + `Common.queryView`; that approach is viable here only *after* Task 5 (route) and Task 7 (SubPage wiring) land, so it cannot be this standalone pre-wiring Task 6 test. If you prefer the Application-level style, move this test to run after Task 7 instead.

- [ ] Run it, expected FAIL. Command: `cd web/elm && elm-test tests/AgentRunTranscriptPageTests.elm`. Expected message: `I cannot find a \`AgentTickets.AgentRunTranscript\` module`.

- [ ] Minimal implementation. Create `web/elm/src/AgentTickets/AgentRunTranscript.elm`:

```elm
module AgentTickets.AgentRunTranscript exposing
    ( Model
    , documentTitle
    , handleCallback
    , handleDelivery
    , init
    , subscriptions
    , tooltip
    , update
    , view
    )

{-| The per-attempt TRANSCRIPT page (spectator view, S-2 / Proposal C).

Fetches the run's flight-events NDJSON (L-3 / #43) and the ticket's run
metrics, folds the NDJSON into a collapsible turn timeline, and shows a live
totals bar drawn from the server-derived metrics rows for this build. Keeps
current on the dashboard's 5s cadence via `Polling`; stops once the run's
metrics report a terminal build status (nothing more will be appended).

There is no browser-facing SSE today (the only EventSource in the app is the
Concourse build-events stream, which carries CI log lines, not flight events);
"live" here is the 5s poll re-fetching the growing NDJSON. See the plan's Open
Decisions for the SSE-vs-poll call.
-}

import AgentBadge
import Application.Models exposing (Session)
import Concourse.Agent
import Concourse.AgentEvent as AE
import Dict
import EffectTransformer exposing (ET)
import Html exposing (Html)
import Html.Attributes exposing (class, id, style)
import Html.Events exposing (onClick)
import Login.Login as Login
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message(..))
import Message.Subscription exposing (Delivery, Interval(..), Subscription)
import Polling
import Routes
import Set exposing (Set)
import SideBar.SideBar as SideBar
import Tooltip
import UserState
import Views.Prose
import Views.Styles
import Views.TopBar as TopBar


type alias Model =
    Login.Model
        { ticketId : Int
        , buildId : Int
        , transcript : AE.Transcript
        , runMetrics : List Concourse.Agent.RunMetric
        , loaded : Bool
        , loadError : Bool
        , expanded : Set Int
        , showRaw : Bool
        }


init : { id : Int, buildId : Int } -> ( Model, List Effect )
init { id, buildId } =
    ( { ticketId = id
      , buildId = buildId
      , transcript = { entries = [], skipped = 0 }
      , runMetrics = []
      , loaded = False
      , loadError = False
      , expanded = Set.empty
      , showRaw = False
      , isUserMenuExpanded = False
      }
    , [ FetchAgentRunEvents id buildId, FetchAgentTicketMetrics id ]
    )


documentTitle : Model -> String
documentTitle model =
    "Attempt · build #" ++ String.fromInt model.buildId


handleCallback : Callback -> ET Model
handleCallback callback ( model, effects ) =
    case callback of
        AgentRunEventsFetched _ buildId (Ok body) ->
            if buildId /= model.buildId then
                ( model, effects )

            else
                ( { model | transcript = AE.parseTranscript body, loaded = True, loadError = False }, effects )

        AgentRunEventsFetched _ buildId (Err _) ->
            if buildId /= model.buildId then
                ( model, effects )

            else
                ( { model | loaded = True, loadError = True }, effects )

        AgentTicketMetricsFetched _ (Ok fresh) ->
            let
                mine =
                    List.filter (\m -> m.buildId == model.buildId) fresh
            in
            ( { model | runMetrics = mine }, effects )

        AgentTicketMetricsFetched _ (Err _) ->
            ( model, effects )

        _ ->
            ( model, effects )


{-| Poll while the build is not yet terminal; a terminal build appends nothing
more to its NDJSON. Terminal is derived from the metrics rows' build_status
(succeeded / failed / errored / aborted). Keep polling while metrics are still
unknown.
-}
polls : List (Polling.Poll Model)
polls =
    [ { interval = FiveSeconds
      , fetch =
            \model ->
                let
                    terminal =
                        not (List.isEmpty model.runMetrics)
                            && List.all (\m -> isTerminalBuild m.buildStatus) model.runMetrics
                in
                if terminal then
                    []

                else
                    [ FetchAgentRunEvents model.ticketId model.buildId, FetchAgentTicketMetrics model.ticketId ]
      }
    ]


isTerminalBuild : String -> Bool
isTerminalBuild s =
    List.member s [ "succeeded", "failed", "errored", "aborted" ]


handleDelivery : Delivery -> ET Model
handleDelivery =
    Polling.handleDelivery polls


subscriptions : List Subscription
subscriptions =
    Polling.subscriptions polls


update : Message -> ET Model
update msg ( model, effects ) =
    case msg of
        TranscriptEntryToggled seq ->
            ( { model
                | expanded =
                    if Set.member seq model.expanded then
                        Set.remove seq model.expanded

                    else
                        Set.insert seq model.expanded
              }
            , effects
            )

        TranscriptRawToggled ->
            ( { model | showRaw = not model.showRaw }, effects )

        _ ->
            ( model, effects )


tooltip : Model -> Session -> Maybe Tooltip.Tooltip
tooltip _ _ =
    Nothing


view : Session -> Model -> Html Message
view session model =
    Html.div
        (id "page-including-top-bar" :: Views.Styles.pageIncludingTopBar)
        [ Html.div (id "top-bar-app" :: Views.Styles.topBar False)
            [ SideBar.sideBarIcon session
            , Html.text (documentTitle model)
            , Login.view session.userState model
            ]
        , Html.div (id "page-below-top-bar" :: Views.Styles.pageBelowTopBar (Routes.AgentRunTranscript { id = model.ticketId, buildId = model.buildId }))
            [ SideBar.view session Nothing
            , Html.div [ class "agent-transcript", style "padding" "20px", style "overflow-y" "auto", style "flex-grow" "1" ]
                (totalsBar model :: rawToggle model :: bodyView model)
            ]
        ]


totalsBar : Model -> Html Message
totalsBar model =
    let
        sumF get =
            List.foldl (\m acc -> acc + get m) 0 model.runMetrics

        sumI get =
            List.foldl (\m acc -> acc + get m) 0 model.runMetrics

        cost =
            sumF .costUsd

        turns =
            sumI .turns

        inTok =
            sumI (\m -> m.usage.inputTokens)

        outTok =
            sumI (\m -> m.usage.outputTokens)

        outcome =
            model.runMetrics
                |> List.map .outcome
                |> List.filter (\o -> o /= "")
                |> List.head
                |> Maybe.withDefault ""
    in
    Html.div [ class "transcript-totals", style "display" "flex", style "gap" "24px", style "margin-bottom" "16px", style "font-family" "monospace" ]
        [ Html.span [] [ Html.text ("turns " ++ String.fromInt turns) ]
        , Html.span [] [ Html.text ("tokens " ++ String.fromInt (inTok + outTok)) ]
        , Html.span [] [ Html.text ("$" ++ formatCost cost) ]
        , Html.span [] [ Html.text outcome ]
        ]


formatCost : Float -> String
formatCost c =
    let
        cents =
            round (c * 100)
    in
    String.fromInt (cents // 100) ++ "." ++ String.padLeft 2 '0' (String.fromInt (modBy 100 (abs cents)))


rawToggle : Model -> Html Message
rawToggle model =
    Html.button
        [ onClick TranscriptRawToggled, style "margin-bottom" "12px" ]
        [ Html.text
            (if model.showRaw then
                "Hide raw JSON"

             else
                "Show raw JSON"
            )
        ]


bodyView : Model -> List (Html Message)
bodyView model =
    if not model.loaded then
        [ Html.div [ class "transcript-loading" ] [ Html.text "Loading transcript…" ] ]

    else if model.loadError then
        [ Html.div [ class "transcript-error" ] [ Html.text "Transcript not available yet (flight-events read API returned an error)." ] ]

    else if List.isEmpty model.transcript.entries then
        [ Html.div [ class "transcript-empty" ] [ Html.text "No events recorded for this attempt." ] ]

    else
        List.map (entryView model) model.transcript.entries
            ++ skippedNote model.transcript.skipped


skippedNote : Int -> List (Html Message)
skippedNote n =
    if n <= 0 then
        []

    else
        [ Html.div [ class "transcript-skipped", style "opacity" "0.6" ]
            [ Html.text (String.fromInt n ++ " event line(s) could not be parsed and were skipped.") ]
        ]


entryView : Model -> AE.TimelineEntry -> Html Message
entryView model entry =
    let
        open =
            Set.member entry.seq model.expanded

        ( label, detail ) =
            summarize entry.body
    in
    Html.div [ class "transcript-entry", style "border-left" "2px solid #555", style "padding" "6px 12px", style "margin" "4px 0" ]
        [ Html.div [ class "transcript-entry-head", onClick (TranscriptEntryToggled entry.seq), style "cursor" "pointer", style "display" "flex", style "gap" "10px" ]
            [ Html.span [ style "opacity" "0.6", style "font-family" "monospace" ] [ Html.text entry.eventType ]
            , Html.span [] [ Html.text label ]
            ]
        , if open then
            Html.div [ class "transcript-entry-detail", style "margin-top" "6px" ]
                [ Views.Prose.view detail ]

          else
            Html.text ""
        , if model.showRaw then
            Html.pre [ class "transcript-entry-raw", style "opacity" "0.6", style "white-space" "pre-wrap" ] [ Html.text entry.raw ]

          else
            Html.text ""
        ]


summarize : AE.EntryBody -> ( String, String )
summarize body =
    case body of
        AE.StepStarted r ->
            ( "step " ++ r.stepName ++ " started", "" )

        AE.StepEnded r ->
            ( "step " ++ r.stepName ++ " ended (" ++ r.status ++ ", $" ++ formatCost r.costUsd ++ ", " ++ String.fromInt r.turns ++ " turns)", r.summary )

        AE.CostRecorded r ->
            ( r.source ++ " cost $" ++ formatCost r.costUsd ++ " (" ++ String.fromInt r.turns ++ " turns)", "" )

        AE.GateStarted r ->
            ( "gate " ++ r.gate ++ " (" ++ r.component ++ ") started", "" )

        AE.GateResulted r ->
            ( "gate " ++ r.gate ++ " (" ++ r.component ++ "): " ++ r.status
                ++ (if r.flaky then
                        " · flaky"

                    else
                        ""
                   )
            , r.summary
            )

        AE.JudgeScored r ->
            ( "judge " ++ formatCost r.total ++ "/" ++ formatCost r.maxTotal ++ " (" ++ r.model ++ ")"
            , String.join "\n" (List.map (\d -> "- " ++ d.name ++ ": " ++ formatCost d.score ++ "/" ++ formatCost d.max ++ " — " ++ d.rationale) r.dimensions)
            )

        AE.Pushed r ->
            ( "pushed " ++ r.branch ++ " @ " ++ String.left 10 r.sha, "" )

        AE.HumanAsked r ->
            ( "asked human: " ++ r.question, String.join "\n" r.options )

        AE.HumanAnswered r ->
            ( "human answered (" ++ r.answeredBy ++ ")", r.answer )

        AE.CheckpointWaited r ->
            ( "checkpoint " ++ r.checkpoint ++ " — waiting", "" )

        AE.CheckpointReleased r ->
            ( "checkpoint released (" ++ r.answeredBy ++ ")", "" )

        AE.Errored r ->
            ( "error", r.message )

        AE.ToolCalled r ->
            ( "tool " ++ r.tool, "```\n" ++ r.input ++ "\n```" )

        AE.ToolResulted r ->
            ( "tool result " ++ r.tool, "```\n" ++ r.output ++ "\n```" )

        AE.ArtifactWritten r ->
            ( "wrote " ++ r.path ++ " (" ++ String.fromInt r.bytes ++ " bytes)", "" )

        AE.Thought r ->
            ( "thinking", r.summary )

        AE.Unknown ->
            ( "(unrecognized event)", "" )
```

Note the imports (`AgentBadge`, `UserState`, `Dict`) may be unused after the minimal cut — remove any the compiler flags as unused; the sibling `AgentTicket.elm` import block is the reference for the exact `Views.Styles.pageBelowTopBar` / `SideBar` / `TopBar` names. If `Views.Styles.topBar`/`pageBelowTopBar` signatures differ, copy the `view` scaffold verbatim from `AgentTickets/AgentTicket.elm`'s `view` and swap only the inner content — do not invent style function names.

- [ ] Run it, expected PASS. Command: `cd web/elm && elm-test tests/AgentRunTranscriptPageTests.elm`. Expected output: `TEST RUN PASSED`.

- [ ] Commit (with Tasks 4/5/7). Deferred — see Task 7.

---

## Task 7 — Wire the page into `SubPage` and link from `AgentTicket`

**Files:**
- Modify `web/elm/src/SubPage/SubPage.elm` (imports `:16-17`; model `:60-61`; init `:150-152`; `genericUpdate` args `:203-204` + `:252-258`; handleCallback `:275-276`; handleDelivery `:327-328`; update `:345-346`; view `:513-520`; tooltip `:565-569`; subscriptions `:608-612`)
- Modify `web/elm/src/AgentTickets/AgentTicket.elm` (run-history row view — `runHistory` / `Html.Lazy.lazy runHistory model.runMetricsByBuild` at `:519`)

`SubPage` threads each page as a positional argument through a `genericUpdate` combinator; adding a page means adding one branch/arg at each of the nine sites. Follow the `AgentTicketModel` pattern exactly — it is the nearest structural twin (a page with `handleCallback`, `handleDelivery`, `update`, `subscriptions`, `tooltip`, `documentTitle`, `view`).

### Steps

- [ ] Add the import. In `web/elm/src/SubPage/SubPage.elm` after line 16-17:

```elm
import AgentTickets.AgentRunTranscript as AgentRunTranscript
import AgentTickets.AgentTicket as AgentTicket
import AgentTickets.AgentTickets as AgentTickets
```

- [ ] Add the model constructor after `AgentTicketModel AgentTicket.Model` (line ~61):

```elm
    | AgentTicketModel AgentTicket.Model
    | AgentRunTranscriptModel AgentRunTranscript.Model
```

- [ ] Add the init dispatch after the `Routes.AgentTicket` arm (line ~152):

```elm
        Routes.AgentRunTranscript { id, buildId } ->
            AgentRunTranscript.init { id = id, buildId = buildId }
                |> Tuple.mapFirst AgentRunTranscriptModel
```

- [ ] Thread the `genericUpdate` argument. This is the mechanical part. `genericUpdate` takes one function per page and dispatches on the model variant. Add a new parameter (e.g. `fART2`) to the `genericUpdate` signature and its model `case`, mirroring `fAT`:

  In the `genericUpdate` type signature block (line ~203-204), add:

```elm
    -> ET AgentTicket.Model
    -> ET AgentRunTranscript.Model
```

  In the model dispatch `case` (line ~256-258), add after the `AgentTicketModel` arm:

```elm
        AgentTicketModel agentTicketModel ->
            fAT ( agentTicketModel, effects )
                |> Tuple.mapFirst AgentTicketModel

        AgentRunTranscriptModel m ->
            fART2 ( m, effects )
                |> Tuple.mapFirst AgentRunTranscriptModel
```

  And add `fART2` to `genericUpdate`'s value-level parameter list. The real head at `SubPage.elm:206` is exactly:

```elm
genericUpdate fBuild fJob fRes fPipe fDash fCaus fNF fFS dFly fAR fAgent fATs fAT ( model, effects ) =
```

  Change it to (add `fART2` immediately after `fAT`, keeping the `( model, effects )` argument last):

```elm
genericUpdate fBuild fJob fRes fPipe fDash fCaus fNF fFS dFly fAR fAgent fATs fAT fART2 ( model, effects ) =
```

  Then update **every caller** of `genericUpdate` (handleCallback, handleDelivery, update) to pass the new page's function immediately after the `fAT` argument, in the same position:

  handleCallback (line ~275-276) add after `AgentTicket.handleCallback callback`:

```elm
        (AgentTicket.handleCallback callback)
        (AgentRunTranscript.handleCallback callback)
```

  handleDelivery (line ~327-328) add after `AgentTicket.handleDelivery delivery`:

```elm
        (AgentTicket.handleDelivery delivery)
        (AgentRunTranscript.handleDelivery delivery)
```

  update (line ~345-346) add after `(Login.update msg >> AgentTicket.update msg)`:

```elm
        (Login.update msg >> AgentTicket.update msg)
        (Login.update msg >> AgentRunTranscript.update msg)
```

- [ ] Add the view arm (line ~518-520):

```elm
        AgentRunTranscriptModel model ->
            ( AgentRunTranscript.documentTitle model
            , AgentRunTranscript.view session model
            )
```

  (Match the tuple shape the sibling `AgentTicketModel` view arm returns — `( documentTitle, view )` vs a record; copy it exactly.)

- [ ] Add the tooltip arm (line ~568-569):

```elm
        AgentRunTranscriptModel model ->
            AgentRunTranscript.tooltip model
```

  (If `tooltip` takes `model session`, match the sibling's arity.)

- [ ] Add the subscriptions arm (line ~611-612):

```elm
        AgentRunTranscriptModel _ ->
            AgentRunTranscript.subscriptions
```

- [ ] Re-target run rows to the transcript. **Each run row is *already* a single `Html.a`** — `runRow` (`AgentTicket.elm:1197`) returns `Html.a [ class "agent-ticket-run-row", href (Routes.toString (Routes.OneOffBuild { id = buildId, highlight = Routes.HighlightNothing })), … ] [ … ]`. Do **not** wrap it in another anchor (nested `<a>` is invalid HTML). Instead **replace** that existing `href` target so the whole row now links to the transcript page:

  Change the `href` line inside `runRow` (line ~1249) from:

```elm
        , href (Routes.toString (Routes.OneOffBuild { id = buildId, highlight = Routes.HighlightNothing }))
```

  to:

```elm
        , href (Routes.toString (Routes.AgentRunTranscript { id = ticketId, buildId = buildId }))
```

  The transcript link **replaces** the old raw-build link (the transcript page is the new spectator destination for an attempt); it is not an additional control, so the row stays exactly one anchor.

  `runRow` does not currently receive `ticketId` — thread it through. `runRow` is called from `runHistory` (`AgentTicket.elm:1180`), which currently has signature `runHistory : Dict Int (List Concourse.Agent.RunMetric) -> Html Message` and is invoked at `AgentTicket.elm:519` via `Html.Lazy.lazy runHistory model.runMetricsByBuild`. Do all three:
  1. Change `runHistory`'s signature to `runHistory : Int -> Dict Int (List Concourse.Agent.RunMetric) -> Html Message` (leading `ticketId` param) and pass it down: `List.map (runRow ticketId)`.
  2. Change `runRow`'s signature to `runRow : Int -> ( Int, List Concourse.Agent.RunMetric ) -> Html Message` (leading `ticketId` param).
  3. Change the call site at `:519` from `Html.Lazy.lazy runHistory model.runMetricsByBuild` to `Html.Lazy.lazy2 runHistory model.ticketId model.runMetricsByBuild` (`lazy` → `lazy2`).

  Confirm `href` and `Routes` are already imported in `AgentTicket.elm` (they are: `Html.Attributes exposing (... href ...)` and `import Routes`).

- [ ] Verify whole-app compilation. Command: `cd web/elm && elm make --output /dev/null src/Main.elm`. Expected output: `Success!`.

- [ ] Run the full Elm test suite. Command: `cd web/elm && elm-test`. Expected output: `TEST RUN PASSED` (all suites, including the four new/modified test files).

- [ ] Commit Tasks 4–7 together. `git add web/elm/src/Message/ web/elm/src/Routes.elm web/elm/tests/RoutesTests.elm web/elm/src/AgentTickets/AgentRunTranscript.elm web/elm/tests/AgentRunTranscriptPageTests.elm web/elm/src/SubPage/SubPage.elm web/elm/src/AgentTickets/AgentTicket.elm && git commit -m "feat(web): agent-run transcript viewer page + route + run-row links (S-2)"`

---

## Task 8 — Rebuild the embedded Elm bundle (MANDATORY)

**Files:**
- Modify (generated) `web/public/elm.min.js`

The served UI is `web/public/elm.min.js`. Editing `web/elm/src/**` without rebuilding leaves the browser on the OLD bundle — the known stale-bundle trap. **There is no local elm-build gate today** (WF-2 is the track that adds one); this rebuild-and-commit step is manual and must not be skipped.

### Steps

- [ ] Rebuild. Command: `bash hack/build-web.sh`. Expected output: a final line `built web/public/elm.min.js (<N> bytes)` with no `elm make` errors. (Equivalent: `yarn build-elm` from repo root. Requires `elm` 0.19.1 and `uglify-js`.)

- [ ] Confirm the bundle changed and contains the new page. Command: `git status --porcelain web/public/elm.min.js`. Expected: the file shows as modified (`M web/public/elm.min.js`).

- [ ] Commit the bundle. `git add web/public/elm.min.js && git commit -m "build(web): rebuild elm.min.js for transcript viewer (S-2)"`

---

## Self-Review

**Spec coverage (Proposal C requirements):**
- Turn timeline of collapsible entries — ✅ `entryView` with per-`seq` expand set (Task 6).
- Tool calls (command + trimmed output), file edits (path + diffstat), thinking summaries — ✅ decoded (`ToolCalled`/`ToolResulted`/`ArtifactWritten`/`Thought`) and rendered, but **not emitted by the runner today** (see grounding section + Open Decision 1). They render only if/when the runner streams them; this is deliberate forward-compatibility, not a gap in this track.
- Gate results, judge verdicts — ✅ `GateResulted`, `JudgeScored` (emitted today by `agent/harvest`).
- Live totals bar (turns / tokens / dollars ticking up while running) — ✅ `totalsBar` from server-derived `RunMetric`, refreshed by the 5s `Polling` fetch; "ticking" is per-step granularity (Open Decision 2).
- Raw JSON behind a toggle — ✅ `TranscriptRawToggled` + per-entry `entry.raw`.
- Placement decided and justified — ✅ dedicated `/agent-tickets/:id/runs/:buildId` page (rationale in Architecture).
- Uses existing SSE heartbeat — ⚠️ **corrected**: no browser-facing SSE exists; grounded on the 5s `Polling` helper instead (Open Decision 2 / Grounding risk).

**Placeholder scan:** No `TODO`/`TBD`/"handle edge cases"/"similar to Task N" in code steps. Every decoder arm, view branch, and route touchpoint is spelled out. The only intentional deferrals are explicit Open Decisions with recommendations.

**Type consistency:** `AgentTicketRunEvents Int Int` (ticketId, buildId) is a **direct member of the flat `Endpoint` union** (`Api/Endpoints.elm:18`, alongside `AgentTicketMetrics Int` at `:51`) — there is no `Endpoints.Agent (...)` wrapper anywhere in the app. It lines up with the `FetchAgentRunEvents Int Int` effect and the `AgentRunEventsFetched Int Int (Fetched String)` callback. `parseTranscript : String -> Transcript`; `Transcript.entries : List TimelineEntry`; `TimelineEntry.body : EntryBody`. The page reads `RunMetric.usage.inputTokens`/`.outputTokens`/`.turns`/`.costUsd`/`.outcome`/`.buildStatus`/`.buildId`, all confirmed present in `Concourse/Agent.elm:138-157`. `Fetched a = Result Http.Error a` (Callback.elm:20). `Http.expectString : Expect String` (elm/http 1.0.0). `Polling.Poll model` + `Polling.handleDelivery`/`subscriptions` confirmed (Polling.elm).

**Known verification-time adjustments (flagged, not placeholders):** the exact `SubPage.genericUpdate` parameter-threading, the `view` return shape (`{title,body}` vs tuple), the `tooltip`/`view` arities, and the `runHistory` `lazy`→`lazy2` change must be matched to the sibling `AgentTicketModel` code at edit time — the plan names each site and the pattern to copy, because these signatures are load-bearing and must not be guessed.

---

## Open Decisions

1. **Rich per-turn events require a runner change that is out of this track's scope (and touches contended code).** The runner (`agent/runner/runner.go:324`) calls claude with `--output-format json`, yielding only a final envelope, so `tool.call`/`tool.result`/thinking/`artifact.written` are never emitted. A true "tool calls + file edits + thinking" timeline needs the runner to adopt `--output-format stream-json` and translate streamed assistant/tool_use/tool_result messages into those events. **Recommendation:** ship S-2 now against the events that exist (steps, cost, gates, judge, push, HITL) with the full-taxonomy decoders already in place; file a **separate** loop-or-human ticket for the runner `stream-json` upgrade, and keep it out of S-2 because it edits contended runner code and belongs with the runner-owning track. Owner: platform/runner lead.

2. **"Live via SSE heartbeat" vs 5s poll.** The scoping doc (S-2 row) says "live totals via the existing SSE heartbeat," but there is no browser-facing SSE for agent progress — the only `EventSource` in the Elm app is the Concourse **build-events** stream (CI log lines), and the "SSE heartbeat" in the codebase is the platform-MCP park heartbeat (server→pod, `atc/api/mcpserver`). This plan uses the existing 5s `Polling` helper (as `AgentTickets/AgentTicket.elm` already does). **Recommendation:** ship with the 5s poll (correct, cheap, consistent with the ticket page); only build a real transcript SSE endpoint if sub-5s latency becomes a product requirement — it is a server track of its own. Owner: web + platform lead.

3. **What does L-3/#43 persist, and does the transcript need cross-step merge server-side or client-side?** A build has multiple metric rows (one per step: implement, harvest, …), each with its own pod-local `events.ndjson`. This plan assumes #43 returns the **merged, time-ordered** stream for the whole build. If instead #43 returns per-step streams (e.g. keyed also by `plan_id`), the page must fetch per step and merge client-side. **Recommendation:** define #43 to return the merged build-level stream ordered by `ts` (simplest client); if that is not what L-3 lands, add a client-side merge fold (the `parseTranscript` `seq`/`ts` model already supports concatenating multiple bodies before folding). Confirm with the L-3 owner before implementing Task 6's fetch. Owner: L-3/#43 author.

4. **Bounded event volume / truncation UX.** `agent/schema/event_reader.go` caps a line at 5 MiB and L-3 "persists **bounded** flight events." If the API truncates, the viewer should say so. **Recommendation:** have #43 include a truncation signal (e.g. a trailing `{"event":"stream.truncated","data":{...}}` line or an HTTP header); the decoder already ignores unknown types, so a `stream.truncated` entry would render via `Unknown` today — add a dedicated `EntryBody` arm + banner once the L-3 shape is known. Owner: L-3/#43 author + web.
