# Dispatcher + Budget Admission + Run-Completion Reconciler (plan 11 dispatch remainder)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. This is a REMAINDER plan: it amends and wraps code that landed 2026-07-17 — read "Landed state" first and do NOT rebuild anything listed there.

**Date:** 2026-07-17
**Status:** draft for review
**Depends-on:** `docs/superpowers/plans/agentic-platform/11-dispatch.md` (the original plan; this remainder supersedes its Tasks 8/11/11b/13 text and re-scopes 9/12/14/15), `00-shared-contracts.md` §2.8.2 + the F17 checkpoint-seam delta + §2.7 budget sharing rule, the landed ticket-core / manual-dispatch / harvest-gates-v0.5 slices (all 2026-07-17), `CONVENTIONS.md` C2/C3.

**Goal:** Make `queued` sufficient — an autonomous, budget-admitted Dispatcher component that wraps the landed `DispatchOne`, plus real per-ticket budget enforcement at both checker construction sites, plus the run-completion reconciler that walks `running` tickets home when their run dies before harvest.

**Architecture:** Everything new lives in `agent/dispatch` (plain Go, hand fakes) plus additive seams elsewhere: `dispatch.TicketBudgets` implements the frozen `budget.TicketBudgets` seam and replaces `budget.NoTicketBudgets{}` at the engine and costs-API construction sites (which instantly arms the ALREADY-LANDED step-level enforcement in `atc/exec/agent_step.go:282-342`); a `Dispatcher` RunnableComponent (`atc.ComponentAgentDispatcher`, polling-only, K8s-gated, off by default behind `--agent-dispatcher-enabled`) lists queued tickets and calls the landed `DispatchOne` per ticket, then runs `reconcileCompletedRuns` applying the F17 decision tree via the single-writer `Transition`, reading runs through an additive `db.PipelineRunFactory.GetRunByID` (co-signed pipeline-runs). Budget admission lives INSIDE `DispatchOne` behind a nil-tolerant `Deps.Budget` so the manual route and the loop share one gate; over-cap tickets STAY queued. The F17 checkpoint branches are implemented but dormant behind a nil-tolerant `QuestionLister` seam (checkpoints are render-refused today; plan 08 plugs in later).

**Tech stack:** Go (`agent/dispatch`, `agent/api/tickets`, `atc/db`, `atc/api`, `atc/atccmd`), plain `testing` + hand fakes + counterfeiter fakes for `agent/dispatch`, Ginkgo/Gomega for `atc/db`, client-go fake clientset for the labeler, theborg live cluster for the end-to-end proof.

---

## 1. Landed state — do NOT rebuild

Verified on `jetbridge` @ `b88d124540`, re-confirmed against HEAD `187cad4926` (2026-07-17 — the workflow-source slice-a advanced the branch 8 commits; deployed migration head is now `1773106066`, and `render.go` gained an additional source-format refusal — see the refusals row below). The executor integrates with these surfaces; re-implementing any of them is an error.

| Surface | Where | Commits |
|---|---|---|
| Renderer core: `RenderInput`/`RenderAgentStep`/`Render` → `template:true` pipeline, one entry job `run`, optional repo git resource, write-ticket busybox task, §8.1 identity env (`AGENT_TICKET_ID` literal, `AGENT_PIPELINE_RUN_ID=((run_id))` per F30), §6.2 prompt templates | `agent/dispatch/render.go:29-341` | `5efeb7792a`, `c2f138e8bc`, `f313727a99` |
| Terminal harvest emission incl. full-scope `gate_policy` pass-through (harvest gates v0.5) | `render.go:146-156`, `render.go:207-226` | `2dbb9dc3fe`, `59d3410745` |
| `DispatchOne(ctx, deps, ticketID, dispatchedBy) (Result, error)`: get+verify QUEUED → resolve live-or-pinned workflow → FREEZE version via non-state `Update` → render → `SaveTemplate` `agent-ticket-<id>` on main (update-in-place re-dispatch) → `CreateRun(pipelineID, nil, dispatchedBy)` → attach secret → `Transition(queued→running, PipelineRunID)`. Pre-transition failures leave the ticket queued (retry-safe) | `agent/dispatch/dispatch.go:88-180`; `NewTeamTemplateSaver` `dispatch.go:264-295` | `c2f138e8bc` |
| Landed `Deps` shape: `{Tickets, Workflows, Templates, Runs, Principals, Credentials, Secrets, ATCExternalURL, RepoBaseURL}` — NOT the original plan-11 Task 9 shape (`Resolver`/`Budget`/`SecretLabeler`/`Team`/`RunTimeout`/`ParkTimeout` never landed) | `dispatch.go:52-69` | `c2f138e8bc`, `9a8eaf452c` |
| Per-run principal mint + `agent-run-<id>` secret attach at dispatch: `resolveRunCredential` (user-first when `t.UserID` set, platform fallback, OAuth over API key), principal name `run-<runID>`, 4 scopes, hardcoded 24h expiry; `lazySecretAttacher` bridge bound in the K8s block | `dispatch.go:188-258`; `atc/atccmd/command.go:1366-1369, 2411-2450` | `9a8eaf452c` |
| `DispatchAgentTicket` route through all six C1 touchpoints, member-tier human-only; 422/409/404 error mapping | `agent/dispatch/handler.go:18-49`; `atc/routes.go:153,320`; wrappa/roles/auditor/handler/command.go | `4c1410a27f`, `99ffd8c2eb` |
| Render-time refusals (sidecars, checkpoints, mcp/empty spec_delivery, non-full-scope gates, hitl, judge, **source-format surfaces**) → 422 | `render.go:57, 60, 128, 155, 158, 161` + source-format refusal `render.go:168-170` (`SourceFormatField()`) | `b4965aa126`, `99ffd8c2eb`, `59d3410745`, `2db630c3e9` |
| `fly agent tickets` full UX incl. `dispatch`, `watch`, `close` | `fly/commands/agent_tickets.go:33-363` | `abe33459bd`, `4c1410a27f`, `f4543940da` |
| Budget library complete: `Checker` (`TicketRemaining`/`GlobalDailyRemaining`/`StepSlice`/`Record`), `Ledger` (judge-spend exclusion in `SpentForTicket`), `TicketBudgets` seam + `NoTicketBudgets` stub, counterfeiter fakes | `agent/budget/` | wave 1 |
| **Step-level slice enforcement FULLY WIRED**: server-verified ticket linkage, `StepSlice` re-resolution per execution, fail-closed on exhausted ticket, attach-only resume. Inert only because both `NewChecker` sites pass `budget.NoTicketBudgets{}` | `atc/exec/agent_step.go:282-342`; sites `command.go:2057-2063` (engine), `atc/api/handler.go:182` (costs API) | landed |
| `agent_tickets.budget_usd` + `user_id` + `attempt_count` columns (migration 1773106062), `Ticket.BudgetUSD/UserID`, `fly create --budget`, negative-budget 400 | `atc/db/migration/migrations/1773106062_create_agent_tickets.up.sql`; `agent/api/tickets/types.go:118-119` | ticket-core |
| Harvest exec owns ticketed close-out for runs that REACH harvest: exit 0/1 → `running→needs_review` (branch on 0+push), exit 2 → `errored`, process-level runErr → `errored`; `ErrStaleTransition` benign | `atc/exec/harvest_step.go:262-274, 327-332` | `1aef877c49` |
| `pipeline_run_lifecycler` (CheckComplete/Finish/Reopen/Archive) + `agent_run_secret_reaper` components already running | `atc/component.go:26,29`; `command.go:1283-1288, 1371-1383`; `atc/runlifecycle/lifecycler.go` | pipeline-runs |
| DB-backed dispatch spec (render → SavePipeline → CreateRun over real stores) | `atc/db/agent_dispatch_test.go` | manual-dispatch slice |
| `tickets.MemoryStore`, `dbfakes.FakePipelineRunFactory`, `budgetfakes.FakeChecker`, `credentialsfakes.FakeSecretAttacher` — reuse these in tests | `agent/api/tickets/memory_store.go`, `atc/db/dbfakes/`, `agent/budget/budgetfakes/` | landed |

Landed facts the tasks below key on:

- `validTransitions` (`agent/api/tickets/types.go:41-49`): `queued → {running, draft, abandoned}` ONLY. **`queued→errored` is NOT a legal edge.** `running → {queued, needs_review, failed, errored}`; `running→queued` bumps `attempt_count` (DB factory `agent_tickets_factory.go:230-236`).
- `agent_tickets` has NO NOTIFY trigger (verified in `1773106062_create_agent_tickets.up.sql`) → the component is polling-only (see Migration allocations).
- `db.PipelineRunFactory` has `GetRun(templateID, number)` but NO `GetRunByID` (verified `atc/db/pipeline_run_factory.go:31-63`).
- `atc.ComponentAgentDispatcher` does not exist (verified `atc/component.go`).
- `Ticket.UserID` is never written anywhere; the create handler comment says "dispatch resolves user_id from users.username in wave 4" (`agent/api/tickets/handler.go:130`).
- `credentials.RunSecretName(runID)` = `agent-run-<id>` (`agent/credentials/secret_attacher.go:26-29`) — the secret name is CONSUMED by the exec; never change it.
- Components without a NOTIFY trigger poll at `defaultComponentInterval = 10s` (`command.go:819`).
- `db.NewPipelineRunFactory(logger, dbConn, lockFactory, checkFactory)` — the constructor gained `logger` and `checkFactory` (F27); plan 11's Task 13 snippet predates this.

---

## 2. Scope

### In

1. **Budget admission live** — `dispatch.TicketBudgets` (the real seam impl), the checker swap at BOTH construction sites, and dispatch-time admission (`TicketRemaining` + `GlobalDailyRemaining`) inside `DispatchOne` behind a nil-tolerant `Deps.Budget`.
2. **Dispatcher RunnableComponent** — the polling loop wrapping the landed `DispatchOne`, per-ticket error isolation, outcome classification (deferred / raced / refused / platform-fault).
3. **Run-completion reconciler** — same component, second pass: F17 decision tree over completed runs, checkpoint branches implemented but dormant behind a nil-tolerant `QuestionLister`, attempt cap enforced on the REQUEUE edge (no ticket-core matrix change — see Risks R1), additive `db.PipelineRunFactory.GetRunByID` (co-signed pipeline-runs).
4. **Task 9 remainder** — `Ticket.UserID` resolution at dispatch (additive `tickets.Update.UserID` + `db.NewAgentUserLookup`), expired-credential check with owner named, principal rename `run-<id>` → `agent-run-<id>` per §2.8.2 + `questions:answer` scope + expiry from a new `--agent-run-timeout` flag, best-effort `concourse/ticket` secret label.
5. **Wiring** — `atc.ComponentAgentDispatcher`, `--agent-dispatcher-enabled` (default OFF), `--agent-dispatcher-max-attempts`, `--agent-run-timeout`; K8s-gated, reusing the ONE `lazySecretAttacher` via `cmd.agentRunSecrets()`.
6. **Proofs** — DB-backed loop/admission/reconciler specs extending `atc/db/agent_dispatch_test.go`; theborg live end-to-end autonomous dispatch.
7. **Docs** — SS11 corrective + landing amendments, plan-11 amendment-log entry, hand-dispatch README touch (all that remains of Task 15).

### Out (stays deferred; named owner)

- **PARK-V2 legs split by owner (do NOT read this as "all deferred to platform-mcp-hitl").** The **run-lifecycle halves live OUTSIDE the remainders**: `reconcileAwaitingRuns` + `awaiting_human` status (migration 1773106032, not landed) + `CreateContinuationBuild` ship with **plan 11-proper / plan 03**, NOT the platform-mcp-hitl remainder — its own Scope-Out correctly punts `reconcileAwaitingRuns` back to plan 11, so NEITHER remainder owns it and it is not a remainder deliverable (do not misread the reciprocal pointers as coverage). The **platform-mcp-hitl remainder** owns only the sidecar/flag halves: `--agent-park-timeout`/`--agent-short-park-max`, `PLATFORM_MCP_SHORT_PARK_MAX_SECONDS`, and **park-aware principal expiry**. That last leg collides with Task 8: platform-mcp-hitl's Task 26 rewrites the SAME `attachRunSecret` mint block this plan's Task 8 owns. Per Task 8's cross-plan ownership box, Task 26 LAYERS its `ParkTimeout` expiry on top of Task 8's landed `agent-run-<id>` name + `questions:answer` scope via the workflow-conditional `dispatch.RunPrincipalTimeout(cfg, runTimeout, parkTimeout)` selector (ordinary run → `RunTimeout` 6h; park/checkpoint workflow → `ParkTimeout` 72h; NO `max(...)`, NO `+12h` margin), never re-diff from the pre-rename baseline. `RevokeByName` wiring stays here. Plan 11 Task 11c is untouched by this remainder. The `agent_run_step_state` migration was reserved at 1773106065 but is now STRANDED BELOW the deployed head (1773106066, workflow-source manifest); it renumbers above head at its own land time (the hole rule).
- **Live checkpoint branches** → plan 08 (platform-mcp-hitl): `agent_run_questions` (~1773106072) does not exist; checkpoints are render-refused. This plan ships the tree with those branches unit-tested against fakes and `nil`-disabled in production wiring.
- **NOTIFY trigger on `agent_tickets`** → not taken (Migration allocations). Polling-only at 10s.
- **Sidecars / spec_delivery mcp / judge / affected-scope gates** → wave 3 owners; refusals stay.
- **Per-ticket model override** → not in scope; workflow-level `defaults.model` / step `model` is the mechanism today (a ticket varies its model only by pointing at a different workflow name/version).
- **Renderer/`RenderInput` changes** → none. This plan does not touch `render.go`.
- **Elm/UI** → none needed; the queue/detail pages and the `/agent` daily-cap gauge landed with the agentic-ui wave.

---

## 3. Slices and verification stories

Each slice is independently shippable in the order A → B → C → D (D can land any time after A; E/F trail). Every slice's verification splits three ways:

**Slice A — Budget admission live (Tasks 1-3).**
- *Gate-verifiable:* `go test ./agent/dispatch/` (TicketBudgets + admission unit tests, hand fakes + `budgetfakes.FakeChecker`); `go build ./atc/... ./agent/...`; `go vet ./...`. All pure Go.
- *Local-verify (human, Postgres):* `ginkgo --focus="over-budget" ./atc/db/` (Task 11's admission spec, if landing together) — otherwise none; the swap sites are compile-checked.
- *Live-verify:* covered by Slice F step 6 (a capped ticket defers on theborg).

**Slice B — Dispatcher loop + wiring (Tasks 4, 10).**
- *Gate-verifiable:* `go test ./agent/dispatch/ -run TestDispatcher`; `go build ./atc/...`; `go vet ./atc/atccmd/`.
- *Local-verify:* `pg_isready && ginkgo --focus="dispatcher loop" ./atc/db/` (Task 11).
- *Live-verify:* Slice F — flag on, ticket auto-dispatches ≤ ~15s.

**Slice C — Run-completion reconciler (Tasks 5-6).**
- *Gate-verifiable:* `go test ./agent/dispatch/ -run TestReconcile` (full F17 tree over fakes, incl. dormant checkpoint branches).
- *Local-verify:* `pg_isready && ginkgo --focus="gets a run by its global id" ./atc/db/` and Task 11's reconciler spec (Postgres). See Risks R5 before loop-dispatching anything that adds `atc/db` specs.
- *Live-verify:* Slice F step 5 — kill a run pre-harvest, watch the ticket walk to `needs_review` untouched.

**Slice D — Credential/principal/user hardening (Tasks 7-9).**
- *Gate-verifiable:* `go test ./agent/dispatch/` (user-resolution, expiry, principal-shape, labeler-with-fake-clientset tests); `go test ./agent/api/tickets/`.
- *Local-verify:* `ginkgo --focus="agent user lookup" ./atc/db/` + tickets-factory `Update.UserID` spec.
- *Live-verify:* Slice F step 7 — secret carries `concourse/ticket` label; principal row named `agent-run-<id>` with 5 scopes and flag-driven expiry; ticket row has `user_id`.

**Slice E — Integration specs + docs (Tasks 11-12).**
- *Gate-verifiable:* docs none; specs compile in-gate.
- *Local-verify:* `pg_isready && ginkgo ./atc/db/ --focus="dispatch"` — the whole extended file green.
- *Live-verify:* n/a.

**Slice F — Live proof (Task 13).** Native-only, theborg. No gate or local leg.

---

## 4. Tasks

### Task 1 [Slice A]: `dispatch.TicketBudgets` — real `budget.TicketBudgets` implementation

**Amends plan 11 Task 8.** Deltas from the original text: (1) the `RenderResolver` type never landed — the impl takes the LANDED `WorkflowResolver` (`dispatch.go:35-38`, `Live`/`Get`) instead; (2) it honors the ticket's PINNED version (`t.WorkflowVersion != nil` → `Get`, else `Live`) — `DispatchOne` freezes the version at dispatch (`dispatch.go:123-128`), so post-dispatch budget resolution must read the frozen config; (3) store errors PROPAGATE (the original swallowed them into "uncapped" — fail-open on a budget read error would bypass the step exec's fail-closed design at `agent_step.go:299-313`); not-found still means uncapped.

