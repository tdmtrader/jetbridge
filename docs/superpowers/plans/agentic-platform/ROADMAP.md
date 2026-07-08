# Agentic Platform — Program Roadmap

> The single entry point. Read this first, then `00-shared-contracts.md`, then the per-workstream plan you own.

## 1. Program overview

**North star.** A human (or, later, Jira) files a **ticket**. The platform dispatches it as a **pipeline run** of a versioned **workflow definition**, executing agent steps in cluster pods via jetbridge, attributed to and funded by the triggering user's vaulted credentials. The agent learns each repo through its **dev-mcp** sidecar, requests reviews from other agents through an **agent-gateway** sidecar, asks the human questions mid-flight when genuinely blocked, and finishes by leaving committed work in its workspace. A deterministic **harvest step** then independently re-verifies the gates, runs the judge, pushes the branch, and updates the ticket. The ticket page shows the diff, proof-carrying review evidence, live plan progress, cost, and score. A human merges — and the platform watches what happens next. Every dollar is budgeted and attributed; workflow versions compete on scorecards, not gates; findings, friction, and outcomes feed a process-intelligence loop whose explicit goal is to migrate catches *leftward* — out of LLM review and into deterministic tooling. (Full vision: `../specs/2026-07-07-agentic-platform-end-state-design.md`.)

**How to use this directory.**
- **`00-shared-contracts.md` is NORMATIVE** for every cross-workstream interface — SQL DDL, Go domain types, MCP tool schemas, API routes, the flight-recorder event schema, the workflow-definition YAML grammar, credential-injection contracts, and the review-checklist / amendment log. If code and 00 disagree, 00 wins (or 00 gets an amendment first).
- Each workstream has exactly one **`NN-<id>.md`** plan. Execute each as its own **forge track** driven by `superpowers:subagent-driven-development` (or `superpowers:executing-plans`).
- **Every plan's Task 1 writes that workstream's contract addenda into `00-shared-contracts.md` before any code lands.** Task 1 is the wave-start agreement wave-mates and downstream consumers read; it is append-only to §11's amendment log. Code follows the frozen contract, never the reverse.

## 2. Waves

Shape: **5 / 2 / 3 / 3 / 1**. The auth-switch contention is resolved structurally — `agent-identity` lands in wave 1 alongside four streams that never touch `api_auth_wrappa.go`, and the two streams that extend the auth switch (`ticket-core`, `agent-step`) move to wave 2 with a declared dependency on identity.

### Wave 1 — foundations (governance, general-CI runs, contracts)

| Workstream | Size | Plan | Tasks | Ships value |
|---|---|---|---|---|
| agent-identity | S | [01-agent-identity.md](01-agent-identity.md) | 8 | Live theborg review publisher authenticates with a scoped, revocable principal; every wave-2+ route is written against principals once. |
| credentials-and-budgets | L | [02-credentials-and-budgets.md](02-credentials-and-budgets.md) | 19 | Vaulted per-user tokens (encrypted, expiry-nagged), cost ledger fed by the existing review job, the single budget library, and the empirical rate-limit answer. |
| pipeline-runs | L | [03-pipeline-runs.md](03-pipeline-runs.md) | 22 | `fly run-pipeline` launches a numbered one-shot run of a `template:` pipeline with a worst-of aggregate status and retention — pure general-CI value. |
| dev-mcp | M | [04-dev-mcp.md](04-dev-mcp.md) | 14 | Frozen five-tool dev contract + contract kit + Go client + this repo's reference impl + the sidecar-image packaging convention every later sidecar reuses. |
| workflow-store | M | [05-workflow-store.md](05-workflow-store.md) | 12 | The five ci-agent phases import as a content-hashed `standard-dev` v1; edits produce diffable versions; a human marks one live via fly. |

*When this wave lands the team can:* run general-CI pipeline runs on theborg, see the existing review job's costs land in the ledger against vaulted tokens, hand interactive agents typed build/test tools, version workflow definitions as data, and use revocable agent principals.

### Wave 2 — the native agent (tracker + real `agent:` step)

