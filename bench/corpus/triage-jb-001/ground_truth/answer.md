# Answer key — triage-jb-001

The recorded routing is the scoping document committed at
`05ef24ec6972e3c28a7711122e60dae0a4ba24cc` as
`docs/superpowers/plans/agentic-platform/2026-07-19-ux4-scoping.md`
(verbatim in `reference.diff`). What follows maps that document's own item IDs
onto the neutral finding IDs the agent under test was given, states the recorded
class for each, and records what happened downstream when the routing was
executed (`outcome-log.diff`, commit `644184e3f011369f3da77dc82caee200bd8fd196`).

**Do not expose any of this, including the ID crosswalk — the document's own IDs
encode their class (`A0-*` = ops, `L-*` = loop, `W-*` = wave, `S-*` = plan).**

---

## 1. The routing table

| Finding | Recorded class | Doc item | Notes |
|---|---|---|---|
| F-01 chronic "flight recorder output missing" | **ops** (primary) + **loop** (secondary) | A0-1 + L-1 | Root cause is deployment skew, not code. Code-side follow-up is F-02's item. |
| F-02 three contradictory truths for one execution | **loop** | L-1 | |
| F-03 event stream not retained / not servable | **loop** | L-3 | |
| F-04 no turn-by-turn transcript view | **plan** | S-2 | Depends on F-03's item and on F-01's ops action. |
| F-05 dispatched ticket is one grey box | **plan** | S-1 | The audit's centre of gravity. |
| F-06 phantom "no instance vars" card | **wave** | W-13 | With an investigate-first half (can dispatch avoid creating the uninstanced parent). |
| F-07 run rows identified by build number | **wave** | W-2 | |
| F-08 "Run error" box does not link to the build | **wave** | W-3 | |
| F-09 `/agent` runs table rows are not links | **wave** | W-4 | |
| F-10 naming soup (six names for one ticket) | **plan** | S-8 | Wire shape is consumed by `ci-agent` too; needs its own rename plan. |
| F-11 inconsistent status vocabulary | **wave** | W-14 | |
| F-12 harvest header renders `step: step` | **wave** | W-1 | |
| F-13 `/agent` mega-page, no sidebar | **plan** | S-3 | |
| F-14 reviews index unlinked, `/reviews` redirects home | **wave** | W-11 | |
| F-15 reviews rows collide (no instance identity) | **wave** | W-12 | |
| F-16 web cannot create/queue/dispatch tickets | **plan** | S-5 | |
| F-17 review means leaving for GitHub compare | **plan** | S-4 | Recorded as the *first* structural item to execute — the endpoint already exists. |
| F-18 no workflow detail page, no lifecycle verbs | **plan** | S-6 | |
| F-19 ticket title wrap gap | **wave** | W-6 | |
| F-20 copy defects (`budget$0.08`, "completed", "(no result)") | **wave** | W-7 | |
| F-21 prose rendering (fence tag, raw `**`) | **wave** | W-5 | |
| F-22 queue ordering + no spend rollups | **wave** | W-8 | |
| F-23 mixed timestamp formats | **wave** | W-9 | |
| F-24 strip chips truncate at the wrong end | **wave** | W-10 | |
| F-25 costs section describes a control that does not exist | **wave** | W-15 | Honest copy now; the control itself is F-26. |
| F-26 no spend view; cap not changeable at runtime | **plan** + **decision** | S-7 | The dashboard half is a plan; the runtime-mutable cap is explicitly flagged as needing an owner call before anything is built. |
| F-27 dead `agent-ticket-<id>` pipelines accumulate | **loop** | L-2 | |
| F-28 principals list mixes run and operator principals | **loop** | L-4 | |
| F-29 `#23`/`#24` errored on refusals, scope landed via `#25` | **ops** | A0-2 | Abandon both. |
| F-30 `#2` queued with no workflow; `#12` does the same work | **ops** | A0-2 | Disposition `#12` first, then conclude `#2` as superseded (never dispatched ⇒ conclude, not abandon). |
| F-31 `#1` spent smoke draft | **ops** | A0-2 | Abandon. |
| F-32 `#12` at needs_review for 1d 21h | **ops** | A0-2 | Disposition it. |
| F-33 review latency not surfaced anywhere | **wave** | WF-4 | Pure presentation; the data is already on the wire. |
| F-34 `badpolicy` workflow undescribed, undeletable | **ops** | A0-2 | Give it a description via a workflow re-import; the missing delete/hide verb is F-18's item, not a separate one. |