**Files:**
- Create: `agent/dispatch/budgets.go`
- Test: `agent/dispatch/budgets_test.go`

**Steps:**

- [ ] **Step 1: Write the failing test** `agent/dispatch/budgets_test.go`:

```go
package dispatch_test

import (
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/agent/workflow"
)

type budgetTicketGetter struct {
	rows map[int]tickets.Ticket
	err  error
}

func (g budgetTicketGetter) Get(id int) (*tickets.Ticket, bool, error) {
	if g.err != nil {
		return nil, false, g.err
	}
	t, ok := g.rows[id]
	if !ok {
		return nil, false, nil
	}
	return &t, true, nil
}

func budgetWorkflows() *fakeWorkflows {
	return &fakeWorkflows{byName: map[string]*workflow.Definition{
		"standard-dev": {Name: "standard-dev", Version: 4, Live: true,
			Config: workflow.Config{Name: "standard-dev", Budget: workflow.Budget{TicketUSD: 15}}},
	}}
}

func TestTicketBudgetsTicketOverrideWinsOverWorkflowDefault(t *testing.T) {
	budget20 := 20.0
	v4 := 4
	getter := budgetTicketGetter{rows: map[int]tickets.Ticket{
		7: {ID: 7, WorkflowName: "standard-dev", BudgetUSD: &budget20},
		8: {ID: 8, WorkflowName: "standard-dev"},                       // live resolution
		9: {ID: 9, WorkflowName: "standard-dev", WorkflowVersion: &v4}, // pinned resolution
	}}
	tb := dispatch.NewTicketBudgets(getter, budgetWorkflows())

	got, found, err := tb.BudgetUSD(7)
	if err != nil || !found || got != 20.0 {
		t.Errorf("ticket override: got=%v found=%v err=%v, want 20/true/nil", got, found, err)
	}
	got, found, err = tb.BudgetUSD(8)
	if err != nil || !found || got != 15.0 {
		t.Errorf("live workflow default: got=%v found=%v err=%v, want 15/true/nil", got, found, err)
	}
	got, found, err = tb.BudgetUSD(9)
	if err != nil || !found || got != 15.0 {
		t.Errorf("pinned workflow default: got=%v found=%v err=%v, want 15/true/nil", got, found, err)
	}
}

func TestTicketBudgetsUncappedCases(t *testing.T) {
	getter := budgetTicketGetter{rows: map[int]tickets.Ticket{
		5: {ID: 5}, // no workflow, no budget
	}}
	tb := dispatch.NewTicketBudgets(getter, budgetWorkflows())

	if _, found, err := tb.BudgetUSD(99); err != nil || found {
		t.Errorf("unknown ticket must be uncapped: found=%v err=%v", found, err)
	}
	if _, found, err := tb.BudgetUSD(5); err != nil || found {
		t.Errorf("workflow-less ticket must be uncapped: found=%v err=%v", found, err)
	}
}

func TestTicketBudgetsPropagatesStoreErrors(t *testing.T) {
	boom := errors.New("db down")
	tb := dispatch.NewTicketBudgets(budgetTicketGetter{err: boom}, budgetWorkflows())
	if _, _, err := tb.BudgetUSD(7); !errors.Is(err, boom) {
		t.Fatalf("store errors must propagate (fail-open would bypass step fail-closed), got %v", err)
	}
}
```

(`fakeWorkflows` is the landed fake in `agent/dispatch/dispatch_test.go:40-55` — same package, reuse it.)

- [ ] **Step 2: Run to verify failure**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ -run TestTicketBudgets`
Expected: FAIL — `undefined: dispatch.NewTicketBudgets`.

- [ ] **Step 3: Write `agent/dispatch/budgets.go`:**

```go
package dispatch

import (
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/workflow"
)

// TicketGetter is the subset of tickets.Store the budget resolver reads.
type TicketGetter interface {
	Get(id int) (*tickets.Ticket, bool, error)
}

// TicketBudgets implements budget.TicketBudgets (§2.7) with the real
// "tickets.budget_usd ?? workflow default ticket_usd" rule the wave-1
// NoTicketBudgets stub stood in for (§2.8.2). Version resolution matches
// DispatchOne: a pinned ticket reads its FROZEN definition, an unpinned
// one reads live (only possible pre-dispatch; DispatchOne pins at claim).
type TicketBudgets struct {
	tickets   TicketGetter
	workflows WorkflowResolver
}

var _ budget.TicketBudgets = (*TicketBudgets)(nil)

func NewTicketBudgets(tg TicketGetter, wf WorkflowResolver) *TicketBudgets {
	return &TicketBudgets{tickets: tg, workflows: wf}
}

// BudgetUSD returns the effective ticket budget. found=false means
// UNCAPPED (unknown ticket, workflow-less ticket, unresolvable or
// default-less workflow). Store errors PROPAGATE so the Checker's callers
// keep their fail-closed semantics (agent_step.go:299-313) instead of
// silently treating a broken budget read as "no cap".
func (b *TicketBudgets) BudgetUSD(ticketID int) (float64, bool, error) {
	t, ok, err := b.tickets.Get(ticketID)
	if err != nil {
		return 0, false, err
	}
	if !ok {
		return 0, false, nil
	}
	if t.BudgetUSD != nil && *t.BudgetUSD > 0 {
		return *t.BudgetUSD, true, nil
	}
	if t.WorkflowName == "" {
		return 0, false, nil
	}

	var def *workflow.Definition
	var found bool
	if t.WorkflowVersion != nil {
		def, found, err = b.workflows.Get(t.WorkflowName, *t.WorkflowVersion)
	} else {
		def, found, err = b.workflows.Live(t.WorkflowName)
	}
	if err != nil {
		return 0, false, err
	}
	if !found {
		// Dangling workflow ref: dispatch errors this ticket loudly
		// elsewhere; budget-wise it is uncapped, not broken.
		return 0, false, nil
	}
	if def.Config.Budget.TicketUSD > 0 {
		return def.Config.Budget.TicketUSD, true, nil
	}
	return 0, false, nil
}
```

- [ ] **Step 4: Run to verify pass**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ -run TestTicketBudgets`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add agent/dispatch/budgets.go agent/dispatch/budgets_test.go
git commit -m "feat(dispatch): dispatch.TicketBudgets - real budget.TicketBudgets impl (ticket override ?? frozen-workflow default)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2 [Slice A]: Dispatch-time budget admission inside `DispatchOne`

**New task** (the admit fragment of plan 11 Task 10, relocated). Admission lives INSIDE `DispatchOne` behind a nil-tolerant `Deps.Budget` so the manual route and the loop share ONE gate. §2.7 rule: over-cap dispatches STAY QUEUED (never failed); platform faults error (ticket also stays queued — `DispatchOne` is pre-transition retry-safe). The route maps the new sentinel to 409 (state conflict, retryable after budget changes; 422 stays reserved for malformed-workflow refusals).

**Files:**
- Modify: `agent/dispatch/dispatch.go` (new `Deps.Budget` field, `ErrBudgetExhausted`, admission block)
- Modify: `agent/dispatch/handler.go` (409 mapping)
- Test: `agent/dispatch/dispatch_test.go` (extend), `agent/dispatch/handler_test.go` (extend)

**Steps:**

- [ ] **Step 1: Write the failing tests** — append to `agent/dispatch/dispatch_test.go`:

```go
func TestDispatchOneDefersWhenTicketBudgetExhausted(t *testing.T) {
	deps, store, _, runs := dispatchDeps(t)
	checker := new(budgetfakes.FakeChecker)
	checker.TicketRemainingReturns(budget.Remaining{LimitUSD: 5, SpentUSD: 6, RemainingUSD: -1, Exhausted: true}, nil)
	deps.Budget = checker
	id := queuedTicket(t, store, "smoke")

	_, err := dispatch.DispatchOne(context.Background(), deps, id, "loop")
	if !errors.Is(err, dispatch.ErrBudgetExhausted) {
		t.Fatalf("want ErrBudgetExhausted, got %v", err)
	}
	if runs.CreateRunCallCount() != 0 {
		t.Error("over-cap admission must run BEFORE CreateRun")
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued {
		t.Errorf("over-cap ticket must STAY queued (never failed), state=%s", got.State)
	}
}

func TestDispatchOneDefersWhenGlobalDailyCapExhausted(t *testing.T) {
	deps, store, _, runs := dispatchDeps(t)
	checker := new(budgetfakes.FakeChecker)
	checker.TicketRemainingReturns(budget.Remaining{}, nil) // uncapped ticket
	checker.GlobalDailyRemainingReturns(budget.Remaining{LimitUSD: 50, SpentUSD: 50, Exhausted: true}, nil)
	deps.Budget = checker
	id := queuedTicket(t, store, "smoke")

	_, err := dispatch.DispatchOne(context.Background(), deps, id, "loop")
	if !errors.Is(err, dispatch.ErrBudgetExhausted) {
		t.Fatalf("want ErrBudgetExhausted, got %v", err)
	}
	if runs.CreateRunCallCount() != 0 {
		t.Error("daily-cap admission must run BEFORE CreateRun")
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued {
		t.Errorf("daily-capped ticket must stay queued, state=%s", got.State)
	}
}

func TestDispatchOneBudgetCheckerErrorIsPlatformFaultNotDeferral(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	checker := new(budgetfakes.FakeChecker)
	checker.TicketRemainingReturns(budget.Remaining{}, errors.New("ledger down"))
	deps.Budget = checker
	id := queuedTicket(t, store, "smoke")

	_, err := dispatch.DispatchOne(context.Background(), deps, id, "loop")
	if err == nil || errors.Is(err, dispatch.ErrBudgetExhausted) {
		t.Fatalf("checker error must surface as a platform fault, got %v", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued {
		t.Errorf("platform fault leaves ticket queued, state=%s", got.State)
	}
}

func TestDispatchOneNilBudgetSkipsAdmission(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t) // deps.Budget nil
	id := queuedTicket(t, store, "smoke")
	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "admin"); err != nil {
		t.Fatalf("nil Budget must preserve landed behavior: %v", err)
	}
}
```

Add imports to the test file: `"github.com/concourse/concourse/agent/budget"`, `"github.com/concourse/concourse/agent/budget/budgetfakes"`.

- [ ] **Step 2: Run to verify failure**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ -run TestDispatchOne`
Expected: FAIL — `deps.Budget undefined` / `undefined: dispatch.ErrBudgetExhausted`.

- [ ] **Step 3: Implement.** In `agent/dispatch/dispatch.go`:

(a) add to the `var (...)` error block:

```go
	// ErrBudgetExhausted: admission refused — the ticket or the global
	// daily cap has no headroom (§2.7). The ticket STAYS QUEUED; the
	// loop logs it as deferred and the route maps it to 409. Never a
	// ticket-state transition: budgets recover (midnight, raised cap).
	ErrBudgetExhausted = errors.New("budget exhausted; dispatch deferred")
```

(b) add to `Deps` (after `Runs`):

```go
	// Budget, when non-nil, gates admission per §2.7: TicketRemaining +
	// GlobalDailyRemaining are consulted BEFORE any side effect beyond
	// the version freeze. nil skips admission (tests; pre-budget wiring).
	Budget budget.Checker
```

with import `"github.com/concourse/concourse/agent/budget"`.

(c) insert the admission block in `DispatchOne` immediately after the `t.WorkflowName == ""` check (before workflow resolution — no reason to render an inadmissible ticket):

```go
	if deps.Budget != nil {
		tr, err := deps.Budget.TicketRemaining(ticketID)
		if err != nil {
			return Result{}, fmt.Errorf("budget admission for ticket %d: %w", ticketID, err)
		}
		if tr.Exhausted {
			return Result{}, fmt.Errorf("%w: ticket %d spent $%.2f of $%.2f", ErrBudgetExhausted, ticketID, tr.SpentUSD, tr.LimitUSD)
		}
		gr, err := deps.Budget.GlobalDailyRemaining()
		if err != nil {
			return Result{}, fmt.Errorf("budget admission (global daily): %w", err)
		}
		if gr.Exhausted {
			return Result{}, fmt.Errorf("%w: global daily cap spent $%.2f of $%.2f", ErrBudgetExhausted, gr.SpentUSD, gr.LimitUSD)
		}
	}
```

(d) in `agent/dispatch/handler.go`, add a case to the error switch BEFORE the generic `err != nil` case:

```go
		case errors.Is(err, ErrBudgetExhausted):
			http.Error(w, err.Error(), http.StatusConflict)
			return
```

- [ ] **Step 4: Handler test** — append to `agent/dispatch/handler_test.go` a spec that wires a `budgetfakes.FakeChecker` returning `Exhausted: true` into the handler's `Deps.Budget`, POSTs a dispatch for a queued ticket, and asserts status 409 with body containing `budget exhausted`. Model it on the existing 422/409 handler tests in that file (reuse their request/store scaffolding verbatim).

- [ ] **Step 5: Run to verify pass**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/`
Expected: PASS (all new + all landed tests — the nil-Budget test proves no regression).

- [ ] **Step 6: Commit**

```bash
git add agent/dispatch/dispatch.go agent/dispatch/handler.go agent/dispatch/dispatch_test.go agent/dispatch/handler_test.go
git commit -m "feat(dispatch): budget admission in DispatchOne - over-cap defers (stays queued), faults error, route maps 409" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3 [Slice A]: Swap `NoTicketBudgets` for the real seam at BOTH checker sites

**New task** (was implicit in plan 11 Task 13). This is the switch that ARMS the landed step-level enforcement. Two sites (C3: `budget.NoTicketBudgets` stays in the budget package for tests/rollback — nothing dropped):

1. **Engine** (`atc/atccmd/command.go:2057-2063`, in `constructEngine` — `dbConn` is a parameter): agent-step admission goes live.
2. **Costs API** (`atc/api/handler.go:182`): the console gauge / rollup reflect true per-ticket caps. `atc/api` deliberately does NOT import `agent/dispatch` (the dispatch handler is injected pre-built at `handler.go:111-113`) — keep it that way by passing the seam IN as a parameter.

**Files:**
- Modify: `atc/atccmd/command.go` (engine site + pass the seam to `api.NewHandler`)
- Modify: `atc/api/handler.go` (additive `ticketBudgets budget.TicketBudgets` param; use at :182)
- Modify: the API test suite's `api.NewHandler` call (find it; pass `budget.NoTicketBudgets{}`)

**Steps:**

- [ ] **Step 1: Engine site.** In `constructEngine` (`command.go:2057`), replace `budget.NoTicketBudgets{}` with:

```go
		dispatch.NewTicketBudgets(db.NewAgentTicketsFactory(dbConn), db.NewAgentWorkflowsFactory(dbConn)),
