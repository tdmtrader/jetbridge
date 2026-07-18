# Agentic-platform remainder plans — program index

**Date:** 2026-07-17 · **Branch:** jetbridge · **Head migration:** 1773106066 (next free 1773106067)

These five plans scope the work remaining after the 2026-07-17 landings (ticket-core,
manual-dispatch, harvest v0 + gates v0.5, agentic-ui waves A–F, and the
workflow-source-format slice-a that a parallel session landed while these were being
written). Each was written with the `superpowers:writing-plans` discipline, then
adversarially reviewed on three lenses (grounding / contracts / quality), fixed, and
cross-checked as a set. Every plan's own final section carries a per-slice execution-level
recommendation; this index records only the **inter-plan** constraints those plans cannot
see individually.

## The five plans

| Plan | Descends from | Tasks | Complexity / Risk | Migrations |
|---|---|---|---|---|
| [judge-evidence](2026-07-17-judge-evidence.md) | plan 09 (harvest-step) | 16 | XL / high | 1773106080 |
| [dispatcher-budget-reconciler](2026-07-17-dispatcher-budget-reconciler.md) | plan 11 (dispatch) | 13 | XL / medium | none |
| [workflow-source-format](2026-07-17-workflow-source-format.md) | 2026-07-17 spec (slice b only) | ~8 | L / medium | none |
| [platform-mcp-hitl](2026-07-17-platform-mcp-hitl.md) | plan 08 | 30 | XL / high | 1773106070/71/72 |
| [delivery-outcomes](2026-07-17-delivery-outcomes.md) | plan 12 | ~19 | XL / high | 1773106090 |

All five independently resolved to a **split** execution level — the natural unit of
dispatch is the *slice*, not the *track*.

## Hard cross-plan chokepoints (serialize; never run concurrently)

1. **Harvest results shape** *(blocker)* — judge-evidence Task 9 and delivery-outcomes B4
   both rewrite `agent/harvest/runner.go`'s results type and both create
   `atc/exec/harvest_step_test.go`. Canonical shape = `agent/schema.Results` (base_sha in
   the metadata map). **judge-evidence lands first; delivery-outcomes B4 adapts to map
   access and merges into the existing files.**
2. **Per-run principal mint** *(blocker)* — dispatcher-budget Task 8 and platform-mcp-hitl
   Task 26 co-edit `attachRunSecret`. Expiry = workflow-conditional
   `RunPrincipalTimeout` (6h ordinary / 72h park), **not** `max(RunTimeout, ParkTimeout+12h)`.
   **Task 8 lands the `agent-run-<id>` rename + `questions:answer` scope first; Task 26
   layers the park-aware expiry.**
3. **`render.go` refusal chain** *(major)* — three plans rewrite the same refusal switch
   and `RenderAgentStep` return literal: workflow-source (3b/5, source-format), judge-evidence
   (T11, judge), platform-mcp-hitl (T25, sidecar/hitl/spec_delivery). **Land order:
   workflow-source → judge-evidence → platform-mcp; each re-greps the whole chain at HEAD
   and preserves the others' edits — never patch cited line numbers.**
4. **Migration spine** *(major)* — one migration per push, ascending applied order, both C2
   dual-constants bumped together. Target 1773106070-72 → 1773106080 → 1773106090. Because
   platform-mcp's 70-72 sits behind its live park-pin gate, judge-evidence 1773106080 will
   likely land first and platform-mcp renumbers above head (ticket-core precedent).
   **Never push two of these migrations together. judge-evidence 80 must precede
   delivery-outcomes 90.**
5. **Elm bundle** *(minor)* — three plans regenerate `web/public/elm.js`: judge-evidence
   (build page), delivery-outcomes + platform-mcp (ticket page). Serialize; each regenerates
   the bundle as its final commit and rebases onto prior Elm work.
6. **`00-shared-contracts.md` §11** *(minor)* — all five append; treat as single-writer per
   merge window and append after the current tail, not a pinned line.

## Recommended program order

- **Phase 0 (native, cheap, first):** workflow-source Task 1 — the slice-a contract
  amendment incl. the §1.1 migration-registry row for 1773106060-69. Every migration plan
  reads a registry that today doesn't even record the landed 1773106066.
- **Phase 1 (collision-free foundation):** dispatcher-budget-reconciler in full. Touches
  none of the chokepoint surfaces (no `render.go`, no `harvest/runner.go`, no migration) and
  lands the `attachRunSecret` baseline platform-mcp Task 26 depends on.
- **Harvest spine:** judge-evidence harvest core (Slices C→D→F) → delivery-outcomes B4.
- **Then:** delivery-outcomes remainder, workflow-source slice b, platform-mcp-hitl (gated
  on its live park-pin, Task 3).
- **Elm** serialized across the three plans at the end of each.

## What can loop concurrently

Greenfield pure-Go packages with no cross-plan file overlap, subject only to the shared
Claude rate-limit window (dispatch → settle → dispatch, no fan-out): `agent/platformmcp` +
`agent/api/questions` (platform-mcp Slices B/C/D), `agent/api/outcomes` + `agent/gitcheck` +
`agent/outcomewatcher` (delivery-outcomes A/C domain code), judge-evidence's `agent/harvest`
judge engine (Slice C), dispatcher-budget's budget lib + reconciler. Everything touching
`render.go`, `agent/harvest/runner.go`, `atc/exec/harvest_step.go`, Elm, or a migration is a
native-gated chokepoint.

## Loop model-tier caveat (verified)

The agent model is selected at **workflow-definition granularity** — `Workflow.Defaults.Model`
or a per-step `Step.Model`, threaded `render.go:82-84` → `AGENT_MODEL` → runner `--model`.
There is **no model column on `agent_tickets`** and no `--model` on the dispatch call, so a
"loop-opus" vs "loop-sonnet" choice is set on the workflow the ticket dispatches through
(distinct dispatch workflows, or per-step Model), and pinned before a batch. Auth/quota is a
single shared OAuth credential regardless of model string. Per-ticket tiering needs a small
plumbing change (a model field on the ticket/run path + render pass-through).

## Orphaned / knowingly-dead work (no owner inside this five-plan program)

- **`reconcileAwaitingRuns`** (PARK-V2 awaiting_human reconciler) — ships with non-remainder
  plans 03/11-proper, not here; both plans' Scope-Out now say so.
- **`questionsCheckpointAdapter`** — the checkpoint-reconciler legs stay knowingly dead
  until platform-mcp builds the adapter *and* some plan lifts the `render.go:59-61`
  checkpoint refusal; no plan in this batch does, so **v0 is checkpoint-free by design.**

## Owner decisions surfaced by the plans

Judge-evidence: results.json shape (pinned), the pod→`agent_reviews` trust bypass (needs a
named security review), harvest_judge ledger attribution, migration number.
Dispatcher-budget: arming real budget caps live, budget-gating manual dispatch, the
spend-attribution shift when UserID resolves. Delivery-outcomes: the human-touch-delta
definition (freezes before scorecards consume it), the watcher's read-only git credential.
Platform-mcp: the auto-injection-always sidecar policy, the park-aware expiry reconciliation.
