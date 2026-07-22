# Track E — Gateway Execution / Disposition Note

> **SUPERSEDED — historical implementation record (not executable).** The approved [Agentic Workflows as Functions over Snapshots design](../../../specs/2026-07-21-agentic-workflows-as-functions-design.md) and [Agentic Functions program plan](../../2026-07-21-agentic-functions-program.md) are authoritative. This document preserves the abandoned ticket-centric roadmap only. **Explicit superseded block:** every section below this banner, including migration reservations at `1773106100+`, `step_kind`, ticket/build/plan keys, restore runner/stub, and `primaryMetric` references, is historical and must not be implemented. **Keep:** fixtures, repetitions, evaluators, controls, and scorecards. **Supersede:** `step_kind`, ticket/build/plan keys, restore runner/stub, and the primary-metric switch.

> **For agentic workers:** this is a **disposition + re-grounding note**, not a task plan. It records why the bench
> adds nothing to `10-gateway-mcp.md`, spot-checks that plan's cited seams still exist at HEAD, and pins the one
> bench-adjacency fact. There is no code to write against the bench here. Execute `10-gateway-mcp.md` as written.

- **Descends-from:** `docs/superpowers/plans/agentic-platform/10-gateway-mcp.md` (unchanged charter), `docs/superpowers/specs/2026-07-19-agent-bench-design.md` §10 (gateway relationship: deliberately none)
- **Tasks:** 2 (T1 = disposition addendum prose; T2 = re-grounding checklist). Neither touches gateway code, gateway migrations, or `render.go`.
- **Complexity / Risk:** trivial / low — the bench claims zero gateway coupling; the only risk is silent seam drift in plan 10 since it was written, which T2 rules out.
- **Migrations:** **NONE.** The bench's `1773106100–102` block (fixtures / experiments+cells / scores) is owned by tracks A1/A2/B. The gateway owns no migration in `00-shared-contracts.md §1.1` and the bench adds none for it. On-disk head is `1773106091` (`create_agent_settings`); gateway consumes `agent_cost_ledger` (`1773106021`) — it never allocates a number.

---

## Context