```

`agent/dispatch` is already imported by `command.go` (the route wiring at :2395). Update the comment above the construction — "wave 1 has no tickets table" is stale.

- [ ] **Step 2: Costs-API site.** In `atc/api/handler.go` add a parameter `ticketBudgets budget.TicketBudgets` immediately after `agentDailyBudgetUSD float64` in `NewHandler`, and change line 182 to:

```go
	costChecker := budget.NewChecker(costLedger, ticketBudgets, budget.Config{
		GlobalDailyCapUSD: agentDailyBudgetUSD,
	})
```

- [ ] **Step 3: Update the callers.** Find them:

Run: `cd /Users/tdmtrader/concourse/concourse && grep -rn "api.NewHandler(\|NewHandler(" atc/atccmd/ atc/api/ | grep -v "func NewHandler"`

In `command.go`'s call (right after `cmd.AgentDailyBudgetUSD` at :2393), pass:

```go
		dispatch.NewTicketBudgets(db.NewAgentTicketsFactory(dbConn), db.NewAgentWorkflowsFactory(dbConn)),
```

In the API test suite's call, pass `budget.NoTicketBudgets{}` (add the `agent/budget` import if missing).

- [ ] **Step 4: Build + vet + targeted tests**

Run: `cd /Users/tdmtrader/concourse/concourse && go build ./atc/... && go vet ./atc/atccmd/ ./atc/api/ && go test ./agent/dispatch/ ./agent/budget/`
Expected: clean build, PASS.

- [ ] **Step 5: Commit**

```bash
git add atc/atccmd/command.go atc/api/handler.go <api-test-suite-file>
git commit -m "feat(budget): arm real per-ticket budgets at both checker sites (engine + costs API)" -m "Step-level enforcement in agent_step.go goes live; NoTicketBudgets retained for tests." -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4 [Slice B]: `Dispatcher` loop — wraps the landed `DispatchOne`

**Amends plan 11 Task 11.** Deltas: (1) wraps the LANDED `Deps` + `DispatchOne(ctx, deps, ticketID, dispatchedBy)` signature (per plan 11's own 2026-07-17 amendment: "Task 10's loop calls the landed DispatchOne"), not the never-landed Task-9 deps shape. (2) **NO `queued→errored` attempt cap** — that edge is not in the landed matrix (`types.go:41-49`); the cap moves to the reconciler's requeue edge (Task 6), and the loop dispatches ANY queued ticket (queued is explicit intent: either the reconciler chose to requeue under cap, or a human re-queued — the loop respects both). See Risks R1. (3) Outcome classification is by error value (the landed `DispatchOne` returns `(Result, error)`, not the original plan's `Outcome` enum): `ErrBudgetExhausted` → deferred (Info, ticket stays queued), `ErrNotQueued`/`ErrStaleTransition` → raced (Debug, benign), refusals → Error log but non-fatal, anything else → platform fault (Error, retried next pass).

**Files:**
- Create: `agent/dispatch/dispatcher.go`
- Test: `agent/dispatch/dispatcher_test.go`

**Steps:**

- [ ] **Step 1: Write the failing test** `agent/dispatch/dispatcher_test.go`:

```go
package dispatch_test

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/budget/budgetfakes"
	"github.com/concourse/concourse/agent/dispatch"
)

// loopDeps builds Deps around the landed test scaffolding (dispatchDeps)
// with n queued tickets in the MemoryStore.
func loopDeps(t *testing.T, n int) (dispatch.Deps, *tickets.MemoryStore, []int) {
	t.Helper()
	deps, store, _, _ := dispatchDeps(t)
	ids := make([]int, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, queuedTicket(t, store, "smoke"))
	}
	return deps, store, ids
}

func TestDispatcherDispatchesEachQueuedTicket(t *testing.T) {
	deps, store, ids := loopDeps(t, 2)
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{})
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, id := range ids {
		got, _, _ := store.Get(id)
		if got.State != tickets.StateRunning {
			t.Errorf("ticket %d state = %s, want running", id, got.State)
		}
	}
}

func TestDispatcherOverBudgetTicketStaysQueuedAndPassContinues(t *testing.T) {
	deps, store, ids := loopDeps(t, 2)
	checker := new(budgetfakes.FakeChecker)
	// First ticket exhausted, second admitted.
	checker.TicketRemainingStub = func(ticketID int) (budget.Remaining, error) {
		if ticketID == ids[0] {
			return budget.Remaining{LimitUSD: 1, SpentUSD: 2, Exhausted: true}, nil
		}
		return budget.Remaining{}, nil
	}
	deps.Budget = checker
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{})
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	first, _, _ := store.Get(ids[0])
	second, _, _ := store.Get(ids[1])
	if first.State != tickets.StateQueued {
		t.Errorf("over-cap ticket must stay queued, got %s", first.State)
	}
	if second.State != tickets.StateRunning {
		t.Errorf("deferral must not starve the pass; second = %s, want running", second.State)
	}
}

func TestDispatcherPlatformFaultIsolatedPerTicket(t *testing.T) {
	deps, store, ids := loopDeps(t, 2)
	// Poison the first ticket: dangling workflow ref → refused, stays queued.
	bad := "no-such-workflow"
	if err := store.Update(ids[0], tickets.Update{WorkflowName: &bad}); err != nil {
		t.Fatal(err)
	}
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{})
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("a per-ticket failure must not fail the pass: %v", err)
	}
	first, _, _ := store.Get(ids[0])
	second, _, _ := store.Get(ids[1])
	if first.State != tickets.StateQueued {
		t.Errorf("refused ticket stays queued, got %s", first.State)
	}
	if second.State != tickets.StateRunning {
		t.Errorf("second ticket must still dispatch, got %s", second.State)
	}
}

func TestDispatcherListFailureReturnsError(t *testing.T) {
	deps, _, _ := loopDeps(t, 0)
	deps.Tickets = failingListStore{}
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{})
	if err := d.Run(context.Background()); err == nil {
		t.Fatal("listing failure must surface (component retries on interval)")
	}
}

type failingListStore struct{ tickets.Store }

func (failingListStore) List(tickets.ListFilter) ([]tickets.Ticket, error) {
	return nil, errors.New("db down")
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ -run TestDispatcher`
Expected: FAIL — `undefined: dispatch.NewDispatcher` / `dispatch.LoopConfig`.

- [ ] **Step 3: Write `agent/dispatch/dispatcher.go`:**

```go
package dispatch

import (
	"context"
	"errors"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"

	"github.com/concourse/concourse/agent/api/tickets"
)

// LoopConfig tunes the Dispatcher beyond DispatchOne's Deps.
type LoopConfig struct {
	// RunReader powers the run-completion reconciler (reconcile.go). nil
	// skips that pass entirely.
	RunReader RunReader
	// Questions is the plan-08 checkpoint seam. nil = no checkpoint rows
	// can exist (checkpoints are render-refused; agent_run_questions is
	// not landed) — the reconciler treats every completed run as
	// checkpoint-free. Plan 08 wires the real store later.
	Questions QuestionLister
	// MaxAttempts caps the reconciler's automatic running→queued
	// re-dispatches (§2.1 bumps attempt_count on that edge). <=0 =
	// uncapped. NEVER enforced against queued tickets: queued→errored is
	// not a legal edge (§1.7) and a human re-queue is explicit intent.
	MaxAttempts int
}

// Dispatcher is the RunnableComponent behind atc.ComponentAgentDispatcher.
// Polling-only (agent_tickets has no NOTIFY trigger; never notify-only per
// the fork's dropped-notification lesson) at the component framework's
// default 10s interval. The Coordinator lock serializes Run across web
// nodes; DispatchOne's guarded queued→running Transition is the intra-pass
// claim, so even a lost lock degrades to redundant-but-safe work.
type Dispatcher struct {
	deps Deps
	cfg  LoopConfig
}

func NewDispatcher(deps Deps, cfg LoopConfig) *Dispatcher {
	return &Dispatcher{deps: deps, cfg: cfg}
}

// Run dispatches every currently-queued ticket, then reconciles completed
// runs. Per-ticket failures never abort the pass (a poison ticket must not
// starve the queue); only listing failures return an error.
func (d *Dispatcher) Run(ctx context.Context) error {
	logger := lagerctx.FromContext(ctx).Session("agent-dispatcher")
	logger.Debug("start")
	defer logger.Debug("done")

	if err := d.dispatchQueued(ctx, logger); err != nil {
		return err
	}
	return d.reconcileCompletedRuns(ctx, logger)
}

func (d *Dispatcher) dispatchQueued(ctx context.Context, logger lager.Logger) error {
	queued, err := d.deps.Tickets.List(tickets.ListFilter{State: tickets.StateQueued})
	if err != nil {
		logger.Error("failed-to-list-queued-tickets", err)
		return err
	}

	for _, t := range queued {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		res, err := DispatchOne(ctx, d.deps, t.ID, "agent-dispatcher")
		switch {
		case err == nil:
			logger.Info("dispatched", lager.Data{"ticket": t.ID, "run": res.RunID, "pipeline": res.PipelineName})
		case errors.Is(err, ErrBudgetExhausted):
			// §2.7: over-cap stays QUEUED, never failed. Re-admitted next
			// pass — headroom returns at local midnight or on a raised cap.
			logger.Info("dispatch-deferred-over-budget", lager.Data{"ticket": t.ID, "reason": err.Error()})
		case errors.Is(err, ErrNotQueued), errors.Is(err, tickets.ErrStaleTransition):
			// Raced: the manual route or another pass claimed it. Benign.
			logger.Debug("ticket-claimed-elsewhere", lager.Data{"ticket": t.ID})
		case errors.Is(err, ErrRenderRefused), errors.Is(err, ErrNoWorkflow), errors.Is(err, ErrWorkflowNotFound):
			// Malformed for v0: loud, non-fatal, stays queued for a human
			// to fix the workflow or transition the ticket away (see plan
			// Risks R2 for the log-cadence tradeoff).
			logger.Error("dispatch-refused", err, lager.Data{"ticket": t.ID})
		default:
			// Platform fault: DispatchOne is pre-transition retry-safe, so
			// the ticket stays queued and next pass retries. Never mark
			// failed (§2.7: platform faults → error, not failed).
			logger.Error("failed-to-dispatch", err, lager.Data{"ticket": t.ID})
		}
	}
	return nil
}
```

If executing this task WITHOUT Task 6, add a temporary stub so it compiles standalone (Task 6 replaces it):

```go
// reconcileCompletedRuns is implemented in reconcile.go (Task 6 of the
// dispatch-remainder plan). Stub keeps this slice shippable on its own.
func (d *Dispatcher) reconcileCompletedRuns(ctx context.Context, logger lager.Logger) error {
	return nil
}
```

- [ ] **Step 4: Run to verify pass**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/dispatch/dispatcher.go agent/dispatch/dispatcher_test.go
git commit -m "feat(dispatch): Dispatcher loop wrapping landed DispatchOne - per-ticket isolation, budget deferral, race-tolerant" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5 [Slice C]: Additive `db.PipelineRunFactory.GetRunByID` (co-signed pipeline-runs)

**Execute plan 11 Task 11b Step 1 as written** (`docs/superpowers/plans/agentic-platform/11-dispatch.md:3266-3313`) with these deltas:

- The interface method + implementation code there is still exactly right (the impl mirrors landed `GetRun` at `pipeline_run_factory.go:295-308`; `pipelineRunsQuery`, `scanPipelineRun`, `sq`, `errors`, `sql` are all in scope in that file).
- The Ginkgo spec goes in `atc/db/pipeline_run_factory_test.go` (a `Describe("GetRunByID")` sibling of the existing `RunBelongsToPipeline`/`TicketBelongsToRun` blocks). The spec text in the original task is valid EXCEPT its `CreateRun(template.ID(), map[string]any{"ticket_id": 1}, "test")` call — match the file's existing `CreateRun` call shape (read the neighboring `GetRun` spec around :237 and copy its params/fixtures).
- **New step the original lacks:** regenerate the counterfeiter fake — dispatch tests use `dbfakes.FakePipelineRunFactory`:

Run: `cd /Users/tdmtrader/concourse/concourse/atc/db && go run github.com/maxbrunsfeld/counterfeiter/v6 -o dbfakes/fake_pipeline_run_factory.go . PipelineRunFactory`
Expected: only `atc/db/dbfakes/fake_pipeline_run_factory.go` changes (`git status` to confirm).

- **Co-sign amendment:** cross-aggregate additive method on a pipeline-runs-owned surface — Task 12's entry 2 bullet (d) MUST land in the same commit as this code.

**Files:**
- Modify: `atc/db/pipeline_run_factory.go`
- Modify: `atc/db/dbfakes/fake_pipeline_run_factory.go` (regenerated)
- Test: `atc/db/pipeline_run_factory_test.go`

**Verification:**

Run: `cd /Users/tdmtrader/concourse/concourse && go build ./atc/... && pg_isready && ginkgo --focus="gets a run by its global id" ./atc/db/`
Expected: clean build; spec PASS. (**Postgres required — local-verify, not gate-verifiable.**)

**Commit:**

```bash
git add atc/db/pipeline_run_factory.go atc/db/dbfakes/fake_pipeline_run_factory.go atc/db/pipeline_run_factory_test.go docs/superpowers/plans/agentic-platform/00-shared-contracts.md
git commit -m "feat(db): additive PipelineRunFactory.GetRunByID for dispatch's run-completion reconciler (co-signed pipeline-runs)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6 [Slice C]: Run-completion reconciler — `reconcileCompletedRuns`

**Amends plan 11 Task 11b.** The F17 decision tree stays normative, but the landed world shrinks and reshapes it:

- **Harvest already closes out runs that reach it** (`harvest_step.go:262-274, 327-332`). The reconciler's real coverage: (a) run failed/errored/aborted BEFORE harvest executed (an agent-step failure halts the plan — harvest is a plan step, not an ensure), (b) web-restart/drain deaths, (c) succeeded-but-ticket-still-running safety net, (d) the attempt cap for the (dormant) send_back branch.
- **Checkpoint branches (F17 b.1/b.2) are implemented but dormant:** `agent_run_questions` does not exist and checkpoints are render-refused. Delta from the original task: do NOT import `agent/api/questions` (not landed) — define a narrow local `CheckpointRow` projection + `QuestionLister` seam here; the adapter that bridges the future questions store to this seam is written when plan 08 lands. `nil` lister = branches skipped.
  - **Adapter hand-off (NAME IT so the wiring survives the workstream boundary):** the bridge is a `questionsCheckpointAdapter` implementing `dispatch.QuestionLister` over the plan-08 `questions.Store`. Its `ListByRun(pipelineRunID int) ([]dispatch.CheckpointRow, error)` calls `questions.Store.ListByRun(pipelineRunID) ([]questions.Question, error)` (defined in plan 08 Task 14b / platform-mcp-hitl Task 19) and MAPS each `questions.Question` → `dispatch.CheckpointRow{ID, StepName, AskedAt, Answered, Answer}`; its `Answer(id, answer, by)` calls the store's answer method. **The return types differ** (`[]questions.Question` vs `[]dispatch.CheckpointRow`), so this is a real (small) type — not a passthrough. **Ownership (resolved 2026-07-17): the platform-mcp-hitl remainder OWNS `questionsCheckpointAdapter` as an explicitly-deferred item** (recorded in its Scope-Out) — it is the `questions.Store` owner, so the adapter is its responsibility to build WHEN checkpoints are activated. This five-plan wave does NOT activate them: platform-mcp-hitl's Task 25 explicitly does NOT lift the `render.go:59-61` checkpoint refusal, and nothing wires `LoopConfig.Questions` to a real adapter. So this plan wires `Questions: nil` (Task 10) and the checkpoint-reconciliation branches (b.1/b.2 below) stay **knowingly-dead code** — provably unreachable until BOTH (a) `questionsCheckpointAdapter` exists AND (b) some plan lifts the checkpoint render refusal, NEITHER of which any of the five remainder plans does. Checkpoint-free is the intended v0 end state; this plan ships the branches unit-tested against fakes so they are correct-when-activated, not to run them now.
- **Attempt cap lives HERE** (delta from Task 11's queued→errored): a send_back requeue that would exceed `MaxAttempts` transitions `running→errored` instead (legal edge, `ErrorDetail` recorded). No ticket-core matrix change (C3-clean). The `running→queued` edge annotation in `types.go:32-36` already names this reconciler as a caller.
- `ErrStaleTransition`/`ErrTicketNotFound` are BENIGN everywhere (harvest raced; two-writers of `running→needs_review` is the recorded contract).
- Deltas: `NewDispatcher(deps, LoopConfig)` (Task 4), not the original `Deps.Questions` field; frozen-workflow resolution uses the LANDED `deps.Workflows.Get` (version pinned by `DispatchOne`), not the never-landed `Resolver`.
- PARK-V2 note (unchanged from the original): an `awaiting_human` run (not landed) would have `completed_at` NULL — never a candidate here. Nothing to do.

**Files:**
- Create: `agent/dispatch/reconcile.go`
- Test: `agent/dispatch/reconcile_test.go`
- Modify: `agent/dispatch/dispatcher.go` (delete the Task 4 stub if present)

**Steps:**

- [ ] **Step 0: Memory-store precondition.** Two tests below require `tickets.MemoryStore.Transition` to bump `AttemptCount` on `running→queued` (§2.1 side effect the DB factory has at `agent_tickets_factory.go:230-236`). Check: `grep -n "AttemptCount" agent/api/tickets/memory_store.go`. If absent, add it to the memory store's Transition (additive, mirrors the contract) with a one-line test in `agent/api/tickets/memory_store_test.go`.

- [ ] **Step 1: Write the failing tests** `agent/dispatch/reconcile_test.go`:

```go
package dispatch_test

import (
	"context"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
)

func intp(i int) *int { return &i }

// reconcileScaffold: a MemoryStore holding one RUNNING ticket pinned to
// workflow smoke/3 (matching smokeDefinition) and dispatched as run 100.
func reconcileScaffold(t *testing.T) (dispatch.Deps, *tickets.MemoryStore, int) {
	t.Helper()
	deps, store, _, _ := dispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	v := 3
	if err := store.Update(id, tickets.Update{WorkflowVersion: &v}); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(id, tickets.StateQueued, tickets.StateRunning,
		tickets.TransitionMeta{PipelineRunID: intp(100)}); err != nil {
		t.Fatal(err)
	}
	return deps, store, id
}

func completedRun(id int, status db.PipelineRunStatus) *dbfakes.FakePipelineRun {
	run := new(dbfakes.FakePipelineRun)
	run.IDReturns(id)
	run.StatusReturns(status)
	run.CompletedAtReturns(time.Unix(1, 0), true)
	return run
}

func runReaderFor(run *dbfakes.FakePipelineRun) *dbfakes.FakePipelineRunFactory {
	f := new(dbfakes.FakePipelineRunFactory)
	f.GetRunByIDStub = func(id int) (db.PipelineRun, bool, error) {
		if run != nil && run.ID() == id {
			return run, true, nil
		}
		return nil, false, nil
	}
	return f
}

// --- checkpoint-free branches (live today) --------------------------------

func TestReconcileSucceededRunSafetyNet(t *testing.T) {
	deps, store, id := reconcileScaffold(t)
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{
		RunReader: runReaderFor(completedRun(100, db.PipelineRunSucceeded)),
	})
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateNeedsReview {
		t.Errorf("succeeded-but-still-running => needs_review safety net, got %s", got.State)
	}
}

