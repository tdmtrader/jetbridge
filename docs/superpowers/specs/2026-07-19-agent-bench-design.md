# The Bench — Step-Level Evaluation and the Inner Improvement Loop

- **Date:** 2026-07-19
- **Status:** Approved design (brainstormed + section-approved 2026-07-19); ready for plan decomposition
- **Scope:** Reshapes the unbuilt improvement-loop workstreams (13-scorecards, 14-process-intel-experiments) around step-level evaluation. 10-gateway-mcp executes **unchanged** on its original charter (see §10). Nothing in landed waves 1–4 changes.
- **Companions:** `2026-07-07-agentic-platform-end-state-design.md` (the outer loop — still normative), `../plans/agentic-platform/ROADMAP.md §0` (landed-state reconciliation), `../plans/agentic-platform/remainders/SUPERVISION.md` (the empirical record this design mechanizes).

## Problem

The original wave-5 improvement loop measures workflows **only end-to-end, on production traffic**. At this team's volume that fails twice:

1. **Sample starvation.** Scorecards accrue one row per ticket (~40 tickets since 2026-07-17), while workflow versions churn in days (develop-fable v1→v4 in two days). Production A/B between versions never reaches signal before the versions are obsolete.
2. **Confounded credit assignment.** Production tickets vary in difficulty, so version deltas mix "workflow improved" with "tickets got easier," and a whole-pipeline delta cannot localize which *step* improved. SUPERVISION.md is the exhibit: the Sonnet-vs-Fable comparison was six heterogeneous tickets with hand-written fidelity verdicts — a human doing credit assignment the platform can't.

Plan 14's experiment substrate inherits both: its matrix cell is `(benchmark_case × workflow_version × repetition)` = one **full ticket** through the whole loop. Nothing in the program can run *one step* against *one fixed input* twice. Three real incidents this month would have been answered by exactly that: ticket #21's turn-cap wall ($6.27 of production trial-and-error to learn 80 < needed < 140), the #23/#24 AUP vocabulary refusals (a prompt-edit regression sweep would have caught the pattern pre-dispatch), and the hand-done Sonnet/Fable comparison.

**The fix:** keep the outer loop (production outcomes — honest, slow, confirming) and add the missing **inner loop**: recorded step executions become replayable fixtures; step variants compete on fixed fixtures under versioned evaluators; discovery happens in the fixture tier at step cost, and production confirms before promotion.

## Design in one paragraph

Every production step execution is automatically captured as a content-addressed **fixture** (input workspace pin, ticket-state snapshot, frozen config, env pins). A **replay** runs one step-variant against a fixture as an ordinary one-step pipeline run, isolated from production state by the stub platform surface; an **evaluator** (itself a versioned workflow definition) scores the candidate output. An **experiment** is a declared matrix of `(fixture × variant × repetition)` cells — step-level cells replay; end-to-end cells dispatch real tickets exactly as plan 14 decided. Scorecards become two-tier (fixture tier discovers, production tier confirms). The retrospective agent's proposals must arrive with bench evidence attached. Every verb is API-first and principal-authenticated so a future supervisor agent is just another principal with a budget — no new trust surface.

## Guiding principles

1. **Three old nouns, three new nouns, zero new runtime concepts.** A replay *is* a pipeline run; a variant *is* a workflow definition; an evaluator *is* a workflow definition. The bench adds only: **fixture**, **experiment** (with cells), **score**. Any design that invents a new execution engine, config format, or credential path is wrong.
2. **Capture is free; replay is deliberate.** Production runs never pay bench costs (capture is fire-and-forget, non-fatal, like gateway metering); nothing replays without a declared experiment and budget.
3. **Labels are joins, not copies.** Fixture "labels" (six-verdict feedback, judge scores, merge outcome, human-touch delta) are **query-time joins** through the fixture's `(ticket_id, build_id, plan_id)` keys into the tables that already own that truth. The bench writes no label rows and runs no label-sync machinery.
4. **The bench writes nothing to production state.** In replay, the platform surface is a stub: reads serve the fixture's snapshot; writes are absorbed and recorded as part of the candidate output. No tickets, no reviews, no outcomes rows from step cells.
5. **Evaluators are versioned and calibrated.** Every score row pins `(evaluator_name, evaluator_version)`. Every experiment batch carries negative controls: an identical-to-baseline variant must tie (catches evaluator noise) and a deliberately-degraded variant must lose (catches evaluator blindness). Control failure flags the whole experiment `evaluator-suspect` — never silently reported.
6. **Revealed complexity.** The minimal experiment spec is two fields (`step_kind`, `variants`); defaults supply fixtures, repetitions, evaluator, controls, and budget. Matrices, holdout policy, corpora, and per-cell drill-down exist when you dig, and never complicate the two-field path.
7. **Production confirms; the bench discovers.** A fixture-tier win is necessary but not sufficient for promotion; `set-live` stays a human decision informed by both tiers.