The bench design reshapes the unbuilt improvement-loop workstreams (13-scorecards, 14-process-intel-experiments)
around step-level evaluation. `10-gateway-mcp.md` is **not** one of those workstreams. Spec §10 ("Gateway
relationship — deliberately none") and the handoff brief ("**E. gateway:** execute 10-gateway-mcp.md as written;
no bench coupling") are explicit: the gateway is the *primary agent's mid-flight capability*, not a bench component.

This note exists because a re-scoping wave is the moment latent drift bites: plan 10 was written **2026-07-08**
against branch `jetbridge` head `fb1c54fac2` (its own anchor caveat, line 44), and three prior waves plus the
2026-07-09 SSE delta have moved every shared file since. So the job here is narrow: (1) confirm plan 10's four
load-bearing seams — the auth/principal tier, its (absence of) migration numbers, the sidecar-image packaging
convention, and the ledger/metering path — still exist at HEAD; (2) restate that plan 10 executes **unchanged**
on its original charter; (3) record the single bench-adjacency fact and stop.

**What the gateway is (charter, verbatim from 10-gateway-mcp.md lines 15–20):** the `mcp-gateway` sidecar exposing
`request_review(diff, rubric)` and `ask_agent(prompt, provider, model)` over a provider-adapter layer (`claude`
v1; `codex`/`cursor`/a future scheduled-pod backend behind the same `Adapter` contract), with **universal
metering** of every cross-agent call into the flight-recorder events (`agent/schema`) and the cost ledger
(fire-and-forget `POST /api/v1/agent/costs`), and a **never-silent budget-slice cutoff** that halts with a
`budget cutoff:` `failed` signal instead of ever truncating quietly. None of this is a bench concept.

---

## Re-grounding checklist (verified at HEAD, this session)

All four named seams — plus the one hard cross-plan ordering dependency plan 10 flagged — are present and
compatible. Line numbers have shifted since 2026-07-08 (expected; plan 10 says anchor to named neighbors, not
raw numbers), so these are symbol-anchored.

| # | Seam plan 10 depends on | Status at HEAD | Anchor |
|---|---|---|---|
| 1 | **Auth / principal tier** — the gateway presents `AGENT_PRINCIPAL_TOKEN` (scope `costs:write`) to the cost route | **INTACT** | `principals.ScopeCostsWrite = "costs:write"` @ `agent/api/principals/types.go:18`; `SubmitAgentCostRecord` case wired to `checkAgentPrincipalHandlerFactory.HandlerForWithLegacyBypass(handler, rejector, principals.ScopeCostsWrite)` @ `atc/wrappa/api_auth_wrappa.go:133–137` |
| 2 | **Ledger / metering path** — fire-and-forget `POST /api/v1/agent/costs` of a `budget.LedgerEntry` with `Source: SourceGateway` | **INTACT** | route `{Path:"/api/v1/agent/costs", Method:"POST", Name: SubmitAgentCostRecord}` @ `atc/routes.go:312`; `SourceGateway = "gateway"` @ `agent/budget/budget.go:27`; `LedgerEntry` (carries `CacheReadTokens`/`CacheCreationTokens`/`Provider`/`Turns`/`CostUSD`/`Metadata`) @ `budget.go:62–79`; `agent_cost_ledger` table @ migration `1773106021` |
| 3 | **Metering event schema** — the §5 constants + payloads the gateway emits (no new constant added) | **INTACT** | `EventSubagentCall`/`EventSubagentResult`/`EventCostRecord`/`EventBudgetWarn`/`EventBudgetStop` @ `agent/schema/event_payloads.go:10–14`; payload structs `SubagentCallData`(:64) / `SubagentResultData`(:72) / `CostRecordData`(:86) / `BudgetData`(:98) |
| 4 | **Sidecar-image packaging convention** — §8.5 `deploy/MCP_IMAGES.md` + a copyable dev-mcp CI job | **PARTIAL — see drift row D3** | `deploy/MCP_IMAGES.md` present and names `mcp-gateway` / `ghcr.io/tdmtrader/mcp-gateway` / port `7782`. The copyable `build-mcp-dev-image` CI job is **not yet in** `deploy/concourse-pipeline.yml` (the doc itself, lines 20–21, says plan 04 Task 13 *will* add it). |
| 5 | **HARD ordering dep: 08 Task 9b in-place SSE upgrade of `atc/api/mcpserver`** (must land before gateway Task 7) | **LANDED — dependency now SATISFIED** | `const DefaultHeartbeat = 15 * time.Second` @ `atc/api/mcpserver/server.go:19`; `type ToolHandler func(ctx, args, progress func(string)) (any, error)` @ `:24`; `NewServerWithHeartbeat(d)` @ `:45` beside the unchanged `NewServer()` @ `:38` |
| 6 | **Adapter reference material** — ci-agent envelope parse the claude adapter mirrors (non-importable, copied) | **INTACT** | `ParseCLIEnvelope` @ `ci-agent/llm/result.go:57`, `cliEnvelope` @ `:40`, `cache_read_input_tokens` field @ `:9`; `ClaudeClient.Call` @ `ci-agent/llm/client.go:39` |
| 7 | **Mirror-shape sidecar** — the platform-mcp package the gateway copies its layout from | **INTACT** | `agent/platformmcp/{config,events,atcclient,...}.go`; serve-mode binary `cmd/platform-mcp/main.go` |
| 8 | **Gateway not yet built** (confirms plan 10 is unexecuted, ready) | **CONFIRMED** | `agent/gatewaymcp`, `cmd/gateway-mcp`, `deploy/Dockerfile.mcp-gateway` all absent |

## Drift table (since 2026-07-08)

| ID | Drift | Effect on plan 10 | Action |
|---|---|---|---|
| D1 | **Migration head moved** `1773106090` → `1773106091` (`create_agent_settings`, dispatcher runtime-toggle) landed after plan 10 was written. | **None.** The gateway owns zero migrations; it only *reads* `agent_cost_ledger` (`1773106021`) via the HTTP cost route. Head number is irrelevant to gateway execution. | No change. |
| D2 | **Auth symbol naming.** Plan 10 prose cites `auth.CheckAgentPrincipalHandler` (line 25; auth also referenced without the handler symbol at lines 37, 87); the landed shape is the factory `auth.CheckAgentPrincipalHandlerFactory` invoked via `.HandlerFor(...)` / `.HandlerForWithLegacyBypass(...)`. | **Cosmetic.** The `principal(costs:write)` tier the gateway's token must satisfy exists exactly and the cost route is wired to it. The gateway is an *outbound client* of this tier, so only the route's acceptance of a `costs:write` token matters — and it does. | No change; treat plan 10's name as "the principal tier", per its own line-anchor caveat. |
| D3 | **Copyable CI template not yet on-disk.** `build-mcp-dev-image` (dev-mcp/plan 04 Task 13) and `Dockerfile.platform-mcp` are not present in `deploy/`; only the §8.5 convention doc (`MCP_IMAGES.md`) has landed. | **No charter change**, but plan 10 Task 1(a)/its packaging task retain a **live dependency**: the "copy dev-mcp's `build-mcp-dev-image` template" step has no template to copy until plan 04 Task 13 lands. This was already a stated wave dependency; it is simply still open. | No change to plan 10; note the sequencing so the gateway's image-build task is scheduled after (or co-lands with) dev-mcp Task 13. |
| D4 | **SSE delta resolved.** Plan 10's hardest external dependency — 08 Task 9b's in-place SSE upgrade of the shared MCP server, flagged "MUST land before this plan's Task 7" — has **landed** (checklist row 5). | **Positive.** The one thing that could have blocked plan 10 Task 7 is now unblocked. `request_review`/`ask_agent` acquire streaming purely by consuming the upgraded `atc/api/mcpserver` (no gateway-local transport code), exactly as plan 10 designed. | No change; the blocker is cleared. |

**Net:** zero drift that touches plan 10's charter, contract surface, or task list. One cosmetic naming note (D2),
one benign head-number move (D1), one still-open upstream CI dependency that predates this note (D3), and one
resolved blocker (D4).

---

## Task 1 — Disposition addendum (prose; does NOT edit `00-shared-contracts.md`)

Per house discipline every bench track opens with a Task-1 contract addendum. Track E's addendum is a **null
disposition**: it records that the bench claims nothing from the gateway. When A1 Task 1 writes its
§11 amendment-log block (the assigned owner — amended 2026-07-19 post-review), the E line reads:

> *2026-07-19 (bench wave; owner: bench-A1 on behalf of Track E; re: gateway): Track E adds **no** migration, **no** route, **no**
> fly verb, and **no** code to `10-gateway-mcp.md`. The gateway owns no bench table (its block in §1.1 stays
> empty) and the bench's `1773106100–102` allocation is fixtures / experiments+cells / scores only (tracks A1/A2/B). The
> gateway executes on its original charter (spec §10). The single bench-touching fact is passive: a review-step
> **variant** (a workflow definition) that mounts the gateway sidecar is gateway-invocable out of the box, with
> **zero** change to the gateway (see Task 2 note). Consumers notified: none — nothing depends on this.*

This is prose describing the intended addendum. It is **not** an instruction to edit the contract file in this
track; A1 owns the actual §11 write. **Owning-task caveat — RESOLVED (amended 2026-07-19 post-review; see Open
decision 2):** the owner now exists on disk. The bench plans dir holds the full six-plan set (`A1-fixture-capture.md`,
`A2-replay-harness.md`, `B-evaluators-corpus.md`, `C-scorecards-two-tier.md`, `D-14-disposition.md`, this file,
plus `README.md`), and **A1 Task 1** carries the checklist bullet that (a) appends D's per-task-disposition pointer
to the §1.12 banner, (b) appends this file's null-disposition §11 line **verbatim** as its own entry (owner
"bench-A1 on behalf of Track E"), and (c) post-land, records one confirmation line in D's and E's own logs. The
former hazard — "a null disposition with no owning task silently never lands" — is closed by that named ownership.

