# The Bench — program index (six-plan cross-track gate)

**Date:** 2026-07-19 · **Branch:** jetbridge · **Head migration (walk const):** `1773106090`
(`legacy_upgrade_test.go:37`) — **on-disk file head is `1773106091`** (`create_agent_settings`,
dispatcher runtime-toggle). The const lags the on-disk head by one (a pre-existing bug): the walk
migrates only to `090`, so `1773106091` is **un-exercised today** and gets pulled into the walk for
the first time when A1 bumps the const to `1773106100`. Confirm `091` applies cleanly then; a latent
`091` fault would surface red and must **not** be mis-attributed to the new `1773106100`.

These six plans decompose the 2026-07-19 agent-bench design (`docs/superpowers/specs/2026-07-19-agent-bench-design.md`)
around the FROZEN cross-track contract skeleton. They add the **inner loop** the outer improvement loop
lacks: production step executions become replayable **fixtures**; step-variants compete on fixed fixtures
under versioned **evaluators**; **scores** land two-tier (fixture tier discovers, production tier confirms).
Each plan was written to `superpowers:writing-plans`, opened with a Task-1 contract addendum, and carries
its own execution-level recommendation. This index records only the **inter-plan** constraints the plans
cannot see individually, the canonical **package map**, and the **residual-drift ledger** — reconciled by
the 2026-07-19 review round (see "Residual contract drift — post-review state" below).

## The six plans

| Plan | Descends from | Tasks | Complexity / Risk | Migration |
|---|---|---|---|---|
| [A1 — fixture capture](A1-fixture-capture.md) | spec §1/§2 + skeleton A1; supersedes plan 14 `agent_benchmark_cases` | 15 | M–L / Low–Medium | **1773106100** `agent_step_fixtures` |
| [A2 — replay + harness](A2-replay-harness.md) | spec §3/§6 + skeleton A2 + 11-dispatch + 14 §1.12.2 + 08 #40-stub | 19 | High / High | **1773106101** `agent_bench_experiments` + `agent_bench_cells` (one file) |
| [B — evaluators + corpus](B-evaluators-corpus.md) | spec §4/§5/§7 + skeleton §1–§4 + A1 + A2 | 16 | L / M | **1773106102** `agent_bench_scores` |
| [C — two-tier scorecards](C-scorecards-two-tier.md) | 13-scorecards (amends) + spec §7 + skeleton | 11 | M / Low–Medium | **none** (conditional index rides plan-13's `1773106111`) |
| [D — plan-14 disposition](D-14-disposition.md) | 14-process-intel (amends) + spec §8 | 9 | Low / Low | **none** (plan-14-owned `1773106103` defect_link retained) |
| [E — gateway disposition](E-gateway-disposition.md) | 10-gateway-mcp (unchanged) + spec §10 | 2 | trivial / low | **none** |

72 tasks total (A2 gained Task 6a — overlay write path + full-bundle pinning — in the 2026-07-19 review round).
The natural dispatch unit is the **plan-slice**, not the plan (A splits capture-first / harness; B splits
judge-factoring+scores / evaluators; D splits docs / retrospective-code; C is gated behind plan 13; E is doc-only).

## Migration spine (blocker — serialize, one per push, strictly ascending)

`1773106100` → `1773106101` → `1773106102`. **Ascending applied order == referential-dependency order**
(`agent_bench_cells.fixture_id` FKs `agent_step_fixtures`; `agent_bench_scores.cell_id` FKs `agent_bench_cells`),
so a lower number landing after a higher one breaks the FK. **Never push two of `100/101/102` in one push.**

- **A1 `1773106100` lands first and alone** (spec Sequencing — cheap, invisible, accrues data daily).
- Shared file: `atc/db/migration/legacy_upgrade_test.go:37` `jetbridgeHeadMigration` — bumped `100→101→102`,
  only-if-higher. The const reads `1773106090` today (one behind the on-disk `1773106091`); A1's bump to
  `100` sweeps `091` into the walk for the first time (see header).
- **C / D / E add no bench migration.** D's retained `agent_reviews.defect_link` keeps its own
  **plan-14-owned `1773106103`** (lands after the bench block, ascending; ALTERs only `agent_reviews`, no FK
  into any bench table). C's *conditional* covering index (only if auto experiment-resolution shows in
  slow-query logs) uses plan-13's reserved **`1773106111`** — never a bench number; preferred home is adding
  it to A2's `1773106101` with A2's sign-off.
- The `92–99` slots belong to delivery-outcomes' block and are untouched here.