func TestReconcileFailedRunNoCheckpointsNeedsReview(t *testing.T) {
	for _, status := range []db.PipelineRunStatus{
		db.PipelineRunFailed, db.PipelineRunErrored, db.PipelineRunAborted,
	} {
		deps, store, id := reconcileScaffold(t)
		d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{
			RunReader: runReaderFor(completedRun(100, status)),
		})
		if err := d.Run(context.Background()); err != nil {
			t.Fatalf("[%s] Run: %v", status, err)
		}
		got, _, _ := store.Get(id)
		if got.State != tickets.StateNeedsReview {
			t.Errorf("[%s] checkpoint-free failure => needs_review (human triage), got %s", status, got.State)
		}
	}
}

func TestReconcileIncompleteRunUntouched(t *testing.T) {
	deps, store, id := reconcileScaffold(t)
	run := new(dbfakes.FakePipelineRun)
	run.IDReturns(100)
	run.StatusReturns(db.PipelineRunRunning)
	run.CompletedAtReturns(time.Time{}, false)
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{RunReader: runReaderFor(run)})
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateRunning {
		t.Errorf("incomplete run must leave the ticket running, got %s", got.State)
	}
}

func TestReconcileMissingRunRowTriagesToNeedsReview(t *testing.T) {
	deps, store, id := reconcileScaffold(t)
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{
		RunReader: runReaderFor(nil), // run row gone
	})
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateNeedsReview {
		t.Errorf("vanished run treated as errored => checkpoint-free triage (needs_review), got %s", got.State)
	}
}

func TestReconcileNilRunReaderSkipsPass(t *testing.T) {
	deps, store, id := reconcileScaffold(t)
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{}) // no RunReader
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateRunning {
		t.Errorf("nil RunReader must skip reconciliation, got %s", got.State)
	}
}

func TestReconcileStaleTransitionBenign(t *testing.T) {
	deps, _, _ := reconcileScaffold(t)
	// Harvest races us: simulate by making every Transition stale.
	deps.Tickets = staleOnTransition{Store: deps.Tickets}
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{
		RunReader: runReaderFor(completedRun(100, db.PipelineRunFailed)),
	})
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("stale transition must be benign, got: %v", err)
	}
}

type staleOnTransition struct{ tickets.Store }

func (s staleOnTransition) Transition(int, tickets.State, tickets.State, tickets.TransitionMeta) error {
	return tickets.ErrStaleTransition
}

// --- checkpoint branches (dormant in prod; exercised via the seam) --------

type fakeQuestions struct {
	rows     []dispatch.CheckpointRow
	released []int
}

func (q *fakeQuestions) ListByRun(int) ([]dispatch.CheckpointRow, error) { return q.rows, nil }
func (q *fakeQuestions) Answer(id int, answer, by string) error {
	q.released = append(q.released, id)
	return nil
}

// checkpointWorkflows: smokeDefinition plus a checkpoint step, registered
// so the pinned smoke/3 lookup resolves the on_reject policy.
func checkpointWorkflows(onReject string) *fakeWorkflows {
	def := smokeDefinition()
	def.Config.Steps = append(def.Config.Steps, workflow.Step{Checkpoint: "plan-approval", OnReject: onReject})
	return &fakeWorkflows{byName: map[string]*workflow.Definition{"smoke": def}}
}

func TestReconcileRejectedSendBackRequeues(t *testing.T) {
	deps, store, id := reconcileScaffold(t)
	deps.Workflows = checkpointWorkflows("send_back")
	qs := &fakeQuestions{rows: []dispatch.CheckpointRow{
		{ID: 1, StepName: "checkpoint-plan-approval", AskedAt: 10, Answered: true, Answer: "reject"},
	}}
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{
		RunReader: runReaderFor(completedRun(100, db.PipelineRunFailed)),
		Questions: qs, MaxAttempts: 3,
	})
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _, _ := store.Get(id)
	// The SAME Run pass listed queued tickets BEFORE reconciling, so the
	// requeued ticket is picked up next pass — assert queued now.
	if got.State != tickets.StateQueued {
		t.Errorf("rejected send_back must requeue, got %s", got.State)
	}
	if got.AttemptCount != 1 {
		t.Errorf("running->queued bumps attempt_count (§2.1), got %d", got.AttemptCount)
	}
}

func TestReconcileSendBackOverAttemptCapErrors(t *testing.T) {
	deps, store, id := reconcileScaffold(t)
	deps.Workflows = checkpointWorkflows("send_back")
	// Reach the cap via the edge that owns attempt_count.
	for i := 0; i < 3; i++ {
		if err := store.Transition(id, tickets.StateRunning, tickets.StateQueued, tickets.TransitionMeta{}); err != nil {
			t.Fatal(err)
		}
		if err := store.Transition(id, tickets.StateQueued, tickets.StateRunning,
			tickets.TransitionMeta{PipelineRunID: intp(100)}); err != nil {
			t.Fatal(err)
		}
	}
	qs := &fakeQuestions{rows: []dispatch.CheckpointRow{
		{ID: 1, StepName: "checkpoint-plan-approval", AskedAt: 10, Answered: true, Answer: "reject"},
	}}
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{
		RunReader: runReaderFor(completedRun(100, db.PipelineRunFailed)),
		Questions: qs, MaxAttempts: 3,
	})
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateErrored {
		t.Errorf("over-cap send_back => running->errored (legal edge), got %s", got.State)
	}
	if got.ErrorDetail == "" {
		t.Error("cap trip must record error_detail")
	}
}

func TestReconcileRejectedFailNeedsReview(t *testing.T) {
	deps, store, id := reconcileScaffold(t)
	deps.Workflows = checkpointWorkflows("fail")
	qs := &fakeQuestions{rows: []dispatch.CheckpointRow{
		{ID: 1, StepName: "checkpoint-plan-approval", AskedAt: 10, Answered: true, Answer: "reject"},
	}}
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{
		RunReader: runReaderFor(completedRun(100, db.PipelineRunFailed)),
		Questions: qs,
	})
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateNeedsReview {
		t.Errorf("rejected fail checkpoint => needs_review, got %s", got.State)
	}
}

func TestReconcileUnansweredCheckpointErrorsAndReleases(t *testing.T) {
	deps, store, id := reconcileScaffold(t)
	deps.Workflows = checkpointWorkflows("send_back")
	qs := &fakeQuestions{rows: []dispatch.CheckpointRow{
		{ID: 4, StepName: "checkpoint-plan-approval", AskedAt: 10, Answered: false},
	}}
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{
		RunReader: runReaderFor(completedRun(100, db.PipelineRunAborted)),
		Questions: qs,
	})
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateErrored {
		t.Errorf("unanswered checkpoint on dead run => errored, got %s", got.State)
	}
	if len(qs.released) != 1 || qs.released[0] != 4 {
		t.Errorf("orphan rows must be released via Answer(id, \"\", \"dispatcher\"), got %v", qs.released)
	}
}

func TestReconcileAllApprovedFallsThroughToTriage(t *testing.T) {
	deps, store, id := reconcileScaffold(t)
	deps.Workflows = checkpointWorkflows("send_back")
	qs := &fakeQuestions{rows: []dispatch.CheckpointRow{
		{ID: 1, StepName: "checkpoint-plan-approval", AskedAt: 10, Answered: true, Answer: "approve"},
	}}
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{
		RunReader: runReaderFor(completedRun(100, db.PipelineRunFailed)),
		Questions: qs,
	})
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateNeedsReview {
		t.Errorf("approved checkpoints + failed run => b.3 triage, got %s", got.State)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ -run TestReconcile`
Expected: FAIL — `undefined: dispatch.CheckpointRow` etc.

- [ ] **Step 3: Write `agent/dispatch/reconcile.go`** (and delete the Task 4 stub in `dispatcher.go` if present):

```go
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/atc/db"
)

// RunReader is the reconciler's read seam over pipeline runs (additive
// db.PipelineRunFactory.GetRunByID, co-signed pipeline-runs).
type RunReader interface {
	GetRunByID(id int) (db.PipelineRun, bool, error)
}

// QuestionLister is the plan-08 checkpoint seam, deliberately narrow and
// LOCAL to this package: agent_run_questions is not landed and checkpoints
// are render-refused, so production wires nil and every completed run is
// checkpoint-free. Plan 08 supplies an adapter over its questions store
// without this package changing shape.
type QuestionLister interface {
	// ListByRun returns the run's kind='checkpoint' rows (any order).
	ListByRun(pipelineRunID int) ([]CheckpointRow, error)
	// Answer releases a row (orphan cleanup: answer "", answeredBy "dispatcher").
	Answer(id int, answer, answeredBy string) error
}

// CheckpointRow is the narrow projection of agent_run_questions the F17
// tree consumes.
type CheckpointRow struct {
	ID       int
	StepName string // "checkpoint-<name>"
	AskedAt  int64
	Answered bool
	Answer   string // meaningful only when Answered
}

// reconcileCompletedRuns is the F17 pass (checkpoint seam delta §6): walk
// RUNNING tickets whose pipeline run is complete and apply the frozen
// decision tree through the single-writer Transition. Harvest is the
// PRIMARY writer of running→needs_review — this pass is the backup for
// runs that died before harvest executed (an agent-step failure halts the
// plan; harvest is a plan step, not an ensure), web-restart deaths, and
// the succeeded-but-still-running safety net. Stale/not-found transitions
// are benign (two-writers recorded). PARK-V2's awaiting_human runs (not
// landed) keep completed_at NULL, so they can never be candidates here.
func (d *Dispatcher) reconcileCompletedRuns(ctx context.Context, logger lager.Logger) error {
	if d.cfg.RunReader == nil {
		return nil
	}
	running, err := d.deps.Tickets.List(tickets.ListFilter{State: tickets.StateRunning})
	if err != nil {
		logger.Error("failed-to-list-running-tickets", err)
		return err
	}

	for _, t := range running {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if t.PipelineRunID == nil {
			// Moved to running outside dispatch (manual transition without
			// a run id). Nothing to reconcile against; humans own it.
			continue
		}
		run, found, err := d.cfg.RunReader.GetRunByID(*t.PipelineRunID)
		if err != nil {
			logger.Error("failed-to-get-run", err, lager.Data{"ticket": t.ID, "run": *t.PipelineRunID})
			continue
		}
		status := db.PipelineRunErrored // vanished run row = errored
		if found {
			if _, complete := run.CompletedAt(); !complete {
				continue // still running; the lifecycler owns completion
			}
			status = run.Status()
		}
		d.reconcileOne(logger, t, status)
	}
	return nil
}