## Components

### 1. Fixture capture

**Trigger:** the existing server-side ingestion path (agent-step / harvest-step metrics ingestion), which already streams `results.json`/`events.ndjson`. At ingestion time, capture promotes the step's I/O to fixture retention. Capture failure is logged and **never** fails the production step (principle 2).

**Workspace pinning — `(repo, base_sha, overlay)`, not tarballs of everything:** the workspace at step entry is a git checkout at a known sha plus small deltas. A fixture pins `{repo, base_sha, overlay_ref}` where the overlay is a bounded tarball of non-git deltas (materialized spec files, prior-step outputs). Replay restores by clone-at-sha + overlay apply, using the outcome-watcher's existing read-only git credential. If the overlay exceeds the size bound, a registry row is **still written** with `replayable=false` and a `skip_reason` — visible and filterable, never silently dropped (house rule: no silent caps). This is plan 14's `beforeRef` generalized to every step boundary.

**Also pinned:** the ticket-state snapshot (envelope + spec + tasks as-of step entry, JSON), the frozen step config (prompt bundle, model, sidecar mix — dispatch already freezes workflow version), and **env pins**: runner image tag and sidecar image tags. Env pins are load-bearing — the A0-1 incident (28 versions of silent runner-image skew changing behavior) is why replays must record and report the environment they ran under.

**Recorded output:** refs to the step's produced workspace state (`result_sha` or overlay), `results.json`, `events.ndjson`. The recorded production execution thereby doubles as a free comparison point alongside any experiment's explicit baseline variant.

**Splits:** at capture, each fixture is deterministically assigned `split: open | holdout` by content-hash (e.g. hash mod 5 == 0 → holdout). Automatic, sticky, un-gameable by selection. Default experiment fixture sets draw from `open` only; holdout is reserved for pre-promotion confirmation. Curated sets are just tags.

