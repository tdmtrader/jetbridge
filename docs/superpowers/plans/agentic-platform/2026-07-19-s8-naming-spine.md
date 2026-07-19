# Naming Spine "Ticket → Attempt → Step" Implementation Plan

> For agentic workers: REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax.

**Track:** S-8 (audit Proposal B) · **Branch:** jetbridge · **Date:** 2026-07-19 · **Depends on:** Wave-2 W-2 (begins the copy phase; must land first)

## Goal

Adopt one vocabulary triple — **Ticket → Attempt → Step** — across web copy, the fly CLI, the run-metrics wire, and the contracts docs, sequenced so no wire byte ever breaks in a single step (every wire change is an ADDITIVE alias, never a rename).

## Architecture

The dispatched pipeline run of a ticket has, today, three names on the wire (`run_id` in `DispatchResponse`, `pipeline_run_id` on `Ticket`/`RunMetrics`) and several in the UI ("run", "build", "instance"). This plan fixes **Attempt** as the one word for that dispatched-run concept and **Step** as the word for one `RunMetrics` row (an agent step inside an attempt), leaving **Ticket** as-is. Phases land in dependency order: (1) web copy alignment on surfaces Wave-2 does not own; (2) an `attempt` column in `fly agent runs` and the web recent-steps table, both reading the *existing* `pipeline_run_id`; (3) additive `attempt_id` wire aliases on `RunMetrics` (mirrors `pipeline_run_id`) and `DispatchResponse` (mirrors `run_id`), each proven not to break the four existing decoders; (4) contracts/CONVENTIONS docs. No migration: `attempt_id` is a read-time mirror of the existing `pipeline_run_id` column and the existing `run_id` response field — no new column is introduced.

## Tech Stack

- **Go** — `agent/schema` (wire types), `atc/db` + `agent/api/metrics` (read-population stores), `agent/api/tickets` (dispatch response), `fly/commands` (CLI table), tested with `go test` and Ginkgo (`fly/integration`).
- **Elm 0.19.1** — `web/elm/src/AgentTickets/AgentTicket.elm`, `web/elm/src/Agent/Agent.elm`; tested with `elm-test`; the served bundle `web/public/elm.min.js` is rebuilt with `hack/build-web.sh`.
- **Markdown** — contracts under `docs/superpowers/plans/agentic-platform/`.

## Vocabulary decision (the spine)

| Concept | ONE canonical word | Today's names (soup) | Wire field(s) |
|---|---|---|---|
| The unit of work | **Ticket** | ticket | `ticket_id`, `id` |
| One dispatched pipeline run of a ticket | **Attempt** | run, pipeline run, build, instance | `pipeline_run_id` (Ticket/RunMetrics), `run_id` (DispatchResponse) — **all the SAME id** (`agent/dispatch/dispatch.go:241` sets `TransitionMeta{PipelineRunID: &runID}`) |
| One agent step inside an attempt | **Step** | run, step | `step_name` + `plan_id` (RunMetrics row) |

`agent_tickets.attempt_count` (`agent/api/tickets/types.go:141`) already uses "attempt", so the ticket surface is the anchor the rest of the vocabulary snaps to.

## File Structure

| File | Change | Responsibility |
|---|---|---|
| `web/elm/src/AgentTickets/AgentTicket.elm` | Modify (copy only, NOT the run-row `view`) | Phase 1: "run" → "attempt" in the surrounding labels/prompts/error box |
| `web/elm/tests/AgentTicketPageTests.elm` | Modify | Phase 1: assert the "attempt" copy renders |
| `web/elm/src/Agent/Agent.elm` | Modify | Phase 2: add an "attempt" column to the recent-steps table (reads existing `pipelineRunId`) |
| `web/elm/tests/AgentPageTests.elm` | Modify | Phase 2: assert the attempt column renders the pipeline-run id |
| `fly/commands/agent_runs.go` | Modify | Phase 2: add an "attempt" column (reads existing `PipelineRunID`) |
| `fly/integration/agent_test.go` | Modify | Phase 2: assert the attempt column |
| `agent/schema/metrics.go` | Modify | Phase 3: add `AttemptID *int json:"attempt_id,omitempty"` (read-time mirror of `PipelineRunID`) |
| `agent/schema/metrics_test.go` | Modify | Phase 3: round-trip + alias-population + old-consumer-tolerance tests |
| `atc/db/agent_run_metrics_factory.go` | Modify | Phase 3: populate `AttemptID` on read next to `Outcome` |
| `atc/db/agent_run_metrics_factory_test.go` | Modify | Phase 3: DB read populates `attempt_id` |
| `agent/api/metrics/memory_store.go` | Modify | Phase 3: populate `AttemptID` on read next to `Outcome` |
| `agent/api/metrics/memory_store_test.go` | Create or Modify | Phase 3: memory store read populates `attempt_id` |
| `agent/api/tickets/types.go` | Modify | Phase 3: add `AttemptID int json:"attempt_id"` to `DispatchResponse` |
| `agent/dispatch/handler.go` | Modify | Phase 3: set `AttemptID: res.RunID` when encoding the dispatch 201 body |
| `agent/dispatch/handler_test.go` | Modify | Phase 3: dispatch body carries `attempt_id == run_id` |
| `docs/superpowers/plans/agentic-platform/CONVENTIONS.md` | Modify | Phase 4: record the Ticket→Attempt→Step spine + the additive-alias rule |
| `docs/superpowers/plans/agentic-platform/00-shared-contracts.md` | Modify | Phase 4: document `attempt_id` as the forward alias of `pipeline_run_id`/`run_id` |
| `web/public/elm.min.js` | Rebuild (generated) | Phases 1 & 2: regenerate the embedded bundle so the deployed web is not stale |

