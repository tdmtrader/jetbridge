# The Bench — Track D: Plan 14 Supersede / Retain Disposition

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Descends-from:** `14-process-intel-experiments.md` (amends it), `docs/superpowers/specs/2026-07-19-agent-bench-design.md` (§8 retrospective loop, handoff brief **D**, "Data model sketch"). Cross-track siblings: `bench/A1-fixtures`, `bench/A2-replay-harness`, `bench/B-evaluators`, `bench/C-scorecards`.
**Tasks:** 9.
**Complexity:** Low. **Risk:** Low (documentation disposition + one prompt-level amendment to an unbuilt M2 task; no landed code changes, no migration, no `render.go` touch).
**Migrations:** **NONE (bench-owned).** Plan 14's reserved `1773106100–102` block is *reclaimed* by the bench (A1 = `1773106100`, A2 = `1773106101`, B = `1773106102`); plan 14's **retained** `agent_reviews.defect_link` keeps its own plan-14-owned `1773106103`. This track allocates no number. See §"Migration-registry reconciliation".

---

**Goal:** Walk every one of plan 14's 20 tasks and mark each **superseded-by-bench**, **retained**, or **retained-with-change**, then land that disposition as (a) a superseding entry + a per-task table pointing at this doc from plan 14's §11 log and milestone headers, and (b) the one concrete behavioral amendment the disposition introduces — the retrospective agent's improvement proposals must now arrive **with bench evidence** (spec §8): a proposed prompt/workflow edit expressed as a bench variant, run against the relevant default fixture set, its results table embedded in the filed `origin:retrospective` ticket (or a stated reason no fixture set applies).

**Architecture:** Plan 14 is **entirely unbuilt at HEAD** (verified: no `agent/experiments`, `agent/intel`, `agent/retrospective`, no `agent_benchmark_cases`/`agent_experiments`/`agent_experiment_runs`/`defect_link` migrations; on-disk migration head is `1773106091_create_agent_settings`). So this disposition governs plan 14's *unexecuted plan*, not landed code — "superseded" means those tasks are **never executed** (the bench replaces them), "retained" means they execute **as written**, and "retained-with-change" means they execute **with this track's amendment folded in**. The disposition splits cleanly along plan 14's own milestone boundary: **M1 (Tasks 1–11, experiments + benchmarks)** is superseded by bench tracks A1 (fixtures) and A2 (experiments/cells) — benchmark cases import as `source:'benchmark'` fixtures and the `agent_benchmark_cases` table is not built; **M2 (Tasks 12–17, analytics)** is retained verbatim, including its F9/F39 window fixes; **the retrospective agent (Tasks 18–20)** is retained with the bench-evidence requirement folded into its seed workflow prompt and its intel-context materializer (the F10 intel-delivery-seam fix on Tasks 18–19 is retained unchanged). One nuance survives the M1/M2 line: plan 14 Task 1's §1.12.2 **"runner dispatches tickets, not the renderer"** decision is **kept verbatim** by bench A2 for workflow (end-to-end) cells — the bench does not re-decide it.