func (d *Dispatcher) reconcileOne(logger lager.Logger, t tickets.Ticket, status db.PipelineRunStatus) {
	log := logger.Session("reconcile", lager.Data{"ticket": t.ID, "run-status": string(status)})

	transition := func(to tickets.State, meta tickets.TransitionMeta) {
		err := d.deps.Tickets.Transition(t.ID, tickets.StateRunning, to, meta)
		switch {
		case err == nil:
			log.Info("reconciled", lager.Data{"to": string(to)})
		case errors.Is(err, tickets.ErrStaleTransition), errors.Is(err, tickets.ErrTicketNotFound):
			log.Debug("reconcile-raced", lager.Data{"to": string(to)}) // harvest won; benign
		default:
			log.Error("failed-to-transition", err, lager.Data{"to": string(to)})
		}
	}

	// (a) Run succeeded but the ticket never left running: harvest should
	// have moved it — safety net. Branch meta stays empty (harvest is the
	// Branch field's only legitimate writer; §2.1 TransitionMeta note).
	if status == db.PipelineRunSucceeded {
		transition(tickets.StateNeedsReview, tickets.TransitionMeta{})
		return
	}

	// (b) failed/errored/aborted. Checkpoint branches b.1/b.2 first — only
	// reachable when a question lister is wired (plan 08; nil today).
	if d.cfg.Questions != nil {
		rows, err := d.cfg.Questions.ListByRun(*t.PipelineRunID)
		if err != nil {
			// Cannot classify without checkpoint state: do NOT guess.
			// Retry next pass.
			log.Error("failed-to-list-checkpoint-rows", err)
			return
		}
		if d.reconcileCheckpoints(log, t, status, rows, transition) {
			return
		}
	}

	// (b.3) Checkpoint-free failure: agent step crashed, gate blew up
	// pre-harvest, abort, drain death — human triage.
	transition(tickets.StateNeedsReview, tickets.TransitionMeta{})
}

// reconcileCheckpoints applies F17 b.1/b.2. Returns true when the ticket
// was decided; false when the run is checkpoint-free / all-approved
// (caller falls through to b.3).
func (d *Dispatcher) reconcileCheckpoints(
	log lager.Logger,
	t tickets.Ticket,
	status db.PipelineRunStatus,
	rows []CheckpointRow,
	transition func(tickets.State, tickets.TransitionMeta),
) bool {
	if len(rows) == 0 {
		return false
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].AskedAt > rows[j].AskedAt })

	// b.1: latest (max asked_at) row ANSWERED with answer != approve.
	latest := rows[0]
	if latest.Answered && latest.Answer != "approve" {
		name := strings.TrimPrefix(latest.StepName, "checkpoint-")
		if d.onRejectFor(log, t, name) == "send_back" {
			// Attempt cap on the REQUEUE edge (not queued→errored, which
			// is not a legal §1.7 edge): past the cap the platform gives
			// up — errored, never failed (the run did not "run badly").
			if d.cfg.MaxAttempts > 0 && t.AttemptCount+1 > d.cfg.MaxAttempts {
				transition(tickets.StateErrored, tickets.TransitionMeta{
					ErrorDetail: fmt.Sprintf("rejected checkpoint %q would exceed %d dispatch attempts", name, d.cfg.MaxAttempts),
				})
				return true
			}
			transition(tickets.StateQueued, tickets.TransitionMeta{}) // §2.1 bumps attempt_count
			return true
		}
		// on_reject fail / empty / unknown step → human triage.
		transition(tickets.StateNeedsReview, tickets.TransitionMeta{})
		return true
	}

	// b.2: any UNANSWERED row on a completed run — sidecar death, abort
	// while parked. Error the ticket, release the orphans so the
	// open-questions index clears (§3.2: a dead row never stays open).
	var unanswered []CheckpointRow
	for _, r := range rows {
		if !r.Answered {
			unanswered = append(unanswered, r)
		}
	}
	if len(unanswered) > 0 {
		transition(tickets.StateErrored, tickets.TransitionMeta{
			ErrorDetail: fmt.Sprintf("checkpoint %q unresolved: run completed %s while parked", unanswered[0].StepName, status),
		})
		for _, r := range unanswered {
			if err := d.cfg.Questions.Answer(r.ID, "", "dispatcher"); err != nil {
				log.Error("failed-to-release-orphan-question", err, lager.Data{"question": r.ID})
			}
		}
		return true
	}

	return false // every checkpoint answered approve → b.3
}

// onRejectFor resolves the FROZEN workflow config (the version DispatchOne
// pinned onto the ticket) and returns the named checkpoint's on_reject
// ("" when unresolvable → the safe needs_review branch).
func (d *Dispatcher) onRejectFor(log lager.Logger, t tickets.Ticket, checkpointName string) string {
	if t.WorkflowName == "" || t.WorkflowVersion == nil {
		return ""
	}
	def, found, err := d.deps.Workflows.Get(t.WorkflowName, *t.WorkflowVersion)
	if err != nil {
		log.Error("failed-to-resolve-frozen-workflow", err)
		return ""
	}
	if !found {
		return ""
	}
	for _, s := range def.Config.Steps {
		if s.Checkpoint == checkpointName {
			return s.OnReject
		}
	}
	return ""
}
```

- [ ] **Step 4: Run to verify pass**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/`
Expected: PASS (all reconcile + dispatcher + landed tests).

- [ ] **Step 5: Commit**

```bash
git add agent/dispatch/reconcile.go agent/dispatch/reconcile_test.go agent/dispatch/dispatcher.go agent/api/tickets/memory_store.go agent/api/tickets/memory_store_test.go
git commit -m "feat(dispatch): run-completion reconciler - F17 tree, checkpoint legs dormant behind nil seam, attempt cap on requeue edge" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 7 [Slice D]: `Ticket.UserID` resolution at dispatch

**New task** (the user-resolution fragment of plan 11 Task 9; the create handler explicitly left it to dispatch — `handler.go:130`). Dispatch-time (not create-time) resolution keeps ticket-core's HTTP layer DB-free and matches the recorded seam. Two additive edits outside `agent/dispatch` (both co-signed in Task 12 entry 2):

- `tickets.Update` gains `UserID *int` (C3 add-alongside; `Update` is the non-state writer, so this is contract-consistent) + the matching `Set("user_id", ...)` case in the DB factory and MemoryStore.
- New `db.NewAgentUserLookup(dbConn)` — `users.id` by username. Usernames are not unique across connectors; most-recent-login wins (single-connector home deployment; documented).

Once `user_id` is populated, the LANDED user-first credential resolution (`dispatch.go:229-239`) activates with zero further changes.

**Files:**
- Modify: `agent/api/tickets/types.go` (`Update.UserID`)
- Modify: `atc/db/agent_tickets_factory.go` (Update case)
- Modify: `agent/api/tickets/memory_store.go` (Update case)
- Create: `atc/db/agent_user_lookup.go`
- Modify: `agent/dispatch/dispatch.go` (`Deps.Users` + resolution block)
- Test: `agent/dispatch/dispatch_test.go` (extend), `atc/db/agent_user_lookup_test.go` (Ginkgo), `atc/db/agent_tickets_factory_test.go` (extend)

**Steps:**

- [ ] **Step 1: Failing unit test** — append to `agent/dispatch/dispatch_test.go`:

```go
type fakeUserLookup struct{ ids map[string]int }

func (f fakeUserLookup) FindByUsername(name string) (int, bool, error) {
	id, ok := f.ids[name]
	return id, ok, nil
}

func TestDispatchOneResolvesAndPersistsUserID(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	deps.Users = fakeUserLookup{ids: map[string]int{"tdm": 42}}
	// Give user 42 a vaulted credential so user-first resolution is provable.
	deps.Credentials = &fakeBackend{
		platformUserID: 9,
		creds: map[int]map[string]*credentials.Credential{
			9:  {credentials.KindAnthropicOAuth: {UserID: 9, Kind: credentials.KindAnthropicOAuth, Token: "platform-tok"}},
			42: {credentials.KindAnthropicOAuth: {UserID: 42, UserName: "tdm", Kind: credentials.KindAnthropicOAuth, Token: "tdm-tok"}},
		},
	}
	attacher := new(credentialsfakes.FakeSecretAttacher)
	deps.Secrets = attacher
	id := queuedTicket(t, store, "smoke") // UserName "tdm", UserID nil

	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "loop"); err != nil {
		t.Fatalf("DispatchOne: %v", err)
	}
	got, _, _ := store.Get(id)
	if got.UserID == nil || *got.UserID != 42 {
		t.Fatalf("user_id must be resolved+persisted at dispatch, got %v", got.UserID)
	}
	if attacher.AttachCallCount() != 1 {
		t.Fatal("expected one Attach")
	}
	_, _, cred, _ := attacher.AttachArgsForCall(0)
	if cred.Token != "tdm-tok" {
		t.Errorf("user-first credential must fund the run once user_id resolves, got token %q", cred.Token)
	}
}

func TestDispatchOneUnknownUserFallsBackToPlatform(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	deps.Users = fakeUserLookup{ids: map[string]int{}} // "tdm" not found
	attacher := new(credentialsfakes.FakeSecretAttacher)
	deps.Secrets = attacher
	id := queuedTicket(t, store, "smoke")

	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "loop"); err != nil {
		t.Fatalf("unknown user must not block dispatch (platform funds it): %v", err)
	}
	got, _, _ := store.Get(id)
	if got.UserID != nil {
		t.Errorf("unresolvable user leaves user_id NULL, got %v", got.UserID)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ -run TestDispatchOneResolves`
Expected: FAIL — `deps.Users undefined`.

- [ ] **Step 3: Implement the seam edits.**

(a) `agent/api/tickets/types.go`, in `Update` after `TargetBranch`:

```go
	// UserID resolves the triggering user (co-signed dispatch remainder,
	// 2026-07-17): dispatch looks up users.id from UserName at dispatch
	// time and records it here — the wave-4 leg the create handler's
	// comment promised. Additive; nil = leave unchanged.
	UserID *int
```

(b) `atc/db/agent_tickets_factory.go` `Update` (after the `TargetBranch` case at :190-192):

```go
	if upd.UserID != nil {
		q = q.Set("user_id", *upd.UserID)
	}
```

(c) `agent/api/tickets/memory_store.go` `Update` — same pattern as its `BudgetUSD` case (copy the pointed-to value).

(d) Create `atc/db/agent_user_lookup.go`:

```go
package db

import (
	"database/sql"
	"errors"
)

// AgentUserLookup resolves users.id from a username for agent-ticket user
// attribution (§2.8.2 user-first credential resolution; dispatch remainder
// 2026-07-17). Usernames are not unique across connectors — the most
// recently logged-in row wins, which is exact for single-connector
// deployments and a documented approximation otherwise.
type AgentUserLookup interface {
	FindByUsername(username string) (int, bool, error)
}

func NewAgentUserLookup(conn DbConn) AgentUserLookup {
	return &agentUserLookup{conn: conn}
}

type agentUserLookup struct{ conn DbConn }

func (l *agentUserLookup) FindByUsername(username string) (int, bool, error) {
	if username == "" {
		return 0, false, nil
	}
	var id int
	err := l.conn.QueryRow(
		`SELECT id FROM users WHERE username = $1 ORDER BY last_login DESC NULLS LAST, id DESC LIMIT 1`,
		username,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}
```

(e) `agent/dispatch/dispatch.go`: add the seam + field:

```go
// UserLookup resolves users.id from a username (db.NewAgentUserLookup).
type UserLookup interface {
	FindByUsername(username string) (int, bool, error)
}
```

to `Deps` (after `Credentials`):

```go
	// Users, when non-nil, resolves the ticket's triggering user id at
	// dispatch (the create handler records only the username). nil skips
	// (platform-funded, as before).
	Users UserLookup
```

and in `DispatchOne`, immediately after the version-freeze block (`dispatch.go:123-128`):

```go
	// Resolve the triggering user's id (the wave-4 leg ticket-core left to
	// dispatch): user-first credential funding and spend attribution key
	// on agent_tickets.user_id, which nothing populated before this.
	// Unresolvable username → platform-funded (found=false is not an
	// error); store faults ARE errors (ticket stays queued, retried).
	if t.UserID == nil && t.UserName != "" && deps.Users != nil {
		uid, found, err := deps.Users.FindByUsername(t.UserName)
		if err != nil {
			return Result{}, fmt.Errorf("resolve user %q: %w", t.UserName, err)
		}
		if found {
			if err := deps.Tickets.Update(ticketID, tickets.Update{UserID: &uid}); err != nil {
				return Result{}, fmt.Errorf("record user id: %w", err)
			}
			t.UserID = &uid
		}
	}
```

- [ ] **Step 4: Run to verify pass**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ ./agent/api/tickets/`
Expected: PASS.

- [ ] **Step 5: Postgres specs (local-verify).** Create `atc/db/agent_user_lookup_test.go` (Ginkgo, `db_test` package, description containing "agent user lookup"): seed a user via `db.NewUserFactory(dbConn).CreateOrUpdateUser("tdm", "local", "sub-1")`, read its id back with a raw query or `GetAllUsers()`, assert `FindByUsername("tdm")` returns it with `found=true` and `FindByUsername("ghost")` returns `found=false`. Extend `atc/db/agent_tickets_factory_test.go` with an `Update(id, tickets.Update{UserID: &uid})` round-trip asserting `Get` returns the id.

Run: `cd /Users/tdmtrader/concourse/concourse && pg_isready && ginkgo --focus="agent user lookup" ./atc/db/ && ginkgo --focus="user_id" ./atc/db/`
Expected: PASS. (**Postgres required.**)

- [ ] **Step 6: Commit** (include Task 12 entry-2 bullet (b) if landing separately from Task 12):

```bash
git add agent/api/tickets/types.go agent/api/tickets/memory_store.go atc/db/agent_tickets_factory.go atc/db/agent_user_lookup.go atc/db/agent_user_lookup_test.go atc/db/agent_tickets_factory_test.go agent/dispatch/dispatch.go agent/dispatch/dispatch_test.go
git commit -m "feat(dispatch): resolve+persist ticket user_id at dispatch - user-first credential funding activates" -m "Additive tickets.Update.UserID (co-signed ticket-core) + db.NewAgentUserLookup." -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 8 [Slice D]: Credential expiry check + principal reconciliation (`agent-run-<id>` name, scope, flag-driven expiry)

**Amends the LANDED `attachRunSecret`/`resolveRunCredential`** (`dispatch.go:188-258`) — plan 11 Task 9 must NOT be green-fielded (the mint+attach landed in `9a8eaf452c` despite the "deferred as a set" wording; Task 12 logs the corrective amendment). Reconciliations against §2.8.2, resolved in the CONTRACT's favor:

1. **Principal name**: landed `run-<id>` → contract `agent-run-<run-id>`. Safe: the principal has NO consumer until wave 3 (recorded in the SS11 2026-07-17 dogfood entry), and the secret-reaper's revoke adapter already computes `credentials.RunSecretName(runID)` = `agent-run-<id>` (`secret_reaper.go:117`) — the rename FIXES the reaper's future revoke-by-name path. The SECRET name is untouched (exec-consumed).
2. **Scopes**: add `principals.ScopeQuestionsAnswer` (additive; wave-3 platform-mcp needs it and minted tokens are immutable — mint it now).
3. **Expiry**: hardcoded 24h → `Deps.RunTimeout` (wired from the new `--agent-run-timeout` flag in Task 10, 6h default per §2.8.2; zero-value falls back to the landed 24h so existing tests/behavior hold until the flag is wired). Park-aware expiry is a PARK-V2 layer ON TOP of this — see the cross-plan ownership box below.
4. **Expired-credential check**: `resolveRunCredential` skips credentials whose `ExpiresAt` is in the past (`0` = unknown = usable) and names the owner in the final error.

> **⚠ CROSS-PLAN OWNERSHIP — the `attachRunSecret` mint block (name/scope/expiry) is edited by BOTH this Task 8 and platform-mcp-hitl remainder Task 26; they collide.** Both diff from the SAME landed baseline (`dispatch.go:188-217`: `Name: fmt.Sprintf("run-%d", runID)`, hardcoded `24*time.Hour`, 4 scopes, no `questions:answer`). This plan's Task 8 renames the principal to `agent-run-<id>` (= `credentials.RunSecretName`, so the reaper's `RevokeByName` adapter can find it — the reason for the rename), adds `principals.ScopeQuestionsAnswer` (5 scopes), and introduces `Deps.RunTimeout` (6h). platform-mcp-hitl Task 26 independently rewrites the same block but **keeps the old `run-<id>` name** (in its pre-reconciliation baseline), adds `Deps.ParkTimeout` (72h, applied PARK-CONDITIONALLY via `RunPrincipalTimeout` — not an unconditional margin), and ALSO adds `ScopeQuestionsAnswer`. Applying Task 26's literal snippet after Task 8 SILENTLY REVERTS the `agent-run-<id>` rename (breaking the reaper path) and leaves two unreconciled timeout fields.
>
> **Reconciliation (authoritative — this plan's Task 8 owns the base mint-block shape):**
> 1. **Name is `agent-run-<id>`, period.** Task 26 must NOT restate `Name: fmt.Sprintf("run-%d", runID)`. Whichever task lands second amends the already-landed mint call, it does NOT re-diff from the pre-rename baseline.
> 2. **`questions:answer` scope is idempotent** — whoever lands second finds it already present; do not add it twice.
> 3. **Expiry is workflow-CONDITIONAL, not a single longest bound.** This plan's Task 8 lands `Deps.RunTimeout` (6h) as the base and mints `now + RunTimeout` unconditionally (its code snippet and its `TestAttachMintsContractShapedPrincipal` test both assert the ~6h window). Task 26 layers `Deps.ParkTimeout` (72h) and changes the ONE expiry line to the frozen workflow-conditional selector `dispatch.RunPrincipalTimeout(cfg, runTimeout, parkTimeout)` — ordinary run → `RunTimeout` (6h); a workflow declaring a park-policy `ask_human` or a checkpoint → `ParkTimeout` (72h). **There is NO `max(...)` and NO `+12h` margin.** `now + max(RunTimeout, ParkTimeout+12h)` = `max(6h, 84h)` = 84h UNCONDITIONALLY (its "non-park run keeps 6h" parenthetical is false), and an 84h token on every ordinary run violates the frozen §8.1 `AGENT_PRINCIPAL_TOKEN` row (ordinary run = now + `--agent-run-timeout`). `RunPrincipalTimeout` is owned by platform-mcp-hitl Task 26 (which defines it, or consumes plan 11's `11-dispatch.md:2465-2476` version if that landed first); this plan's Task 8 never carries it forward.
> 4. **Sequencing:** land this Task 8 FIRST (it is the rename + reaper fix, a superset-independent change); Task 26 then reduces to "add `Deps.ParkTimeout` + `--agent-park-timeout`; change the expiry line to the workflow-conditional `RunPrincipalTimeout(cfg, RunTimeout, ParkTimeout)` selector; keep the `agent-run-<id>` name and the `questions:answer` scope already present." If platform-mcp-hitl lands first instead, Task 8 amends ITS landed block (apply only the rename + `RunTimeout`; the scope is already there). This box must be mirrored into platform-mcp-hitl Task 26 before either executes — that file is outside this plan's edit scope, so flag it to the platform-mcp-hitl planner. platform-mcp-hitl Task 26's own ownership box already carries the contract-faithful `RunPrincipalTimeout` reconciliation.
> 5. **Beyond the mint block, the two plans make ADD-alongside edits to the SAME three files** — `agent/dispatch/dispatch.go` (this plan's Task 8 adds `Deps.RunTimeout` + rewrites `attachRunSecret`/`resolveRunCredential`; Task 26 adds `Deps.ParkTimeout` + `RunInput`/`wf` threading + `RunPrincipalTimeout`), `atc/atccmd/command.go` (both add a flag near `:234` and a `Deps`-literal field near `:2395`), and `agent/dispatch/dispatch_test.go` (both append principal-mint tests). These regions collide if the two plans' Slice D / Slice F run in parallel — do NOT. The second-landing plan AMENDS the already-landed `Deps` struct, flag block, and `Deps` literal (C3 ADD-alongside), re-greps the `attachRunSecret` call before editing so the `agent-run-<id>` rename and `questions:answer` scope are not reverted, and never block-replaces.

**Files:**
- Modify: `agent/dispatch/dispatch.go`
- Test: `agent/dispatch/dispatch_test.go` (extend)

**Steps:**

- [ ] **Step 1: Failing tests** — append to `agent/dispatch/dispatch_test.go` (imports: `"strings"`, `"time"` if missing):

```go
func TestAttachMintsContractShapedPrincipal(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	deps.RunTimeout = 6 * time.Hour
	pstore := principals.NewMemoryStore()
	deps.Principals = pstore
	id := queuedTicket(t, store, "smoke")

	before := time.Now()
	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "loop"); err != nil {
		t.Fatalf("DispatchOne: %v", err)
	}
	// Run id 555 comes from dispatchDeps' FakePipelineRun.
	// NOTE: adapt this lookup to the principals.MemoryStore surface (List
	// or Get-by-name) — do not add production methods for the test.
	p := principalByName(t, pstore, "agent-run-555")
	wantScopes := map[string]bool{
		principals.ScopeTicketsRead: true, principals.ScopeTicketsWrite: true,
		principals.ScopeMetricsWrite: true, principals.ScopeCostsWrite: true,
		principals.ScopeQuestionsAnswer: true,
	}
	if len(p.Scopes) != len(wantScopes) {
		t.Errorf("want 5 scopes incl. questions:answer, got %v", p.Scopes)
	}
	for _, s := range p.Scopes {
		if !wantScopes[s] {
			t.Errorf("unexpected scope %q", s)
		}
	}
	if p.ExpiresAt == nil {
		t.Fatal("expiry must be set")
	}
	lo := before.Add(6*time.Hour - time.Minute).Unix()
	hi := before.Add(6*time.Hour + time.Minute).Unix()
	if *p.ExpiresAt < lo || *p.ExpiresAt > hi {
		t.Errorf("expiry must be now+RunTimeout (6h), got %d not in [%d,%d]", *p.ExpiresAt, lo, hi)
	}
}

func TestResolveRunCredentialSkipsExpiredNamingOwner(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	deps.Users = fakeUserLookup{ids: map[string]int{"tdm": 42}}
	expired := time.Now().Add(-time.Hour).Unix()
	deps.Credentials = &fakeBackend{
		platformUserID: 9,
		creds: map[int]map[string]*credentials.Credential{
			// user cred expired; platform cred valid → platform funds the run
			42: {credentials.KindAnthropicOAuth: {UserID: 42, UserName: "tdm", Kind: credentials.KindAnthropicOAuth, Token: "stale", ExpiresAt: expired}},
			9:  {credentials.KindAnthropicOAuth: {UserID: 9, Kind: credentials.KindAnthropicOAuth, Token: "platform-tok"}},
		},
	}
	attacher := new(credentialsfakes.FakeSecretAttacher)
	deps.Secrets = attacher
	id := queuedTicket(t, store, "smoke")

	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "loop"); err != nil {
		t.Fatalf("expired user cred must fall back to platform: %v", err)
	}
	_, _, cred, _ := attacher.AttachArgsForCall(0)
	if cred.Token != "platform-tok" {
		t.Errorf("expected platform fallback past expired user cred, got %q", cred.Token)
	}
}