**Sources:** `production` (auto), `synthetic` (fault-injection, §5 — carries `ground_truth`), `benchmark` (imported end-to-end cases, absorbing plan 14's `agent_benchmark_cases`).

**Non-replayable flags:** fixtures whose step parked on `ask_human`, or whose capture was partial, are flagged and excluded from default sets (visible, filterable, never silently dropped).

### 2. Fixture registry

One table, `agent_step_fixtures` (sketch — final DDL is the bench-core plan's Task 1 contract addendum):

```
id, created_at, source ('production'|'synthetic'|'benchmark'),
step_kind ('review'|'implement'|'plan'|'workflow'), repo,
ticket_id NULL, build_id NULL, plan_id NULL,          -- join keys; labels are joins (principle 3)
content_hash,                                          -- of the pinned input bundle; dedup + split
split ('open'|'holdout'), tags TEXT[], pinned BOOL, replayable BOOL, skip_reason TEXT,
input_ref JSONB,   -- {repo, base_sha, overlay_ref, ticket_snapshot_ref, config_ref, env: {runner_image, sidecars[]}}
output_ref JSONB,  -- {result_ref, results_json_ref, events_ref}
ground_truth JSONB NULL  -- synthetic only: injected-fault manifest
```

**Blob storage:** overlays/snapshots go in a dedicated fixture store; small JSON inline in DB under a bound (the L-3 512KiB evidence precedent), larger blobs in a PVC-backed store addressed by `*_ref`. Blobs must survive artifact-fabric GC and web restarts and be mountable into replay pods. Exact store mechanics (PVC vs registry-blob) are a plan-level decision — the *contract* is the ref shape plus retention: unpinned production fixtures expire (default 30d); pinned, holdout, and synthetic fixtures persist.

### 3. Replay runner

A replay cell renders to an ordinary **one-step template pipeline run**: `restore` (task step: clone `repo@base_sha`, apply overlay, mount snapshot/config) → **step-under-test** (the variant's step, exactly as dispatch would render it, with the variant's env pins honored) → `evaluate` (§4). A small bench renderer produces this; it reuses dispatch's render library and **must not touch `render.go`'s refusal chain** (contended chokepoint).

**Isolation:** the platform-mcp surface in replay is the **stub ATC from ticket #40's contract-test kit**, serving the fixture's ticket snapshot read-only; writes (submit_spec, update_task_status, review publishes) are absorbed and recorded into the candidate output. `ask_human` is disabled. Step cells create **no tickets** and touch no production tables; their spend lands in the existing ledger attributed to the experiment (new `source` value), inside the experiment's budget envelope.

**Workflow (end-to-end) cells** are the exception, and deliberately so: per plan 14's already-decided §1.12.2, they create ordinary tickets (`origin` tag for experiments) and ride the real dispatcher — one render+admit+run path, no second entrypoint. The bench keeps that decision verbatim.

**Nondeterminism:** `repetitions: k` per cell; results report distributions (per-metric mean/min/max across reps), and single-rep experiments say so on the results surface.

### 4. Evaluators

An evaluator is a **workflow definition** whose input is `(fixture, candidate_output)` and whose output is a score envelope — deterministic ones as task steps (gates, grounding checks), LLM ones as agent steps with schema-constrained output (the judge's existing method). The harvest judge engine (`agent/harvest/judge.go`) is factored to be invocable from the bench — it *is* the first implement-step evaluator; do not build a second judge.

**Score envelope** (stored per cell × evaluator, pins mandatory):

```
{metrics: {name: number}, verdicts?: [...], rationale_ref?,
 pins: {evaluator_name, evaluator_version, fixture_id, variant, rep}}
```

**Per step kind (the evaluability ladder):**

| Step kind | Evaluator v1 | Ground truth |
|---|---|---|
| review | precision vs joined six-verdict labels; **recall vs fault-injection ground truth** (§5) | production feedback joins; synthetic manifests |
| implement | gates (deterministic) + judge score + downstream-review finding count | production labels via joins where present |
| plan | deterministic grounding checks (cited files/lines exist — mechanizing the human verifier prompts) | — |
| plan (later slice) | **implementor-variance**: fix the plan, run k cheap implementors, score by mean *and variance* of downstream gates/judge — a good plan makes weak implementors succeed | downstream evaluators |
| workflow | existing outcome metrics (aggregate status, cost, findings; production cells add merge outcome / human-touch delta by join) | outcome tables |

### 5. Fault-injection corpus (review recall)

A corpus-builder workflow takes a known-clean merged diff, injects realistic bug classes (an agent step), and registers `synthetic` fixtures whose `ground_truth` lists the injected faults (class, location, description). Reviewer recall = fraction of injected faults caught; volume is cheap and arbitrary. Two house lessons apply: injected-fault *classes* should seed from recurring production review findings (this is the leftward-migration engine — when a deterministic gate learns a class, that class retires from LLM-review scope, and the corpus records the transfer), and corpus prompts must mind the **AUP vocabulary lesson** (#23/#24: "inject bugs" phrasing needs the CI-context-first framing; the corpus builder is itself the first refusal-regression testbed).

### 6. Experiment harness

**Spec (YAML, minimal form is two fields):**

```yaml
step_kind: review
variants: [live, review-prompts@v5]
# defaults: fixtures=default open set for step_kind, repetitions=1,
#           evaluator=live evaluator for step_kind, controls=auto, budget=default envelope
```

Full form reveals: `fixtures:` (tags/ids/filters), `repetitions:`, `evaluator:` (pinned version), `controls:` (auto | none — none requires a stated reason, recorded), `budget_usd:`. `variants: [live, ...]` sugar resolves the current live definition. Controls `auto` = one identical-to-baseline clone + one degraded clone (e.g. truncated-context) synthesized by the harness.

**Cells:** `(fixture × variant × rep)` rows, three-way status taxonomy (`ok|failed|error` — "variant did badly" ≠ "bench broke"), each linked to its pipeline run (step cells) or ticket (workflow cells). Budget exhaustion mid-matrix marks remaining cells `skipped-budget`; results render partial and say so. Never silent truncation.

**Surface (API-first, supervisor-ready):** `POST/GET /api/v1/agent/bench/experiments`, `GET .../experiments/:id/results` (variant × metric table with baseline deltas + control verdicts), fixture list/tag/pin routes; fly verbs `fly agent bench run|results|fixtures`. Route additions follow the #36 six-touchpoint pattern. Web UI is deliberately later (S-track); API+fly first.

### 7. Scorecards — two-tier (reshapes 13)

Plan 13 survives with a superseding amendment: its production-traffic rollup becomes the **production tier** (confirming, honest, slow), and a **fixture tier** is added — same scorecard shape, computed over `agent_bench_scores` with paired-on-fixture deltas and control status. The promotion view shows both tiers side-by-side; a candidate with no production traffic still renders a real fixture-tier column. The amendment preserves 13's applied F8 fix (live flag + hash read authoritatively from `agent_workflow_definitions`).

### 8. Retrospective loop (reshapes 14-M2)

14-M2's analytics (findings/calibration/friction, windowed) survive as planned. The retrospective agent gains one requirement: **improvement proposals must arrive with bench evidence** — a proposed prompt/workflow edit is expressed as a variant, run against the relevant default fixture set, and the ticket it files embeds the results table (or states why no fixture set applies — e.g. a brand-new step kind). Proposals stay human-merged; evidence turns the suggestion box into a loop.

### 9. Supervisor readiness (design-now, build-never-yet)

The future supervisor is **just another principal with a budget**. Requirements on this design, auditable per verb: every iteration action (run experiment, read results/fixtures, import workflow version, propose promotion, file ticket) exists as an API verb under principal auth — **no human-only verb in the experiment loop**. Its freedom is confined to the consequence-free fixture tier by construction (principle 4); its writes to production reality pass the same human gates as everyone's (`merge`, `set-live`). Anti-Goodhart guardrails are structural, not policy: rolling fresh production fixtures, the sticky holdout split, per-batch negative controls, evaluator-version pinning, and the experiment budget envelope under the global daily cap.

### 10. Gateway relationship (deliberately none)

10-gateway-mcp executes **as planned, on its original charter**: the primary agent's mid-flight capability — `request_review` (fresh-context reviews before harvest), `ask_agent`, provider adapters, universal metering, never-silent cutoff. The bench needs **zero gateway changes**: a review-step variant that wants another provider is simply a workflow definition that mounts the gateway sidecar — the same way a production step would. Provider diversity thereby becomes measurable offline and usable mid-flight through one contract, and "does a mid-run fresh-context review improve outcomes?" is itself just a workflow-cell experiment.

## The basic experience (the simplicity contract)

What a user (or supervisor) does in the common case — anything below that requires more than this is over-built:

```
$ fly agent bench run --step review --variant review-prompts@v5
  → experiment 7 created: 2 variants (v5, live) × 24 fixtures × controls; budget $12 (default envelope)
$ fly agent bench results 7
  → variant × metric table: precision / recall / cost / turns, deltas vs live, controls: PASS
```

Capture requires nothing. Curation requires nothing (defaults + auto-splits). Two fields declare an experiment. One table answers it. Corpora, holdouts, evaluator pinning, per-cell transcripts, and workflow-cell matrices are all *there* — behind flags and drill-down routes, never in the way.

## Data model sketch

| Table | Purpose | Notes |
|---|---|---|
| `agent_step_fixtures` | fixture registry (§2) | labels via joins — no label tables |
| `agent_bench_experiments` | spec, status, budget, created_by | absorbs plan 14's `agent_experiments` shape |
| `agent_bench_cells` | (experiment, fixture, variant, rep) → run/ticket link + status | generalizes `agent_experiment_runs` |
| `agent_bench_scores` | score envelopes, evaluator-pinned | one row per cell × evaluator |

Plan 14's `agent_benchmark_cases` table is **not built**; benchmark cases import as `source: benchmark` fixtures. Migration numbers: claim from the remainders registry above real head `1773106090` (14's reserved 1773106100–102 block is the natural home; renumber per registry discipline at execution time). No changes to any existing table.

## Failure handling

- Capture failure → logged, production run unaffected (principle 2). Partial capture → fixture flagged non-replayable with reason.
- Replay restore failure (sha unfetchable, overlay mismatch) → cell `error` with reason; fixture flagged for review.
- Evaluator failure → cell `error`; distinguished from a low score. Controls failing → experiment flagged `evaluator-suspect`; results annotated, never suppressed.
- Budget exhaustion → remaining cells `skipped-budget`; partial results honest.
- Web restart mid-experiment → cells are pipeline runs / tickets; existing supervisor-resume and DB-backed state apply; the experiment runner is a RunnableComponent with polling (never notify-only — house rule).

## Testing approach

- **Round-trip self-test:** a deterministic echo-step fixture (capture → replay reproduces recorded output byte-stable) is the bench's own canary suite.
- Bench renderer: golden-file tests per rendered replay pipeline (dispatch's honesty pattern).
- Evaluator calibration: the negative controls *are* the test; plus unit suites per evaluator with fixed fixtures.
- Contract tests: fixture-store round-trip; stub-ATC write-absorption.
- Live theborg test (mandatory, throwaway namespace): one real replay pod with stub sidecar — fake clientset can't exercise sidecar transport (house lesson).

## Out of scope

- The supervisor agent itself (this design only guarantees its verbs exist).
- The parallel-variants + selector grammar extension (online best-of-N): **reserved as a declared-but-inert slot**, owner-gated; the grammar stays linear (decision 13).
- Auto-promotion of any kind; evaluator changes are human-approved like workflow promotions.
- Web UI for the bench (API + fly first; rides the S-track later).
- Cross-team/cross-repo fixture sharing, privacy machinery (single team).
- Naming bikeshed: "bench" is a working title — run it through the S-8 naming-spine review before fly verbs freeze.

## Handoff briefs (for plan-writers)

Each plan follows `superpowers:writing-plans`, opens with a Task-1 contract addendum to `00-shared-contracts.md §11`, and obeys house rules: migration-registry claims + ascending merge order; never touch `render.go`'s refusal chain; TDD; `make test-quick` green per merge; no `--race`; live tests in throwaway namespaces only.

**A. bench-core** (largest): fixture registry DDL + factory; capture hook in the ingestion path (fire-and-forget); fixture store (blob mechanics decided here); splits/tags/retention; replay renderer (one-step template pipeline, stub-ATC sidecar wiring, write-absorption); experiment tables + runner component + budget envelope; API routes + fly verbs (six-touchpoint). Verify vs plan 14 M1 and mark superseded pieces explicitly.
**B. evaluators + corpus:** score envelope + `agent_bench_scores`; judge-engine factoring for bench invocation; review precision (feedback joins) + recall (fault-injection corpus builder, AUP-vocabulary-aware); implement evaluator composition (gates+judge+downstream-findings); deterministic plan grounding checks. Implementor-variance is a *declared later slice* — design the seam, don't build it.
**C. 13-scorecards amendment:** two-tier rollup; fixture-tier over `agent_bench_scores`; paired deltas; promotion view; carries F8.
**D. 14 supersede/retain:** explicit disposition per task — M1 experiments/benchmarks → superseded by A (benchmark import becomes fixture import); M2 analytics → retained as planned; retrospective agent → retained + bench-evidence requirement (§8).
**E. gateway:** execute 10-gateway-mcp.md as written; no bench coupling (§10).

**Sequencing:** A's capture slice lands **first and alone if needed** — it is cheap, invisible, and every day it runs accrues the data everything else consumes. Then B (review evaluator + corpus → first bake-off), then the rest of A (harness), C, D. E proceeds in parallel on its own wave-3 track.

## Open items for the planning phase

1. Fixture-store blob mechanics (PVC vs registry-blob) and size bounds (overlay cap, inline-JSON cap).
2. Stub-ATC write-absorption scope: exactly which platform-mcp/review write verbs are recorded vs rejected in replay.
3. Six-verdict → fixture join path: verify `agent_feedback → agent_reviews (ticket linkage 1773106080) → fixture` resolves per-finding; pin the join in B's Task 1.
4. Default fixture-set policy (recency window? per-repo caps?) and the holdout hash fraction.
5. Experiment budget envelope default + its seat under the global daily cap (dispatcher admission precedent).
6. Judge-engine factoring seam (what moves out of `agent/harvest` so bench can invoke it without importing harvest).
7. Degraded-control synthesis (what "deliberately worse" means per step kind — truncated context is the v1 candidate).
8. Migration numbers at execution time (registry: real head 1773106090; 1773106067 free; 14's 1773106100–102 reservation).
9. Bench naming through S-8 before fly verbs freeze.

## Amendments

*(append-only; supersede by entry, not by edit)*