| Workstream | Size | Plan | Tasks | Ships value |
|---|---|---|---|---|
| ticket-core | M | [06-ticket-core.md](06-ticket-core.md) | 14 | Tickets/specs/plan-tasks with a single-writer state machine, CRUD on principals, fly commands, and a minimal Elm ticket page — the team's agent-work tracker. |
| agent-step | L | [07-agent-step.md](07-agent-step.md) | 19 | First-class `agent:` step running claude in a jetbridge pod with sidecars, server-side metrics ingestion, a live sidecar-MCP wiring proof, and the live review job cut over onto it. |

*When this wave lands the team can:* file and track agent work as tickets, and run the live theborg review job on a restart-surviving native `agent:` step with queryable tokens/turns/cost per step.

### Wave 3 — sidecar surfaces + the closed loop (hand-dispatched)

| Workstream | Size | Plan | Tasks | Ships value |
|---|---|---|---|---|
| platform-mcp-hitl | L | [08-platform-mcp-hitl.md](08-platform-mcp-hitl.md) | 19 | The agent's mid-flight surface: `read_ticket`/`submit_spec`/`submit_plan`/`update_task_status`/`ask_human` with park/resume, checkpoint gates, and a notification the team actually sees. |
| harvest-step | L | [09-harvest-step.md](09-harvest-step.md) | 19 | Deterministic terminal step: re-run gates via dev-mcp, run the rubric judge, push `agent/ticket-N` with harvest-only credentials, walk the ticket with evidence. |
| gateway-mcp | M | [10-gateway-mcp.md](10-gateway-mcp.md) | 12 | `request_review`/`ask_agent` over a provider-adapter layer, universal metering into ledger + flight recorder, and a budget-slice cutoff that never silently truncates. |

*When this wave lands the team can:* run the full loop by hand — a hand-written `template:` pipeline (dispatched via `fly run-pipeline`) where an agent implements a ticket with `ask_human` available, harvest independently verifies + judges + pushes with isolated credentials, and the §5 credential posture is closed. (The only scaffolding retired next wave is one pipeline YAML.)

### Wave 4 — dispatch + honest outcomes

| Workstream | Size | Plan | Tasks | Ships value |
|---|---|---|---|---|
| dispatch | L | [11-dispatch.md](11-dispatch.md) | 15 | The renderer (definition version → golden-file-validated `template:` pipeline) + the dispatcher that claims queued tickets, admits budgets, attaches vaulted creds, and runs them. |
| delivery-outcomes | L | [12-delivery-outcomes.md](12-delivery-outcomes.md) | 16 | Ticket PR view (diff, evidence, judge score, plan progress, cost) + dispositions + the webhook-free outcome watcher recording merged / merged-with-fixes / human-touch delta. |
| scorecards | M | [13-scorecards.md](13-scorecards.md) | 10 | Side-by-side workflow-version scorecards (gate pass rate, cost, turns, findings, judge scores, verdicts) and a per-run "where did the turns go" panel — read-only over existing tables. |

*When this wave lands the team can:* make "queued" the whole human action — the dispatcher renders and runs the ticket automatically — see the PR view with automatic merge outcomes, and compare workflow versions on data.

### Wave 5 — self-improvement

| Workstream | Size | Plan | Tasks | Ships value |
|---|---|---|---|---|
| process-intel-experiments | L | [14-process-intel-experiments.md](14-process-intel-experiments.md) | 20 | Opt-in benchmark experiments across workflow versions (scorecard delta), finding/calibration/friction analytics, and a retrospective agent that files `origin:retrospective` improvement tickets. |

*When this wave lands the team can:* run a benchmark experiment across two workflow versions and read the scorecard delta, and let a retrospective run mine a month of findings/friction/outcomes and file concrete improvement tickets into the same human-merged queue — the platform improving itself on theborg.

## 3. Workstream index

1. **agent-identity** (wave 1) — *scoped, revocable per-agent principals replacing the static publish token.* Headline: rewrites the exact auth seam everyone else extends, a wave ahead of them. Key risks: must not break the live theborg review job mid-cutover (mandatory dual-accept window with a verified dual-running period); keep the scope taxonomy coarse (per-role principal + optional run claim) since it is defined before all consumers exist.