func TestResolveRunCredentialAllExpiredErrorsWithOwner(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	expired := time.Now().Add(-time.Hour).Unix()
	deps.Credentials = &fakeBackend{
		platformUserID: 9,
		creds: map[int]map[string]*credentials.Credential{
			9: {credentials.KindAnthropicOAuth: {UserID: 9, UserName: "platform", Kind: credentials.KindAnthropicOAuth, Token: "stale", ExpiresAt: expired}},
		},
	}
	id := queuedTicket(t, store, "smoke")

	_, err := dispatch.DispatchOne(context.Background(), deps, id, "loop")
	if err == nil || !strings.Contains(err.Error(), "expired") || !strings.Contains(err.Error(), "platform") {
		t.Fatalf("all-expired must error naming the owner, got %v", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateQueued {
		t.Errorf("credential failure is pre-transition: ticket stays queued, got %s", got.State)
	}
}
```

Write the small `principalByName(t, store, name)` helper against whatever enumeration `principals.MemoryStore` actually exposes (check `agent/api/principals/` first).

- [ ] **Step 2: Run to verify failure**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ -run 'TestAttachMints|TestResolveRunCredential'`
Expected: FAIL (name `run-555`, 4 scopes, 24h expiry, no expiry check).

- [ ] **Step 3: Implement in `dispatch.go`.**

(a) `Deps` gains (after `Secrets`):

```go
	// RunTimeout bounds the per-run principal token (§2.8.2: expires_at =
	// now + --agent-run-timeout). Zero preserves the pre-flag 24h default.
	RunTimeout time.Duration
```

(b) in `attachRunSecret`, replace the mint block's name/scopes/expiry:

```go
		timeout := deps.RunTimeout
		if timeout <= 0 {
			timeout = 24 * time.Hour // pre-flag default, kept for zero-value Deps
		}
		expires := time.Now().Add(timeout).Unix()
		_, token, err := deps.Principals.Create(principals.CreateSpec{
			// §2.8.2 name — identical to the secret name so the reaper's
			// RevokeByName(RunSecretName(runID)) adapter finds it.
			Name:        credentials.RunSecretName(runID),
			Description: fmt.Sprintf("per-run principal for pipeline run %d (ticket %d)", runID, t.ID),
			Scopes: []string{
				principals.ScopeTicketsRead,
				principals.ScopeTicketsWrite,
				principals.ScopeMetricsWrite,
				principals.ScopeCostsWrite,
				principals.ScopeQuestionsAnswer, // wave-3 platform-mcp; additive now (tokens are immutable)
			},
			CreatedBy: "dispatch",
			ExpiresAt: &expires,
		})
```

(c) rewrite `resolveRunCredential` with the expiry filter (add `"strings"` import):

```go
func resolveRunCredential(deps Deps, t *tickets.Ticket) (*credentials.Credential, error) {
	kinds := []string{credentials.KindAnthropicOAuth, credentials.KindAnthropicAPIKey}
	now := time.Now().Unix()
	var expiredOwners []string

	usable := func(cred *credentials.Credential) bool {
		if cred.ExpiresAt > 0 && cred.ExpiresAt <= now {
			owner := cred.UserName
			if owner == "" {
				owner = fmt.Sprintf("user %d", cred.UserID)
			}
			expiredOwners = append(expiredOwners, fmt.Sprintf("%s (%s, expired %s)",
				owner, cred.Kind, time.Unix(cred.ExpiresAt, 0).UTC().Format(time.RFC3339)))
			return false
		}
		return true
	}

	if t.UserID != nil {
		for _, kind := range kinds {
			cred, found, err := deps.Credentials.Resolve(*t.UserID, kind)
			if err != nil {
				return nil, err
			}
			if found && usable(cred) {
				return cred, nil
			}
		}
	}

	platformID, _, found, err := deps.Credentials.UserBySub(credentials.PlatformUserSub)
	if err != nil {
		return nil, err
	}
	if found {
		for _, kind := range kinds {
			cred, credFound, err := deps.Credentials.Resolve(platformID, kind)
			if err != nil {
				return nil, err
			}
			if credFound && usable(cred) {
				return cred, nil
			}
		}
	}

	if len(expiredOwners) > 0 {
		return nil, fmt.Errorf("no usable Anthropic credential for ticket %d — expired: %s; re-vault with `fly agent auth` (or `--platform`)",
			t.ID, strings.Join(expiredOwners, "; "))
	}
	return nil, fmt.Errorf("no vaulted Anthropic credential for ticket %d (user or platform): run `fly agent auth` or `fly agent auth --platform`", t.ID)
}
```

- [ ] **Step 4: Run to verify pass**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/`
Expected: PASS. If any LANDED test asserted the old `run-<id>` principal name, update it — the contract name won (Task 12 amendment records the decision).

- [ ] **Step 5: Commit**

```bash
git add agent/dispatch/dispatch.go agent/dispatch/dispatch_test.go
git commit -m "fix(dispatch): principal reconciled to SS2.8.2 - agent-run-<id> name, questions:answer scope, flag-driven expiry; expired creds skipped with owner named" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 9 [Slice D]: `concourse/ticket` secret label (best-effort)

**New task** (the label fragment of plan 11 Tasks 1/9; §2.8.2: "a labeling failure is logged, never fatal — GC keys off `concourse/agent-run` alone"). The labeler is a `Deps` seam applied by `attachRunSecret` after a successful `Attach`; only the K8s wiring (Task 10) supplies an implementation, so the manual route (deps built in `constructAPIMembers`, no clientset — see the lazy-attacher precedent) leaves it nil for now (Risks R4).

**Files:**
- Create: `agent/dispatch/labels.go`
- Test: `agent/dispatch/labels_test.go`
- Modify: `agent/dispatch/dispatch.go` (`Deps.SecretLabels` + call site)

**Steps:**

- [ ] **Step 1: Failing test** `agent/dispatch/labels_test.go`:

```go
package dispatch_test

import (
	"context"
	"testing"

	"github.com/concourse/concourse/agent/dispatch"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestK8sRunSecretLabelerPatchesTicketLabel(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "agent-run-555", Namespace: "concourse",
			Labels: map[string]string{"concourse/agent-run": "555"},
		},
	})
	l := dispatch.NewK8sRunSecretLabeler(client, "concourse")
	if err := l.Label(context.Background(), 555, 42); err != nil {
		t.Fatalf("Label: %v", err)
	}
	got, err := client.CoreV1().Secrets("concourse").Get(context.Background(), "agent-run-555", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Labels["concourse/ticket"] != "42" {
		t.Errorf("want concourse/ticket=42, labels=%v", got.Labels)
	}
	if got.Labels["concourse/agent-run"] != "555" {
		t.Errorf("existing labels must survive the merge patch, labels=%v", got.Labels)
	}
}

func TestK8sRunSecretLabelerMissingSecretErrors(t *testing.T) {
	l := dispatch.NewK8sRunSecretLabeler(fake.NewSimpleClientset(), "concourse")
	if err := l.Label(context.Background(), 999, 42); err == nil {
		t.Fatal("labeling a missing secret must error (caller logs, never fatal)")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ -run TestK8sRunSecretLabeler`
Expected: FAIL — `undefined: dispatch.NewK8sRunSecretLabeler`.

- [ ] **Step 3: Write `agent/dispatch/labels.go`:**

```go
package dispatch

import (
	"context"
	"fmt"

	"github.com/concourse/concourse/agent/credentials"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// RunSecretLabeler adds the operator-filtering concourse/ticket label to a
// run's credential secret (§2.8.2). Best-effort by contract: the caller
// logs failures and never fails a dispatched run over one — the
// secret-reaper's GC keys off concourse/agent-run alone.
type RunSecretLabeler interface {
	Label(ctx context.Context, runID, ticketID int) error
}

func NewK8sRunSecretLabeler(client kubernetes.Interface, namespace string) RunSecretLabeler {
	return &k8sRunSecretLabeler{client: client, namespace: namespace}
}

type k8sRunSecretLabeler struct {
	client    kubernetes.Interface
	namespace string
}

func (l *k8sRunSecretLabeler) Label(ctx context.Context, runID, ticketID int) error {
	patch := []byte(fmt.Sprintf(`{"metadata":{"labels":{"concourse/ticket":"%d"}}}`, ticketID))
	_, err := l.client.CoreV1().Secrets(l.namespace).Patch(
		ctx, credentials.RunSecretName(runID), types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	return err
}
```

(b) `Deps` gains (after `Secrets`):

```go
	// SecretLabels, when non-nil, adds the concourse/ticket label after a
	// successful Attach. Best-effort: failures are logged, never fatal.
	SecretLabels RunSecretLabeler
```

(c) in `attachRunSecret`, after the successful `Attach` (add imports `"code.cloudfoundry.org/lager/v3"`, `"code.cloudfoundry.org/lager/v3/lagerctx"`; `lagerctx.FromContext` returns a no-sink logger when the ctx carries none, so the route path is safe):

```go
	if deps.SecretLabels != nil && t.ID > 0 {
		if err := deps.SecretLabels.Label(ctx, runID, t.ID); err != nil {
			// Best-effort by contract (§2.8.2): operator filtering only.
			lagerctx.FromContext(ctx).Session("attach-run-secret").Error("failed-to-label-run-secret", err,
				lager.Data{"run": runID, "ticket": t.ID})
		}
	}
```

- [ ] **Step 4: Add a unit test that a labeler failure does NOT fail dispatch** (wire an always-erroring fake labeler into `dispatchDeps`, assert `DispatchOne` succeeds and the ticket reaches running), then run the package:

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/dispatch/labels.go agent/dispatch/labels_test.go agent/dispatch/dispatch.go agent/dispatch/dispatch_test.go
git commit -m "feat(dispatch): best-effort concourse/ticket label on the run credential secret" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 10 [Slice B]: `atc.ComponentAgentDispatcher` + flags + K8s-gated wiring

**Amends plan 11 Task 13.** Deltas from the original snippet — read ALL of them before touching `command.go`:

- **Strip every PARK-V2 leg**: no `WithShortParkMax`, no `PrincipalRevoker`, no `--agent-park-timeout`/`--agent-short-park-max` (platform-mcp-hitl item).
- **Reuse `cmd.agentRunSecrets()`** (the landed lazy bridge, `command.go:2413-2418`) — do NOT construct a second `credentials.NewK8sSecretAttacher` as the snippet showed; the HTTP handler and the loop must share ONE attacher instance.
- **`db.NewPipelineRunFactory` signature changed** (F27): `(logger, dbConn, lockFactory, checkFactory)`; better, reuse the in-scope `dbPipelineRunFactory` the lifecycler already uses (`command.go:1287`).
- **No `Resolver`/`Team`/`Budget-in-cfg` deps** — the landed `Deps` uses `Workflows`/`Templates`; `Questions` is nil until plan 08.
- **New: enable flag, default OFF** — an always-on dispatcher changes the dogfood dispatch-timing calculus (push→settle→dispatch; self-upgrade restarts web and double-spends agents); first deploy must be an explicit opt-in.
- **No `Interval` on the component** — polling-only at the framework default 10s (`defaultComponentInterval`, `command.go:819`); never notify-only, and there is no NOTIFY trigger to consume anyway.

**Files:**
- Modify: `atc/component.go` (constant)
- Modify: `atc/atccmd/command.go` (flags; K8s block wiring; route-deps additions)

**Steps:**

- [ ] **Step 1: Constant.** In `atc/component.go` after `ComponentAgentRunSecretReaper`:

```go
	ComponentAgentDispatcher               = "agent_dispatcher"
```

Run: `go build ./atc/` — clean.

- [ ] **Step 2: Flags.** In the `RunCommand` struct next to `AgentDailyBudgetUSD` (:238):

```go
	AgentDispatcherEnabled     bool          `long:"agent-dispatcher-enabled" description:"Run the autonomous agent-ticket dispatcher loop (Kubernetes runtime only). When off, tickets dispatch only via the manual route/fly."`
	AgentDispatcherMaxAttempts int           `long:"agent-dispatcher-max-attempts" default:"3" description:"Max automatic re-dispatches per ticket (reconciler send_back requeues); past the cap the ticket errors. 0 = uncapped."`
	AgentRunTimeout            time.Duration `long:"agent-run-timeout" default:"6h" description:"Per-run agent principal token expiry (contracts §2.8.2). The run secret itself is collected by the run-secret reaper on run completion."`
```

- [ ] **Step 3: Route deps.** In the `dispatch.NewHTTPHandler(dispatch.Deps{...})` construction (`command.go:2395-2405`) add the new fields so the manual route gets admission + attribution + flag-driven expiry too:

```go
				Users: db.NewAgentUserLookup(dbConn),
				Budget: budget.NewChecker(
					db.NewAgentCostLedgerFactory(dbConn),
					dispatch.NewTicketBudgets(db.NewAgentTicketsFactory(dbConn), db.NewAgentWorkflowsFactory(dbConn)),
					budget.Config{GlobalDailyCapUSD: cmd.AgentDailyBudgetUSD},
				),
				RunTimeout: cmd.AgentRunTimeout,
```

(`SecretLabels` stays nil here — no clientset in `constructAPIMembers`; Risks R4.)

- [ ] **Step 4: Component wiring.** In the K8s block of `backendComponents`, AFTER the `agent_run_secret_reaper` append (`command.go:1383`) and still inside `if cmd.Kubernetes.Namespace != ""`:

```go
		if cmd.AgentDispatcherEnabled {
			dispatcherDeps := dispatch.Deps{
				Tickets:     db.NewAgentTicketsFactory(dbConn),
				Workflows:   db.NewAgentWorkflowsFactory(dbConn),
				Templates:   dispatch.NewTeamTemplateSaver(teamFactory, atc.DefaultTeamName),
				Runs:        dbPipelineRunFactory,
				Principals:  db.NewAgentPrincipalsFactory(dbConn),
				Credentials: db.NewAgentUserCredentialsFactory(dbConn),
				Secrets:     cmd.agentRunSecrets(), // the ONE shared lazy attacher (bound just above)
				Users:       db.NewAgentUserLookup(dbConn),
				Budget: budget.NewChecker(
					db.NewAgentCostLedgerFactory(dbConn),
					dispatch.NewTicketBudgets(db.NewAgentTicketsFactory(dbConn), db.NewAgentWorkflowsFactory(dbConn)),
					budget.Config{GlobalDailyCapUSD: cmd.AgentDailyBudgetUSD},
				),
				SecretLabels:   dispatch.NewK8sRunSecretLabeler(k8sClientset, cmd.Kubernetes.Namespace),
				RunTimeout:     cmd.AgentRunTimeout,
				ATCExternalURL: cmd.ExternalURL.String(),
				RepoBaseURL:    cmd.AgentRepoBaseURL,
			}
			components = append(components, RunnableComponent{
				Component: atc.Component{
					Name: atc.ComponentAgentDispatcher,
				},
				Runnable: dispatch.NewDispatcher(dispatcherDeps, dispatch.LoopConfig{
					RunReader:   dbPipelineRunFactory,
					Questions:   nil, // plan 08's checkpoint seam; checkpoints are render-refused until it lands
					MaxAttempts: cmd.AgentDispatcherMaxAttempts,
				}),
				// Interval deliberately omitted: defaultComponentInterval (10s)
				// polling — agent_tickets has no NOTIFY trigger (see the plan's
				// migration-allocations section for the recorded decision).
			})
		}
```

Confirm scope first: `teamFactory`, `dbPipelineRunFactory`, `dbConn`, `k8sClientset` must all be in scope in `backendComponents` (verified 2026-07-17: `backendComponents` spans `command.go:1100-1403`; the local `teamFactory := db.NewTeamFactory(dbConn, lockFactory)` sits at `command.go:1119` and `dbPipelineRunFactory := db.NewPipelineRunFactory(...)` at `command.go:1126` — both in scope at the insertion point). Grep with `grep -n "teamFactory\|dbPipelineRunFactory" atc/atccmd/command.go` and **reuse the existing `teamFactory` variable directly** — do NOT grep only for `dbTeamFactory` (that is a differently-named local inside the unrelated `constructPool` at `command.go:1428`, not in scope here) and do NOT construct a second `db.NewTeamFactory(...)`.

- [ ] **Step 5: Build + vet**

Run: `cd /Users/tdmtrader/concourse/concourse && go build ./atc/... && go vet ./atc/atccmd/ && go test ./agent/dispatch/`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add atc/component.go atc/atccmd/command.go
git commit -m "feat(atc): agent_dispatcher component - K8s-gated, off by default (--agent-dispatcher-enabled), polling-only 10s" -m "Route deps gain admission/user-resolution/run-timeout; loop shares the lazy secret attacher." -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 11 [Slice E]: DB-backed loop + admission + reconciler specs

**Re-scopes plan 11 Task 12** onto the landed `atc/db/agent_dispatch_test.go` (same file, same suite fixtures: `dbConn`, `lockFactory`, `teamFactory`, `defaultTeam`, `checkFactory`, `logger`). **Postgres required — local-verify, never a loop gate** (Risks R5).

**Files:**
- Modify: `atc/db/agent_dispatch_test.go`

**Steps:**

- [ ] **Step 1: Append the specs.** Read the file first and reuse its existing workflow fake (`liveOnlyWorkflows`) and deps shape verbatim; the spec SHAPES below are normative, the identifiers must match the file:

```go
var _ = Describe("the dispatcher loop over real stores", func() {
	// smokeWorkflows returns the file's landed smoke fixture as a
	// WorkflowResolver — a renderable spec_delivery:files workflow with one
	// agent step. Mirrors the inline liveOnlyWorkflows construction at the
	// top of this file (the "dispatching a ticket end-to-end" Describe) so
	// DispatchOne renders and persists cleanly. No Budget block: the
	// over-budget spec caps via the ticket's own budget_usd + a ledger row.
	smokeWorkflows := func() dispatch.WorkflowResolver {
		return liveOnlyWorkflows{def: &workflow.Definition{
			Name: "smoke", Version: 2, ContentHash: "hash2", Live: true,
			Config: workflow.Config{
				Name:         "smoke",
				SpecDelivery: "files",
				Defaults:     workflow.Defaults{Model: "claude-sonnet-5", MaxTurns: 5},
				Prompts:      map[string]string{"do": "Read ticket/spec.md and do it."},
				Steps: []workflow.Step{
					{Agent: "implement", Prompt: "do", Inputs: []string{"ticket"}, Outputs: []string{"workspace"}},
				},
			},
		}}
	}

	newDeps := func(workflows dispatch.WorkflowResolver) dispatch.Deps {
		return dispatch.Deps{
			Tickets:   db.NewAgentTicketsFactory(dbConn),
			Workflows: workflows,
			Templates: dispatch.NewTeamTemplateSaver(teamFactory, defaultTeam.Name()),
			Runs:      db.NewPipelineRunFactory(logger, dbConn, lockFactory, checkFactory),
			Budget: budget.NewChecker(
				db.NewAgentCostLedgerFactory(dbConn),
				dispatch.NewTicketBudgets(db.NewAgentTicketsFactory(dbConn), workflows),
				budget.Config{},
			),
			ATCExternalURL: "http://concourse.home",
			RepoBaseURL:    "https://github.com",
		}
	}

	queueTicket := func(budgetUSD *float64) int {
		ticketsFactory := db.NewAgentTicketsFactory(dbConn)
		id, err := ticketsFactory.Create(&tickets.Ticket{
			Title: "loop me", Body: "b", Origin: "fly",
			Repo: "tdmtrader/jetbridge", UserName: "tdm", CreatedBy: "tdm",
			WorkflowName: "smoke", BudgetUSD: budgetUSD,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(ticketsFactory.Transition(id, tickets.StateDraft, tickets.StateQueued,
			tickets.TransitionMeta{})).To(Succeed())
		return id
	}

	It("dispatches every queued ticket in one pass", func() {
		deps := newDeps(smokeWorkflows())
		id1, id2 := queueTicket(nil), queueTicket(nil)

		d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{})
		Expect(d.Run(context.Background())).To(Succeed())

		store := db.NewAgentTicketsFactory(dbConn)
		for _, id := range []int{id1, id2} {
			got, found, err := store.Get(id)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(got.State).To(Equal(tickets.StateRunning))
			Expect(got.PipelineRunID).ToNot(BeNil())
		}
	})

	It("defers an over-budget ticket, leaving it queued", func() {
		deps := newDeps(smokeWorkflows())
		one := 1.0
		id := queueTicket(&one)

		// Spend past the cap (SourceAgentStep counts; harvest_judge would not).
		ledger := db.NewAgentCostLedgerFactory(dbConn)
		Expect(ledger.Insert(budget.LedgerEntry{
			TicketID: &id, Source: budget.SourceAgentStep, CostUSD: 2.0,
		})).To(Succeed())

		d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{})
		Expect(d.Run(context.Background())).To(Succeed())

		got, _, err := db.NewAgentTicketsFactory(dbConn).Get(id)
		Expect(err).ToNot(HaveOccurred())
		Expect(got.State).To(Equal(tickets.StateQueued), "over-cap must stay queued")
		Expect(got.PipelineRunID).To(BeNil())
	})

	It("reconciles a run that died before harvest to needs_review", func() {
		runFactory := db.NewPipelineRunFactory(logger, dbConn, lockFactory, checkFactory)
		deps := newDeps(smokeWorkflows())
		deps.Runs = runFactory
		id := queueTicket(nil)

		d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{RunReader: runFactory})
		Expect(d.Run(context.Background())).To(Succeed())

		store := db.NewAgentTicketsFactory(dbConn)
		got, _, err := store.Get(id)
		Expect(err).ToNot(HaveOccurred())
		Expect(got.State).To(Equal(tickets.StateRunning))

		// Kill the run pre-harvest.
		run, found, err := runFactory.GetRunByID(*got.PipelineRunID)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(run.Finish(db.PipelineRunFailed)).To(Succeed())

		Expect(d.Run(context.Background())).To(Succeed())
		got, _, err = store.Get(id)
		Expect(err).ToNot(HaveOccurred())
		Expect(got.State).To(Equal(tickets.StateNeedsReview))
	})
})
```

Add missing imports (`agent/budget` — `agent/dispatch`, `agent/api/tickets`, `atc/db` are already imported by the file).

- [ ] **Step 2: Run (local-verify)**

Run: `cd /Users/tdmtrader/concourse/concourse && pg_isready && ginkgo --focus="dispatcher loop" ./atc/db/`
Expected: 3 specs PASS. Then the full package once: `ginkgo ./atc/db/` (~90s) — no regressions.

- [ ] **Step 3: Commit**

```bash
git add atc/db/agent_dispatch_test.go
git commit -m "test(db): dispatcher loop + budget deferral + reconciler over real stores" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 12 [Slice E]: Contract amendments + plan bookkeeping + Task 15 doc touch

Doc-only. SS11 is APPEND-ONLY and its tail already holds a 2026-07-18-dated entry (harvest v0.5 — the stamp came from a pod UTC clock): append AFTER it; the log is append-ordered, not date-ordered. **Cross-plan: all five remainder plans append to this §11 tail — re-read it at commit time and append after whatever entry is now LAST (a sibling may have appended since); treat §11 as single-writer per merge window and land docs commits serially.**

**Files:**
- Modify: `docs/superpowers/plans/agentic-platform/00-shared-contracts.md` (§11 tail)
- Modify: `docs/superpowers/plans/agentic-platform/11-dispatch.md` (amendment log)
- Modify: `agent/dispatch/` package doc (hand-dispatch note — all that remains of plan 11 Task 15)

**Steps:**

- [ ] **Step 1: Append to SS11** (after the 2026-07-18 harvest v0.5 entry):

```markdown
- 2026-07-17 (corrective, dispatch bookkeeping — appended after the 2026-07-18 entry; the log is append-ordered): the manual-dispatch entry above says per-run principal minting/secret attach were "deferred as a set", but commit 9a8eaf452c landed the mint+attach the SAME day (resolveRunCredential user-first/platform-fallback, principal `run-<id>` 4-scope 24h, lazySecretAttacher bridge in command.go); the later dogfood entry already acknowledged "minted but no consumer until wave 3". Executors must AMEND attachRunSecret, never re-implement it. No code change; text correction only.
- 2026-07-17 (dispatch remainder — dispatcher loop, budget admission, run-completion reconciler; co-signed ticket-core (b, c), pipeline-runs (d), credentials-and-budgets (a); all additive):
  (a) budget admission is LIVE: `dispatch.TicketBudgets` (tickets.budget_usd ?? FROZEN-workflow default; store errors propagate — no fail-open) replaces `budget.NoTicketBudgets{}` at BOTH checker sites (engine `constructEngine`, costs API via an additive `api.NewHandler` param), arming the already-landed step-level enforcement; `DispatchOne` gains nil-tolerant `Deps.Budget` admission (TicketRemaining + GlobalDailyRemaining BEFORE CreateRun) — over-cap → `ErrBudgetExhausted`, ticket STAYS queued, route maps 409; platform faults error and stay queued.
  (b) `tickets.Update` gains `UserID *int` (non-state writer, additive): dispatch resolves users.id from UserName via new `db.NewAgentUserLookup` (most-recent-login wins across connectors) and persists it at dispatch — the wave-4 leg the create handler's comment promised; user-first credential funding activates. Expired vaulted credentials are now skipped with the owner named in the error.
  (c) attempt-cap decision: `queued→errored` is NOT added to the §1.7 matrix; the cap is enforced on the reconciler's REQUEUE edge (`running→errored` with error_detail when a send_back would exceed `--agent-dispatcher-max-attempts`). The dispatcher loop dispatches ANY queued ticket (queued = explicit intent, incl. human re-queues).
  (d) `db.PipelineRunFactory` gains additive `GetRunByID(id)` (reconciler read path; mirrors GetRun).
  (e) §2.8.2 principal drift RESOLVED in the contract's favor: minted name is now `agent-run-<run-id>` (= `credentials.RunSecretName`; fixes the reaper's revoke-by-name path), scopes gain `questions:answer` (5 total), expiry = now + `--agent-run-timeout` (new flag, 6h default; the landed hardcoded 24h remains the zero-value fallback). Secret NAME unchanged (exec-consumed). `concourse/ticket` label applied best-effort via `dispatch.RunSecretLabeler` (K8s strategic-merge patch; loop wiring only — the route path has no clientset and skips it for now).
  (f) component: `atc.ComponentAgentDispatcher`, K8s-gated, OFF by default behind `--agent-dispatcher-enabled`, POLLING-ONLY at the 10s default (agent_tickets has NO NOTIFY trigger; none added — zero migrations; deployed head is 1773106066 (the workflow-source `agent_workflow_source_manifest` column), so a future trigger must take 1773106067+; the PARK-V2-reserved 1773106065 is now stranded below head and renumbers up at its own land time, with the C2 dual-constant bump). Reconciler ships with the F17 checkpoint branches DORMANT behind a nil `QuestionLister` seam local to agent/dispatch (`CheckpointRow` projection); plan 08 supplies the adapter. Harvest remains the primary writer of running→needs_review; stale = benign (two-writers recorded).
```

- [ ] **Step 2: Append to plan 11's amendment log** (`11-dispatch.md`, after the 2026-07-17 entry):

```markdown
- **2026-07-17 (remainder plan):** Tasks 8/11/11b/13 are superseded by `remainders/2026-07-17-dispatcher-budget-reconciler.md` (amended for the landed DispatchOne/Deps shape, the no-queued→errored matrix reality, polling-only, the lazy-attacher reuse, and the dormant checkpoint seam). Task 9's mint/attach LANDED in 9a8eaf452c — its remainder (user-id, expiry check, principal reconciliation, label) is Tasks 7-9 there. Tasks 12/14 re-scoped as its Tasks 11/13. Task 15 is de-facto done (manual-dispatch slice); only the doc touch remains. Tasks 2-6/7 renderer surfaces: landed in v0 form with wave-3 refusals — amend, never green-field. Task 11c (PARK-V2) is UNTOUCHED and stays with the platform-mcp-hitl item.
```

- [ ] **Step 3: Hand-dispatch doc touch (Task 15 remainder).** In the `agent/dispatch` package comment (or README if one exists), record: the wave-3 hand-written template is retired; hand-dispatch = `fly agent tickets dispatch` (or the route); the autonomous loop is the same call under `--agent-dispatcher-enabled`.

- [ ] **Step 4: Commit**

```bash
git add docs/superpowers/plans/agentic-platform/00-shared-contracts.md docs/superpowers/plans/agentic-platform/11-dispatch.md agent/dispatch/
git commit -m "docs(contracts): dispatch-remainder amendments - corrective 9a8eaf452c entry, co-signs, principal reconciliation, polling-only decision" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 13 [Slice F]: theborg live end-to-end autonomous dispatch proof

**Re-scopes plan 11 Task 14.** Native-only; respects the dogfood dispatch-timing rule (push → settle → dispatch; self-upgrade restarts web and double-spends agents). The component ships OFF — this task is the controlled first enablement on theborg/cicd.

**Steps (checklist, human at the wheel):**

- [ ] 1. Land + release everything above through the normal release pipeline; WAIT for the deploy to settle (`kubectl -n cicd get pods` steady; `fly -t cicd builds` quiet; per MEMORY, no dispatches mid-upgrade).
- [ ] 2. Enable the loop: add `CONCOURSE_AGENT_DISPATCHER_ENABLED=true` to the web deployment env (helm value or `kubectl -n cicd set env deployment/<web> CONCOURSE_AGENT_DISPATCHER_ENABLED=true`); wait for the web rollout to settle again.
- [ ] 3. Verify the component registered: web logs show `agent-dispatcher` sessions every ~10s (`kubectl -n cicd logs deploy/<web> | grep agent-dispatcher | tail`).
- [ ] 4. Autonomous dispatch: `fly agent tickets create --title "loop smoke" --repo <repo> --workflow <smoke-workflow> --budget 5 --queue` (NO `--dispatch`). Within ~15s `fly agent tickets show <id>` shows `running` with a run id and pipeline `agent-ticket-<id>`; dispatched-by attribution reads `agent-dispatcher`.
- [ ] 5. Reconciler proof: abort the run's build mid-flight (`fly -t cicd abort-build` on the instance pipeline's `run` job). After the lifecycler completes the run, within ~15s the ticket walks to `needs_review` with NO manual transition; web logs show `reconciled` with `to:needs_review`.
- [ ] 6. Admission proof: give a ticket `--budget 0.01` after its ledger already carries spend (or pre-insert a ledger row), queue it, observe `dispatch-deferred-over-budget` in logs and the ticket parked in `queued`. Do NOT choke the shared `--agent-daily-budget-usd` on cicd during work hours to test the daily leg — that starves real dogfood work.
- [ ] 7. Secret/principal/user proof: `kubectl -n cicd get secret agent-run-<runid> -o jsonpath='{.metadata.labels}'` shows both `concourse/agent-run` and `concourse/ticket: "<ticket-id>"`; the principals table shows `agent-run-<runid>` with 5 scopes and expiry ≈ now+6h; the ticket row has `user_id` set; new ledger rows attribute along the user credential path.
- [ ] 8. Single-dispatch proof: exactly ONE run per queued ticket across the window (Coordinator + guarded transition claim); the `agent-ticket-<id>` template shows run count 1 (plus deliberate re-dispatches only).
- [ ] 9. Rollback drill: remove the env var, roll the web, confirm the component disappears from logs and manual dispatch still works.
- [ ] 10. Record outcomes (incl. any flake) in MEMORY and, if behavior deviated from this plan, a follow-up SS11 amendment.

