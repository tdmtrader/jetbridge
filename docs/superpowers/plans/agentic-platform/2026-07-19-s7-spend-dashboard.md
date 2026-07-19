# Spend Dashboard + Cap Control Implementation Plan

> For agentic workers: REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax.

**Goal:** Ship a dedicated `/agent/spend` dashboard (audit Proposal H) that renders a daily-spend sparkline, a burn-down of open tickets against their per-ticket budgets, spend broken down by workflow / model / step, and an explicit unattributed bucket — all from the existing agent cost-ledger rollups.

**Architecture:** The agent cost-ledger already aggregates spend server-side via `budget.Ledger.Rollup(groupBy, since, until)` behind `GET /api/v1/agent/costs?group_by=…` (`agent/api/costs/handler.go`). Today `group_by` accepts `day|user|ticket|workflow`; this track adds two more dimensions — `model` and `step` — that read the ledger's existing `model` and `step_name` columns (no migration). A new Elm SubPage `Agent/Spend.elm` fires one rollup fetch per dimension plus the ticket list, joins ticket budgets against per-ticket spend for the burn-down, and renders inline SVG. The daily cap is displayed honestly as a deploy-time value (`--agent-daily-budget-usd`); making it runtime-mutable is a flagged Open Decision, not built here.

**Tech Stack:** Go 1.x (agent API, `atc/db` squirrel queries, `agent/budget`), Elm 0.19.1 (web SubPage, `elm-test`), `fly` CLI (go-flags), the six-touchpoint agent-route pattern for the Elm route, and `hack/build-web.sh` for the embedded `elm.min.js` bundle.

---

## File Structure

| File | Create/Modify | Responsibility |
|------|---------------|----------------|
| `agent/budget/budget.go` | Modify | Add `GroupByModel`/`GroupByStep` constants; extend `ValidGroupBy`. |
| `agent/budget/memory.go` | Modify | Add `model`/`step` key cases to the in-memory `Rollup` (test ledger parity). |
| `agent/budget/checker_test.go` | Modify | Unit test that `ValidGroupBy` accepts `model`/`step` and the memory ledger groups by them. |
| `atc/db/agent_cost_ledger_factory.go` | Modify | Add `model`/`step_name` `keyExpr` cases to the SQL `Rollup`. |
| `atc/db/agent_cost_ledger_factory_test.go` | Modify | Ginkgo spec: rollup by `model` and by `step`. |
| `agent/api/costs/handler.go` | Modify | Update the `group_by` validation error string to list `model|step`. |
| `agent/api/costs/handler_test.go` | Modify | Assert the new dimensions are accepted (200, not 400). |
| `atc/api/mcpserver/tools.go` | Modify | Add `model`/`step` to the `agent_cost_rollup` tool enum, description, and error string (surface parity). |
| `fly/commands/agent_costs.go` | Modify | Add `choice:"model"`/`choice:"step"` to `--group-by`. |
| `web/elm/src/Message/Effects.elm` | Modify | New `FetchAgentSpendRollup String` effect (group_by query param). |
| `web/elm/src/Message/Callback.elm` | Modify | New `AgentSpendRollupFetched (Fetched CostRollup)` callback. |
| `web/elm/src/Routes.elm` | Modify | Six-touchpoint `AgentSpend` route (type, parser, sitemap, `toString`, `getGroups`, `withGroups`). |
| `web/elm/src/Views/Styles.elm` | Modify | Add a `Routes.AgentSpend ->` branch to `pageBelowTopBar` (exhaustive `case route of`, no wildcard) — the Elm build fails until this branch exists. |
| `web/elm/src/Agent/Spend.elm` | Create | The dashboard page: model, fetches, sparkline, burn-down, breakdown tables, unattributed callout. |
| `web/elm/src/SubPage/SubPage.elm` | Modify | Register `AgentSpendModel` across `genericUpdate`, init, update, view, tooltip, subscriptions, handleCallback, handleDelivery. |
| `web/elm/src/Agent/Agent.elm` | Modify | W-15 honest cap copy + a "full spend dashboard →" link to `/agent/spend`. |
| `web/elm/tests/AgentSpendPageTests.elm` | Create | `elm-test` coverage for the new page (renders sections after callbacks; honest cap copy). |
| `web/public/elm.min.js` | Modify (generated) | Rebuilt bundle — the served UI. Must be regenerated and committed. |

---

## Phase 1 — Server: `model` and `step` rollup dimensions (additive, no migration)

### Task 1: Add `GroupByModel` / `GroupByStep` to the budget vocabulary

**Files:**
- Modify: `agent/budget/budget.go:43-57`
- Modify: `agent/budget/memory.go:56-102`
- Test: `agent/budget/checker_test.go`

Steps:

- [ ] Write the failing test. Append to `agent/budget/checker_test.go`:

```go
func TestValidGroupByAcceptsModelAndStep(t *testing.T) {
	for _, g := range []string{budget.GroupByModel, budget.GroupByStep} {
		if !budget.ValidGroupBy(g) {
			t.Fatalf("ValidGroupBy(%q) = false, want true", g)
		}
	}
	if budget.GroupByModel != "model" || budget.GroupByStep != "step" {
		t.Fatalf("group_by tokens drifted: model=%q step=%q", budget.GroupByModel, budget.GroupByStep)
	}
}

func TestMemoryLedgerRollupByModelAndStep(t *testing.T) {
	m := &budget.MemoryLedger{}
	base := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(m.Insert(budget.LedgerEntry{OccurredAt: base, Source: budget.SourceAgentStep, Model: "opus", StepName: "implement", CostUSD: 1.0}))
	must(m.Insert(budget.LedgerEntry{OccurredAt: base, Source: budget.SourceAgentStep, Model: "opus", StepName: "harvest", CostUSD: 2.0}))
	must(m.Insert(budget.LedgerEntry{OccurredAt: base, Source: budget.SourceAgentStep, Model: "sonnet", StepName: "implement", CostUSD: 4.0}))

	byModel, err := m.Rollup(budget.GroupByModel, base.Add(-time.Hour), time.Time{})
	must(err)
	got := map[string]float64{}
	for _, r := range byModel {
		got[r.Key] = r.CostUSD
	}
	if got["opus"] != 3.0 || got["sonnet"] != 4.0 {
		t.Fatalf("by model = %+v, want opus=3 sonnet=4", got)
	}

	byStep, err := m.Rollup(budget.GroupByStep, base.Add(-time.Hour), time.Time{})
	must(err)
	got = map[string]float64{}
	for _, r := range byStep {
		got[r.Key] = r.CostUSD
	}
	if got["implement"] != 5.0 || got["harvest"] != 2.0 {
		t.Fatalf("by step = %+v, want implement=5 harvest=2", got)
	}
}
```

- [ ] Run it, expect FAIL:

```
cd /Users/tdmtrader/concourse/concourse && go test ./agent/budget/ -run 'TestValidGroupByAcceptsModelAndStep|TestMemoryLedgerRollupByModelAndStep'
```

Expected: `undefined: budget.GroupByModel` (compile failure), or once constants exist, `by model = map[] ...` because the memory ledger folds `model`/`step` into the day default.

- [ ] Minimal implementation. In `agent/budget/budget.go`, extend the rollup-dimension block (currently ends at `GroupByWorkflow`):

