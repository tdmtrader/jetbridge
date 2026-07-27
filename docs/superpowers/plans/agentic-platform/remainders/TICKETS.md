# Remainder tracks → jetbridge tickets

> **SUPERSEDED — historical implementation record (not executable).** The approved [Agentic Workflows as Functions over Snapshots design](../../../specs/2026-07-21-agentic-workflows-as-functions-design.md) and [Agentic Functions program plan](../../2026-07-21-agentic-functions-program.md) are authoritative. This mapping of remainder-plan slices onto jetbridge tickets is historical; the ticket system itself is now a compatibility adapter, not the orchestration center.

**Date:** 2026-07-18 · maps the five [remainder plans](README.md) onto the ticket system.

Each plan **slice** is one ticket. A slice was sized to the proven loop envelope
(≈ ticket #14: 8 files, +682/−41), so it is also the right size for a single
dispatched run. Two kinds:

- **Loop ticket** — dispatchable through jetbridge (`--workflow develop`). The agent
  implements in a pod, harvest pushes `agent/ticket-N`, you merge. Body points at the
  plan slice, which is in the repo checkout, so the agent executes it verbatim.
- **Native ticket** — filed for tracking only (create + `queue`, never `dispatch`).
  You do the work at your machine; the ticket is the queue/dashboard record and gets
  closed by hand. Everything that touches a migration, Elm, postgres specs, the live
  cluster, or a cross-plan security/refusal boundary is native (see plan assessments).

## What is the ticket vs. what the run produces

A ticket (the `agent_tickets` row) is authored at `create` from: **title, body, repo,
target_branch, workflow (name/version), budget, origin, external_ref**. The rest of the
row — state, user, `workflow_definition_id` (frozen at dispatch), `pipeline_run_id` (set
at dispatch), `branch` (set by harvest at push), attempt_count, timestamps — is filled by
the lifecycle.

The **spec** (`agent_ticket_specs`) and the **plan / tasks** (`agent_ticket_tasks`) are
NOT part of the ticket at creation. They are child rows the *run* produces, written by the
agent via `submit_spec` / `submit_plan` (platform-mcp, wave 3) and versioned. Whether they
exist at all is a property of the **workflow**:

- **direct-dev** — "the body IS the spec; one implement step, no submit_spec/submit_plan,
  no checkpoint." No spec/plan rows are produced. (This is effectively what tickets #12/#14
  used.)
- **standard-dev** — a plan agent *generates* the spec and plan during the run and a human
  approves them at a checkpoint.
- **test-first-dev** — failing tests are the contract, mirrored into the spec.

**Consequence for these remainder plans:** the plans are pre-written specs+plans, so the
fit is **direct-dev** — the slice text goes in the **body** (the whole contract) and the
agent implements it. No spec/plan rows are produced; the ticket page's spec/task panels
stay empty by design, and the "plan" lives in the body prose + the repo file. Using
standard-dev instead would make the agent re-derive its own plan from a short body
(spending budget to reproduce work already done, possibly diverging). Populating the
structured task rows from a pre-authored plan needs `submit_plan` — today only an in-run
agent (platform-mcp, unbuilt) or a raw member-authed HTTP POST can call it — which is the
same "no ticket import" gap noted at the bottom of this file.

The body reaches the agent by prompt-template interpolation (`{{.Ticket.Body}}`,
`render.go:37`), not by `platform-mcp read_ticket` — which is why dispatch works today
without the wave-3 sidecar. `spec_delivery: mcp` is refused in v0; direct-dev needs no spec
delivery at all.

## How the ticket fields map

| Ticket field | Value |
|---|---|
| `--title` | slice name (e.g. "dispatcher: budget lib + admission") |
| `--body` | "Implement Slice A (Tasks 1–2) of `docs/superpowers/plans/agentic-platform/remainders/<plan>.md`, exactly as written — TDD, commit per step. The plan is in the repo checkout at that path." |
| `--repo` | `tdmtrader/concourse` |
| `--target-branch` | **`jetbridge`** (NOT the default `main` — the git resource tracks this) |
| `--workflow` | `develop` for loop code tickets; omit for native tracking tickets |
| `--budget` | round-trips but does **not enforce** until dispatcher-budget Slice A lands |