---

## Phase 1 — Web copy alignment (no wire change)

Scope: only the AgentTicket copy Wave-2 does **not** own. W-2 owns the run-row `view` (the `build N`/`attempt N` label at `runRow`, `web/elm/src/AgentTickets/AgentTicket.elm:~1197`); W-3 owns the error-box *link*; W-7 owns unrelated copy. This phase touches the section label (`~1187`), the dispatch prompt (`~723`), the "latest run" preface (`~897`), and the error-box *title text* (`~677`) — all outside the run-row `view` function. **Re-grep at HEAD before editing**; if Wave-2 already changed a string, adapt to the landed text rather than the line numbers below.

### Task 1.1 — "runs" → "attempts" in the AgentTicket labels and prompts

**Files:** Modify `web/elm/src/AgentTickets/AgentTicket.elm`; Test `web/elm/tests/AgentTicketPageTests.elm`.

- [ ] Write the failing test. Append this `test` inside the existing `describe "ticket detail page"` block in `web/elm/tests/AgentTicketPageTests.elm` (place it beside the other rendering tests; reuse the file's existing `givenTicketLoaded`/detail-fixture helper — grep the file for the helper that feeds a loaded `Detail` and renders the page, and call it exactly as the neighbouring tests do):

```elm
        , test "labels the dispatched run as an attempt, not a run" <|
            \_ ->
                givenDispatchableTicketLoaded
                    |> Common.queryView
                    |> Expect.all
                        [ Query.has [ text "attempts" ]
                        , Query.has [ text "latest attempt — " ]
                        , Query.hasNot [ text "latest run — " ]
                        ]
```

  If the file has no `givenDispatchableTicketLoaded` helper, use the exact loaded-ticket setup the existing `"Dispatch run"` test at `AgentTicketPageTests.elm:160` uses (copy its pipeline verbatim) so the dispatch controls and the "latest …" preface both render.

- [ ] Run it, expect FAIL:

```bash
cd web/elm && npx elm-test tests/AgentTicketPageTests.elm 2>&1 | tail -20
```

  Expected: the new test fails with a query miss for `text "attempts"` / `text "latest attempt — "` (the page still says "runs" / "latest run — ").

- [ ] Minimal implementation. In `web/elm/src/AgentTickets/AgentTicket.elm`, change these four string literals (verify each still exists at HEAD first):
  - the runs-section label: `formLabel "runs"` → `formLabel "attempts"` (currently `~1187`).
  - the dispatch prompt: `Html.text "Dispatch a run now?"` → `Html.text "Dispatch an attempt now?"` (currently `~723`).
  - the latest-summary preface: `Html.text "latest run — "` → `Html.text "latest attempt — "` (currently `~897`).
  - the error-box title: `Html.text "Run error"` → `Html.text "Attempt error"` (currently `~677`). If W-3 has already restyled this box, keep W-3's link/structure and change only the visible title text.

- [ ] Run it, expect PASS:

```bash
cd web/elm && npx elm-test tests/AgentTicketPageTests.elm 2>&1 | tail -20
```

  Expected: `TEST RUN PASSED`. If any neighbouring test asserted the OLD copy ("Run error", "latest run"), update that assertion in the same commit — grep `web/elm/tests/AgentTicketPageTests.elm` for the old strings first.

- [ ] Commit:

```bash
git add web/elm/src/AgentTickets/AgentTicket.elm web/elm/tests/AgentTicketPageTests.elm
git commit -m "s8(web): name the dispatched run an 'attempt' in ticket-page copy"
```

### Task 1.2 — Rebuild the embedded Elm bundle

**Files:** Rebuild `web/public/elm.min.js` (generated).

There is **no local elm-build gate today** (WF-2 adds one); the deployed web serves `web/public/elm.min.js` verbatim, so an un-rebuilt bundle silently ships the old copy. This task is mandatory whenever `web/elm/**` changed.

- [ ] Confirm the toolchain is present:

```bash
elm --version && uglifyjs --version
```

  Expected: `0.19.1` and a uglify-js version. If missing: `npm i -g elm@0.19.1-5 uglify-js`.

- [ ] Rebuild:

```bash
./hack/build-web.sh
```

  Expected final line: `built web/public/elm.min.js (<N> bytes)` and a clean exit.

- [ ] Sanity-check the new copy made it into the bundle:

```bash
grep -c "latest attempt" web/public/elm.min.js
```

  Expected: `1` (or more). A `0` means the bundle is stale — rerun `./hack/build-web.sh`.

- [ ] Commit the regenerated bundle:

```bash
git add web/public/elm.min.js
git commit -m "s8(web): rebuild elm.min.js for attempt-copy rename"
```

---

## Phase 2 — Attempt column in fly and the web recent-steps table (reads existing `pipeline_run_id`)

Both surfaces list `RunMetrics` rows (one per STEP) with no attempt reference today, so a reader cannot tell which dispatched run a step belongs to. This phase adds an **attempt** column that renders the *existing* `pipeline_run_id` — no wire change, so it lands before Phase 3.

### Task 2.1 — fly `agent runs`: add an "attempt" column

**Files:** Modify `fly/commands/agent_runs.go`; Test `fly/integration/agent_test.go`.

- [ ] Write the failing test. In `fly/integration/agent_test.go`, extend the `agent runs` fixture and **update the three existing position-sensitive `gbytes.Say` assertions** in the `It("renders the fused outcome …")` block. Add a `pipelineRun := 77` local at the top of the `BeforeEach` and set `PipelineRunID: &pipelineRun` **only on the first fixture element** (the `BuildID: 1 … implement` row); leave the harvest and review rows' `PipelineRunID` nil so they render `-`.

  Because the new `attempt` column is inserted **between `step` and `workflow`**, it shifts every data row — so the three existing assertions at `agent_test.go:170-172` must each gain the attempt cell or they will stop matching. Update them in place (re-grep at HEAD for the exact current lines first; the anchors below are the pre-edit positions):

  - line 170: `implement\s+develop@3\s+failed` → `implement\s+77\s+develop@3\s+failed` (implement row carries attempt `77`)
  - line 171: `harvest\s+failed` → `harvest\s+-\s+failed` (harvest row has no attempt → `-`)
  - line 172: `review\s+ok` → `review\s+-\s+ok` (review row has no attempt → `-`)

  Also add an `"attempt"` header assertion right after the command is started (the table prints headers because `Fly.PrintTableHeaders` is set in the suite):

```go
			Expect(sess.Out).To(gbytes.Say(`step\s+attempt\s+workflow\s+status`))
```

- [ ] Run it, expect FAIL:

```bash
go test ./fly/integration/ -run TestIntegration -count=1 2>&1 | tail -30
```

  Expected: the `agent runs` spec fails — the header row has no `attempt` column and the `implement` row has no `77` between step and workflow. (If the suite name differs, run `ginkgo --focus="agent runs" ./fly/integration/`.)

- [ ] Minimal implementation. In `fly/commands/agent_runs.go`, add the header cell between `step` and `workflow` in the `ui.Table{Headers: …}` literal:

```go
		{Contents: "step", Color: color.New(color.Bold)},
		{Contents: "attempt", Color: color.New(color.Bold)},
		{Contents: "workflow", Color: color.New(color.Bold)},
```

  Add an `attempt` cell to each data row, between `{Contents: r.StepName}` and `{Contents: workflow}`:

```go
		attempt := "-"
		if r.PipelineRunID != nil {
			attempt = strconv.Itoa(*r.PipelineRunID)
		}
		table.Data = append(table.Data, ui.TableRow{
			{Contents: r.StepName},
			{Contents: attempt},
			{Contents: workflow},
			statusCell,
			{Contents: fmt.Sprintf("$%.2f", r.CostUSD)},
			{Contents: fmt.Sprintf("%d/%d", r.Usage.InputTokens, r.Usage.OutputTokens)},
			{Contents: strconv.Itoa(r.Turns)},
			{Contents: ticket},
		})
```

  (`strconv` is already imported.)

- [ ] Run it, expect PASS:

```bash
go test ./fly/integration/ -run TestIntegration -count=1 2>&1 | tail -30
```

  Expected: the `agent runs` spec passes; the header reads `step  attempt  workflow  status …` and the `implement` row shows `77`.

- [ ] Commit:

```bash
git add fly/commands/agent_runs.go fly/integration/agent_test.go
git commit -m "s8(fly): add an 'attempt' column (pipeline-run id) to agent runs"
```

### Task 2.2 — Web recent-steps table: add an "attempt" column

**Files:** Modify `web/elm/src/Agent/Agent.elm`; Test `web/elm/tests/AgentPageTests.elm`.

- [ ] Write the failing test. In `web/elm/tests/AgentPageTests.elm`, find the test that renders the recent-runs table (grep for `"Recent runs"` or `agent-runs-table`). Add a fixture row whose `pipeline_run_id` is set and assert both the header and the value. Model the fixture on the file's existing run-metric JSON/`RunMetric` fixture; the new assertions are:

```elm
        , Query.has [ text "attempt" ]
        , Query.has [ text "77" ]
```

  Ensure the fixture row you assert against decodes `"pipeline_run_id": 77` (add it to that row's JSON, or set `pipelineRunId = Just 77` if the test builds `RunMetric` records directly).

- [ ] Run it, expect FAIL:

```bash
cd web/elm && npx elm-test tests/AgentPageTests.elm 2>&1 | tail -20
```

  Expected: the test fails on the missing `attempt` header / `77` cell.

- [ ] Minimal implementation. In `web/elm/src/Agent/Agent.elm`:
  - add a header cell to `runsHeaderRow` between `step` and `workflow`:

```elm
        [ tableHeaderCell "left" "step"
        , tableHeaderCell "left" "attempt"
        , tableHeaderCell "left" "workflow"
```

  - add a matching data cell to `runRow`, between `runStepCell expandedRuns r` and the `workflow` cell:

```elm
    Html.tr [ class "agent-run-row" ]
        [ runStepCell expandedRuns r
        , tableCell "left" (attemptRef r)
        , tableCell "left" (workflowRef r.workflowName r.workflowVersion)
```

  - add the helper near `workflowRef`:

```elm
{-| The attempt (dispatched pipeline run) a step belongs to. "-" for CI-agent
steps, which have no dispatched run. Reads the existing pipeline_run_id field;
the attempt_id wire alias (Phase 3) is not required here.
-}
attemptRef : Agent.RunMetric -> String
attemptRef r =
    case r.pipelineRunId of
        Just runId ->
            String.fromInt runId

        Nothing ->
            "-"
```

  (`RunMetric.pipelineRunId : Maybe Int` already exists and already decodes `"pipeline_run_id"` — `web/elm/src/Concourse/Agent.elm:140,173`.)

- [ ] Run it, expect PASS:

```bash
cd web/elm && npx elm-test tests/AgentPageTests.elm 2>&1 | tail -20
```

  Expected: `TEST RUN PASSED`.

- [ ] Commit:

```bash
git add web/elm/src/Agent/Agent.elm web/elm/tests/AgentPageTests.elm
git commit -m "s8(web): add an 'attempt' column to the /agent recent-steps table"
```

### Task 2.3 — Rebuild the embedded Elm bundle

**Files:** Rebuild `web/public/elm.min.js` (generated).

- [ ] Rebuild:

```bash
./hack/build-web.sh
```

  Expected: `built web/public/elm.min.js (<N> bytes)`.

- [ ] Commit:

```bash
git add web/public/elm.min.js
git commit -m "s8(web): rebuild elm.min.js for the attempt column"
```

---

## Phase 3 — Additive `attempt_id` wire aliases (never remove the old fields)

`attempt_id` becomes the forward-looking canonical name for the dispatched-run id. It is added **alongside** `pipeline_run_id` (RunMetrics) and `run_id` (DispatchResponse); both old fields stay. `attempt_id` is server-derived on read (a mirror of the already-populated value), exactly like `outcome`/`build_status` — never accepted from an ingesting client. Every task here proves the four existing decoders (fly Go struct, go-concourse Go struct, web Elm decoder, and the ci-agent costs producer) are unaffected.

### Task 3.1 — `RunMetrics.AttemptID` field + round-trip + tolerance tests

**Files:** Modify `agent/schema/metrics.go`; Test `agent/schema/metrics_test.go`.

- [ ] Write the failing test. Append to `agent/schema/metrics_test.go`:

```go
func TestRunMetricsAttemptIDAlias(t *testing.T) {
	// attempt_id is emitted alongside pipeline_run_id (never instead of it).
	pr := 77
	rm := schema.RunMetrics{BuildID: 5, PlanID: "p", StepName: "s", Status: schema.RunStatusOK, PipelineRunID: &pr, AttemptID: &pr}
	data, err := json.Marshal(rm)
	requireNoErr(t, err)
	requireContains(t, string(data), `"pipeline_run_id":77`)
	requireContains(t, string(data), `"attempt_id":77`)

	// omitempty: a metric with no dispatched run emits neither field.
	bare, err := json.Marshal(schema.RunMetrics{BuildID: 5, PlanID: "p", StepName: "s", Status: schema.RunStatusOK})
	requireNoErr(t, err)
	if strings.Contains(string(bare), "attempt_id") || strings.Contains(string(bare), "pipeline_run_id") {
		t.Fatalf("bare metric must omit both ids, got %s", bare)
	}

	// forward-compat: a payload carrying attempt_id but not pipeline_run_id
	// still decodes without error (old field simply stays nil).
	var back schema.RunMetrics
	requireNoErr(t, json.Unmarshal([]byte(`{"build_id":5,"plan_id":"p","step_name":"s","status":"ok","attempt_id":77}`), &back))
	if back.AttemptID == nil || *back.AttemptID != 77 {
		t.Fatalf("attempt_id did not decode: %+v", back.AttemptID)
	}
}
```

- [ ] Run it, expect FAIL:

```bash
go test ./agent/schema/ -run TestRunMetricsAttemptIDAlias -count=1 2>&1 | tail -20
```

  Expected: compile error `unknown field AttemptID in struct literal` (the field does not exist yet).

- [ ] Minimal implementation. In `agent/schema/metrics.go`, add the field to `RunMetrics` immediately after `PipelineRunID`:

```go
	PipelineRunID   *int   `json:"pipeline_run_id,omitempty"`
	// AttemptID is the forward-looking canonical name for the dispatched
	// pipeline run this step belongs to — the SAME id as PipelineRunID
	// (S-8 naming spine, Ticket→Attempt→Step). It is populated on read as a
	// mirror of PipelineRunID and, like Outcome/BuildStatus, is never
	// accepted from an ingesting client. Emitted alongside pipeline_run_id
	// (never instead of it); pipeline_run_id is retained until a future
	// deprecation pass so no existing consumer breaks.
	AttemptID       *int   `json:"attempt_id,omitempty"`
	BuildID         int    `json:"build_id"`
```

- [ ] Run it, expect PASS:

```bash
go test ./agent/schema/ -count=1 2>&1 | tail -20
```

  Expected: `ok  github.com/concourse/concourse/agent/schema`. Note `TestRunMetricsRoundTrip` still passes because the ingest fixture leaves `AttemptID` nil (omitempty).

- [ ] Commit:

```bash
git add agent/schema/metrics.go agent/schema/metrics_test.go
git commit -m "s8(schema): add attempt_id alias field to RunMetrics (additive)"
```

### Task 3.2 — Populate `AttemptID` on read in the DB factory

**Files:** Modify `atc/db/agent_run_metrics_factory.go`; Test `atc/db/agent_run_metrics_factory_test.go`.

- [ ] Write the failing test. In `atc/db/agent_run_metrics_factory_test.go`, find an existing test that inserts a metric with a non-nil `PipelineRunID` and reads it back via `GetByBuild`/`ListByTicket`/`ListRecent` (grep the file for `PipelineRunID`). Add an assertion that the read row's `AttemptID` mirrors `PipelineRunID`. If no such fixture exists, add to the nearest read-back test:

```go
			Expect(got[0].AttemptID).NotTo(BeNil())
			Expect(*got[0].AttemptID).To(Equal(*got[0].PipelineRunID))
```

  (Insert a metric with `PipelineRunID` set in that test's setup if it does not already — mirror the existing `Upsert(&schema.RunMetrics{…})` call and add `PipelineRunID: &pr` with `pr := <the build's run id>`.)

- [ ] Run it, expect FAIL:

```bash
ginkgo --focus="attempt" ./atc/db/ 2>&1 | tail -30
```

  Expected: `Expected <*int | nil> not to be nil` — the read path does not populate `AttemptID` yet. (PostgreSQL must be running: `pg_isready`.)

- [ ] Minimal implementation. In `atc/db/agent_run_metrics_factory.go`, in `scanRunMetricsRows`, add one line right after `rm.Outcome = rm.DeriveOutcome()` (currently `~289`):

```go
		rm.Outcome = rm.DeriveOutcome()
		rm.AttemptID = rm.PipelineRunID
```

- [ ] Run it, expect PASS:

```bash
ginkgo --focus="attempt" ./atc/db/ 2>&1 | tail -30
```

  Expected: the focused spec passes.

- [ ] Commit:

```bash
git add atc/db/agent_run_metrics_factory.go atc/db/agent_run_metrics_factory_test.go
git commit -m "s8(db): mirror pipeline_run_id into attempt_id on metrics read"
```

### Task 3.3 — Populate `AttemptID` on read in the memory store

**Files:** Modify `agent/api/metrics/memory_store.go`; Test `agent/api/metrics/memory_store_test.go` (Create if absent).

- [ ] Write the failing test. If `agent/api/metrics/memory_store_test.go` does not exist, create it:

```go
package metrics_test

import (
	"testing"

	"github.com/concourse/concourse/agent/api/metrics"
	schema "github.com/concourse/concourse/agent/schema"
)

func TestMemoryStorePopulatesAttemptID(t *testing.T) {
	s := metrics.NewMemoryStore()
	pr := 77
	if err := s.Upsert(&schema.RunMetrics{BuildID: 5, PlanID: "p", StepName: "s", Status: schema.RunStatusOK, PipelineRunID: &pr}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := s.GetByBuild(5)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 row, got %d", len(got))
	}
	if got[0].AttemptID == nil || *got[0].AttemptID != 77 {
		t.Fatalf("attempt_id not populated on read: %+v", got[0].AttemptID)
	}
}
```

  If the file already exists, append `TestMemoryStorePopulatesAttemptID` to it and drop the package/import header.

- [ ] Run it, expect FAIL:

```bash
go test ./agent/api/metrics/ -run TestMemoryStorePopulatesAttemptID -count=1 2>&1 | tail -20
```

  Expected: `attempt_id not populated on read: <nil>`.

- [ ] Minimal implementation. In `agent/api/metrics/memory_store.go`, in the `list` method, add one line right after `out[i].Outcome = out[i].DeriveOutcome()` (currently `~97`):

```go
		out[i].Outcome = out[i].DeriveOutcome()
		out[i].AttemptID = out[i].PipelineRunID
```

- [ ] Run it, expect PASS:

```bash
go test ./agent/api/metrics/ -count=1 2>&1 | tail -20
```

  Expected: `ok  github.com/concourse/concourse/agent/api/metrics`.

- [ ] Commit:

```bash
git add agent/api/metrics/memory_store.go agent/api/metrics/memory_store_test.go
git commit -m "s8(metrics): mirror pipeline_run_id into attempt_id in memory store"
```

### Task 3.4 — `DispatchResponse.AttemptID` (mirror of `run_id`)

**Files:** Modify `agent/api/tickets/types.go` (the `DispatchResponse` type), `agent/dispatch/handler.go` (the encode site, `~47`); Test `agent/dispatch/handler_test.go`.

The `DispatchResponse` type lives in `agent/api/tickets/types.go:307`, but the 201 body is encoded in `agent/dispatch/handler.go:47` (`json.NewEncoder(w).Encode(tickets.DispatchResponse{RunID: res.RunID, PipelineName: res.PipelineName})`), where `res` is the `dispatch.Result` returned by `DispatchOne`. `res.RunID` IS the pipeline-run id (`agent/dispatch/dispatch.go:105`).

- [ ] Write the failing test. `agent/dispatch/handler_test.go` is a plain Go test (not Ginkgo). `TestDispatchHandlerHappyPath` already decodes `rec.Body` into `var resp tickets.DispatchResponse` and asserts `resp.RunID == 555`. Add, right after that assertion block:

```go
	if resp.AttemptID != resp.RunID {
		t.Errorf("attempt_id %d != run_id %d", resp.AttemptID, resp.RunID)
	}
	if !strings.Contains(rec.Body.String(), `"attempt_id":`) {
		t.Errorf("body missing attempt_id field: %s", rec.Body)
	}
```

  (`strings` is already imported by this file.)

- [ ] Run it, expect FAIL:

```bash
go test ./agent/dispatch/ -count=1 2>&1 | tail -20
```

  Expected: compile error `resp.AttemptID undefined` (the decoded variable is `resp`; the field does not exist on `DispatchResponse` yet).

- [ ] Minimal implementation. In `agent/api/tickets/types.go`, add the field to `DispatchResponse`:

```go
type DispatchResponse struct {
	RunID int `json:"run_id"`
	// AttemptID is the S-8 canonical name for the created dispatched run —
	// the SAME value as RunID. Emitted alongside run_id (never instead of
	// it) so existing clients keep decoding run_id.
	AttemptID    int    `json:"attempt_id"`
	PipelineName string `json:"pipeline_name"`
}
```

  In `agent/dispatch/handler.go`, set `AttemptID` from the same `res.RunID` already assigned to `RunID` (line `~47`):

```go
		json.NewEncoder(w).Encode(tickets.DispatchResponse{
			RunID:        res.RunID,
			AttemptID:    res.RunID,
			PipelineName: res.PipelineName,
		})
```

- [ ] Run it, expect PASS:

```bash
go test ./agent/dispatch/ ./agent/api/tickets/ -count=1 2>&1 | tail -20
```

  Expected: both packages `ok`.

- [ ] Commit:

```bash
git add agent/api/tickets/types.go agent/dispatch/handler.go agent/dispatch/handler_test.go
git commit -m "s8(api): add attempt_id alias to the dispatch 201 body (additive)"
```

### Task 3.5 — Prove the four existing decoders still decode (no regression)

**Files:** Test only — `go-concourse/concourse/agent_metrics_test.go`, `fly/integration/agent_test.go` (already green from Phase 2), and a re-run of the web decoder tests.

This task adds no production code; it locks in that adding `attempt_id` broke nothing.

- [ ] Add a decode-tolerance assertion to `go-concourse/concourse/agent_metrics_test.go` (a Ginkgo spec, `Describe("AgentRunMetrics")` at `:12`, whose `ghttp.RespondWithJSONEncoded` fixture is a `[]agentschema.RunMetrics` struct slice at `:18`). On the first fixture element set both ids, and assert the client (`agentschema.RunMetrics`) decodes both and they agree:

```go
	// in the fixture element at agent_metrics_test.go:18, add:
	//     PipelineRunID: intPtr(77), AttemptID: intPtr(77),
	// then in the assertion block after `runs, err := client.AgentRunMetrics(5)`:
			Expect(*runs[0].PipelineRunID).To(Equal(77))
			Expect(*runs[0].AttemptID).To(Equal(77))
```

  If the file has no `intPtr` helper, use a local `pr := 77` above the fixture and `&pr` for both fields.

- [ ] Run the whole affected surface, expect PASS:

```bash
go test ./agent/schema/ ./agent/api/metrics/ ./agent/api/tickets/ ./go-concourse/concourse/ -count=1 2>&1 | tail -20
go test ./fly/integration/ -run TestIntegration -count=1 2>&1 | tail -10
cd web/elm && npx elm-test 2>&1 | tail -5
```

  Expected: every package `ok` / `TEST RUN PASSED`. The Elm run proves `Concourse.Agent.decodeRunMetric` — which does not read `attempt_id` and ignores the unknown field — still decodes every fixture. The ci-agent costs producer (`ci-agent/publish/publish.go`) is untouched: it POSTs to `/api/v1/agent/costs` and never decodes `RunMetrics`, so the alias cannot affect it (see Grounding Risk 2).

- [ ] Commit:

```bash
git add go-concourse/concourse/agent_metrics_test.go
git commit -m "s8(test): prove existing decoders tolerate the attempt_id alias"
```

---

## Phase 4 — Contracts and conventions

### Task 4.1 — Record the spine and the additive-alias rule

**Files:** Modify `docs/superpowers/plans/agentic-platform/CONVENTIONS.md`, `docs/superpowers/plans/agentic-platform/00-shared-contracts.md`.

This phase is documentation; verification is a content check, not a test run.

- [ ] In `docs/superpowers/plans/agentic-platform/CONVENTIONS.md`, append a "Naming spine" section (append after the current tail — do not insert at a pinned line; §11-style single-writer discipline):

```markdown
## Naming spine — Ticket → Attempt → Step (S-8, 2026-07-19)

One vocabulary triple, everywhere (copy, CLI, wire, docs):

- **Ticket** — the unit of work (`agent_tickets`; wire `ticket_id`/`id`).
- **Attempt** — one dispatched pipeline run of a ticket. The dispatched-run id
  has two legacy wire names — `pipeline_run_id` (Ticket, RunMetrics) and
  `run_id` (DispatchResponse) — that are the SAME id
  (`agent/dispatch/dispatch.go` sets `TransitionMeta{PipelineRunID: &runID}`).
  The canonical name is `attempt_id`, emitted ADDITIVELY alongside both legacy
  fields. `agent_tickets.attempt_count` already counts attempts.
- **Step** — one agent step inside an attempt (`RunMetrics`; wire
  `step_name` + `plan_id`).

Wire rule: renames are forbidden while any of ci-agent / fly / web / go-concourse
still reads the old name. Add the new name as a read-time alias
(`attempt_id` mirrors `pipeline_run_id`, populated where `outcome` is derived),
keep the old field, and schedule removal only after every consumer has migrated.
```

- [ ] In `docs/superpowers/plans/agentic-platform/00-shared-contracts.md`, locate the `RunMetrics` field table / §2.4 (grep `pipeline_run_id`) and add a row/line documenting `attempt_id` as "server-derived read-time mirror of `pipeline_run_id`; forward-canonical name for the dispatched run (S-8); never accepted on ingest." Add the same note next to the `DispatchResponse` `run_id` documentation. Append after the current tail of the relevant section, not at a pinned line.

- [ ] Verify the docs render and reference real fields:

```bash
grep -n "attempt_id" docs/superpowers/plans/agentic-platform/CONVENTIONS.md docs/superpowers/plans/agentic-platform/00-shared-contracts.md
```

  Expected: matches in both files.

- [ ] Commit:

```bash
git add docs/superpowers/plans/agentic-platform/CONVENTIONS.md docs/superpowers/plans/agentic-platform/00-shared-contracts.md
git commit -m "s8(docs): record the Ticket→Attempt→Step spine and additive-alias rule"
```

---

## Self-Review

**Spec coverage (the four ordered phases from the track brief):**
- Phase 1 copy-level in web/fly — Tasks 1.1–1.2 (web copy; fly copy is folded into the Phase-2 column since fly has no free-standing "run" noun to relabel — its command name `agent runs` is a CLI contract and MUST NOT change; noted in Open Decisions).
- Phase 2 fly attempt column — Task 2.1, plus the symmetric web column (Task 2.2) so both step-list surfaces gain attempt context.
- Phase 3 API additive aliases — Tasks 3.1–3.5, with a dedicated regression task proving all four existing decoders still decode.
- Phase 4 docs — Task 4.1.
- "Every phase includes a task proving old consumers still decode" — Phase 3 Task 3.5 is that proof for the wire; Phases 1–2 change no wire, and their Elm/fly tests exercise the unchanged decoders end-to-end.

**Placeholder scan:** no TODO/TBD/"similar to"/"add validation" placeholders; every code step shows real code or an exact string edit with its current line anchor and a re-grep instruction. The two "grep for the existing helper/fixture" instructions are grounded (the helpers exist in the named test files) and give the exact fallback (copy the cited neighbouring test verbatim).

**Type consistency:**
- `RunMetrics.AttemptID` is `*int` matching `PipelineRunID` (`*int`); `json:"attempt_id,omitempty"` matches `pipeline_run_id,omitempty` so a bare metric emits neither — asserted in Task 3.1.
- `DispatchResponse.AttemptID` is `int` matching `RunID` (`int`), no `omitempty` (both always present) — matches the existing `run_id` shape.
- Elm `attemptRef` consumes the existing `RunMetric.pipelineRunId : Maybe Int`; no new decoder field is required for Phase 2.
- Read-population lines are placed at the exact existing sites where `Outcome` is derived (DB factory `~289`, memory store `~97`), so `AttemptID` cannot diverge from `PipelineRunID`.

**No coordination-constraint violations:** no edit to `render.go`'s refusal switch or `RenderAgentStep` return literal (this track does not touch `agent/dispatch/render.go`); no migration (verified `attempt_id` maps to the existing `pipeline_run_id` column and the existing `run_id` field — no new column, so no slot claimed from the 1773106067 head); all wire changes additive/back-compat; both Elm phases rebuild `web/public/elm.min.js`; Phase 3 does not follow the six-touchpoint route pattern because it adds no route — it only widens two existing response bodies.

## Open Decisions

1. **Is an "attempt" the pipeline run or the build?** — `agent_tickets.attempt_count` increments per *dispatch* (pipeline-run scoped), but the web `runRow` groups by `build_id` and W-2 labels those build-groups "attempt N". A pipeline run can span multiple builds (re-trigger / checkpoint re-dispatch), so build-grouped "attempt N" can over-count. **Recommendation:** fix Attempt = the dispatched pipeline run (`pipeline_run_id`), which this plan's wire alias encodes; treat the W-2 build-grouped label as a v0 approximation and, in the S-1 ticket-DAG track, derive the "attempt N" ordinal from distinct `pipeline_run_id`s (falling back to `build_id` when it is absent). Owner: whoever executes S-1. This plan does not change W-2's grouping.
2. **fly command/flag names stay put.** `fly agent runs` is a shipped CLI surface; renaming the subcommand to `attempts` would break scripts. **Recommendation:** keep the command name, add the `attempt` *column* (Task 2.1), and only consider a `fly agent attempts` alias subcommand in a later, separately-scoped CLI change. Owner: fly maintainer.
3. **When do the legacy wire fields get removed?** This plan only *adds* `attempt_id`; it never removes `pipeline_run_id`/`run_id`. **Recommendation:** schedule the removal one release after web + go-concourse decoders have switched to reading `attempt_id`, and only after confirming no external consumer of the run-metrics wire exists; track it as a follow-up "deprecate pipeline_run_id/run_id" ticket. Owner: platform maintainer. Do not remove in this plan.