```go
// Rollup dimensions for GetAgentCostRollup (?group_by=).
const (
	GroupByUser     = "user"
	GroupByTicket   = "ticket"
	GroupByDay      = "day"
	GroupByWorkflow = "workflow" // reads metadata->>'workflow' (see contract addendum)
	GroupByModel    = "model"    // reads the ledger model column
	GroupByStep     = "step"     // reads the ledger step_name column
)

func ValidGroupBy(g string) bool {
	switch g {
	case GroupByUser, GroupByTicket, GroupByDay, GroupByWorkflow, GroupByModel, GroupByStep:
		return true
	}
	return false
}
```

- [ ] In `agent/budget/memory.go`, add the two cases to the `switch groupBy` inside `Rollup` (before the `default:` day branch):

```go
		case GroupByModel:
			key = e.Model
		case GroupByStep:
			key = e.StepName
```

- [ ] Run it, expect PASS:

```
cd /Users/tdmtrader/concourse/concourse && go test ./agent/budget/ -run 'TestValidGroupByAcceptsModelAndStep|TestMemoryLedgerRollupByModelAndStep'
```

Expected: `ok  github.com/concourse/concourse/agent/budget`

- [ ] Commit:

```
git add agent/budget/budget.go agent/budget/memory.go agent/budget/checker_test.go
git commit -m "feat(budget): add model and step rollup dimensions"
```

### Task 2: SQL `Rollup` `keyExpr` for `model` and `step_name`

**Files:**
- Modify: `atc/db/agent_cost_ledger_factory.go:76-90`
- Test: `atc/db/agent_cost_ledger_factory_test.go`

Steps:

- [ ] Write the failing test. In `atc/db/agent_cost_ledger_factory_test.go`, inside the existing `Describe("AgentCostLedgerFactory", …)` insert-then-rollup spec (near the other `ledger.Rollup(...)` assertions around line 87-123), add:

```go
		By("rolling up by model")
		rows, err = ledger.Rollup(budget.GroupByModel, since, time.Time{})
		Expect(err).NotTo(HaveOccurred())
		modelCost := map[string]float64{}
		for _, r := range rows {
			modelCost[r.Key] = r.CostUSD
		}
		Expect(modelCost["opus"]).To(BeNumerically("~", 3.0, 0.0001))
		Expect(modelCost["sonnet"]).To(BeNumerically("~", 4.0, 0.0001))

		By("rolling up by step")
		rows, err = ledger.Rollup(budget.GroupByStep, since, time.Time{})
		Expect(err).NotTo(HaveOccurred())
		stepCost := map[string]float64{}
		for _, r := range rows {
			stepCost[r.Key] = r.CostUSD
		}
		Expect(stepCost["implement"]).To(BeNumerically("~", 5.0, 0.0001))
		Expect(stepCost["harvest"]).To(BeNumerically("~", 2.0, 0.0001))
```

To keep the assertion self-contained (no guessing at pre-existing fixture totals), insert three known rows at the top of the spec and assert exactly their sums — do NOT reconcile against whatever the file already inserts. Add, before the `By("rolling up by model")` block:

```go
By("seeding known model/step fixture rows")
Expect(ledger.Insert(db.LedgerEntry{OccurredAt: since.Add(time.Hour), Source: budget.SourceAgentStep, Model: "opus", StepName: "implement", CostUSD: 1.0})).NotTo(HaveOccurred())
Expect(ledger.Insert(db.LedgerEntry{OccurredAt: since.Add(time.Hour), Source: budget.SourceAgentStep, Model: "opus", StepName: "harvest", CostUSD: 2.0})).NotTo(HaveOccurred())
Expect(ledger.Insert(db.LedgerEntry{OccurredAt: since.Add(time.Hour), Source: budget.SourceAgentStep, Model: "sonnet", StepName: "implement", CostUSD: 4.0})).NotTo(HaveOccurred())
```

giving deterministic totals opus=3.0 / sonnet=4.0 (by model) and implement=5.0 / harvest=2.0 (by step), which the assertions above expect. Read the top of the existing spec first to match the real `Insert` signature and entry type (the exact struct name — `db.LedgerEntry` vs `budget.LedgerEntry` — and the `since` variable name; adapt the snippet to what the file actually uses). If the spec's OTHER pre-existing rows happen to carry a `Model`/`StepName` that collides with `opus`/`sonnet`/`implement`/`harvest`, use distinct fixture keys here (e.g. `model-a`, `step-a`) and update the four expected sums to the three rows you inserted — never guess at a total you did not seed.

- [ ] Run it, expect FAIL:

```
cd /Users/tdmtrader/concourse/concourse && ginkgo --focus="AgentCostLedgerFactory" ./atc/db/
```

Expected: `unsupported group_by "model"` returned by `Rollup`, failing the `err` expectation.

- [ ] Minimal implementation. In `atc/db/agent_cost_ledger_factory.go`, add two cases to the `switch groupBy` (before `default:`):

```go
	case budget.GroupByModel:
		keyExpr = `COALESCE(model, '')`
	case budget.GroupByStep:
		keyExpr = `COALESCE(step_name, '')`
```

- [ ] Run it, expect PASS:

```
cd /Users/tdmtrader/concourse/concourse && ginkgo --focus="AgentCostLedgerFactory" ./atc/db/
```

Expected: `SUCCESS! -- N Passed`.

- [ ] Commit:

```
git add atc/db/agent_cost_ledger_factory.go atc/db/agent_cost_ledger_factory_test.go
git commit -m "feat(db): agent cost rollup by model and step_name"
```

### Task 3: Surface-parity — costs handler, MCP tool, fly choices

**Files:**
- Modify: `agent/api/costs/handler.go:104-112`
- Modify: `atc/api/mcpserver/tools.go:1223`, `:1229`, `:1249`
- Modify: `fly/commands/agent_costs.go:15`
- Test: `agent/api/costs/handler_test.go`

Steps:

- [ ] Write the failing test. In `agent/api/costs/handler_test.go`, add a test asserting `group_by=model` and `group_by=step` reach the ledger (HTTP 200), and that the error string for a bad value now mentions `model`/`step`. The file already has a `newHandler()` helper (line 16) that returns `(*costs.Handler, *budget.MemoryLedger)` built from `budget.NewMemoryLedger()` + `budget.NewChecker(ledger, budget.NoTicketBudgets{}, budget.Config{GlobalDailyCapUSD: 50, Location: time.UTC})`; the existing `TestGetRollup*` tests (line 120+) call `h, _ := newHandler()` then `h.GetRollup(rec, req)`. Follow that exact convention — do NOT introduce `fakeLedger`/`fakeChecker`; those types do not exist. Concretely:

```go
func TestGetRollupAcceptsModelAndStep(t *testing.T) {
	for _, g := range []string{"model", "step"} {
		h, _ := newHandler()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/costs?group_by="+g, nil)
		rec := httptest.NewRecorder()
		h.GetRollup(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("group_by=%s: status = %d, want 200; body=%s", g, rec.Code, rec.Body.String())
		}
	}
}

func TestGetRollupRejectsUnknownGroupByWithModelStepInMessage(t *testing.T) {
	h, _ := newHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/costs?group_by=nonsense", nil)
	rec := httptest.NewRecorder()
	h.GetRollup(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "model") || !strings.Contains(rec.Body.String(), "step") {
		t.Fatalf("error body %q must list model and step", rec.Body.String())
	}
}
```

(If you need to vary the checker config, construct it inline the way `newHandler` does: `ledger := budget.NewMemoryLedger()`, `checker := budget.NewChecker(ledger, budget.NoTicketBudgets{}, budget.Config{Location: time.UTC})`, then `h := costs.NewHandler(ledger, checker, "")`.) Because `GetRollup` calls `budget.ValidGroupBy`, the 200 case already passes once Task 1 landed; the assertion that must drive a change is the error-string one.