**Model tier:** the loop model is set on the workflow, not the ticket (there is no
per-ticket model field — see README). To run a tier, pin `Defaults.Model` on the
`develop` workflow (or keep `develop-opus` / `develop-fable` variants) before
dispatching that ticket.

**Dispatch discipline:** loop tickets that share a file must be dispatched **one at a
time**, each merged and the release settled before the next (push → settle → dispatch;
a self-upgrade restarts web and double-spends). Loop and native both draw the same
shared Claude window.

---

## Phase 0 — docs debt (native, first)

| Ticket | Plan / tasks | Kind |
|---|---|---|
| workflow-source: slice-a contract amendment + §1.1 registry | workflow-source T1 | native-opus |

## Phase 1 — dispatcher-budget (collision-free foundation)

| Ticket | Plan / tasks | Kind | Depends on |
|---|---|---|---|
| dispatcher: budget lib + admission | dispatcher T1–2 | **loop-opus** | — |
| dispatcher: polling loop | dispatcher T4 | **loop-opus** | budget lib |
| dispatcher: run-completion reconciler | dispatcher T6 | **loop-fable** | polling loop |
| dispatcher: secret labeler | dispatcher T9 | **loop-sonnet** | — |
| dispatcher: checker swaps (arms caps live) | dispatcher T3 | native-sonnet | budget lib |
| dispatcher: GetRunByID | dispatcher T5 | native-opus | — |
| dispatcher: UserID resolution | dispatcher T7 | native-opus | — |
| dispatcher: per-run principal / creds | dispatcher T8 | native-fable | — |
| dispatcher: component wiring | dispatcher T10 | native-fable | GetRunByID, UserID |
| dispatcher: DB specs + docs | dispatcher T11–12 | native-sonnet | — |
| dispatcher: theborg live proof | dispatcher T13 | native-fable | all above |

## Phase 2 — judge-evidence harvest core

| Ticket | Plan / tasks | Kind | Depends on |
|---|---|---|---|
| judge: engine | judge-evidence T5–7 | **loop-opus** | — |
| judge: flight recorder (owns results shape) | judge-evidence T8–9 | **loop-fable** | engine |
| judge: exec ingestion | judge-evidence T12 | **loop-opus** ⚠ | native admission |
| judge: contract addendum | judge-evidence T1 | native-opus | — |
| judge: migration 1773106080 + reviews + factories | judge-evidence T2–4 | native-opus | — |
| judge: exec + render admission | judge-evidence T10–11 | native-fable | migration, engine, flight recorder |
| judge: Elm build page + public plan | judge-evidence T13 | native-fable | — |
| judge: live judged-harvest proof | judge-evidence T14–16 | native-fable | all above |

⚠ exec ingestion is loop-able only with a mandatory human read of the ledger-idempotency
and pod→`agent_reviews` trust legs at review.

## Phase 3 — delivery-outcomes (B4 depends on Phase 2)

| Ticket | Plan / tasks | Kind | Depends on |
|---|---|---|---|
| outcomes: disposition handler + routes + fly | delivery-outcomes B1–3 | **loop-opus** | — |
| outcomes: gitcheck + merge detector | delivery-outcomes C1–2 | **loop-opus** † | — |
| outcomes: watcher + diff route + wiring | delivery-outcomes C3–5 | **loop-opus** † | gitcheck |
| outcomes: contract freeze (human-touch delta) | delivery-outcomes A1 | native-fable | — |
| outcomes: migration 1773106090 + domain + factory | delivery-outcomes A2–4 | native-opus | judge migration 80 first |
| outcomes: harvest sha seeding | delivery-outcomes B4 | native-opus | judge flight recorder |
| outcomes: Elm PR-view remainder | delivery-outcomes D1–4 | native-opus | — |
| outcomes: live watcher smoke | delivery-outcomes E1–2 | native-fable | all above |

† gated on confirming the `git` binary is in the agent-runner image (`kubectl exec`); else run native.

## Phase 4 — workflow-source slice b — **LANDED 2026-07-18** (no tickets needed)