## Package map (canonical, 2026-07-19 post-review — use these names EVERYWHERE)

| Path | Go package | Owner | Contents |
|---|---|---|---|
| `agent/api/fixtures` | `fixtures` | A1 | Fixture, Source, Split, InputRef, OutputRef, EnvPins, GroundTruthFault, FixtureFilter, Bundle, Store; PLUS the synthetic family moved here from B: InjectedFault, Loc, SyntheticFixture, SyntheticFixtureStore (A1's factory implements RegisterSynthetic natively) |
| `atc/db/agent_step_fixtures_factory.go` | `db` | A1 | implements fixtures.Store + fixtures.SyntheticFixtureStore |
| `agent/api/bench` | `bench` | A2 | Experiment, Cell, Matrix, Store, ScoreReader, FixtureSelector (Admit REMOVED from the consumer interface — admission is A2-owned in benchrunner) |
| `agent/benchstub` + `cmd/benchstub` | `benchstub` | A2 | replay stub binary (now absorbs+records POST /api/v1/agent/costs) |
| `agent/benchrunner` | `benchrunner` | A2 | polling runner, admission (Admit lives HERE), control synthesis, reconcile-time scoring call |
| `agent/benchrunner/replay` | `replay` | A2 | replay renderer (MOVED from agent/bench/replay) |
| `atc/db/agent_bench_factory.go` | `db` | A2 | experiments/cells |
| `agent/benchscore` | `benchscore` | B | ScoreEnvelope, Pins (RENAMED from agent/bench root; fixture.go types moved to agent/api/fixtures) |
| `agent/benchscore/eval` | `eval` | B | evaluators, EvalDeps, ScoreCell (MOVED from agent/bench/eval) |
| `agent/benchscore/corpus` | `corpus` | B | fault-injection corpus (MOVED from agent/bench/corpus) |
| `agent/api/benchcorpus` | `benchcorpus` | B | registration route handler (route now accepts source ∈ {synthetic, benchmark}) |
| `agent/harvest/judge` | `judge` | B | factored judge (unchanged) |
| `atc/db/agent_bench_scores_factory.go` | `db` | B | scores |

The `agent/bench/` directory NO LONGER EXISTS in any plan. The bare qualifier `bench.` now always
means A2's `agent/api/bench`.

## Hard cross-plan chokepoints (serialize; never run concurrently)

1. **Migration spine** *(blocker)* — see above. A1 → A2 → B ascending; one migration per push.
2. **`00-shared-contracts.md` single-writer** *(major)* — six writers touch the same doc: A1 (§1.15
   `agent_step_fixtures` + §1.1 registry reconcile + §1.12 SUPERSEDED banner + §11, **including absorbing D's and
   E's deferred §11 lines by name** — R3 resolved 2026-07-19), A2 (§1.12.3 + seven §4.2
   route rows + §11), B (§B-scores/§B-eval prose + §4.2 `RegisterSyntheticFixtures` + §11), C (§11
   supersede-by-entry + §4.2 `GetAgentWorkflowPromotion`), and D + E (**defer** their §11 / §1.12 lines to A1
   Task 1 — the named owner). Treat as **single-writer per merge window; append at the current tail, never at a
   pinned line.**
3. **`render.go` refusal chain** *(major cross-program; minor within bench)* — within the bench block **only
   A2** goes near it: `agent/benchrunner/replay.RenderReplay` reuses `dispatch.RenderAgentStep` **additively**, never
   calls `dispatch.Render`, never touches `render.go:152-212` (guard: `grep -rn "dispatch.Render(" agent/benchrunner/replay/`
   == 0). B's judge factoring keeps `render.go` *compiling* via `harvest.JudgeConfig = judge.Config` aliases
   (edits `agent/harvest`, not the chain). NB the chain is **also** contended by the remainders program
   (workflow-source / judge-evidence / platform-mcp) — A2 must re-grep at HEAD and preserve their edits, never
   patch cited line numbers.
4. **Fixture / score Go-type seams** *(resolved 2026-07-19 — the package map above is canonical)* — the contract
   handshakes between producers and consumers: A1 produces `agent/api/fixtures` (`Fixture`, `Store` incl.
   `List(FixtureFilter)`/`Tag`/`Bundle`/2-arg `Pin`, plus the synthetic family `InjectedFault`/`Loc`/
   `SyntheticFixture`/`SyntheticFixtureStore` — `RegisterSynthetic` implemented natively by A1's factory);
   A2 consumes them via `bench.Fixtures` + `fixtures.Bundle`/`FixtureFilter` (`Admit` removed from the consumer
   interface — admission lives in `agent/benchrunner`); B produces `benchscore.ScoreEnvelope`/`Pins`, and the
   score-write transport is A2's reconcile-time `eval.ScoreCell(ctx, reg, scoresFactory, cellID, …)` call — no
   `AGENT_BENCH_CELL_ID` env seam exists. Formerly Residual **R1/R4/R6** — all resolved/dissolved below.
5. **Six-touchpoint route surface** *(major)* — `atc/routes.go`, `atc/wrappa/api_auth_wrappa.go` (exhaustive
   switch; the `default: panic` fails web startup if any new route lacks an auth case), `atc/api/handler.go`,
   `fly/commands/agent.go`, `atc/atccmd/command.go`, `atc/component.go` are additively edited by A2 (7 routes +
   runner component + fixture-store/runner/budget flags), B (1 route), C (1 route + promotion fly verb) and A1
   (reaper component + fixture-store flags). Additive-merge; each re-greps the named symbol at HEAD, not a line.

## Recommended land order

- **Phase 0 (docs, cheap, first):** D's plan-14 banners (D Tasks 1–4) and E (doc-only) — they cost nothing and
  immediately stop anyone executing superseded plan-14 M1 tasks. (The former "B shared-types pre-slice" is GONE —
  2026-07-19 review: the synthetic family moved into A1's `agent/api/fixtures`, so B has no land-before-A1
  requirement and **A1 truly lands first**.)
- **Phase 1 (capture, first & alone):** A1 in full — migration `100`, both capture hooks, host-path store, reaper.
  Every day it runs it accrues the fixtures everything downstream consumes. Master switch (`--agent-fixture-store-dir`
  empty) makes the first deploy a no-op. **Accrual honesty (2026-07-19):** v1 captures are non-replayable
  (`input-bundle-partial`) and expire in 30d; the replayable corpus begins accruing only when A2's Task 6a deploys —
  plan the first bake-off ≥ a week after A2, or pin a starter set.
- **Phase 2 (harness):** A2 — migration `101`, replay renderer, the deployable `benchstub` binary, the polling
  runner + budget envelope, routes/fly, **plus Task 6a (overlay write path + full-bundle pinning — the slice that
  makes NEW captures land `replayable=true`)**. After A1's `100`.
- **Phase 3 (evaluators + scores):** B — migration `102`, judge factoring, precision/recall/composite/
  grounding evaluators, corpus; scored web-side at A2's reconcile (no evaluate step in v1). After A2's `101`
  (scores FK cells).
- **Phase 4 (scorecards):** C — after B's `102` (reads `agent_bench_scores`) **and** after plan 13 lands (hard
  prereq: C imports `agent/api/scorecards`, `db.NewScorecardStore`, route `GetAgentWorkflowScorecard`, none at HEAD).
  **Scheduling note (2026-07-19):** plan 13 is itself unlanded and unscheduled — not in this bench program nor the
  remainders set. Before dispatching C, either queue `13-scorecards.md` execution as a pre-Phase-4 slice of this
  program or explicitly park C until plan 13 is scheduled elsewhere; "after plan 13 lands" is not self-executing.
- **Phase 5 (disposition code):** D Tasks 5–7 fold into plan-14 M2 Tasks 18–19 — gated on plan-14 M2 being built
  in the worktree (`agent/intel/`, `agent/retrospective/`). D Task 9 (live smoke) gated on A2 being live.
- **E** proceeds in parallel on plan 10's own wave-3 track; it ships nothing here.

## What can loop concurrently

Greenfield pure-Go packages with no cross-plan file overlap, subject only to the shared Claude rate-limit window
(dispatch → settle → dispatch, no fan-out): A1's leaf `agent/api/fixtures` package (types / hash / blobstore / capturer
/ reaper — hermetic, `t.TempDir()`, no DB); A2's `agent/benchstub` + `cmd/benchstub`, `agent/benchrunner/replay` golden
renderer, and `agent/api/bench` domain types; B's `agent/benchscore` + `agent/benchscore/eval` + `agent/benchscore/corpus`
evaluator library; C's `agent/api/scorecards` fixture types + `atc/db`
`FixtureScorecardStore` (gated on plan 13); D's `agent/retrospective` prompt/context deltas (gated on plan-14 M2).
**Everything touching a migration, `00-shared-contracts.md`, `render.go`, the six route-surface files, or
`agent/harvest` is a serialized chokepoint** — not loopable.