**Tech Stack:** Markdown (this doc + the plan-14 amendments it directs); Go (plain `testing`, matching `agent/api/reviews` / the retrospective's own suite) for the two retrospective-amendment code tasks; the workflow-definition §6 YAML grammar (`agent/workflow`) for the seed-prompt edit. No SQL, no Elm, no new route. `make test-quick` is the per-merge gate.

---

## Context

**Charter (spec handoff brief D):** *"14 supersede/retain: explicit disposition per task — M1 experiments/benchmarks → superseded by A (benchmark import becomes fixture import); M2 analytics → retained as planned; retrospective agent → retained + bench-evidence requirement (§8)."* The bench design (spec §Problem) reshapes plan 14 because plan 14's matrix cell is one **full ticket** through the whole loop — nothing in it can run *one step* against *one fixed input* twice. The bench adds that inner loop; plan 14's outer-loop analytics and its retrospective survive, and its experiment substrate is replaced by the fixture/experiment/score tables.

**Why a disposition doc and not a rewrite of plan 14.** Plan 14's M2 is good work and lands as written; duplicating Tasks 12–17 here would create two sources of truth. This track therefore **references** the retained tasks by number and only **amends** plan 14 in place (a superseding banner + §11 entry + milestone-header banners) so a future reader of plan 14 is redirected here for M1 and told M2 is live. The substance — the per-task walk — lives in this doc (§"Per-task disposition of plan 14"), which plan 14 points at.

### What the bench replaces M1 with (the frozen skeleton, for grounding)

Per the FROZEN cross-track contract skeleton and spec "Data model sketch", the bench's four tables replace plan 14's three M1 tables:

| Plan 14 M1 table (NOT built) | Bench replacement (owner) | Migration |
|---|---|---|
| `agent_benchmark_cases` | folded into `agent_step_fixtures` (`source='benchmark'`, `ground_truth` for synthetic) — **A1** table; import rides **B's** registration route (amended 2026-07-19 post-review) | `1773106100` |
| `agent_experiments` | `agent_bench_experiments` (absorbs its `name`/`description`/`status`/`created_by` shape; adds `step_kind`, `spec` JSONB, `budget_usd`, `control_status`) — **A2** | `1773106101` (with cells) |
| `agent_experiment_runs` | `agent_bench_cells` (`benchmark_case_id`→`fixture_id`; `workflow_name/version`→`variant/variant_version`; adds `control_role`, `pipeline_run_id` **and** `ticket_id` for the step-vs-workflow cell duality) — **A2** | `1773106101` |
| — | `agent_bench_scores` (one row per cell × evaluator; score envelope) — **B** | `1773106102` |

The `agent_experiment_runs.ticket_id` column plan 14 Task 1 §1.12.2 added for workflow cells is preserved **verbatim** as `agent_bench_cells.ticket_id` (skeleton §DDL notes: *"Plan 14 §1.12.2's 'runner dispatches tickets, not the renderer' is preserved verbatim for workflow cells — the `ticket_id` column is that seam"*).

### Migration-registry reconciliation (load-bearing)

- **On-disk head is `1773106091`** (`create_agent_settings`, dispatcher runtime-toggle) — verified against `atc/db/migration/migrations/`. Some plan-14 prose says head is `1773106090`; that predates the settings migration. The `92–99` slots belong to delivery-outcomes' block and are **not** touched here.
- **The bench reclaims plan 14's reserved `1773106100–102` block.** Because plan 14's M1 tables were **never built** (M1 unbuilt at HEAD), those numbers carry no on-disk migration and are free to reassign. A1→`100`, A2→`101`, B→`102`, strictly ascending, one per push (house spine rule; ascending == referential-dependency order: cells FK fixtures, scores FK cells).
- **Plan 14's retained `agent_reviews.defect_link` migration keeps `1773106103`** (Task 12). It is **plan-14-owned, not a bench allocation** (skeleton §4). `103 > 102`, so it lands after the bench block in ascending order; it only `ALTER`s `agent_reviews` (no FK into any bench table), so its position relative to the bench block is unconstrained beyond "ascending". The `jetbridgeHeadMigration` const progression is unchanged from plan 14's plan (A/B bump it to `102`; the retained defect-link task bumps it to `103`).

### Prior-wave / cross-track assumptions

- **A1 (fixtures)** lands `agent_step_fixtures` + the capture hook. This track's "benchmark import becomes fixture import" disposition points at **A1's table via B's registration route** (`source:'benchmark'`, amended 2026-07-19 post-review — A1 ships no importer); it does not re-specify the import.
- **A2 (replay/harness)** lands `agent_bench_experiments`/`agent_bench_cells`, the experiment runner component, the budget envelope, and the six-touchpoint bench routes/fly verbs (`/api/v1/agent/bench/*`, `fly agent bench run|results|fixtures`). This track's retrospective-evidence tasks **call A2's `POST /experiments` + `GET /experiments/:id/results`** as a principal; it does not re-specify them. A2 also carries plan 14 §1.12.2 forward verbatim for workflow cells. **A2 owns the experiment budget envelope**: `agent_bench_experiments.budget_usd = 0` ⇒ the default `$12` envelope resolved at admission, charged as ordinary `source:'agent_step'` ledger rows attributed to the experiment via the cell join (A2 §1.12.3 — no bench/experiment ledger source exists; the CHECK forbids it; amended 2026-07-19 post-review), seated under the global daily cap (spec §3/§9, skeleton open Q7). This track spends nothing from that envelope out of the retrospective ticket's budget — the two pools are distinct.
- **B (evaluators)** lands `agent_bench_scores` + the review precision/recall evaluators. The retrospective's default-fixture-set evidence reads through B's evaluators via A2's results endpoint; not re-specified here.
- **C (13-scorecards amendment)** lands the two-tier (fixture + production) scorecard. The retrospective's evidence table is the A2 results projection, not C's scorecard, but the promotion decision a human makes on a retrospective proposal reads C's two tiers side-by-side. Referenced, not duplicated.
- **ticket-core / platform-mcp / workflow-store / budget** — plan 14 M2's retained tasks consume these exactly as plan 14's Context lists (`tickets.Store.Transition`/`Create`/`SubmitSpec`, `origin:'retrospective'`, `CreateAgentTicket`, the platform credential §1.13). This track adds no new consumption beyond the bench API call named above.

**Anchor caveat:** plan 14 is unbuilt, so every file this track's code tasks touch (`agent/retrospective/*`) is **created by plan 14 M2 Tasks 18–19**, marked **NEW (plan-14-M2-owned)** below. This track's code tasks are *deltas folded into those tasks at execution time*, not edits to landed files. Treat the plan-14 line numbers cited as "the location of the quoted code in plan 14's task body", not repo line anchors. The code tasks (5–7) carry an explicit precondition gate — see it below the disposition table.

### Contract §1.12 / §1.1 / §11 addendum this track contributes (the A track places it, single-writer)

The bench design (open item 8) and skeleton §4 require the migration-block reclaim and the M1-supersede to be recorded in `00-shared-contracts.md`, where cross-track planners read it. This track does **not** write that file — **A1 (`bench/A1-fixture-capture.md`) Task 1** owns §1.12's reclaim (owner assigned 2026-07-19 post-review; its Task-1 checklist enumerates absorbing this track's deferred §11 line by name), and a second writer would collide on §1.12. This track supplies the text and defers the write to A1 (tracked in "Open decisions → contract §1.12 single-writer"):

- **§1.12 supersede banner (prose):** *"Superseded 2026-07-19 by the bench (spec `2026-07-19-agent-bench-design.md`). `agent_benchmark_cases`/`agent_experiments`/`agent_experiment_runs` are NOT built; benchmark cases import as `source:'benchmark'` `agent_step_fixtures`, experiments become `agent_bench_experiments`+`agent_bench_cells`, scores `agent_bench_scores`. Plan 14 §1.12.2's 'runner dispatches tickets, not the renderer' decision is retained verbatim for workflow cells (`agent_bench_cells.ticket_id`). Plan 14's M2 analytics (§1.12.1 defect-link, `agent/intel`) and the retrospective are retained; the retrospective additionally requires bench evidence on proposals (spec §8). Full per-task disposition: `bench/D-14-disposition.md`."*
- **§1.1 registry annotation:** block `1773106100–109` — *"`100/101/102` reclaimed by bench (fixtures / experiments+cells / scores); `103` retained by plan-14 `agent_reviews.defect_link`; `104–109` free within the block."*
- **§11 log entry (prose):** the disposition summary + this doc's path.

---

## Per-task disposition of plan 14 (the walk)

Legend: **SUPERSEDED** = not executed; the bench replaces it. **RETAINED** = executes as written. **RETAINED+Δ** = executes with this track's amendment folded in.