---

## 5. Migration allocations

**This item lands ZERO migrations.** Decision record:

- Everything schema-shaped already landed: `agent_tickets.budget_usd`/`user_id`/`attempt_count` (1773106062), `pipeline_runs` completion columns (1773106031). `users` is ancient core schema.
- **Polling-only trigger decision:** `agent_tickets` has no NOTIFY trigger (verified in the 1773106062 up.sql) and this plan does NOT add one. The dispatcher polls at the component framework's default 10s (`defaultComponentInterval`, `command.go:819`) — ≤10s dispatch latency against multi-minute agent runs is immaterial, and it keeps plan 11's "dispatch owns no migrations" property intact. MEMORY's rule (never notify-only) is trivially satisfied.
- **If a NOTIFY trigger is ever chosen** (owner call, not this plan): it MUST take **1773106067** or higher — above the deployed head **1773106066** (the workflow-source `agent_workflow_source_manifest` column, landed 2026-07-17 in the parallel slice-a advance; dual constants NOW READ 1773106066: `atc/db/migration/legacy_upgrade_test.go` `jetbridgeHeadMigration = 1773106066` AND `docs/migration/migrate-preflight.sh` `JETBRIDGE_VERSION=1773106066`, both to be bumped in the SAME commit per C2). **1773106065 is now STRANDED BELOW head**: it was reserved for PARK-V2 `agent_run_step_state`, but the workflow-source slice took 1773106066 first, so that reserved migration must itself be renumbered above the then-current head when it lands (the hole rule; SS11 2026-07-12/2026-07-17). Migrations merge lowest-version-first; the version-pointer migrator silently skips lower numbers merged late (ticket-core renumber precedent, SS11 2026-07-17).
- No renumbering of existing migrations is needed for this item.

