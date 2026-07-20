# The Bench — Evaluators + Fault-Injection Corpus Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Descends-from:** `docs/superpowers/specs/2026-07-19-agent-bench-design.md` (§4 evaluators, §5 fault-injection corpus, §7 two-tier scorecards); the FROZEN cross-track contract skeleton (§1 DDL, §2 score envelope, §3 seam map, §4 migration registry); `docs/superpowers/plans/agentic-platform/00-shared-contracts.md`; sibling bench plans **A1** (`agent_step_fixtures` capture, migration `1773106100`) and **A2** (`agent_bench_experiments` + `agent_bench_cells`, migration `1773106101`, replay runner). This is bench **Track B**.

**Goal:** Ship the evaluator library and the score contract for the bench — the `agent_bench_scores` table + envelope, the judge factored out of `agent/harvest` so it is invocable without importing the harvest package, the review evaluator (precision via the six-verdict feedback join + recall via a fault-injection corpus), the implement evaluator (gates + judge + downstream-finding count), and deterministic plan grounding checks — all as **versioned** evaluators the replay runner (A2) invokes per cell.

**Architecture:** Evaluators are **workflow definitions** (spec principle 1: the bench adds only three new nouns — fixture, experiment, score — and an evaluator is *not* a fourth). B's contribution is the score envelope + `agent_bench_scores` table, plus the Go that implements the deterministic-evaluator tier — which is the body of a workflow-definition/task-step, not a standalone bench concept. A new `agent/harvest/judge` subpackage hoists `RunJudge` and its data types out of `agent/harvest` (a pure file-move — the coupling graph is already one-directional onto `agent/schema`), with thin `harvest.JudgeConfig = judge.Config` aliases so `atc.HarvestStep`, `atc/plan.go`, and `agent/dispatch/render.go` compile unchanged. This is **skeleton-mandated** (§3 item 3, open Q8); it gives the judge types a standalone, judge-only import surface (importable without `gates.go`/`runner.go`/`evidence.go`) — it does **not** make the whole bench harvest-free (the implement-composite evaluator still imports `agent/harvest` for the gate machinery — see Task 12). `agent/benchscore` owns the `ScoreEnvelope` type (the spec §4 shape). The frozen `ground_truth` shape (`InjectedFault`/`Loc`) and the synthetic-fixture write contract (`SyntheticFixture`/`SyntheticFixtureStore`) live in A1's `agent/api/fixtures` (amended 2026-07-19 post-review — moved out of B; A1's `atc/db` factory implements `RegisterSynthetic` natively) and are imported by `eval`/`corpus`/`benchcorpus` — B defines no fixture type. `agent/benchscore/eval` owns the `Evaluator` interface, an in-process registry keyed by `(name, version)`, and the concrete evaluators; `eval.ScoreCell` is the library entrypoint A2's benchrunner reconcile invokes after a cell's step-under-test completes — it runs the pinned evaluator, then persists one `agent_bench_scores` row via `db.AgentBenchScoresFactory`. **Where** the evaluate runs is pinned in Open Decision D8 (amended 2026-07-19 post-review: web-side deterministic tier v1 at A2's reconcile; judge-in-pod rides a later rendered-step slice) — B ships the library, not the runner. Labels are **joins** (principle 3): review precision joins `agent_reviews`/`agent_feedback` through the fixture's `build_id`; the implement evaluator joins downstream reviews through `ticket_id`. Recall needs ground truth the production tables cannot supply, so `agent/benchscore/corpus` builds it: a fault-injection **workflow** takes a clean merged diff, injects realistic bug classes (CI-context-first framing per the #23/#24 AUP lesson), and registers `source:'synthetic'` fixtures whose `ground_truth` lists the injected faults. The implementor-variance plan evaluator is a **declared later slice** — the seam is designed, the body is not built.

**Tech Stack:** Go (plain `testing` for `agent/benchscore/*` and `agent/api/benchcorpus` matching `agent/api/reviews`/`agent/api/outcomes`; Ginkgo/Gomega for `atc/db`, `atc/wrappa`, `atc/api/auth`, migration walk); PostgreSQL migration + `agent_reviews_factory.go`-recipe factory; the factored `claude` CLI judge (os/exec, `--dangerously-skip-permissions`); counterfeiter fakes; jessevdk/go-flags (fly); real-git-fixture tests for grounding checks (the `agent/harvest/workspace_test.go` recipe). **No Elm** (the bench web surface is the deliberately-later S-track).

**Tasks:** 16. **Complexity:** L. **Risk:** M (the judge factoring touches three cross-package callers via aliases; the corpus builder is an LLM step under the AUP-refusal risk; everything else is additive: one new table, new packages, one new route).