2. **credentials-and-budgets** (wave 1) — *vault, rate-limit probe, cost ledger, and the single owner of all budget arithmetic.* Headline: answers the shared-rate-limit question empirically before anything is designed around a guess. Key risks: if the probe shows shared windows, budget defaults and dispatch concurrency get redesigned (hence probe-first); the budget-slice library binds gateway (wave 3) and dispatch (wave 4) — get consumer review before freezing.

3. **pipeline-runs** (wave 1) — *run-once lifecycle over instanced pipelines, as pure general CI.* Headline: the most invasive core diff — suppressing reactive semantics per-instance without regressing live pipelines. Key risks: the topgun spec proving non-template pipelines are untouched is non-negotiable; completion detection vs aborts/retriggers/late downstream builds is the subtle open item.

4. **dev-mcp** (wave 1) — *frozen five-tool per-repo dev contract + client + reference impl + sidecar-image convention.* Headline: establishes the sidecar packaging every later sidecar reuses verbatim. Key risks: streaming semantics for long test runs is the hardest design point (a bad choice surfaces as harvest timeouts); the second fixture repo guards against a too-Concourse-shaped contract.

5. **workflow-store** (wave 1) — *versioned, content-hashed workflow definitions; grammar decided here, renderer excluded.* Headline: makes workflow iteration versioned data in week one. Key risks: the grammar is the highest-leverage schema decision — constrain v1 to what agent-step and the renderer consume; checkpoint/gate-policy slots ship declared-but-inert with wave-3 consumers reviewing shapes before freeze.

6. **ticket-core** (wave 2) — *tickets/specs/plan-tasks with a single-writer transition function.* Headline: the one mutation path every later writer (dispatch, platform-mcp, harvest, watcher) must go through. Key risks: the lifecycle enum must be complete up front (all §9 states) to avoid a follow-up migration; single-writer discipline is what prevents wave-4 races.

7. **agent-step** (wave 2) — *native `agent:` step with server-side ingestion + live sidecar-wiring proof + live-job cutover.* Headline: the widest file surface in the program (validator + planner + engine + exec), bounded by the fresh `run:`/`sidecar` recipe. Key risks: supervisor resume of a mid-conversation agent needs explicit semantics (live test required); sidecar startup ordering is the unproven wiring being retired here.

8. **platform-mcp-hitl** (wave 3) — *ask_human, checkpoints, park/resume, and a concrete notification channel.* Headline: owns checkpoint-gate execution on the ask_human park/resume primitive. Key risks: park/resume across web-node restarts is the hard case a fake clientset cannot exercise (live test mandatory); parked pods consume resources indefinitely — measure pause-pod cost while parked.

9. **harvest-step** (wave 3) — *independent re-verification, rubric judge, and credential-isolated push.* Headline: verify-state-not-transcripts made real; closes the §5 credential posture. Key risks: flaky gates make re-verification cry wolf — the fixture flaky case + retry stance are the platform's trust foundation; the rubric→six-verdict mapping is a three-party agreement to sign off before freezing.

10. **gateway-mcp** (wave 3) — *provider-agnostic subagents with metering and never-silent cutoff.* Headline: one contract, adapters behind it (claude CLI at GA). Key risks: bundling provider CLIs makes a fat churny image (pin versions, automate rebuilds); cutoff-mid-conversation semantics must match the never-silent-truncation rule and be tested.

11. **dispatch** (wave 4) — *renderer (golden-file validated) + dispatcher that makes "queued" sufficient.* Headline: the integration point of four frozen schemas; owns no new tables. Key risks: golden-file tests per definition version are the honesty mechanism; claim semantics under concurrent web nodes need SQL-level care; state races are mitigated only if everyone goes through the single transition function.