**Class distribution of the recorded routing:** wave 16, plan 8 (one of which
carries a `decision`), ops 6, loop 4, decision 1 (inside F-26). The skew is the
point — see the rubric.

## 2. Emergent items the triage produced (not in the finding list)

These are not gradable routings; they are outputs a good triage produces. The
document filed them as `WF-1`, `WF-2`, `WF-3`:

- **Make the image skew visible** (`loop` for the visible half; `decision` for
  the auto-bump half). The `/agent` credentials view and `fly agent runs` should
  warn when the running step-image tag is not from the web's version family. The
  optional second half — the release chain auto-bumping the GitOps repo after the
  image build — is recorded as a `decision` because of its blast radius.
- **Make the loop Elm-capable** (`loop`, then an ops rebuild). Add a Node + Elm
  toolchain to `deploy/agent-runner/Dockerfile` and an `elm-build` gate to the
  harvest gate vocabulary, where the gate must also verify the committed bundle
  was regenerated when `web/elm/**` changed. Until this lands, every `wave` item
  above is `wave` *because* of the missing capability, not because of its nature.
- **Pre-dispatch spec lint** (`loop`). A warn-not-block admission pass over ticket
  prose against a known-refusal vocabulary list seeded from F-29's two refusals.

## 3. The evidence that makes the routing derivable

Every one of these is checkable at the pre-state commit
`6188b2a8c1e3b954434a82ae8c90423cb469c199`.

**Why the Elm findings are `wave` and not `loop`** — this is the case's central
discrimination, and it is not a matter of taste:

- `deploy/agent-runner/Dockerfile` builds `agent-runner` and `harvest-runner` on
  `node:20-bookworm-slim` with the Claude CLI, `git`, `ca-certificates`, `curl`
  and the Go toolchain. There is **no Elm compiler** in the image, so an agent in
  a pod cannot compile Elm at all.
- `agent/harvest/gates.go` defines the entire gate vocabulary as exactly three
  commands: `build` → `go build ./...`, `test` → `go test ./...`,
  `lint` → `go vet ./...`. All Go. **No gate can observe Elm breakage.**
- The committed bundle `web/public/elm.min.js` is what the deployed web serves.
  An agent that edited `web/elm/src/**` without regenerating it would pass every
  gate and ship a change that has no effect — the repo's documented
  "stale embedded bundle" failure mode.
- The rule is already written down in the repo at pre-state, so it is
  discoverable and not merely inferable:
  `docs/superpowers/plans/agentic-platform/remainders/2026-07-17-delivery-outcomes.md:105`
  ("no Elm toolchain in gates — do NOT dispatch this slice to the loop") and
  `:956`; `remainders/2026-07-17-platform-mcp-hitl.md:77`; and the dogfoodability
  note in `plans/agentic-platform/15-platform-home.md:28`.

**Why F-01 is `ops` and cannot be fixed by code:**

- The deployed step image is `registry.home/agent-runner:v0.2.167` (given in the
  task's environment block). `deploy/concourse-pipeline.yml:502-510` shows the
  image build job tags `registry.home/agent-runner:v${NEXT_VERSION}`, so the image
  tag *is* a repo version.
- Tag `v0.2.167` resolves to commit `5715e7db2d` (2026-07-18T09:07).
  `agent/harvest/flight.go` — the flight recorder itself — was added at
  `8457f107c9` (2026-07-18T15:00 PDT), *after* that tag.
  `git tag --contains 8457f107c9` does not list `v0.2.167`. The deployed runner
  therefore cannot write flight output; every harvest ingests an empty volume and
  `atc/exec/harvest_step.go` degrades the metrics row to
  `status=error` / "flight recorder output missing".
- The image reference lives in the GitOps repository reconciled by ArgoCD (stated
  in the task), so the fix is a build + a GitOps bump: an operator action, not a
  code change in this repository.
- Consequently F-01's ops action is a **hard prerequisite** for F-03 (there is no
  event data to persist until the runner can write it) and for F-04.

**Why the four `loop` items are the four `loop` items** — the recorded rationale:
all four are pure Go, none of them touches `agent/dispatch/render.go`, and none of
them was believed to need a migration.