---

## 6. Risks & open decisions

**R1 — Attempt-cap mechanism (owner sign-off needed).** This plan enforces the cap on the reconciler's requeue edge (`running→errored` past cap) instead of plan 11 Task 11's `queued→errored`, which is NOT a legal edge in the landed matrix (`types.go:41-49`). Consequences: (a) no ticket-core matrix change; (b) a human re-queue (errored→queued) always dispatches regardless of attempt_count — treated as explicit intent; (c) the cap is DORMANT until checkpoints land (nothing automatic requeues today, so nothing can loop). Alternative rejected: a co-signed `queued→errored` matrix ADD — heavier, and it would let the loop erroneously kill human-requeued tickets. If the owner prefers the matrix ADD, it is C3-additive + one SS11 entry + a matrix table-test row, and the loop gains a pre-dispatch cap check.

**R2 — Refused-ticket log cadence.** A render-refused queued ticket re-renders and Error-logs every 10s pass forever (render is pure CPU — cheap, but noisy). Options if the noise bites: bounce `queued→draft` (legal edge, but that edge records no reason — no error_detail side effect) or a per-pass dedupe. Shipped behavior: stay queued, log loud. Owner may revisit after dogfood.

**R3 — Costs-API budget swap changes gauge semantics.** The `/agent` console per-ticket remaining flips from "uncapped" to real caps the moment Task 3 lands, and `TicketRemaining` now ERRORS (500 on that endpoint) if a budget read fails rather than reporting uncapped. Deliberate (fail-open budget reads are worse); noted for UI triage.

**R4 — Manual-route dispatches skip the `concourse/ticket` label** (no clientset in `constructAPIMembers`; the lazy bridge covers Attach but not Label). Operator-filtering only — cosmetic. Fix later by extending `lazySecretAttacher` with a Label leg if it matters.

**R5 — In-pod gate vs `atc/db` specs (verify BEFORE loop-dispatching Tasks 5/7/11).** Harvest gates run full-scope `go test ./...`. Tickets #13/#14 passed with `atc/db` in tree, which implies DB suites skip or fail-soft without Postgres in the agent pod — but this is UNVERIFIED. Action: before assigning any task that adds `atc/db` specs to a loop ticket, run the gate command in a Postgres-less container and confirm `atc/db` skips cleanly; otherwise keep Tasks 5/7/11 native. Do not guess.

**R6 — Multiple budget-checker instances** (engine, dispatcher, route, costs API) are independently constructed but stateless over the same ledger — safe by design (§2.7: every dollar enters the ledger once; checks are reads). No shared-state hazard.

**R7 — First live enablement risks double-spend if mistimed.** The enable flag + Task 13's settle-first checklist are the mitigation; never enable mid-release. Rollback = unset one env var.

**R8 — Test-surface drift.** Task 8's principal assertions assume an enumeration on `principals.MemoryStore`, and Task 6 assumes (or adds) the `attempt_count` bump in `tickets.MemoryStore` — both are checked-and-adapt steps in their tasks, not silent assumptions. Do not add production methods just for tests.

---

## 7. Complexity, risk, and recommended execution level

Honest read: the hard design questions are settled in this plan (admission placement, cap mechanism, seam shapes, wiring); what remains is disciplined execution against exact landed code, plus Postgres-backed legs and one live leg that the loop's gates cannot verify.

**Recommendation: SPLIT.**

| Slice | Tasks | Level | Why |
|---|---|---|---|
| A (budget lib + admission) | 1-2 | **loop-opus** | Pure Go, hand fakes + counterfeiter fakes, complete code+tests in-plan; comfortably inside the ticket-#14 envelope (~6 files). Moderate judgment only where landed tests need touching. |
| A (checker swaps) | 3 | **native-sonnet** (or fold into the Task 10 native session) | Touches `command.go` + the `api.NewHandler` signature + its test-suite caller — shared surfaces, small diff, human-reviewed is cheaper than coordinating a loop merge. |
| B (loop) | 4 | **loop-opus** | Well-specced pure-Go TDD around the landed `DispatchOne`; the error-classification table is written out verbatim. |
| B (wiring) | 10 | **native-fable** | `command.go`/flags/component registration + scope verification — the one place mid-flight judgment and a full local build matter; also the rollback surface and the shared-attacher invariant. |
| C (GetRunByID) | 5 | **native-opus** | Tiny, but Postgres spec + counterfeiter regen + same-commit co-sign amendment; the gate cannot verify the spec (R5 unresolved). |
| C (reconciler) | 6 | **loop-fable** | Largest pure-Go task; the F17 tree + dormant-seam subtleties reward the stronger model, but it is fake-driven TDD with complete code in-plan — in-envelope (~3 files). loop-opus acceptable if fable budget is tight; the tree tests are exhaustive enough to catch drift. |
| D (user-id) | 7 | **native-opus** | Crosses four packages incl. ticket-core types + DB factory + two Postgres specs + co-sign — coordination-heavy for a loop ticket. |
| D (principal/cred + label) | 8-9 | **loop-opus** | Pure Go, client-go fake clientset, full code above; amends two functions in one file plus one new file. Merge gate: human confirms no landed test still asserts `run-<id>` before merging. |
| E (DB specs) | 11 | **native-sonnet** | Postgres-only verification the loop cannot run (R5); mechanical against the shapes above, but fixtures need eyeballs. |
| E (docs) | 12 | **native-sonnet** | Append-only normative doc edits; trivial mechanically, human review non-negotiable. |
| F (live) | 13 | **native-fable** | Live theborg, dispatch-timing rule, first enablement of an autonomous spender — owner attention required. |

Sequencing notes: loop tickets A(1-2) → B(4) → C(6) → D(8-9) all touch `agent/dispatch/dispatch.go`/`dispatch_test.go` at some point — dispatch them SERIALLY, never in parallel (worktree-collision rule + the shared Claude rate-limit window argue for serial anyway). Everything else fits in two native sessions: one for 3+5+7+10+11+12 (wiring + Postgres + docs — order: 5 and 7 before 10 so the wiring compiles against the final `Deps`), one for 13 (live). If the owner prefers fewer moving parts, the entire plan is a comfortable two-day native-fable execution; the loop split exists to dogfood the loop, not because native is infeasible.