- [ ] Run it, expect FAIL:

```
cd /Users/tdmtrader/concourse/concourse && go test ./agent/api/costs/ -run 'TestGetRollup'
```

Expected: `error body "group_by must be one of user|ticket|day|workflow, got \"nonsense\"" must list model and step`.

- [ ] Minimal implementation. In `agent/api/costs/handler.go`, update the validation message in `GetRollup`:

```go
	if !budget.ValidGroupBy(groupBy) {
		http.Error(w, fmt.Sprintf("group_by must be one of user|ticket|day|workflow|model|step, got %q", groupBy), http.StatusBadRequest)
		return
	}
```

- [ ] In `atc/api/mcpserver/tools.go`, update the `agent_cost_rollup` tool so its schema and error match. Description (line ~1223):

```go
		"Roll up agent cost-ledger spend grouped by day, user, ticket, workflow, model, or step over an optional time window. Returns aggregate totals plus per-group rows.",
```

Enum (line ~1229):

```go
					"enum":        []string{budget.GroupByDay, budget.GroupByUser, budget.GroupByTicket, budget.GroupByWorkflow, budget.GroupByModel, budget.GroupByStep},
```

Error string (line ~1249):

```go
				return nil, fmt.Errorf("group_by must be one of day|user|ticket|workflow|model|step, got %q", groupBy)
```

- [ ] In `fly/commands/agent_costs.go`, extend the `GroupBy` field choices (line 15):

```go
	GroupBy string `long:"group-by" default:"day" choice:"day" choice:"user" choice:"ticket" choice:"workflow" choice:"model" choice:"step" description:"Rollup dimension"`
```

- [ ] Run it, expect PASS:

```
cd /Users/tdmtrader/concourse/concourse && go test ./agent/api/costs/ ./atc/api/mcpserver/ ./fly/... -run 'TestGetRollup|CostRollup|Cost'
```

Expected: `ok` for `agent/api/costs`; the MCP and fly packages compile and pass (no behavioral change beyond the enum). If a package has no matching test it prints `no test files` / `ok` — acceptable.

- [ ] Commit:

```
git add agent/api/costs/handler.go agent/api/costs/handler_test.go atc/api/mcpserver/tools.go fly/commands/agent_costs.go
git commit -m "feat(agent): expose model/step group_by across costs handler, MCP tool, fly"
```

---

## Phase 2 — Web wire: new fetch effect + callback

The new page fires one rollup fetch per dimension. Rather than overload the existing `AgentCostRollupFetched` (which `Agent/Agent.elm` already disambiguates by `group_by` for two dimensions), add a dedicated effect + callback so the Spend page can store all five rollups in one `Dict` keyed by `group_by`.

### Task 4: `FetchAgentSpendRollup` effect and `AgentSpendRollupFetched` callback

**Files:**
- Modify: `web/elm/src/Message/Effects.elm:224` (effect variant), `:826-830` (perform branch)
- Modify: `web/elm/src/Message/Callback.elm:77`

Steps:

- [ ] Add the callback. In `web/elm/src/Message/Callback.elm`, next to `AgentCostRollupFetched` (line 77):

```elm
    | AgentSpendRollupFetched (Fetched Concourse.Agent.CostRollup)
```

- [ ] Add the effect variant. In `web/elm/src/Message/Effects.elm`, next to `FetchAgentCostRollup` (line 224):

```elm
    | FetchAgentSpendRollup String
```

- [ ] Add the perform branch. In `web/elm/src/Message/Effects.elm`, after the `FetchAgentCostRollup ->` branch (line 826-830), following the exact query-param pattern `FetchAgentTicketCosts` uses (line 940-948):

```elm
        FetchAgentSpendRollup groupBy ->
            let
                base =
                    Api.get Endpoints.AgentCostRollup
            in
            { base | query = [ Url.Builder.string "group_by" groupBy ] }
                |> Api.expectJson Concourse.Agent.decodeCostRollup
                |> Api.request
                |> Task.attempt AgentSpendRollupFetched
```