**Basic-experience guardrail.** The bench's simplicity contract (spec §6, principle 6: *revealed, not upfront,
complexity*) says the minimal experiment is two fields — `step_kind` + `variants` — with defaults supplying
fixtures, repetitions, evaluator, controls, and budget. Track E protects that path by **subtraction**: because
the gateway is orthogonal, "use another provider" never becomes a bench spec field, a new flag, or a branch in the
two-field path. Provider choice lives entirely inside the *variant's own workflow definition* (whether it mounts
the gateway sidecar and what `provider`/`model` it asks `ask_agent`/`request_review` for) — exactly where a
production step already declares it. So `fly agent bench run --step review --variant review-prompts@v5` stays two
fields whether the variant calls one provider or three; provider diversity is revealed by opening the variant, not
by complicating the experiment surface. Any future edit that adds a gateway/provider knob to the bench experiment
spec violates this guardrail and belongs in the workflow definition instead.

## Task 2 — Re-grounding checklist (verification only; no code)

The checklist and drift tables above are the deliverable. **This is a point-in-time snapshot** (verified at HEAD
`644184e3f0`-era, session 2026-07-19); every shared file it checks has already moved once since plan 10 was written
on 2026-07-08, so the snapshot decays over the gap between here and plan 10's eventual execution. To reproduce the
verification (the greps run this session), an executor runs the block below — **all paths are repo-relative, so
`cd` to the repo root first** (`cd "$(git rev-parse --show-toplevel)"`):