**Migrations (registry-disciplined, §4 of the skeleton):**
- **`1773106102`** — `agent_bench_scores` (this plan's only migration). `scores.cell_id → agent_bench_cells(id)` (A2's `1773106101`), whose `fixture_id → agent_step_fixtures(id)` (A1's `1773106100`). Numeric order == referential-dependency order == mandatory merge order: **A1 `100` → A2 `101` → B `102`**, strictly ascending, never two in one push (house spine rule). The on-disk migration **file** head is `1773106091` (`create_agent_settings`), so `102` is collision-free. **Grounding caveat (flag to A1/A2):** the migration-**walk** const `jetbridgeHeadMigration` in `atc/db/migration/legacy_upgrade_test.go:37` currently reads **`1773106090`** — it lags the on-disk file head `1773106091` (that file exists but the const was never bumped to it), so the Legacy Database Upgrade walk currently migrates only to `090` and `1773106091` is **un-exercised** by the walk. This is harmless for B: the const is monotonic (`set only if lower`), and A1 raising it to `100` applies every file `≤ 100` (091 included, exercised once). But whoever lands the first bench migration (A1) should confirm `091` applies cleanly in the walk since it is currently untested. B lands **after** A1 and A2; a lower number landing after a higher one would break the FK.
- **No `agent_feedback` migration.** Open Q4 (per-finding join determinism) is resolved *without* a schema change — see Task 1. This keeps B's footprint to the single frozen `1773106102` and honors principle 3 (the bench writes no columns onto label tables).

---

## Context

**Charter (spec §"Handoff briefs" B; skeleton Track B).** B owns EVALUATORS + the score contract. It descends from the superseded plan 14-M1 (its `agent_benchmark_cases`/`agent_experiments` are **not built** — A supersedes them) and reshapes plan 13's rollup only in that C now reads B's `agent_bench_scores`. B builds the machinery that turns a replayed candidate output into a **versioned, pinned score**.

Scope-in → task mapping (every scope item maps):

| scope_in item (skeleton Track B) | Tasks |
|---|---|
| (1) score envelope + `agent_bench_scores` table, evaluator-version-PINNED | 1 (contract), 2 (migration), 4 (envelope type), 5 (factory) |
| (2) factor `agent/harvest/judge.go` invocable from the bench without importing the whole harvest package; it IS the first implement evaluator; **do not build a second judge** | 1 (symbol set), 3 (factoring), 7 (judge evaluator wraps the factored `judge.RunJudge`) |
| (3) review evaluator — precision via the six-verdict feedback join; recall via the fault-injection corpus | 1 (join pin), 8 (precision), 9 (recall), 10 (corpus), 11 (synthetic-fixture registration route; corpus-workflow dispatch deferred to D3) |
| (4) fault-injection corpus builder (a workflow; AUP-vocabulary-aware; classes seed from recurring production findings — the leftward-migration engine) | 10 (library + classes + injector prompt), 11 (synthetic-fixture registration route); workflow-definition builder + dispatch deferred to D3 |
| (5) implement evaluator composition (gates + judge + downstream-review finding count) | 12 |
| (6) deterministic PLAN grounding checks (cited files/lines exist) | 13 |
| (7) implementor-variance plan evaluator — DECLARED LATER SLICE; design the seam, do NOT build it | 13 (seam + skip), 1 (declaration) |
| evaluators are versioned workflow definitions | 1, 6 (registry), 14 (workflow-definition wiring + version resolution) |
| the negative-control contract (identical ties, degraded loses) rests on evaluator determinism | 6 (determinism guarantee), 14 (determinism canary over a real evaluator), 15 (live) |

**Scope OUT (do not implement):**
- **The implementor-variance plan evaluator body** — spec §4 marks it "plan (later slice)". Task 13 lands the *seam* (interface + a recorded skip), not the k-implementor fan-out.
- **The replay runner, the `restore` pipeline rendering + the reconcile-time scoring wiring, the stub-ATC main, fixture blob mechanics, capture, splits/tags/retention, experiment tables + runner + budget admission, the `/experiments` + `/fixtures` route family, `fly agent bench run|results|fixtures`** — all **A** (A1 capture, A2 harness). B provides the `Evaluator` seam A2 calls and the `agent_bench_scores` factory A2 writes through; B does not own the experiment surface.
- **Two-tier scorecards / paired-on-fixture deltas / the promotion view** — **C**. C reads `agent_bench_scores` (this plan's table) read-only; its covering indexes ride in B's `1773106102`.
- **`agent_reviews.defect_link`** — retained by **D**/plan-14 on its own `~1773106103`; not a bench allocation.
- **The gateway** — **E**, zero bench coupling (spec §10).
- **A second judge.** The harvest judge, factored, *is* the implement evaluator's LLM component (spec §4). Building a parallel scorer is the explicit anti-goal.
- **Web UI.** API + fly first (spec §6); the bench web surface is the S-track.
- **Auto-promotion / evaluator auto-approval.** Evaluator versions are human-approved like workflow promotions (spec "Out of scope").

**Prior waves (assumed LANDED exactly as `00-shared-contracts.md` + the earlier plans' §11 addenda define):**
- **harvest-step** (wave 3): `agent/harvest/judge.go` — `RunJudge(ctx, cfg JudgeConfig, opts JudgeOpts) (*JudgeResult, error)` @ `judge.go:102`; `JudgeOpts` (`judge.go:45-50` — `ClaudePath`, `WorkDir`, `Diff`, `Timeout`), `JudgeResult`/`JudgeIssue` (`judge.go:29-42`/`:19-27`), `DefaultJudgeTimeout` (`judge.go:16`), `buildJudgePrompt` (`judge.go:205`), `extractJSON`/`judgeEnvelope`/`judgeVerdict` (`judge.go:54-97`), `judgeJSONBlockRe` (`judge.go:89`). `JudgeConfig`/`RubricDimension` + `JudgeConfig.Validate`/`RubricHash` @ `agent/harvest/policy.go:34-81`; `harvest.Config.Judge *JudgeConfig` @ `policy.go:98`. `RunGates(policy GatePolicy, workspaceDir string, events *schema.EventWriter) ([]GateOutcome, error)` @ `gates.go:57`, `GateOutcome` @ `gates.go:16-25`, `gateCommands` @ `gates.go:30-34`. The **only** non-stdlib import of `RunJudge` is `agent/schema` (`judge.go:13`) — the factoring is a file-move, not a logic change. `agent/api/reviews.Store.GetByBuild(buildID) ([]StoredReview, error)` (`types.go:138`) + `ListByTicket(ticketID) ([]StoredReview, error)` (`types.go:145`); `reviews.BuildReviewResponse`/`Finding`/`FindingFeedback` (`handler.go:39-67`); judge findings land in `agent_reviews.review` as `observations` `category:"judge"`, gate failures as `proven_issues` `category:"gate"`.
- **feedback** (wave 1): six verdicts `{accurate, false_positive, noisy, overly_strict, partially_correct, missed_context}` @ `agent/api/feedback/handler.go:11-19`; `agent_feedback` unique key `(repo, commit_sha, finding_id, reviewer)`; `atc/db.agentFeedbackFactory.GetByReview(repo, commit)` @ `agent_feedback_factory.go:58` (per-commit read; no `build_id`, no review FK — the Q4 gotcha).
- **agent-step** (wave 2): `agent/schema.JudgeScoreDimension {Name, Score, Max, Rationale}` @ `agent/schema/event_payloads.go:131`; `schema.EventCostRecord`; `budget.SourceHarvestJudge` @ `agent/budget/budget.go:28`.
- **workflow-store** (wave 4): `agent/workflow.Store` (`List`, `LiveVersions`, `Versions`, `Get`); an evaluator that is an LLM workflow resolves `(name, version)` through it (`agent/api/workflows/handler.go:60-88` `Summarize`).
- **dispatch** (wave 4): `RenderInput` (`agent/dispatch/render.go:37-52`), `RenderAgentStep` (`render.go:63`), `Render` (`render.go:152`, refusal chain `:152-212` — **never touched here**), `Ticket.ID <= 0` skips the terminal harvest step (`render.go:246`); `TemplateSaver`/`RunCreator` (`dispatch.go:58-65`).
- **A1 fixtures** (bench, LANDED before B): `agent_step_fixtures` (`1773106100`) with `source`, `step_kind`, `repo`, `ticket_id`, `build_id`, `plan_id`, `content_hash`, `split`, `input_ref`, `output_ref`, `ground_truth`; the `agent/api/fixtures` types, including the synthetic family (`InjectedFault`/`Loc`/`SyntheticFixture`/`SyntheticFixtureStore` — amended 2026-07-19 post-review: moved from B into A1); and the factory's native `RegisterSynthetic` implementation. B imports these types (see Task 1).
- **A2 harness** (bench, LANDED before B): `agent_bench_experiments` + `agent_bench_cells` (`1773106101`); the replay runner that renders `[restore → step-under-test]` (no evaluate step in v1 — amended 2026-07-19 post-review) and, at reconcile on terminal `ok`, calls `eval.ScoreCell` (B's entrypoint, Task 6) per cell; `GET /experiments/:id/results` projects B's `agent_bench_scores.metrics`.

**This plan PRODUCES (contract surface `agent-bench-scores` + `agent-bench-evaluators`):**
- `00-shared-contracts.md` **§B-scores** — `agent_bench_scores` DDL (`1773106102`); the frozen `ScoreEnvelope` JSON shape; `agent/benchscore.ScoreEnvelope` type; `atc/db.NewAgentBenchScoresFactory`.
- `00-shared-contracts.md` **§B-eval** — `agent/benchscore/eval.Evaluator` interface, the built-in evaluator name+version registry, `eval.ScoreCell` (the entrypoint A2's benchrunner reconcile calls; locus in D8, amended 2026-07-19 post-review), the per-finding review-precision join pin (Q4), the judge-factoring symbol set (Q8), the two evaluator-version namespaces (Go-constant deterministic tier vs workflow-definition LLM tier — both land in `agent_bench_scores.evaluator_version`; `review-precision v1` is NOT a workflow version), the implementor-variance later-slice seam. (The synthetic-fixture contract + the frozen ground-truth shape moved to A1's `agent/api/fixtures` — amended 2026-07-19 post-review; B consumes them by import.)
- `00-shared-contracts.md` **§4.2** — one additive route row `RegisterSyntheticFixtures` (owner: bench-B).

**This plan CONSUMES:**
- **harvest** — `RunJudge`/`RunGates` (factored), `reviews.Store.GetByBuild`/`ListByTicket`, the finding categories (`judge`/`gate`).
- **feedback** — the six verdicts, `agentFeedbackFactory.GetByReview`.
- **A1** — `agent_step_fixtures` rows (join keys `build_id`/`ticket_id`/`repo`; `output_ref`; `ground_truth`); the `agent/api/fixtures` types (`InjectedFault`/`Loc`/`SyntheticFixture`/`SyntheticFixtureStore` — amended 2026-07-19 post-review); and the `RegisterSynthetic` write path.
- **A2** — `agent_bench_cells(id)` (the score FK) and the runner that calls `eval.ScoreCell`.
- **agent-identity** — `CheckAgentAuthorizationHandler` tier + `UserNameFunc` for the one route.

**Anchor caveat:** line anchors were verified on branch `jetbridge` at the head recorded above. A1 and A2 land before B and shift anchors in `atc/routes.go`, `atc/wrappa/api_auth_wrappa.go`, `atc/api/handler.go`, and the migration head const. Treat anchors as "the location of the quoted symbol"; place additions adjacent to the A1/A2 additions named in each step (search for the named symbol).

### Basic-experience guardrail

The spec's simplicity contract (§"The basic experience") is that two fields declare an experiment and one table answers it. B is the "one table answers it" half, and it keeps the default path trivial by **revealing** evaluator complexity, never requiring it:

- **The default evaluator is implied by `step_kind`.** `eval.DefaultEvaluator(step_kind)` (Task 6) returns `review-precision` for review, `implement-composite` for implement, `plan-grounding` for plan. It returns **`""` for `workflow`** — workflow cells are end-to-end tickets scored off the existing outcome/scorecard tables (A2/C), **not** routed through `eval.ScoreCell`, so B registers no `workflow` evaluator and the default map has **no unbacked entry** (every name `DefaultEvaluator` returns is one `DefaultRegistry()` can resolve; see D4). A two-field spec (`step_kind`, `variants`) names **no** evaluator — for the kinds B scores (review/implement/plan), A2 resolves the default and B scores it. `evaluator:` in the spec is the *only* way to override, and it is never in the two-field path.
- **The default review evaluator is precision-only.** Precision needs *nothing* the user must supply — it is a pure join over labels that already exist (Task 8). **Recall** requires a synthetic corpus (Tasks 9–11); a fixture set with no `ground_truth` simply scores `recall` absent (the metric is omitted, not zero — an unmeasured metric is never a failing one). So `fly agent bench run --step review --variant v5` returns a precision/cost/turns table with zero setup; recall appears only once someone has built a corpus.
- **The score envelope's `metrics` map is the whole contract the default results table reads.** `metrics[k]` values are plain numbers (Task 4) so A2's `variant × metric` table and C's paired deltas read them positionally with no evaluator-specific knowledge. `verdicts`, `rationale_ref`, corpus recall, holdout, and evaluator pinning are all *there* (behind the envelope's optional fields and the `evaluator:` override) but never widen the default surface.
- **Determinism, not configuration, powers the controls.** The negative-control contract (identical variant ties, degraded loses) needs no user input — it rests on evaluators being deterministic given the same fixture (the determinism canary over a **real** deterministic evaluator, Task 14), so `controls: auto` is the default and the user configures nothing.

---

### Task 1: Wave-start contract addendum — B's slice of the bench contract skeleton

Formalize B's slice of the FROZEN skeleton **as plan prose** (this Task edits no contract file; it records the addenda a follow-up A0 contract commit will fold into `00-shared-contracts.md §11`, exactly as the exemplar's Task 1 does). Everything downstream in this plan builds against the decisions frozen here.

**Files:** none modified (prose task; the decisions below are the contract of record for B).

**Steps:**

- [ ] **`agent_bench_scores` DDL (migration `1773106102`)** — conform to skeleton §1.B verbatim. One row **per cell × evaluator**; `(evaluator_name, evaluator_version)` are columns (pin parts 1+2, principle 5), the remaining pins (`fixture_id`, `variant`, `rep`) are reachable via `cell_id`. `metrics JSONB` is `{name: number}`; `verdicts JSONB` is optional `[{name, score, max, rationale}]` (`schema.JudgeScoreDimension`-shaped); `status` is `ok|error` only; `cost_usd NUMERIC(12,6)` is the evaluator's own spend. `UNIQUE (cell_id, evaluator_name, evaluator_version)`. Two covering indexes (`_cell`, `_evaluator`) — C's fixture-tier rollup reads them, so **C claims no migration**. The full DDL is Task 2.

- [ ] **`rationale_ref` semantics — documented skeleton amendment (NOT a silent drift).** The frozen skeleton (§1.B) defines `rationale_ref TEXT` as "a blob handle when rationale exceeds the inline bound (blob://…, L-3 512KiB precedent)". B **extends** it: on `status='ok'` rows it is empty or a `blob://` handle (unchanged); on `status='error'` rows it carries the evaluator's **inline error reason** (e.g. `"rubric parse failed"` — see `ScoreCell`, Task 6, and the factory test, Task 5). This is a deliberate overload keyed on `status`: a consumer (A2's results view, C's rollup) MUST gate on `status` and only dereference `rationale_ref` as a blob URI when `status='ok'`. The frozen DDL has no numeric-free error column and `metrics` values are numbers only, so the status-gated `rationale_ref` is the honest home for the error string without a second migration. **Raise this as an amendment** in the A0 contract commit (below) so downstream never dereferences an error string as a blob handle.

- [ ] **Score envelope (the served projection; skeleton §2, FROZEN)** — the API projection of the score columns; **no redundant envelope blob column**. `pins` mandatory; `metrics` values are numbers only; `verdicts` entries conform to `schema.JudgeScoreDimension {name, score, max, rationale}` for judge-derived evaluators, open `[...]` for deterministic ones (gate pass/fail lists, grounding-check hits). Consumption contract: A2's `GET /experiments/:id/results` renders `metrics[k]` per `(variant × rep)`; C aggregates `metrics[k]` paired on `pins.fixture_id`. Neither consumer reads a label column off the fixture — labels arrive via joins (§3). B's `agent/benchscore.ScoreEnvelope` (Task 4) is this shape. **(Amended 2026-07-19 post-review — §2-envelope amendment):** `pins` carries `variant_version` — the mandatory set is `{evaluator_name, evaluator_version, fixture_id, variant, variant_version, rep}` — and `pins.variant` is the **bare workflow name**, never `name@version` (matches A2's name-only `variant` column; nothing parses `name@version` except A2's fly CLI boundary).

- [ ] **Score `status` taxonomy (resolves open Q12).** Scores use **`ok|error` only** — never `failed`. Rationale: "the variant did badly" is a low *number* inside an `ok` row (a valid measurement); `error` is reserved for the evaluator itself crashing, timing out, or getting un-parseable model output (spec §"Failure handling": evaluator failure is distinguished from a low score). Cells carry the richer `ok|failed|error|skipped-budget` (A2); scores never need `failed`. **Pinned.**

- [ ] **Judge-factoring symbol set (resolves open Q8).** New package **`agent/harvest/judge`**. Moved symbols (a file-move; the dossier confirms the graph is one-directional onto `agent/schema`): `RunJudge`, `Opts` (was `JudgeOpts`), `Result` (was `JudgeResult`), `Issue` (was `JudgeIssue`), `Config` (was `JudgeConfig`), `RubricDimension`, `Config.Validate`, `Config.RubricHash`, `DefaultJudgeTimeout`, and the unexported `buildJudgePrompt`/`extractJSON`/`judgeEnvelope`/`judgeVerdict`/`judgeJSONBlockRe`. `agent/harvest` keeps **type aliases** (`type JudgeConfig = judge.Config`, `type RubricDimension = judge.RubricDimension`, `type JudgeResult = judge.Result`, `type JudgeIssue = judge.Issue`, `type JudgeOpts = judge.Opts`, `const DefaultJudgeTimeout = judge.DefaultJudgeTimeout`) so `atc.HarvestStep.Judge *harvest.JudgeConfig` (`atc/steps.go:456`), `atc/plan.go:449`, `harvest.Config.Judge` (`policy.go:98`), `harvest_step.go:208-298`, and `agent/dispatch/render.go`'s `harvestJudge` converter compile **unchanged**. `RunJudge` gets **no** alias (a func alias is not a Go type-alias); `runner.go` (in-package) is edited to call `judge.RunJudge` directly. **Honest import contract (do not overclaim):** the bench's *judge* path imports `agent/harvest/judge` and never `agent/harvest` — that is what the factoring buys (a judge-only surface with no `gates.go`/`runner.go`/`evidence.go`). It does **not** make the `agent/benchscore/eval` package harvest-free: the implement-composite evaluator (Task 12, same `package eval`) imports `agent/harvest` for the gate machinery (`RunGates`/`GatePolicy`/`Gate`), so the whole `eval` package transitively compiles `agent/harvest`. That is the **one deliberate harvest edge**, acknowledged in Task 12; the Task 3 import guard protects only the `judge` subpackage's purity (it does not, and cannot, assert the bench never imports `agent/harvest`). **Pinned** — Task 3 executes it.

- [ ] **Per-finding review-precision join (resolves open Q4).** The gotcha: `agent_feedback` has no `build_id` and no review FK; its `GetByReview` join to `agent_reviews` is **per-commit**, and `agent_feedback.ticket_id` is written but never read back — so a commit reviewed by >1 build is ambiguous. **Resolution — no schema change; disambiguate through the fixture's `build_id`:** a review fixture is 1:1 with a captured review-step execution, so it carries the exact `build_id` of that review. The precision evaluator joins **fixture.`build_id` → `agent_reviews.build_id`** (`reviews.Store.GetByBuild`) to get *this review's* commit + finding-id set, then `agent_feedback` on `(repo, commit_sha, finding_id)` filtered to that set. This is deterministic per build; it needs no new column, adds no B migration, and honors principle 3 ("the bench writes no label rows"). **Documented v1 limit (house rule — visible, filterable, never silent):** when the *same commit* was reviewed by two builds whose nondeterministic judge assigned the *same* `finding_id` to *different* content, feedback cannot be attributed to a single build; such fixtures are counted and surfaced (the precision evaluator records `ambiguous_findings` in `metrics`), not dropped. This chose the "fixture disambiguator" option over "add `review_build_id` to `agent_feedback`" precisely to keep B at one migration and avoid a write-path change to the landed feedback handler. **Pinned** (superseding the skeleton's "likely add a deterministic key" with the equally-valid no-column resolution, recorded in Open Decisions D1).

- [ ] **Synthetic/benchmark fixture-registration contract (B's slice of the fixture surface) — types A1-owned, route B-owned (amended 2026-07-19 post-review).** A1 owns `agent_step_fixtures`, the *capture* (`source:'production'`) write path, **and** the shared types, all in A1's `agent/api/fixtures`:
  - `fixtures.SyntheticFixtureStore{ RegisterSynthetic(f SyntheticFixture) (id int, err error) }`, implemented **natively** by A1's `atc/db` fixtures factory (`Insert` with the request's `Source`, `Pinned=true` — retention-exempt, spec §2 — `Split=AssignSplit(ContentHash)`, `GroundTruth` carried).
  - **one** `fixtures.SyntheticFixture` row type (NOT a `SyntheticFixture`/`StoredSyntheticFixture` split): `{Repo, StepKind, BaseSHA, OverlayRef, ContentHash, GroundTruth []InjectedFault, Tags, Source, Pinned}`.
  - **`ground_truth` shape (FROZEN), defined ONCE in `agent/api/fixtures`:** `InjectedFault{class, location:Loc{file, line}, description}` — the injected-fault manifest the recall evaluator (Task 9) scores against; `class` ∈ the Task-10 taxonomy. `fixtures.InjectedFault`/`fixtures.Loc` are imported by `eval`, `corpus`, and `benchcorpus` (Task 6/10/11) — **no triplicated struct** on the exact shape the recall matcher and the registration route must agree on.
  This dissolves the former land-order constraint: B defines **no** fixture type, and nothing of B's must precede A1 (the old "shared `agent/bench` types land before A1" requirement is DELETED). B owns the *registration route* (`agent/api/benchcorpus`, Task 11), which accepts `source ∈ {synthetic, benchmark}` (default `synthetic`) and **any** `step_kind` (the forced `StepKind:'review'` is dropped) — benchmark import rides this route; A1 ships no importer. B tests against a `MemoryStore` implementing `fixtures.SyntheticFixtureStore`.

- [ ] **Evaluators are versioned workflow definitions (with a built-in deterministic tier).** Spec principle 1: an evaluator *is* a workflow definition. B honors this two ways: **LLM evaluators** (the judge) resolve `(evaluator_name, evaluator_version)` through `agent/workflow.Store` exactly like any workflow (Task 14) — when the judge tier lands as a rendered step it runs as a **pod agent step** (metered, budgeted, no platform credential in the web process; spec §4/§9 — deferred per D8 as amended). **Deterministic evaluators** (precision, recall, grounding, gate-count) are built-in Go with a **per-evaluator version constant** (`eval.Version(name)`, Task 6); they need only DB joins + the fixture blobs; their execution locus is A2's wiring choice — **pinned in Open Decision D8 (amended 2026-07-19 post-review: web-side deterministic tier v1 at A2's reconcile; judge-in-pod rides a later rendered-step slice)**. Both write score rows pinning `(name, version)`; a scorecard cannot silently compare across evaluator versions (principle 5). Bumping a deterministic evaluator's logic bumps its constant — the score history stays honest. **(Amended 2026-07-19 post-review):** two evaluator-version **namespaces** exist and never mix — the deterministic tier's versions are Go constants (`eval.Version(name)`), the LLM tier's versions are workflow-definition versions (`workflow.Store`); both land in `agent_bench_scores.evaluator_version`, and `review-precision v1` is NOT a workflow version.

- [ ] **Implement-evaluator composition formula (spec §4 row).** `implement-composite` = `gates` (deterministic, `RunGates` over the candidate workspace) **+** `judge` (the factored `RunJudge` score) **+** `downstream_findings` (count of `proven_issues + observations` on the fixture's downstream review, joined by `ticket_id` via `reviews.Store.ListByTicket`, where present; absent → metric omitted, not zero). Emits one envelope: `metrics{gates_passed, gates_total, judge_total, judge_max, downstream_findings, cost_usd, turns}`, `verdicts` = the judge dimensions. Task 12.

- [ ] **Review precision + recall definitions (spec §4 row).** `precision = accurate / (accurate + false_positive + noisy + overly_strict)` over joined six-verdict labels (`partially_correct` and `missed_context` are excluded from both numerator and denominator — they are neither a clean hit nor a clean false alarm; recorded separately as `partial`/`missed` counts). `recall = |caught ∩ injected| / |injected|` over a synthetic fixture's `ground_truth`, where a caught finding matches an injected fault by **same file AND line within ±`GroundTruthLineTolerance` (=3) AND compatible class** (Task 9). Default review evaluator is **precision-only** (guardrail); recall is a separate evaluator that requires `ground_truth`.

- [ ] **Plan grounding-check definition (spec §4 row).** `plan-grounding` (deterministic): parse the candidate plan's cited `file:line` references; `grounding = |citations resolving to an existing file with ≥ that many lines| / |citations|`; `dangling_citations` lists the misses. Mechanizes the human verifier prompts. Task 13.

- [ ] **Implementor-variance is a DECLARED LATER SLICE (spec §4 "plan (later slice)").** B lands the *seam*: `eval.Evaluator` is satisfiable by a future `plan-implementor-variance` evaluator (fix the plan, run k cheap implementors, score by mean *and variance* of downstream gates/judge). Task 13 registers its name with `Version = 0` and an implementation that returns `status:'error'`, `metrics{}`, `rationale:"implementor-variance evaluator is a declared later slice (spec §4)"` — visible, never a silent gap, never dispatched by default. Building the fan-out is out of scope.

- [ ] **One route added (six-touchpoint, §3.5 pattern):** `RegisterSyntheticFixtures` — `POST /api/v1/agent/bench/fixtures/synthetic`, auth tier `CheckAgentAuthorizationHandler` (team-less agent tier; also principal-authenticable so the corpus workflow — and a future supervisor — can call it, spec §9). **(Amended 2026-07-19 post-review):** the route accepts `source ∈ {synthetic, benchmark}` (default `synthetic`) and **any** `step_kind`; the route NAME is unchanged and provisional until S-8 (with `benchmark` accepted, the `Synthetic` in the name is a known misnomer — rename is one coordinated S-8 amendment). Handler in B-owned `agent/api/benchcorpus`. Task 11. B adds **no** `/experiments` or `/fixtures` list/tag/pin routes and **no** `fly agent bench run|results|fixtures` verbs (all A2); the `fly agent bench corpus` verb is B's, but it (and the corpus-workflow dispatch it orchestrates) is **deferred to the D3 slice** — Task 11 ships only the route + client (touchpoints 1-4), since the verb's dispatch→wait→read-artifact→POST flow has no concrete steps until D3.

- [ ] **Degraded-control synthesis (open Q9) is NOT B's.** A degraded control is a *variant* (truncated-context clone of the baseline workflow), synthesized by A2's harness — not an evaluator. B's only obligation is that evaluators are deterministic enough that a degraded variant reliably *loses*; the determinism canary over a real evaluator (Task 14) is B's half of that contract. Recorded so no one waits on B for degraded synthesis.

- [ ] **primaryMetric + control tie-tolerance (raised amendment, 2026-07-19 post-review; second amendment same day).** The control-verdict primary-metric map is frozen jointly with A2: `{review: "precision", plan: "grounding", implement: none in v1, workflow: none}`. **v1 web-side scoring covers `review`/`plan` only** — the implement-composite (Task 12) embeds gates + the judge, which are pod-side work; A2's v1 reconcile never invokes it, implement cells go `ok` unscored (annotated), and implement auto-controls skip (`implement-scoring-deferred`). B still BUILDS implement-composite (hermetic tests; the library is the pod slice's body) — only its production wiring waits for the judge-in-pod slice, at which point `implement→"judge_total"` is the interim pin and the composite scalar is the one remaining A2↔B joint decision. Per-metric tie-tolerance semantics for A2's control verdict (baseline-clone ties, degraded loses) are **jointly owned with A2** (A2 Open decisions 3/4 cross-ref); B's half of the contract remains evaluator determinism (Task 14's canary).

- [ ] Note for the A0 contract commit: fold the above into `00-shared-contracts.md §11` as amendment "bench-B: agent_bench_scores + score envelope + evaluator library + judge factoring + synthetic/benchmark fixture-registration route (types `fixtures.SyntheticFixtureStore`/`fixtures.SyntheticFixture`/`fixtures.InjectedFault`/`fixtures.Loc` are A1-owned in `agent/api/fixtures`) + review-precision fixture-build_id join (Q4, no schema change) + Q8/Q12 pins", and append the `RegisterSyntheticFixtures` row to §4.2. **Explicit skeleton amendments to raise here (not silent drift):** (1) `rationale_ref` also carries the inline error reason on `status='error'` rows (status-gated; §1.B extension); (2) `DefaultEvaluator("workflow")` returns `""` — the workflow-cell default is owned by A2/C off outcome tables, not registered by B (skeleton's §5-Q10 workflow-cell path; supersedes any read of the guardrail that had B own a `outcome-metrics` evaluator); and, raised 2026-07-19 post-review: (3) `pins` gain `variant_version` and `pins.variant` is the bare name; (4) the registration route generalizes to `source ∈ {synthetic, benchmark}` + any step_kind; (5) the two evaluator-version namespaces; (6) the frozen primaryMetric map (tie-tolerance jointly with A2). **Affects:** A1 (owns the fixture types incl. the synthetic family; its factory implements `RegisterSynthetic` natively — no land-order constraint), A2 (reconcile-time `eval.ScoreCell` call + results projection + D8 as amended + the workflow-cell owner), C (reads agent_bench_scores; must gate on `status` before dereferencing `rationale_ref`), D (benchmark import repoints at B's route; defect_link is separate).

---

### Task 2: Migration `1773106102` — `agent_bench_scores`

**Files:**
- Create: `atc/db/migration/migrations/1773106102_create_agent_bench_scores.up.sql`
- Create: `atc/db/migration/migrations/1773106102_create_agent_bench_scores.down.sql`
- Modify: `atc/db/migration/legacy_upgrade_test.go:37` (`jetbridgeHeadMigration` const)

**Steps:**

- [ ] **Write the up migration** (skeleton §1.B verbatim). Migration files are picked up via `go:embed migrations` — no registration code. The `REFERENCES agent_bench_cells(id)` FK requires A2's `1773106101` to be applied first (the ascending merge order):

```sql
CREATE TABLE agent_bench_scores (
    id                SERIAL PRIMARY KEY,
    cell_id           INTEGER NOT NULL REFERENCES agent_bench_cells(id) ON DELETE CASCADE,
    evaluator_name    TEXT NOT NULL,       -- evaluator IS a workflow definition (§4); pin part 1
    evaluator_version INTEGER NOT NULL,    -- pin part 2 (principle 5 — every score row version-pinned)
    metrics           JSONB NOT NULL,      -- {name: number}; scorecards aggregate over this map
    verdicts          JSONB,               -- optional [{name,score,max,rationale}] (JudgeScoreDimension-shaped)
    rationale_ref     TEXT NOT NULL DEFAULT '',   -- blob handle when rationale exceeds the inline bound
    status            TEXT NOT NULL DEFAULT 'ok'
                      CHECK (status IN ('ok','error')),  -- evaluator failure ⟹ error, NOT a low score (§Failure, Q12)
    cost_usd          NUMERIC(12,6) NOT NULL DEFAULT 0,  -- evaluator's own spend (LLM evaluators)
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (cell_id, evaluator_name, evaluator_version)
);

CREATE INDEX agent_bench_scores_cell      ON agent_bench_scores (cell_id);
CREATE INDEX agent_bench_scores_evaluator ON agent_bench_scores (evaluator_name, evaluator_version);
```

- [ ] **Write the down migration** `1773106102_create_agent_bench_scores.down.sql`:

```sql
DROP TABLE agent_bench_scores;
```

- [ ] **Bump the head const** in `atc/db/migration/legacy_upgrade_test.go:37` — set to `1773106102` **only if the current value is lower** (never lower it; A1/A2 will have set `1773106100`/`1773106101` before B). **Note the current on-branch value is `1773106090`** (it lags the on-disk file head `1773106091`); the "only if lower" rule self-heals — by the time B lands, A1's `100` has already swept `091` into the walk. Confirm the const reads `1773106101` (A2's value) before bumping to `102`; if it still reads `1773106090`/`1773106091`, A1/A2 have not landed and B must wait:

```go
const jetbridgeHeadMigration = 1773106102
```

- [ ] **Run to verify:** `pg_isready && ginkgo --focus='Legacy Database Upgrade' ./atc/db/migration/` — expect green (the suite migrates empty + fixture DBs to HEAD; a SQL syntax error, a missing down file, an FK to a not-yet-applied `agent_bench_cells`, or a stale head const fails here). If it fails with `relation "agent_bench_cells" does not exist`, A2's `1773106101` has not landed — B **must** merge after A2 (the merge-order hazard).
- [ ] **Commit:** `git add atc/db/migration && git commit -m "feat(bench): migration 1773106102 - agent_bench_scores table"`

---

### Task 3: Factor the judge into `agent/harvest/judge` (invocable from the bench)

This factoring is **skeleton-mandated** (§3 item 3; open Q8: "exact set of symbols moving to `agent/harvest/judge`") — B raises an amendment to change it, not a silent drop. Be honest about what it buys: `agent/harvest`'s only non-stdlib import is `agent/schema` (verified: `gates.go`/`runner.go`/`judge.go`/`evidence.go` each import only stdlib + `agent/schema`), so there is **no heavy dependency graph to isolate** — the `os/exec` git plumbing is stdlib. What the hoist *does* buy is a **judge-only import surface**: a consumer that needs only the judge (a pure `review-judge`, or a future scorer) imports `agent/harvest/judge` and compiles neither `gates.go` nor `runner.go` nor `evidence.go`, and the exported types shed the `Judge…` stutter. It does **not** make `agent/benchscore/eval` harvest-free — Task 12's implement-composite evaluator imports `agent/harvest` for `RunGates`/`GatePolicy`/`Gate`, so the whole `eval` package still compiles `agent/harvest` (the one deliberate edge). Keep aliases in `harvest` so no caller churns. **Pure file-move — no logic change** (Q8).

**Files:**
- Create: `agent/harvest/judge/judge.go` (moved from `agent/harvest/judge.go`)
- Create: `agent/harvest/judge/policy.go` (the judge-only types moved from `agent/harvest/policy.go`)
- Create: `agent/harvest/judge/judge_test.go` (moved from `agent/harvest/judge_test.go`)
- Modify: `agent/harvest/policy.go` (delete moved types; add aliases; keep `GatePolicy`/`Gate`/`Config`)
- Modify: `agent/harvest/judge.go` → delete (contents moved)
- Modify: `agent/harvest/runner.go` (call `judge.RunJudge`)
- Modify: `agent/harvest/policy_test.go`, `agent/harvest/runner_test.go` (retarget moved symbols via the aliases)

**Steps:**

- [ ] **Move `judge.go` → `agent/harvest/judge/judge.go`**, renaming the exported types to drop the stutter: `JudgeOpts`→`Opts`, `JudgeResult`→`Result`, `JudgeIssue`→`Issue`; `RunJudge`, `DefaultJudgeTimeout`, `buildJudgePrompt`, `extractJSON`, `judgeEnvelope`, `judgeVerdict`, `judgeJSONBlockRe` keep their names. Package clause becomes `package judge`. The import of `agent/schema` stays. `buildJudgePrompt(cfg JudgeConfig, ...)` → `buildJudgePrompt(cfg Config, ...)`.

- [ ] **Move the judge-only types from `policy.go` → `agent/harvest/judge/policy.go`:** `JudgeConfig`→`Config` (with its `Validate`/`RubricHash` methods) and `RubricDimension` (keeps its name). `package judge`. Leave `GatePolicy`, `Gate`, and the HARVEST_CONFIG `Config` (rename-collision note: `harvest.Config` and `judge.Config` are different packages — no collision) in `agent/harvest/policy.go`.

- [ ] **Add aliases + re-export to `agent/harvest/policy.go`** so every existing cross-package caller compiles unchanged:

```go
import judge "github.com/concourse/concourse/agent/harvest/judge"

// Judge types moved to agent/harvest/judge (bench-invocable, no harvest
// import). Aliases keep atc.HarvestStep.Judge, atc/plan.go, render.go, and
// harvest_step.go compiling against harvest.JudgeConfig unchanged.
type (
	JudgeConfig     = judge.Config
	RubricDimension = judge.RubricDimension
	JudgeResult     = judge.Result
	JudgeIssue      = judge.Issue
	JudgeOpts       = judge.Opts
)

const DefaultJudgeTimeout = judge.DefaultJudgeTimeout
```

Keep `harvest.Config.Judge *JudgeConfig` (`policy.go:98`) — it now resolves to `*judge.Config` via the alias, so `harvest_step.go:208-298` (`step.plan.Judge.Validate()`, `Judge: step.plan.Judge`) is untouched.

- [ ] **Edit `runner.go:150-154`** — the sole in-package `RunJudge` caller — to call the subpackage directly (aliases are for *cross-package* callers; in-package `runner.go` imports the subpackage):

```go
import judge "github.com/concourse/concourse/agent/harvest/judge"
// ...
res, jerr := judge.RunJudge(ctx, cfg.Judge, judge.Opts{
	ClaudePath: claudeCLI, WorkDir: workspaceDir, Diff: diff, Timeout: judgeTimeout,
})
```

(`cfg.Judge` is `*harvest.JudgeConfig` == `*judge.Config`; deref as the current code does.)

- [ ] **Delete `agent/harvest/judge.go`** (contents moved) and **move `judge_test.go` → `agent/harvest/judge/judge_test.go`** with `package judge` (or `judge_test`), retargeting `JudgeConfig`→`Config`, `JudgeOpts`→`Opts`, etc. Update `agent/harvest/policy_test.go` / `runner_test.go` references: they may keep using the `harvest.JudgeConfig` alias (no change) OR import `judge` — prefer leaving them on the alias to minimize churn, except any test that constructed `harvest.JudgeOpts{}` directly (retarget to `judge.Opts{}` since `RunJudge` moved).

- [ ] **Run:** `go build ./... && go test ./agent/harvest/... ./atc/exec/... -run 'Judge|Harvest'` and `go vet ./atc/... ./agent/dispatch/...` — expect green. The build is the real guard: a missed alias surfaces as `undefined: harvest.JudgeConfig` at `atc/steps.go:456` / `atc/plan.go:449` / `render.go`.
- [ ] **Verify the bench-invocability property directly** — add `agent/harvest/judge/import_test.go` asserting the package's own transitive imports do not include `agent/harvest` (a guard against re-coupling):

```go
package judge_test

import (
	"go/build"
	"testing"
)

func TestJudgePackageDoesNotImportHarvest(t *testing.T) {
	pkg, err := build.Import("github.com/concourse/concourse/agent/harvest/judge", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range pkg.Imports {
		if imp == "github.com/concourse/concourse/agent/harvest" {
			t.Fatalf("agent/harvest/judge must not import agent/harvest (re-coupling): %v", pkg.Imports)
		}
	}
}
```

- [ ] **Commit:** `git add agent/harvest atc && git commit -m "refactor(harvest): factor RunJudge into agent/harvest/judge subpackage (bench-invocable); alias in harvest keeps callers unchanged"`

---

### Task 4: `agent/benchscore` — the `ScoreEnvelope` type (FROZEN)

The envelope B produces and A2/C consume. **(Amended 2026-07-19 post-review:** the frozen `ground_truth` shape and the synthetic-fixture write contract moved to A1's `agent/api/fixtures` — B imports them and defines no fixture type; the former land-before-A1 constraint is DELETED, and A1 truly lands first.**)** Plain `testing`, matching `agent/api/reviews`.

**Files:**
- Create: `agent/benchscore/score.go` (the envelope)
- Test: `agent/benchscore/score_test.go`

**Steps:**

- [ ] **Write the failing test** `agent/benchscore/score_test.go`:

```go
package benchscore_test

import (
	"encoding/json"
	"testing"

	"github.com/concourse/concourse/agent/benchscore"
)

func TestScoreEnvelopeRoundTripAndMetricsAreNumbers(t *testing.T) {
	env := benchscore.ScoreEnvelope{
		Metrics: map[string]float64{"precision": 0.83, "recall": 0.71, "cost_usd": 0.42, "turns": 14},
		Verdicts: []benchscore.Verdict{{Name: "correctness", Score: 7.0, Max: 10.0, Rationale: "solid"}},
		RationaleRef: "blob://fixtures/x",
		Pins: benchscore.Pins{
			EvaluatorName: "review-judge", EvaluatorVersion: 3,
			FixtureID: 812, Variant: "review-prompts", VariantVersion: 5, Rep: 1,
		},
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	// metrics must serialize as a flat number map so A2's variant×metric
	// table and C's paired deltas read them positionally.
	var raw struct {
		Metrics map[string]json.Number `json:"metrics"`
		Pins    map[string]any         `json:"pins"`
	}
	dec := json.NewDecoder(bytesReader(b))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Metrics["precision"].Float64(); err != nil {
		t.Errorf("metric precision must be a number: %v", err)
	}
	for _, k := range []string{"evaluator_name", "evaluator_version", "fixture_id", "variant", "variant_version", "rep"} {
		if _, ok := raw.Pins[k]; !ok {
			t.Errorf("pins.%s is mandatory", k)
		}
	}
}

func TestValidateRejectsMissingPins(t *testing.T) {
	if err := (benchscore.ScoreEnvelope{Metrics: map[string]float64{"x": 1}}).Validate(); err == nil {
		t.Error("empty pins must be rejected (pins mandatory, principle 5)")
	}
	if err := (benchscore.ScoreEnvelope{
		Metrics: map[string]float64{"x": 1},
		Pins:    benchscore.Pins{EvaluatorName: "e", EvaluatorVersion: 1, FixtureID: 1, Variant: "v", VariantVersion: 1, Rep: 1},
	}).Validate(); err != nil {
		t.Errorf("valid envelope rejected: %v", err)
	}
}
```

(`bytesReader` is a one-line `bytes.NewReader` helper in the test file.)

- [ ] **Run** `go test ./agent/benchscore/` — expect compile failure (package does not exist).
- [ ] **Write `agent/benchscore/score.go`:**

```go
// Package benchscore holds the bench score contract (spec §4): the score
// envelope evaluators produce and A2's results view / C's scorecards
// consume. Metrics values are numbers only, so both consumers read them
// positionally. Labels are joins (principle 3): nothing here copies a
// verdict/outcome off a fixture.
package benchscore

import (
	"encoding/json"
	"fmt"
)

// Verdict is one scored dimension, shaped like schema.JudgeScoreDimension
// so judge-derived evaluators map straight through. Deterministic
// evaluators leave Verdicts nil.
type Verdict struct {
	Name      string  `json:"name"`
	Score     float64 `json:"score"`
	Max       float64 `json:"max"`
	Rationale string  `json:"rationale,omitempty"`
}

// Pins are the mandatory provenance of a score (principle 5): every score
// row pins its evaluator name+version and the cell coordinates it scored.
// (Amended 2026-07-19 post-review — R6): Variant is the BARE workflow name,
// never "name@version"; VariantVersion carries the integer.
type Pins struct {
	EvaluatorName    string `json:"evaluator_name"`
	EvaluatorVersion int    `json:"evaluator_version"`
	FixtureID        int    `json:"fixture_id"`
	Variant          string `json:"variant"`
	VariantVersion   int    `json:"variant_version"`
	Rep              int    `json:"rep"`
}

// ScoreEnvelope is the spec §4 shape (FROZEN). It is the API projection of
// an agent_bench_scores row: Metrics/Verdicts/RationaleRef/Pins map to
// columns; there is no redundant envelope blob column.
type ScoreEnvelope struct {
	Metrics      map[string]float64 `json:"metrics"`
	Verdicts     []Verdict          `json:"verdicts,omitempty"`
	RationaleRef string             `json:"rationale_ref,omitempty"`
	Pins         Pins               `json:"pins"`
}

func (e ScoreEnvelope) Validate() error {
	p := e.Pins
	if p.EvaluatorName == "" || p.EvaluatorVersion <= 0 || p.FixtureID <= 0 || p.Variant == "" || p.VariantVersion <= 0 || p.Rep <= 0 {
		return fmt.Errorf("bench: score envelope pins are mandatory and must be set: %+v", p)
	}
	if e.Metrics == nil {
		return fmt.Errorf("bench: score envelope metrics must be non-nil")
	}
	return nil
}

// VerdictsJSON returns the verdicts column payload (nil when none).
func (e ScoreEnvelope) VerdictsJSON() (json.RawMessage, error) {
	if len(e.Verdicts) == 0 {
		return nil, nil
	}
	return json.Marshal(e.Verdicts)
}
```

- [ ] **Run** `go test ./agent/benchscore/` — expect pass.
> **Moved (2026-07-19 post-review):** the ground-truth + synthetic-fixture types formerly drafted here (`InjectedFault`/`Loc`/`SyntheticFixture`/`SyntheticFixtureStore`) are **A1-owned** in `agent/api/fixtures`; A1's `atc/db` factory implements `RegisterSynthetic` natively. B imports them (Tasks 6/10/11) and defines no fixture type — no land-order constraint remains.

- [ ] **Commit:** `git add agent/benchscore && git commit -m "feat(bench): benchscore.ScoreEnvelope - the frozen score envelope (pins incl. variant_version)"`

---

### Task 5: `atc/db` — `AgentBenchScoresFactory` (persist envelopes, evaluator-pinned upsert)

Follows the `agent_reviews_factory.go` recipe (squirrel `psql`, `ON CONFLICT` upsert, epoch scan). Backs `eval.ScoreCell`; read by A2's results route and C's rollup.

**Files:**
- Create: `atc/db/agent_bench_scores_factory.go`
- Create: `atc/db/dbfakes/fake_agent_bench_scores_factory.go` (generated)
- Test: `atc/db/agent_bench_scores_factory_test.go`

**Steps:**

- [ ] **Write the failing spec** `atc/db/agent_bench_scores_factory_test.go` (Ginkgo, matching `agent_reviews_factory_test.go`; the suite seeds an experiment + a cell + a fixture via raw inserts so the FK holds):

```go
package db_test

import (
	"github.com/concourse/concourse/agent/benchscore"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentBenchScoresFactory", func() {
	var factory db.AgentBenchScoresFactory
	var cellID int

	BeforeEach(func() {
		factory = db.NewAgentBenchScoresFactory(dbConn)
		_, err := dbConn.Exec("DELETE FROM agent_bench_scores")
		Expect(err).NotTo(HaveOccurred())
		// minimal FK chain: fixture (100) -> cell (101) -> score (102)
		var fixtureID int
		Expect(dbConn.QueryRow(`INSERT INTO agent_step_fixtures
			(source, step_kind, repo, content_hash, input_ref)
			VALUES ('production','review','r','h','{}'::jsonb) RETURNING id`).Scan(&fixtureID)).To(Succeed())
		var expID int
		Expect(dbConn.QueryRow(`INSERT INTO agent_bench_experiments
			(name, step_kind, spec) VALUES ('e','review','{}'::jsonb) RETURNING id`).Scan(&expID)).To(Succeed())
		Expect(dbConn.QueryRow(`INSERT INTO agent_bench_cells
			(experiment_id, fixture_id, variant, repetition)
			VALUES ($1,$2,'live',1) RETURNING id`, expID, fixtureID).Scan(&cellID)).To(Succeed())
	})

	env := func(name string, ver int, prec float64) benchscore.ScoreEnvelope {
		return benchscore.ScoreEnvelope{
			Metrics: map[string]float64{"precision": prec, "cost_usd": 0.1},
			Verdicts: []benchscore.Verdict{{Name: "d", Score: 7, Max: 10, Rationale: "ok"}},
			Pins:    benchscore.Pins{EvaluatorName: name, EvaluatorVersion: ver, FixtureID: 1, Variant: "live", VariantVersion: 1, Rep: 1},
		}
	}

	It("upserts one row per (cell, evaluator_name, evaluator_version)", func() {
		Expect(factory.Upsert(cellID, env("review-precision", 1, 0.80), "ok", 0.1, "")).To(Succeed())
		// same evaluator+version overwrites (re-score), not duplicates
		Expect(factory.Upsert(cellID, env("review-precision", 1, 0.90), "ok", 0.1, "")).To(Succeed())
		rows, err := factory.ByCell(cellID)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].Metrics["precision"]).To(BeNumerically("~", 0.90, 1e-9))
		Expect(rows[0].Status).To(Equal("ok"))
		Expect(rows[0].Verdicts).To(HaveLen(1))
	})

	It("keeps distinct evaluator versions as distinct rows", func() {
		Expect(factory.Upsert(cellID, env("review-precision", 1, 0.80), "ok", 0.1, "")).To(Succeed())
		Expect(factory.Upsert(cellID, env("review-precision", 2, 0.85), "ok", 0.1, "")).To(Succeed())
		rows, _ := factory.ByCell(cellID)
		Expect(rows).To(HaveLen(2))
	})

	It("records an error score distinct from a low score", func() {
		// status='error' rows carry the inline error reason in rationale_ref
		// (the documented §1.B amendment — Task 1); consumers gate on status.
		Expect(factory.Upsert(cellID, env("review-judge", 3, 0), "error", 0, "rubric parse failed")).To(Succeed())
		rows, _ := factory.ByCell(cellID)
		Expect(rows[0].Status).To(Equal("error"))
		Expect(rows[0].RationaleRef).To(Equal("rubric parse failed"))
	})

	It("reads by evaluator for C's rollup", func() {
		Expect(factory.Upsert(cellID, env("review-precision", 1, 0.80), "ok", 0.1, "")).To(Succeed())
		rows, err := factory.ByEvaluator("review-precision", 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].CellID).To(Equal(cellID))
	})
})
```

- [ ] **Run** `ginkgo --focus='AgentBenchScoresFactory' ./atc/db/` — expect compile failure.
- [ ] **Write `atc/db/agent_bench_scores_factory.go`:**

```go
package db

import (
	"database/sql"
	"encoding/json"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/agent/benchscore"
)

//counterfeiter:generate . AgentBenchScoresFactory

// AgentBenchScore is a persisted score row (agent_bench_scores, §1.B).
type AgentBenchScore struct {
	ID               int
	CellID           int
	EvaluatorName    string
	EvaluatorVersion int
	Metrics          map[string]float64
	Verdicts         []benchscore.Verdict
	RationaleRef     string
	Status           string
	CostUSD          float64
	CreatedAt        int64
}

// AgentBenchScoresFactory persists and reads score envelopes. Upsert is
// keyed on (cell_id, evaluator_name, evaluator_version) — re-scoring the
// same cell with the same evaluator version overwrites; a new version is a
// new row (principle 5).
type AgentBenchScoresFactory interface {
	Upsert(cellID int, env benchscore.ScoreEnvelope, status string, costUSD float64, rationaleRef string) error
	ByCell(cellID int) ([]AgentBenchScore, error)
	ByEvaluator(name string, version int) ([]AgentBenchScore, error)
}

type agentBenchScoresFactory struct {
	conn DbConn
}

func NewAgentBenchScoresFactory(conn DbConn) AgentBenchScoresFactory {
	return &agentBenchScoresFactory{conn: conn}
}

func (f *agentBenchScoresFactory) Upsert(cellID int, env benchscore.ScoreEnvelope, status string, costUSD float64, rationaleRef string) error {
	metrics, err := json.Marshal(env.Metrics)
	if err != nil {
		return err
	}
	verdicts, err := env.VerdictsJSON()
	if err != nil {
		return err
	}
	_, err = psql.Insert("agent_bench_scores").
		Columns("cell_id", "evaluator_name", "evaluator_version", "metrics", "verdicts", "rationale_ref", "status", "cost_usd").
		Values(cellID, env.Pins.EvaluatorName, env.Pins.EvaluatorVersion, metrics, verdicts, rationaleRef, status, costUSD).
		Suffix(`ON CONFLICT (cell_id, evaluator_name, evaluator_version) DO UPDATE SET
			metrics = EXCLUDED.metrics, verdicts = EXCLUDED.verdicts,
			rationale_ref = EXCLUDED.rationale_ref, status = EXCLUDED.status,
			cost_usd = EXCLUDED.cost_usd, created_at = now()`).
		RunWith(f.conn).Exec()
	return err
}

func (f *agentBenchScoresFactory) query(pred sq.Sqlizer) ([]AgentBenchScore, error) {
	rows, err := psql.Select(
		"id", "cell_id", "evaluator_name", "evaluator_version",
		"metrics", "verdicts", "rationale_ref", "status", "cost_usd",
		"extract(epoch from created_at)::bigint",
	).From("agent_bench_scores").Where(pred).OrderBy("id ASC").RunWith(f.conn).Query()
	if err != nil {
		return nil, err
	}
	defer Close(rows)
	var out []AgentBenchScore
	for rows.Next() {
		var s AgentBenchScore
		var metricsRaw []byte
		var verdictsRaw sql.NullString
		if err := rows.Scan(&s.ID, &s.CellID, &s.EvaluatorName, &s.EvaluatorVersion,
			&metricsRaw, &verdictsRaw, &s.RationaleRef, &s.Status, &s.CostUSD, &s.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(metricsRaw, &s.Metrics); err != nil {
			return nil, err
		}
		if verdictsRaw.Valid && verdictsRaw.String != "" {
			_ = json.Unmarshal([]byte(verdictsRaw.String), &s.Verdicts)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (f *agentBenchScoresFactory) ByCell(cellID int) ([]AgentBenchScore, error) {
	return f.query(sq.Eq{"cell_id": cellID})
}

func (f *agentBenchScoresFactory) ByEvaluator(name string, version int) ([]AgentBenchScore, error) {
	return f.query(sq.Eq{"evaluator_name": name, "evaluator_version": version})
}
```

(`psql`, `DbConn`, `Close` are existing `atc/db` package symbols — match the exact names used in `agent_reviews_factory.go`; adjust if that file uses a different `Close`/conn helper.)

- [ ] **Run** `ginkgo --focus='AgentBenchScoresFactory' ./atc/db/` — expect pass.
- [ ] **Generate the fake:** `cd atc/db && go run github.com/maxbrunsfeld/counterfeiter/v6 -o dbfakes/fake_agent_bench_scores_factory.go . AgentBenchScoresFactory && cd ../..` then `go build ./atc/...`.
- [ ] **Commit:** `git add atc/db && git commit -m "feat(bench): AgentBenchScoresFactory - evaluator-pinned score upsert + reads"`

---

### Task 6: `agent/benchscore/eval` — `Evaluator` interface, registry, `ScoreCell`, registry plumbing tests

The library seam A2's benchrunner reconcile calls per cell (execution locus pinned in Open Decision D8, amended 2026-07-19 post-review: web-side deterministic tier v1). **The caller is A2's benchrunner reconcile — `ScoreCell` takes the cell id as a Go parameter; nothing parses env vars.** `Evaluator` turns `(fixture, candidate output)` into a `ScoreEnvelope`; the registry resolves the pinned `(name, version)`; `ScoreCell` runs it and persists the row. Deterministic evaluators carry a version constant. This task exercises the **registry/`EvaluatorFunc` plumbing**; the real **determinism canary** (over a concrete deterministic evaluator — B's half of the negative-control contract) lands in Task 14, where the evaluators exist. Plain `testing`.

**Files:**
- Create: `agent/benchscore/eval/evaluator.go` (interface, `EvalInput`, `EvalDeps`, registry, `DefaultEvaluator`, `Version`)
- Create: `agent/benchscore/eval/run.go` (`ScoreCell`)
- Create: `agent/benchscore/eval/echo_test.go` (registry + `EvaluatorFunc` plumbing tests; the real determinism canary is Task 14)

**Steps:**

- [ ] **Write the failing test** `agent/benchscore/eval/echo_test.go`:

```go
package eval_test

import (
	"context"
	"testing"

	"github.com/concourse/concourse/agent/benchscore"
	"github.com/concourse/concourse/agent/benchscore/eval"
)

func TestDefaultEvaluatorPerStepKind(t *testing.T) {
	cases := map[string]string{
		"review": "review-precision", "implement": "implement-composite",
		"plan": "plan-grounding",
	}
	for kind, want := range cases {
		if got := eval.DefaultEvaluator(kind); got != want {
			t.Errorf("DefaultEvaluator(%q) = %q, want %q", kind, got, want)
		}
	}
	// workflow cells are end-to-end tickets scored off outcome tables (A2/C),
	// NOT routed through eval.ScoreCell — B registers no workflow evaluator, so
	// the default returns "" (no unbacked entry in the default map). (D4)
	if got := eval.DefaultEvaluator("workflow"); got != "" {
		t.Errorf("DefaultEvaluator(\"workflow\") = %q, want \"\" (owned by A2/C, not B)", got)
	}
}

func TestRegistryResolvesVersionedEvaluator(t *testing.T) {
	// a deterministic evaluator's built-in version is pinned onto its scores.
	reg := eval.NewRegistry()
	reg.Register("echo", 1, eval.EvaluatorFunc(func(_ context.Context, in eval.EvalInput) (benchscore.ScoreEnvelope, error) {
		return benchscore.ScoreEnvelope{Metrics: map[string]float64{"echo": float64(in.Fixture.ID)}}, nil
	}))
	ev, ver, err := reg.Resolve("echo", 0) // 0 = "the current built-in version"
	if err != nil || ver != 1 {
		t.Fatalf("resolve current: ver=%d err=%v", ver, err)
	}
	env, err := ev.Evaluate(context.Background(), eval.EvalInput{Fixture: eval.Fixture{ID: 42}})
	if err != nil || env.Metrics["echo"] != 42 {
		t.Fatalf("evaluate: %+v err=%v", env, err)
	}
}

// This checks only the registry/EvaluatorFunc PLUMBING — that Resolve returns
// the registered func and it is invoked consistently. It is NOT the
// negative-control determinism guarantee: a fake echo func is deterministic by
// construction, so this test would pass even if a real evaluator became
// nondeterministic. The real determinism canary — over a concrete
// deterministic evaluator (plan-grounding), asserting byte-identical metrics —
// lands in Task 14, where the evaluators exist.
func TestRegistryInvokesEvaluatorConsistently(t *testing.T) {
	reg := eval.NewRegistry()
	reg.Register("echo", 1, eval.EvaluatorFunc(func(_ context.Context, in eval.EvalInput) (benchscore.ScoreEnvelope, error) {
		return benchscore.ScoreEnvelope{Metrics: map[string]float64{"echo": float64(len(in.Candidate.ResultsJSON))}}, nil
	}))
	ev, _, _ := reg.Resolve("echo", 1)
	in := eval.EvalInput{Candidate: eval.Candidate{ResultsJSON: []byte(`{"a":1}`)}}
	a, _ := ev.Evaluate(context.Background(), in)
	b, _ := ev.Evaluate(context.Background(), in)
	if a.Metrics["echo"] != b.Metrics["echo"] {
		t.Fatal("registry must invoke the same registered func on each call")
	}
}
```

- [ ] **Run** `go test ./agent/benchscore/eval/` — expect compile failure.
- [ ] **Write `agent/benchscore/eval/evaluator.go`:**

```go
// Package eval owns the bench evaluator library (spec §4): the Evaluator
// seam A2's benchrunner reconcile calls per cell (execution locus per plan
// D8, amended 2026-07-19: web-side deterministic tier v1), a
// name+version registry (evaluators are versioned — principle 5), and the
// concrete evaluators. The judge runs the factored agent/harvest/judge (a
// judge-only import surface). NOTE: this package DOES import agent/harvest —
// implement.go needs RunGates/GatePolicy/Gate for the gate machinery (the one
// deliberate harvest edge). The judge factoring keeps the JUDGE path
// harvest-free, not the whole package.
package eval

import (
	"context"
	"fmt"

	"github.com/concourse/concourse/agent/api/fixtures"
	"github.com/concourse/concourse/agent/benchscore"
)

// Fixture is the subset of an agent_step_fixtures row an evaluator reads
// (join keys + refs). A2 supplies it from the cell's fixture; B never
// queries the fixtures table directly (A1 owns it).
type Fixture struct {
	ID          int
	StepKind    string
	Repo        string
	BuildID     int             // review precision joins agent_reviews on this (Q4)
	TicketID    int             // implement downstream-findings joins reviews on this
	CommitSHA   string          // resolved from agent_reviews via BuildID; may be ""
	BaseSHA     string
	GroundTruth []InjectedFault // synthetic fixtures only (recall)
}

// Candidate is the replayed step's captured output (A2 restores it).
type Candidate struct {
	WorkspaceDir string // restored candidate workspace (grounding/judge/gates read files here); "" if not materialized
	ResultsJSON  []byte // the variant's results.json
	ReviewJSON   []byte // review-step variants: the review.json (findings)
	Diff         string // base_sha..candidate patch, for the judge
}

// EvalInput is everything an evaluator reads. VariantVersion threads the
// frozen workflow version into the pins (§2 amendment, 2026-07-19).
type EvalInput struct {
	Fixture        Fixture
	Candidate      Candidate
	Variant        string
	VariantVersion int
	Rep            int
	Deps           EvalDeps
}

// EvalDeps are the join surfaces evaluators need. A2 wires the real
// stores; tests wire memory/fake ones. Kept as narrow interfaces so eval
// never imports atc/db (import-cycle discipline, per tickets/handler.go).
type EvalDeps struct {
	Reviews  ReviewJoin  // GetByBuild / ListByTicket over agent_reviews
	Feedback FeedbackJoin // GetByReview over agent_feedback (six verdicts)
	ClaudePath string     // judge CLI ("" -> "claude")
}

// Evaluator scores one candidate against one fixture.
type Evaluator interface {
	Evaluate(ctx context.Context, in EvalInput) (benchscore.ScoreEnvelope, error)
}

// EvaluatorFunc adapts a func to Evaluator.
type EvaluatorFunc func(context.Context, EvalInput) (benchscore.ScoreEnvelope, error)

func (f EvaluatorFunc) Evaluate(ctx context.Context, in EvalInput) (benchscore.ScoreEnvelope, error) {
	return f(ctx, in)
}

// Registry resolves a pinned (name, version). version 0 means "the
// current built-in version" (the two-field default path never names a
// version); an explicit version must match a registered one.
type Registry struct {
	byName map[string]registered
}

type registered struct {
	version int
	ev      Evaluator
}

func NewRegistry() *Registry { return &Registry{byName: map[string]registered{}} }

func (r *Registry) Register(name string, version int, ev Evaluator) {
	r.byName[name] = registered{version: version, ev: ev}
}

func (r *Registry) Resolve(name string, version int) (Evaluator, int, error) {
	reg, ok := r.byName[name]
	if !ok {
		return nil, 0, fmt.Errorf("eval: unknown evaluator %q", name)
	}
	if version != 0 && version != reg.version {
		return nil, 0, fmt.Errorf("eval: evaluator %q version %d not registered (current %d)", name, version, reg.version)
	}
	return reg.ev, reg.version, nil
}

func (r *Registry) Version(name string) int { return r.byName[name].version }

// DefaultEvaluator maps a step_kind to its default evaluator name (the
// guardrail: a two-field spec names no evaluator; this resolves it). It
// returns only names DefaultRegistry() can resolve — the default map has NO
// hole (D4). "workflow" is deliberately absent: workflow cells are end-to-end
// tickets scored off the outcome/scorecard tables (A2/C), not routed through
// eval.ScoreCell, so B registers no workflow evaluator and this returns "".
func DefaultEvaluator(stepKind string) string {
	switch stepKind {
	case "review":
		return "review-precision"
	case "implement":
		return "implement-composite"
	case "plan":
		return "plan-grounding"
	}
	return "" // includes "workflow": owned by A2/C, not B
}
```

- [ ] **Write `agent/benchscore/eval/run.go`** — the A2-called entrypoint that runs the pinned evaluator and persists the row (evaluator failure ⟹ `status:'error'`, principle 2 / Q12):

```go
package eval

import (
	"context"

	"github.com/concourse/concourse/agent/benchscore"
)

// ScoreWriter is the persistence surface (db.AgentBenchScoresFactory).
type ScoreWriter interface {
	Upsert(cellID int, env benchscore.ScoreEnvelope, status string, costUSD float64, rationaleRef string) error
}

// ScoreCell is the single library entrypoint A2's benchrunner reconcile
// calls after a cell's step-under-test completes (plan D8, amended
// 2026-07-19 post-review: web-side deterministic tier v1; ScoreCell is the
// library either way). The caller passes the cell id as a Go parameter —
// nothing parses env vars. It resolves the pinned evaluator, runs it, stamps
// the pins, and writes one score row. An evaluator error is a stored 'error'
// score (distinguished from a low score), never a panic and never a failed
// production/replay step.
func ScoreCell(
	ctx context.Context,
	reg *Registry,
	writer ScoreWriter,
	cellID int,
	evaluatorName string, evaluatorVersion int,
	in EvalInput,
) error {
	ev, resolvedVersion, err := reg.Resolve(evaluatorName, evaluatorVersion)
	if err != nil {
		return err
	}
	pins := benchscore.Pins{
		EvaluatorName: evaluatorName, EvaluatorVersion: resolvedVersion,
		FixtureID: in.Fixture.ID, Variant: in.Variant,
		VariantVersion: in.VariantVersion, Rep: in.Rep,
	}
	env, evalErr := ev.Evaluate(ctx, in)
	env.Pins = pins
	if env.Metrics == nil {
		env.Metrics = map[string]float64{}
	}
	if evalErr != nil {
		// status='error': rationale_ref carries the inline error reason (the
		// documented §1.B amendment — Task 1). Consumers gate on status before
		// treating rationale_ref as a blob:// handle.
		return writer.Upsert(cellID, benchscore.ScoreEnvelope{Metrics: map[string]float64{}, Pins: pins},
			"error", 0, evalErr.Error())
	}
	cost := env.Metrics["cost_usd"]
	return writer.Upsert(cellID, env, "ok", cost, env.RationaleRef)
}
```

- [ ] **Add the join interfaces** to `evaluator.go` (referenced by `EvalDeps`; concrete impls land in Tasks 8/12). Keep them narrow so `eval` never imports `atc/db`:

```go
// ReviewJoin is the agent_reviews read surface (satisfied by
// agent/api/reviews.Store).
type ReviewJoin interface {
	GetByBuild(buildID int) ([]ReviewRecord, error)
	ListByTicket(ticketID int) ([]ReviewRecord, error)
}

// FeedbackJoin is the agent_feedback read surface (satisfied by an adapter
// over agentFeedbackFactory.GetByReview).
type FeedbackJoin interface {
	GetByReview(repo, commit string) ([]FeedbackRecord, error)
}

// ReviewRecord / FeedbackRecord are the eval-local shapes (findings + six
// verdicts) so eval depends on no api package. A2 adapts the real stores.
type ReviewRecord struct {
	BuildID      int
	CommitSHA    string
	FindingIDs   []string // proven_issues + observations ids
}
type FeedbackRecord struct {
	FindingID string
	Verdict   string // one of the six
	Reviewer  string
}

// InjectedFault / Loc are the frozen ground_truth shape (spec §5), defined
// ONCE in A1's agent/api/fixtures (amended 2026-07-19 post-review) and
// aliased here — eval.InjectedFault keeps working without re-declaring the
// struct (one definition the recall matcher, the corpus, and the
// registration route all share).
type (
	InjectedFault = fixtures.InjectedFault
	Loc           = fixtures.Loc
)
```

- [ ] **Run** `go test ./agent/benchscore/eval/` — expect pass.
- [ ] **Commit:** `git add agent/benchscore/eval && git commit -m "feat(bench): Evaluator interface, versioned registry, ScoreCell, determinism canary"`

---

### Task 7: The judge evaluator (implement-step LLM scorer — the factored judge, not a second one)

Wraps `agent/harvest/judge.RunJudge` (Task 3) as an `Evaluator`. This is the LLM component of `implement-composite` and the standalone `implement-judge`. **No second scorer** (scope-out).

**Files:**
- Create: `agent/benchscore/eval/judge.go`
- Test: `agent/benchscore/eval/judge_test.go`

**Steps:**

- [ ] **Write the failing test** `agent/benchscore/eval/judge_test.go` (fakes the CLI via a stub `claude` on PATH — the `judge_test.go` recipe; if that is heavy, assert the mapping from a `judge.Result` to the envelope through an injected runner seam):

```go
package eval_test

import (
	"context"
	"testing"

	"github.com/concourse/concourse/agent/benchscore/eval"
	judge "github.com/concourse/concourse/agent/harvest/judge"
	"github.com/concourse/concourse/agent/schema"
)

func TestJudgeEvaluatorMapsResultToEnvelope(t *testing.T) {
	ev := eval.NewJudgeEvaluator(judge.Config{
		Rubric:        []judge.RubricDimension{{Name: "correctness", Weight: 1, Guidance: "g"}},
		PassThreshold: 7,
	})
	// inject a fake runner so no real CLI is shelled.
	ev.RunJudge = func(_ context.Context, _ judge.Config, _ judge.Opts) (*judge.Result, error) {
		return &judge.Result{
			Total: 8, MaxTotal: 10, Pass: true, Model: "m", CostUSD: 0.42, Turns: 5,
			// Result.Dimensions is []schema.JudgeScoreDimension (the factoring
			// does NOT create a judge.Dimension type — JudgeScoreDimension stays
			// in agent/schema, unmoved). Empty here keeps the test on the numeric
			// mapping.
			Dimensions: []schema.JudgeScoreDimension{},
		}, nil
	}
	env, err := ev.Evaluate(context.Background(), eval.EvalInput{
		Candidate: eval.Candidate{WorkspaceDir: t.TempDir(), Diff: "diff"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if env.Metrics["judge_total"] != 8 || env.Metrics["cost_usd"] != 0.42 || env.Metrics["turns"] != 5 {
		t.Fatalf("metrics mismap: %+v", env.Metrics)
	}
}

func TestJudgeEvaluatorErrorsSurfaceAsEvalError(t *testing.T) {
	ev := eval.NewJudgeEvaluator(judge.Config{
		Rubric: []judge.RubricDimension{{Name: "d", Weight: 1}}, PassThreshold: 7,
	})
	ev.RunJudge = func(_ context.Context, _ judge.Config, _ judge.Opts) (*judge.Result, error) {
		return nil, context.DeadlineExceeded
	}
	_, err := ev.Evaluate(context.Background(), eval.EvalInput{Candidate: eval.Candidate{WorkspaceDir: t.TempDir()}})
	if err == nil {
		t.Fatal("judge failure must surface as an evaluator error (ScoreCell records status=error)")
	}
}
```

(Note: `judge.Result.Dimensions` is `[]schema.JudgeScoreDimension` (the factoring leaves `JudgeScoreDimension` in `agent/schema` — there is no `judge.Dimension` type); the real evaluator maps those to `benchscore.Verdict`. The empty slice above keeps this test focused on the numeric mapping.)

- [ ] **Run** `go test ./agent/benchscore/eval/ -run Judge` — expect compile failure.
- [ ] **Write `agent/benchscore/eval/judge.go`:**

```go
package eval

import (
	"context"

	"github.com/concourse/concourse/agent/benchscore"
	judge "github.com/concourse/concourse/agent/harvest/judge"
)

// JudgeEvaluatorVersion bumps whenever the judge-to-envelope mapping or the
// default rubric wiring changes (score-history honesty, principle 5).
const JudgeEvaluatorVersion = 1

// JudgeEvaluator scores a candidate workspace+diff via the factored judge.
// It is the implement-step LLM scorer — the SAME engine harvest runs, not
// a second judge (spec §4). RunJudge is an injectable seam for tests.
type JudgeEvaluator struct {
	Config  judge.Config
	RunJudge func(context.Context, judge.Config, judge.Opts) (*judge.Result, error)
}

func NewJudgeEvaluator(cfg judge.Config) *JudgeEvaluator {
	return &JudgeEvaluator{Config: cfg, RunJudge: judge.RunJudge}
}

func (e *JudgeEvaluator) Evaluate(ctx context.Context, in EvalInput) (benchscore.ScoreEnvelope, error) {
	res, err := e.RunJudge(ctx, e.Config, judge.Opts{
		ClaudePath: in.Deps.ClaudePath,
		WorkDir:    in.Candidate.WorkspaceDir,
		Diff:       in.Candidate.Diff,
	})
	if err != nil {
		return benchscore.ScoreEnvelope{}, err
	}
	verdicts := make([]benchscore.Verdict, 0, len(res.Dimensions))
	for _, d := range res.Dimensions {
		verdicts = append(verdicts, benchscore.Verdict{Name: d.Name, Score: d.Score, Max: d.Max, Rationale: d.Rationale})
	}
	return benchscore.ScoreEnvelope{
		Metrics: map[string]float64{
			"judge_total": res.Total, "judge_max": res.MaxTotal,
			"cost_usd": res.CostUSD, "turns": float64(res.Turns),
		},
		Verdicts: verdicts,
	}, nil
}
```

- [ ] **Run** `go test ./agent/benchscore/eval/ -run Judge` — expect pass.
- [ ] **Commit:** `git add agent/benchscore/eval && git commit -m "feat(bench): judge evaluator wrapping the factored judge.RunJudge (no second judge)"`

---

### Task 8: The review-precision evaluator (six-verdict feedback join — Q4 pinned)

Precision over joined six-verdict labels, disambiguated by the fixture's `build_id` (Task 1, Q4). Deterministic — needs only DB joins (no LLM, no workspace); its execution locus is D8. Plain `testing` with fake join deps.

**Files:**
- Create: `agent/benchscore/eval/review_precision.go`
- Test: `agent/benchscore/eval/review_precision_test.go`

**Steps:**

- [ ] **Write the failing test** `agent/benchscore/eval/review_precision_test.go`:

```go
package eval_test

import (
	"context"
	"testing"

	"github.com/concourse/concourse/agent/benchscore/eval"
)

type fakeReviews struct{ byBuild map[int]eval.ReviewRecord }
func (f fakeReviews) GetByBuild(b int) ([]eval.ReviewRecord, error) { return []eval.ReviewRecord{f.byBuild[b]}, nil }
func (f fakeReviews) ListByTicket(int) ([]eval.ReviewRecord, error) { return nil, nil }

type fakeFeedback struct{ rows []eval.FeedbackRecord }
func (f fakeFeedback) GetByReview(string, string) ([]eval.FeedbackRecord, error) { return f.rows, nil }

func TestReviewPrecisionJoinsFeedbackThroughFixtureBuildID(t *testing.T) {
	deps := eval.EvalDeps{
		Reviews: fakeReviews{byBuild: map[int]eval.ReviewRecord{
			99: {BuildID: 99, CommitSHA: "abc", FindingIDs: []string{"f1", "f2", "f3", "f4"}},
		}},
		Feedback: fakeFeedback{rows: []eval.FeedbackRecord{
			{FindingID: "f1", Verdict: "accurate"},
			{FindingID: "f2", Verdict: "false_positive"},
			{FindingID: "f3", Verdict: "noisy"},
			{FindingID: "f4", Verdict: "partially_correct"}, // excluded from both num and denom
		}},
	}
	ev := eval.NewReviewPrecisionEvaluator()
	env, err := ev.Evaluate(context.Background(), eval.EvalInput{
		Fixture: eval.Fixture{ID: 5, StepKind: "review", Repo: "r", BuildID: 99},
		Deps:    deps,
	})
	if err != nil {
		t.Fatal(err)
	}
	// accurate=1 / (accurate+false_positive+noisy+overly_strict)=3 => 0.3333
	if got := env.Metrics["precision"]; got < 0.33 || got > 0.34 {
		t.Fatalf("precision = %v, want ~0.333", got)
	}
	if env.Metrics["partial"] != 1 {
		t.Fatalf("partially_correct must be counted separately: %+v", env.Metrics)
	}
	if env.Metrics["labeled_findings"] != 4 {
		t.Fatalf("labeled_findings = %v, want 4", env.Metrics["labeled_findings"])
	}
}

func TestReviewPrecisionOmitsMetricWhenNoLabels(t *testing.T) {
	ev := eval.NewReviewPrecisionEvaluator()
	env, _ := ev.Evaluate(context.Background(), eval.EvalInput{
		Fixture: eval.Fixture{ID: 5, StepKind: "review", BuildID: 99},
		Deps: eval.EvalDeps{
			Reviews:  fakeReviews{byBuild: map[int]eval.ReviewRecord{99: {BuildID: 99}}},
			Feedback: fakeFeedback{},
		},
	})
	// no feedback -> precision is unmeasured (omitted), never a failing 0.
	if _, ok := env.Metrics["precision"]; ok {
		t.Fatal("precision must be omitted when there are no labels, not reported as 0")
	}
}
```

- [ ] **Run** `go test ./agent/benchscore/eval/ -run ReviewPrecision` — expect compile failure.
- [ ] **Write `agent/benchscore/eval/review_precision.go`:**

```go
package eval

import (
	"context"

	"github.com/concourse/concourse/agent/benchscore"
)

// ReviewPrecisionVersion bumps when the precision formula or the join
// changes (principle 5).
const ReviewPrecisionVersion = 1

// cleanHit / cleanMiss partition the six verdicts (§4). accurate is a hit;
// false_positive/noisy/overly_strict are misses; partially_correct and
// missed_context are neither (counted separately).
var (
	cleanHit  = map[string]bool{"accurate": true}
	cleanMiss = map[string]bool{"false_positive": true, "noisy": true, "overly_strict": true}
)

type ReviewPrecisionEvaluator struct{}

func NewReviewPrecisionEvaluator() *ReviewPrecisionEvaluator { return &ReviewPrecisionEvaluator{} }

// Evaluate joins the fixture's review (by build_id — Q4 disambiguation)
// to its six-verdict feedback and computes precision. Server-side; no
// workspace, no LLM.
func (e *ReviewPrecisionEvaluator) Evaluate(ctx context.Context, in EvalInput) (benchscore.ScoreEnvelope, error) {
	reviews, err := in.Deps.Reviews.GetByBuild(in.Fixture.BuildID)
	if err != nil {
		return benchscore.ScoreEnvelope{}, err
	}
	findingIDs := map[string]bool{}
	commit := in.Fixture.CommitSHA
	for _, r := range reviews {
		if commit == "" {
			commit = r.CommitSHA
		}
		for _, id := range r.FindingIDs {
			findingIDs[id] = true
		}
	}
	fb, err := in.Deps.Feedback.GetByReview(in.Fixture.Repo, commit)
	if err != nil {
		return benchscore.ScoreEnvelope{}, err
	}

	var hit, miss, partial, missed, labeled, ambiguous float64
	seen := map[string]bool{}
	for _, f := range fb {
		if !findingIDs[f.FindingID] {
			continue // feedback for a different review of the same commit
		}
		if seen[f.FindingID] {
			ambiguous++ // >1 verdict for one finding-id in this review (v1 limit, surfaced)
		}
		seen[f.FindingID] = true
		labeled++
		switch {
		case cleanHit[f.Verdict]:
			hit++
		case cleanMiss[f.Verdict]:
			miss++
		case f.Verdict == "partially_correct":
			partial++
		case f.Verdict == "missed_context":
			missed++
		}
	}

	metrics := map[string]float64{
		"labeled_findings": labeled, "partial": partial,
		"missed": missed, "ambiguous_findings": ambiguous,
	}
	if denom := hit + miss; denom > 0 {
		metrics["precision"] = hit / denom
	}
	// precision omitted entirely when denom==0 (unmeasured != failing).
	return benchscore.ScoreEnvelope{Metrics: metrics}, nil
}
```

- [ ] **Run** `go test ./agent/benchscore/eval/ -run ReviewPrecision` — expect pass.
- [ ] **Provide the A2 adapters** (thin, so `eval` stays free of api/db imports) as a doc note in this task: A2 wires `EvalDeps.Reviews` from `agent/api/reviews.Store` (mapping `StoredReview` → `ReviewRecord` by decoding `proven_issues`/`observations` ids) and `EvalDeps.Feedback` from an adapter over `atc/db.agentFeedbackFactory.GetByReview` (mapping `feedback.StoredFeedback` → `FeedbackRecord`). These adapters live in A2's runner package (they import both api packages); B ships only the interfaces + records so its tests need no DB.
- [ ] **Commit:** `git add agent/benchscore/eval && git commit -m "feat(bench): review-precision evaluator - six-verdict join via fixture build_id (Q4)"`

---

### Task 9: The review-recall evaluator (fault-injection ground truth)

Recall over a synthetic fixture's `ground_truth`: fraction of injected faults the candidate review caught. Deterministic; the matcher is shared with the corpus (Task 10). Plain `testing`.

**Files:**
- Create: `agent/benchscore/eval/review_recall.go`
- Test: `agent/benchscore/eval/review_recall_test.go`

**Steps:**

- [ ] **Write the failing test** `agent/benchscore/eval/review_recall_test.go`:

```go
package eval_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/concourse/concourse/agent/benchscore/eval"
)

func TestReviewRecallMatchesInjectedFaultsByLocationAndClass(t *testing.T) {
	gt := []eval.InjectedFault{
		{Class: "nil-deref", Location: eval.Loc{File: "a.go", Line: 10}, Description: "x"},
		{Class: "off-by-one", Location: eval.Loc{File: "b.go", Line: 42}, Description: "y"},
		{Class: "resource-leak", Location: eval.Loc{File: "c.go", Line: 5}, Description: "z"},
	}
	// candidate review caught fault 1 (exact) and fault 2 (line 44, within ±3), missed fault 3.
	review := map[string]any{
		"proven_issues": []map[string]any{
			{"id": "g1", "category": "gate", "file": "a.go", "line": 10, "title": "nil deref possible"},
		},
		"observations": []map[string]any{
			{"id": "j1", "category": "judge", "file": "b.go", "line": 44, "title": "loop bound off by one"},
		},
	}
	reviewJSON, _ := json.Marshal(review)

	ev := eval.NewReviewRecallEvaluator()
	env, err := ev.Evaluate(context.Background(), eval.EvalInput{
		Fixture:   eval.Fixture{ID: 7, StepKind: "review", GroundTruth: gt},
		Candidate: eval.Candidate{ReviewJSON: reviewJSON},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := env.Metrics["recall"]; got < 0.66 || got > 0.67 {
		t.Fatalf("recall = %v, want ~0.667 (2 of 3)", got)
	}
	if env.Metrics["injected"] != 3 || env.Metrics["caught"] != 2 {
		t.Fatalf("counts: %+v", env.Metrics)
	}
}

func TestReviewRecallErrorsWithoutGroundTruth(t *testing.T) {
	ev := eval.NewReviewRecallEvaluator()
	_, err := ev.Evaluate(context.Background(), eval.EvalInput{
		Fixture: eval.Fixture{ID: 7, StepKind: "review"}, // no ground_truth
	})
	if err == nil {
		t.Fatal("recall requires a synthetic fixture with ground_truth; a production fixture is an evaluator error")
	}
}
```

- [ ] **Run** `go test ./agent/benchscore/eval/ -run ReviewRecall` — expect compile failure.
- [ ] **Write `agent/benchscore/eval/review_recall.go`:**

```go
package eval

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/concourse/concourse/agent/benchscore"
)

// ReviewRecallVersion bumps when the matcher or recall formula changes.
const ReviewRecallVersion = 1

// GroundTruthLineTolerance: a caught finding matches an injected fault if
// same file AND |line diff| <= this (§4). Reviewers cite the symptom line,
// not always the exact injected line.
const GroundTruthLineTolerance = 3

type candidateFinding struct {
	File  string `json:"file"`
	Line  int    `json:"line"`
	Title string `json:"title"`
}
type candidateReview struct {
	ProvenIssues []candidateFinding `json:"proven_issues"`
	Observations []candidateFinding `json:"observations"`
}

type ReviewRecallEvaluator struct{}

func NewReviewRecallEvaluator() *ReviewRecallEvaluator { return &ReviewRecallEvaluator{} }

func (e *ReviewRecallEvaluator) Evaluate(ctx context.Context, in EvalInput) (benchscore.ScoreEnvelope, error) {
	if len(in.Fixture.GroundTruth) == 0 {
		return benchscore.ScoreEnvelope{}, fmt.Errorf("recall evaluator requires a synthetic fixture with ground_truth")
	}
	var cr candidateReview
	if len(in.Candidate.ReviewJSON) > 0 {
		if err := json.Unmarshal(in.Candidate.ReviewJSON, &cr); err != nil {
			return benchscore.ScoreEnvelope{}, fmt.Errorf("recall: candidate review.json parse: %w", err)
		}
	}
	found := append(append([]candidateFinding{}, cr.ProvenIssues...), cr.Observations...)

	caught := 0
	for _, fault := range in.Fixture.GroundTruth {
		if MatchesFault(fault, found) {
			caught++
		}
	}
	injected := len(in.Fixture.GroundTruth)
	return benchscore.ScoreEnvelope{Metrics: map[string]float64{
		"recall":   float64(caught) / float64(injected),
		"injected": float64(injected),
		"caught":   float64(caught),
	}}, nil
}

// MatchesFault reports whether any candidate finding lands on an injected
// fault (same file, line within tolerance). Shared with the corpus builder
// (Task 10) so injection and scoring agree on "same location".
func MatchesFault(fault InjectedFault, found []candidateFinding) bool {
	for _, f := range found {
		if f.File != fault.Location.File {
			continue
		}
		d := f.Line - fault.Location.Line
		if d < 0 {
			d = -d
		}
		if d <= GroundTruthLineTolerance {
			return true
		}
	}
	return false
}
```

- [ ] **Run** `go test ./agent/benchscore/eval/ -run ReviewRecall` — expect pass.
- [ ] **Commit:** `git add agent/benchscore/eval && git commit -m "feat(bench): review-recall evaluator - matches candidate findings to injected ground truth"`

---

### Task 10: The fault-injection corpus library — bug classes + injector prompt (AUP-aware)

The `agent/benchscore/corpus` library: the bug-class taxonomy (seeded from recurring production findings — the leftward-migration engine), the fault-injector prompt with CI-context-first framing (the #23/#24 AUP lesson), and the injected-fault manifest type. This is the *content*; the workflow + registration is Task 11.

**Files:**
- Create: `agent/benchscore/corpus/classes.go`
- Create: `agent/benchscore/corpus/inject.go`
- Test: `agent/benchscore/corpus/inject_test.go`

**Steps:**

- [ ] **Write the failing test** `agent/benchscore/corpus/inject_test.go`:

```go
package corpus_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/benchscore/corpus"
)

func TestBugClassesSeedFromRecurringProductionFindings(t *testing.T) {
	// leftward-migration: every class carries the production finding-category
	// it was promoted from, so a class that a deterministic gate later
	// subsumes can be retired with an audit trail.
	if len(corpus.BugClasses) == 0 {
		t.Fatal("bug-class taxonomy must be non-empty")
	}
	for _, c := range corpus.BugClasses {
		if c.Name == "" || c.SeedFinding == "" || c.InjectGuidance == "" {
			t.Errorf("class %q under-specified: %+v", c.Name, c)
		}
	}
	if corpus.ClassByName("nil-deref") == nil {
		t.Error("expected a nil-deref class")
	}
}

func TestInjectorPromptIsCIContextFirst(t *testing.T) {
	// The #23/#24 AUP lesson: "inject bugs" phrasing draws vocabulary
	// refusals. The prompt must lead with the CI/evaluation context and
	// never use bare adversarial verbs as the framing.
	p := corpus.BuildInjectorPrompt(corpus.InjectRequest{
		Repo: "tdmtrader/concourse", Diff: "diff --git a/x.go b/x.go\n", Classes: corpus.BugClasses[:2],
	})
	head := p[:min(len(p), 300)]
	if !strings.Contains(head, "test corpus") && !strings.Contains(head, "evaluation") && !strings.Contains(head, "CI") {
		t.Errorf("prompt must open with CI/evaluation framing (AUP lesson); head=%q", head)
	}
	if strings.HasPrefix(strings.TrimSpace(p), "Inject") {
		t.Error("prompt must not open with a bare adversarial verb (AUP lesson)")
	}
	// it must ask for the ground-truth manifest in the frozen shape.
	if !strings.Contains(p, "\"class\"") || !strings.Contains(p, "\"location\"") {
		t.Error("prompt must request the {class, location, description} manifest")
	}
}

func min(a, b int) int { if a < b { return a }; return b }
```

- [ ] **Run** `go test ./agent/benchscore/corpus/` — expect compile failure.
- [ ] **Write `agent/benchscore/corpus/classes.go`** — the taxonomy, each class recording the production finding-category it was promoted from (the leftward-migration audit trail):

```go
// Package corpus builds the review-recall ground truth (spec §5): a
// fault-injection workflow takes a clean merged diff, injects realistic
// bug classes, and yields a ground_truth manifest. Injected classes seed
// from recurring production review findings (the leftward-migration
// engine) and the injector prompt is AUP-vocabulary-aware (#23/#24).
package corpus

// BugClass is one injectable defect family.
type BugClass struct {
	Name           string // stable id; matches ground_truth.class
	SeedFinding    string // the production finding-category this was promoted from
	InjectGuidance string // how the injector should realize it (fed to the LLM step)
	Retired        bool   // true once a deterministic gate subsumes the class (leftward migration)
}

// BugClasses is the v1 taxonomy. Grow it from SUPERVISION.md's recurring
// findings; retire an entry (Retired=true) when a gate learns to catch it
// deterministically, recording the transfer.
var BugClasses = []BugClass{
	{Name: "nil-deref", SeedFinding: "judge:correctness",
		InjectGuidance: "remove a guard so a pointer/interface can be nil at a deref site"},
	{Name: "off-by-one", SeedFinding: "judge:correctness",
		InjectGuidance: "shift a loop bound or slice index by one"},
	{Name: "resource-leak", SeedFinding: "judge:correctness",
		InjectGuidance: "drop a defer Close / cancel on an early-return path"},
	{Name: "error-swallow", SeedFinding: "judge:robustness",
		InjectGuidance: "discard an error return that the original code checked"},
	{Name: "wrong-boundary", SeedFinding: "judge:correctness",
		InjectGuidance: "flip a < to <= (or > to >=) at a boundary check"},
	{Name: "unhandled-concurrency", SeedFinding: "judge:robustness",
		InjectGuidance: "remove a lock/atomic around shared state accessed by >1 goroutine"},
}

func ClassByName(name string) *BugClass {
	for i := range BugClasses {
		if BugClasses[i].Name == name {
			return &BugClasses[i]
		}
	}
	return nil
}
```

- [ ] **Write `agent/benchscore/corpus/inject.go`** — the request/manifest types + the CI-context-first prompt:

```go
package corpus

import (
	"fmt"
	"strings"

	"github.com/concourse/concourse/agent/api/fixtures"
)

// InjectRequest is the corpus builder's input for one clean diff.
type InjectRequest struct {
	Repo    string
	BaseSHA string
	Diff    string // the known-clean merged diff to seed from
	Classes []BugClass
}

// InjectedFault / Loc are the frozen ground_truth shape, defined ONCE in
// A1's agent/api/fixtures (amended 2026-07-19 post-review) and aliased here —
// corpus imports agent/api/fixtures (a leaf package; no cycle) instead of
// re-declaring the struct (one definition the injector and the recall
// matcher share).
type (
	InjectedFault = fixtures.InjectedFault
	Loc           = fixtures.Loc
)

// InjectionResult is what the injector step emits: the modified overlay
// (patch text) plus the ground-truth manifest of what it changed.
type InjectionResult struct {
	OverlayPatch string          `json:"overlay_patch"`
	GroundTruth  []InjectedFault `json:"ground_truth"`
}

// BuildInjectorPrompt renders the agent-step prompt. AUP LESSON (#23/#24):
// lead with the CI/evaluation purpose; never open with a bare adversarial
// verb. The corpus builder is itself the first refusal-regression testbed.
func BuildInjectorPrompt(req InjectRequest) string {
	var b strings.Builder
	b.WriteString("You are building a CI test corpus that measures how well an automated code reviewer ")
	b.WriteString("catches defects. This is a controlled evaluation harness: the modified code is never ")
	b.WriteString("shipped — it exists only so the reviewer-under-test has known defects to find.\n\n")
	b.WriteString(fmt.Sprintf("Starting from this known-good diff for %s, produce a variant that contains ", req.Repo))
	b.WriteString("a small number of realistic, subtle defects drawn from the classes below. Keep the ")
	b.WriteString("change minimal and plausible (the kind of mistake a hurried engineer makes).\n\n")
	b.WriteString("Defect classes to draw from:\n")
	for _, c := range req.Classes {
		b.WriteString(fmt.Sprintf("  - %s: %s\n", c.Name, c.InjectGuidance))
	}
	b.WriteString("\nSeed diff:\n")
	b.WriteString(req.Diff)
	b.WriteString("\n\nEmit, as the fenced ```json block, an object with two keys:\n")
	b.WriteString("  \"overlay_patch\": the unified patch introducing the defects\n")
	b.WriteString("  \"ground_truth\": an array of {\"class\":..., \"location\":{\"file\":...,\"line\":...}, \"description\":...}\n")
	b.WriteString("Every defect you introduce MUST appear in ground_truth with its exact file and line.\n")
	return b.String()
}
```

- [ ] **Run** `go test ./agent/benchscore/corpus/` — expect pass.
- [ ] **Commit:** `git add agent/benchscore/corpus && git commit -m "feat(bench): fault-injection corpus - bug-class taxonomy (leftward-migration) + AUP-aware injector prompt"`

---

### Task 11: `RegisterSyntheticFixtures` route (six-touchpoint) — the synthetic-fixture write path

Wire the load-bearing corpus surface: the `RegisterSyntheticFixtures` route that persists an `InjectionResult` as `source:'synthetic'` fixtures carrying `ground_truth` — and **(amended 2026-07-19 post-review)** doubles as the **benchmark import path**: it accepts `source ∈ {synthetic, benchmark}` (default `synthetic`) and **any** `step_kind` (the forced `StepKind:'review'` is dropped); A1 ships no importer. The route NAME is unchanged (provisional until S-8 — a known misnomer once `benchmark` is accepted). Follows the #36 six-touchpoint pattern. **Scope note (finding 13):** the in-memory corpus *workflow builder* (`BuildWorkflow`/`CorpusWorkflowName`) is **deferred** to the slice that actually dispatches the corpus workflow (Open Decision D3) — B ships no dead `workflow.Config` constructor ahead of its consumer. This task ships the registration route + reuses the injector prompt (Task 10); the workflow-definition constructor lands with D3's dispatch path.

**Files:**
- Create: `agent/api/benchcorpus/handler.go` (the registration Server + `MemoryStore` implementing `fixtures.SyntheticFixtureStore`)
- Create: `agent/api/benchcorpus/handler_test.go`
- Create: `agent/api/benchcorpus/synthetic_fixture.go` (the `RegisterRequest` wire type; references `fixtures.SyntheticFixture`/`fixtures.SyntheticFixtureStore`/`fixtures.InjectedFault` from A1's `agent/api/fixtures` — no duplicated contract type)
- Modify: `atc/routes.go` (route decl), `atc/wrappa/api_auth_wrappa.go` (auth tier), `atc/api/handler.go` (route→Server), `go-concourse/concourse/agent_bench.go` (client)
- **Deferred to D3's dispatch slice (NOT this task):** `agent/benchscore/corpus/workflow.go` (the `BuildWorkflow` constructor) and the `fly agent bench corpus` verb's `Execute()` + orchestration.

**Steps:**

- [ ] **Write the failing handler test** `agent/api/benchcorpus/handler_test.go` (plain `testing`, matching `agent/api/reviews`; auth is enforced by the wrappa tier, not the handler — the handler only reads the verified writer):

```go
package benchcorpus_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/concourse/concourse/agent/api/benchcorpus"
	"github.com/concourse/concourse/agent/api/fixtures"
)

func TestRegisterSyntheticFixturesWritesGroundTruth(t *testing.T) {
	store := benchcorpus.NewMemoryStore()
	h := benchcorpus.NewHandler(store, func(*http.Request) string { return "tdmtrader" })

	body := benchcorpus.RegisterRequest{
		Repo: "tdmtrader/concourse", BaseSHA: "abc",
		Fixtures: []fixtures.SyntheticFixture{{
			OverlayRef: "blob://ov/1", ContentHash: "h1",
			GroundTruth: []fixtures.InjectedFault{
				{Class: "nil-deref", Location: fixtures.Loc{File: "a.go", Line: 10}, Description: "x"},
			},
		}},
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/agent/bench/fixtures/synthetic", bytes.NewReader(raw))
	w := httptest.NewRecorder()
	h.Register(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	got := store.All()
	if len(got) != 1 || got[0].StepKind != "review" || got[0].Repo != "tdmtrader/concourse" {
		t.Fatalf("stored fixture wrong: %+v", got)
	}
	if len(got[0].GroundTruth) != 1 || got[0].GroundTruth[0].Class != "nil-deref" {
		t.Fatalf("ground_truth not persisted: %+v", got[0])
	}
	if got[0].Source != "synthetic" || !got[0].Pinned {
		t.Fatalf("synthetic fixtures must be source=synthetic pinned=true: %+v", got[0])
	}
}

func TestRegisterRejectsFaultWithoutLocation(t *testing.T) {
	h := benchcorpus.NewHandler(benchcorpus.NewMemoryStore(), func(*http.Request) string { return "u" })
	body := benchcorpus.RegisterRequest{Repo: "r", Fixtures: []fixtures.SyntheticFixture{{
		ContentHash: "h", GroundTruth: []fixtures.InjectedFault{{Class: "nil-deref"}}, // no file
	}}}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/x", bytes.NewReader(raw))
	w := httptest.NewRecorder()
	h.Register(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a fault without a location must be rejected: %d", w.Code)
	}
}

// (Amended 2026-07-19 post-review — the route generalization.)
func TestRegisterAcceptsBenchmarkSourceAndAnyStepKind(t *testing.T) {
	store := benchcorpus.NewMemoryStore()
	h := benchcorpus.NewHandler(store, func(*http.Request) string { return "u" })
	body := benchcorpus.RegisterRequest{
		Repo: "r", BaseSHA: "abc", Source: "benchmark", StepKind: "workflow",
		Fixtures: []fixtures.SyntheticFixture{{ContentHash: "h1"}},
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/x", bytes.NewReader(raw))
	w := httptest.NewRecorder()
	h.Register(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	got := store.All()
	if got[0].Source != "benchmark" || got[0].StepKind != "workflow" || !got[0].Pinned {
		t.Fatalf("benchmark-source fixture wrong: %+v", got[0])
	}
}

func TestRegisterRejectsUnknownSource(t *testing.T) {
	h := benchcorpus.NewHandler(benchcorpus.NewMemoryStore(), func(*http.Request) string { return "u" })
	body := benchcorpus.RegisterRequest{Repo: "r", Source: "production",
		Fixtures: []fixtures.SyntheticFixture{{ContentHash: "h"}}}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/x", bytes.NewReader(raw))
	w := httptest.NewRecorder()
	h.Register(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("source=production must be rejected (route accepts synthetic|benchmark): %d", w.Code)
	}
}
```

- [ ] **Run** `go test ./agent/api/benchcorpus/` — expect compile failure.
- [ ] **Write `agent/api/benchcorpus/synthetic_fixture.go`** — the `RegisterRequest` wire type only; the store row type (`fixtures.SyntheticFixture`) and the `fixtures.SyntheticFixtureStore` contract live in A1's `agent/api/fixtures` (amended 2026-07-19 post-review), imported here:

```go
// Package benchcorpus serves RegisterSyntheticFixtures (§4.2, bench-B): the
// corpus builder's synthetic-fixture write path AND the benchmark import
// path (amended 2026-07-19 post-review — source ∈ {synthetic, benchmark},
// any step_kind). A1 owns agent_step_fixtures and its production-capture
// writer; this is B's registration writer over the
// fixtures.SyntheticFixtureStore contract A1's atc/db factory implements.
// The ground-truth shape (fixtures.InjectedFault/fixtures.Loc) and the store
// row/interface (fixtures.SyntheticFixture/fixtures.SyntheticFixtureStore)
// live in A1's agent/api/fixtures — ONE definition, so no benchcorpus-local
// duplicate types.
package benchcorpus

import "github.com/concourse/concourse/agent/api/fixtures"

// RegisterRequest is the POST body. Source selects the fixture family:
// "synthetic" (default — corpus-built, ground-truth-carrying) or "benchmark"
// (imported curated cases); StepKind is any step kind (default "review", the
// corpus builder's kind). Per-fixture rows are fixtures.SyntheticFixture
// (the ONE store type — no request/stored split): the client sets
// OverlayRef/ContentHash/GroundTruth/Tags and the handler fills
// Repo/StepKind/BaseSHA/Source/Pinned before writing.
type RegisterRequest struct {
	Repo     string                      `json:"repo"`
	BaseSHA  string                      `json:"base_sha"`
	Source   string                      `json:"source,omitempty"`    // synthetic (default) | benchmark
	StepKind string                      `json:"step_kind,omitempty"` // any step kind; default "review"
	Fixtures []fixtures.SyntheticFixture `json:"fixtures"`
}
```

- [ ] **Write `agent/api/benchcorpus/handler.go`** — the Server (inject `UserNameFunc` to avoid the accessor import cycle, per `tickets/handler.go:14`) + `MemoryStore`:

```go
package benchcorpus

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/concourse/concourse/agent/api/fixtures"
)

// UserNameFunc resolves the acting principal/human (wired in
// atc/api/handler.go; avoids importing accessor here — import-cycle).
type UserNameFunc func(*http.Request) string

type Handler struct {
	store    fixtures.SyntheticFixtureStore
	userName UserNameFunc
}

func NewHandler(store fixtures.SyntheticFixtureStore, userName UserNameFunc) *Handler {
	return &Handler{store: store, userName: userName}
}

// Register handles POST /api/v1/agent/bench/fixtures/synthetic. Auth is the
// wrappa tier (CheckAgentAuthorizationHandler / principal); this reads only
// the verified writer.
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Repo == "" || len(req.Fixtures) == 0 {
		http.Error(w, "repo and at least one fixture are required", http.StatusBadRequest)
		return
	}
	// (Amended 2026-07-19 post-review): source ∈ {synthetic, benchmark},
	// default synthetic; step_kind is any kind, default "review" (the corpus
	// builder's kind). Both sources land Pinned=true — curated/synthetic
	// imports are retention-exempt (spec §2).
	source := req.Source
	if source == "" {
		source = "synthetic"
	}
	if source != "synthetic" && source != "benchmark" {
		http.Error(w, "source must be synthetic or benchmark", http.StatusBadRequest)
		return
	}
	stepKind := req.StepKind
	if stepKind == "" {
		stepKind = "review"
	}
	ids := make([]int, 0, len(req.Fixtures))
	for _, f := range req.Fixtures {
		if f.ContentHash == "" {
			http.Error(w, "content_hash is required", http.StatusBadRequest)
			return
		}
		for _, gt := range f.GroundTruth {
			if gt.Class == "" || gt.Location.File == "" {
				http.Error(w, "every ground_truth fault needs a class and a location.file", http.StatusBadRequest)
				return
			}
		}
		id, err := h.store.RegisterSynthetic(fixtures.SyntheticFixture{
			Repo: req.Repo, StepKind: stepKind, BaseSHA: req.BaseSHA,
			OverlayRef: f.OverlayRef, ContentHash: f.ContentHash,
			GroundTruth: f.GroundTruth, Tags: f.Tags,
			Source: source, Pinned: true,
		})
		if err != nil {
			http.Error(w, fmt.Sprintf("register: %v", err), http.StatusInternalServerError)
			return
		}
		ids = append(ids, id)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"fixture_ids": ids})
}

// MemoryStore is the in-memory fixtures.SyntheticFixtureStore for handler tests.
type MemoryStore struct {
	mu   sync.Mutex
	rows []fixtures.SyntheticFixture
	next int
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{next: 1} }

func (m *MemoryStore) RegisterSynthetic(f fixtures.SyntheticFixture) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.next
	m.next++
	m.rows = append(m.rows, f)
	return id, nil
}

func (m *MemoryStore) All() []fixtures.SyntheticFixture {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]fixtures.SyntheticFixture{}, m.rows...)
}
```

- [ ] **Run** `go test ./agent/api/benchcorpus/` — expect pass.

  > **Deferred (finding 13):** `agent/benchscore/corpus/workflow.go` — the `BuildWorkflow(req) workflow.Config` constructor + `CorpusWorkflowName` — is **NOT written here**. It is an in-memory `workflow.Config` nothing in B registers or dispatches; its only consumer is D3's drive path (dispatch → read `InjectionResult` → POST). Shipping it now lands dead code. It lands with the D3 slice that dispatches the corpus workflow, together with the `fly agent bench corpus` verb below. (When written: `SpecDelivery:"files"` so the dispatch renderer accepts it without touching the refusal chain; `Step.Prompt` is a KEY into `Config.Prompts` (config.go:74), not inline text; field names verified against `agent/workflow/config.go` — `Name`:12, `SpecDelivery`:19, `SystemPrompt`:38, `Prompts`:23, `Steps`:25, `Step.Agent`:73, `Step.Prompt`:74.)

- [ ] **Six-touchpoint wiring** (place each addition adjacent to the A2 bench-route additions — search for A2's `ListAgentBenchExperiments`/`GetAgentBenchResults` route names; **anchor caveat applies** — A2 shifts these line numbers, so search for the named symbol, not the line):
  1. **`atc/routes.go`** (~`:301-348`): add `{Path: "/api/v1/agent/bench/fixtures/synthetic", Method: "POST", Name: atc.RegisterSyntheticFixtures}` and the `RegisterSyntheticFixtures = "RegisterSyntheticFixtures"` `RouteName` const.
  2. **`atc/wrappa/api_auth_wrappa.go`**: add `case atc.RegisterSyntheticFixtures:` to the `CheckAgentAuthorizationHandler` group — team-less agent tier, principal-authenticable (a supervisor can call it, spec §9). The exhaustive `switch name {` is at **`:44`** (branch head); the `CheckAgentAuthorizationHandler` cases are around **`:246-264`**; omitting the case hits the `default: panic("you missed a spot…")` at **`:272-273`** and fails startup. (Anchors verified on `jetbridge` HEAD; A2/A1 shift them — match the symbol.)
  3. **`atc/api/handler.go`** (`~:366`): `atc.RegisterSyntheticFixtures: http.HandlerFunc(benchCorpusServer.Register)`, constructing `benchCorpusServer := benchcorpus.NewHandler(benchCorpusStore, userNameFunc)` where `benchCorpusStore` is A1's fixtures factory (satisfying `fixtures.SyntheticFixtureStore`) and `userNameFunc` is the existing injected func.
  4. **`go-concourse/concourse/agent_bench.go`** (append to A2's file, or create if B lands the client first): `func (c *client) RegisterSyntheticFixtures(req benchcorpus.RegisterRequest) ([]int, error)` sending `internal.Request{RequestName: atc.RegisterSyntheticFixtures}`.
  5. **`fly agent bench corpus` verb — DEFERRED to D3's dispatch slice (finding 18).** The verb's job (dispatch the corpus workflow, wait, read the terminal step's `InjectionResult` artifact, POST it to `RegisterSyntheticFixtures`) is exactly D3's deferred client-driven flow — it has no concrete `Execute()` steps until D3 lands. This task ships **only** touchpoints 1-4 (the durable route + client seam). Do **not** scaffold `AgentBenchCorpusCommand` here; add it, its `Execute()`, and a `fly/integration` spec (mock ATC, `versions.go`=0.1.0) in the D3 slice.

- [ ] **Run:** `go build ./... && ginkgo ./atc/wrappa/ ./atc/api/accessor/ && go test ./agent/api/benchcorpus/` — the wrappa exhaustive-switch guard is the fail-fast signal for a missed auth tier.
- [ ] **Commit:** `git add agent/api/benchcorpus atc go-concourse && git commit -m "feat(bench): RegisterSyntheticFixtures route - synthetic + benchmark sources (touchpoints 1-4; fly verb + workflow builder deferred to D3)"`

---

### Task 12: The implement-composite evaluator (gates + judge + downstream findings)

Composes `RunGates` (deterministic, over the candidate workspace) + the judge evaluator (Task 7) + a downstream-review finding count (joined by `ticket_id`). One envelope (spec §4 row / Task-1 formula). Plain `testing`. **Production wiring deferred (second amendment 2026-07-19 post-review):** gates (`go build/test`) and the judge (shells `claude` with the platform credential) are pod-side work that must never run in the ATC web process, so A2's v1 reconcile does NOT invoke this evaluator — implement cells go `ok` unscored (annotated) until the judge-in-pod slice renders it as a pod step. This task still lands in full (hermetic tests, no live gates/judge in CI): the library IS that slice's body, and building it now keeps the envelope shape frozen.

**Files:**
- Create: `agent/benchscore/eval/implement.go`
- Test: `agent/benchscore/eval/implement_test.go`

**Steps:**

- [ ] **Write the failing test** `agent/benchscore/eval/implement_test.go` (inject the gate + judge sub-evaluators so no CLI/`go build` shells):

```go
package eval_test

import (
	"context"
	"testing"

	"github.com/concourse/concourse/agent/benchscore"
	"github.com/concourse/concourse/agent/benchscore/eval"
)

func TestImplementCompositeMergesGatesJudgeAndDownstream(t *testing.T) {
	ev := eval.NewImplementCompositeEvaluator()
	ev.RunGates = func(ws string) (passed, total int, err error) { return 2, 3, nil }
	ev.Judge = eval.EvaluatorFunc(func(_ context.Context, _ eval.EvalInput) (benchscore.ScoreEnvelope, error) {
		return benchscore.ScoreEnvelope{
			Metrics:  map[string]float64{"judge_total": 8, "judge_max": 10, "cost_usd": 0.4, "turns": 6},
			Verdicts: []benchscore.Verdict{{Name: "correctness", Score: 8, Max: 10}},
		}, nil
	})
	deps := eval.EvalDeps{Reviews: fakeReviews{byBuild: map[int]eval.ReviewRecord{}}}
	// downstream review for the ticket has 4 findings.
	deps.Reviews = downstreamReviews{count: 4}

	env, err := ev.Evaluate(context.Background(), eval.EvalInput{
		Fixture:   eval.Fixture{ID: 3, StepKind: "implement", TicketID: 55},
		Candidate: eval.Candidate{WorkspaceDir: t.TempDir()},
		Deps:      deps,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := env.Metrics
	if m["gates_passed"] != 2 || m["gates_total"] != 3 || m["judge_total"] != 8 || m["downstream_findings"] != 4 {
		t.Fatalf("composite metrics wrong: %+v", m)
	}
	if len(env.Verdicts) != 1 {
		t.Fatalf("judge verdicts must pass through: %+v", env.Verdicts)
	}
}

type downstreamReviews struct{ count int }
func (d downstreamReviews) GetByBuild(int) ([]eval.ReviewRecord, error) { return nil, nil }
func (d downstreamReviews) ListByTicket(int) ([]eval.ReviewRecord, error) {
	ids := make([]string, d.count)
	for i := range ids { ids[i] = "f" }
	return []eval.ReviewRecord{{FindingIDs: ids}}, nil
}
```

- [ ] **Run** `go test ./agent/benchscore/eval/ -run ImplementComposite` — expect compile failure.
- [ ] **Write `agent/benchscore/eval/implement.go`:**

```go
package eval

import (
	"context"

	"github.com/concourse/concourse/agent/benchscore"
	"github.com/concourse/concourse/agent/harvest"
	"github.com/concourse/concourse/agent/schema"
)

// ImplementCompositeVersion bumps when the composition changes.
const ImplementCompositeVersion = 1

// ImplementCompositeEvaluator = gates + judge + downstream-finding count
// (spec §4 / Task 1 formula). RunGates and Judge are seams so tests avoid
// shelling go-build / the claude CLI.
type ImplementCompositeEvaluator struct {
	RunGates func(workspaceDir string) (passed, total int, err error)
	Judge    Evaluator
	Policy   harvest.GatePolicy // default gates (build/test/lint, scope=full)
}

func NewImplementCompositeEvaluator() *ImplementCompositeEvaluator {
	e := &ImplementCompositeEvaluator{
		Policy: harvest.GatePolicy{Gates: []harvest.Gate{
			{Gate: "build", Scope: "full"}, {Gate: "test", Scope: "full"}, {Gate: "lint", Scope: "full"},
		}},
	}
	e.RunGates = e.defaultRunGates
	return e
}

func (e *ImplementCompositeEvaluator) defaultRunGates(ws string) (int, int, error) {
	outcomes, err := harvest.RunGates(e.Policy, ws, (*schema.EventWriter)(nil))
	if err != nil {
		return 0, 0, err
	}
	passed := 0
	for _, o := range outcomes {
		if o.Status == "ok" {
			passed++
		}
	}
	return passed, len(e.Policy.Gates), nil
}

func (e *ImplementCompositeEvaluator) Evaluate(ctx context.Context, in EvalInput) (benchscore.ScoreEnvelope, error) {
	metrics := map[string]float64{}

	passed, total, gerr := e.RunGates(in.Candidate.WorkspaceDir)
	if gerr != nil {
		return benchscore.ScoreEnvelope{}, gerr
	}
	metrics["gates_passed"] = float64(passed)
	metrics["gates_total"] = float64(total)

	var verdicts []benchscore.Verdict
	if e.Judge != nil {
		jenv, jerr := e.Judge.Evaluate(ctx, in)
		if jerr != nil {
			return benchscore.ScoreEnvelope{}, jerr
		}
		for k, v := range jenv.Metrics {
			metrics[k] = v // judge_total, judge_max, cost_usd, turns
		}
		verdicts = jenv.Verdicts
	}

	// downstream-review finding count: join by ticket_id where present.
	if in.Fixture.TicketID > 0 && in.Deps.Reviews != nil {
		revs, err := in.Deps.Reviews.ListByTicket(in.Fixture.TicketID)
		if err == nil && len(revs) > 0 {
			n := 0
			for _, r := range revs {
				n += len(r.FindingIDs)
			}
			metrics["downstream_findings"] = float64(n)
		}
		// absent downstream review -> metric omitted (unmeasured != zero).
	}

	return benchscore.ScoreEnvelope{Metrics: metrics, Verdicts: verdicts}, nil
}
```

(This is the **one** file that imports `agent/harvest` — for `RunGates`/`GatePolicy`/`Gate`, which are gate machinery, not judge. Because `implement.go` is `package eval`, this makes the **whole `agent/benchscore/eval` package** a transitive importer of `agent/harvest` — the single, deliberate, acknowledged harvest edge. That is consistent with (not contradicted by) the judge factoring: Task 3 bought the *judge* a harvest-free import surface — a judge-only consumer never pulls `gates.go`/`runner.go`/`evidence.go` — but it never claimed the eval package would be harvest-free, and it cannot, because gates live in `agent/harvest`. This is why the plan's earlier "the bench never imports `agent/harvest`" wording was corrected to "the bench's *judge path* never imports `agent/harvest`" (Task 1). If A2 prefers gates to run in-pod as a task step rather than in-runner (Open Decision D8/D2), the `RunGates` seam lets it inject that without changing this file.)

- [ ] **Run** `go test ./agent/benchscore/eval/ -run ImplementComposite` — expect pass.
- [ ] **Commit:** `git add agent/benchscore/eval && git commit -m "feat(bench): implement-composite evaluator - gates + judge + downstream findings"`

---

### Task 13: The plan-grounding evaluator + the implementor-variance later-slice seam

Deterministic plan grounding (cited `file:line` exist in the restored workspace) + the declared-but-not-built implementor-variance seam. Plain `testing` with a real temp workspace.

**Files:**
- Create: `agent/benchscore/eval/plan_grounding.go`
- Create: `agent/benchscore/eval/implementor_variance.go` (seam only)
- Test: `agent/benchscore/eval/plan_grounding_test.go`

**Steps:**

- [ ] **Write the failing test** `agent/benchscore/eval/plan_grounding_test.go`:

```go
package eval_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/agent/benchscore/eval"
)

func TestPlanGroundingResolvesCitationsAgainstWorkspace(t *testing.T) {
	ws := t.TempDir()
	// a file with 20 lines
	if err := os.WriteFile(filepath.Join(ws, "real.go"), []byte(twentyLines()), 0o644); err != nil {
		t.Fatal(err)
	}
	// candidate plan cites real.go:10 (ok), real.go:999 (past EOF), gone.go:3 (missing)
	plan := "See `real.go:10` and `real.go:999` and `gone.go:3`."

	ev := eval.NewPlanGroundingEvaluator()
	env, err := ev.Evaluate(context.Background(), eval.EvalInput{
		Fixture:   eval.Fixture{ID: 1, StepKind: "plan"},
		Candidate: eval.Candidate{WorkspaceDir: ws, ResultsJSON: []byte(`{"result":` + jsonQuote(plan) + `}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := env.Metrics["grounding"]; got < 0.33 || got > 0.34 {
		t.Fatalf("grounding = %v, want ~0.333 (1 of 3)", got)
	}
	if env.Metrics["citations"] != 3 || env.Metrics["dangling"] != 2 {
		t.Fatalf("counts: %+v", env.Metrics)
	}
}

func TestImplementorVarianceIsADeclaredLaterSlice(t *testing.T) {
	ev := eval.NewImplementorVarianceEvaluator()
	env, err := ev.Evaluate(context.Background(), eval.EvalInput{Fixture: eval.Fixture{StepKind: "plan"}})
	// the seam exists and is registrable, but running it is an explicit,
	// visible "not built yet" (never a silent gap).
	if err == nil {
		t.Fatal("implementor-variance must return an explicit not-built error (declared later slice)")
	}
	if env.Metrics != nil && len(env.Metrics) != 0 {
		t.Fatal("later-slice evaluator must emit no metrics")
	}
}
```

(`twentyLines`/`jsonQuote` are one-line test helpers.)

- [ ] **Run** `go test ./agent/benchscore/eval/ -run 'PlanGrounding|ImplementorVariance'` — expect compile failure.
- [ ] **Write `agent/benchscore/eval/plan_grounding.go`:**

```go
package eval

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/concourse/concourse/agent/benchscore"
)

// PlanGroundingVersion bumps when the citation grammar or resolution rule
// changes.
const PlanGroundingVersion = 1

// citationRe matches `path/to/file.ext:NN` citations (optionally backticked).
var citationRe = regexp.MustCompile("`?([\\w./-]+\\.\\w+):(\\d+)`?")

type PlanGroundingEvaluator struct{}

func NewPlanGroundingEvaluator() *PlanGroundingEvaluator { return &PlanGroundingEvaluator{} }

// Evaluate parses the candidate plan's file:line citations and checks each
// resolves to an existing file with at least that many lines, against the
// restored workspace. Mechanizes the human plan-verifier prompts.
func (e *PlanGroundingEvaluator) Evaluate(ctx context.Context, in EvalInput) (benchscore.ScoreEnvelope, error) {
	planText := in.Candidate.PlanText()
	matches := citationRe.FindAllStringSubmatch(planText, -1)

	total := len(matches)
	dangling := 0
	var danglingList []benchscore.Verdict
	for _, m := range matches {
		file, lineStr := m[1], m[2]
		line, _ := strconv.Atoi(lineStr)
		if !fileHasLine(filepath.Join(in.Candidate.WorkspaceDir, file), line) {
			dangling++
			danglingList = append(danglingList, benchscore.Verdict{Name: file + ":" + lineStr, Score: 0, Max: 1, Rationale: "dangling citation"})
		}
	}
	metrics := map[string]float64{"citations": float64(total), "dangling": float64(dangling)}
	if total > 0 {
		metrics["grounding"] = float64(total-dangling) / float64(total)
	}
	return benchscore.ScoreEnvelope{Metrics: metrics, Verdicts: danglingList}, nil
}

func fileHasLine(path string, line int) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		n++
		if n >= line {
			return true
		}
	}
	return n >= line
}
```

- [ ] **Add `PlanText()` to `agent/benchscore/eval/evaluator.go`'s `Candidate`** (the plan citation text is the step's `result` field of results.json):

```go
// PlanText extracts the plan prose from the candidate results.json
// {"result": "..."} for grounding checks. Falls back to the raw bytes.
func (c Candidate) PlanText() string {
	var r struct {
		Result string `json:"result"`
	}
	if len(c.ResultsJSON) > 0 && json.Unmarshal(c.ResultsJSON, &r) == nil && r.Result != "" {
		return r.Result
	}
	return string(c.ResultsJSON)
}
```

(add `encoding/json` to that file's imports).

- [ ] **Write `agent/benchscore/eval/implementor_variance.go`** — the seam, not the body:

```go
package eval

import (
	"context"
	"fmt"

	"github.com/concourse/concourse/agent/benchscore"
)

// ImplementorVarianceVersion is 0 — a declared-but-unbuilt slice never has a
// shippable version (spec §4 "plan (later slice)"). The seam exists so the
// registry, the results table, and a future supervisor already know the
// name; the body — fix the plan, run k cheap implementors, score by mean AND
// variance of downstream gates/judge — is out of scope for this plan.
const ImplementorVarianceVersion = 0

const ImplementorVarianceName = "plan-implementor-variance"

type ImplementorVarianceEvaluator struct{}

func NewImplementorVarianceEvaluator() *ImplementorVarianceEvaluator {
	return &ImplementorVarianceEvaluator{}
}

func (e *ImplementorVarianceEvaluator) Evaluate(ctx context.Context, in EvalInput) (benchscore.ScoreEnvelope, error) {
	return benchscore.ScoreEnvelope{Metrics: map[string]float64{}},
		fmt.Errorf("implementor-variance evaluator is a declared later slice (spec §4); not built")
}
```

- [ ] **Run** `go test ./agent/benchscore/eval/ -run 'PlanGrounding|ImplementorVariance'` — expect pass.
- [ ] **Commit:** `git add agent/benchscore/eval && git commit -m "feat(bench): plan-grounding evaluator + implementor-variance later-slice seam"`

---

### Task 14: Register the built-in evaluators + wire LLM evaluators as versioned workflow definitions

Assemble the default registry (deterministic evaluators by version constant; the judge/LLM evaluators resolvable through `workflow.Store`) so A2's runner resolves a pinned `(name, version)` for any `step_kind`. Plain `testing`.

**Files:**
- Create: `agent/benchscore/eval/builtins.go`
- Test: `agent/benchscore/eval/builtins_test.go`

**Steps:**

- [ ] **Write the failing test** `agent/benchscore/eval/builtins_test.go`:

```go
package eval_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/agent/benchscore/eval"
)

func TestDefaultRegistryHasEveryStepKindDefault(t *testing.T) {
	reg := eval.DefaultRegistry()
	// workflow is intentionally excluded: DefaultEvaluator("workflow")=="" —
	// workflow cells are scored off outcome tables (A2/C), not by a B evaluator.
	for _, kind := range []string{"review", "implement", "plan"} {
		name := eval.DefaultEvaluator(kind)
		ev, ver, err := reg.Resolve(name, 0)
		if err != nil || ev == nil || ver <= 0 {
			t.Fatalf("default evaluator %q for %q unresolved: ver=%d err=%v", name, kind, ver, err)
		}
	}
}

// No unbacked default (finding 4/12, D4): every NON-empty name DefaultEvaluator
// returns must resolve in DefaultRegistry, and "workflow" must map to "" — the
// default map has no hole that Resolve then rejects as "unknown evaluator".
func TestDefaultMapHasNoUnbackedEntry(t *testing.T) {
	reg := eval.DefaultRegistry()
	for _, kind := range []string{"review", "implement", "plan", "workflow", "nonsense"} {
		name := eval.DefaultEvaluator(kind)
		if name == "" {
			continue // no B evaluator for this kind (workflow/unknown) — not a hole
		}
		if _, _, err := reg.Resolve(name, 0); err != nil {
			t.Errorf("DefaultEvaluator(%q)=%q is unbacked by DefaultRegistry: %v", kind, name, err)
		}
	}
	if got := eval.DefaultEvaluator("workflow"); got != "" {
		t.Errorf("workflow default must be \"\" (owned by A2/C, not registered by B), got %q", got)
	}
}

// The REAL determinism canary (finding 19): a CONCRETE deterministic evaluator
// over a fixed input must yield byte-identical metrics across two runs — the
// negative-control contract (an identical-to-baseline variant reliably ties).
// plan-grounding is pure over a temp workspace (no DB, no LLM), so it is the
// canary subject; this test FAILS if plan-grounding ever becomes
// nondeterministic (unlike the Task-6 echo-func plumbing test).
func TestBuiltinEvaluatorsAreDeterministic(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "real.go"),
		[]byte("package p\n\nfunc a() {}\nfunc b() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ev := eval.NewPlanGroundingEvaluator()
	in := eval.EvalInput{
		Fixture:   eval.Fixture{ID: 1, StepKind: "plan"},
		Candidate: eval.Candidate{WorkspaceDir: ws, ResultsJSON: []byte(`{"result":"see real.go:2 and gone.go:9"}`)},
	}
	first, err := ev.Evaluate(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ev.Evaluate(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(first.Metrics) // Go marshals map keys sorted -> stable
	b, _ := json.Marshal(second.Metrics)
	if string(a) != string(b) {
		t.Fatalf("plan-grounding must be byte-identical across runs (control contract): %s vs %s", a, b)
	}
}

func TestBuiltinVersionsArePinnedNotZero(t *testing.T) {
	reg := eval.DefaultRegistry()
	for _, name := range []string{"review-precision", "review-recall", "implement-composite", "plan-grounding"} {
		if v := reg.Version(name); v <= 0 {
			t.Errorf("built-in evaluator %q must carry a positive version, got %d", name, v)
		}
	}
	// the later-slice seam is registered at version 0 (declared, not built).
	if reg.Version(eval.ImplementorVarianceName) != 0 {
		t.Errorf("implementor-variance must be registered at version 0 (declared later slice)")
	}
}
```

- [ ] **Run** `go test ./agent/benchscore/eval/ -run 'Registry|Builtin|DefaultMap|Deterministic'` — expect compile failure.
- [ ] **Write `agent/benchscore/eval/builtins.go`:**

```go
package eval

// DefaultRegistry wires the built-in deterministic evaluators at their
// pinned versions plus the declared-but-unbuilt implementor-variance seam.
// LLM evaluators (the judge) are registered by A2's runner from a
// workflow.Store resolution (evaluators are workflow definitions,
// principle 1) — this function stays free of the workflow/db packages so it
// is trivially unit-testable; A2 calls reg.Register("implement-judge", ver,
// NewJudgeEvaluator(cfg)) after resolving the definition.
func DefaultRegistry() *Registry {
	reg := NewRegistry()
	reg.Register("review-precision", ReviewPrecisionVersion, NewReviewPrecisionEvaluator())
	reg.Register("review-recall", ReviewRecallVersion, NewReviewRecallEvaluator())
	reg.Register("implement-composite", ImplementCompositeVersion, NewImplementCompositeEvaluator())
	reg.Register("plan-grounding", PlanGroundingVersion, NewPlanGroundingEvaluator())
	reg.Register(ImplementorVarianceName, ImplementorVarianceVersion, NewImplementorVarianceEvaluator())
	return reg
}
```

- [ ] **Document the LLM-evaluator resolution seam** (prose in this task, no extra code here): an evaluator whose name resolves to a `workflow.Store` definition (e.g. `implement-judge`, `review-judge`) is a **versioned workflow definition** — A2's runner resolves `(name, version)` via `store.Get(name, version)` (or `LiveVersions` for the two-field default), constructs `NewJudgeEvaluator` from the definition's rubric, and `reg.Register`s it with that resolved workflow version as the pinned `evaluator_version`. That keeps the "evaluator IS a workflow definition" invariant (principle 1) for the LLM tier while the deterministic tier carries Go version constants. B ships the `JudgeEvaluator` + the constants; A2 owns the `workflow.Store` resolution (it already resolves workflow versions for variants).
- [ ] **Run** `go test ./agent/benchscore/eval/` (full package) — expect all green.
- [ ] **Commit:** `git add agent/benchscore/eval && git commit -m "feat(bench): default evaluator registry - pinned built-ins + LLM-via-workflow-store seam"`

---

### Task 15: Live scaffold — injector prompt-shape guard (real replay-scored cell deferred to A2's live wiring)

The house lesson (MEMORY.md): a fake clientset cannot exercise the sidecar/pod transport; the judge evaluator shells a real `claude` CLI. **Scope (findings 17/18):** the *real* end-to-end proof — one replay-scored cell producing an `agent_bench_scores` row, and a real `claude` drive of the injector confirming no AUP refusal — rides A2's live replay wiring (there is no `corpus.RunInjector` yet, and no replay runner in B). This task ships an **honest scaffold**: a compiling, asserting, live-tagged guard on the injector prompt shape, so the file is real (not a non-asserting placeholder) while the CLI drive lands with A2.

**Files:**
- Create: `agent/benchscore/eval/live_score_test.go` (`//go:build live`, plain Go, not Ginkgo — the `atc/worker/jetbridge/live_*_test.go` pattern)

**Steps:**

- [ ] **Write `agent/benchscore/eval/live_score_test.go`** (build-tagged `live`; skips unless `BENCH_LIVE=1`). It (1) builds a tiny known-clean diff, calls `corpus.BuildInjectorPrompt`, and asserts the **AUP-refusal check** — a real `claude` invocation with the CI-context-first prompt returns a parseable `InjectionResult` and is **not** refused (this is the #23/#24 regression testbed the spec §5 names); (2) runs `NewJudgeEvaluator` against a real temp workspace + diff with the platform credential in-env and asserts a non-error `ScoreEnvelope` with `judge_total > 0`. No cluster is required for (1)/(2) — they shell the CLI locally; the sidecar-transport half rides A2's live replay test.

```go
//go:build live

package eval_test

import (
	"os"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/benchscore/corpus"
)

// TestLiveInjectorPromptNotRefused is a SCAFFOLD (finding 17): under the live
// tag it guards the injector prompt SHAPE (non-empty + CI/evaluation-framed).
// The real AUP-refusal regression — driving a real `claude` with this prompt and
// asserting a parseable InjectionResult (spec §5) — and the real replay-scored
// cell land with A2's replay wiring (no corpus.RunInjector exists yet). It
// imports ONLY what it uses (no unused judge/eval imports) so `go test -tags
// live` compiles, and it asserts (not a non-asserting placeholder). Kept behind
// BENCH_LIVE=1 so make test-quick never shells claude.
func TestLiveInjectorPromptNotRefused(t *testing.T) {
	if os.Getenv("BENCH_LIVE") != "1" {
		t.Skip("set BENCH_LIVE=1 (live-tagged scaffold)")
	}
	prompt := corpus.BuildInjectorPrompt(corpus.InjectRequest{
		Repo: "tdmtrader/concourse", Diff: "diff --git a/x.go b/x.go\n@@ -1 +1 @@\n-a\n+b\n",
		Classes: corpus.BugClasses[:2],
	})
	if strings.TrimSpace(prompt) == "" {
		t.Fatal("injector prompt must be non-empty")
	}
	if !strings.Contains(prompt, "CI test corpus") && !strings.Contains(prompt, "evaluation") {
		t.Fatal("injector prompt must be CI/evaluation-framed (AUP lesson, spec §5)")
	}
}
```

(The real `claude` CLI drive — `corpus.RunInjector` + a replay-scored cell — lands with A2's replay wiring; this file is the compiling, asserting prompt-shape guard. Keep it behind `BENCH_LIVE=1` so `make test-quick` never shells `claude`.)

- [ ] **Run (local, opt-in):** `BENCH_LIVE=1 go test -tags live -run '^TestLive' ./agent/benchscore/eval/ -v -count=1` — expect the prompt guard green; the CLI drive is validated against theborg with the platform credential once A2 lands.
- [ ] **Commit:** `git add agent/benchscore/eval && git commit -m "test(bench): live-tagged injector prompt-shape scaffold (BENCH_LIVE; real CLI drive rides A2)"`

---

### Task 16: Full-suite green + `make test-quick`

**Files:** none (verification + a final import sweep).

**Steps:**

- [ ] **Run the workstream's suites:**

```bash
pg_isready                                                   # PostgreSQL for atc/db + migration specs
go test ./agent/benchscore/... ./agent/api/benchcorpus/           # envelope, evaluators, registry, corpus, handler (plain testing)
go test ./agent/harvest/... ./agent/harvest/judge/           # factoring: judge subpackage + harvest aliases green
ginkgo --focus='AgentBenchScoresFactory' ./atc/db/           # score factory
ginkgo --focus='Legacy Database Upgrade' ./atc/db/migration/ # migration 1773106102 walk
ginkgo ./atc/wrappa/ ./atc/api/accessor/                     # route auth wiring (exhaustive-switch guard)
go build ./...                                               # aliases + new packages compile everywhere
```

- [ ] **Run the per-merge gate:** `make test-quick` (unit + ci-agent, PostgreSQL) — expect green. **Never** pass `--race` (CLAUDE.md: parallel-compilation failures).
- [ ] **Confirm no accidental coupling:** `go test ./agent/harvest/judge/ -run Import` (Task 3's guard) stays green — the bench-invocable judge must not re-import `agent/harvest`.
- [ ] **Commit** any test-only fixups: `git commit -am "test(bench): workstream suites green under make test-quick"`

---

## Execution notes

**Running this workstream's test suite (all green before close-out):**

```bash
pg_isready
go test ./agent/benchscore/ ./agent/benchscore/eval/ ./agent/benchscore/corpus/ ./agent/api/benchcorpus/
go test ./agent/harvest/... ./agent/harvest/judge/
ginkgo --focus='AgentBenchScoresFactory' ./atc/db/
ginkgo --focus='Legacy Database Upgrade' ./atc/db/migration/
ginkgo ./atc/wrappa/ ./atc/api/accessor/
ginkgo ./go-concourse/concourse/                # RegisterSyntheticFixtures client
go build ./...
# NOTE: no `ginkgo ./fly/integration/ --focus="agent bench corpus"` here — the
# `fly agent bench corpus` verb + its integration spec are DEFERRED to the D3
# dispatch slice (finding 18); this plan authors no such spec, so do not list a
# suite that cannot run.
```

Per CLAUDE.md: unit tests run in parallel with `-p`; **never** pass `--race`. `agent/benchscore/*` and `agent/api/benchcorpus` are plain `testing` (matching `agent/api/reviews`/`agent/api/outcomes`) so they are fast and hermetic. The `plan_grounding_test.go` writes a temp file and reads it back — no network. The judge evaluator's real-CLI path is gated behind `BENCH_LIVE=1` and `//go:build live`, so it never runs under `make test-quick`.

**Merge order (house spine rule — one migration per push):** B's `1773106102` must land **after** A1's `1773106100` and A2's `1773106101` (ascending == FK-dependency order: `scores.cell_id → cells.id`, `cells.fixture_id → fixtures.id`). If B's migration walk fails with `relation "agent_bench_cells" does not exist`, A2 has not merged — do not force B ahead. The judge factoring (Task 3) has **no** migration and can land independently of A1/A2 (it is a pure refactor); land it early to unblock B's evaluator tasks and to de-risk the cross-package aliases before the table work.

**Live-test requirements (theborg pattern per CLAUDE.md / MEMORY.md):**
- The judge evaluator and the corpus injector both shell a real `claude` CLI funded by the platform credential (`CLAUDE_CODE_OAUTH_TOKEN` in-env, §8.2). Validate locally with `BENCH_LIVE=1` — no cluster needed for the CLI half. **This in-env credential is a local-dev convenience only:** per D8 as amended (2026-07-19), v1 replay scoring is web-side deterministic-tier only; when the judge-in-pod evaluator lands as a rendered step, the platform token lives in the pod, never in the ATC web process (§9, no new trust surface).
- The full replay→score loop (sidecar transport + reconcile-time scoring) is A2's live test; B's evaluators plug into it. When A2's throwaway-namespace replay lands, confirm one review cell produces an `agent_bench_scores` row with a `precision` metric and one implement cell produces `gates_*`/`judge_*`/`downstream_findings`. Use a THROWAWAY namespace (never `cicd`/`concourse`).
- The **AUP refusal regression** (spec §5) is the corpus builder's own first test: run `corpus.BuildInjectorPrompt` through a real `claude` and confirm a parseable `InjectionResult` (no vocabulary refusal). Re-run it whenever the injector prompt changes.

**Rollback notes for the risky diffs:**
- **Task 3 (judge factoring)** is the only change touching landed cross-package code. It is behaviour-preserving (a file-move + type aliases); the `go build ./...` + the import guard are the proof. If a caller breaks post-merge, the aliases in `agent/harvest/policy.go` are the single revert point — restore the moved types into `agent/harvest` and drop the subpackage. No data, no migration, no runtime path changes.
- **Migration `1773106102`** is additive (one table + two indexes); `DROP TABLE agent_bench_scores` fully reverses it. It touches no existing table.
- **The `RegisterSyntheticFixtures` route** is inert until the corpus builder calls it; shipping the code without running a corpus is a no-op. It writes only `source:'synthetic'` fixtures (pinned, retention-exempt) — it cannot corrupt production capture rows.
- **Evaluators never fail a production or replay step** (principle 2): `eval.ScoreCell` turns any evaluator error into a stored `status:'error'` score, never a panic. A2's runner treats a score-write error as a cell-level `error`, not a crash.

---

## Open decisions

1. **D1 — per-finding review-precision join (resolves spec open Q4).** **Chosen:** disambiguate through the fixture's `build_id` (join `fixture.build_id → agent_reviews.build_id`, then `agent_feedback` on `(repo, commit_sha, finding_id)`); **no** `review_build_id`/`build_id` column added to `agent_feedback`, **no** B migration beyond `1773106102`. Rationale: keeps B at one migration, avoids a write-path change to the landed feedback handler, and honors principle 3 (the bench writes no columns onto label tables). **Residual v1 limit (documented, surfaced as `metrics.ambiguous_findings`):** a commit reviewed by two builds whose nondeterministic judge assigned the same `finding_id` to different content. **Alternative (rejected here, revisit if ambiguity proves common):** add nullable `review_build_id` to `agent_feedback` + populate it from the feedback UI's known build — a cleaner key but a second migration and a landed-handler change.
2. **D2 — gates in-runner vs in-pod for `implement-composite`.** The `RunGates` seam (Task 12) lets the caller choose where gates run: server-side over the restored workspace, or an in-pod task step (per-repo commands via dev-mcp, wave 3). **B ships the seam, not a hard default** — the choice is subsumed by D8 (amended 2026-07-19 post-review: web-side deterministic tier v1 — gates run server-side over the restored workspace at reconcile). If a later slice wires gates in-pod, it injects that through the seam with no change to `implement.go`. Revisit alongside D8.
3. **D3 — how the corpus workflow's output reaches `RegisterSyntheticFixtures`.** Two candidates: (a) `fly agent bench corpus` dispatches the workflow, waits, reads the `InjectionResult` artifact, and POSTs the registration (client-driven, simplest, chosen for v1); (b) a B-owned RunnableComponent harvests completed corpus runs and registers server-side (supervisor-friendlier, no fly in the loop). The route is the durable seam either way; (b) is a later slice.
4. **D4 — workflow-cell default evaluator (RESOLVED: not B's; no registry hole).** `DefaultEvaluator("workflow")` returns **`""`** (not `"outcome-metrics"`). Workflow cells are end-to-end tickets (A2/plan-14's path) whose metrics come from the existing outcome/scorecard tables, **not** an `eval.ScoreCell` replay — so B registers **no** `workflow` evaluator, and `DefaultRegistry()` never advertises a name it cannot resolve (the "one table answers it, default map has no hole" guardrail holds; verified by `TestDefaultMapHasNoUnbackedEntry`, Task 14). The earlier draft had `DefaultEvaluator` return `"outcome-metrics"` and D4 claim "B registers the name as a thin pass-through" — that was **internally inconsistent** (no task registered it; Task 14's test excludes `workflow`; `Resolve` would reject it as "unknown evaluator"). Corrected here (finding 4/12): the workflow-cell scoring path is **owned by A2/C** off outcome tables, not by a B evaluator. If a genuine workflow-cell *evaluator* is ever wanted, it is a new A2/C task with its own registered name+version — not a silent default. Owner confirmable at S-8.
5. **D5 — bug-class taxonomy growth + retirement cadence.** Task 10 seeds six classes from SUPERVISION.md's recurring findings. The leftward-migration rule (a class retires — `Retired=true` — when a deterministic gate subsumes it) needs a periodic review pass; not automated in v1. Who owns the review cadence (retrospective agent? a scheduled task?) is open.
6. **D6 — score `rationale_ref` semantics + blob mechanics (documented skeleton amendment).** On `status='ok'` rows `rationale_ref` is empty or a blob handle when rationale exceeds the inline bound (L-3 512KiB precedent); the store mechanics (PVC vs registry-blob) are A1's fixture-store decision (spec open Q1/Q2) and B stores the handle string only — confirm B and A1 share one blob store. **On `status='error'` rows `rationale_ref` carries the evaluator's inline error reason** (there is no numeric-free error column in the frozen DDL and `metrics` is numbers-only). This is a **status-gated overload raised as an explicit §1.B amendment** (Task 1, finding 6/14), NOT a silent drift: every consumer (A2 results, C rollup) MUST check `status` and only dereference `rationale_ref` as a `blob://` URI when `status='ok'`.
7. **D7 — bench naming through S-8 (spec open Q9).** All `agent_bench_scores` / `review-precision` / `implement-composite` / `plan-grounding` / `/bench/fixtures/synthetic` names are **provisional**; a rename is a coordinated pre-freeze amendment before the fly verb + route path + table name freeze.
8. **D8 — WHERE the evaluate runs (execution + credential locus). (Amended 2026-07-19 post-review — supersedes the earlier "pipeline `evaluate` step" default.)** **Pinned default: web-side deterministic tier v1; judge-in-pod later.** A2's `StepExecutor.Reconcile`, on a cell's terminal `ok`, fetches the candidate output refs and calls `eval.ScoreCell(ctx, reg, scoresFactory, cell.ID, …)` with `EvalDeps` wired to real stores — mirroring the platform's existing server-side ingestion pattern (production steps are also scored/ingested web-side). Consequently the rendered replay plan is **`[restore → step-under-test]` — NO evaluate step in v1**, and no `AGENT_BENCH_CELL_ID` env var exists (the cell id is in hand at reconcile; the caller passes it as a Go parameter). The **LLM-judge evaluator rides a later rendered-step slice**: when it lands, it runs as a pod agent step — metered, budgeted, platform credential in the pod, never the ATC web process shelling `claude` (§9) — and pod-side evaluators will then need a cell-id env + an ingestion route (deferred; noted in A2 §1.12.3). A `ScoreCell` error at reconcile sets the cell `status='error'` with reason `evaluate-failed` — never `failed` (the variant is not to blame). The `BENCH_LIVE=1` local CLI path (Task 15) is a **dev-only convenience** for exercising the prompt/judge locally — it is *not* the production execution model. D2 (gates in-runner vs in-pod) is a sub-case of this decision. **Second amendment (same day): the v1 web-side tier covers `review`/`plan` ONLY — the implement-composite embeds gates (`go build/test` over the workspace) + the judge, both pod-side; running either in the ATC web process is wrong on resources and on credentials. Implement cells therefore go `ok` unscored in v1 (A2 annotates them), implement auto-controls skip (`implement-scoring-deferred`), and implement scoring lands whole with the judge-in-pod slice.**