Completed cross-session (commits `0fb031cddb..5a43b5189d`): schema + exec env transport,
runner materialization (skills/context/system-prompt), renderer resolution + refusal
removal, example deploy pipeline + §8.1 contract rows, and the live skills-smoke proof
(ticket #15, v0.2.167). The render.go spine constraint drops to two editors
(judge-evidence T11 → platform-mcp T25).

## Phase 5 — platform-mcp-hitl (gated on the park-pin)

| Ticket | Plan / tasks | Kind | Depends on |
|---|---|---|---|
| **hitl: real-CLI park pin (ENTRY GATE)** | platform-mcp T3 | native-fable | SSE upgrade |
| hitl: SSE heartbeat upgrade | platform-mcp T2 | **loop-opus** | — |
| hitl: sidecar core + ask_human | platform-mcp T11,13–15 | **loop-opus** | park pin |
| hitl: sidecar mechanical | platform-mcp T12,16–17 | **loop-sonnet** | sidecar core |
| hitl: checkpoint endpoint + kit | platform-mcp T18,20–21 | **loop-opus** | — |
| hitl: webhook library | platform-mcp T22 | **loop-sonnet** | — |
| hitl: contract addendum | platform-mcp T1 | native-fable | — |
| hitl: questions data plane + routes (mig 70–72) | platform-mcp T4–10 | native-opus | — |
| hitl: ListByRun store | platform-mcp T19 | native-opus | — |
| hitl: notifier component | platform-mcp T23 | native-opus | — |
| hitl: image build + smoke | platform-mcp T24 | native-fable | — |
| hitl: refusal-lift + expiry | platform-mcp T25–26 | native-fable | dispatcher T8 |
| hitl: Elm question banner | platform-mcp T27–28 | native-opus | — |
| hitl: restart-while-parked proof | platform-mcp T29–30 | native-fable | all above |

---

## Ready-to-run: Phase 1 loop tickets

Create as drafts first (review the queue), then `dispatch --id N` one at a time. These do
not enforce budget yet and touch none of the cross-plan chokepoints.

```bash
T=home   # fly target
REPO=tdmtrader/concourse
BR=jetbridge
PLAN=docs/superpowers/plans/agentic-platform/remainders/2026-07-17-dispatcher-budget-reconciler.md

fly -t $T agent tickets create --repo $REPO --target-branch $BR --workflow develop \
  --title "dispatcher: budget lib + admission (Slice A, T1-2)" \
  --body "Implement Slice A (Tasks 1-2) of $PLAN, exactly as written — TDD, commit each step. The plan is in the repo checkout at that path. Do not touch files outside agent/dispatch and its tests."

# after it merges and the release settles, file + dispatch the next:
fly -t $T agent tickets create --repo $REPO --target-branch $BR --workflow develop \
  --title "dispatcher: polling loop (Slice B, T4)" \
  --body "Implement Task 4 of $PLAN, exactly as written — TDD, commit each step. The plan is in the repo checkout at that path."
```

Native tracking ticket (filed, not dispatched — you implement by hand, then `close`):

```bash
fly -t $T agent tickets create --repo $REPO --target-branch $BR \
  --title "dispatcher: component wiring (T10, native)" \
  --body "Native task — see Task 10 of $PLAN. command.go flag + component registration + K8s-gated wiring; verify teamFactory in scope. Local build + make test-unit before closing." \
  --queue
```

## Gap: there is no native "import a ticket from a file"

`fly agent tickets create` takes an inline `--body` only (no `--body-file`); the
structured spec + plan-tasks cannot be set from fly at all (the `POST .../spec` and
`.../plan` routes exist and are member-authorized, but go-concourse exposes no client
method, so only a raw authenticated HTTP call or an in-run agent via platform-mcp can
populate them). The one real file/dir import path is `fly agent workflows import PATH` —
for workflow **definitions**, not tickets.

**Recommendation:** add a small `fly agent tickets import <file.yml|dir>` that mirrors
the workflow importer — a manifest carrying title/body/repo/target-branch/workflow/
budget and optional spec + plan-tasks, POSTing create then the (already member-authorized)
spec/plan routes. It is a self-contained ~1-ticket addition and would make this very
manifest executable as data. Natural home: a new slice on the dispatcher-budget plan, or
its own tiny plan. Until then, the shell `create` calls above are the mechanism.