12. **delivery-outcomes** (wave 4) — *ticket PR view, dispositions, and a webhook-free outcome watcher.* Headline: accumulates the platform's most honest quality metric with no webhook and no form. Key risks: human-touch delta under rebases/squash merges is genuinely hard (v1 best-effort, documented assumptions); the delta definition must be fixed before scorecards consume it.

13. **scorecards** (wave 4) — *side-by-side version comparison + per-run turn/dollar attribution.* Headline: promotion decisions get data; writes no domain tables. Key risks: outcome columns land dark until the same-wave watcher fills them (nullable-by-design keeps neither blocking); small-team samples are noisy — present counts, resist overreading.

14. **process-intel-experiments** (wave 5) — *experiments, analytics, and the self-improving retrospective loop.* Headline: deliberately the widest terminal workstream, two internally-sequenced separately-shippable milestones. Key risks: experiment batches multiply spend (the daily cap is load-bearing); retrospective proposal quality is unproven — template-shaped proposals, manual trigger first, everything human-gated.

## 4. Contract surfaces

Each surface is owned by exactly one producer; its DDL/types/schema live in `00-shared-contracts.md` and are frozen (or amended) by the producer's **Task 1**.

| Surface | Producer → consumers | Where defined |
|---|---|---|
| agent-principal-auth | agent-identity → ticket-core, agent-step, platform-mcp, gateway, harvest, dispatch | 00 §1.2, §4.1–4.2; 01 Task 1 |
| user-credential-vault + secret-helper | credentials-and-budgets → dispatch, gateway | 00 §1.3, §8.2; 02 Task 1 |
| budget-library + cost-ledger | credentials-and-budgets → dispatch, gateway, agent-step, scorecards, delivery-outcomes, process-intel | 00 §1.4, §2.7; 02 Task 1 |
| platform-credential-policy | credentials-and-budgets → harvest, process-intel, gateway | 00 §1.13, §8.2/§8.3, decision 23; 02 Task 1 |
| pipeline-runs API + lifecycle | pipeline-runs → dispatch, platform-mcp, process-intel | 00 §1.5, §7; 03 Task 1 |
| dev-mcp contract + Go client | dev-mcp → harvest, agent-step, process-intel | 00 §3.1, decision 11; 04 Task 1 |
| sidecar-image packaging convention | dev-mcp → platform-mcp, gateway | 00 §8.5; 04 Task 1 |
| workflow-definition schema + hash | workflow-store → dispatch, harvest, platform-mcp, scorecards, process-intel | 00 §2.2, §6 (§6.1–6.2 DECIDED HERE); 05 Task 1 |
| ticket tables + transition function | ticket-core → platform-mcp, dispatch, harvest, delivery-outcomes, process-intel | 00 §1.7, §2.1; 06 Task 1 |
| agent-step config schema (plan union) | agent-step → dispatch, workflow-store, gateway | 00 §2.8; 07 Task 1 |
| events/results shared schema + run-metrics | agent-step → gateway, scorecards, process-intel, harvest | 00 §1.8, §2.4, §5, decision 2; 07 Task 1 |
| harvest terminal-step schema + gate-policy | harvest-step → dispatch, delivery-outcomes, scorecards, process-intel | 00 §2.8, §6.3, §1.10; 09 Task 1 |
| judge-rubric → verdict mapping | harvest-step → delivery-outcomes, scorecards, process-intel | 00 §6.4, decision 15; 09 Task 1 |
| platform-mcp tool contract | platform-mcp → dispatch, workflow-store, process-intel | 00 §3.2, §1.9, decisions 12/22; 08 Task 1 |
| gateway tool contract + metering | gateway-mcp → scorecards, process-intel | 00 §3.3, §5; 10 Task 1 |
| agent-outcomes schema | delivery-outcomes → scorecards, process-intel | 00 §1.11, §2.5, decision 18; 12 Task 1 |
| renderer library + golden templates | dispatch → process-intel | 00 §2.8 (render-time-resolution rule); 11 Task 1 (§2.8.2) |
| scorecard rollup API | scorecards → process-intel | owning plan (13) Task 1 |

## 5. Scaffolding decisions