| # | Plan 14 task | Disposition | Bench owner / replacement | Notes |
|---|---|---|---|---|
| 1 | Wave-start contract addendum (§1.12.2: experiment/benchmark schema freeze, analytics routes, dispatch-reuse) | **SPLIT** | A2 (experiment schema + dispatch-reuse), M2-retained (analytics + retrospective routes) | The **schema freeze** (`agent_experiments`/`agent_experiment_runs` shapes, `experiments.Store`, `ScorecardForTickets`) → **SUPERSEDED** by A2's `agent_bench_experiments`/`agent_bench_cells` + A2's results endpoint. The **"runner dispatches tickets, not the renderer" decision** → **RETAINED verbatim** by A2 for workflow cells (the `ticket_id` seam). The **daily-cap admission** (`budget.GlobalDailyRemaining()`) → **RETAINED** by A2 as the experiment-budget-envelope precedent (skeleton open Q7). The **analytics routes** (`GetAgentFindingAnalytics/Calibration/Friction`) and **`RunAgentRetrospective`** → **RETAINED** (M2). The **two experiment routes** (`ListAgentExperiments`, `GetAgentExperimentDelta`) → **SUPERSEDED** by A2's `/bench/experiments*`. |
| 2 | Migrations `1773106100–102` (`agent_benchmark_cases`, `agent_experiments`, `agent_experiment_runs`) | **SUPERSEDED** | A1 `1773106100` (fixtures), A2 `1773106101` (experiments+cells), B `1773106102` (scores) | Block **reclaimed** (numbers were never built). `agent_benchmark_cases` is **not built** — benchmark cases import as `source:'benchmark'` fixtures (A1). |
| 3 | `agent/api/experiments` — domain types, matrix validation, MemoryStore | **SUPERSEDED** | A2 `agent/api/bench` types (`Experiment`/`Cell`) + A1 fixture types | `Matrix{Cases,Workflows,Repetitions}` → A2's experiment `spec` JSONB (`{variants[], fixtures?, repetitions, evaluator, controls, budget_usd}`, spec §6). `BenchmarkCase` → A1 fixture `input_ref`/`output_ref`/`ground_truth`. |
| 4 | `atc/db.NewAgentExperimentFactory` (squirrel factory) | **SUPERSEDED** | A2 bench experiment/cell factory | Same squirrel/`ON CONFLICT`/epoch-scan recipe, new tables. |
| 5 | `agent/api/experiments` HTTP handler — benchmark CRUD routes | **SUPERSEDED** | A1 fixture list/tag/pin routes + A2 experiment routes | Benchmark CRUD collapses into fixture registry routes; no `POST/GET /benchmarks`. |
| 6 | `fly agent benchmarks` (list/create) | **SUPERSEDED** | `fly agent bench fixtures` (A1/A2 six-touchpoint) | Verb renamed under the `bench` family (provisional pending S-8, skeleton open Q11). |
| 7 | Benchmark extraction skill port (`extract-benchmark`, mines `{prompt, beforeRef, referenceRef}`) | **SUPERSEDED** | B's registration route (`source:'benchmark'`) into A1's `agent_step_fixtures` | The *mining idea* survives: a merged PR still yields `{prompt, before_ref, reference_ref}` — but it registers a **`source:'benchmark'` `agent_step_fixtures` row** (workflow-kind fixture with `ground_truth` optional) instead of an `agent_benchmark_cases` row. Import path (amended 2026-07-19 post-review): benchmark import rides **B's** registration route (`RegisterSyntheticFixtures`, `source ∈ {synthetic, benchmark}` + any step_kind) — A1 ships no importer; this track does not port the skill. |
| 8 | Experiment CRUD API (create/list/get) | **SUPERSEDED** | A2 `POST/GET /api/v1/agent/bench/experiments`, `GET .../:id` | `202`/`pending`-then-runner semantics preserved by A2. |
| 9 | `experiment_runner` RunnableComponent (daily-cap-admitted ticket dispatch) | **SUPERSEDED** | A2 bench experiment runner | **Mechanism retained inside A2:** step cells replay one-step pipeline runs; **workflow cells** create `origin` tickets and ride the dispatcher via `Transition` (plan 14 §1.12.2 verbatim). Daily-cap admission via `GlobalDailyRemaining()` **retained** as the envelope precedent. Polling-not-notify-only house rule preserved. |
| 10 | Experiment scorecard-delta view (`GetAgentExperimentDelta`, `ScorecardForTickets`) | **SUPERSEDED** | A2 `GET /experiments/:id/results` (variant × metric, baseline deltas, control verdicts) + C fixture tier | The **`ScorecardForTickets` ticket-scoped rollup recipe** is **retained-in-spirit** by C's **production tier** for workflow-cell deltas (still a ticket-restricted rollup over `agent_run_metrics`/`agent_outcomes`); the primary bench delta reads `agent_bench_scores` (B's score envelope). |
| 11 | `fly agent experiments` (run/list/show `--delta`) | **SUPERSEDED** | `fly agent bench run|results` (A2) | The two-field basic-experience path (`fly agent bench run --step review --variant …`) is the direct heir of `experiments run`. |
| 12 | Defect→ticket linking convention + `agent_reviews.defect_link` migration `1773106103` | **RETAINED** | plan 14 (M2) — unchanged | Calibration's missed-issue key. Migration `1773106103` stays **plan-14-owned** (not a bench number); lands after the bench `100–102` block, ascending. `reviews.Store.SetAgentReviewDefectLink` unchanged. **Interaction note:** distinct from bench open Q4 (B may add `build_id`/`review_id` to `agent_feedback` for the per-**finding** join). `defect_link` is on `agent_reviews` (per-**review**) and is orthogonal — B's key does not subsume or conflict with it. |
| 13 | `agent/intel` — finding analytics (findings per repo/version, recurring classes, catches-migrate-leftward) | **RETAINED** | plan 14 (M2) — unchanged | Includes the **F9** window fix (`applyWindow` on `m.created_at`). |
| 14 | `agent/intel` — calibration (FP + missed-issue rates) | **RETAINED** | plan 14 (M2) — unchanged | Includes **F9/F39** window fixes (`sq.Expr` form, not Sqlizer-in-map). |
| 15 | `agent/intel` — friction mining (2–3 frozen signatures) | **RETAINED** | plan 14 (M2) — unchanged | Includes **F9** window fix. |
| 16 | `atc/db` intel Queryer + analytics API routes | **RETAINED** | plan 14 (M2) — unchanged | Includes **F9/F39** window fixes; the three analytics routes ride plan 14 Task 1's addendum. |
| 17 | Minimal Elm analytics view | **RETAINED** | plan 14 (M2) — unchanged | Read-only dashboard; no bench coupling. |
| 18 | Retrospective workflow definition + intel-context materializer | **RETAINED+Δ** | plan 14 (M2) + **this track Tasks 5–6** | `RenderIntelMarkdown` gains a **bench-evidence guidance** section (default fixture set per implicated `step_kind`); the seed prompt gains a **mandatory `## Bench evidence`** proposal-template section. F10 spec-delivery fix retained. |
| 19 | `retrospective_trigger` component (manual + recurring cadence) | **RETAINED+Δ** | plan 14 (M2) + **this track Task 7** | Delivers the bench-guidance-augmented snapshot as the ticket spec; otherwise unchanged (still one `origin:'retrospective'` ticket via `SubmitSpec`+`Transition`). F10 spec-delivery fix retained. |
| 20 | `RunAgentRetrospective` route + `fly agent retrospective run` | **RETAINED** | plan 14 (M2) — unchanged | No bench coupling; the manual trigger is unchanged. |