**Coordination constraints the routing had to respect** (all present at pre-state
in `docs/superpowers/plans/agentic-platform/remainders/README.md`):

1. **Do not touch `agent/dispatch/render.go`'s refusal chain** — README chokepoint
   3 names it as contended across three in-flight tracks.
2. **Claim migration numbers in the registry before adding one** — README
   chokepoint 4; one migration per push, ascending applied order.
3. **Dispatch timing: push → let the release chain settle → dispatch.** Never
   dispatch while a self-upgrade is mid-flight; the web restart errors the run and
   double-spends. (README "What can loop concurrently"; the repo's dispatch
   discipline in `remainders/TICKETS.md`.)
4. **Ticket bodies must be self-contained** — the agent gets the ticket file and a
   checkout, nothing else.
5. **Elm bundle rule** — regenerate and commit `web/public/elm.min.js` in the same
   change, or the deployed web serves a stale bundle.

## 4. The recorded execution order

1. F-01's ops action (refresh the runner image) — everything downstream of the
   flight recorder is blocked on it and it stops the noise at the source.
2. F-29 – F-32, F-34 queue hygiene (ops, same sitting).
3. File the four `loop` tickets as drafts now; dispatch them one at a time after
   the next push has settled.
4. The Elm `wave` session on one branch, ending with a bundle rebuild.
5. F-17's plan (in-app diff) pulled forward as the first structural item — the
   endpoint already exists, so it is the cheapest high-trust win.
6. F-05's plan doc (step DAG) next, then F-04's (transcript viewer).
7. The emergent loop improvements ride along whenever a dispatch slot is free.

## 5. What actually happened when the routing was executed

Recorded the same day in `outcome-log.diff`. This is the outcome signal — the
routing can be scored against results, not only against its author's intent.

- **F-01 ops action: correct and validated.** The image job built
  `registry.home/agent-runner:v0.2.196` from the audited web commit; the GitOps
  bump `v0.2.167 → v0.2.196` synced in ~40s. Proof the diagnosis was right: the
  very next run's harvest recorded `status=ok · "1 gate(s) ok; pushed
  agent/ticket-41"` — a real summary, where every previous run had recorded
  "flight recorder output missing". The chronic noise stopped at the source.
- **F-29/F-31 ops actions: executed.** F-30/F-32 correctly left to the owner
  because `#12` is a code-merge decision, not a hygiene action.
- **F-27, and the two emergent loop improvements: dispatched clean** on the fresh
  image and later merged. These `loop` routings held.
- **F-02's loop ticket: the routing's one substantive miss.** It was dispatched and
  the agent *correctly stopped at its own verification gate*: a CHECK constraint
  `status IN ('ok','failed','error')` exists at migration `1773106060` (amended at
  `1773106061`), so adding an `incomplete` status **requires a migration** — which
  the ticket body had forbidden, on the belief that no constraint existed. The
  ticket was held for a respec. Two things this proves: the "these four need no
  migration" rationale was asserted rather than verified for this item, and the
  instruction to *verify constraint-freedom as task zero* is what stopped it from
  becoming a bad push. (A submission that routes F-02 to `loop` is still correct —
  it later landed as a loop-sized change once the migration was allowed for.)

  **The constraint is visible at pre-state**:
  `atc/db/migration/migrations/1773106060_create_agent_run_metrics.up.sql:11`
  declares `status TEXT NOT NULL CHECK (status IN ('ok','failed','error'))`, and
  `1773106061_agent_run_metrics_parked.up.sql:5-7` re-states it with `'parked'`
  added. A submission that checks this and says F-02's item **does** need a
  migration — and therefore has to claim a slot in the registry before it can be
  dispatched — is *more correct than the recorded routing* and should be scored
  above it, not penalised for disagreeing.
- **F-03's loop ticket held** before dispatch for the same migration risk, on the
  strength of what F-02 had just shown. F-28's held only to keep the review batch
  small.
- **A finding the triage missed entirely (`WF-5`):** a ticket created with an empty
  workflow is accepted at create but rejected at dispatch, and no CLI verb can set
  the workflow afterwards, so it strands un-dispatchable. F-30 in the finding list
  is exactly this ticket (`#2`, "Queued with no workflow"), and the triage routed
  it as hygiene without asking *how* a ticket reaches that state. Credit a
  submission that notices the gap behind F-30.