**Accepted (permanent or cleanly-removable glue):**
- **End-of-wave-3 hand-written `template:` pipeline** with real `agent:` + `harvest:` steps, dispatched manually via `fly run-pipeline -v ticket_id=N`. Closes the full loop one wave before the dispatcher using **zero throwaway code** — the only artifact retired is one pipeline YAML, replaced by dispatch's renderer in wave 4.
- **Dual-accept window for the static publish token** during agent-identity's cutover, so the live theborg review job never breaks; the static token is deleted at window end. All later routes are written against principals from day one.
- **agent-step (wave 2) consumes its Anthropic token from an ordinary var-source/K8s secret** until dispatch attaches vaulted per-user credentials in wave 4 — pipeline-config-level only, no code change when the vaulted path takes over.
- **workflow-store's checkpoint + gate-policy grammar slots ship declared-but-inert** in wave 1, with wave-3 consumers reviewing the slot shapes before the grammar freezes — avoiding both over-design and a later breaking schema change.
- **scorecards' outcome-derived columns are nullable LEFT JOINs** designed at wave-4 start with delivery-outcomes; they render dark until the same-wave watcher fills them. Neither workstream blocks the other.
- **process-intel-experiments is one wide terminal workstream** (respecting the 14-track cap) with two internally-sequenced, separately-shippable milestones; split into two forge tracks at execution time if needed.

**Rejected:**
- **The glue-dispatch workstream** (shell agent task + shell harvest + static-token platform writes): orphans the renderer, breaks down on parking semantics, keeps agent and push credentials co-resident for ~3 waves, and forces the flight-recorder ingestion path to be written twice. Early value is achieved with permanent pieces instead (the hand-written pipeline above).
- **The stub-harvest renderer variant:** because the renderer lives in dispatch (wave 4) and harvest's terminal-step schema freezes in wave 3, the renderer targets a real frozen schema — no stub is ever built. The renderer↔harvest knot is dissolved by *sequencing*, not a placeholder.
- **Double ingestion** (artifact-triggered flight recorder followed by native ingest): server-side ingestion is built once, inside agent-step, in wave 2 — meeting spec §5's "ingest server-side, not just as artifacts" from the first native run.

## 6. Decisions needing human review

These are open calls flagged in `00-shared-contracts.md §10` and cross-cutting hazards surfaced in the plans' Execution notes. Each is a crisp yes/no a human can rule on before or during the relevant wave.