**Summary:** M1 Tasks 1(partial)/2–11 → **SUPERSEDED** (10 tasks, replaced by A1/A2/B). M2 Tasks 1(partial)/12–17,20 → **RETAINED** (7 tasks). Retrospective Tasks 18–19 → **RETAINED+Δ** (2 tasks, this track's amendment).

---

### Task 1: Superseding entry in plan 14's §11 amendment log

Land the redirect so a reader of plan 14 learns M1 is superseded and where the disposition lives.

**Files:**
- Modify: `docs/superpowers/plans/agentic-platform/14-process-intel-experiments.md` (append to `## §11 Amendment log (this plan)`, after the F39 entry)

**Steps:**

- [ ] Append this dated entry to plan 14's §11 log:

```markdown
- **2026-07-19 (bench supersede/retain disposition — see `bench/D-14-disposition.md`):**
  The bench design (`docs/superpowers/specs/2026-07-19-agent-bench-design.md`) reshapes this plan around
  step-level evaluation. **M1 (Tasks 1-partial, 2–11) is SUPERSEDED**: `agent_benchmark_cases`/`agent_experiments`/
  `agent_experiment_runs` are NOT built — benchmark cases import as `source:'benchmark'` fixtures
  (`agent_step_fixtures`, track A1), experiments become `agent_bench_experiments`+`agent_bench_cells` (track A2),
  scores `agent_bench_scores` (track B). The reserved `1773106100–102` block is reclaimed by the bench
  (A1=100, A2=101, B=102). **Task 1 §1.12.2's "runner dispatches tickets, not the renderer" decision and its
  daily-cap admission are RETAINED verbatim** by A2 for workflow (end-to-end) cells (`agent_bench_cells.ticket_id`).
  **M2 (Tasks 12–17, 20) is RETAINED as written** (incl. F9/F39 window fixes); the retained
  `agent_reviews.defect_link` migration keeps its own `1773106103`. **The retrospective (Tasks 18–19) is
  RETAINED WITH ONE CHANGE** (the F10 intel-delivery-seam fix on these tasks is retained unchanged):
  improvement proposals must arrive with bench evidence — the proposed edit is expressed as a bench variant,
  run against the relevant default fixture set, and the filed `origin:retrospective` ticket embeds the results
  table (or states why no fixture set applies), per spec §8. Full per-task table +
  the retrospective amendment: `bench/D-14-disposition.md`.
```

- [ ] Verify the disposition table in this doc still covers all 20 plan-14 tasks: `grep -c '^| [0-9]' docs/superpowers/plans/agentic-platform/bench/D-14-disposition.md` → expect ≥ 20 numbered rows.
- [ ] Verify: `grep -n "bench supersede/retain disposition" docs/superpowers/plans/agentic-platform/14-process-intel-experiments.md` → one hit in §11.
- [ ] Commit: `git add docs/superpowers/plans/agentic-platform/14-process-intel-experiments.md && git commit -m "docs(bench): plan-14 §11 supersede/retain entry (see D-14-disposition)"`

---

### Task 2: Milestone-1 supersede banner + migration-block reclaim note in plan 14

Redirect the M1 body so no one executes the superseded tasks.

**Files:**
- Modify: `docs/superpowers/plans/agentic-platform/14-process-intel-experiments.md` (insert a banner under `## Milestone 1 — Experiments & benchmarks`; add a one-line reclaim note under Task 2's migration header)

**Steps:**

- [ ] Immediately under `## Milestone 1 — Experiments & benchmarks`, insert:

```markdown
> **SUPERSEDED 2026-07-19 by the bench (`bench/D-14-disposition.md`).** Do NOT execute Tasks 2–11. The
> experiment substrate is replaced by `agent_step_fixtures` (A1), `agent_bench_experiments`+`agent_bench_cells`
> (A2), `agent_bench_scores` (B). `agent_benchmark_cases` is NOT built; benchmark cases import as
> `source:'benchmark'` fixtures (via B's registration route — A1 ships no importer). The reserved `1773106100–102` block is reclaimed by the bench
> (A1=100, A2=101, B=102). Task 1's §1.12.2 "runner dispatches tickets" decision + daily-cap admission are
> carried forward verbatim by A2 for workflow cells.
```

- [ ] Under `### Task 2: Migrations 1773106100–102`, add: `> Reclaimed — see the Milestone-1 supersede banner. These three tables are not built; the numbers are reassigned to the bench (A1/A2/B).`
- [ ] Verify: `grep -n "SUPERSEDED 2026-07-19 by the bench" docs/superpowers/plans/agentic-platform/14-process-intel-experiments.md` → the M1 banner.
- [ ] Commit: `git add … && git commit -m "docs(bench): banner plan-14 Milestone 1 superseded, reclaim 100-102"`

---

### Task 3: Milestone-2 retained banner in plan 14 (analytics; F9/F39 ride-along; defect-link ownership)

Confirm M2 executes as written and clarify the retained `defect_link` migration's number and its orthogonality to the bench.

**Files:**
- Modify: `docs/superpowers/plans/agentic-platform/14-process-intel-experiments.md` (insert a banner under `## Milestone 2 — Process intelligence`)

**Steps:**

- [ ] Immediately under `## Milestone 2 — Process intelligence`, insert:

```markdown
> **RETAINED 2026-07-19 (`bench/D-14-disposition.md`).** Tasks 12–17 and 20 execute AS WRITTEN — the bench does
> not change the analytics library, the analytics API routes, the Elm view, or the manual retrospective route.
> The F9/F39 window fixes ride along unchanged. The `agent_reviews.defect_link`
> migration (Task 12) keeps `1773106103`, plan-14-owned, landing after the bench `100–102` block (ascending);
> it is orthogonal to any bench per-finding join key (bench open Q4 touches `agent_feedback`, not `agent_reviews`).
> Tasks 18–19 (retrospective) are RETAINED WITH the bench-evidence amendment (the F10 spec-delivery fix on
> those tasks is retained unchanged) — see D-14 Tasks 5–7.
```

- [ ] Verify: `grep -n "RETAINED 2026-07-19" docs/superpowers/plans/agentic-platform/14-process-intel-experiments.md` → the M2 banner.
- [ ] Commit: `git add … && git commit -m "docs(bench): banner plan-14 Milestone 2 retained (analytics + defect-link)"`

---

### Task 4: Retrospective bench-evidence requirement — banner + spec in plan 14 (Tasks 18–20)

Record the one behavioral change the disposition introduces, in plan 14, before the code tasks fold it in.

**Files:**
- Modify: `docs/superpowers/plans/agentic-platform/14-process-intel-experiments.md` (insert a banner under `### Task 18` and `### Task 19`)

**Steps:**

- [ ] Under `### Task 18: Retrospective workflow definition + intel-context materializer`, insert:

```markdown
> **AMENDED 2026-07-19 by the bench (`bench/D-14-disposition.md`, Tasks 5–6).** Spec §8: improvement proposals
> must arrive WITH bench evidence. `RenderIntelMarkdown` gains a "Bench evidence" guidance section naming the
> default fixture set for each implicated `step_kind`; the seed prompt's proposal template gains a MANDATORY
> `## Bench evidence` section instructing the agent to (1) express the proposed edit as a bench variant,
> (2) run a fixture-tier experiment against the default fixture set for the target step (two-field spec:
> `step_kind` + `variants: [live, <variant>]`, defaults supply the rest), (3) embed the variant × metric
> results table (with control verdicts) — or (4) state why no fixture set applies (e.g. a brand-new step kind).
> The agent calls A2's `POST /api/v1/agent/bench/experiments` + `GET .../:id/results` under its in-pod
> principal token (spec §9 — the retrospective agent is the first real principal exercising the bench loop).
> The experiment's cell spend is charged to the experiment's OWN budget envelope (A2-resolved at admission,
> `budget_usd=0 ⇒ default`, under the global daily cap — spec §3/§9), NOT to this ticket's budget.
> Fixture-tier (step-cell) experiments only — cheap, fast, bounded; NOT workflow-cell end-to-end.
```

- [ ] Under `### Task 19: retrospective_trigger component`, insert:

```markdown
> **AMENDED 2026-07-19 (`bench/D-14-disposition.md`, Task 7).** The trigger delivers the bench-evidence-augmented
> snapshot as the ticket spec (same `SubmitSpec`+`Transition` path). `max_turns` is raised so the agent has room
> to launch and poll one fixture-tier experiment (compose the proposal, issue the two-field POST, poll
> `GET .../results`); the experiment's own budget envelope (A2-resolved; spend lands as ordinary
> `source:'agent_step'` ledger rows attributed to the experiment via the cell join — A2 §1.12.3, no bench ledger
> source exists — under the global daily cap, spec §3/§9) funds the cells, NOT this ticket's budget (see D-14
> Task 6). Otherwise unchanged.
```

- [ ] Verify: `grep -c "AMENDED 2026-07-19" docs/superpowers/plans/agentic-platform/14-process-intel-experiments.md` → 2.
- [ ] Commit: `git add … && git commit -m "docs(bench): plan-14 retrospective bench-evidence amendment banners"`

---

> **PRECONDITION GATE (Tasks 5–7 — code).** Do NOT start Tasks 5–7 until plan 14 M2 Tasks 13/18/19 have landed
> `agent/intel/` and `agent/retrospective/` in **this worktree** — verify with
> `test -d agent/retrospective && test -d agent/intel`. Neither package exists at HEAD (verified: absent;
> on-disk migration head `1773106091`); both are **created by plan 14 M2**, and this track's code tasks are
> deltas folded into plan 14 Tasks 18/19 at execution time, not edits to landed files. **Run standalone in the
> current repo, each TDD "red" step below produces a package-missing BUILD failure, not the assertion failure it
> describes** — so M2 must be built first. The `Files:` verb for the code tasks is therefore
> **Extend (fold-in delta applied during plan 14 Task 18/19 execution)**, not `Modify` of a landed file.

---

### Task 5: `RenderIntelMarkdown` — bench-evidence guidance section

Fold into plan 14 M2 **Task 18** (respect the precondition gate above). The intel snapshot the retrospective agent reads must tell it which default fixture set backs each kind of proposal, so the "run a variant against the relevant default fixture set" instruction is grounded.

**Files:**
- Extend (fold-in delta during plan 14 Task 18): `agent/retrospective/context.go` — **NEW (plan-14-M2-owned; created by plan 14 Task 18)**: extend `RenderIntelMarkdown(fa *intel.FindingAnalytics, cal *intel.Calibration, fr *intel.Friction) []byte`
- Extend (fold-in delta during plan 14 Task 18): `agent/retrospective/context_test.go` — **NEW (plan-14-M2-owned)**: add the failing test below

**Steps:**

- [ ] Add a failing test to `agent/retrospective/context_test.go` (plain `testing`, matching plan 14 Task 18's suite):

```go
func TestRenderIntelMarkdownIncludesBenchEvidenceGuidance(t *testing.T) {
	md := retrospective.RenderIntelMarkdown(
		&intel.FindingAnalytics{
			Recurring: []intel.RecurringClass{{Category: "nil-deref", Count: 4, DistinctRepos: 2}},
		},
		&intel.Calibration{FalsePositiveRate: 0.4, EvaluatedFindings: 10},
		&intel.Friction{Signatures: []intel.FrictionSignature{{Name: "turn_burn", Value: 42, SampleSize: 30}}},
	)
	s := string(md)
	// names the bench-evidence contract and the default fixture sets per step kind
	for _, want := range []string{
		"Bench evidence", "review", "implement",
		"default fixture set", "no fixture set applies",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("bench-evidence guidance missing %q:\n%s", want, s)
		}
	}
}
```

- [ ] Run `go test ./agent/retrospective/ -run TestRenderIntelMarkdownIncludesBenchEvidenceGuidance` → **red**: assertion failure (section absent) IF plan 14 M2 is built in this worktree; otherwise a package-missing build failure — build M2 first per the precondition gate.
- [ ] Extend `RenderIntelMarkdown` to append a deterministic guidance section (after the friction section, before `return`). No LLM; static text keyed by the fixed evaluability ladder (spec §4). Keep it short — it is instruction, not data:

```go
	b.WriteString("\n## Bench evidence (required on every proposal)\n\n")
	b.WriteString("Each proposal you file MUST carry bench evidence. Express the change as a workflow variant, ")
	b.WriteString("run it against the **default fixture set** for the step it targets, and paste the variant × ")
	b.WriteString("metric results table (with control verdicts) into the proposal's `## Bench evidence` section.\n\n")
	b.WriteString("Default fixture set by target step kind:\n")
	b.WriteString("- **review** — precision (vs six-verdict feedback) + recall (vs fault-injection ground truth)\n")
	b.WriteString("- **implement** — gates + judge score + downstream-review finding count\n")
	b.WriteString("- **plan** — deterministic grounding checks (cited files/lines exist)\n")
	b.WriteString("- **workflow** — outcome metrics (aggregate status, cost, findings)\n\n")
	b.WriteString("If **no fixture set applies** (e.g. a brand-new step kind), say so explicitly in the ")
	b.WriteString("`## Bench evidence` section and file the proposal anyway — a stated reason is acceptable evidence.\n")
```

- [ ] Run `go test ./agent/retrospective/` → green (this test + plan 14's existing `TestRenderIntelMarkdownIncludesSignatures`/`TestSeedDefinitionParses`).
- [ ] Commit: `git add agent/retrospective/context.go agent/retrospective/context_test.go && git commit -m "feat(retrospective): bench-evidence guidance in intel snapshot (D-14 §8)"`

---

### Task 6: Seed workflow prompt — mandatory `## Bench evidence` proposal section

Fold into plan 14 M2 **Task 18** (respect the precondition gate). Amend the seed workflow's proposal template so every filed proposal has a `## Bench evidence` section, and give the agent enough turns to launch and poll one fixture-tier experiment.

**Files:**
- Extend (fold-in delta during plan 14 Task 18): `agent/retrospective/seed_workflow.yml` — **NEW (plan-14-M2-owned)**: extend the proposal template + raise `max_turns`
- Extend (fold-in delta during plan 14 Task 18): `docs/agentic/retrospective-workflow.yml` — **NEW (plan-14-M2-owned)**: keep byte-identical to the embed
- Extend (fold-in delta during plan 14 Task 18): `agent/retrospective/context_test.go` (or a new `seed_test.go`) — assert the seed prompt carries the bench-evidence template

**Steps:**

- [ ] Add a failing golden assertion (extends plan 14 Task 18's `TestSeedDefinitionParses`). Per the resolved auth seam (Open decisions — direct principal-token HTTP is the in-pod path; `fly` may not be present in the pod), the seed prompt carries **one** run recipe (the POST), so the golden asserts that recipe, not a parallel `fly` verb:

```go
func TestSeedPromptRequiresBenchEvidence(t *testing.T) {
	s := string(retrospective.SeedDefinition())
	for _, want := range []string{
		"## Bench evidence",                    // the mandatory proposal section
		"POST /api/v1/agent/bench/experiments", // the single in-pod principal-token HTTP recipe (auth seam — Open decisions)
		"no fixture set applies",               // the escape hatch keeps brand-new step kinds trivial
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("seed prompt missing bench-evidence element %q", want)
		}
	}
}
```

- [ ] Run `go test ./agent/retrospective/ -run TestSeedPromptRequiresBenchEvidence` → red (assertion failure if M2 is built; otherwise package-missing build failure — build M2 first).
- [ ] In `agent/retrospective/seed_workflow.yml`, extend the proposal template inside the `retrospect` prompt to add the `## Bench evidence` section, and add a short "how to run it" recipe. Append to the existing template block (after `## Expected effect`). Lead with the single in-pod recipe (direct principal-token HTTP) — do NOT add a parallel `fly agent bench run` line, keeping the invocation surface singular and consistent with the resolved auth seam:

```yaml
      ## Bench evidence
      Express THIS change as a workflow variant and run a fixture-tier bench experiment against the default
      fixture set for the step it targets, then paste the variant × metric results table below.
      - Minimal run (two fields — defaults supply fixtures, repetitions, evaluator, controls, budget):
          POST /api/v1/agent/bench/experiments  {"step_kind":"<review|implement|plan|workflow>",
                                                  "variants":["live","<your-variant>"]}
        (use your in-pod AGENT_PRINCIPAL_TOKEN as the bearer; poll GET /api/v1/agent/bench/experiments/<id>/results
         until complete, then paste its variant × metric table AND the control verdict line).
      - If NO fixture set applies (e.g. a brand-new step kind), write exactly: "no fixture set applies: <reason>".
        A stated reason is acceptable evidence — file the proposal anyway.
      Do NOT hand-wave this section. A proposal with an empty or missing `## Bench evidence` section is incomplete.
```

- [ ] Raise `max_turns` so the agent has room to compose the proposal, issue the two-field POST, and poll `GET .../results` until the experiment completes (launching + polling one experiment is several extra turns). In `defaults:` set `max_turns: 120` (was 60). **Do NOT** inflate the ticket budget to "fund the experiment": the experiment's cell spend is a **separate budget seat** — ordinary `source:'agent_step'` ledger rows attributed to the experiment via the cell join (A2 §1.12.3 — no bench/experiment ledger source exists; the CHECK forbids it; amended 2026-07-19 post-review) — resolved A2-side at admission (`agent_bench_experiments.budget_usd = 0` ⇒ default `$12` envelope, seated under the global daily cap — spec §3/§9), and the step creates no experiment cells itself (spec §3: "step cells create no tickets"). The `$12` envelope is **not** drawn from this ticket's budget — the two pools are distinct. Note further that the retrospective ticket is **platform-funded** (§1.13): plan 14 Task 18 Step 4 sets no `ticket_usd` override, so the workflow's `budget.ticket_usd` field may be inert for it. Leave `budget.ticket_usd` as plan 14 seeds it (`5.0`); if a modest bump for the agent's own reasoning turns is later shown to be honored and needed, size it to that in-step work only (compose review, POST, poll) — never to the experiment envelope. **`max_turns` is the load-bearing change here.**
- [ ] Copy the edited YAML byte-identically into `docs/agentic/retrospective-workflow.yml`.
- [ ] Run `go test ./agent/retrospective/` → green. Confirm the definition still parses under the §6 grammar (plan 14 Task 18's `TestSeedDefinitionParses` covers `Parse`).
- [ ] Commit: `git add agent/retrospective/seed_workflow.yml docs/agentic/retrospective-workflow.yml agent/retrospective/context_test.go && git commit -m "feat(retrospective): mandatory ## Bench evidence proposal section + room to launch one experiment"`

---

### Task 7: Trigger delivers the bench-evidence-augmented snapshot

Fold into plan 14 M2 **Task 19** (respect the precondition gate). The trigger already renders the intel snapshot via `RenderIntelMarkdown` and delivers it as the ticket spec (plan 14 Task 19, F10 fix). With Task 5 landed, the snapshot now carries bench guidance automatically — this task adds the assertion that closes the loop and confirms no other trigger behavior changed.

**Files:**
- Extend (fold-in delta during plan 14 Task 19): `agent/retrospective/trigger.go` — **NEW (plan-14-M2-owned)**: no behavioral change beyond consuming Task 5's richer markdown (it already calls `RenderIntelMarkdown`)
- Extend (fold-in delta during plan 14 Task 19): `agent/retrospective/trigger_test.go` — **NEW (plan-14-M2-owned)**: assert the delivered spec carries the bench-evidence guidance

**Steps:**

- [ ] Add to plan 14 Task 19's trigger spec (the one asserting `SubmitSpecCallCount() == 1` and that `spec.Body` contains the snapshot): also assert the delivered spec carries the bench-evidence contract, so a regression that drops the guidance is caught here:

```go
	It("delivers a spec that carries the bench-evidence requirement", func() {
		// ... existing arrange: fake intel Analyzer returns a snapshot with one recurring class ...
		Expect(trigger.RunOnce(ctx, "manual")).To(Succeed())
		Expect(ticketStore.SubmitSpecCallCount()).To(Equal(1))
		_, spec := ticketStore.SubmitSpecArgsForCall(0)
		Expect(spec.Body).To(ContainSubstring("## Bench evidence"))
		Expect(spec.Body).To(ContainSubstring("no fixture set applies"))
	})
```

- [ ] Confirm `trigger.go` needs **no** code change — it already renders via `RenderIntelMarkdown` and submits the result as the spec; the guidance flows through unchanged. (If plan 14's trigger instead composed the spec from parts, add the one line that includes the `RenderIntelMarkdown` output; do not fork the renderer.)
- [ ] Run `ginkgo ./agent/retrospective/` → green.
- [ ] Commit: `git add agent/retrospective/trigger_test.go && git commit -m "test(retrospective): assert delivered spec carries the bench-evidence requirement"`

---

### Task 8: Whole-track test sweep + retained-M2 non-regression

Confirm the retrospective amendment is green and the retained M2 tasks still compile/pass unchanged.

**Files:** none (verification only).

**Steps:**

- [ ] `go test ./agent/retrospective/` → green (context, seed-parse, bench-evidence guidance, seed-prompt, trigger specs).
- [ ] `make test-quick` (unit + ci-agent; PostgreSQL up per CLAUDE.md; never `--race`) → green — confirms the amendment did not perturb the retained `agent/intel` analytics suites or the `atc/db` factories.
- [ ] `go build ./... && cd agent && go build ./... && cd ..` → clean.
- [ ] Commit: none (verification task); if any fixup was needed, commit it with `fix(retrospective): …`.

---

### Task 9: Live retrospective bench-evidence smoke (theborg, THROWAWAY namespace)

Fake stores cannot exercise the agent actually calling A2's bench API and pasting a real results table. One live run confirms the closed loop (gated on A2 being live).

**Files:** none (live verification).

**Steps:**

- [ ] **Precondition:** A2's bench routes (`POST /api/v1/agent/bench/experiments`, `GET .../:id/results`) and at least one default `review` fixture set are live on theborg, and the `agent-platform-credential` secret exists in the target namespace (retrospective is platform-funded, §1.13). If A2 is not yet live, **defer this task** — record "deferred pending A2" in §11 and rely on Tasks 5–7's fake-backed golden coverage.
- [ ] In a **THROWAWAY** namespace (NOT `cicd`/`concourse`): import the amended `retrospective` workflow (`fly -t theborg agent workflows import`), then `fly -t theborg agent retrospective run`.
- [ ] Confirm one `origin:'retrospective'` ticket is filed and dispatches; on completion, confirm at least one **proposal** ticket the agent filed has a non-empty `## Bench evidence` section (either a real A2 results table or a stated no-fixture reason). Confirm the experiment's spend via the attribution join (amended 2026-07-19 post-review — no bench ledger source exists; spend lands as ordinary `source:'agent_step'` rows): sum the experiment's cells' `agent_run_metrics` costs through the `agent_bench_cells.pipeline_run_id → pipeline run → agent_run_metrics` join and confirm the total is ≤ the `$12` envelope — a budget seat distinct from the retrospective ticket's budget. Confirm nothing auto-merged (proposals are human-gated — the scope-out guarantee).
- [ ] `t.Cleanup`-style: close/dispose the throwaway tickets, delete any rendered pipelines, delete the namespace.
- [ ] Record the result (pass / deferred) in this doc's §11 log.

---

## Execution notes

**Test tiers.** The only code in this track is the two-file retrospective amendment (`context.go`, `seed_workflow.yml`) plus a trigger-test assertion, folded into plan 14 M2 Tasks 18–19. All of it is plain-`testing` / Ginkgo unit-level (`go test ./agent/retrospective/`), fast, no DB. `make test-quick` is the per-merge gate and also proves the retained M2 suites are undisturbed. Never pass `--race` (parallel compilation breaks, per CLAUDE.md).

**Code-task precondition.** Tasks 5–7 fold into plan 14 M2 Tasks 18–19 and only make sense once M2 is being executed (`agent/intel/` and `agent/retrospective/` must exist in the worktree — see the precondition gate above the code tasks). They carry their own fake-backed coverage and do not require A2 to be merged (the no-fixture escape hatch keeps the retrospective functional). Task 9 (live) is gated on A2.

**Ordering.** Tasks 1–4 (plan-14 banners) are pure docs and can land first and alone — they cost nothing and immediately stop anyone executing superseded M1 tasks. The single `00-shared-contracts.md §1.12` write is deferred to A1's Task 1 (owner assigned 2026-07-19 post-review) to avoid a two-writer merge (see the "Contract §1.12/§1.1/§11 addendum" Context subsection and the Open-decisions coordination item). Tasks 5–7 (retrospective code) come after M2 is under execution. Task 9 (live) is gated on A2.

**Retrospective evidence ↔ C two-tier promotion.** A proposal's `## Bench evidence` table is the fixture-tier discovery signal (A2 results). Promotion (`set-live` / merge of the proposal) is a human decision reading C's production tier alongside — a fixture-tier win is necessary, not sufficient (spec principle 7). This track produces the evidence; C renders both tiers; no code here reads C.

**No `render.go` touch, no migration.** This track allocates no migration number and never touches dispatch's render library or its refusal chain. The retrospective is an ordinary `agent:` step (plan 14 Task 18) rendered by the existing dispatcher.

## Basic-experience guardrail

The disposition must not inflate the spec's two-field minimal path (spec principle 6, §"The basic experience"). It does not:

- **The bench-evidence requirement reuses the exact two-field spec.** The retrospective agent runs `{"step_kind": "...", "variants": ["live", "<variant>"]}` — the *same* two fields a human types as `fly agent bench run --step review --variant review-prompts@v5`. Defaults supply fixtures, repetitions, evaluator, controls, and the `$12` budget envelope. The agent never specifies fixtures, holdout fraction, or evaluator versions.
- **Complexity is revealed, not required.** The proposal's `## Bench evidence` section *appears* only when evidence exists; the intel snapshot's guidance surfaces the results table when it is there, and never demands corpora/holdout/pinning knobs from the proposer.
- **The escape hatch keeps brand-new step kinds trivial.** "No fixture set applies: `<reason>`" is accepted evidence — a proposal for a step kind with no default fixture set is filed with one line, not blocked. Evidence is a prompt-level requirement, never a gate; proposals stay human-merged.
- **Capture stays free and curation stays defaulted** (spec principles 2, 6) — nothing in this track adds a per-fixture or per-experiment knob to the retrospective's path.

## Scope-out (do NOT implement)

- **Re-specifying A1/A2/B/C surfaces.** The fixture importer, the bench experiment tables/routes/runner, the score envelope, and the two-tier scorecard belong to their tracks. This track references them; it does not rebuild them.
- **A platform-mcp `run_bench_experiment` tool.** v1 uses direct principal-token HTTP (Open decisions — auth seam). The mcp tool is a declared v2 owned by A2/platform-mcp.
- **An exported proposal-shape checker.** A structural `HasBenchEvidence`-style helper (assert a `## Bench evidence` section carries a results table or a stated no-fixture reason) is **deferred** until an S-track reviewer surface actually consumes it (revealed complexity, spec principle 6). Spec §8 asks only that proposals *arrive with* evidence — a prompt-level requirement — not for a structural checker. The "section present" assertion the plan needs today is already covered by the `strings.Contains` goldens in Tasks 6–7; no exported, uncalled helper is built here.
- **Blocking proposals on evidence.** The bench-evidence requirement is prompt-level, not a gate — nothing here blocks a proposal for lacking a `## Bench evidence` section. Proposals stay human-merged (spec §8, plan 14 scope-out: nothing auto-merges).
- **Editing the retained M2 analytics tasks (12–17, 20).** They execute as written; this track only banners them retained.
- **Any migration.** None allocated; plan 14's `defect_link` keeps its own `1773106103`.
- **Workflow-cell (end-to-end) bench experiments as retrospective evidence.** Evidence is fixture-tier (step-cell) only — cheap, fast, bounded. End-to-end evidence would blow the agent's step budget and reintroduce plan 14's sample-starvation problem.

## Open decisions

- **[RESOLVED — auth seam] Retrospective→bench auth seam.** The retrospective agent calls A2's bench routes **directly over HTTP under its in-pod `AGENT_PRINCIPAL_TOKEN`** — no new platform-mcp verb in v1. Rationale: bench routes are principal-authenticable by construction (skeleton §5/§9); the retrospective step already runs with a principal token and an ATC base URL in-pod (the platform sidecar's `ATC_EXTERNAL_URL`/`AGENT_PRINCIPAL_TOKEN`, `agent/platformmcp/config.go`). `fly` may not be present in the pod, so the seed prompt carries the single HTTP recipe (Task 6), not a parallel `fly agent bench run` verb. A dedicated platform-mcp `run_bench_experiment`/`get_bench_results` tool pair is the ergonomic v2 (declared, not built) — it belongs to A2/platform-mcp, not this track; if A2 later adds it, the seed prompt swaps the recipe for the tool names with no other change. **Dependency:** Tasks 5–7 assume A2 has landed `POST /api/v1/agent/bench/experiments` (two-field spec `{step_kind, variants}`) and `GET .../:id/results` (variant × metric table + control verdicts). If A2 has not landed at execution time, Tasks 5–7 land the prompt/materializer text but their live-results assertions defer to A2 completion (the no-fixture escape hatch keeps the retrospective functional meanwhile; Task 9 is the gated live proof).
- **[RESOLVED — Task 6] Evidence tier.** Fixture-tier (step-cell) experiments only.
- **[OPEN — for A/S-8] Bench naming.** `agent_bench_*` / `/bench/` / `fly agent bench` are provisional (spec open item 9, skeleton open Q11). If S-8 renames the surface, this track's seed-prompt recipe and the disposition table's "bench owner" column swap names in one coordinated edit — no logic change.
- **[OPEN — for A2] Default fixture-set identity per `step_kind`.** Task 5's guidance names the *evaluators* per step kind (from spec §4's ladder), which are stable; the concrete default fixture *set* (recency window, per-repo caps, `hash mod N` holdout fraction — spec open item 4, skeleton open Q6) is A2's decision. The retrospective's two-field spec relies on A2 resolving "default open set for `step_kind`" at admission, so no fixture identity is hard-coded here.
- **[RESOLVED 2026-07-19 post-review — owner assigned] Contract §1.12 single-writer.** The §1.12 supersede banner + §1.1 registry annotation + §11 log entry this track contributes (see the "Contract §1.12/§1.1/§11 addendum" subsection under Context) land via **A1 (`bench/A1-fixture-capture.md`) Task 1**, whose checklist enumerates absorbing this track's (and E's) deferred §11 lines by name — not a second writer. Confirm at execution time that A1's Task-1 contract commit carries this text, and record the confirmation in this doc's §11 log. No file is edited by this track for that write.

## §11 Amendment log (this plan)

*(append-only; supersede by entry, not by edit)*

- 2026-07-19 (Track D initial): full supersede/retain disposition of plan 14's 20 tasks — M1 (Tasks 1-partial, 2–11) superseded by bench A1/A2/B (benchmark cases → `source:'benchmark'` fixtures; `agent_benchmark_cases` not built; `1773106100–102` reclaimed); M2 analytics (Tasks 12–17, 20) retained verbatim (incl. F9/F39 window fixes; the F10 spec-delivery fix is retained on retrospective Tasks 18–19; `defect_link` keeps plan-14-owned `1773106103`); retrospective (Tasks 18–19) retained with the spec-§8 bench-evidence requirement folded in (intel-snapshot guidance, mandatory `## Bench evidence` proposal section, fixture-tier-only, direct principal-token HTTP to A2, experiment spend on the experiment's own envelope not the ticket budget). Plan 14 §1.12.2 "runner dispatches tickets" + daily-cap admission retained verbatim by A2 for workflow cells. No migration; no `render.go` touch.