```bash
cd "$(git rev-parse --show-toplevel)"   # all greps below are repo-root-relative
# 1. auth/principal tier + costs:write scope
grep -n "ScopeCostsWrite" agent/api/principals/types.go
grep -n "SubmitAgentCostRecord" atc/wrappa/api_auth_wrappa.go        # → HandlerFor...(ScopeCostsWrite)
# 2. ledger/metering path
grep -n "SubmitAgentCostRecord\|/api/v1/agent/costs" atc/routes.go
grep -nE "SourceGateway|type LedgerEntry|CacheReadTokens" agent/budget/budget.go
ls atc/db/migration/migrations/*agent_cost_ledger*                    # 1773106021
# 3. metering events (no new constant added by gateway)
grep -nE "EventSubagent(Call|Result)|EventCostRecord|EventBudget(Warn|Stop)|SubagentResultData|BudgetData" agent/schema/event_payloads.go
# 4. sidecar packaging convention (+ drift D3: template job not yet in pipeline)
ls deploy/MCP_IMAGES.md
grep -rn "build-mcp-dev-image" deploy/concourse-pipeline.yml || echo "D3: template job not landed yet"
# 5. HARD ordering dep — 08 Task 9b SSE upgrade (must precede gateway Task 7)
grep -nE "DefaultHeartbeat|func NewServerWithHeartbeat|type ToolHandler" atc/api/mcpserver/server.go
# 6. adapter references + mirror package + gateway-not-built
grep -nE "func ParseCLIEnvelope|type cliEnvelope" ci-agent/llm/result.go
ls agent/platformmcp/config.go cmd/platform-mcp/main.go
ls agent/gatewaymcp 2>/dev/null || echo "gateway not yet built — plan 10 ready to execute"
```

Expected: rows 1–3, 5, 6 all hit (INTACT); the pipeline grep in row 4 is the one intentional miss (drift D3);
the final `ls` reports the gateway absent. If any INTACT row misses, plan 10 has drifted beyond a line-number
shift and needs a re-anchor pass before execution — that, not any bench change, is the only thing that would
re-open Track E.