- **Migration merge-order hazard (cross-cutting, all waves with migrations).** The migrator is version-pointer based: if a higher-numbered migration block deploys before a lower-numbered one, the lower one is *never applied*. Blocks are pre-allocated per workstream (`1773106010–…109`, decision 3). **Rule to confirm:** wave branches must merge (and theborg must deploy) in ascending migration-number order — or hold all deploys until every merged branch's migrations are present. Flagged explicitly in 02, 05, 13, 14 Execution notes.
- **ci-agent module-boundary re-implementations (agent-step, gateway, dev-mcp).** ci-agent has *no* Go dependency on the main module today (decision 2 corrected the false claim it did). Consequence: `agent/schema` becomes its own nested stdlib-only module consumed via `require`+`replace` from both sides, and gateway re-implements claude-CLI cost parsing locally rather than importing ci-agent's `llm` package. **Confirm:** the nested-module seam and the deliberate local re-implementation are acceptable vs. a larger refactor that makes ci-agent importable.
- **Squash-merge human-touch-delta limitation (delivery-outcomes).** Human-touch delta = numstat of non-`concourse-agent[bot]` commits between pushed sha and merge on a first-parent walk (decision 18); rebases/squash merges fall back to a patch-id heuristic with documented v1 limits. **Confirm:** the delta *definition* (lines vs hunks, squash behavior) is acceptable *before scorecards consume it* — it cannot change afterward without invalidating history.
- **Team-less agent-route authorization change (agent-identity, decision 21).** Today's team-less `/api/v1/agent/*` feedback routes are silently admin-only and their `DefaultRoles` entries are dead. The new `CheckAgentAuthorizationHandler` hardcodes team `main`, which *loosens* access to main-team viewer/member. **Confirm:** loosening from admin-only to main-team is the intended posture.
- **Judge funding and cap (credentials-and-budgets + harvest, decision 8, §1.13, §8.2/§8.3, decision 23).** Platform-initiated LLM work (harvest judge, retrospective agent) is funded by a dedicated `agent-platform` service-user credential delivered via a long-lived `agent-platform-credential` K8s secret; judge spend is capped separately, *outside* the ticket budget. **Confirm:** the separate platform-credential + separate cap model, and the syncer that maintains that secret.
- **Grammar-frozen-here decisions (workflow-store, §6.1–6.2).** Workflow grammar v1 is a linear sequence of `agent`/`checkpoint` steps, harvest appended implicitly, no branching/loops (decision 13); prompts are inline in the YAML so the content hash covers them (decision 14). **Confirm:** linear-only v1 and inline-prompt hashing are the right constraints before wave-3/4 consumers build on them.
- **Cutoff overshoot-by-one-call (gateway).** The claude CLI reports cost only at call end, so v1 cutoff is pre-call admission plus post-call accounting — a call that pushes cumulative over the ceiling is *allowed to finish* (metered) and the next is refused, overshooting the slice by at most one call. **Confirm:** never-silent-truncation-with-bounded-overshoot is preferred over mid-call termination.
- **Live-review-job cutover parity (agent-step).** The permanent `agent:` step ports the ci-agent prompt; exact parity is traded for the permanent step, verified by a dual-running window. **Confirm:** score divergence beyond ±2 blocks retirement of the shell job (the plan's stated gate) is the acceptable acceptance bar.
- **Notifications v1 = single generic webhook flag (platform-mcp, decision 19).** ask_human/checkpoint notifications are a ticket-page banner plus one polling-backed generic webhook. **Confirm:** one channel + banner is sufficient for v1 (deliberately chosen against the fork's lossy-NOTIFY lesson).

## 7. Execution protocol

**Wave discipline.** A wave starts only once every predecessor wave's **contract surfaces and Task-1 addenda are committed** to `00-shared-contracts.md`. Within a wave, workstreams run in parallel forge tracks; the shared-file appends they all make (`jetbridgeHeadMigration` in `atc/db/migration/legacy_upgrade_test.go`, the `atc/wrappa` auth switch, `atc/api/handler.go`'s `NewHandler` signature, `atc/routes.go`, `accessor/roles.go`) are **append-only / union-on-conflict** — a merge conflict is a mechanical re-add, and on migration-const conflict the **highest** number wins.

**Per-merge test-tier expectation (from `CLAUDE.md`).** Run **`make test-quick`** (unit + ci-agent, ~5 min, PostgreSQL required) green before any merge. Run each plan's fuller in-order suite from its **Execution notes** for the packages it touches (e.g. `ginkgo ./atc/db/`, `ginkgo ./atc/wrappa/`, `make test-fly-integration`, `elm-test`). Never pass `--race` (parallel-compilation failures). Run the **live theborg tests where a plan requires them** — always against a THROWAWAY namespace (never `cicd`/`concourse`), except agent-identity's Task 8 cutover which is deliberately against the live `cicd` publisher and is gated on human sign-off. Live tests are mandatory (not optional) for: agent-identity cutover (01), agent-step sidecar-wiring + cutover (07), platform-mcp restart-while-parked (08), harvest credential-isolation (09), gateway sidecar-wiring + cutoff (10), and dispatch K8s secret behavior (11) — the fake clientset cannot exercise localhost sidecar transport or park/resume across restarts.

**Re-verifying a plan after edits.** These plans were produced and checked by grounding + quality verifier prompts. After editing any `NN-<id>.md`, **re-run its grounding + quality verifier prompts** before treating it as executable again — verify every file path/line reference still resolves, every contract citation still matches `00-shared-contracts.md`, and the task graph's dependencies still hold. If a contract decision must change after its wave froze, **append a superseding entry to §11's amendment log** rather than editing the frozen text in place, and re-verify every downstream consumer plan against the amendment.
