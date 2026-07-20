# Bench A2 — Replay Runner + Experiment Harness + API/fly Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

- **Descends-from:** `docs/superpowers/specs/2026-07-19-agent-bench-design.md` (§3 replay runner, §6 experiment harness, "The basic experience"); the FROZEN cross-track contract skeleton; `11-dispatch.md` (render library + DispatchOne), `14-process-intel-experiments.md` §1.12.2 (runner-dispatches-tickets, preserved verbatim), `08-platform-mcp-hitl.md` (#40 stub-ATC).
- **Consumes (read-only):** A1 `agent_step_fixtures` fixture contract (registry rows + blob-bundle bytes + `Store` list/tag/pin/bundle surface — admission policy is A2-owned, amended 2026-07-19 post-review); B `agent_bench_scores` score envelope (§2 skeleton) via a narrow `ScoreReader`, plus B's evaluator library `agent/benchscore/eval` called web-side at reconcile.
- **Tasks:** 19 (Tasks 1–18 plus Task 6a, inserted 2026-07-19 post-review)
- **Complexity/Risk:** High — introduces a new one-step template-pipeline renderer (must reuse `render.go` additively, NEVER touch its refusal chain), a new RunnableComponent with a budget envelope, a deployable stub-ATC binary that does not exist today, and the full six-touchpoint API+fly surface. Live-cluster sidecar transport is unexercisable by the fake clientset (SC-11 house lesson) → one mandatory throwaway-namespace theborg test.
- **Migrations:** **`1773106101`** — `agent_bench_experiments` + `agent_bench_cells` (ONE file, both tables — a single multi-statement `.up.sql`; the `go:embed` migrator applies every statement in a file, so two `CREATE TABLE`s in one migration is ordinary SQL on its own merits. NB: plan-14 did NOT put its two tables in one file — it split them across `1773106101`+`1773106102` (one table each); A2's single-file choice stands on the migrator's semantics, not a borrowed precedent). On-disk head is `1773106091` (`create_agent_settings`). A2's `cells.fixture_id` FKs A1's `agent_step_fixtures` (`1773106100`), so **`1773106100` MUST land before `1773106101`** (ascending == referential-dependency order; never push the two together — house spine rule, one migration per push).

**Goal:** Build the REPLAY + EXPERIMENT execution engine of the bench: a one-step template pipeline (`restore → step-under-test` — no evaluate step in v1; scoring runs web-side at cell reconcile, amended 2026-07-19 post-review) that replays a captured fixture against a step variant under a stubbed platform surface, an experiment harness (tables + polling runner + budget envelope) that expands a two-field spec into `(fixture × variant × rep)` cells with auto-synthesized negative controls, and the `POST/GET /api/v1/agent/bench/*` API + `fly agent bench run|results|fixtures` verbs.

**Architecture:** A replay cell renders to an ordinary `db.PipelineRun` via a NEW `agent/benchrunner/replay.RenderReplay` that reuses dispatch's exported `RenderAgentStep` **additively** (it never calls `dispatch.Render` and never touches its refusal chain, `render.go:152-212`) and assembles its own `atc.Config`. Isolation comes from a NEW deployable stub-ATC binary (`agent/benchstub` + `cmd/benchstub`) — the #40 contract-test mux extracted from its `*testing.T` lifecycle — mounted as an inline sidecar on the step-under-test with `ask_human` disabled and writes absorbed. `agent_bench_experiments`/`agent_bench_cells` (migration `1773106101`) plus a `bench.Store` factory hold the matrix; `agent/benchrunner` is a polling RunnableComponent (never notify-only) that admits cells under the global daily cap + a per-experiment envelope, renders step cells and rides the REAL dispatcher for workflow cells (plan 14 §1.12.2 verbatim). Seven HTTP routes + a `go-concourse` client + a `fly agent bench` family follow the #36 six-touchpoint pattern.

**Tech Stack:** Go (plain `testing` for `agent/api/bench`, `agent/benchstub`, and `agent/benchrunner` — matching `agent/api/tickets`/`agent/api/outcomes`; Ginkgo/Gomega for `atc/db`, `agent/benchrunner/replay` golden files, `atc/wrappa`, `atc/api/accessor`), PostgreSQL migration + `agent_reviews_factory.go`-recipe factory, counterfeiter fakes, `text/template`-free additive render reuse, jessevdk/go-flags (fly + web flags), the RunnableComponent framework (`atc/component`).

---

## Context

**Charter (bench design §"Handoff briefs" A, the replay/harness slice; the FROZEN skeleton's A2 row).** A2 owns REPLAY + EXPERIMENT execution. It descends additively from the wave-3/4 dispatch + platform-mcp + budget surfaces and supersedes plan 14's M1 experiment substrate (its `agent_experiments`/`agent_experiment_runs` shapes are absorbed and generalized here; `agent_benchmark_cases` is NOT built — benchmark cases import as `source:'benchmark'` fixtures, A1's job).

Scope-in → task mapping (every charter item maps):

| scope_in item | Tasks |
|---|---|
| Replay renderer: one-step template pipeline `restore → step` (evaluate relocated to reconcile-time web-side scoring — amended 2026-07-19 post-review), reuse render library ADDITIVELY, never touch the refusal chain, #40 stub-ATC as read-only platform surface (write-absorption incl. cost posts; `ask_human` disabled; step cells create NO tickets) | 5 (stub-ATC binary), 6 (renderer), 9 (step-cell execution + reconcile-time scoring) |
| Overlay write path + full-bundle pinning (carve-out accepted from A1's Scope-out — 2026-07-19 post-review) | 6a |
| `agent_bench_experiments` + `agent_bench_cells` tables (generalize plan 14's experiments/experiment_runs) | 1 (addendum), 2 (migration), 3 (types), 4 (factory) |
| Experiment runner RunnableComponent (polling, never notify-only) + budget envelope under the global daily cap; workflow(end-to-end) cells ride the REAL dispatcher per plan 14 §1.12.2 verbatim | 8 (matrix + admission), 9 (step cells), 10 (workflow cells), 11 (controls + completion), 15 (component wiring) |
| API routes (`POST/GET /api/v1/agent/bench/experiments`, `.../results`, fixtures list/tag/pin) via six-touchpoint + fly `agent bench run\|results\|fixtures` | 12 (experiments), 13 (results), 14 (fixtures), 15 (routes/auth), 16 (client), 17 (fly) |
| Negative-control auto-synthesis (identical-to-baseline + degraded), three-way `ok\|failed\|error` cell taxonomy, skipped-budget honesty | 3 (taxonomy), 7 (control synthesis), 8 (skipped-budget), 11 (control verdicts) |
| Two-field basic experience = ACCEPTANCE BAR (revealed complexity) | 3 (defaults resolution), 12 (create handler), 17 (fly `run`) + the **Basic-experience guardrail** note below |

**Basic-experience guardrail (the simplicity contract — spec "The basic experience").** The minimal path MUST stay trivial: `fly agent bench run --step review --variant review-prompts@v5` produces `experiment N: 2 variants (v5, live) × <default open set> × controls; budget $<default> (default envelope)`. Two fields — `step_kind` and `variants` — are the only required inputs anywhere in this plan. Every other knob (fixtures, repetitions, evaluator pin, controls mode, budget) is a **default resolved at admission** (Task 3 `ResolveDefaults`, Task 8 admission), never a required argument and never on the two-field path. The `live` baseline is auto-included, controls auto-synthesize (Task 7), and the default open fixture set + default evaluator + default envelope are supplied server-side. Any task step, fly flag, or API field that forces the caller to supply a matrix, a holdout policy, an evaluator version, or a budget on the minimal path is a plan defect — those are drill-down/full-form only (Task 12 full-spec branch, Task 17 full flags). This note is the fresh-eyes check for every task that touches the spec surface.

**Prior waves (assumed LANDED exactly as 00-shared-contracts.md + the earlier plans define; verified at HEAD unless marked NEW):**

- **dispatch (wave 4):** `agent/dispatch.RenderAgentStep(in RenderInput, step workflow.Step) (atc.AgentStep, error)` @ `agent/dispatch/render.go:63` (exported per-step renderer — the additive reuse seam); `dispatch.RenderInput` struct (`Workflow workflow.Config`, `WorkflowName/Version/Hash`, `Ticket tickets.Ticket`, `Spec *tickets.Spec`, `PlanTasks []tickets.Task`, `ATCExternalURL`, `RepoBaseURL`) @ `render.go:37`; `dispatch.Render` @ `render.go:152` with the refusal chain `render.go:152-212` (**MUST NOT be touched**); `dispatch.TemplateSaver.SaveTemplate(name string, cfg atc.Config) (int, error)` and `dispatch.RunCreator.CreateRun(templatePipelineID int, params map[string]any, createdBy string) (db.PipelineRun, error)` @ `dispatch.go:58-65`; `dispatch.DispatchOne(ctx, deps Deps, ticketID int, dispatchedBy string) (Result, error)` @ `dispatch.go:125`; the `Dispatcher` RunnableComponent shape @ `dispatch/dispatcher.go` (polling-only, `Run(ctx) error`).
- **db (wave 3):** `db.PipelineRunFactory.CreateRun` @ `atc/db/pipeline_run_factory.go:132` (materializes `((run_id))`, fires the entry job `run`); `db.PipelineRun` interface @ `atc/db/pipeline_run.go:24` (`CheckComplete`, `Status`, `Number`, `CreatedBy`, …).
- **atc types:** `atc.AgentStep` @ `atc/steps.go:403` — has `Sidecars []SidecarSource` (`:422`), `Env TaskEnv` (`:429`), `Inputs`/`Outputs`; `atc.SidecarSource`/`SidecarConfig` @ `atc/sidecar.go:13,60` (inline `Config *SidecarConfig{Name,Image,Command,Args,Env []SidecarEnvVar,...}`); `atc.TaskStep`/`atc.TaskConfig` (busybox base64 idiom, `render.go:377,469`); `atc.Config{Template bool, Jobs, Resources}`.
- **budget (wave 2):** `budget.Checker` @ `agent/budget/budget.go:96` — `GlobalDailyRemaining() (Remaining, error)` (`:101`), `Remaining{LimitUSD, SpentUSD, RemainingUSD, Exhausted}` (`:85`), counterfeiter `budgetfakes.FakeChecker`.
- **workflow (wave 2):** `workflow.Config` @ `agent/workflow/config.go:10` (`SpecDelivery`, `Prompts`, `Steps []Step`, `Defaults`, …); `workflow.Step` @ `config.go:71`; `workflow.Store`/`Definition` (`Live(name)`, `Get(name, version)`) for variant + evaluator resolution.
- **tickets (wave 2):** `tickets.Store.Transition`, `tickets.Ticket`, `tickets.Spec`, `tickets.Task`, `tickets.StateDraft/StateQueued`, `tickets.MemoryStore`, `ticketsfakes.FakeStore`; `origin` field on create. **`origin` is CHECK-constrained** to `('web','fly','jira','retrospective')` (`atc/db/migration/migrations/1773106062_create_agent_tickets.up.sql:9-10`) and `tickets.ValidOrigin` (`agent/api/tickets/types.go:90`) allows only those four — there is **no `'bench'` origin**. A2 workflow cells therefore reuse the already-legal **`origin:'fly'`** (as plan 14 §1.12.2 does); `tickets.MemoryStore.Create` defaults empty→`'web'` and does NOT enforce the CHECK, so a `'bench'` value would pass MemoryStore tests but FAIL the production DB insert — a silent test-green/prod-red trap avoided by using `'fly'`. Bench tickets stay distinguishable by `created_by="experiment-<id>"` + their workflow linkage. A distinct `'bench'` origin is a FUTURE amendment requiring a ticket-core-owned CHECK+`ValidOrigin` extension (a separate migration — do NOT bundle it into A2's one migration-per-push).
- **platform-mcp (#40, wave 3):** `agent/platformmcp/contracttest.NewStubATC(t *testing.T, ticketID int)` @ `stub_atc.go:118` (the mux, currently `httptest`+`*testing.T`-bound, `:234`); `contracttest.StubToken = "cap1.0.contract-test-token"` (`:21`); the sidecar env pointers `ATC_EXTERNAL_URL`/`AGENT_PRINCIPAL_TOKEN`/`AGENT_TICKET_ID` @ `agent/platformmcp/config.go`.
- **six-touchpoint (wave 1+):** `atc/routes.go` Routes slice; `atc/wrappa/api_auth_wrappa.go:36` exhaustive switch with `default: panic` (`:271`) + `CheckAgentAuthorizationHandler` team-less tier (`:215-246`); `atc/api/accessor/roles.go`; `atc/api/handler.go:59` `NewHandler` + agent server map (`:161-187`); `go-concourse/concourse/agent_*.go`; `fly/commands/agent.go:6` `AgentCommand` (additive-merge convention) registered `fly/commands/fly.go:94`.

**Wave-mates (parallel, NOT landed — A2 consumes their contracts read-only):**
- **A1 fixtures** — owns `agent_step_fixtures` (migration `1773106100`), the blob store, and (per the six-touchpoint split) the fixture **row type** + a `Store` with read/`List(FixtureFilter)`/tag/pin + a **bundle fetch** `Bundle(id)` → `{Repo, BaseSHA string; Overlay, TicketSnapshot, Config []byte; Env EnvPins}`. **Admission POLICY (default sets, per-repo caps, recency) is A2-owned in `agent/benchrunner`, built atop A1's `List(FixtureFilter)` — `Admit` is NOT an A1 (or `bench.Fixtures`) method (amended 2026-07-19 post-review, honoring A1's Q6).** A2 consumes a NARROW subset via the `bench.Fixtures` consumer interface (Task 1) — A1's `db` factory satisfies it. A1's capture slice lands FIRST and alone (spec Sequencing); by the time A2 builds, `agent/api/fixtures` + `db.NewAgentStepFixturesFactory` exist.
- **B evaluators** — owns `agent_bench_scores` (migration `1773106102`, FKs `cells.id`), the judge factoring, the **score envelope** (§2 skeleton), and the evaluator library `agent/benchscore/eval`. A2 consumes it two ways (amended 2026-07-19 post-review): read-only via `bench.ScoreReader` (Task 1/13), and at reconcile — `StepExecutor.Reconcile` calls `eval.ScoreCell(ctx, reg, scoresFactory, cell.ID, …)` web-side with `EvalDeps` wired to real stores. The cell id is a Go parameter; there is NO evaluate step and NO env-var score seam in v1.

**Anchor caveat:** `Modify:` line anchors were verified on branch `jetbridge` near HEAD, but A1/B and other S-track work shift anchors in `atc/routes.go`, `atc/wrappa/api_auth_wrappa.go`, `atc/api/accessor/roles.go`, `atc/api/handler.go`, `atc/api/api_suite_test.go`, `atc/atccmd/command.go`, `atc/component.go`, and `atc/db/migration/legacy_upgrade_test.go`. Treat every anchor as "the location of the quoted code"; place additions adjacent to the named symbol (grep it), not at a literal line.

---

### Task 1: Wave-start contract addendum — A2's slice of the bench contract (experiments/cells DDL, replay+stub contract, runner/budget decisions, six routes, A1/B seams)

A2 owns `agent_bench_experiments`/`agent_bench_cells`, the replay renderer contract, the experiment-runner behavior, and the bench API/fly surface. Freeze all of it in `00-shared-contracts.md` before any code, where A1 (fixture consumer interface), B (score-write seam), and C (results shape) read it. This task DESCRIBES and INSERTS the addendum text; it edits only the contract doc.

**Files:**
- Modify: `docs/superpowers/plans/agentic-platform/00-shared-contracts.md` (insert `### 1.12.3` after plan 14's `### 1.12.2` region, before the next `### 1.13`; append the bench route rows to the §4.2 table after the last agent route; append a §11 Amendment-log entry at end of file)

**Steps:**

- [ ] Insert the `### 1.12.3` subsection (the authoritative A2 contract slice). It fixes the following, resolving skeleton open questions **3 (stub write-absorption), 7 (budget envelope default), 9 (degraded per step kind), 10 (one cells table), 12 (score status)** for A2's slice:

````markdown
### 1.12.3 Bench replay + harness addendum — owner: **bench-A2** (2026-07-19; consumers: A1 fixtures, B evaluators, C scorecards)

**Tables (migration `1773106101`, ONE file, both tables — a single multi-statement `.up.sql`, valid on the `go:embed` migrator's own semantics; NOT a plan-14 precedent — plan 14 split its two tables across `1773106101`+`1773106102`). Supersedes plan 14 `agent_experiments`/`agent_experiment_runs`.** DDL is the FROZEN skeleton §A2 with ONE coordinated amendment (below): `agent_bench_experiments` (`spec JSONB` = full §6 experiment spec, `budget_usd`, `status pending|running|complete|error|evaluator-suspect`, `control_status pending|pass|fail|none`) and `agent_bench_cells` (`fixture_id NOT NULL → agent_step_fixtures RESTRICT`, `variant`, `variant_version`, `control_role ''|baseline-clone|degraded`, `repetition`, mutually-exclusive `pipeline_run_id` (STEP cells) / `ticket_id` (WORKFLOW cells), `status pending|running|ok|failed|error|skipped-budget`, `skip_reason`, `env JSONB` (fixture-pinned + resolved runner/sidecar image tags, recorded at cell start — env-skew visibility, amended 2026-07-19 post-review), `UNIQUE(experiment_id, fixture_id, variant, variant_version, control_role, repetition)`). **SKELETON AMENDMENT (coordinate before Task 2 lands):** the frozen skeleton's key omitted `control_role`, but negative controls carry the SAME baseline variant name + variant_version (a `baseline-clone` is byte-identical to `live@V`), so the baseline cell, its baseline-clone, and its degraded clone all collapse to the same `(experiment_id, fixture_id, 'live', V, repetition)` tuple and the 2nd/3rd `AddCells` insert would hit a UNIQUE violation once admission resolves concrete versions. Adding the already-frozen `control_role` column to the key is the minimal fix (it is the exact dimension that distinguishes those rows). **One cells table for both kinds (open Q10 resolved):** step cells set `pipeline_run_id`; workflow cells set `ticket_id` and ride the real dispatcher (plan 14 §1.12.2, preserved verbatim). `fixture_id` is NOT NULL for every cell (workflow cells run a `source:'benchmark'` fixture).

**Replay pipeline contract (spec §3; amended 2026-07-19 post-review — evaluate relocated to reconcile).** A step cell renders to a one-step template pipeline via `agent/benchrunner/replay.RenderReplay`, plan `[restore(task) → step-under-test(agent)]` — **NO evaluate step in v1** — assembled into `atc.Config{Template:true, Jobs:[{Name:"run", PlanSequence:…}]}`. The renderer REUSES `dispatch.RenderAgentStep` ADDITIVELY for the step-under-test and MUST NOT call `dispatch.Render` or modify its refusal chain. `restore` clones `repo@base_sha` (read-only git cred) and applies the fixture overlay + mounts the ticket-snapshot/config bytes (bounded, inlined base64 — the `render.go:377/469` busybox idiom; an over-bound overlay is instead read from the fixture store dir mounted into the replay pod — Task 6a). It emits the artifacts the variant's step `inputs` name (`repo`, `ticket`, `skills`). The step-under-test is `RenderAgentStep(in, variantStep)` with `in.ATCExternalURL` = the sidecar stub URL and `in.Ticket.ID` = the snapshot ticket id (so `RenderAgentStep` bakes `ATC_EXTERNAL_URL`/`AGENT_TICKET_ID` at the stub), then additively: `Env["AGENT_PRINCIPAL_TOKEN"]=StubToken` and `Sidecars=[stub-ATC inline SidecarConfig]`. **Scoring is web-side at reconcile:** on a step cell's terminal success, the runner's `StepExecutor.Reconcile` fetches the candidate output refs and calls `eval.ScoreCell(ctx, reg, scoresFactory, cell.ID, …)` (B's library, import `agent/benchscore/eval`) with `EvalDeps` wired to real stores — mirroring the platform's existing server-side ingestion pattern (production steps are also scored/ingested web-side). The cell id is a Go parameter; **no `AGENT_BENCH_CELL_ID` env var is emitted anywhere.** A `ScoreCell` error at reconcile sets the cell `status='error'` with reason `evaluate-failed` — never `failed` (the variant is not to blame). The LLM-judge evaluator rides a later rendered-step slice; future pod-side evaluators will need a cell-id env + an ingestion route (both deferred). No harvest terminal step is ever emitted (the renderer never sets `Ticket.ID>0` on a `Render` call — it does not call `Render`).

**Stub-ATC in replay (open Q3 resolved for A2's surface; Q2 — fixture-store blob mechanics/size caps — stays open for A1).** The #40 contract-test mux is extracted to a deployable binary `agent/benchstub` + `cmd/benchstub` (the mux is `*testing.T`-bound today; A2 builds the standalone main). Config points it at a fixture ticket-snapshot (not a hardcoded literal). **Write-absorption scope:** reads (`GET ticket`, `list_tasks`, `get_task`) serve the fixture snapshot; writes (`submit_spec`, `submit_plan`, `update_task_status`, and — 2026-07-19 post-review — `POST /api/v1/agent/costs`, which returns `200 {"status":"recorded"}`) return 200 and are RECORDED to an absorbed-writes log that becomes part of the candidate output (evaluators may inspect intent). Cost posts are **absorbed-and-recorded, NOT forwarded** — forwarding would put a real credential inside the isolation boundary (see A2 Open decision 7 on gateway spend visibility). `ask_human` (`POST questions`) is DISABLED — it returns `409 {"error":"ask_human disabled in replay"}` so the step never parks. Fixtures whose captured step parked on `ask_human` are `replayable=false` (A1) and excluded from default sets. Step cells write NOTHING to production tables and create NO tickets; their spend lands in the existing ledger as the already-legal `source:'agent_step'` (the cost IS agent-step spend), attributed to the experiment via the `agent_bench_cells.pipeline_run_id → run → agent_run_metrics` join (no env var — amended 2026-07-19 post-review) — NOT a new `source:'bench'` value. **The ledger `source` column is CHECK-constrained** to `('agent_step','gateway','harvest_judge','retrospective','ci_agent','probe')` (`1773106021_create_agent_cost_ledger.up.sql:10-11`), so a `'bench'` value would fail the insert exactly as a `'bench'` ticket origin would; a distinct bench ledger source is a FUTURE budget-owned CHECK extension (see Open decision #5), not an A2 write.

**Experiment runner (RunnableComponent `agent_bench_runner`, polling-only, never notify-only).** Each tick: `ClaimPending()` one experiment; resolve defaults (below); expand the matrix `(fixture × variant × rep)` + auto-controls into `agent_bench_cells` (pending); then admit cells one at a time. A `running` experiment with ZERO cells is treated as "expand now" (the crash-between-claim-and-expand window — 2026-07-19 post-review); re-expansion is idempotent via the 6-col UNIQUE + `ON CONFLICT DO NOTHING`, and finalization requires at least one cell. **Budget envelope (open Q7 resolved):** default `budget_usd` = `--agent-bench-experiment-budget-default` (default `$12`); before EACH cell the runner requires BOTH `budget.Checker.GlobalDailyRemaining()` not `Exhausted` AND `experimentSpent(id) + estimate < budget_usd` (envelope; `experimentSpent` = SUM of the experiment's cell run costs joined `pipeline_run_id → agent_run_metrics.build_id`). When either fails, remaining pending cells are marked `skipped-budget` with a `skip_reason` and the results render partial — never silent truncation. Workflow cells additionally ride the dispatcher's own admission (plan 14 §1.12.2 verbatim); the runner still gates them on `GlobalDailyRemaining()` + envelope before queueing. An experiment is `complete` when every cell status is terminal.

**Negative controls (spec §5; open Q9 resolved).** `controls: auto` (default) synthesizes two extra variants off the baseline (the `live` variant if present, else `variants[0]`): a `baseline-clone` (byte-identical to baseline → MUST tie) and a `degraded` clone (deliberately worse → MUST lose). Degraded synthesis per `step_kind`: `review`/`plan` drop the step `Context` block and halve `MaxTurns` (truncated-context, the v1 candidate). **`controls:auto` synthesizes controls ONLY for kinds the v1 web-side scorer covers (`review`/`plan` — second amendment 2026-07-19 post-review): `step_kind:'workflow'` skips with reason `no-evaluator-for-workflow-kind`, and `step_kind:'implement'` skips with reason `implement-scoring-deferred` (the implement evaluator embeds gates + the judge — pod-side work that must never run in the ATC web process; it lands with the judge-in-pod slice), both `control_status='none'`.** **Frozen primary-metric map (2026-07-19 post-review):** `primaryMetric = {review: "precision", plan: "grounding", implement: none in v1 ("judge_total" when the judge-in-pod slice lands — the composite scalar stays the one A2↔B joint decision), workflow: none}`. Control verdict: after cells complete, if `baseline-clone` metrics do NOT tie baseline (within the evaluator's tie tolerance) OR `degraded` does NOT lose, the experiment `control_status='fail'` and `status='evaluator-suspect'` — annotated on results, NEVER suppressed. `controls: none` requires a stated `controls_reason` (recorded) → `control_status='none'`.

**Defaults resolution (the two-field contract — spec §6).** Minimal spec = `{step_kind, variants}`. Admission fills: `variants` gains `live` as baseline if absent; `fixtures` = default open set for `step_kind` — **A2-owned (benchrunner admission atop A1's `List(FixtureFilter)`), amended 2026-07-19 post-review. Frozen v1 default-set policy: newest N=24 replayable open production fixtures per `step_kind`, per-repo cap ⌈N/#repos-present⌉, recency window 30d (= the retention window);** `repetitions=1`; `evaluator` = live evaluator definition for `step_kind`; `controls=auto`; `budget_usd` = default envelope. Admission also PINS the fixture set (`Fixtures.Pin(id,true)`, retention-exempt) and freezes each variant version (`workflow.Store.Live/Get`). **Env pins (2026-07-19 post-review):** v1 deliberately runs current images — variant tests usually want that — and RECORDS both the fixture-pinned and the resolved runner+sidecar tags per cell (`agent_bench_cells.env`, surfaced in results) so env skew is always visible.

**Score status taxonomy (open Q12 resolved):** cells use `ok|failed|error|skipped-budget` ("variant did badly" ≠ "bench broke" ≠ "budget stopped"); B's `agent_bench_scores.status` uses `ok|error` only (evaluator failure ≠ low score). A2's results reader treats a missing score row for an `ok` cell as "evaluator pending/failed" and renders it distinctly (under reconcile-time scoring this is a crash-window rarity — an evaluator failure normally maps the cell to `status='error'`, reason `evaluate-failed`; amended 2026-07-19 post-review). **`implement` cells are the deliberate exception (second amendment): v1 skips their scoring entirely (pod-side gates+judge — see the controls paragraph), so an `ok` implement cell with no score row renders `unscored (implement scoring deferred)`, not "pending/failed"; run-metrics (cost/turns/status) still record via the run's own ingestion.**

**A1 consumer interface (`bench.Fixtures`, consumer-side per Go idiom — A1's `db` factory satisfies it):** `List(fixtures.FixtureFilter) ([]fixtures.Fixture, error)`, `Get(id int) (fixtures.Fixture, bool, error)`, `Pin(id int, pinned bool) error`, `Tag(id int, add, remove []string) error`, `Bundle(id int) (fixtures.Bundle, error)` (`{Repo, BaseSHA string; Overlay, TicketSnapshot, Config []byte; Env EnvPins}`). **SKELETON AMENDMENT (2026-07-19 post-review — raised, exactly like the control_role key amendment): `Admit` is REMOVED from this consumer interface.** Admission policy (default sets, caps, recency) is implemented in `agent/benchrunner` atop `List(FixtureFilter)`, honoring A1's Q6 ("policy is A2's, not a registry concern") and killing the A1→A2 dependency inversion; `FixtureSelector` stays in `agent/api/bench` as the full-form filter the runner maps onto `FixtureFilter`. **B read seam (`bench.ScoreReader`):** `ScoresForExperiment(experimentID int) ([]CellScore, error)` where `CellScore{CellID, FixtureID, Variant string, VariantVersion int, ControlRole, Rep int, Metrics map[string]float64, Status string}` — projected from B's `agent_bench_scores ⋈ agent_bench_cells`.

**Routes (six-touchpoint; §4.2 additions below). All authorized (agent-identity `CheckAgentAuthorizationHandler` tier, decision 21). Every verb is principal-authenticable (spec §9 — supervisor-ready).**
````

- [ ] Append the route rows to the §4.2 table (after the LAST real agent route — plan 14's experiment routes were superseded and never built, so there is nothing to grep; anchor on the dispatcher/tickets/outcomes rows that DO exist at HEAD, e.g. after the `GetAgentDispatcher`/`SetAgentDispatcher` rows):

```markdown
| `CreateAgentBenchExperiment` | POST | `/api/v1/agent/bench/experiments` | authorized member | bench-A2 |
| `ListAgentBenchExperiments` | GET | `/api/v1/agent/bench/experiments` | authorized viewer | bench-A2 |
| `GetAgentBenchExperiment` | GET | `/api/v1/agent/bench/experiments/:experiment_id` | authorized viewer | bench-A2 |
| `GetAgentBenchResults` | GET | `/api/v1/agent/bench/experiments/:experiment_id/results` | authorized viewer | bench-A2 |
| `ListAgentBenchFixtures` | GET | `/api/v1/agent/bench/fixtures` | authorized viewer | bench-A2 (consumes A1) |
| `TagAgentBenchFixture` | PUT | `/api/v1/agent/bench/fixtures/:fixture_id/tags` | authorized member | bench-A2 (consumes A1) |
| `PinAgentBenchFixture` | PUT | `/api/v1/agent/bench/fixtures/:fixture_id/pin` | authorized member | bench-A2 (consumes A1) |
```

- [ ] Append to the §11 Amendment log:

```markdown
- 2026-07-19 (bench-A2 planning): added §1.12.3 — agent_bench_experiments + agent_bench_cells (migration 1773106101, supersedes plan-14 M1 shapes; one cells table for step (pipeline_run_id) + workflow (ticket_id) cells, fixture_id NOT NULL RESTRICT). **SKELETON KEY AMENDMENT:** agent_bench_cells UNIQUE gains `control_role` → `UNIQUE(experiment_id, fixture_id, variant, variant_version, control_role, repetition)` (the frozen key omitted control_role, so a baseline cell and its byte-identical baseline-clone/degraded controls — same variant+version — collided; control_role is the distinguishing dimension). Workflow cells create tickets with the already-legal `origin:'fly'` (no `'bench'` origin exists; the CHECK forbids it) and their step-cell spend uses ledger `source:'agent_step'` (no `'bench'` ledger source — also CHECK-forbidden). Replay pipeline contract (restore→step via additive dispatch.RenderAgentStep reuse — refusal chain untouched; NO evaluate step in v1 — scoring is web-side at reconcile via B's eval.ScoreCell (import agent/benchscore/eval) with the cell id as a Go parameter, no AGENT_BENCH_CELL_ID env var anywhere; ScoreCell error → cell status='error' reason 'evaluate-failed'; amended 2026-07-19 post-review); deployable stub-ATC (agent/benchstub+cmd/benchstub extracted from #40's *testing.T-bound mux) with write-absorption (reads serve snapshot; submit_* absorbed+recorded; POST /api/v1/agent/costs absorbed+recorded, never forwarded; ask_human disabled); experiment runner agent_bench_runner (polling, never notify-only, two-phase: claim+start then reconcile+finalize; running-with-zero-cells re-expands idempotently via the 6-col UNIQUE + ON CONFLICT DO NOTHING) with budget envelope (default $12) gated on GlobalDailyRemaining()+per-experiment spend, skipped-budget honesty, workflow cells ride the real dispatcher (plan 14 §1.12.2 verbatim); negative-control auto-synthesis (baseline-clone must tie, degraded=truncated-context must lose → control fail flags evaluator-suspect; workflow-kind auto-controls SKIPPED reason no-evaluator-for-workflow-kind AND implement-kind SKIPPED reason implement-scoring-deferred — v1 web-side scoring covers review/plan only, implement's gates+judge are pod-side and ride the judge-in-pod slice (second amendment 2026-07-19); frozen primary metrics review→precision / plan→grounding / implement→none-in-v1 / workflow→none); two-field defaults resolution with the A2-owned frozen default-set policy (newest N=24 replayable open production per step_kind, per-repo cap ⌈N/#repos⌉, 30d window); cells carry env JSONB (pinned + resolved image tags — env-skew visibility); overlay write path + full-bundle pinning accepted from A1's Scope-out as Task 6a (PutOverlay/ErrOverCap, --agent-fixture-overlay-max-bytes default 33554432, overlay-over-bound:<n>B, InputBundleComplete=true; historical input-bundle-partial rows excluded forever); restore clone REUSES the outcome-watcher read-only git cred (no new credential path); bench.Fixtures (A1 consume; Admit REMOVED 2026-07-19 post-review — admission is A2-owned in benchrunner atop List(FixtureFilter)) + bench.ScoreReader (B consume) narrow interfaces; seven §4.2 routes (bench experiments create/list/get, results, fixtures list/tag/pin). Reads-only over A1/B tables; the only writes are agent_bench_* rows and the tickets it queues via the existing Transition seam. Affects: A1 fixtures (consumer iface), B evaluators (score-write seam + read shape), C scorecards (results/fixture-tier read).
```

- [ ] Verify: `grep -n "1.12.3\|CreateAgentBenchExperiment\|agent_bench_runner\|evaluate-failed" docs/superpowers/plans/agentic-platform/00-shared-contracts.md` — expect the addendum in the §1.12 region, seven route rows, and the §11 entry (and ZERO hits for `AGENT_BENCH_CELL_ID` — the env var was removed by the 2026-07-19 post-review amendment).
- [ ] Commit: `git add docs/superpowers/plans/agentic-platform/00-shared-contracts.md && git commit -m "docs(agentic): bench-A2 contract addendum - experiments/cells, replay+stub, runner/budget, routes"`

---

### Task 2: Migration `1773106101` — `agent_bench_experiments` + `agent_bench_cells`

**Files:**
- Create: `atc/db/migration/migrations/1773106101_create_agent_bench_experiments.up.sql`
- Create: `atc/db/migration/migrations/1773106101_create_agent_bench_experiments.down.sql`
- Modify: `atc/db/migration/legacy_upgrade_test.go:37` (`jetbridgeHeadMigration` const)

**Steps:**

- [ ] Write the `.up.sql` — the FROZEN skeleton §A2 DDL with the §1.12.3 `control_role`-in-UNIQUE amendment (both tables in ONE multi-statement file; the `go:embed` migrator applies every statement in a `.up.sql`, so two `CREATE TABLE`s + their indexes in one file is ordinary SQL — this is NOT borrowed from plan 14, which split its two tables across two files). Migration files are picked up via `go:embed migrations` (`atc/db/migration/migration.go`) — no registration code:

```sql
CREATE TABLE agent_bench_experiments (
    id             SERIAL PRIMARY KEY,
    name           TEXT NOT NULL,
    description    TEXT NOT NULL DEFAULT '',
    step_kind      TEXT NOT NULL
                   CHECK (step_kind IN ('review','implement','plan','workflow')),
    spec           JSONB NOT NULL,
    budget_usd     NUMERIC(12,6) NOT NULL DEFAULT 0,
    status         TEXT NOT NULL DEFAULT 'pending'
                   CHECK (status IN ('pending','running','complete','error','evaluator-suspect')),
    control_status TEXT NOT NULL DEFAULT 'pending'
                   CHECK (control_status IN ('pending','pass','fail','none')),
    created_by     TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at   TIMESTAMPTZ
);

CREATE TABLE agent_bench_cells (
    id              SERIAL PRIMARY KEY,
    experiment_id   INTEGER NOT NULL REFERENCES agent_bench_experiments(id) ON DELETE CASCADE,
    fixture_id      INTEGER NOT NULL REFERENCES agent_step_fixtures(id),
    variant         TEXT NOT NULL,
    variant_version INTEGER,
    control_role    TEXT NOT NULL DEFAULT ''
                    CHECK (control_role IN ('','baseline-clone','degraded')),
    repetition      INTEGER NOT NULL DEFAULT 1,
    pipeline_run_id INTEGER,
    ticket_id       INTEGER,
    status          TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending','running','ok','failed','error','skipped-budget')),
    skip_reason     TEXT NOT NULL DEFAULT '',
    -- (amended 2026-07-19 post-review) fixture-pinned + resolved runner/sidecar
    -- image tags, recorded at cell start; env skew is always visible in results.
    env             JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- control_role is IN the key (skeleton amendment §1.12.3): a baseline cell
    -- and its baseline-clone/degraded controls share the same variant+version,
    -- so control_role is the only column that distinguishes them. NULL-safe
    -- because fixture_id + control_role are both NOT NULL.
    UNIQUE (experiment_id, fixture_id, variant, variant_version, control_role, repetition)
);

CREATE INDEX agent_bench_cells_experiment ON agent_bench_cells (experiment_id);
CREATE INDEX agent_bench_cells_fixture    ON agent_bench_cells (fixture_id);
CREATE INDEX agent_bench_cells_run        ON agent_bench_cells (pipeline_run_id) WHERE pipeline_run_id IS NOT NULL;
CREATE INDEX agent_bench_cells_ticket     ON agent_bench_cells (ticket_id)       WHERE ticket_id IS NOT NULL;
```

- [ ] Write the `.down.sql` (drop cells first — it FKs experiments):

```sql
DROP TABLE agent_bench_cells;
DROP TABLE agent_bench_experiments;
```

- [ ] Set the head const in `atc/db/migration/legacy_upgrade_test.go:37` to `1773106101` **only if the current value is lower** (A1's `1773106100` must already be landed for the `cells.fixture_id` FK to resolve — this migration will not apply otherwise):

```go
const jetbridgeHeadMigration = 1773106101
```

- [ ] Run: `pg_isready && ginkgo --focus='Legacy Database Upgrade' ./atc/db/migration/` — expect green (a SQL error, a missing down file, an absent `agent_step_fixtures` table for the FK, or a stale head const fails here).
- [ ] Commit: `git add atc/db/migration && git commit -m "feat(bench): migration 1773106101 - agent_bench_experiments + agent_bench_cells"`

---

### Task 3: `agent/api/bench` — domain types, spec validation, defaults resolution, MemoryStore

Package `agent/api/bench` holds the experiment/cell types, the two-field spec + defaults contract, the cell/control/status taxonomies, the A1/B consumer interfaces, an in-memory `Store` for handler/runner tests, and the counterfeiter fake. Plain `testing`, matching `agent/api/tickets`/`agent/api/outcomes`.

**Files:**
- Create: `agent/api/bench/types.go`
- Create: `agent/api/bench/spec.go` (validation + `ResolveDefaults`)
- Create: `agent/api/bench/memory_store.go`
- Create: `agent/api/bench/benchfakes/fake_store.go` (generated)
- Test: `agent/api/bench/spec_test.go`, `agent/api/bench/memory_store_test.go`

**Steps:**

- [ ] Write the failing `agent/api/bench/spec_test.go` — the two-field acceptance bar + control/taxonomy validation:

```go
package bench_test

import (
	"testing"

	"github.com/concourse/concourse/agent/api/bench"
)

func TestMinimalTwoFieldSpecResolves(t *testing.T) {
	// The basic experience: step_kind + one candidate variant, nothing else.
	spec := bench.ExperimentSpec{
		StepKind: bench.StepReview,
		Variants: []bench.Variant{{Name: "review-prompts", Version: 5}},
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("minimal spec must validate: %v", err)
	}
	r := spec.ResolveDefaults(bench.Defaults{BudgetUSD: 12, EvaluatorFor: map[bench.StepKind]bench.Evaluator{
		bench.StepReview: {Name: "review-judge", Version: 3},
	}})
	// live baseline auto-included, ahead of the candidate.
	if len(r.Variants) != 2 || r.Variants[0].Name != "live" {
		t.Fatalf("live baseline must be auto-prepended: %+v", r.Variants)
	}
	if r.Repetitions != 1 || r.Controls != bench.ControlsAuto || r.BudgetUSD != 12 {
		t.Fatalf("defaults not filled: %+v", r)
	}
	if r.Evaluator.Name != "review-judge" || r.Evaluator.Version != 3 {
		t.Fatalf("default evaluator not resolved: %+v", r.Evaluator)
	}
	if r.Baseline().Name != "live" {
		t.Fatalf("baseline must be live: %+v", r.Baseline())
	}
}

func TestSpecValidationRejects(t *testing.T) {
	cases := map[string]bench.ExperimentSpec{
		"empty step_kind": {Variants: []bench.Variant{{Name: "x"}}},
		"bad step_kind":   {StepKind: "frobnicate", Variants: []bench.Variant{{Name: "x"}}},
		"no variants":     {StepKind: bench.StepReview},
		"controls none without reason": {
			StepKind: bench.StepReview, Variants: []bench.Variant{{Name: "x"}}, Controls: bench.ControlsNone,
		},
		"negative repetitions": {
			StepKind: bench.StepReview, Variants: []bench.Variant{{Name: "x"}}, Repetitions: -1,
		},
	}
	for name, spec := range cases {
		if err := spec.Validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestControlsNoneWithReasonValidates(t *testing.T) {
	spec := bench.ExperimentSpec{
		StepKind: bench.StepReview, Variants: []bench.Variant{{Name: "x"}},
		Controls: bench.ControlsNone, ControlsReason: "smoke test, controls run separately",
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("controls=none with a reason must validate: %v", err)
	}
	r := spec.ResolveDefaults(bench.Defaults{BudgetUSD: 12})
	if r.Controls != bench.ControlsNone {
		t.Fatalf("controls=none must survive defaults: %+v", r)
	}
}
```

- [ ] Run `go test ./agent/api/bench/` — expect compile failure (package does not exist).
- [ ] Write `agent/api/bench/types.go` — the taxonomies, row types, consumer interfaces, and `Store`:

```go
// Package bench holds the bench replay+harness domain (shared-contracts
// §1.12.3): experiments, cells, the two-field spec, and the narrow A1
// (Fixtures) / B (ScoreReader) consumer interfaces. Labels are joins —
// this package writes only agent_bench_* rows (principle 3/4).
package bench

import (
	"encoding/json"
	"errors"

	"github.com/concourse/concourse/agent/api/fixtures" // A1 (landed first)
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

type StepKind string

const (
	StepReview    StepKind = "review"
	StepImplement StepKind = "implement"
	StepPlan      StepKind = "plan"
	StepWorkflow  StepKind = "workflow"
)

func (k StepKind) Valid() bool {
	switch k {
	case StepReview, StepImplement, StepPlan, StepWorkflow:
		return true
	}
	return false
}

type ControlsMode string

const (
	ControlsAuto ControlsMode = "auto"
	ControlsNone ControlsMode = "none"
)

type ControlRole string

const (
	ControlNone     ControlRole = ""
	ControlBaseline ControlRole = "baseline-clone"
	ControlDegraded ControlRole = "degraded"
)

type CellStatus string

const (
	CellPending  CellStatus = "pending"
	CellRunning  CellStatus = "running"
	CellOK       CellStatus = "ok"
	CellFailed   CellStatus = "failed"
	CellError    CellStatus = "error"
	CellSkipped  CellStatus = "skipped-budget"
)

func (s CellStatus) Terminal() bool {
	switch s {
	case CellOK, CellFailed, CellError, CellSkipped:
		return true
	}
	return false
}

type ExperimentStatus string

const (
	ExpPending          ExperimentStatus = "pending"
	ExpRunning          ExperimentStatus = "running"
	ExpComplete         ExperimentStatus = "complete"
	ExpError            ExperimentStatus = "error"
	ExpEvaluatorSuspect ExperimentStatus = "evaluator-suspect"
)

type ControlStatus string

const (
	CtrlPending ControlStatus = "pending"
	CtrlPass    ControlStatus = "pass"
	CtrlFail    ControlStatus = "fail"
	CtrlNone    ControlStatus = "none"
)

// Variant is a workflow-definition name; "live" resolves the current live
// definition at admission. Version 0 means "resolve live version at admit".
type Variant struct {
	Name    string `json:"name"`
	Version int    `json:"version,omitempty"`
}

// Evaluator is the versioned evaluator workflow definition (B). Empty name
// means "resolve the live evaluator for the step_kind at admit".
type Evaluator struct {
	Name    string `json:"name,omitempty"`
	Version int    `json:"version,omitempty"`
}

// FixtureSelector is the (optional) full-form fixture filter PLUS the
// admission step-kind context. On the two-field path the caller supplies
// none of the filter fields — the benchrunner's admission (A2-owned; Admit
// was REMOVED from Fixtures, 2026-07-19 post-review skeleton amendment)
// sets StepKind from the experiment and resolves the zero-selector to the
// frozen default set (newest 24 replayable open production fixtures per
// step_kind, per-repo cap, 30d window) atop fixtures.FixtureFilter.
type FixtureSelector struct {
	StepKind StepKind `json:"step_kind,omitempty"` // admission-set (not caller-set); resolves the default set
	IDs      []int    `json:"ids,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Split    string   `json:"split,omitempty"` // "open" (default) | "holdout"
	Limit    int      `json:"limit,omitempty"`
}

var (
	ErrExperimentNotFound = errors.New("bench experiment not found")
	ErrCellNotFound       = errors.New("bench cell not found")
)

// Experiment mirrors agent_bench_experiments. Timestamps epoch seconds.
type Experiment struct {
	ID            int              `json:"id"`
	Name          string           `json:"name"`
	Description   string           `json:"description,omitempty"`
	StepKind      StepKind         `json:"step_kind"`
	Spec          ResolvedSpec     `json:"spec"`
	BudgetUSD     float64          `json:"budget_usd"`
	Status        ExperimentStatus `json:"status"`
	ControlStatus ControlStatus    `json:"control_status"`
	CreatedBy     string           `json:"created_by,omitempty"`
	CreatedAt     int64            `json:"created_at,omitempty"`
	CompletedAt   int64            `json:"completed_at,omitempty"`
	Cells         []Cell           `json:"cells,omitempty"`
}

// Cell mirrors agent_bench_cells. Exactly one of PipelineRunID (step) /
// TicketID (workflow) is set; FixtureID is always set.
type Cell struct {
	ID             int         `json:"id"`
	ExperimentID   int         `json:"experiment_id"`
	FixtureID      int         `json:"fixture_id"`
	Variant        string      `json:"variant"`
	VariantVersion *int        `json:"variant_version,omitempty"`
	ControlRole    ControlRole `json:"control_role,omitempty"`
	Repetition     int         `json:"repetition"`
	PipelineRunID  *int        `json:"pipeline_run_id,omitempty"`
	TicketID       *int        `json:"ticket_id,omitempty"`
	Status         CellStatus  `json:"status"`
	SkipReason     string      `json:"skip_reason,omitempty"`
	// Env records the fixture-pinned AND resolved runner/sidecar image tags
	// (recorded at cell start — env-skew visibility; 2026-07-19 post-review).
	Env json.RawMessage `json:"env,omitempty"`
}

// CellScore is B's per-cell score projection (bench.ScoreReader).
type CellScore struct {
	CellID         int                `json:"cell_id"`
	FixtureID      int                `json:"fixture_id"`
	Variant        string             `json:"variant"`
	VariantVersion int                `json:"variant_version"`
	ControlRole    ControlRole        `json:"control_role"`
	Rep            int                `json:"rep"`
	Metrics        map[string]float64 `json:"metrics"`
	Status         string             `json:"status"` // ok | error (B's taxonomy)
}

// Fixtures is A2's NARROW consumer view of A1's fixture Store (interface at
// the consumer per Go idiom; db.NewAgentStepFixturesFactory satisfies it).
// Admit was REMOVED from this interface (raised skeleton amendment,
// 2026-07-19 post-review — like control_role): admission POLICY lives in
// agent/benchrunner, built atop List (A1's Q6: policy is A2's, not a
// registry concern).
//
//counterfeiter:generate . Fixtures
type Fixtures interface {
	List(fixtures.FixtureFilter) ([]fixtures.Fixture, error)
	Get(id int) (fixtures.Fixture, bool, error)
	Pin(id int, pinned bool) error
	Tag(id int, add, remove []string) error
	Bundle(id int) (fixtures.Bundle, error)
}

// ScoreReader is A2's read-only view of B's agent_bench_scores.
//
//counterfeiter:generate . ScoreReader
type ScoreReader interface {
	ScoresForExperiment(experimentID int) ([]CellScore, error)
}

// WorkflowOutcomes is A2's read-only projection for WORKFLOW cells (which
// have no agent_bench_scores rows): agent_run_metrics ⋈ agent_outcomes by
// ticket — cost_usd, findings count, and 0/1 indicator metrics for terminal
// status / merge outcome when present. A2 OWNS this projection (2026-07-19
// post-review; B's disclaimer stands). Backed by a small atc/db reader.
//
//counterfeiter:generate . WorkflowOutcomes
type WorkflowOutcomes interface {
	OutcomesForTickets(ticketIDs []int) (map[int]map[string]float64, error)
}

// Store persists agent_bench_experiments / agent_bench_cells. Implemented
// by atc/db.NewAgentBenchFactory and MemoryStore.
//
//counterfeiter:generate . Store
type Store interface {
	CreateExperiment(name, description string, kind StepKind, spec ResolvedSpec, budgetUSD float64, createdBy string) (Experiment, error)
	GetExperiment(id int) (Experiment, bool, error)
	ListExperiments() ([]Experiment, error)
	// ClaimPending atomically flips one pending experiment to running and
	// returns it (nil,false when none). The runner's per-tick claim (phase A).
	ClaimPending() (*Experiment, bool, error)
	// ListRunning returns every experiment currently status='running'. The
	// runner's per-tick reconcile enumeration (phase B) — ClaimPending only
	// ever returns PENDING rows, so without this a running experiment is
	// never revisited and its cells stay 'running' forever.
	ListRunning() ([]Experiment, error)
	AddCells(experimentID int, cells []Cell) ([]Cell, error)
	ListCells(experimentID int) ([]Cell, error)
	LinkRun(cellID, pipelineRunID int) error
	LinkTicket(cellID, ticketID int) error
	// SetCellEnv records the pinned+resolved image tags at cell start
	// (agent_bench_cells.env — 2026-07-19 post-review).
	SetCellEnv(cellID int, env json.RawMessage) error
	SetCellStatus(cellID int, status CellStatus, skipReason string) error
	SetExperimentStatus(id int, status ExperimentStatus) error
	SetControlStatus(id int, cs ControlStatus) error
	FinishExperiment(id int) error // stamps completed_at, status=complete
}

// ResolvedSpec is the admission-resolved spec stored in agent_bench_experiments.spec.
type ResolvedSpec struct {
	StepKind       StepKind        `json:"step_kind"`
	Variants       []Variant       `json:"variants"`
	Fixtures       FixtureSelector `json:"fixtures"`
	Repetitions    int             `json:"repetitions"`
	Evaluator      Evaluator       `json:"evaluator"`
	Controls       ControlsMode    `json:"controls"`
	ControlsReason string          `json:"controls_reason,omitempty"`
	BudgetUSD      float64         `json:"budget_usd"`
}

// Baseline is the delta/control reference: the "live" variant if present,
// else the first variant.
func (r ResolvedSpec) Baseline() Variant {
	for _, v := range r.Variants {
		if v.Name == "live" {
			return v
		}
	}
	if len(r.Variants) > 0 {
		return r.Variants[0]
	}
	return Variant{}
}

func (r ResolvedSpec) JSON() json.RawMessage { b, _ := json.Marshal(r); return b }
```

- [ ] Write `agent/api/bench/spec.go` — the input spec, `Validate`, and `ResolveDefaults`:

```go
package bench

import "fmt"

// ExperimentSpec is the CALLER's spec (the two-field minimal form plus
// optional full-form fields). ResolveDefaults turns it into a ResolvedSpec.
type ExperimentSpec struct {
	StepKind       StepKind        `json:"step_kind"`
	Variants       []Variant       `json:"variants"`
	Fixtures       FixtureSelector `json:"fixtures,omitempty"`
	Repetitions    int             `json:"repetitions,omitempty"`
	Evaluator      Evaluator       `json:"evaluator,omitempty"`
	Controls       ControlsMode    `json:"controls,omitempty"`
	ControlsReason string          `json:"controls_reason,omitempty"`
	BudgetUSD      float64         `json:"budget_usd,omitempty"`
}

// Defaults are the server-supplied fillers (from web flags + the live
// evaluator registry) applied at admission.
type Defaults struct {
	BudgetUSD    float64
	Repetitions  int // 0 => 1
	EvaluatorFor map[StepKind]Evaluator
}

func (s ExperimentSpec) Validate() error {
	if !s.StepKind.Valid() {
		return fmt.Errorf("step_kind must be one of review|implement|plan|workflow, got %q", s.StepKind)
	}
	if len(s.Variants) == 0 {
		return fmt.Errorf("at least one variant is required")
	}
	for i, v := range s.Variants {
		if v.Name == "" {
			return fmt.Errorf("variant %d has no name", i)
		}
	}
	if s.Repetitions < 0 {
		return fmt.Errorf("repetitions must be >= 0 (0 = default)")
	}
	if s.Controls == ControlsNone && s.ControlsReason == "" {
		return fmt.Errorf("controls: none requires a controls_reason (recorded)")
	}
	if s.Controls != "" && s.Controls != ControlsAuto && s.Controls != ControlsNone {
		return fmt.Errorf("controls must be auto or none, got %q", s.Controls)
	}
	return nil
}

// ResolveDefaults fills every unset knob (the revealed-complexity contract):
// live baseline prepended, default open fixture set, reps=1, default
// evaluator, controls=auto, default budget envelope.
func (s ExperimentSpec) ResolveDefaults(d Defaults) ResolvedSpec {
	r := ResolvedSpec{
		StepKind:       s.StepKind,
		Fixtures:       s.Fixtures,
		Repetitions:    s.Repetitions,
		Evaluator:      s.Evaluator,
		Controls:       s.Controls,
		ControlsReason: s.ControlsReason,
		BudgetUSD:      s.BudgetUSD,
	}
	// live baseline auto-prepended unless already present.
	hasLive := false
	for _, v := range s.Variants {
		if v.Name == "live" {
			hasLive = true
		}
	}
	if hasLive {
		r.Variants = append([]Variant{}, s.Variants...)
	} else {
		r.Variants = append([]Variant{{Name: "live"}}, s.Variants...)
	}
	if r.Repetitions == 0 {
		if d.Repetitions > 0 {
			r.Repetitions = d.Repetitions
		} else {
			r.Repetitions = 1
		}
	}
	if r.Controls == "" {
		r.Controls = ControlsAuto
	}
	if r.Evaluator.Name == "" {
		r.Evaluator = d.EvaluatorFor[s.StepKind]
	}
	if r.BudgetUSD == 0 {
		r.BudgetUSD = d.BudgetUSD
	}
	if r.Fixtures.Split == "" {
		r.Fixtures.Split = "open"
	}
	return r
}
```

- [ ] Write `agent/api/bench/memory_store.go` (in-memory `Store` for handler/runner tests — mutex-guarded map, `ClaimPending` flips the oldest pending experiment, `ListRunning` returns all `status=running` experiments, `AddCells` assigns ids AND skips rows whose 6-col key already exists — mirroring the factory's `ON CONFLICT DO NOTHING` re-expansion idempotency (2026-07-19 post-review), `SetCellEnv` stamps the env blob, `Finish` stamps `completed_at`). Follow `agent/api/outcomes/memory_store.go`'s idiom. Then `agent/api/bench/memory_store_test.go` asserting: create→get round-trips the spec; `ClaimPending` returns and running-flips exactly one; a second `ClaimPending` on the same experiment returns `false`; `ListRunning` returns exactly the claimed-but-unfinished experiments (and drops them once `FinishExperiment` stamps complete); `AddCells` then `ListCells` preserves order and ids; `LinkRun`/`SetCellStatus`/`FinishExperiment` mutate as expected; unknown ids return `ErrExperimentNotFound`/`ErrCellNotFound`.
- [ ] Run `go test ./agent/api/bench/` — expect pass.
- [ ] Generate the fakes: `cd agent/api/bench && go run github.com/maxbrunsfeld/counterfeiter/v6 -o benchfakes/fake_store.go . Store && go run github.com/maxbrunsfeld/counterfeiter/v6 -o benchfakes/fake_fixtures.go . Fixtures && go run github.com/maxbrunsfeld/counterfeiter/v6 -o benchfakes/fake_score_reader.go . ScoreReader && go run github.com/maxbrunsfeld/counterfeiter/v6 -o benchfakes/fake_workflow_outcomes.go . WorkflowOutcomes && cd ../../..` then `go build ./agent/...`.
- [ ] Commit: `git add agent/api/bench && git commit -m "feat(bench): agent/api/bench domain types, two-field spec+defaults, MemoryStore, fakes"`

> **A1 dependency note (settled 2026-07-19 post-review):** `types.go` imports `agent/api/fixtures` (`fixtures.Fixture`, `fixtures.FixtureFilter`, `fixtures.Bundle`). Those shapes are CONFIRMED by the post-review decisions and frozen in A1 Tasks 1/3: `FixtureFilter{StepKind, Split, Tags, IDs, Limit}` and `Bundle{Repo, BaseSHA string; Overlay, TicketSnapshot, Config []byte; Env EnvPins}`. A1 ships NO snapshot-ticket type — the `SnapshotTicket` adapter lives in `agent/benchrunner/replay` (Task 6).

---

### Task 4: `atc/db` `AgentBenchFactory` implementing `bench.Store`

The persistence backing (squirrel `psql`, epoch scan, `ClaimPending` via a guarded `UPDATE ... RETURNING`). Follows `agent_reviews_factory.go`/`agent_outcomes_factory.go`.

**Files:**
- Create: `atc/db/agent_bench_factory.go`
- Create: `atc/db/dbfakes/fake_agent_bench_factory.go` (generated)
- Test: `atc/db/agent_bench_factory_test.go`

**Steps:**

- [ ] Write the failing Ginkgo spec `atc/db/agent_bench_factory_test.go` (needs a seeded `agent_step_fixtures` row for the `cells.fixture_id` FK — insert one directly in `BeforeEach` since A1's factory may not be importable in this suite):

```go
package db_test

import (
	"github.com/concourse/concourse/agent/api/bench"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentBenchFactory", func() {
	var factory db.AgentBenchFactory
	var fixtureID int

	BeforeEach(func() {
		factory = db.NewAgentBenchFactory(dbConn)
		_, err := dbConn.Exec("DELETE FROM agent_bench_cells")
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec("DELETE FROM agent_bench_experiments")
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec("DELETE FROM agent_step_fixtures")
		Expect(err).NotTo(HaveOccurred())
		// seed one replayable fixture (A1's table) for the FK.
		err = dbConn.QueryRow(
			`INSERT INTO agent_step_fixtures (source, step_kind, repo, content_hash, input_ref)
			 VALUES ('production','review','tdmtrader/concourse','h1','{}'::jsonb) RETURNING id`,
		).Scan(&fixtureID)
		Expect(err).NotTo(HaveOccurred())
	})

	spec := bench.ResolvedSpec{StepKind: bench.StepReview, Repetitions: 1, Controls: bench.ControlsAuto,
		Variants: []bench.Variant{{Name: "live"}, {Name: "review-prompts", Version: 5}}}

	It("creates, gets, and lists experiments and claims exactly one pending", func() {
		exp, err := factory.CreateExperiment("exp1", "d", bench.StepReview, spec, 12, "tdmtrader")
		Expect(err).NotTo(HaveOccurred())
		Expect(exp.Status).To(Equal(bench.ExpPending))
		Expect(exp.Spec.Variants).To(HaveLen(2))

		got, found, err := factory.GetExperiment(exp.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got.BudgetUSD).To(BeNumerically("~", 12, 0.0001))

		claimed, ok, err := factory.ClaimPending()
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(claimed.ID).To(Equal(exp.ID))
		// second claim finds nothing (already running).
		_, ok2, _ := factory.ClaimPending()
		Expect(ok2).To(BeFalse())
	})

	It("adds cells, links a run, and mirrors status", func() {
		exp, _ := factory.CreateExperiment("exp2", "", bench.StepReview, spec, 12, "u")
		v5 := 5
		live7 := 7 // admission resolves "live" to a CONCRETE version — the three
		//            "live"@7 rows below share (experiment, fixture, variant,
		//            variant_version, repetition) and differ ONLY by control_role,
		//            so this insert succeeds ONLY because control_role is in the
		//            UNIQUE key (§1.12.3 amendment). Leaving variant_version NULL
		//            here would mask the collision (NULLs are distinct in PG).
		cells, err := factory.AddCells(exp.ID, []bench.Cell{
			{FixtureID: fixtureID, Variant: "live", VariantVersion: &live7, Repetition: 1},
			{FixtureID: fixtureID, Variant: "review-prompts", VariantVersion: &v5, Repetition: 1},
			{FixtureID: fixtureID, Variant: "live", VariantVersion: &live7, ControlRole: bench.ControlBaseline, Repetition: 1},
			{FixtureID: fixtureID, Variant: "live", VariantVersion: &live7, ControlRole: bench.ControlDegraded, Repetition: 1},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(cells).To(HaveLen(4))
		Expect(cells[0].ID).To(BeNumerically(">", 0))

		Expect(factory.LinkRun(cells[0].ID, 9001)).To(Succeed())
		Expect(factory.SetCellStatus(cells[0].ID, bench.CellOK, "")).To(Succeed())
		Expect(factory.SetCellStatus(cells[1].ID, bench.CellSkipped, "envelope exhausted")).To(Succeed())

		listed, _ := factory.ListCells(exp.ID)
		Expect(listed).To(HaveLen(4))
		Expect(*listed[0].PipelineRunID).To(Equal(9001))
		Expect(listed[0].Status).To(Equal(bench.CellOK))
		Expect(listed[1].Status).To(Equal(bench.CellSkipped))
		Expect(listed[1].SkipReason).To(Equal("envelope exhausted"))
	})

	It("sets control status and finishes the experiment", func() {
		exp, _ := factory.CreateExperiment("exp3", "", bench.StepReview, spec, 12, "u")
		Expect(factory.SetControlStatus(exp.ID, bench.CtrlPass)).To(Succeed())
		Expect(factory.SetExperimentStatus(exp.ID, bench.ExpRunning)).To(Succeed())
		Expect(factory.FinishExperiment(exp.ID)).To(Succeed())
		got, _, _ := factory.GetExperiment(exp.ID)
		Expect(got.Status).To(Equal(bench.ExpComplete))
		Expect(got.ControlStatus).To(Equal(bench.CtrlPass))
		Expect(got.CompletedAt).To(BeNumerically(">", 0))
	})
})
```

- [ ] Run `ginkgo --focus="AgentBenchFactory" ./atc/db/` — expect compile failure `undefined: db.NewAgentBenchFactory`.
- [ ] Write `atc/db/agent_bench_factory.go`. Key details: `CreateExperiment` inserts `spec` as `spec.JSON()` and scans back epoch `created_at`; `ClaimPending` is `UPDATE agent_bench_experiments SET status='running' WHERE id = (SELECT id FROM agent_bench_experiments WHERE status='pending' ORDER BY id ASC LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING <cols>` (SKIP LOCKED so parallel web nodes never claim the same experiment); `ListRunning` is `SELECT <cols> FROM agent_bench_experiments WHERE status='running' ORDER BY id ASC` (the runner's phase-B reconcile enumeration); `AddCells` batch-inserts **with `ON CONFLICT DO NOTHING` on the 6-col UNIQUE** (re-expansion idempotency for the crash-between-claim-and-expand window — 2026-07-19 post-review) and RETURNS the inserted ids (loop or `unnest`); `SetCellEnv` writes the `env JSONB`; scanning `variant_version`/`pipeline_run_id`/`ticket_id` uses `sql.NullInt64` → `*int`, `env` scans as raw JSON; `spec` scans via `json.Unmarshal` into `ResolvedSpec`. Declare `type AgentBenchFactory interface { bench.Store }` with `//counterfeiter:generate . AgentBenchFactory`.
- [ ] Generate the fake: `cd atc/db && go run github.com/maxbrunsfeld/counterfeiter/v6 -o dbfakes/fake_agent_bench_factory.go . AgentBenchFactory && cd ../..`
- [ ] Run `ginkgo --focus="AgentBenchFactory" ./atc/db/ && go build ./atc/db/...` — expect green.
- [ ] Commit: `git add atc/db && git commit -m "feat(bench): AgentBenchFactory (experiments + cells persistence) + fake"`

---

### Task 5: Deployable stub-ATC — `agent/benchstub` + `cmd/benchstub` (extracted from #40's `*testing.T`-bound mux)

The #40 stub is `httptest`+`*testing.T`-bound with a hardcoded ticket literal (`stub_atc.go:118,142,234`). A replay pod needs a standalone, network-reachable stub that (a) serves the FIXTURE's ticket snapshot, (b) absorbs+records writes, (c) disables `ask_human`. Extract the mux into a testing-free package `agent/benchstub`; make `contracttest.NewStubATC` delegate to it (its existing contract suite guards the refactor); add a `cmd/benchstub` main.

**Files:**
- Create: `agent/benchstub/server.go` (the testing-free mux + `Config` + write-absorption `Recorder`)
- Create: `cmd/benchstub/main.go` (reads env/flags, serves `http.ListenAndServe`)
- Test: `agent/benchstub/server_test.go` (plain `testing` + `httptest`)
- Modify: `agent/platformmcp/contracttest/stub_atc.go` (delegate `NewStubATC`'s mux to `benchstub.NewMux`; keep the `*testing.T` lifecycle wrapper + `StubToken`)

**Steps:**

- [ ] Write the failing `agent/benchstub/server_test.go`:

```go
package benchstub_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/concourse/concourse/agent/benchstub"
)

const token = "cap1.0.contract-test-token"

func serve(t *testing.T) (*httptest.Server, *benchstub.Recorder) {
	snapshot := []byte(`{"ticket":{"id":42,"title":"fixture ticket","state":"running"},` +
		`"spec":{"title":"s","acceptance_criteria":["ok"],"body_md":"b"},` +
		`"tasks":[{"ordering":1,"title":"t","status":"pending","detail_md":"d"}]}`)
	mux, rec := benchstub.NewMux(benchstub.Config{TicketID: 42, Token: token, Snapshot: snapshot})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s, rec
}

func TestServesFixtureSnapshotReadOnly(t *testing.T) {
	s, _ := serve(t)
	req, _ := http.NewRequest("GET", s.URL+"/api/v1/agent/tickets/42", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("get ticket: %v code=%d", err, resp.StatusCode)
	}
	var body struct{ Ticket struct{ Title string } }
	json.NewDecoder(resp.Body).Decode(&body)
	if body.Ticket.Title != "fixture ticket" {
		t.Fatalf("stub must serve the fixture snapshot, got %q", body.Ticket.Title)
	}
}

func TestAbsorbsAndRecordsWrites(t *testing.T) {
	s, rec := serve(t)
	req, _ := http.NewRequest("POST", s.URL+"/api/v1/agent/tickets/42/spec",
		bytes.NewReader([]byte(`{"title":"candidate spec"}`)))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("submit_spec must be absorbed (200), got %d", resp.StatusCode)
	}
	writes := rec.Writes()
	if len(writes) != 1 || writes[0].Verb != "submit_spec" {
		t.Fatalf("write not recorded: %+v", writes)
	}
}

func TestAbsorbsCostPosts(t *testing.T) {
	// (2026-07-19 post-review) the runner sidecar posts costs to the platform
	// surface; in replay that surface is the stub, which must absorb AND
	// record them — never forward (a forward would need a real credential
	// inside the isolation boundary).
	s, rec := serve(t)
	req, _ := http.NewRequest("POST", s.URL+"/api/v1/agent/costs",
		bytes.NewReader([]byte(`{"usd":0.42,"model":"sonnet"}`)))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("cost posts must be absorbed (200), got %d", resp.StatusCode)
	}
	var body struct{ Status string }
	json.NewDecoder(resp.Body).Decode(&body)
	if body.Status != "recorded" {
		t.Fatalf(`cost absorption must ack {"status":"recorded"}, got %+v`, body)
	}
	writes := rec.Writes()
	if len(writes) != 1 || writes[0].Verb != "submit_cost" {
		t.Fatalf("cost write not recorded: %+v", writes)
	}
}

func TestAskHumanDisabled(t *testing.T) {
	s, _ := serve(t)
	req, _ := http.NewRequest("POST", s.URL+"/api/v1/agent/tickets/42/questions",
		bytes.NewReader([]byte(`{"prompt":"?"}`)))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("ask_human must be disabled (409), got %d", resp.StatusCode)
	}
}

func TestRejectsWrongToken(t *testing.T) {
	s, _ := serve(t)
	req, _ := http.NewRequest("GET", s.URL+"/api/v1/agent/tickets/42", nil)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing token must 401, got %d", resp.StatusCode)
	}
}
```

- [ ] Run `go test ./agent/benchstub/` — expect compile failure (package does not exist).
- [ ] Write `agent/benchstub/server.go`. `Config{TicketID int, Token string, Snapshot []byte}`. `NewMux(cfg) (http.Handler, *Recorder)` builds the same route shape as `stub_atc.go:124-232` MINUS the `*testing.T`, with these behavior deltas: `GET {base}` writes `cfg.Snapshot` verbatim (not the hardcoded literal); `POST {base}/spec`, `POST {base}/plan`, `PUT {base}/tasks/{ordering}` read the body, append `Recorder.record(Write{Verb, Body})`, and return `200` (absorbed); **`POST /api/v1/agent/costs` is absorbed the same way (2026-07-19 post-review): one mux entry returns `200 {"status":"recorded"}` and appends `Recorder.record(Write{Verb:"submit_cost", Body})` — absorb-and-record, NOT forward (forwarding would put a real credential inside the isolation boundary; Open decision 7 tracks the resulting gateway-spend blind spot);** `POST {base}/questions`, `GET/PUT .../questions/...` return `409 {"error":"ask_human disabled in replay"}`. `Recorder` is a mutex-guarded `[]Write` with `Writes() []Write` and `WriteJSONL(w io.Writer)` (so `cmd/benchstub` can dump the absorbed writes to the candidate-output artifact path). `Write{Verb string, Body json.RawMessage}`.
- [ ] Run `go test ./agent/benchstub/` — expect pass.
- [ ] Write `cmd/benchstub/main.go`: read `AGENT_TICKET_ID`, `AGENT_PRINCIPAL_TOKEN`, `AGENT_BENCH_SNAPSHOT_FILE` (path to the fixture ticket-snapshot JSON mounted by `restore`), and `AGENT_BENCH_ABSORBED_WRITES_FILE` (where to dump the recorder on SIGTERM); `benchstub.NewMux`; `http.ListenAndServe(":"+port, mux)` on `AGENT_BENCH_STUB_PORT` (default `8090`). On shutdown, write the recorder JSONL so absorbed writes ride into the candidate output. Keep it ~40 lines.
- [ ] Refactor `agent/platformmcp/contracttest/stub_atc.go`: replace the inline mux construction in `NewStubATC` with `mux, _ := benchstub.NewMux(benchstub.Config{TicketID: ticketID, Token: StubToken, Snapshot: defaultContractSnapshot(ticketID)})` where `defaultContractSnapshot` returns the same JSON literal the suite asserted before (`stub_atc.go:142-144`), preserving `AutoAnswer` + question routes ONLY if the contract suite needs them — since `benchstub` disables questions, keep the `*testing.T` stub's question routes as an override layered on top of `benchstub`'s mux (wrap: try questions first, else delegate). Guarding test: the existing `contracttest.Run`/`RunSSEHeartbeats` suites (`agent/platformmcp/contracttest/contracttest_test.go`) must still pass — they exercise `ask_human` park/resume, which `benchstub` disables, so the wrapper MUST re-enable questions for `contracttest`. If that wrapper proves invasive, keep `contracttest` untouched and only SHARE the `StubToken` + snapshot-shape constants — note the decision inline. (Prefer the minimal shared surface; the deployable path is `benchstub`, the contract path is `contracttest`.)
- [ ] Run `go test ./agent/benchstub/ ./agent/platformmcp/contracttest/ && go build ./cmd/benchstub/` — expect green.
- [ ] Commit: `git add agent/benchstub cmd/benchstub agent/platformmcp/contracttest && git commit -m "feat(bench): deployable stub-ATC (agent/benchstub + cmd/benchstub) with write-absorption + ask_human disabled"`

> **Image note (deferred to Task 15/18):** `cmd/benchstub` builds a small image tag (`concourse/benchstub:<ver>`) mounted as the replay step's sidecar. The renderer (Task 6) references the tag via a web flag; the live test (Task 18) is the only place the sidecar transport is actually exercised (SC-11: fake clientset can't).

---

### Task 6: Replay renderer — `agent/benchrunner/replay.RenderReplay` (restore → step-under-test)

The heart of A2. A NEW package that reuses `dispatch.RenderAgentStep` ADDITIVELY and assembles its own `atc.Config`. It NEVER calls `dispatch.Render` and NEVER touches the refusal chain. Golden-file tests (dispatch's honesty pattern). **(Amended 2026-07-19 post-review:** the rendered plan is `[restore → step-under-test]` — the evaluate step is GONE; scoring runs web-side at reconcile (Task 9) via B's `eval.ScoreCell`. `ReplayInput` therefore carries no evaluator fields and no cell id, and no `AGENT_BENCH_CELL_ID` env var exists. The package lives at `agent/benchrunner/replay` per the canonical package map.**)**

**Files:**
- Create: `agent/benchrunner/replay/render.go`
- Test: `agent/benchrunner/replay/render_test.go`
- Test: `agent/benchrunner/replay/testdata/` (golden `.json` files)

**Steps:**

- [ ] Write the failing Ginkgo spec `agent/benchrunner/replay/render_test.go`. It builds a minimal `ReplayInput` (a `review` variant with a single prompt + inputs `["repo","ticket"]`, a fixture bundle — no evaluator: v1 renders none) and asserts the rendered `atc.Config`:

```go
package replay_test

import (
	"testing"

	"github.com/concourse/concourse/agent/api/fixtures"
	"github.com/concourse/concourse/agent/benchrunner/replay"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestReplayRender(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Bench Replay Render Suite")
}

func variantWorkflow() workflow.Config {
	return workflow.Config{
		SchemaVersion: 2, Name: "review-prompts", SpecDelivery: "files",
		Defaults: workflow.Defaults{Model: "sonnet", MaxTurns: 40},
		Prompts:  map[string]string{"review": "Review {{.Ticket.repo}}"},
		Steps: []workflow.Step{{
			Agent: "review", Prompt: "review",
			Inputs: []string{"repo", "ticket"}, Outputs: []string{"workspace", "review-out"},
		}},
	}
}

var _ = Describe("RenderReplay", func() {
	base := replay.ReplayInput{
		StubURL:         "http://localhost:8090",
		StubToken:       "cap1.0.contract-test-token",
		StubImage:       "concourse/benchstub:v1",
		RepoBaseURL:     "https://github.com",
		Variant:         variantWorkflow(),
		VariantName:     "review-prompts",
		VariantVersion:  5,
		VariantHash:     "vh5",
		SnapshotTicket:  replay.SnapshotTicket{ID: 42, Repo: "tdmtrader/concourse", TargetBranch: "main"},
		Bundle: fixtures.Bundle{
			Repo: "tdmtrader/concourse", BaseSHA: "abc123",
			Overlay: []byte("OVERLAY"), TicketSnapshot: []byte(`{"ticket":{"id":42}}`), Config: []byte("CONFIG"),
			Env: fixtures.EnvPins{RunnerImage: "runner:pinned-tag"},
		},
	}

	It("renders restore -> step-under-test as one template job", func() {
		cfg, err := replay.RenderReplay(base)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.Template).To(BeTrue())
		Expect(cfg.Jobs).To(HaveLen(1))
		Expect(cfg.Jobs[0].Name).To(Equal("run"))
		plan := cfg.Jobs[0].PlanSequence
		Expect(plan).To(HaveLen(2)) // NO evaluate step in v1 (2026-07-19 post-review)

		// [0] restore task, produces the artifacts the variant's step names.
		Expect(plan[0].Config).To(BeAssignableToTypeOf(&atc.TaskStep{}))
		restore := plan[0].Config.(*atc.TaskStep)
		Expect(restore.Name).To(Equal("restore"))
		outNames := []string{}
		for _, o := range restore.Config.Outputs {
			outNames = append(outNames, o.Name)
		}
		Expect(outNames).To(ContainElements("repo", "ticket"))

		// [1] step-under-test = RenderAgentStep, additively stubbed. No cell-id
		// env var: scoring is web-side at reconcile (2026-07-19 post-review).
		Expect(plan[1].Config).To(BeAssignableToTypeOf(&atc.AgentStep{}))
		step := plan[1].Config.(*atc.AgentStep)
		Expect(step.Name).To(Equal("review"))
		Expect(step.Env["ATC_EXTERNAL_URL"]).To(Equal("http://localhost:8090"))
		Expect(step.Env["AGENT_TICKET_ID"]).To(Equal("42"))
		Expect(step.Env["AGENT_PRINCIPAL_TOKEN"]).To(Equal("cap1.0.contract-test-token"))
		Expect(step.Sidecars).To(HaveLen(1))
		Expect(step.Sidecars[0].Config.Image).To(Equal("concourse/benchstub:v1"))
	})

	It("never emits a harvest step (no ticket delivery in replay)", func() {
		cfg, _ := replay.RenderReplay(base)
		for _, s := range cfg.Jobs[0].PlanSequence {
			Expect(s.Config).NotTo(BeAssignableToTypeOf(&atc.HarvestStep{}))
		}
	})

	It("matches the golden pipeline", func() {
		cfg, _ := replay.RenderReplay(base)
		assertGolden(cfg, "testdata/review_replay.golden.json") // marshals cfg, compares; UPDATE=1 rewrites
	})
})
```

- [ ] Run `ginkgo ./agent/benchrunner/replay/` — expect compile failure (package does not exist).
- [ ] Write `agent/benchrunner/replay/render.go`. `ReplayInput` carries the resolved cell context (the variant workflow config & pins, the fixture bundle — including its pinned `Env` — + snapshot ticket, stub coords, repo base; NO evaluator fields and NO cell id — amended 2026-07-19 post-review). `RenderReplay(in ReplayInput) (atc.Config, error)`:
  1. Build `restore` via a NEW `restoreTask(in)` — a busybox+git `atc.TaskStep` whose script (built with the `render.go:392-400` base64 idiom) decodes `in.Bundle.TicketSnapshot`/`Config` inline and the `Overlay` inline when it is ≤ the 512 KiB render bound — an over-bound overlay is instead read from the fixture store dir mounted into the replay pod (branch detailed in Task 6a; the 32 MiB cap is 64× the render bound) — clones `repo@base_sha`, `git checkout in.Bundle.BaseSHA`, applies the overlay tarball, and writes the snapshot/config into the `ticket`/`skills` output dirs. `Outputs` = the set of artifact names the variant step's `Inputs` reference (`repo`/`ticket`/`skills`) plus a `workspace` seed. **SINGLE clone-URL source + NO new git-cred path (findings 9/10):** the clone URL is the EXISTING `in.RepoBaseURL + "/" + in.Bundle.Repo + ".git"` — the exact idiom dispatch's own renderer uses at `render.go:280`, where `RepoBaseURL` is the pre-existing `--agent-repo-base-url` flag (default `https://github.com`, `command.go:247`) already carried on `dispatch.RenderInput.RepoBaseURL`. Do NOT add a bench URL template — that is the extra knob-for-one-URL smell finding 10 flags; there is exactly one URL source. The read-only clone REUSES the outcome-watcher's credential (do NOT invent `--agent-bench-git-*` — that would be the FOURTH git-cred system, finding 9): preferred — restore from the outcome-watcher's LOCAL bare mirror (`MirrorCache`, `--agent-outcome-git-dir`) offline when it is reachable by the replay pod (no network, no credential at replay time); otherwise inject the same `outcomewatcher.Auth` (`--agent-outcome-git-username`/`-token`) via a temp GIT_ASKPASS helper — never argv.
  2. Build the step-under-test: construct a `dispatch.RenderInput{Workflow: in.Variant, WorkflowName: in.VariantName, WorkflowVersion: in.VariantVersion, WorkflowHash: in.VariantHash, Ticket: in.SnapshotTicket.AsTicket(), ATCExternalURL: in.StubURL, RepoBaseURL: in.RepoBaseURL}` (Spec/PlanTasks from the snapshot), call `dispatch.RenderAgentStep(ri, in.Variant.Steps[0])`, then additively set `step.Env["AGENT_PRINCIPAL_TOKEN"]=in.StubToken`, `step.Sidecars = []atc.SidecarSource{{Config: stubSidecar(in)}}` where `stubSidecar` is the inline `atc.SidecarConfig{Name:"platform-stub", Image: in.StubImage, Env: [{AGENT_TICKET_ID}, {AGENT_PRINCIPAL_TOKEN}, {AGENT_BENCH_STUB_PORT}, {AGENT_BENCH_SNAPSHOT_FILE}]}`. **Do not** pass `in.Variant.Steps[0]` through `dispatch.Render`; call `RenderAgentStep` directly (additive reuse). If the variant declares >1 step for a step-cell, error (`replay v1 renders single-step variants; multi-step is a workflow cell`).
  3. **No evaluate step (amended 2026-07-19 post-review).** v1 renders no evaluator into the pipeline: scoring runs web-side when the runner reconciles the cell (Task 9 calls B's `eval.ScoreCell` — import `agent/benchscore/eval` — with the cell id as a Go parameter). The LLM-judge evaluator rides a later rendered-step slice; when pod-side evaluators return they will need a cell-id env + an ingestion route (both deferred, §1.12.3).
  4. Assemble `atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "run", PlanSequence: []atc.Step{{Config: restore}, {Config: &step}}}}}`. No git `Resources` (isolation — the clone happens inside `restore`).
  Add a package-level comment stating the refusal-chain-untouched invariant and the additive-reuse rule.
- [ ] Add `assertGolden` + `testdata/review_replay.golden.json` (marshal `cfg` with indent; `UPDATE=1 ginkgo …` rewrites goldens — the dispatch honesty precedent).
- [ ] Run `ginkgo ./agent/benchrunner/replay/` — expect pass. Then `grep -rn "dispatch.Render(" agent/benchrunner/replay/` — expect **zero** hits (only `RenderAgentStep` is used); and confirm `git diff --stat agent/dispatch/render.go` is empty (render.go untouched).
- [ ] Commit: `git add agent/benchrunner/replay && git commit -m "feat(bench): replay renderer (restore->step, no evaluate step) reusing RenderAgentStep additively"`

> **`replay.SnapshotTicket` / `AsTicket()` (settled 2026-07-19 post-review):** A2 needs a `tickets.Ticket`-shaped value from the fixture snapshot to feed `RenderInput`. A1 does NOT ship it — the `SnapshotTicket` adapter (snapshot JSON → `tickets.Ticket{ID, Repo, TargetBranch, …}`) lives HERE in `agent/benchrunner/replay`, per this plan's original adapter footnote; A2's tests construct it directly.

---

### Task 6a: Overlay write path + full-bundle pinning (accepted from A1's Scope-out — inserted 2026-07-19 post-review)

A1's capture slice deliberately shipped NO overlay machinery and marks every v1 production capture `replayable=false, skip_reason='input-bundle-partial'` — the honest early-capture posture. This task is the accepted handoff that flips it: land the overlay write path in A1's (now-landed) `agent/api/fixtures` package, and enrich BOTH server-side capture hooks so NEW captures pin the FULL restore bundle (base_sha + overlay + ticket snapshot + step config) and land `replayable=true`. A1's `Capturer` is already general — given `InputBundleComplete: true` it emits replayable rows; A1's "A2 passes the signal" wording becomes true here, with no A1-side code change. **The replayable corpus begins accruing only when this slice deploys** (A1's accrual-honesty note): plan the first bake-off ≥ a week after it, or pin a starter set.

**Files:**
- Modify: `agent/api/fixtures/blobstore.go` (extend `BlobStore` + `DirBlobStore` with `PutOverlay(r io.Reader, max int64) (ref string, err error)` + `ErrOverCap` — the exact extension A1's Task-5 comment reserved for A2; regenerate the fake)
- Modify: `agent/api/fixtures/capture.go` (`CaptureInput.Overlay io.Reader`, `Config.OverlayMax int64`, the overlay branch + the `overlay-over-bound:<n>B` skip_reason)
- Modify: `atc/exec/agent_step.go`, `atc/exec/harvest_step.go` (full-bundle assembly at ingestion; `InputBundleComplete: true`)
- Modify: `agent/benchrunner/replay/render.go` (the over-bound-overlay store-dir-mount branch in `restore`)
- Test: `agent/api/fixtures/blobstore_test.go`, `agent/api/fixtures/capture_test.go`, `atc/exec/agent_step_test.go`, `atc/exec/harvest_step_test.go`, `agent/benchrunner/replay/render_test.go` (+ a golden)

**Steps:**

- [ ] Write the failing blob-store specs (extend `agent/api/fixtures/blobstore_test.go`): `PutOverlay(strings.NewReader(payload), max)` streams to a content-addressed blob (temp-file + rename — the same `writeOnce` crash-safety) and returns `blob://<sha256>`; two identical overlays dedup to one on-disk file; a payload over `max` returns `ErrOverCap` and leaves NO partial file behind; `Get`/`Delete` round-trip the returned ref.
- [ ] Run `go test ./agent/api/fixtures/` — **expect FAIL** (`undefined: ErrOverCap`; no `PutOverlay` method). Observe RED before implementing (house TDD rule).
- [ ] Implement `PutOverlay` on `DirBlobStore` (stream through a `sha256` hasher into the temp file, enforcing `max` as it reads — never buffer the whole overlay), add both to the `BlobStore` interface, regenerate `fixturesfakes/fake_blob_store.go`. Green.
- [ ] Write the failing capturer specs (extend `agent/api/fixtures/capture_test.go`): a complete bundle WITH an overlay under the cap → `replayable=true`, `input_ref.overlay_ref = blob://…`, and the overlay digest participates in the content hash (`HashBundle.OverlayDigest` — changes only NEW captures' hashes; frozen split columns on existing rows are untouched); an overlay OVER the cap → the row is STILL inserted, `replayable=false, skip_reason='overlay-over-bound:<n>B'`, and no overlay blob is stored (house rule: visible, filterable, never silently dropped); skip-reason precedence is `parked-ask-human` > `input-pin-unresolved` > `overlay-over-bound:<n>B` > `input-bundle-partial`.
- [ ] Run — expect RED; implement the overlay branch in `Capturer.Capture` (`Config.OverlayMax`, threaded from the flag below). Green.
- [ ] Write the failing hook specs (both `atc/exec` suites): the agent + harvest ingestion paths now assemble the FULL restore bundle — the workspace overlay tarball (the non-git delta the flight already materializes), the ticket-state snapshot JSON (serialized ticket+spec+tasks — the SAME shape `benchstub` serves, so replay and capture agree), and the frozen step config — and pass `InputBundleComplete: true`; assert the fixture row lands `replayable=true` with all pins present, and that a missing overlay artifact degrades to `input-bundle-partial` (capture stays fire-and-forget, non-fatal, strictly after the metrics upsert — principle 2 unchanged).
- [ ] Implement the hook enrichment. The ONLY behavioral change to A1's hooks is a richer `CaptureInput` + the `true` signal.
- [ ] Flag: `--agent-fixture-overlay-max-bytes` (default `33554432` — the frozen 32 MiB contract value) joins the Task 15 flag group; `command.go` threads it into `fixtures.Config.OverlayMax` at capturer construction.
- [ ] **Replay-side fidelity (the DEC-14 mount fallback):** overlays ≤ the 512 KiB render bound stay inline-base64 in the `restore` step (Task 6's idiom); ABOVE the bound, `restoreTask` emits a step that reads the blob from the fixture store dir MOUNTED into the replay pod (A1's Q2 explicitly promises mountability) — the 32 MiB cap is 64× the render bound, resolved via this mount fallback, never by inflating the render. Add the renderer branch + a golden covering an over-bound overlay.
- [ ] **Historical rows are excluded FOREVER:** rows already captured with `skip_reason='input-bundle-partial'` are NOT backfilled, re-pinned, or flipped — fresh captures supersede them within days; re-pinning is not worth building. (Recorded identically in A1's non-replayable rule.)
- [ ] Run `go test ./agent/api/fixtures/ && ginkgo ./atc/exec/ --focus="fixture" && ginkgo ./agent/benchrunner/replay/ && go build ./...` — green.
- [ ] Commit: `git add agent/api/fixtures atc/exec agent/benchrunner/replay atc/atccmd && git commit -m "feat(bench): overlay write path + full-bundle pinning - new captures land replayable=true (A1 handoff)"`

---

### Task 7: Negative-control auto-synthesis

`controls: auto` synthesizes two extra variants off the baseline: a `baseline-clone` (byte-identical → MUST tie) and a `degraded` (truncated-context/halved-turns → MUST lose). Pure function over the resolved baseline `workflow.Config`; no I/O.

**Package placement (one package per domain, no duplicate `bench` name):** `SynthesizeControl` lives in `agent/benchrunner` — its sole consumer (the runner's matrix/step-executor) — NOT a separate package named `bench`. A second package literally named `bench` (alongside `agent/api/bench`) would force an import alias everywhere both are used; putting the synthesizer where it is consumed removes that churn and matches the landed one-package-per-domain precedent (`agent/api/tickets`, `outcomes`, `feedback`). It imports `agent/api/bench` (for `ControlRole`/`StepKind`) + `agent/workflow`, both of which `benchrunner` already depends on.

**Files:**
- Create: `agent/benchrunner/controls.go`
- Test: `agent/benchrunner/controls_test.go`

**Steps:**

- [ ] Write the failing `agent/benchrunner/controls_test.go`:

```go
package benchrunner_test

import (
	"testing"

	"github.com/concourse/concourse/agent/api/bench"
	"github.com/concourse/concourse/agent/benchrunner"
	"github.com/concourse/concourse/agent/workflow"
)

func baselineWF() workflow.Config {
	return workflow.Config{
		Name: "review-prompts", SpecDelivery: "files",
		Defaults: workflow.Defaults{Model: "sonnet", MaxTurns: 40},
		Prompts:  map[string]string{"review": "big prompt"},
		Context:  []string{"CONVENTIONS.md"},
		ContextFiles: map[string]string{"CONVENTIONS.md": "lots of context"},
		Steps: []workflow.Step{{Agent: "review", Prompt: "review", MaxTurns: 40,
			Context: []string{"EXTRA.md"}}},
	}
}

func TestBaselineCloneIsByteIdentical(t *testing.T) {
	clone := benchrunner.SynthesizeControl(baselineWF(), bench.ControlBaseline, bench.StepReview)
	if clone.Steps[0].MaxTurns != 40 || len(clone.Context) != 1 {
		t.Fatalf("baseline-clone must be byte-identical to baseline: %+v", clone)
	}
}

func TestDegradedTruncatesContextAndTurns(t *testing.T) {
	deg := benchrunner.SynthesizeControl(baselineWF(), bench.ControlDegraded, bench.StepReview)
	if len(deg.Context) != 0 || len(deg.Steps[0].Context) != 0 {
		t.Fatalf("degraded review must drop context: %+v", deg)
	}
	if deg.Steps[0].MaxTurns != 20 { // halved
		t.Fatalf("degraded must halve max_turns: got %d", deg.Steps[0].MaxTurns)
	}
	// the original is untouched (pure function).
	orig := baselineWF()
	if len(orig.Context) != 1 {
		t.Fatal("SynthesizeControl must not mutate its input")
	}
}
```

- [ ] Run `go test ./agent/benchrunner/ -run TestBaselineClone` — expect compile failure.
- [ ] Write `agent/benchrunner/controls.go` (package `benchrunner`): `SynthesizeControl(base workflow.Config, role bench.ControlRole, kind bench.StepKind) workflow.Config` deep-copies `base` and, for `ControlDegraded`, applies the per-kind degradation (drop `Context`/`ContextFiles` and per-step `Context`, halve every step `MaxTurns` and `Defaults.MaxTurns` — `max(1, n/2)`). `ControlBaseline` returns the deep copy unchanged. **Workflow- and implement-kind experiments never reach `SynthesizeControl` (amended 2026-07-19 post-review, second amendment):** `controls:auto` synthesizes ONLY for kinds the v1 web-side scorer covers (`review`/`plan`); `step_kind:'workflow'` skips auto-controls at matrix expansion (Task 8) with `control_status='none'` + recorded reason `no-evaluator-for-workflow-kind`, and `step_kind:'implement'` likewise skips with reason `implement-scoring-deferred` (pod-side gates+judge — lands with the judge-in-pod slice) — document both in the package comment. Document also that the degradation is the v1 candidate (truncated-context) per §1.12.3 / open Q9.
- [ ] Run `go test ./agent/benchrunner/ -run TestControl` (or `-run 'TestBaselineClone|TestDegraded'`) — expect pass.
- [ ] Commit: `git add agent/benchrunner/controls.go agent/benchrunner/controls_test.go && git commit -m "feat(bench): negative-control synthesis (baseline-clone + degraded truncated-context)"`

---

### Task 8: Experiment runner — matrix expansion + budget admission + skipped-budget

`agent/benchrunner` is the RunnableComponent. This task builds the claim→expand→admit skeleton with the budget envelope; step-cell and workflow-cell EXECUTION land in Tasks 9/10, control verdicts in Task 11. Runner tests use `bench.MemoryStore` + `budgetfakes.FakeChecker` + fakes.

**Files:**
- Create: `agent/benchrunner/runner.go`
- Create: `agent/benchrunner/matrix.go`
- Create: `agent/benchrunner/admission.go` (the A2-owned fixture-admission policy — added 2026-07-19 post-review)
- Test: `agent/benchrunner/matrix_test.go`, `agent/benchrunner/budget_test.go`, `agent/benchrunner/admission_test.go`

**Steps:**

- [ ] Write the failing `agent/benchrunner/admission_test.go` (fake `bench.Fixtures`; 2026-07-19 post-review — admission is A2-OWNED, `Admit` is NOT a `Fixtures` method): `AdmitFixtures(fx bench.Fixtures, sel bench.FixtureSelector) ([]fixtures.Fixture, error)` — with a zero selector it builds `fixtures.FixtureFilter{StepKind: sel.StepKind, Split: "open"}`, calls `fx.List`, and applies the FROZEN v1 default-set policy: newest **N=24** replayable open `source:'production'` fixtures per step_kind, per-repo cap ⌈N/#repos-present⌉, recency window 30d (= the retention window). Full-form selectors (IDs/Tags/Split/Limit) pass through to `List` and bypass the default policy. Assert: the per-repo cap bites, stale (>30d) / non-replayable / non-production rows are excluded, and holdout is drawn ONLY when `split:'holdout'` is requested.
- [ ] Write the failing `agent/benchrunner/matrix_test.go`: `ExpandMatrix(exp bench.Experiment, fixtureIDs []int)` returns cells = `variants × fixtures × reps` PLUS, when `Controls==auto`, one `baseline-clone` + one `degraded` cell per `(fixture × rep)` (variant = baseline name, `control_role` set). Assert counts: 2 variants × 3 fixtures × 1 rep = 6 candidate cells + 2 controls × 3 fixtures = 6 → 12 cells; `controls:none` → 6 cells only. **A `workflow`-kind experiment with `controls:auto` expands NO control cells** — the runner instead records `control_status='none'` with reason `no-evaluator-for-workflow-kind`; **an `implement`-kind experiment likewise expands NO control cells** (`control_status='none'`, reason `implement-scoring-deferred`) (2026-07-19 post-review, second amendment; one assertion each).
- [ ] Write the failing `agent/benchrunner/budget_test.go`: an `admit` helper marks cells `skipped-budget` once the envelope is exceeded OR `GlobalDailyRemaining().Exhausted`. Drive: `FakeChecker.GlobalDailyRemainingReturns(Remaining{Exhausted:true},nil)` → every not-yet-run cell becomes `skipped-budget` with `skip_reason` "global daily cap exhausted"; and a `spentFunc` returning `>= budget_usd` → `skip_reason` "experiment budget envelope exhausted". Assert NO cell is silently dropped (every pending cell ends terminal or skipped).
- [ ] Run `go test ./agent/benchrunner/` — expect compile failure.
- [ ] Write `agent/benchrunner/matrix.go`: `ExpandMatrix` + a `cellPlan` struct that also carries the resolved variant/evaluator workflow configs (for Task 9). Write `agent/benchrunner/runner.go` scaffolding: `Runner` struct (deps: `bench.Store`, `bench.Fixtures`, `budget.Checker`, `workflow.Store`, a `StepExecutor` (Task 9) + `WorkflowExecutor` (Task 10) seam — each exposes `Start(cell, plan)` AND `Reconcile(cell)`, `Defaults`, `EnvelopeSpent func(experimentID int) (float64, error)`). **`Run(ctx) error` runs TWO phases every tick — this is load-bearing (finding: a claim-and-start-only runner leaves cells `running` forever and no experiment ever reaches `complete`):**
  - **Phase A — claim & start (new/pending experiments):** `ClaimPending()` one experiment; resolve fixtures via the benchrunner-owned `AdmitFixtures(fx, sel)` (`admission.go`; `sel.StepKind` set from the experiment — `Admit` is NOT a `Fixtures` method, amended 2026-07-19 post-review; pin each admitted fixture with `Fixtures.Pin(id, true)`); `ExpandMatrix`; `AddCells`; per-cell `admit` (budget check → `skipped-budget` or `Start`). `ClaimPending` only returns PENDING rows, so a claimed experiment is never re-claimed here.
  - **Phase B — reconcile & finalize (in-flight experiments):** `ListRunning()` every `status=running` experiment; for each, `ListCells` — **a `running` experiment with ZERO cells is treated as "expand now" (the crash-between-claim-and-expand window, 2026-07-19 post-review): re-run the phase-A expansion for it; re-expansion is idempotent via the 6-col UNIQUE + `ON CONFLICT DO NOTHING`** — then call the correct executor's `Reconcile(cell)` on every NON-terminal cell (drives `running → ok|failed|error`); then `finalizeIfDone(exp)` (Task 11), which additionally requires `len(cells) > 0`. Without this phase, `Reconcile`/`finalizeIfDone` are never reached (the only executor pass in phase A is `Start`, when cells are still non-terminal).
  Per-cell failures never abort the pass (log + continue); only the `ClaimPending`/`ListRunning`/`ListCells` failures return an error. Budget order per §1.12.3: check `GlobalDailyRemaining()` first, then envelope.
- [ ] Run `go test ./agent/benchrunner/` — expect pass.
- [ ] Commit: `git add agent/benchrunner && git commit -m "feat(bench): experiment runner - matrix expansion + budget-envelope admission + skipped-budget"`

---

### Task 9: Runner — step-cell execution (render replay → CreateRun → link → reconcile + web-side scoring)

A step cell renders via Task 6, persists+creates a run via dispatch's `TemplateSaver`/`RunCreator`, links `cell.pipeline_run_id`, records the cell env, and (on a later tick) reconciles run completion → `ok|failed|error` — scoring the cell web-side via B's `eval.ScoreCell` on success (the reconcile-time scoring locus, amended 2026-07-19 post-review).

**Files:**
- Create: `agent/benchrunner/step_executor.go`
- Test: `agent/benchrunner/step_executor_test.go`

**Steps:**

- [ ] Write the failing `agent/benchrunner/step_executor_test.go` (fakes for `TemplateSaver`/`RunCreator`/`bench.Store`/`bench.Fixtures`): `StepExecutor.Start(cell, plan)` calls `Fixtures.Bundle(cell.FixtureID)`, `replay.RenderReplay(...)`, `Templates.SaveTemplate("agent-bench-cell-<id>", cfg)`, `Runs.CreateRun(pipelineID, nil, "agent-bench")`, then `Store.LinkRun(cell.ID, run.ID())`, **`Store.SetCellEnv(cell.ID, env)` — recording the fixture-pinned tags (`Bundle.Env`) AND the resolved current runner/sidecar tags (v1 records, does not honor — 2026-07-19 post-review)** — and `Store.SetCellStatus(cell.ID, CellRunning, "")`. Assert the template name, the linked run id, the env record, and status. Then `StepExecutor.Reconcile(cell)` reads the run (`RunReader.GetRunByID`), and when `CheckComplete` reports terminal, maps run status → cell status: failed→`failed`, errored→`error`; **on succeeded, BEFORE marking `ok`, for `review`/`plan` cells it fetches the candidate output refs and calls `eval.ScoreCell(ctx, reg, scoresFactory, cell.ID, …)` (B's library, import `agent/benchscore/eval`) with `EvalDeps` wired to real stores — reconcile-time web-side scoring (2026-07-19 post-review); `implement` cells SKIP the scoring call (second amendment — pod-side gates+judge, deferred to the judge-in-pod slice) and go straight to `ok`. A `ScoreCell` error sets the cell `status='error'` with reason `evaluate-failed` — NEVER `failed` (the variant is not to blame).** Assert the mapping for each terminal status, the happy path (score written, cell `ok`), the implement-skip path (cell `ok`, ScoreCell NOT called), and the `evaluate-failed` case.
- [ ] Run `go test ./agent/benchrunner/ -run StepExecutor` — expect compile failure.
- [ ] Write `agent/benchrunner/step_executor.go`. `Start` builds the `replay.ReplayInput` from the cell + plan (the variant config, pins, stub coords from `Runner` config, bundle bytes incl. `Bundle.Env` — no evaluator fields), renders, saves, creates the run, links, records the cell env (`SetCellEnv`: pinned + resolved tags), marks running. `Reconcile` maps the terminal run status and, on success, runs the web-side scoring call for `review`/`plan` cells (`eval.ScoreCell` with `EvalDeps` wired to real stores — the reconcile-time scoring locus, §1.12.3) before `SetCellStatus(ok)`; `implement` cells skip straight to `ok` (scoring deferred to the judge-in-pod slice); a scoring error maps to `error`/`evaluate-failed`. Mirror the dispatcher's `RunReader` seam (`GetRunByID`) and the `runStatusFromBuildStatus` worst-of precedence (errored>failed>succeeded) from `atc/db/pipeline_run.go:190`. A render/save/create error marks the cell `error` with the reason (never leaves it `running` forever).
- [ ] Run `go test ./agent/benchrunner/ -run StepExecutor` — expect pass.
- [ ] Commit: `git add agent/benchrunner && git commit -m "feat(bench): step-cell execution (replay render + run creation + completion reconcile)"`

---

### Task 10: Runner — workflow-cell execution (dispatch real tickets, plan 14 §1.12.2 verbatim)

Workflow (end-to-end) cells are the deliberate exception: they create ordinary tickets and ride the REAL dispatcher — one render+admit+run path, no second entrypoint. Preserve plan 14 §1.12.2 verbatim.

**Files:**
- Create: `agent/benchrunner/workflow_executor.go`
- Test: `agent/benchrunner/workflow_executor_test.go`

**Steps:**

- [ ] Write the failing `agent/benchrunner/workflow_executor_test.go` (fakes: `tickets.MemoryStore`, `bench.MemoryStore`): `WorkflowExecutor.Start(cell, plan)` creates an `agent_tickets` row (`origin:'fly'` — the only legal origin without a ticket-migration change; there is NO `'bench'` origin, and `MemoryStore.Create` would silently accept one while the real DB CHECK rejects it — `repo`=fixture repo, `workflow_name`/`workflow_version`=the variant, `body`=fixture prompt, `created_by`="experiment-<id>" which is how bench tickets stay distinguishable), transitions it `draft→queued` via `tickets.Store.Transition`, and sets `cell.ticket_id`. Assert the ticket exists at `StateQueued`, `origin=="fly"`, and `cell.TicketID` is linked. Then `Reconcile(cell)` mirrors the ticket's terminal state into the cell: `merged`/`merged_with_fixes`/`needs_review`→`ok`, `failed`→`failed`, `errored`→`error` (plan 14 §1.12.2 mapping verbatim); assert each.
- [ ] Run `go test ./agent/benchrunner/ -run WorkflowExecutor` — expect compile failure.
- [ ] Write `agent/benchrunner/workflow_executor.go`. `Start` builds the ticket via the tickets store's create (`origin:'fly'` — reuse the legal origin, per §1.12.3; do NOT invent `'bench'`) + `Transition(id, StateDraft, StateQueued, TransitionMeta{})` — NO call to `dispatch.Render`, `RenderReplay`, or `CreateRun` (the dispatcher owns that path; §1.12.2). `Reconcile` reads the ticket state and mirrors it per the table. Add a package doc-comment quoting the §1.12.2 decision so a future editor does not "optimize" it into a direct render. The runner still gated workflow cells on `GlobalDailyRemaining()`+envelope in Task 8 before calling `Start`; the dispatcher does its own per-ticket admission on top (double-gate is intentional and safe — the tighter of the two wins).
- [ ] Run `go test ./agent/benchrunner/ -run WorkflowExecutor` — expect pass.
- [ ] Commit: `git add agent/benchrunner && git commit -m "feat(bench): workflow-cell execution via real dispatcher tickets (plan 14 §1.12.2 verbatim)"`

---

### Task 11: Runner — control verdicts + experiment completion + evaluator-suspect

Once cells are terminal, evaluate the negative controls against B's scores and finalize the experiment. `baseline-clone` MUST tie the baseline; `degraded` MUST lose. A control failure flags `evaluator-suspect`.

**Files:**
- Create: `agent/benchrunner/controls_verdict.go`
- Modify: `agent/benchrunner/runner.go` (call the verdict + finalize from phase B when all cells terminal)
- Test: `agent/benchrunner/controls_verdict_test.go`
- Test: `agent/benchrunner/runner_test.go` (the two-tick driver — proves the loop actually reconciles + finalizes)

**Steps:**

- [ ] Write the failing `agent/benchrunner/controls_verdict_test.go` (fake `bench.ScoreReader`): `ControlVerdict(scores []bench.CellScore, baseline bench.Variant, tol float64) bench.ControlStatus`. Cases: baseline-clone metrics within `tol` of baseline AND degraded strictly worse on the primary metric → `pass`; baseline-clone diverges beyond `tol` → `fail`; degraded not worse (ties or beats) → `fail`; no control cells (controls=none) → `none`. Then a `Finalize` test: all cells terminal + `control_status=fail` → `SetExperimentStatus(evaluator-suspect)`; `control_status=pass` → `FinishExperiment` (status=complete). A cell still `running`/`pending` → no finalize.
- [ ] Write the failing `agent/benchrunner/runner_test.go` — the **two-tick driver** that exercises `Run` end-to-end (the prior task tests call `Start`/`Reconcile`/`Finalize` directly and never drive the loop, so nothing catches a runner that starts but never reconciles). Use `bench.MemoryStore` + fake executors + fake `ScoreReader`: seed one pending experiment; **tick 1** `Run(ctx)` → phase A claims it, expands+admits, `Start`s its cells (all `running`), experiment now `running` (not yet finalized — assert `ListRunning` returns it and no cell is terminal); then simulate run/ticket completion on the fake executors so `Reconcile` will report terminal; **tick 2** `Run(ctx)` → phase B `ListRunning` finds it, `Reconcile` drives every cell terminal, `finalizeIfDone` runs the verdict and `FinishExperiment`; assert the experiment is now `complete` (or `evaluator-suspect` on a seeded control-fail) and every cell is `Terminal()`. Assert a mid-flight tick with a still-`running` cell does NOT finalize. **Tick 3 (crash-window — added 2026-07-19 post-review):** seed a second experiment directly as `running` with ZERO cells (simulating a crash between claim and expand); the tick's phase B treats it as "expand now" (idempotent re-expansion — the MemoryStore's duplicate-skipping `AddCells` mirrors `ON CONFLICT DO NOTHING`) and does NOT finalize it (`finalizeIfDone` requires `len(cells) > 0`).
- [ ] Run `go test ./agent/benchrunner/ -run Control` — expect compile failure.
- [ ] Write `agent/benchrunner/controls_verdict.go`: `ControlVerdict` aggregates `CellScore.Metrics` per `control_role`, compares on the step-kind's primary metric via the **FROZEN `primaryMetric(kind)` map (2026-07-19 post-review, second amendment): `review→"precision"`, `plan→"grounding"`, `implement→none in v1` (`"judge_total"` when the judge-in-pod slice lands — the composite scalar stays the A2↔B joint decision), `workflow→none`** (workflow and implement experiments carry no auto-controls — their control_status is `none`), applies the tie tolerance, and returns the status. Add `finalizeIfDone(exp)` to `runner.go`, **called from the runner's phase-B reconcile pass** (Task 8) — after `Reconcile` has driven cells terminal, NOT from phase A (in phase A cells are still non-terminal, so finalize there is a no-op). If every cell `Terminal()` **and `len(cells) > 0`** (a zero-cell experiment is never "done" — 2026-07-19 post-review), compute the verdict via `ScoreReader`, `SetControlStatus`, then `SetExperimentStatus(evaluator-suspect)` on fail else `FinishExperiment`. Missing score rows for `ok` cells → verdict `pending` (do not finalize yet). **Score wait is now a narrow crash-window guard (amended 2026-07-19 post-review — reconcile-time scoring):** scores are written SYNCHRONOUSLY by `Reconcile` itself (`eval.ScoreCell` runs web-side before the cell is marked `ok`; a scoring error maps the cell to `error`/`evaluate-failed`), so there is no out-of-band ingestion to wait on. A missing score row for an `ok` cell can only arise from a crash between the score write and the status write. `--agent-bench-score-timeout` (operator-internal, off the two-field path) is RETAINED solely as that crash-window guard, so a never-scored experiment eventually finalizes `complete` with a logged warning rather than hanging (honest partial).
- [ ] Run `go test ./agent/benchrunner/` — expect pass (the full package).
- [ ] Commit: `git add agent/benchrunner && git commit -m "feat(bench): control verdicts (tie/lose) + experiment finalize + evaluator-suspect flag"`

---

### Task 12: `agent/api/bench` HTTP handler — create / list / get experiment

The write + read experiment endpoints. `CreateExperiment` validates the two-field spec, resolves defaults, and inserts a `pending` experiment (the runner picks it up — 202-semantics). Plain `testing`, `UserNameFunc` injection (no `accessor` import — cycle, per `agent/api/tickets/handler.go:14`).

**Files:**
- Create: `agent/api/bench/handler.go`
- Test: `agent/api/bench/handler_test.go`

**Steps:**

- [ ] Write the failing `agent/api/bench/handler_test.go`: `POST /experiments` with `{"step_kind":"review","variants":[{"name":"review-prompts","version":5}]}` → 200, body is an `Experiment` with `status:"pending"`, `spec.variants` includes `live`, `budget_usd` = the injected default. Bad body (`step_kind:""`) → 400. `GET /experiments` → list newest-first. `GET /experiments/:id` → the experiment with its cells; unknown id → 404; non-integer id → 400. Assert `created_by` = the injected username.
- [ ] Run `go test ./agent/api/bench/ -run Handler` — expect compile failure (`NewHandler` undefined).
- [ ] Write `agent/api/bench/handler.go` with the **FINAL** constructor signature so Task 13 adds only a method body, not a constructor rewrite (finding: don't ship a 3-arg `NewHandler` in Task 12 that Task 13 immediately rewrites to 4-arg — that churns every test helper). `Handler{store Store, userName UserNameFunc, defaults Defaults, scoreReader ScoreReader, workflowOutcomes WorkflowOutcomes}`, `NewHandler(store Store, userName UserNameFunc, defaults Defaults, scoreReader ScoreReader, workflowOutcomes WorkflowOutcomes) *Handler` (amended 2026-07-19 post-review: the FINAL signature also carries the `WorkflowOutcomes` reader that Task 13's workflow-cell branch consumes — same never-rewrite rationale). Task 12's `handler_test.go`'s `newHandler` helper passes nil/fake `ScoreReader` + `WorkflowOutcomes` (Results is not exercised until Task 13). `CreateExperiment` decodes `struct{ Name, Description string; Spec ExperimentSpec }` (or accept the spec fields at top level — keep the fly path a flat body), `spec.Validate()` → 400, `spec.ResolveDefaults(h.defaults)`, `store.CreateExperiment(name, desc, spec.StepKind, resolved, resolved.BudgetUSD, h.userName(r))` → 200. `ListExperiments`/`GetExperiment` read paths. Add a stub `Results` method that returns `501 Not Implemented` until Task 13 fills its body (so the route can be wired now and the constructor never changes). Reuse the `ticketID`/`writeJSON`/`readJSON` helper idiom from `agent/api/outcomes/handler.go` (or a local `experimentID`).
- [ ] Run `go test ./agent/api/bench/ -run Handler` — expect pass.
- [ ] Commit: `git add agent/api/bench && git commit -m "feat(bench): experiment create/list/get handler (two-field spec, defaults resolution)"`

---

### Task 13: `agent/api/bench` results handler — variant × metric table with baseline deltas + control verdicts

`GET /experiments/:id/results` joins cells → B's scores (via `ScoreReader`) into a `variant × metric` table with per-metric deltas vs the baseline and the control verdict, honestly rendering `skipped-budget` cells + missing scores. This is what C's fixture tier and the fly `results` verb consume.

**Files:**
- Modify: `agent/api/bench/handler.go` (add `Results` + the results DTO)
- Test: `agent/api/bench/results_test.go`

**Steps:**

- [ ] Write the failing `agent/api/bench/results_test.go` (fake `ScoreReader` returning per-cell metrics for `live`, a candidate, and the two controls): `GET /experiments/1/results` → a `ResultsResponse` with one row per variant, each carrying `metrics[k]` (mean across reps) and `deltas[k]` vs the baseline (`live`), plus `control_status`, `skipped_count`, and a `single_rep bool` flag (spec §3 — single-rep experiments say so). Assert: the baseline row has zero deltas; a candidate's `precision` delta = candidate − baseline; a `skipped-budget` cell increments `skipped_count` and does not corrupt the mean; an `ok` cell with no score row is reported in a `pending_scores` count (open Q12 — evaluator pending ≠ low score; a crash-window rarity under reconcile-time scoring, amended 2026-07-19 post-review). **Workflow-cell branch (2026-07-19 post-review):** for an experiment whose cells carry `ticket_id` (a `workflow` experiment — no `agent_bench_scores` rows exist), the response's `VariantRow.Metrics` map is populated from the fake `WorkflowOutcomes` by-ticket projection — `cost_usd`, findings count, and 0/1 indicator metrics for terminal status / merge outcome when present.
- [ ] Run `go test ./agent/api/bench/ -run Results` — expect compile failure.
- [ ] Fill the `Results` method body in `handler.go` (the stub returning 501 from Task 12 — the `ScoreReader` and `WorkflowOutcomes` are ALREADY constructor fields and the route is already wired, so this task adds NO constructor or test-helper edits): load the experiment + cells (`store`), `scores := h.scoreReader.ScoresForExperiment(id)`, group by variant (+control_role), average `Metrics` across reps, compute deltas vs `spec.Baseline()`, attach `experiment.ControlStatus`, count `skipped-budget` cells and `ok`-cells-without-scores. **Workflow-cell branch (2026-07-19 post-review — A2 OWNS this projection):** for cells with `ticket_id` set, call `h.workflowOutcomes.OutcomesForTickets(...)` and project the per-ticket `agent_run_metrics`/`agent_outcomes` values into the SAME `VariantRow.Metrics` map (cost_usd, findings count, terminal-status / merge-outcome 0/1 indicators when present). DTO: `ResultsResponse{ExperimentID int, Baseline string, ControlStatus string, SingleRep bool, SkippedCount, PendingScores int, Rows []VariantRow, EnvRecords []CellEnv}`, `VariantRow{Variant string, Version int, ControlRole string, Metrics, Deltas map[string]float64, Reps int}`, `CellEnv{CellID int, Env json.RawMessage}` — **`EnvRecords` echoes each cell's recorded pinned-vs-resolved image tags (`agent_bench_cells.env`) so env skew is always visible (2026-07-19 post-review)**. `metrics` values are numbers only (skeleton §2) so C's paired-on-fixture deltas read positionally.
- [ ] Run `go test ./agent/api/bench/` — expect pass (whole package).
- [ ] Commit: `git add agent/api/bench && git commit -m "feat(bench): results endpoint (variant x metric, baseline deltas, control verdict, skipped honesty)"`

---

### Task 14: `agent/api/bench` fixtures handler — list / tag / pin (consumes A1)

The fixture list/tag/pin routes are A2's, backed by A1's `Fixtures` store. Read + two curation writes; no fixture rows are created here (capture is A1's).

**Files:**
- Modify: `agent/api/bench/handler.go` (add `NewFixturesHandler` + `ListFixtures`/`TagFixture`/`PinFixture`)
- Test: `agent/api/bench/fixtures_handler_test.go`

**Steps:**

- [ ] Write the failing `agent/api/bench/fixtures_handler_test.go` (fake `Fixtures`): `GET /fixtures?step_kind=review&tag=curated&split=open` → `Fixtures.List(filter)` mapped to JSON; `PUT /fixtures/:id/tags` body `{"add":["curated"],"remove":["stale"]}` → `Fixtures.Tag(id, add, remove)` → 200; `PUT /fixtures/:id/pin` body `{"pinned":true}` → `Fixtures.Pin(id,true)` → 200. Bad id → 400; unknown fixture → 404.
- [ ] Run `go test ./agent/api/bench/ -run Fixtures` — expect compile failure.
- [ ] Write `FixturesHandler{fx Fixtures}` + the three methods, mapping query params → `fixtures.FixtureFilter`. Keep the JSON shape = A1's `fixtures.Fixture` (no re-copy of label columns — labels are joins, principle 3).
- [ ] Run `go test ./agent/api/bench/` — expect pass.
- [ ] Commit: `git add agent/api/bench && git commit -m "feat(bench): fixtures list/tag/pin handler (consumes A1 fixture store)"`

---

### Task 15: Six-touchpoint wiring — routes, auth tiers, ATC + component wiring, web flags

Registers the seven routes, wires the exhaustive wrappa switch (all on `CheckAgentAuthorizationHandler`; the `default: panic` is this task's failing test), adds roles, threads the bench + fixtures servers through `atc/api/handler.go`, adds the web-flag group, constructs the stores + runner in `atc/atccmd/command.go`, and registers the `agent_bench_runner` RunnableComponent (polling).

**Files:**
- Modify: `atc/routes.go` (7 route-name consts + 7 `Routes` entries, adjacent to the REAL agent routes at HEAD — the tickets/dispatcher block, `routes.go:323-348`; plan-14's experiment routes were superseded and never built, so there is no `CreateAgentExperiment` to grep)
- Modify: `atc/wrappa/api_auth_wrappa.go` (7 routes into the `CheckAgentAuthorizationHandler` case group)
- Modify: `atc/api/accessor/roles.go` (Viewer for list/get/results/fixtures-list; Member for create/tag/pin)
- Modify: `atc/api/handler.go` (`NewHandler` params: `benchStore bench.Store`, `benchFixtures bench.Fixtures`, `benchScores bench.ScoreReader`; construct `benchServer`/`benchFixturesServer`; handlers map)
- Modify: `atc/api/api_suite_test.go` (append the new `NewHandler` args)
- Modify: `atc/component.go` (`ComponentAgentBenchRunner = "agent_bench_runner"`)
- Modify: `atc/atccmd/command.go` (flag group; store + runner construction; component registration)
- Test: `agent/api/bench/route_registration_test.go`

**Steps:**

- [ ] Write the failing `agent/api/bench/route_registration_test.go` (precedent `agent/api/feedback/route_registration_test.go`): assert all seven `atc.<Name>` route consts exist in `atc.Routes` with the §1.12.3 method+path.
- [ ] Run `go test ./agent/api/bench/ -run TestBenchRoutesRegistered` — expect `undefined: atc.CreateAgentBenchExperiment`.
- [ ] Add the 7 consts + 7 `Routes` entries to `atc/routes.go` (there is NO plan-14 `CreateAgentExperiment` block — it was never built; anchor on the real agent routes at HEAD, e.g. after the `GetAgentDispatcher`/`SetAgentDispatcher` const + `Routes` rows, `routes.go:171-172` and `:347-348`. Route ordering is functionally irrelevant, so just append to the existing agent block):

```go
	CreateAgentBenchExperiment = "CreateAgentBenchExperiment"
	ListAgentBenchExperiments  = "ListAgentBenchExperiments"
	GetAgentBenchExperiment    = "GetAgentBenchExperiment"
	GetAgentBenchResults       = "GetAgentBenchResults"
	ListAgentBenchFixtures     = "ListAgentBenchFixtures"
	TagAgentBenchFixture       = "TagAgentBenchFixture"
	PinAgentBenchFixture       = "PinAgentBenchFixture"
```
```go
	{Path: "/api/v1/agent/bench/experiments", Method: "POST", Name: CreateAgentBenchExperiment},
	{Path: "/api/v1/agent/bench/experiments", Method: "GET", Name: ListAgentBenchExperiments},
	{Path: "/api/v1/agent/bench/experiments/:experiment_id", Method: "GET", Name: GetAgentBenchExperiment},
	{Path: "/api/v1/agent/bench/experiments/:experiment_id/results", Method: "GET", Name: GetAgentBenchResults},
	{Path: "/api/v1/agent/bench/fixtures", Method: "GET", Name: ListAgentBenchFixtures},
	{Path: "/api/v1/agent/bench/fixtures/:fixture_id/tags", Method: "PUT", Name: TagAgentBenchFixture},
	{Path: "/api/v1/agent/bench/fixtures/:fixture_id/pin", Method: "PUT", Name: PinAgentBenchFixture},
```

- [ ] Run `go test ./agent/api/bench/ -run TestBenchRoutesRegistered` → PASS; then `ginkgo ./atc/wrappa/` → panic `you missed a spot: "CreateAgentBenchExperiment"`.
- [ ] Add all seven to the `CheckAgentAuthorizationHandler` case group in `atc/wrappa/api_auth_wrappa.go` (grep the real `CheckAgentAuthorizationHandler` case that already lists `atc.GetAgentTicketDiff` etc., `api_auth_wrappa.go:245-246`, and add the seven route consts to that case group — NOT next to any plan-14 experiment routes, which do not exist; case ordering is irrelevant). Roles in `atc/api/accessor/roles.go`: `ListAgentBenchExperiments`/`GetAgentBenchExperiment`/`GetAgentBenchResults`/`ListAgentBenchFixtures` → `ViewerRole`; `CreateAgentBenchExperiment`/`TagAgentBenchFixture`/`PinAgentBenchFixture` → `MemberRole`.
- [ ] Run `ginkgo ./atc/wrappa/ ./atc/api/accessor/` → green.
- [ ] Add `ComponentAgentBenchRunner = "agent_bench_runner"` to `atc/component.go`.
- [ ] Add the web-flag group to `atc/atccmd/command.go` (near the dispatcher/outcome groups):

```go
	AgentBench struct {
		Enabled          bool          `long:"agent-bench-runner-enabled" description:"Run the experiment runner component (polling)."`
		ExperimentBudget float64       `long:"agent-bench-experiment-budget-default" default:"12" description:"Default per-experiment budget envelope (USD), under the global daily cap."`
		CheckInterval    time.Duration `long:"agent-bench-check-interval" default:"30s" description:"Experiment runner polling interval (never notify-only)."`
		ScoreTimeout     time.Duration `long:"agent-bench-score-timeout" default:"30m" description:"How long to wait for evaluator scores before finalizing an experiment complete with a warning."`
		StubImage        string        `long:"agent-bench-stub-image" description:"Image tag for the replay stub-ATC sidecar (cmd/benchstub). Empty disables step replay."`
		// (added 2026-07-19 post-review, Task 6a) lives with the --agent-fixture-*
		// family semantically but is wired here with the bench group — the overlay
		// producer is A2's.
		OverlayMaxBytes  int64         `long:"agent-fixture-overlay-max-bytes" default:"33554432" description:"Max fixture overlay blob size in bytes (32 MiB frozen contract value); an over-bound overlay records skip_reason overlay-over-bound:<n>B."`
	} `group:"Agent Bench"`
```

> **NO new git-credential path (guiding principle 1 — "any design that invents a new credential path is wrong").** The replay `restore` clone REUSES existing knobs — do NOT add `--agent-bench-git-*` flags (that would be the FOURTH git-cred/repo-cache system; the codebase already flags this hazard at `agent/gitcheck/mirror.go:6-16`, "consolidation is future work"). **Clone URL:** the pre-existing `--agent-repo-base-url` (default `https://github.com`, `command.go:247`) already carried on `RenderInput.RepoBaseURL` — one source, the same idiom dispatch uses at `render.go:280` (finding 10). **Credential:** the outcome-watcher's read-only cred — `--agent-outcome-git-username`/`--agent-outcome-git-token` via `outcomewatcher.Auth` (verified: flags @ `atc/atccmd/command.go:257-260`; `Auth = gitcheck.Auth` @ `agent/outcomewatcher/mirror_cache.go:10`). Preferred: where the outcome-watcher's bare mirror (`MirrorCache`, `--agent-outcome-git-dir`) is reachable by the replay pod, restore from the LOCAL mirror offline (no network, no credential at replay time); otherwise thread the same `outcomewatcher.Auth` through. Construct `benchrunner.New(...)` in Task 15's `command.go` wiring with `cmd.AgentRepoBaseURL` + the existing outcome-git cred values, not new bench ones.

- [ ] In `atc/api/handler.go`: add the four params (`benchStore bench.Store`, `benchFixtures bench.Fixtures`, `benchScores bench.ScoreReader`, `benchWorkflowOutcomes bench.WorkflowOutcomes` — the fourth added 2026-07-19 post-review for the workflow-cell results branch), construct `benchServer := benchapi.NewHandler(benchStore, agentUserName, benchapi.Defaults{...}, benchScores, benchWorkflowOutcomes)` and `benchFixturesServer := benchapi.NewFixturesHandler(benchFixtures)` after the other agent servers, and map the seven routes (`CreateAgentBenchExperiment→benchServer.CreateExperiment`, `…Results→benchServer.Results`, `ListAgentBenchFixtures→benchFixturesServer.ListFixtures`, etc.). Alias import `benchapi "github.com/concourse/concourse/agent/api/bench"`.
- [ ] In `atc/atccmd/command.go`: construct `db.NewAgentBenchFactory(dbConn)`, `db.NewAgentStepFixturesFactory(dbConn, blobs)` (A1's — note the two-arg constructor: the fixture `DirBlobStore` hydrates `Bundle`, so the bench surfaces require `--agent-fixture-store-dir` to be set; without it, gate per the caveat below), `db.NewAgentBenchScoreReader(dbConn)` (B's), and a small `db`-backed `bench.WorkflowOutcomes` reader (`agent_run_metrics ⋈ agent_outcomes` by ticket), pass to `NewHandler`. **Reconcile-time scoring deps (2026-07-19 post-review):** also construct B's `db.NewAgentBenchScoresFactory(dbConn)` + the `agent/benchscore/eval` registry and thread them into `benchrunner.New(...)` so `StepExecutor.Reconcile` can call `eval.ScoreCell` with `EvalDeps` wired to real stores. Register the component gated on `cmd.AgentBench.Enabled && cmd.AgentBench.StubImage != ""`, appended where the dispatcher/outcome components are, `Runnable: benchrunner.New(...)`, `Interval: cmd.AgentBench.CheckInterval`. Update `atc/api/api_suite_test.go` with `bench.NewMemoryStore()`, a fake `Fixtures`, and fake/nil `ScoreReader` + `WorkflowOutcomes`.
- [ ] Run `go build ./... && ginkgo ./atc/wrappa/ ./atc/api/accessor/ && go test ./agent/api/bench/` → green.
- [ ] Commit: `git add atc agent docs && git commit -m "feat(bench): register bench routes + agent_bench_runner component + web flags"`

> **Cross-track wiring caveat:** `db.NewAgentStepFixturesFactory` (A1) and `db.NewAgentBenchScoreReader` (B) must exist by the time this task lands. Because A1 lands first (Sequencing) and B lands before A2's harness (spec: "then B … then the rest of A"), both are present. If a track lags, gate the runner + fixtures/results routes behind a nil-check (server returns 501 when its dependency is nil) rather than blocking the build — note the temporary gate in the commit.

---

### Task 16: `go-concourse` client — `agent_bench.go`

One client method per route, over `internal.Request`/`Response` + rata (the `agent_tickets.go` idiom). `RequestName` = the `atc.RouteName` from Task 15.

**Files:**
- Create: `go-concourse/concourse/agent_bench.go`
- Test: `go-concourse/concourse/agent_bench_test.go`

**Steps:**

- [ ] Write the failing `agent_bench_test.go` (ghttp, the `agent_tickets_test.go` recipe): mock ATC responses for `CreateAgentBenchExperiment` (POST body → `Experiment`), `GetAgentBenchResults` (→ `ResultsResponse`), `ListAgentBenchFixtures`. Assert method, path, query, and decode.
- [ ] Run `ginkgo ./go-concourse/concourse/ --focus="AgentBench"` — expect compile failure.
- [ ] Write `agent_bench.go`: `CreateBenchExperiment(bench.ExperimentSpec, name, description string) (bench.Experiment, error)`, `ListBenchExperiments() ([]bench.Experiment, error)`, `GetBenchExperiment(id int) (bench.Experiment, error)`, `GetBenchResults(id int) (bench.ResultsResponse, error)`, `ListBenchFixtures(filter fixtures.FixtureFilter) ([]fixtures.Fixture, error)`, `TagBenchFixture(id int, add, remove []string) error`, `PinBenchFixture(id int, pinned bool) error`.
- [ ] Run `ginkgo ./go-concourse/concourse/ --focus="AgentBench"` — expect pass.
- [ ] Commit: `git add go-concourse/concourse && git commit -m "feat(bench): go-concourse client for agent bench routes"`

---

### Task 17: `fly agent bench run|results|fixtures`

The CLI. `run` is the two-field acceptance bar; `results` renders the variant × metric table; `fixtures` lists/tags/pins. Append `Bench AgentBenchCommand` to `AgentCommand` (additive-merge convention, `fly/commands/agent.go:6`).

**Files:**
- Create: `fly/commands/agent_bench.go`
- Modify: `fly/commands/agent.go` (add the `Bench` field)
- Test: `fly/integration/agent_bench_test.go`

**Steps:**

- [ ] Write the failing `fly/integration/agent_bench_test.go` (mock ATC, the `agent_tickets` integration recipe; mock version = `versions.go` `0.1.0`): `fly agent bench run --step review --variant review-prompts@v5` posts `{"step_kind":"review","variants":[{"name":"review-prompts","version":5}]}` and prints `experiment N: … variants … × … fixtures × controls; budget $… (default envelope)`; `fly agent bench results N` renders a table (variant, precision, recall, cost, turns, Δ, controls); `fly agent bench fixtures --step review` lists fixtures. Assert the **minimal** `run` invocation needs exactly `--step` + `--variant` (the guardrail).
- [ ] Run `ginkgo ./fly/integration/ --focus="agent bench"` — expect build/compile failure.
- [ ] Write `fly/commands/agent_bench.go`: `AgentBenchCommand{ Run AgentBenchRunCommand; Results AgentBenchResultsCommand; Fixtures AgentBenchFixturesCommand }`. `AgentBenchRunCommand`: `Step string \`long:"step" required:"true"\``, `Variant []string \`long:"variant" required:"true"\`` (repeatable; parse `name@vN` → `bench.Variant`; a bare `name` → version 0 = live), plus HIDDEN full-form flags (`--fixtures-tag`, `--repetitions`, `--evaluator`, `--budget`, `--controls`) that never appear on the minimal path. `Execute` builds `bench.ExperimentSpec`, calls `target.Client().CreateBenchExperiment(...)`, prints the summary. `Results` calls `GetBenchResults` → `ui.Table`. `Fixtures` sub-verbs `list`/`tag`/`pin`. Parse `name@vN` defensively (memory: parallel-session string-arg lesson).
- [ ] Add `Bench AgentBenchCommand \`command:"bench" description:"Run step-level bench experiments and read results"\`` to `AgentCommand` in `fly/commands/agent.go`.
- [ ] Run `ginkgo ./fly/integration/ --focus="agent bench"` — expect pass.
- [ ] Commit: `git add fly && git commit -m "feat(bench): fly agent bench run|results|fixtures (two-field minimal path)"`

---

### Task 18: Live theborg verification (throwaway namespace) + round-trip echo self-test

Two verifications the fake clientset cannot do. (a) The bench's own canary: a deterministic echo-step fixture where capture→replay reproduces the recorded output byte-stable. (b) SC-11 (house lesson): one real replay pod with the stub sidecar — the fake clientset's `GetLogs`/sidecar transport is a no-op, so the sidecar redirect + write-absorption + `ask_human`-disabled path is only real on a live cluster.

**Files:**
- Create: `agent/benchrunner/replay/live_replay_test.go` (`//go:build live`, plain Go, the `atc/worker/jetbridge/live_*_test.go` recipe)
- Create: `agent/benchrunner/roundtrip_test.go` (hermetic echo-step canary, no cluster)

**Steps:**

- [ ] Write `agent/benchrunner/roundtrip_test.go`: build a synthetic `echo` fixture whose captured output is a fixed JSON; render+execute the replay against a fake run whose "output" echoes the input; assert the candidate output equals the recorded output byte-for-byte (the canary that proves the restore→step wiring + reconcile-time scoring is faithful without a cluster — amended 2026-07-19 post-review). Hermetic; runs under `make test-quick`.
- [ ] Write `agent/benchrunner/replay/live_replay_test.go` (`//go:build live`): against a THROWAWAY namespace (NOT `cicd`/`concourse`), create a replay pod from a real `RenderReplay` config whose step-under-test is a trivial agent step + the `benchstub` sidecar; assert within a 5m bound that (1) the sidecar answers `GET /api/v1/agent/tickets/<id>` with the fixture snapshot, (2) a `submit_spec` from the step is absorbed (200) and appears in the absorbed-writes artifact, (3) an `ask_human` attempt gets 409 and the step does NOT park. `t.Cleanup` deletes pods then the namespace. Run: `KUBECONFIG=~/.kube/config K8S_TEST_NAMESPACE=<throwaway> go test -tags live -run '^TestLiveReplaySidecar$' -v -count=1 -timeout 5m ./agent/benchrunner/replay/`. **Prerequisite (2026-07-19 post-review):** the replayable corpus only accrues AFTER Task 6a (full-bundle pinning) deploys — A1-only v1 captures are `input-bundle-partial` and inadmissible; run this against captures post-dating Task 6a, or pin a starter set.
- [ ] Run the hermetic canary: `go test ./agent/benchrunner/ -run RoundTrip` — expect pass. Run the live test against theborg once (manual, throwaway ns) — expect the three assertions green.
- [ ] Commit: `git add agent/benchrunner && git commit -m "test(bench): round-trip echo canary + live theborg replay-sidecar verification (SC-11)"`

---

## Execution notes

**Running this workstream's test suite (all green before close-out — `make test-quick` is the per-merge gate; PostgreSQL required):**

```bash
pg_isready
go test ./agent/api/bench/ ./agent/benchstub/                 # types, spec, handlers, stub (plain testing)
go test ./agent/benchrunner/                                  # matrix, budget admission, executors, control synthesis+verdicts, two-tick driver, round-trip canary (plain testing)
ginkgo ./agent/benchrunner/replay/                                 # replay golden (git on PATH for restore fixtures)
ginkgo --focus="AgentBenchFactory" ./atc/db/                 # DB factory (needs a seeded agent_step_fixtures row)
ginkgo --focus='Legacy Database Upgrade' ./atc/db/migration/ # migration 1773106101 walk (needs 1773106100 present)
ginkgo ./atc/wrappa/ ./atc/api/accessor/                     # exhaustive-switch + roles guard
ginkgo ./go-concourse/concourse/ --focus="AgentBench"        # client
ginkgo ./fly/integration/ --focus="agent bench"              # fly (builds the binary; mock version == versions.go 0.1.0)
go build ./... ./cmd/benchstub/                              # nothing else breaks; stub binary builds
```

Per CLAUDE.md: unit tests run in parallel with `-p`; **never** pass `--race` (parallel-compilation failures). Do not lower `atc/db` template-DB timeouts. `agent/benchrunner/replay`'s `restore`-fixture tests shell out to real `git` in `TempDir`s — hermetic, no network, safe under `make test-unit` once `git` is confirmed on the CI PATH.

**Live-test requirements (theborg pattern per CLAUDE.md / MEMORY.md):**
- Task 18's live test is `//go:build live`, run only against a THROWAWAY namespace (never `cicd`/`concourse`). It is the ONLY place the stub-sidecar transport is exercised — the fake clientset's `GetLogs` returns instantly and never mounts a sidecar (SC-11 house lesson). Colima/Docker is usually down on this machine → use theborg (`kube-context theborg`).
- Enabling the runner live: deploy with `--agent-bench-runner-enabled --agent-bench-stub-image=concourse/benchstub:<ver>` and a REPLAYABLE fixture set — captures post-dating Task 6a's full-bundle pinning (A1-only v1 captures are `input-bundle-partial`; 2026-07-19 post-review) or a pinned starter set. Verify a two-field experiment via `fly agent bench run --step review --variant <name>@vN` then `fly agent bench results N` within a couple of `--agent-bench-check-interval` ticks.

**Holdout confirmation recipe (pre-promotion — minimal v1, 2026-07-19 post-review):** before promoting a winning variant, run the same experiment once against the holdout split via the FULL-FORM spec — `fly agent bench run --step review --variant <name>@vN` with the full-form body carrying `fixtures: {split: holdout}` (holdout fixtures are drawn ONLY through this explicit selector — never into default sets). Name the experiment so it reads as a "pre-promotion confirmation". C's PromotionView surfaces the latest holdout-split experiment for the versions under promotion (or "no holdout confirmation run"); richer holdout-confirmation UX is a recorded follow-up in the README residual list.

**Merge-order hazard (house spine rule — one migration per push):** land A1's `1773106100` FIRST, then A2's `1773106101`. Never push both together; ascending applied order == referential-dependency order (`cells.fixture_id` FKs `agent_step_fixtures`), so an out-of-order landing breaks the FK. B's `1773106102` lands after A2's harness.

**Rollback notes for the risky diffs:**
- **Migration `1773106101`** is additive (two new tables + four indexes); `.down.sql` drops both. No existing table is touched. If the head-const merge conflicts, the **higher** number wins.
- **The runner is off by default** (`--agent-bench-runner-enabled` absent, `--agent-bench-stub-image` empty). Shipping the code without the flags is a no-op — the safe first deploy. Capture (A1) accrues fixtures meanwhile; turn the runner on once a fixture set exists.
- **`render.go` is untouched** — the replay renderer is an additive new package. If `RenderReplay` misbehaves, the blast radius is `agent/benchrunner/replay` + the runner; production dispatch is unaffected. The `grep -rn "dispatch.Render(" agent/benchrunner/replay/` == 0 check (Task 6) is the guard that additive reuse held.
- **`contracttest` refactor (Task 5)** is behavior-preserving (the #40 contract suite guards it). If the `benchstub` delegation destabilizes the questions/SSE contract tests, fall back to the "share constants only" branch noted in the task — `benchstub` (deployable) and `contracttest` (test kit) then coexist without a shared mux.

## Scope-out (explicitly NOT in this plan)

- **Fixture capture, the fixture registry table (`agent_step_fixtures`, `1773106100`), the blob store, splits/tags/retention machinery** — A1. A2 consumes them read-only via `bench.Fixtures` + `fixtures.Bundle`. **Carve-out (amended 2026-07-19 post-review):** the OVERLAY WRITE PATH + full-bundle pinning are accepted from A1's Scope-out INTO this plan as Task 6a — `BlobStore.PutOverlay`/`ErrOverCap`, `--agent-fixture-overlay-max-bytes`, the `overlay-over-bound:<n>B` skip_reason, and the capture enrichment that flips new captures `replayable=true`.
- **`agent_bench_scores`, the score envelope production, judge-engine factoring, review precision/recall, corpus builder** — B. A2 CALLS B's `eval.ScoreCell` (import `agent/benchscore/eval`) web-side at reconcile with the cell id as a Go parameter, and reads via `bench.ScoreReader`; there is NO evaluate step and NO env-var score seam in v1 (amended 2026-07-19 post-review).
- **Two-tier scorecards / fixture-tier rollup / promotion view** — C (reads A2's results + B's scores).
- **Plan 14 M2 analytics + the retrospective-agent bench-evidence requirement** — D.
- **Gateway** — E (spec §10: zero bench coupling).
- **Web UI for the bench** — S-track (spec: API + fly first).
- **Auto-promotion / `set-live`** — human decision, out of scope (spec §7/§9).
- **The parallel-variants online best-of-N selector grammar** — declared-but-inert, owner-gated (spec decision 13); the linear grammar stays.
- **Implementor-variance plan evaluator** — a declared LATER B slice; A2's renderer already supports it (an evaluator that runs k implementors is just another evaluator workflow), but nothing here builds it.

## Open decisions (raise as amendments, do not guess silently)

1. **`fixtures.Bundle` / `SnapshotTicket` shapes** — RESOLVED (2026-07-19 post-review): A1 ships `Bundle{Repo, BaseSHA string; Overlay, TicketSnapshot, Config []byte; Env EnvPins}` + `FixtureFilter` in `agent/api/fixtures`, hydrated to bytes by `Store.Bundle(id)`; the snapshot→`tickets.Ticket` adapter (`SnapshotTicket`/`AsTicket`) stays A2-side in `agent/benchrunner/replay` (Task 6).
2. **Overlay mount vs inline** (skeleton open Q2) — RESOLVED (2026-07-19 post-review): the cap (32 MiB) is 64× the render bound (`maxSkillsRenderBytes = 512KiB`) — resolved via the store-dir mount fallback (Task 6a): overlays ≤ the render bound inline as base64; larger overlays are read by the `restore` step from the fixture store dir mounted into the replay pod (A1's Q2 explicitly promises mountability).
3. **Score tie tolerance** (Task 11) — `ControlVerdict`'s `tol` for "baseline-clone ties baseline" is per-evaluator; v1 uses a single `--agent-bench-control-tolerance` (default e.g. 5% of the primary metric). B's evaluators may want per-metric tolerances — revisit when B's score envelope pins tolerance semantics.
4. **Primary metric per step kind** (Task 11/13) — FROZEN (2026-07-19 post-review, second amendment): `primaryMetric = {review: "precision", plan: "grounding", implement: none in v1, workflow: none}`. Implement scoring (gates+judge — pod-side) is wholly deferred to the judge-in-pod slice; when it lands, `implement→"judge_total"` is the interim pin and the composite scalar is the ONE remaining A2↔B joint decision; per-metric tie-tolerance semantics for the control verdict are likewise jointly owned with B (see Open decision 3; B Task 1's addendum cross-refs both).
5. **Ledger source attribution** — §1.12.3 pins that step-cell spend lands in the existing ledger as the already-legal `source:'agent_step'` (the ledger `source` CHECK forbids a `'bench'` value, `1773106021:10-11`), attributed to the experiment via the `agent_bench_cells.pipeline_run_id → run → agent_run_metrics` join (no env var — amended 2026-07-19 post-review). A DISTINCT bench ledger source (a nicer report label) is a FUTURE budget-owned CHECK+writer extension (a separate migration, not an A2 write) — coordinate with budget/B if wanted. A2's envelope enforcement (Task 8) reads `agent_run_metrics` costs and does NOT depend on the `source` label, so nothing here blocks on it.
6. **Bench naming through S-8** (skeleton open Q11) — all `agent_bench_*` / `/bench/` / `fly agent bench` names are provisional pending the S-8 naming-spine review. A rename is a coordinated pre-freeze amendment across Tasks 1/2/3/15/16/17; do not freeze fly verbs before S-8.
7. **Gateway spend in replay** (added 2026-07-19 post-review; Task 5) — gateway-mounting variants' provider spend in replay appears ONLY in the stub's absorbed-writes log (`submit_cost` records), NOT the ledger, until a forwarding/credential design lands (forwarding would put a real credential inside the isolation boundary — Task 5 deliberately absorbs, never forwards). Coordinate with plan 10 + budget before trusting cost metrics on gateway-mounting experiments. E's Open decision 3 cross-refs here ("owned by A2 Task 5 + Open decision 7").
8. **`contracttest` delegation vs constant-sharing** (Task 5) — prefer delegating `NewStubATC`'s mux to `benchstub.NewMux` (single source of truth), but if re-enabling `ask_human` for the contract suite proves invasive, keep the two implementations separate and share only `StubToken` + the snapshot shape. Decide during Task 5 based on how cleanly the questions/SSE routes layer on top.