## Residual contract drift — post-review state (reconciled 2026-07-19)

The pre-review residuals R1–R8 were reconciled in the 2026-07-19 review round. The package map above and the
plans' amended text (marked "(amended 2026-07-19 post-review)") are authoritative. Status:

**R1 — RESOLVED.** A1 renamed `agent/fixture` → `agent/api/fixtures` (package `fixtures`, matching the
`agent/api/outcomes` template), aligned `Pin` to the 2-arg form, and grew its `Store` to
`Insert`/`Get`/`List(FixtureFilter)`/`ListReplayable`/`ListExpired`/`Pin(id, pinned)`/`Tag`/`Bundle`/`Delete`
(+ the `FixtureFilter`/`Bundle` types). `Admit` was **removed** from A2's `bench.Fixtures` consumer interface —
admission + the default-set policy are A2-owned, implemented in `agent/benchrunner` atop A1's `List(FixtureFilter)`
(frozen v1 default-set policy in A2 §1.12.3: newest N=24 replayable open production fixtures per step_kind,
per-repo cap, 30d window). A2 compiles against A1 as written.

**R2 — RESOLVED.** C now consumes A2's real **6-column** key — `UNIQUE (experiment_id, fixture_id, variant,
variant_version, control_role, repetition)` (control_role IN the key). C's "raise as an A2 amendment" framing is
gone; its Task 6 control fixtures stay at rep ≥ 2 with the note "rep value arbitrary — the 6-col key permits any rep".

**R3 — RESOLVED.** A1 Task 1 enumerates absorbing both `D-14-disposition.md`'s §1.12 disposition pointer and
`E-gateway-disposition.md`'s null-disposition §11 line **by name** (E's line lands verbatim, owner "bench-A1 on
behalf of Track E"); post-land, one confirmation line goes in D's and E's own logs.

**R4 — DISSOLVED.** The synthetic family (`InjectedFault`/`Loc`/`SyntheticFixture`/`SyntheticFixtureStore`) moved
into A1's `agent/api/fixtures`; A1's factory implements `RegisterSynthetic` natively (`Source` from the request,
`Pinned=true`, `Split=AssignSplit(ContentHash)`, GroundTruth carried). B's `agent/benchscore` root keeps only
`ScoreEnvelope`/`Pins`, and the "B types land before A1" constraint is gone — A1 truly lands first.

**R5 — RESOLVED (package map).** B's packages renamed `agent/bench` → `agent/benchscore` (+ `/eval`, `/corpus`);
A2's replay renderer moved to `agent/benchrunner/replay`. The `agent/bench/` directory no longer exists in any
plan; the bare qualifier `bench.` always means A2's `agent/api/bench`. See the Package map above; final naming
still rides S-8.

**R6 — RESOLVED.** B's `Pins` gained `variant_version int`; `pins.variant` is the **bare name** (never
`name@version` — the §2 example is fixed); `Validate()` + the mandatory-pins tests cover it; `ScoreCell` stamps it
via `EvalInput.VariantVersion`. String parsing of `name@version` exists only at A2's fly CLI boundary.

**R7 — PARTIALLY RESOLVED.** The primary-metric map is frozen A2↔B: `{review: "precision", implement:
"judge_total" (interim), plan: "grounding", workflow: none}`, and `controls:auto` synthesizes controls only for
kinds with a registered evaluator (`workflow` → `control_status='none'`, reason `no-evaluator-for-workflow-kind`).
**Still open:** the implement **composite scalar** (and its per-metric tie-tolerance semantics) remains the one
A2↔B joint decision.

**R8 — STANDS, with DEC'd correction.** B's no-schema-change Q4 join (`fixture.build_id` disambiguator,
`metrics.ambiguous_findings`) and `DefaultEvaluator("workflow") == ""` remain documented, owner-approved decisions —
not drift. **Correction (2026-07-19):** the workflow-cell metrics projection is **A2-owned** — A2 Task 13 projects
`agent_run_metrics`/`agent_outcomes` by ticket into the same VariantRow.Metrics map for cells with `ticket_id`
(cost_usd, findings count, terminal status, merge outcome when present); B's disclaimer stands. Workflow-kind
experiments are viable via benchmark fixtures: B's registration route accepts `source ∈ {synthetic, benchmark}` and
any step_kind.

## 2026-07-19 review round

New items surfaced and dispositioned by the fable review:

- **R9/R10 — RESOLVED (A2 Task 6a — overlay write path + full-bundle pinning).** `fixtures.BlobStore` gains
  `PutOverlay(r, max) (ref, err)` + `ErrOverCap`; flag `--agent-fixture-overlay-max-bytes` (default 33554432); the
  `overlay-over-bound:<n>B` skip_reason write path; capture enrichment at agent/harvest step ingestion assembles the
  full restore bundle and passes `InputBundleComplete: true` so NEW captures land `replayable=true`. Historical
  `input-bundle-partial` rows are **excluded forever** (superseded by fresh captures within days). Overlays above
  the 512 KiB render bound restore via the fixture-store dir mounted into the replay pod.
- **R11 — RESOLVED (reconcile-time scoring).** Score-write transport: A2's `StepExecutor.Reconcile`, on terminal
  `ok`, calls `eval.ScoreCell` web-side with real stores (mirroring the platform's server-side ingestion pattern).
  The rendered replay plan is `[restore → step-under-test]` — no evaluate step in v1; the `AGENT_BENCH_CELL_ID`
  env var no longer exists; a `ScoreCell` error sets the cell `status='error'` with reason `evaluate-failed`
  (never `failed`). The LLM-judge evaluator rides a later rendered-step slice (judge-in-pod later).
  **Second amendment (same day): the v1 web-side tier covers `review`/`plan` ONLY.** The implement-composite
  embeds gates (`go build/test`) + the judge (shells `claude` with the platform credential) — pod-side work that
  must never run in the ATC web process. Implement cells run and record run-metrics but go `ok` unscored
  (annotated `implement scoring deferred`); implement auto-controls skip (`implement-scoring-deferred`, like
  workflow's); the frozen primaryMetric map is `{review: precision, plan: grounding, implement: none-in-v1,
  workflow: none}`. B still builds the implement-composite library in full (hermetic tests) — it is the
  judge-in-pod slice's body. **The first bake-off target is review — exactly the spec's sequencing.**
- **Still-open follow-ups:**
  - **Gateway-spend-in-replay visibility (A2 Open decision 7):** replay cost POSTs are absorbed + recorded by
    `benchstub` (never forwarded — no credential inside the isolation boundary), so gateway-mounting variants'
    provider spend is visible only in the absorbed-writes log, not the ledger, until a forwarding/credential design
    lands. Coordinate with plan 10 + budget before trusting cost metrics on gateway-mounting experiments.
  - **Holdout-confirmation UX:** the pre-promotion recipe (`fixtures: {split: holdout}` full-form) is documented in
    A2's execution notes; a PromotionView surface ("latest holdout-split experiment for these versions, or 'no
    holdout confirmation run'") is deferred (C Open decision 6).
  - **Judge-in-pod slice** (the largest named follow-up): renders the evaluator as a pod step and delivers, as
    one coherent set — implement-cell scoring (gates + judge + the R11 second amendment's deferral), the
    implement composite scalar (the one remaining A2↔B joint decision, R7), the pod-side cell-id env + score
    ingestion route, and the LLM-judge evaluator registration (B Task 14's workflow definition).
  - **Plan-13 scheduling** — see the Phase 4 note; "after plan 13 lands" is not self-executing.
  - **S-8 naming** — unchanged; all bench names remain provisional pending the naming-spine review.

## Owner decisions surfaced by the plans

A1: agent-step `step_kind`/`repo`/`base_sha` derivation (heuristic v1), reaper interval vs store-dir pressure,
`content_hash` bundle fields. A2: overlay mount-vs-inline bound (resolved 2026-07-19: store-dir mount above the
512 KiB render bound), ledger-source attribution (`agent_step`, no `'bench'` source — attribution via the cell
join, resolved), `contracttest`→`benchstub` delegation vs constant-share, gateway-spend-in-replay visibility
(Open decision 7). B: judge-factoring symbol boundary (file-move), Q4 fixture-`build_id` join (R8). C:
metric-polarity hint (deferred), single-experiment source selection, `evaluator-suspect` presentation,
holdout-confirmation surface (Open decision 6). D: retrospective→bench auth seam (direct principal-token HTTP,
resolved), fixture-tier-only evidence, the §1.12 single-writer handoff (resolved — A1 Task 1). E: gateway
image-build sequencing behind dev-mcp Task 13, the replay cost-route wiring (resolved — benchstub absorb-and-record,
A2 Task 5 + Open decision 7), the null-disposition §11 owner (resolved — A1 Task 1). **Bench naming**
(`agent_bench_*`, `/bench/`, `fly agent bench`, `agent/api/fixtures`, `agent/benchscore` vs `agent/api/bench`) is
provisional across all six plans pending the **S-8 naming-spine review** (open Q11) — a rename is one coordinated
pre-freeze amendment.