- [ ] Verify it compiles (the real check is Task 6/7's page test; here just confirm no Effects/Callback type error):

```
cd /Users/tdmtrader/concourse/concourse/web/elm && elm make --output /dev/null src/Message/Effects.elm
```

Expected: `Success!` (a bare module compile; if it reports "I cannot find a `AgentSpendRollupFetched`" then Callback edit is missing — fix before proceeding).

- [ ] Commit:

```
git add web/elm/src/Message/Effects.elm web/elm/src/Message/Callback.elm
git commit -m "feat(web): FetchAgentSpendRollup effect + AgentSpendRollupFetched callback"
```

---

## Phase 3 — Web route: the six-touchpoint `/agent/spend`

### Task 5: `Routes.AgentSpend`

**Files:**
- Modify: `web/elm/src/Routes.elm:62` (type), `:324-326` (parser), `:505` (sitemap `oneOf`), `:622-624` (`toString`), `:744-745` (`getGroups`), `:787-788` (`withGroups`)
- Modify: `web/elm/src/Views/Styles.elm` — add a `Routes.AgentSpend ->` branch to `pageBelowTopBar` (`pageBelowTopBar` is at line 79; its `case route of` is exhaustive with NO wildcard, enumerating every Route variant, so adding the `AgentSpend` type below makes it non-exhaustive and the Elm build fails until this branch exists).

Steps:

- [ ] Add the route type. In `web/elm/src/Routes.elm`, next to `| Agent` (line 62):

```elm
    | AgentSpend
```

- [ ] Add the parser. After the `agent` parser (line 324-326):

```elm
agentSpend : Parser ((b -> Route) -> a) a
agentSpend =
    map (always <| AgentSpend) (s "agent" </> s "spend")
```

- [ ] Register in the sitemap `oneOf` (line 500-507 region). Add `agentSpend` **before** `agent` so a two-segment `/agent/spend` is attempted before the single-segment `/agent` (both are exact-length matches in elm/url, but ordering the more specific first is the house style):

```elm
        , agentReviews
        , agentSpend
        , agent
        , agentTicket
        , agentTickets
```

- [ ] Add `toString`. In the `toString` `case route of` (after the `Agent ->` branch, line 622-624):

```elm
        AgentSpend ->
            ( [ "agent", "spend" ], [] )
                |> RouteBuilder.build
```

- [ ] Add `getGroups`. In the `getGroups` `case route of` (after `Agent ->` at line 744-745):

```elm
        AgentSpend ->
            []
```

- [ ] Add `withGroups`. In the `withGroups` `case route of` (after `Agent ->` at line 787-788):

```elm
        AgentSpend ->
            route
```

- [ ] Add the `pageBelowTopBar` branch (MANDATORY — compile blocker). In `web/elm/src/Views/Styles.elm`, `pageBelowTopBar` (line 79) is `case route of` with **no wildcard** — it enumerates every Route variant (`Build … AgentTicket`). Adding the `AgentSpend` type above makes this `case` non-exhaustive and the Elm build fails until a branch exists. Add it after the `Routes.Agent ->` branch (~line 141), mirroring that branch's attribute list exactly:

```elm
                Routes.AgentSpend ->
                    [ style "box-sizing" "border-box"
                    , style "display" "flex"
                    , style "height" "100%"
                    ]
```

- [ ] (Optional, not required) `Views.TopBar.breadcrumbs` has a `_ ->` fallback (after `Routes.Agent ->`, ~line 171-174 of `web/elm/src/Views/TopBar.elm`), so `AgentSpend` already renders a generic crumb with no edit. Add a dedicated `Routes.AgentSpend ->` branch there only if a labelled "spend" crumb is wanted; the compiler does not force it.

- [ ] Verify the round-trip with the existing route test suite (it exercises `toString`/`parsePath`):

```
cd /Users/tdmtrader/concourse/concourse && yarn test 2>&1 | tail -20
```

Expected: **until the `Views/Styles.elm` branch above is added, `yarn test`/the Elm build FAILS to compile** with a non-exhaustive `pageBelowTopBar` pattern-match error naming `AgentSpend`; once the branch is in place the suite passes. (If `RoutesTests.elm` enumerates routes exhaustively it may also need the new case — add `AgentSpend` there mirroring the `Agent` entry if the compiler flags a missing pattern.)

- [ ] Commit:

```
git add web/elm/src/Routes.elm web/elm/src/Views/Styles.elm
git commit -m "feat(web): add /agent/spend route (six-touchpoint)"
```

---

## Phase 4 — Web page: `Agent/Spend.elm`

### Task 6: Page skeleton — model, fetches, empty view

**Files:**
- Create: `web/elm/src/Agent/Spend.elm`
- Test: `web/elm/tests/AgentSpendPageTests.elm`

The page fetches five rollups (`day`, `ticket`, `workflow`, `model`, `step`) plus the ticket list (reusing the existing `FetchAgentTickets`/`AgentTicketsFetched` wire — no new ticket plumbing). Results land in a `Dict String CostRollup` keyed by `group_by`.

Steps:

- [ ] Write the failing test. Create `web/elm/tests/AgentSpendPageTests.elm`:

```elm
module AgentSpendPageTests exposing (all)

import Application.Application as Application
import Common
import Message.Callback as Callback
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (class, text)


sampleDayRollup : { groupBy : String, summary : { dailyCapUsd : Float, dailySpentUsd : Float, dailyRemainingUsd : Float, dailyExhausted : Bool }, rows : List { key : String, entries : Int, inputTokens : Int, outputTokens : Int, turns : Int, costUsd : Float } }
sampleDayRollup =
    { groupBy = "day"
    , summary = { dailyCapUsd = 20, dailySpentUsd = 12.34, dailyRemainingUsd = 7.66, dailyExhausted = False }
    , rows =
        [ { key = "2026-07-10", entries = 2, inputTokens = 100, outputTokens = 200, turns = 3, costUsd = 5.0 }
        , { key = "2026-07-11", entries = 4, inputTokens = 300, outputTokens = 400, turns = 6, costUsd = 7.34 }
        ]
    }


all : Test
all =
    describe "agent spend page"
        [ test "renders the daily-spend section heading after the day rollup arrives" <|
            \_ ->
                Common.init "/agent/spend"
                    |> Application.handleCallback (Callback.AgentSpendRollupFetched (Ok sampleDayRollup))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-spend" ]
                    |> Query.has [ text "Daily spend" ]
        , test "states the cap is deploy-time set (honest copy)" <|
            \_ ->
                Common.init "/agent/spend"
                    |> Application.handleCallback (Callback.AgentSpendRollupFetched (Ok sampleDayRollup))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-spend" ]
                    |> Query.has [ text "--agent-daily-budget-usd" ]
        ]
```

Confirm `Common.queryView` exists (grep `web/elm/tests/Common.elm`); if the helper is named differently (e.g. `Common.queryView` vs a `Application.view >> Query.fromHtml` inline), match the existing convention used in `AgentPageTests.elm`.

- [ ] Run it, expect FAIL:

```
cd /Users/tdmtrader/concourse/concourse/web/elm && npx elm-test tests/AgentSpendPageTests.elm
```

Expected: compile error — `Agent.Spend` module does not exist / `SubPage` has no `AgentSpend` route mapping yet (that mapping is Task 7). The page module is created here; the SubPage wiring in Task 7 makes the route render.

- [ ] Minimal implementation — create `web/elm/src/Agent/Spend.elm` with the skeleton (model, init firing the six fetches, handleCallback storing rollups + tickets, empty-ish view with the `agent-spend` container and section stubs filled in by later tasks):

```elm
module Agent.Spend exposing
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

import Application.Models exposing (Session)
import Colors
import Concourse.Agent as Agent
import Concourse.AgentTicket as AgentTicket
import Dict exposing (Dict)
import EffectTransformer exposing (ET)
import Html exposing (Html)
import Html.Attributes exposing (class, href, id, style, title)
import Http
import Login.Login as Login
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message(..))
import Message.Subscription exposing (Delivery, Interval(..), Subscription)
import Polling
import Routes
import SideBar.SideBar as SideBar
import Svg
import Svg.Attributes as SvgAttr
import Tooltip
import Views.Styles
import Views.TopBar as TopBar


type alias Model =
    Login.Model
        { rollups : Dict String Agent.CostRollup
        , rollupError : Maybe String
        , tickets : Maybe (List AgentTicket.Ticket)
        , ticketsError : Maybe String
        }


dimensions : List String
dimensions =
    [ "day", "ticket", "workflow", "model", "step" ]


init : ( Model, List Effect )
init =
    ( { rollups = Dict.empty
      , rollupError = Nothing
      , tickets = Nothing
      , ticketsError = Nothing
      , isUserMenuExpanded = False
      }
    , List.map FetchAgentSpendRollup dimensions ++ [ FetchAgentTickets ]
    )


documentTitle : String
documentTitle =
    "Agent spend"


handleCallback : Callback -> ET Model
handleCallback callback ( model, effects ) =
    case callback of
        AgentSpendRollupFetched (Ok rollup) ->
            ( { model
                | rollups = Dict.insert rollup.groupBy rollup model.rollups
                , rollupError = Nothing
              }
            , effects
            )

        AgentSpendRollupFetched (Err err) ->
            ( { model | rollupError = Just (errorMessage "spend" err) }, effects )

        AgentTicketsFetched (Ok tickets) ->
            ( { model | tickets = Just tickets, ticketsError = Nothing }, effects )

        AgentTicketsFetched (Err err) ->
            ( { model | ticketsError = Just (errorMessage "tickets" err) }, effects )

        _ ->
            ( model, effects )


errorMessage : String -> Http.Error -> String
errorMessage what err =
    case err of
        Http.BadStatus { status } ->
            if status.code == 403 then
                "not authorized — the agent " ++ what ++ " API is admin-only"

            else
                "couldn't load " ++ what

        _ ->
            "couldn't load " ++ what


update : Message -> ET Model
update _ ( model, effects ) =
    ( model, effects )


tooltip : Model -> Session -> Maybe Tooltip.Tooltip
tooltip _ _ =
    Nothing


polls : List (Polling.Poll Model)
polls =
    [ { interval = OneMinute
      , fetch = \_ -> List.map FetchAgentSpendRollup dimensions ++ [ FetchAgentTickets ]
      }
    ]


handleDelivery : Delivery -> ET Model
handleDelivery =
    Polling.handleDelivery polls


subscriptions : List Subscription
subscriptions =
    Polling.subscriptions polls



-- VIEW


view : Session -> Model -> Html Message
view session model =
    Html.div
        (id "page-including-top-bar" :: Views.Styles.pageIncludingTopBar)
        [ Html.div
            (id "top-bar-app" :: Views.Styles.topBar False)
            [ Html.div
                [ style "display" "flex", style "align-items" "center" ]
                (SideBar.sideBarIcon session
                    :: TopBar.breadcrumbs session Routes.AgentSpend
                )
            , Login.view session.userState model
            ]
        , Html.div
            (id "page-below-top-bar" :: Views.Styles.pageBelowTopBar Routes.AgentSpend)
            [ SideBar.view session Nothing
            , Html.div
                [ class "agent-spend"
                , style "padding" "16px"
                , style "width" "100%"
                , style "box-sizing" "border-box"
                , style "overflow-y" "auto"
                ]
                [ Html.h1
                    [ style "font-size" "18px", style "margin" "0", style "color" Colors.text ]
                    [ Html.text "Agent spend" ]
                , dailySpendSection model
                , ticketBurndownSection model
                , breakdownSection "By workflow" "workflow" model
                , breakdownSection "By model" "model" model
                , breakdownSection "By step" "step" model
                , unattributedSection model
                ]
            ]
        ]



-- SECTION PLACEHOLDERS (filled by Tasks 8-10)


mutedColor : String
mutedColor =
    "#b0b0b0"


subtleColor : String
subtleColor =
    "#7a7a7a"


sectionTitle : String -> Html Message
sectionTitle t =
    Html.h2
        [ style "font-size" "15px", style "margin" "24px 0 8px 0", style "color" Colors.text ]
        [ Html.text t ]


dailySpendSection : Model -> Html Message
dailySpendSection model =
    Html.div []
        [ sectionTitle "Daily spend"
        , capCopy model
        ]


{-| Honest statement of where the daily cap is set (W-15). The cap is a
deploy-time flag; this page never pretends to control it.
-}
capCopy : Model -> Html Message
capCopy model =
    let
        line =
            case Dict.get "day" model.rollups of
                Just rollup ->
                    if rollup.summary.dailyCapUsd > 0 then
                        "Daily cap $"
                            ++ String.fromFloat rollup.summary.dailyCapUsd
                            ++ " — set at deploy time via --agent-daily-budget-usd; not editable here."

                    else
                        "No daily cap set — spend is unbounded (deploy-time flag: --agent-daily-budget-usd)."

                Nothing ->
                    "loading…"
    in
    Html.p
        [ style "color" subtleColor, style "font-family" "monospace", style "font-size" "12px" ]
        [ Html.text line ]


ticketBurndownSection : Model -> Html Message
ticketBurndownSection _ =
    Html.div [] [ sectionTitle "Ticket burn-down" ]


breakdownSection : String -> String -> Model -> Html Message
breakdownSection heading _ _ =
    Html.div [] [ sectionTitle heading ]


unattributedSection : Model -> Html Message
unattributedSection _ =
    Html.div [] [ sectionTitle "Unattributed" ]
```

Note on `tooltip`: the skeleton above already uses the signature `SubPage.elm` demands — `tooltip : Model -> Session -> Maybe Tooltip.Tooltip` (SubPage calls `AgentSpend.tooltip model session`, exactly as it calls `Agent.tooltip`). This is why the import block includes `import Tooltip` and `Session` (from `Application.Models`). Do NOT write `Maybe never`/`Maybe Never` — it will not unify with `Maybe Tooltip.Tooltip` and the SubPage `tooltip` branch (Task 7) won't compile.

- [ ] Do not run the page test yet — it needs Task 7's SubPage wiring to route `/agent/spend`. Proceed to Task 7, then run.

### Task 7: SubPage wiring

**Files:**
- Modify: `web/elm/src/SubPage/SubPage.elm` — import (14), `Model` variant (59), `genericUpdate` type+body (191-258), `init` (142), `update` dispatch (330-360), `handleCallback` (263-296), `handleDelivery` (315-328), `documentTitle`/`view` (515-563), `tooltip` (565-600), `subscriptions` (601-620)

Every `case … of` over the SubPage `Model` is exhaustive; the compiler will name each missing branch, so work compiler-error to compiler-error.

Steps:

- [ ] Add the import (line 14 area):

```elm
import Agent.Spend as AgentSpend
```

- [ ] Add the `Model` variant (line 59 area, next to `AgentModel Agent.Model`):

```elm
    | AgentSpendModel AgentSpend.Model
```

- [ ] Extend `genericUpdate`'s type signature — add one arrow before `-> ET Model` (after the `AgentTicket.Model` line, 204):

```elm
    -> ET AgentSpend.Model
```

- [ ] Add the parameter `fSpend` to `genericUpdate`'s argument list (line 206) and the body case:

```elm
genericUpdate fBuild fJob fRes fPipe fDash fCaus fNF fFS dFly fAR fAgent fATs fAT fSpend ( model, effects ) =
```

and, after the `AgentTicketModel` branch (line 256-258):

```elm
        AgentSpendModel agentSpendModel ->
            fSpend ( agentSpendModel, effects )
                |> Tuple.mapFirst AgentSpendModel
```

- [ ] Update `init` — add the route mapping (after `Routes.Agent ->`, line 142-144):

```elm
        Routes.AgentSpend ->
            AgentSpend.init
                |> Tuple.mapFirst AgentSpendModel
```

- [ ] Update `handleCallback` — the first `genericUpdate` call (263-276) gains a 14th arg:

```elm
        (AgentSpend.handleCallback callback)
```

placed after `(AgentTicket.handleCallback callback)`. The nested `LoggedOut` `genericUpdate` (279-292) gains a 14th `handleLoggedOut`:

```elm
                        handleLoggedOut
```

- [ ] Update `handleDelivery` (315-328) — add after `(AgentTicket.handleDelivery delivery)`:

```elm
        (AgentSpend.handleDelivery delivery)
```

- [ ] Update the `update` dispatch `genericUpdate` (330-360) — add a 14th arg mirroring the others. The Spend page has no Login menu interplay beyond the shared `Login.update`; match the `Agent` line (`(Login.update msg >> Agent.update msg)`) style:

```elm
        (Login.update msg >> AgentSpend.update msg)
```

placed after the `AgentTicket.update` arg. (Confirm the exact ordering by reading 330-360; the positional slot must be the 14th, aligning with `fSpend` last.)

- [ ] Update `documentTitle` + `view` (`case mdl of`, 515-563) — add:

```elm
        AgentSpendModel model ->
            ( AgentSpend.documentTitle
            , AgentSpend.view session model
            )
```

- [ ] Update `tooltip` (565-600) — add:

```elm
        AgentSpendModel model ->
            AgentSpend.tooltip model
```

(match arity: SubPage's `tooltip mdl =` is `Model -> Session -> Maybe Tooltip.Tooltip`, and each branch applies only the model — `AgentSpend.tooltip model` — so it must return `Session -> Maybe Tooltip.Tooltip`. The `Agent/Spend.elm` signature `tooltip : Model -> Session -> Maybe Tooltip.Tooltip` (Task 6) satisfies this exactly.)

- [ ] Update `subscriptions` (601-620) — add:

```elm
        AgentSpendModel _ ->
            AgentSpend.subscriptions
```

- [ ] Run the page test, expect PASS:

```
cd /Users/tdmtrader/concourse/concourse/web/elm && npx elm-test tests/AgentSpendPageTests.elm
```

Expected: `TEST RUN PASSED`. If the compiler flags a `tooltip`/`update` signature mismatch, fix `Agent/Spend.elm` to match the arity SubPage expects (read the `Agent` wiring lines it mirrors), then re-run.

- [ ] Commit:

```
git add web/elm/src/Agent/Spend.elm web/elm/src/SubPage/SubPage.elm web/elm/tests/AgentSpendPageTests.elm
git commit -m "feat(web): Agent/Spend page skeleton wired into SubPage"
```

### Task 8: Daily-spend sparkline

**Files:**
- Modify: `web/elm/src/Agent/Spend.elm` (`dailySpendSection`)
- Test: `web/elm/tests/AgentSpendPageTests.elm`

Steps:

- [ ] Write the failing test. Add to `AgentSpendPageTests.elm`:

```elm
        , test "draws one sparkline polyline point per day-rollup row" <|
            \_ ->
                Common.init "/agent/spend"
                    |> Application.handleCallback (Callback.AgentSpendRollupFetched (Ok sampleDayRollup))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.findAll [ class "agent-spend-sparkline" ]
                    |> Query.count (Expect.equal 1)
```

(Add `import Expect` if not already imported.)

- [ ] Run it, expect FAIL:

```
cd /Users/tdmtrader/concourse/concourse/web/elm && npx elm-test tests/AgentSpendPageTests.elm
```

Expected: `Query.count … Expect.equal 1` fails with `0` (no sparkline yet).

- [ ] Implement `dailySpendSection` with an inline SVG sparkline over the day rollup's `costUsd` series:

```elm
dailySpendSection : Model -> Html Message
dailySpendSection model =
    Html.div []
        (sectionTitle "Daily spend"
            :: capCopy model
            :: (case Dict.get "day" model.rollups of
                    Just rollup ->
                        [ sparkline (List.map .costUsd rollup.rows)
                        , dailyTotalsLine rollup.rows
                        ]

                    Nothing ->
                        [ Html.p [ style "color" mutedColor ] [ Html.text "loading…" ] ]
               )
        )


{-| A compact SVG sparkline of the daily cost series. Empty and single-point
series are handled explicitly so we never divide by a zero span.
-}
sparkline : List Float -> Html Message
sparkline values =
    let
        w =
            240.0

        h =
            40.0

        maxV =
            List.maximum values |> Maybe.withDefault 0

        n =
            List.length values

        point i v =
            let
                x =
                    if n <= 1 then
                        0

                    else
                        toFloat i / toFloat (n - 1) * w

                y =
                    if maxV <= 0 then
                        h

                    else
                        h - (v / maxV * h)
            in
            String.fromFloat x ++ "," ++ String.fromFloat y
    in
    if List.isEmpty values then
        Html.p [ style "color" mutedColor, style "font-size" "12px" ] [ Html.text "no spend recorded" ]

    else
        Svg.svg
            [ SvgAttr.class "agent-spend-sparkline"
            , SvgAttr.width (String.fromFloat w)
            , SvgAttr.height (String.fromFloat h)
            , SvgAttr.viewBox ("0 0 " ++ String.fromFloat w ++ " " ++ String.fromFloat h)
            ]
            [ Svg.polyline
                [ SvgAttr.fill "none"
                , SvgAttr.stroke "#7aa37a"
                , SvgAttr.strokeWidth "1.5"
                , SvgAttr.points (String.join " " (List.indexedMap point values))
                ]
                []
            ]


dailyTotalsLine : List Agent.CostRow -> Html Message
dailyTotalsLine rows =
    let
        total =
            List.map .costUsd rows |> List.sum
    in
    Html.p
        [ style "color" mutedColor, style "font-family" "monospace", style "font-size" "12px" ]
        [ Html.text ("window total: $" ++ formatUsd total ++ " over " ++ String.fromInt (List.length rows) ++ " days") ]
```

Add the `formatUsd` helper (copy the integer-cents implementation from `Agent/Agent.elm:444-473` verbatim — repeat it rather than cross-reference):

```elm
formatUsd : Float -> String
formatUsd amount =
    let
        cents =
            round (amount * 100)

        sign =
            if cents < 0 then
                "-"

            else
                ""

        absCents =
            abs cents

        dollars =
            absCents // 100

        remainder =
            modBy 100 absCents

        fraction =
            if remainder < 10 then
                "0" ++ String.fromInt remainder

            else
                String.fromInt remainder
    in
    sign ++ String.fromInt dollars ++ "." ++ fraction
```

- [ ] Run it, expect PASS:

```
cd /Users/tdmtrader/concourse/concourse/web/elm && npx elm-test tests/AgentSpendPageTests.elm
```

Expected: `TEST RUN PASSED`.

- [ ] Commit:

```
git add web/elm/src/Agent/Spend.elm web/elm/tests/AgentSpendPageTests.elm
git commit -m "feat(web): daily-spend sparkline on /agent/spend"
```

### Task 9: Per-ticket burn-down

Join the ticket list (each `Ticket.budgetUsd`) with the `group_by=ticket` rollup (spend per `ticket_id`). Render a bar per open ticket with a budget: spent vs. budget, amber when over.

**Files:**
- Modify: `web/elm/src/Agent/Spend.elm` (`ticketBurndownSection`)
- Test: `web/elm/tests/AgentSpendPageTests.elm`

Steps:

- [ ] Write the failing test. Add a ticket-rollup + ticket-list fixture and assert a burn-down row renders:

```elm
sampleTicketRollup : { groupBy : String, summary : { dailyCapUsd : Float, dailySpentUsd : Float, dailyRemainingUsd : Float, dailyExhausted : Bool }, rows : List { key : String, entries : Int, inputTokens : Int, outputTokens : Int, turns : Int, costUsd : Float } }
sampleTicketRollup =
    { groupBy = "ticket"
    , summary = { dailyCapUsd = 20, dailySpentUsd = 12.34, dailyRemainingUsd = 7.66, dailyExhausted = False }
    , rows = [ { key = "42", entries = 3, inputTokens = 10, outputTokens = 20, turns = 4, costUsd = 6.0 } ]
    }
```

and a test:

```elm
        , test "renders a burn-down row for a budgeted ticket that has spend" <|
            \_ ->
                Common.init "/agent/spend"
                    |> Application.handleCallback (Callback.AgentSpendRollupFetched (Ok sampleTicketRollup))
                    |> Tuple.first
                    |> Application.handleCallback (Callback.AgentTicketsFetched (Ok [ budgetedTicket ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-spend-burndown" ]
                    |> Query.has [ text "#42" ]
```

where `budgetedTicket` is a full `Concourse.AgentTicket.Ticket` record with `id = 42`, `budgetUsd = Just 10`, `state = "in_progress"` and the remaining fields filled (read `Concourse.AgentTicket.Ticket` for the exact field set — it has `id, title, body, state, origin, repo, targetBranch, workflowName, budgetUsd, userName, branch, createdAt, updatedAt, workflowVersion, pipelineRunId, attemptCount, errorDetail, completedAt`). Provide every field; no placeholders.

- [ ] Run it, expect FAIL (no `agent-spend-burndown` node yet).

- [ ] Implement `ticketBurndownSection`:

```elm
ticketBurndownSection : Model -> Html Message
ticketBurndownSection model =
    let
        spentByTicket =
            case Dict.get "ticket" model.rollups of
                Just rollup ->
                    rollup.rows
                        |> List.filterMap (\r -> Maybe.map (\_ -> ( r.key, r.costUsd )) (String.toInt r.key))
                        |> Dict.fromList

                Nothing ->
                    Dict.empty

        budgeted =
            case model.tickets of
                Just tickets ->
                    tickets
                        |> List.filterMap
                            (\t ->
                                case t.budgetUsd of
                                    Just b ->
                                        if b > 0 then
                                            Just ( t, b, Maybe.withDefault 0 (Dict.get (String.fromInt t.id) spentByTicket) )

                                        else
                                            Nothing

                                    Nothing ->
                                        Nothing
                            )

                Nothing ->
                    []
    in
    Html.div [ class "agent-spend-burndown" ]
        (sectionTitle "Ticket burn-down"
            :: (if List.isEmpty budgeted then
                    [ Html.p [ style "color" mutedColor, style "font-family" "monospace", style "font-size" "12px" ]
                        [ Html.text "no budgeted tickets with recorded spend" ]
                    ]

                else
                    List.map burndownRow budgeted
               )
        )


burndownRow : ( AgentTicket.Ticket, Float, Float ) -> Html Message
burndownRow ( t, budget, spent ) =
    let
        pct =
            min 100 (spent / budget * 100)

        over =
            spent > budget

        barColor =
            if over then
                "#e0a44e"

            else
                "#7aa37a"
    in
    Html.div
        [ style "margin" "6px 0", style "font-family" "monospace", style "font-size" "12px", style "color" Colors.text ]
        [ Html.div [ style "display" "flex", style "justify-content" "space-between", style "max-width" "320px" ]
            [ Html.a
                [ href (Routes.toString (Routes.AgentTicket { id = t.id }))
                , title t.title
                , style "color" "#7a9ac0"
                , style "text-decoration" "none"
                ]
                [ Html.text ("#" ++ String.fromInt t.id) ]
            , Html.text ("$" ++ formatUsd spent ++ " / $" ++ formatUsd budget)
            ]
        , Html.div [ style "max-width" "320px", style "height" "6px", style "background" "#3d3c3c" ]
            [ Html.div [ style "height" "6px", style "width" (String.fromFloat pct ++ "%"), style "background" barColor ] [] ]
        ]
```

- [ ] Run it, expect PASS.

- [ ] Commit:

```
git add web/elm/src/Agent/Spend.elm web/elm/tests/AgentSpendPageTests.elm
git commit -m "feat(web): per-ticket budget burn-down on /agent/spend"
```

### Task 10: Breakdown tables (workflow / model / step) + unattributed callout

**Files:**
- Modify: `web/elm/src/Agent/Spend.elm` (`breakdownSection`, `unattributedSection`)
- Test: `web/elm/tests/AgentSpendPageTests.elm`

Steps:

- [ ] Write the failing test. Add fixtures for `model` and `step` rollups (same shape as `sampleTicketRollup`, `groupBy = "model"` / `"step"`, rows like `{ key = "opus", …, costUsd = 3.0 }`) and:

```elm
        , test "renders a by-model breakdown row" <|
            \_ ->
                Common.init "/agent/spend"
                    |> Application.handleCallback (Callback.AgentSpendRollupFetched (Ok sampleModelRollup))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-spend-breakdown-model" ]
                    |> Query.has [ text "opus" ]
        , test "calls out the unattributed (empty-key) ticket bucket" <|
            \_ ->
                Common.init "/agent/spend"
                    |> Application.handleCallback (Callback.AgentSpendRollupFetched (Ok ticketRollupWithUnattributed))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-spend-unattributed" ]
                    |> Query.has [ text "unattributed" ]
```

where `ticketRollupWithUnattributed` has a row with `key = ""` and `costUsd = 9.0`.

- [ ] Run it, expect FAIL.

- [ ] Implement `breakdownSection` (a small sorted-desc table over a dimension's rollup rows) and `unattributedSection` (the empty-key row from the `ticket` rollup):

```elm
breakdownSection : String -> String -> Model -> Html Message
breakdownSection heading dim model =
    Html.div [ class ("agent-spend-breakdown-" ++ dim) ]
        (sectionTitle heading
            :: (case Dict.get dim model.rollups of
                    Just rollup ->
                        let
                            named =
                                rollup.rows
                                    |> List.filter (\r -> r.key /= "")
                                    |> List.sortBy (\r -> -r.costUsd)
                        in
                        if List.isEmpty named then
                            [ Html.p [ style "color" mutedColor, style "font-size" "12px" ] [ Html.text "no records" ] ]

                        else
                            [ Html.table
                                [ style "border-collapse" "collapse", style "font-family" "monospace", style "font-size" "12px", style "color" Colors.text ]
                                (List.map breakdownRow named)
                            ]

                    Nothing ->
                        [ Html.p [ style "color" mutedColor, style "font-size" "12px" ] [ Html.text "loading…" ] ]
               )
        )


breakdownRow : Agent.CostRow -> Html Message
breakdownRow r =
    Html.tr []
        [ Html.td [ style "padding" "3px 16px 3px 0" ] [ Html.text r.key ]
        , Html.td [ style "padding" "3px 0", style "text-align" "right" ] [ Html.text ("$" ++ formatUsd r.costUsd) ]
        ]


{-| Spend the by-ticket rollup reports under no ticket at all (CI review runs,
harvest pushes, platform housekeeping — the empty-string key). Called out
explicitly so it never hides. -}
unattributedSection : Model -> Html Message
unattributedSection model =
    let
        usd =
            case Dict.get "ticket" model.rollups of
                Just rollup ->
                    rollup.rows |> List.filter (\r -> r.key == "") |> List.map .costUsd |> List.sum

                Nothing ->
                    0
    in
    Html.div [ class "agent-spend-unattributed" ]
        [ sectionTitle "Unattributed"
        , Html.p
            [ style "color" mutedColor, style "font-family" "monospace", style "font-size" "12px" ]
            [ Html.text ("unattributed (no ticket): $" ++ formatUsd usd) ]
        ]
```

- [ ] Run it, expect PASS.

- [ ] Commit:

```
git add web/elm/src/Agent/Spend.elm web/elm/tests/AgentSpendPageTests.elm
git commit -m "feat(web): workflow/model/step breakdowns + unattributed callout"
```

### Task 11: Link from the /agent console + honest cap copy (W-15)

**Files:**
- Modify: `web/elm/src/Agent/Agent.elm:1033-1046` (`dailyCapGauge` uncapped copy) and the costs section header

Steps:

- [ ] Add a "Full spend dashboard →" link at the top of `costsSection` in `Agent/Agent.elm`. Locate `costsSection` (line 968) and prepend a link to the section's children:

```elm
        spendLink =
            Html.a
                [ href (Routes.toString Routes.AgentSpend)
                , style "color" "#7a9ac0"
                , style "text-decoration" "none"
                , style "font-family" "monospace"
                , style "font-size" "12px"
                ]
                [ Html.text "Full spend dashboard →" ]
```

and include `spendLink` as the first child of the `Just rollup ->` branch's list (before `costSummaryLine rollup.summary`). The `dailyCapGauge` already states `(web flag: --agent-daily-budget-usd)` when uncapped (line 1045) — no copy change needed there; leave it.

- [ ] Verify the full elm-test suite still passes (this touches the existing /agent page):

```
cd /Users/tdmtrader/concourse/concourse/web/elm && npx elm-test tests/AgentPageTests.elm tests/AgentSpendPageTests.elm
```

Expected: `TEST RUN PASSED`.

- [ ] Commit:

```
git add web/elm/src/Agent/Agent.elm
git commit -m "feat(web): link /agent costs to the full spend dashboard"
```

---

## Phase 5 — Rebuild the embedded bundle

### Task 12: Regenerate and commit `web/public/elm.min.js`

There is **no local elm-build gate today** (WF-2 will add one). The served UI is the committed `web/public/elm.min.js`; editing `web/elm/src/**` without regenerating leaves the deployed web on the OLD bundle. This step is mandatory.

**Files:**
- Modify (generated): `web/public/elm.min.js`

Steps:

- [ ] Run the whole elm-test suite once more to be sure nothing else regressed:

```
cd /Users/tdmtrader/concourse/concourse && yarn test 2>&1 | tail -20
```

Expected: `TEST RUN PASSED`.

- [ ] Rebuild the bundle (requires elm 0.19.1 + uglify-js):

```
cd /Users/tdmtrader/concourse/concourse && ./hack/build-web.sh
```

Expected: `built web/public/elm.min.js (<N> bytes)` and a nonzero, changed file.

- [ ] Confirm the bundle actually changed (not a stale no-op):

```
cd /Users/tdmtrader/concourse/concourse && git status --short web/public/elm.min.js
```

Expected: ` M web/public/elm.min.js`.

- [ ] Commit:

```
git add web/public/elm.min.js
git commit -m "build(web): rebuild elm.min.js for /agent/spend dashboard"
```

---

## Self-Review

**Spec coverage:**
- Daily-spend sparkline — Task 8 (SVG polyline over the `day` rollup).
- Burn-down against per-ticket budgets — Task 9 (join ticket list `budgetUsd` × `ticket` rollup spend).
- Spend by workflow / model / step — Tasks 1-3 add the `model`/`step` server dimensions; Task 10 renders all three tables.
- Unattributed bucket called out — Task 10 (`unattributedSection`, empty-key `ticket` row).
- Cap control — displayed honestly (Option B) in Task 6 `capCopy` + Task 11 link; runtime-mutable cap is the Open Decision below (not built).

**Placeholder scan:** every code step contains real code (no TODO/TBD/"similar to"). `formatUsd` is repeated verbatim rather than cross-referenced. The one deliberately deferred item — a full `Concourse.AgentTicket.Ticket` fixture in Task 9's test — lists all 18 fields to fill and points at the type definition; fill them from the record, do not stub.

**Type consistency:**
- Server: `budget.RollupRow` unchanged; new dimensions reuse the existing `keyExpr` scan shape. `ValidGroupBy` is the single gate consulted by both the HTTP handler and the MCP tool, so they cannot drift.
- Wire: `AgentSpendRollupFetched (Fetched Concourse.Agent.CostRollup)` reuses the existing `decodeCostRollup` — no new decoder, no wire-shape change. Additive only.
- Elm: `Agent.CostRollup`/`CostRow`/`CostSummary` reused unchanged; the page stores `Dict String CostRollup`. `Ticket.budgetUsd : Maybe Float` and `Ticket.id : Int` are the join keys (confirmed in `Concourse/AgentTicket.elm`).
- The SubPage `genericUpdate` signature grows by exactly one `ET AgentSpend.Model` arrow and one positional arg at every call site; the Elm compiler enforces exhaustiveness so an omission fails the build, not runtime.

**Coordination:**
- `agent/dispatch/render.go` is untouched.
- No migration in the core plan (the `model`/`step_name` ledger columns already exist). A slot is only needed under Open Decision Option A — flagged, not hard-coded.
- All wire changes are additive/back-compat: unknown-token fallbacks already exist server-side (`ValidGroupBy`) and the new callback is a fresh variant.
- Route `/agent/spend` overlaps S-3's planned IA (`/agent/{…,spend,…}`). S-3 should reuse this route rather than re-mint it — see Open Decisions.

---

## Open Decisions

1. **Runtime-mutable daily cap — build an admin API + persistence, or keep the deploy-time flag and stay honest?** (raised in the S-7 scope; needs a human owner)

   - **Option A — runtime-mutable cap (new admin API + persistence).** Scope, grounded in the current code:
     - **Persistence:** no settings/kv table exists today. Add a singleton `agent_settings` table (one column `daily_budget_usd float8`, one row) via a new migration. **The `remainders/README.md` claim of head `1773106066` / next-free `1773106067` is STALE** — `atc/db/migration/migrations/` already contains `1773106080_add_ticket_linkage_to_agent_reviews` and `1773106090_create_agent_outcomes`, so the real head is `1773106090` and the next-free slot is **above** that (e.g. `1773106091`, or the next coordinated number). Do **not** hard-code any number without re-`ls`-ing the migrations dir to confirm the current head AND FLAGGING the chosen slot in `remainders/README.md` first (ticket-core renumber precedent); treat every number quoted here as needing re-verification at build time.
     - **Checker refactor:** today `budget.NewChecker` is built once with `Config{GlobalDailyCapUSD: agentDailyBudgetUSD}` (`atc/api/handler.go:193-195`, also wired at `atc/atccmd/command.go:1421/2145/2497`). `GlobalDailyRemaining()` reads that fixed value. To honor a runtime override the checker must resolve the cap per-call — e.g. `Config` grows a `CapProvider func() (float64, bool)` that reads the settings row, falling back to the flag when unset. This is a real change to the single source of budget truth and must preserve the "0 = uncapped" convention and the daily-window semantics.
     - **Admin-gated route (six-touchpoint):** `PUT /api/v1/agent/settings/daily-budget`. (1) `atc/routes.go` const + entry; (2) `atc/wrappa/api_auth_wrappa.go` — the **admin** tier, i.e. the `CheckAdminHandler` block alongside `CreateAgentPrincipal`/`RevokeAgentPrincipal` (lines 141-156), NOT the team-less agent block; (3) handler; (4) a settings factory read/write in `atc/db`; (5) go-concourse client method; (6) fly + the Elm form.
     - **UI:** the `capCopy` line becomes an admin-only editable field (mirror the principals mint fence's admin affordance and its 403-hides-the-control pattern from `Agent/Agent.elm`).
     - Cost: ~1 migration + ~6 route touchpoints + a checker-contract change + UI form. Roughly a track of its own.
   - **Option B — deploy-time flag, honest page (built by this plan).** The dashboard states exactly where the cap is set (`--agent-daily-budget-usd`) and offers no fake control. Zero new routes, migrations, or checker changes.
   - **Recommendation: Option B for this track; defer Option A to its own track.** The cap is a spend **safety** control; a deploy-time flag with an audit trail is defensible, and Option A introduces a brand-new settings subsystem plus a change to the single-source-of-truth checker that deserves its own review. W-15 already asks only for honest copy. If product wants in-UI cap editing, file Option A as a follow-up with the migration slot re-verified at that time.

2. **Route ownership: does `/agent/spend` belong to S-7 or S-3?** S-3 (IA split, Proposal E) plans the full `/agent/{tickets,runs,reviews,workflows,spend,admin}` set. This plan introduces `/agent/spend` now via the six-touchpoint pattern. **Recommendation:** land it here (S-7 needs a surface and shouldn't block on S-3); S-3 then **reuses** `Routes.AgentSpend` and folds it into the shared sidebar/breadcrumb shell rather than re-minting the route. Note this in the S-3 plan so the two don't collide.

3. **Five separate rollup fetches per page load (day/ticket/workflow/model/step) + the ticket list.** Each is a full ledger aggregation. At a 1-minute poll this is six queries/min per open dashboard — modest, but if it proves heavy, a single `GET /api/v1/agent/costs/summary` endpoint could return all dimensions in one response. **Recommendation:** ship the five-fetch version (reuses the existing endpoint with zero server surface change); only add a bundled endpoint if profiling shows it matters. No decision needed to start.