**Exit criterion (binds this snapshot to plan 10's execution moment).** Because the snapshot decays, it is not
enough to have run the greps in this session. The **plan-10 executor MUST re-run this exact grep block immediately
before starting plan 10's Task 1**, from the repo root. Any INTACT-row miss (rows 1–3, 5, 6) at that moment
**re-opens Track E and blocks plan 10** until a re-anchor pass reconciles the drifted seam; only then does plan 10
proceed. The row-4 pipeline miss (D3) and the gateway-absent `ls` remain the two expected misses and do not block.

---

## The single bench-adjacency fact (spec §10)

Spec §10 spells out the one place the bench and gateway touch, and it requires **zero** gateway change:

> A review-step variant that wants another provider is simply a **workflow definition that mounts the gateway
> sidecar** — the same way a production step would. Provider diversity thereby becomes measurable offline and
> usable mid-flight through one contract, and "does a mid-run fresh-context review improve outcomes?" is itself
> just a **workflow-cell experiment**.

Concretely, this rides seams that already exist — some already frozen by other tracks (render library, gateway env
contract), one **resolved 2026-07-19 post-review** (replay cost-route wiring — benchstub absorb-and-record; second
bullet):

- **A variant is a workflow definition** (bench principle 1: "a variant *is* a workflow definition"). A workflow
  definition can declare a gateway sidecar today — that is exactly what the gateway's own §8.1 env contract
  (`GATEWAY_MCP_URL=http://127.0.0.1:7782/mcp`, plus the run's `AGENT_PRINCIPAL_TOKEN`/`ATC_EXTERNAL_URL`/
  `AGENT_BUDGET_SLICE_USD`) is for. The bench renderer (track A) reuses `agent/dispatch`'s render library
  additively — `RenderAgentStep` @ `agent/dispatch/render.go:63` and `Render` @ `:152` — and **must not touch the
  refusal chain** (`render.go:152–212`), which is unrelated to sidecars. Sidecar wiring is a property of the
  workflow-definition input the renderer already consumes, so a gateway-mounting variant renders through the same
  path with no bench-side special case.
- **A gateway call in replay meters via the ordinary cost route — but wiring that route through the stub is
  track-A work, not free.** The gateway meters by POSTing a `budget.LedgerEntry` to the ordinary cost route
  (`POST /api/v1/agent/costs`, checklist row 2) with its own existing `Source: budget.SourceGateway` (`"gateway"`,
  `agent/budget/budget.go:27`) — it does **not** carry a new bench `source` value, and per the 2026-07-19 review
  none will exist (A2 §1.12.3: experiment spend lands as ordinary `source:'agent_step'` ledger rows attributed to
  the experiment via the `agent_bench_cells.pipeline_run_id` → run → metrics join; the `agent_cost_ledger` CHECK
  gains no bench value). How a gateway (or step) sub-call's spend is *attributed to the experiment* and *seated
  under the experiment budget envelope* (spec §3) is owned by A/B's harness, not resolved by the gateway.
  Concretely there was a real replay-wiring seam here, not a free ride: the replay stub redirects `ATC_EXTERNAL_URL`
  (`agent/platformmcp/config.go:16,50`) and the gateway sidecar reads that **same** var for its cost POST, yet the
  pre-review stub ATC mux (`agent/platformmcp/contracttest/stub_atc.go`) implemented only ticket/spec/plan/tasks/
  questions routes — no `POST /api/v1/agent/costs` — so a cost POST to the redirected stub URL would have 404'd.
  **Resolved 2026-07-19 post-review (A2 Task 5 + A2 Open decision 7):** A2's `benchstub` absorbs + records the cost
  POST (`200 {"status":"recorded"}` + a `submit_cost` absorbed-write) — deliberately absorb-and-record, NOT forward
  (forwarding would put a real credential inside the isolation boundary). Replay provider spend on gateway-mounting
  variants is therefore visible in the absorbed-writes log only, not the ledger, until a forwarding/credential
  design lands. See Open decision 3.
- **"Does a mid-run fresh-context review help?" is a workflow-cell experiment**, not a gateway feature. Per plan
  14 §1.12.2 (preserved verbatim by the bench: "runner dispatches tickets, not the renderer"), a workflow cell
  creates an ordinary ticket and rides the real dispatcher; the variant under test is a workflow definition that
  either does or does not invoke `request_review` mid-flight. The gateway is the mechanism the variant uses; the
  bench is the harness that scores whether using it changed the outcome. One contract, two consumers, no coupling.

**Conclusion:** Track E ships nothing. `10-gateway-mcp.md` executes unchanged on its original charter. The bench
is a downstream consumer of the gateway's already-frozen contract, never a modifier of it.

---

## Execution notes

- **Test tiers:** none for Track E specifically — it writes no code. Plan 10's own tiers stand unchanged
  (`go test ./agent/gatewaymcp/...` + `cmd/gateway-mcp`; contract tests; the `//go:build live` throwaway-namespace
  sidecar test per the SC-11 house lesson that a fake clientset can't exercise sidecar transport). The bench's own
  live test (spec §"Testing approach": one real replay pod with a stub sidecar) is track A/B's, not E's.
- **Merge gate:** n/a for E (no code). The re-grounding greps above are the whole verification.
- **No `render.go` edits, no migration, no route, no fly verb** originate in Track E.

## Scope-out

- Any change to `10-gateway-mcp.md`'s charter, tasks, contract surface, or migrations (there are none to change).
- Building the review-step-variant-that-mounts-the-gateway workflow definition — that is a *user/workflow-author*
  artifact and, when exercised as an experiment, a track-A/B concern, not a plan.
- Adding a provider/gateway field to the bench experiment spec (violates the basic-experience guardrail).
- Re-anchoring plan 10's line numbers — only needed if a checklist INTACT row ever misses; not needed today.

## Open decisions

1. **D3 sequencing only:** confirm the gateway's image-build task is scheduled after (or co-lands with) dev-mcp
   plan 04 Task 13, which owns the `build-mcp-dev-image` CI template the gateway copies. This is a pre-existing
   wave dependency surfaced here, not a new decision — resolved by scheduling, not by any edit to plan 10.
2. **Cross-plan handoff — who lands the null-disposition §11 lines. RESOLVED (amended 2026-07-19 post-review):**
   the owning plan now exists — **A1 (`bench/A1-fixture-capture.md`) Task 1** explicitly enumerates absorbing
   **both** D's and E's deferred §11 lines by name (`D-14-disposition.md`, `E-gateway-disposition.md`): D's
   per-task-disposition pointer is appended to the §1.12 banner, E's null-disposition line lands verbatim as its
   own entry (owner "bench-A1 on behalf of Track E"), and post-land one confirmation line is recorded in D's and
   E's own logs. The handoff is recorded on the owning plan, no longer assumed.
3. **Replay cost-route wiring (skeleton open Q3). RESOLVED (amended 2026-07-19 post-review) — owned by A2 Task 5 +
   A2 Open decision 7:** A2's `benchstub` absorbs + records `POST /api/v1/agent/costs` (one mux entry returning
   `200 {"status":"recorded"}` + a `submit_cost` entry in the absorbed-writes log) — absorb-and-record, deliberately
   NOT forward, since forwarding would put a real credential inside the isolation boundary. A gateway (or step)
   cost POST in replay therefore no longer 404s, but gateway-mounting variants' provider spend in replay appears
   **only in the absorbed-writes log, not the ledger**, until a forwarding/credential design lands (A2 Open
   decision 7 — coordinate with plan 10 + budget before trusting cost metrics on gateway-mounting experiments).
   The original seam observation stands as history: the replay stub redirects `ATC_EXTERNAL_URL`
   (`agent/platformmcp/config.go:16,50`), the gateway reads that same var for its cost POST, and the pre-review
   stub mux had no costs route. Track E builds nothing here; it names the seam and its now-assigned owner.
