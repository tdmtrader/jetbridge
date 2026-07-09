# Process Intelligence & Experiments Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the platform's improvement loop — store benchmark cases, run opt-in experiments (batches of pipeline runs across workflow-definition variants under the daily cap) producing scorecard deltas, mine findings/calibration/friction from the tables prior waves fill, and ship a retrospective `agent:` workflow that files `origin:retrospective` improvement tickets into the same human-merged queue.

**Architecture:** Two internally-sequenced, separately-shippable milestones. **M1 (experiments):** an `agent/experiments` package + `atc/db.NewAgentExperimentFactory` over migrations `1773106100–102`, plus an `experiment_runner` RunnableComponent (polling + notify, never notify-only — fork lesson) that claims `pending` experiments and materializes each matrix cell as an `origin:'fly'` ticket queued through `tickets.Store.Transition` — the wave-4 dispatcher then renders and runs it, so experiments and normal dispatch share one render+run path (no second renderer entrypoint). Cell queueing is admitted against `budget.Checker.GlobalDailyRemaining()`. **M2 (intelligence):** an `agent/intel` read-only analytics library over the already-populated `agent_reviews`/`agent_feedback`/`agent_run_metrics`/`agent_cost_ledger`/`agent_outcomes` tables (finding analytics, calibration, friction signatures), a defect→ticket linking convention, and a rendered retrospective workflow definition dispatched through the same renderer+pipeline-runs path M1 uses. Every write to a ticket goes through `tickets.Store.Transition` (single-writer discipline); every read is a query, never a materialized rollup.

**Tech Stack:** Go (atc, agent/*, go-concourse, fly, ci-agent extraction skill port), PostgreSQL migrations + squirrel (`agent_reviews_factory.go` recipe), Ginkgo/Gomega + counterfeiter fakes (atc/db, atc/wrappa, atc/experiments component), plain `testing` (`agent/api/*`, `agent/intel` matching `agent/api/reviews`), jessevdk/go-flags (fly), Elm 0.19 + elm-test (web/elm), the `pipeline_run_lifecycler` RunnableComponent recipe (`atc/runlifecycle/lifecycler.go`), the workflow-definition YAML grammar (`agent/workflow`).

---

## Context

**Charter (workstreams.json id `process-intel-experiments`, size L, wave 5, depends_on: scorecards, delivery-outcomes, dispatch, ticket-core).** The deliberately-widest terminal workstream, kept single to respect the 14-track cap, with two internally-sequenced milestones split into forge tracks at execution time if needed.

**Scope-in → task mapping (every item maps):**

| scope_in item | Tasks |
|---|---|
| **M1** benchmark case storage decision (DB, resolves open item 6) + `agent_benchmark_cases`/`agent_experiments`/`agent_experiment_runs` migrations | 2 (migrations), 3 (types), 4 (factory) |
| **M1** extraction-skill port mining `{ticket prompt, beforeRef, referenceRef}` from team repos | 5 (benchmarks API), 6 (fly `agent benchmarks`), 7 (extraction skill port) |
| **M1** experiment runner: batch of pipeline runs across definition variants via dispatch's renderer + pipeline-runs API, under the global daily cap, producing a scorecard delta view | 8 (experiments API), 9 (runner component + budget admission; batches run via ticket-dispatch so dispatch's renderer + pipeline-runs API produce the runs — §1.12.2), 10 (scorecard delta view), 11 (fly `agent experiments`) |
| **M2** finding analytics (findings per repo & workflow version; recurring classes as automation candidates; catches-migrate-leftward metric) | 13 (analytics lib: findings), 16 (analytics API), 17 (Elm views) |
| **M2** calibration (FP rate from six-verdict feedback; missed-issue rate via a lightweight defect→ticket linking convention agreed before data accrues) | 12 (defect-link convention addendum + migration), 14 (analytics lib: calibration), 16, 17 |
| **M2** friction mining limited to 2-3 high-signal flight-recorder signatures (failing-test loops, tool-error rates) | 15 (analytics lib: friction), 16, 17 |
| **M2** retrospective workflow v1 (open item 9): manual-triggered then recurring `agent:` step run reading findings/verdicts/friction/outcomes, filing template-shaped `origin:retrospective` tickets, funded per platform-credential policy | 18 (retrospective workflow YAML + intel-context materializer), 19 (retrospective trigger component + cron flag), 20 (fly `agent retrospective run`) |
| Analytics API + minimal Elm views | 16, 17 |
| Wave-start agreements (benchmark/experiment schema + defect-link convention + daily-cap reliance) recorded in the contracts doc before parallel divergence | 1 (addendum), 12 (defect-link addendum) |

**Scope OUT (do NOT implement):** auto-applying process changes (proposals are tickets; main stays human-gated — the retrospective only *files* tickets, never merges); new metric ingestion pipelines (M2 reads existing tables only — no new `agent_run_metrics`/`agent_reviews`/`agent_cost_ledger`/`agent_outcomes` write paths); promotion gates (never — scorecards/deltas inform, humans decide).

**Prior waves (assumed LANDED exactly as 00-shared-contracts.md + earlier plans' §11 addenda define):**

- **scorecards** (wave 4): the `scorecard-rollup-api` surface — route `GetAgentWorkflowScorecard GET /api/v1/agent/workflows/:workflow_name/scorecard?versions=3,4` (authorized viewer), returning per-version rollups over `agent_run_metrics`/`agent_cost_ledger`/`agent_outcomes`/judge scores/gate results keyed by workflow-definition version (gate pass rate, cost per ticket, turns, findings per ticket, judge scores, human verdict distributions, with counts alongside rates), indexed by `(workflow_version, day)`, ok/failed/error enforced. Its only Store method is `Scorecard(workflowName string, versions []int) (*Scorecard, error)` — keyed by `(workflow_name, workflow_version)`, **no ticket-set filter**. This plan's experiment delta view (Task 10) needs a rollup restricted to one experiment's specific tickets (a strict subset of a version's traffic), which that surface cannot express; rather than grow a new scorecards method it reuses the *scorecard SQL recipe* (aggregate column list + `FILTER`/`LEFT JOIN agent_outcomes`) in an experiment-owned `ScorecardForTickets` query over the same shared tables filtered by `ticket_id`. It does NOT depend on the scorecards package at runtime.
- **delivery-outcomes** (wave 4): `agent_outcomes` table (migration 1773106090) + `agent/api/outcomes` (`Outcome`, `MergeState` constants `MergeOpen`/`Merged`/`MergedWithFixes`/`ClosedUnmerged`, `Store`, additive `BaseSha`/`CreatedAt`/`UpdatedAt`); `atc/db.NewAgentOutcomesFactory`; the `agent/gitcheck` package + `agent/outcomewatcher` RunnableComponent (component name `agent_outcome_watcher`); the §1.11.1 addendum (human-touch delta = LINES via numstat of non-`concourse-agent[bot]` first-parent commits; `merged_with_fixes ⇔ human_commit_count > 0`; merge-detection heuristics v1); routes `GetAgentTicketOutcome`, `SetAgentTicketDisposition`, `GetAgentTicketDiff`, `GetAgentTicketReviews`. This plan reads `agent_outcomes` via LEFT JOIN on `ticket_id`.
- **dispatch** (wave 4): the `renderer-library-and-golden-templates` surface — a renderer library resolving a workflow-definition version into a `template: true` pipeline config with fully-resolved `agent:`/`harvest:` step config (render-time-resolution rule, contracts §2.8), sidecar mix, gate policy, checkpoint declarations, materialized `spec.md`/`plan.md` run inputs; golden-file-validated against `atc configvalidate`; plus the dispatcher RunnableComponent that claims `queued` tickets, admits against `budget`, attaches vaulted credentials, creates the run, and transitions the ticket through `tickets.Store.Transition`. This plan's experiment runner reuses dispatch's *dispatch-a-ticket* path: it creates `origin:retrospective`/experiment tickets in the appropriate state and lets the dispatcher render+run them (Task 9 chooses the ticket-dispatch reuse over calling the renderer directly — see Task 1 addendum). Its exported surface consumed here: `atc/db.NewAgentTicketsFactory` (ticket-core) + the dispatcher claiming `queued` tickets; no direct call into the renderer library from this workstream.
- **ticket-core** (wave 2): `agent/api/tickets` — `Ticket` (fields incl. `Repo`, `WorkflowName`, `WorkflowVersion *int`, `Origin`, `State`, `BudgetUSD *float64`, `CreatedBy`, `ExternalRef`), `State` constants (`StateDraft`, `StateQueued`, …), `Origin` values `web/fly/jira/retrospective`, `ListFilter{State, Repo, Origin, Limit}`, `TransitionMeta{PipelineRunID *int, Branch string, ErrorDetail string}` (**no `By` field** — frozen by ticket-core addendum §2.1.1; attribution is carried by `Ticket.CreatedBy`, set at `Create` time), `Store` (with `Transition` as the ONLY state writer, `Create` always inserts `state='draft'`), `MemoryStore`, `CreateRequest`, errors, `ValidOrigin`; `atc/db.NewAgentTicketsFactory(dbConn)` (dbfakes `FakeAgentTicketsFactory`). **CreateAgentTicket origin rule (ticket-core addendum §2.1):** principal writes may create only `origin:"retrospective"`; the retrospective agent's platform-mcp principal creates its proposal tickets through the existing `CreateAgentTicket` route.
- **credentials-and-budgets** (wave 1): `agent/budget` (`Checker` with `GlobalDailyRemaining() (Remaining, error)`, `TicketRemaining`, `Record(LedgerEntry)`; `Remaining{LimitUSD, SpentUSD, RemainingUSD, Exhausted}`; `Ledger.Rollup(groupBy, since, until)`; `GroupByWorkflow`/`GroupByUser`/`GroupByTicket`/`GroupByDay`; `RollupRow{Key, Entries, InputTokens, OutputTokens, Turns, CostUSD}`); `agent_cost_ledger` with `source` CHECK including `'retrospective'`; the platform-credential policy §1.13 (`agent-platform` service user funds `source IN ('harvest_judge','retrospective','probe')`; global daily cap includes platform spend) + the long-lived `agent-platform-credential` K8s secret (§8.2); the `--agent-daily-budget-usd` web flag (0 = unlimited). The retrospective workflow is funded by the platform credential; the experiment runner's admission uses `GlobalDailyRemaining()`.
- **workflow-store** (wave 1): `agent/workflow` — `Definition{ID, Name, Version, ContentHash, Live, RawYAML, Config}`, `Config` (parsed §6 YAML: `SchemaVersion`, `Name`, `Defaults`, `Budget`, `Sidecars`, `Prompts`, `Steps[]` with `Agent`/`Checkpoint`/`Prompt`/`Sidecars`/`BudgetSliceUSD`/`Inputs`/`Outputs`, `HITL`, `GatePolicy`, `Judge`), `Parse([]byte) (Config, error)`, `Store` (`Import`, `Get`, `Live`, `List`, `Versions`, `Promote`); `atc/db.NewAgentWorkflowsFactory`. The retrospective workflow (Task 18) is imported through this store like any other definition.
- **agent-step** (wave 2): `agent/schema` nested module (`schema.RunMetrics` with `Results json.RawMessage`, `EventCounts map[string]int`, `WorkflowName`, `WorkflowVersion *int`, `Status`; `schema.Results` with `Metadata map[string]interface{}`; event reader/writer + event-type constants incl. `tool.result`, `gate.result`); `agent/api/metrics.Store` with `ListByTicket(ticketID) ([]schema.RunMetrics, error)` + `db.NewAgentRunMetricsFactory`. Friction mining (Task 15) reads `agent_run_metrics.event_counts` and the events artifact handle.
- **harvest-step** (wave 3): `agent_reviews`/`agent_feedback` ticket linkage columns (migration 1773106080) with `reviews.StoredReview.TicketID *int`/`PipelineRunID *int` and `reviews.Store.ListByTicket(ticketID) ([]StoredReview, error)`; judge cited issues land in `agent_reviews.review` `observations` with `category: "judge"`, gate failures as `proven_issues` with `category: "gate"`, feedback on judge findings submitted with `finding_type: "judge"` (contracts §6.4.1). Finding analytics (Task 13) reads these.
- **agent-identity** (wave 1): `CheckAgentAuthorizationHandler(handler, rejector)` wrappa case group giving team-less `/api/v1/agent/*` routes real main-team viewer/member authorization (contracts decision 21); `accessor.GetAccessor(r).Claims().UserName` resolves the acting human; scope constants live in `agent/api/principals`.

**Wave-mates (parallel, none — this is wave 5, terminal).** No shared-file merge hazards beyond the additive `atc/routes.go`/wrappa/roles/`atc/atccmd/command.go`/`atc/component.go`/`legacy_upgrade_test.go` edits every agent workstream makes; migration block `1773106100–109` is this workstream's alone.

**This plan PRODUCES (contract surfaces `experiment-substrate` §1.12 — this plan is its owner):**
- 00-shared-contracts.md **§1.12 Experiment substrate** — `agent_benchmark_cases`, `agent_experiments`, `agent_experiment_runs` DDL as written, plus the Task 1 additive `defect_link` column on `agent_reviews` (§1.12.1 addendum, Task 12).
- 00-shared-contracts.md **§4.2** rows `ListAgentBenchmarkCases`/`CreateAgentBenchmarkCase`, `CreateAgentExperiment`/`GetAgentExperiment` (already in the table) plus additive rows `ListAgentExperiments`, `GetAgentExperimentDelta`, the analytics routes `GetAgentFindingAnalytics`/`GetAgentCalibration`/`GetAgentFriction`, and the retrospective-trigger route `RunAgentRetrospective` (POST `/api/v1/agent/retrospective`, member) declared in Task 1's addendum.
- The defect→ticket linking convention (Task 1/12 addendum §1.12.1) that missed-issue-rate depends on, agreed before data accrues.

**This plan CONSUMES:**
- **`scorecard-rollup-api`** (§4.2 `GetAgentWorkflowScorecard`, over §1.8/§1.4/§1.11) — as a *recipe* only: the experiment delta view (Task 10) reuses scorecards' aggregate-SQL shape and numeric field names in its own ticket-scoped `ScorecardForTickets` query. It does NOT call the scorecards route or import the scorecards package (that surface has no ticket-set filter — see the scorecards prior-wave note above).
- **`renderer-library-and-golden-templates`** + **`pipeline-runs-api-and-lifecycle`** (§1.5/§2.3/§7) — via ticket dispatch: the runner enqueues tickets the dispatcher renders and runs.
- **`ticket-tables-and-transition-function`** (§1.7/§2.1) — `origin:retrospective` seam, `Transition`, `CreateAgentTicket`.
- **`budget-library-and-cost-ledger`** + **`platform-credential-policy`** (§1.4/§2.7/§1.13) — daily-cap admission + retrospective funding.
- **`agent-outcomes-schema`** (§1.11/§2.5) — merge state, human-touch delta for finding-analytics correlation and the catches-migrate-leftward metric.
- **`events-results-shared-schema-and-run-metrics`** (§1.8/§2.4/§5) — `event_counts` + results metadata for friction mining.
- **`harvest-terminal-step-schema-and-gate-policy`** + **`judge-rubric-and-verdict-mapping`** (§1.10/§6.4) — findings (`category: "judge"`/`"gate"`) + six-verdict feedback for finding analytics and calibration.
- **`workflow-definition-schema-and-hash`** (§1.6/§2.2/§6) — the retrospective workflow is a definition.
- **§4.1/§4.2 + decision 21** (agent-identity) — `CheckAgentAuthorizationHandler` tier for all new routes.

**Anchor caveat:** `Modify:` line anchors were verified on branch `jetbridge` at head `fb1c54fac2` (pre-wave-1). Four prior waves shift every anchor in `atc/routes.go`, `atc/wrappa/api_auth_wrappa.go`, `atc/api/accessor/roles.go`, `atc/api/handler.go`, `atc/atccmd/command.go`, `atc/component.go`, and `atc/db/migration/legacy_upgrade_test.go` — treat anchors as "the location of the quoted code", and place additions adjacent to the wave-4 agent additions named in each step (e.g. after the `GetAgentWorkflowScorecard`/`GetAgentTicketOutcome` rows).

---

### Task 1: Wave-start contract addendum — experiment/benchmark schema freeze, analytics routes, dispatch-reuse decision

The charter requires the benchmark/experiment schema "as needed" and the daily-cap reliance to be explicit rather than silently assumed. This workstream owns §1.12, so this addendum is an owner amendment: it pins the API request/response shapes the contracts doc names but never bodies, records the experiment-runner-dispatches-tickets decision (avoiding a second renderer entrypoint), and declares the five additive routes (two experiment, three analytics, and the M2 retrospective trigger). Task 12 adds the defect-link column addendum separately (it touches `agent_reviews`, harvest-step's table, so it is a distinct sign-off note).

**Files:**
- Modify: `docs/superpowers/plans/agentic-platform/00-shared-contracts.md` (insert `### 1.12.2` after the §1.12 closing paragraph — the `[DECIDED HERE: benchmark cases live in the DB…]` line region, before `### 1.13`; append five rows to the §4.2 route table after the `GetAgentExperiment` row; append to the §11 Amendment log at end of file)

**Steps:**

- [ ] **Step 1: Insert the §1.12.2 addendum** immediately after §1.12's `agent_experiment_runs` block and the "[DECIDED HERE: benchmark cases live in the DB…]" paragraph (before `### 1.13`):

````markdown
### 1.12.2 Experiment substrate wave-5 addendum — owner: **process-intel-experiments** (2026-07-08; affects/notified: scorecards — recipe reuse only, no change to the scorecards surface)

**Runner dispatches tickets, not the renderer directly [DECIDED HERE]:** the experiment runner does NOT call dispatch's renderer library or `PipelineRunFactory.CreateRun` directly. For each `(benchmark_case, workflow{name,version}, repetition)` matrix cell it creates an ordinary `agent_tickets` row (`origin:'fly'`, `repo`=case.repo, `workflow_name`/`workflow_version` = the variant, `body` = case.prompt, `created_by`='experiment-<id>'), sets `agent_experiment_runs.ticket_id`, and transitions the ticket `draft→queued` through `tickets.Store.Transition`. The wave-4 dispatcher then renders and runs it exactly like any queued ticket, so experiments and normal dispatch share one render+admit+run+transition path (no second entrypoint to keep in sync). The runner links `agent_experiment_runs.pipeline_run_id` from the ticket's `pipeline_run_id` once the dispatcher sets it, and mirrors the ticket's terminal state into `agent_experiment_runs.status` (`merged`/`merged_with_fixes`/`needs_review`→`ok`, `failed`→`failed`, `errored`→`error`). `agent_experiment_runs` gains one additive column: `ticket_id INTEGER` (join key `agent_tickets.id`, per the plain-column convention).

**Daily-cap admission [DECIDED HERE]:** before queueing each matrix cell the runner calls `budget.Checker.GlobalDailyRemaining()`; when `Exhausted` it stops queueing further cells that tick and leaves the experiment `running` with un-queued cells `pending` (never `error`) — the cap is load-bearing for experiment batches (charter risk). It resumes next tick. An experiment completes when every cell's `agent_experiment_runs.status` is terminal (`ok`/`failed`/`error`).

**API request/response shapes (the contracts doc names these routes but not bodies):**
- `POST /api/v1/agent/benchmarks` body: `{"name","repo","prompt","before_ref","reference_ref","tags":[],"notes"}` → 200 `BenchmarkCase` (§2.9), 400 on missing name/repo/prompt/before_ref/reference_ref, 409 on duplicate name.
- `GET /api/v1/agent/benchmarks` (`?repo=&tag=`) → 200 `[]BenchmarkCase`.
- `POST /api/v1/agent/experiments` body: `{"name","description","matrix":{"cases":["name"...],"workflows":[{"name","version"}...],"repetitions":N}}` → 200 `Experiment` (§2.9), 400 on empty cases/workflows or unknown case name or unknown workflow version, 202 semantics: row is created `pending`; the runner picks it up.
- `GET /api/v1/agent/experiments` → 200 `[]Experiment` (metadata, newest first).
- `GET /api/v1/agent/experiments/:experiment_id` → 200 `Experiment` incl. `runs:[]ExperimentRun`, 404 unknown, 400 non-integer id.
- `GET /api/v1/agent/experiments/:experiment_id/delta` (`?baseline=<workflow_version>`) → 200 `ExperimentDelta` (§2.9): per-workflow-version scorecard-shaped columns for the experiment's variants side-by-side, computed by an experiment-owned ticket-scoped rollup (`experiments.Store.ScorecardForTickets`) over `agent_run_metrics`/`agent_cost_ledger`/`agent_outcomes` filtered to each variant's tickets, plus per-column deltas vs the baseline version; 404 unknown experiment. **Note:** the wave-4 `scorecard-rollup-api` (`GetAgentWorkflowScorecard`, keyed by `(workflow_name, workflow_version)`) has no ticket-set filter, so this workstream computes the ticket-restricted rollup itself over the shared tables rather than calling that route — no new scorecards surface is required, and the numeric field names match so the delta columns are labeled consistently.

**Go domain types (§2.9, owner: process-intel-experiments, `agent/api/experiments/types.go`):** `BenchmarkCase{ID, Name, Repo, Prompt, BeforeRef, ReferenceRef, Tags []string, Notes, CreatedBy, CreatedAt int64}`, `Experiment{ID, Name, Description, Matrix, Status, CreatedBy, CreatedAt, CompletedAt int64, Runs []ExperimentRun}`, `Matrix{Cases []string, Workflows []WorkflowRef, Repetitions int}` (`WorkflowRef{Name string, Version int}`), `ExperimentRun{BenchmarkCaseName, WorkflowName, WorkflowVersion, Repetition int, TicketID *int, PipelineRunID *int, Status string}`, `ExperimentDelta{ExperimentID, Baseline int, Columns []DeltaColumn}` (`DeltaColumn{WorkflowVersion int, Scorecard json.RawMessage, Deltas map[string]float64}`). `//counterfeiter:generate . Store` interface `CreateCase`/`ListCases`/`GetCaseByName`/`CreateExperiment`/`GetExperiment`/`ListExperiments`/`ClaimPending`/`LinkRun`/`SetRunStatus`/`FinishExperiment`/`ScorecardForTickets` (`ScorecardForTickets(workflowName string, version int, ticketIDs []int) (json.RawMessage, map[string]float64, error)` — the ticket-scoped rollup backing the delta view; the scorecards package has no ticket-set-filtered surface). `atc/db.NewAgentExperimentFactory(conn DbConn)` implements it.

**Analytics routes (M2, additive to §4.2):**
- `GET /api/v1/agent/analytics/findings` (`?repo=&workflow_name=&since=&until=`) → `FindingAnalytics` (§2.10): findings-per-ticket per repo/workflow version, recurring finding categories ranked as automation candidates, catches-migrate-leftward series (findings-per-ticket over time vs escaped-defect count). Authorized viewer.
- `GET /api/v1/agent/analytics/calibration` (`?workflow_name=&since=&until=`) → `Calibration` (§2.10): false-positive rate from six-verdict feedback (`false_positive`+`noisy`+`overly_strict` over evaluated findings), missed-issue rate from `agent_reviews.defect_link` (§1.12.1). Authorized viewer.
- `GET /api/v1/agent/analytics/friction` (`?workflow_name=&since=&until=`) → `Friction` (§2.10): the 2–3 frozen signatures (failing-test-loop rate, tool-error rate, turn-burn outliers). Authorized viewer.

**Retrospective trigger route (M2, additive to §4.2):**
- `POST /api/v1/agent/retrospective` (`RunAgentRetrospective`) → 200 `{"filed": true}` on success. Synchronously invokes the retrospective trigger's `RunOnce(ctx, "manual")`, which reads findings/calibration/friction, renders an intel snapshot, and files one `origin:'retrospective'` ticket through `CreateAgentTicket` + `Transition` (draft→queued). Authorized member (initiates platform-funded work; funded by the platform credential per §1.13). No new write path — the only mutation is the ticket, via the existing Transition seam.
````

- [ ] **Step 2: Append five route rows** to the §4.2 table (after the `CreateAgentExperiment / GetAgentExperiment` row):

```markdown
| `ListAgentExperiments` | GET | `/api/v1/agent/experiments` | authorized viewer | process-intel-experiments |
| `GetAgentExperimentDelta` | GET | `/api/v1/agent/experiments/:experiment_id/delta` | authorized viewer | process-intel-experiments |
| `GetAgentFindingAnalytics` / `GetAgentCalibration` / `GetAgentFriction` | GET | `/api/v1/agent/analytics/{findings,calibration,friction}` | authorized viewer | process-intel-experiments |
| `RunAgentRetrospective` | POST | `/api/v1/agent/retrospective` | authorized member | process-intel-experiments |
```

`RunAgentRetrospective` is the M2 manual-trigger route (Task 20): it synchronously invokes the retrospective trigger's `RunOnce(ctx, "manual")`, which files an `origin:retrospective` ticket. Member tier (it initiates platform-funded work), on the `CheckAgentAuthorizationHandler` group like every other `/api/v1/agent/*` route (decision 21).

- [ ] **Step 3: Append to the §11 Amendment log:**

```markdown
- 2026-07-08 (process-intel-experiments wave-5 planning; owner of §1.12; affects/notified: scorecards — recipe reuse only, no change to the scorecards surface):
  added §1.12.2 — experiment-runner-dispatches-tickets decision (no second renderer entrypoint; matrix cells become origin:'fly' tickets queued through Transition; agent_experiment_runs gains additive ticket_id), daily-cap admission via budget.GlobalDailyRemaining() (over-cap cells stay pending, never error), the benchmark/experiment/analytics HTTP request-response shapes, §2.9 experiment Go types (agent/api/experiments) — incl. the experiment-owned `experiments.Store.ScorecardForTickets(workflowName, version, ticketIDs)` ticket-scoped rollup that backs the delta view (the wave-4 scorecard-rollup-api has no ticket-set filter, so the delta reuses its SQL recipe rather than its route/package — NO change to the scorecards surface is required or made), §2.10 analytics types (agent/intel), and five additive §4.2 routes: ListAgentExperiments, GetAgentExperimentDelta, GetAgentFindingAnalytics/Calibration/Friction (all authorized viewer), and RunAgentRetrospective (POST /api/v1/agent/retrospective, authorized member — the M2 manual retrospective trigger that files an origin:retrospective ticket). Reads-only over prior-wave tables; no new ingestion (RunAgentRetrospective writes only a ticket via the existing Transition seam).
```

- [ ] **Step 4: Verify the entry landed:** `grep -n "experiment-runner-dispatches-tickets\|§1.12.2\|RunAgentRetrospective" docs/superpowers/plans/agentic-platform/00-shared-contracts.md` — expect §1.12.2 in the §1.12 region and §11 log, and `RunAgentRetrospective` in both the §1.12.2 addendum and the §4.2 route table (five new rows total).

- [ ] **Step 5: Commit:**

```bash
git add docs/superpowers/plans/agentic-platform/00-shared-contracts.md
git commit -m "docs(process-intel): wave-5 addendum - experiment substrate shapes, dispatch reuse, analytics routes" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Milestone 1 — Experiments & benchmarks

### Task 2: Migrations 1773106100–102 — experiment substrate tables

**Files:**
- Create: `atc/db/migration/migrations/1773106100_create_agent_benchmark_cases.up.sql`
- Create: `atc/db/migration/migrations/1773106100_create_agent_benchmark_cases.down.sql`
- Create: `atc/db/migration/migrations/1773106101_create_agent_experiments.up.sql`
- Create: `atc/db/migration/migrations/1773106101_create_agent_experiments.down.sql`
- Create: `atc/db/migration/migrations/1773106102_create_agent_experiment_runs.up.sql`
- Create: `atc/db/migration/migrations/1773106102_create_agent_experiment_runs.down.sql`
- Modify: `atc/db/migration/legacy_upgrade_test.go:37` (`jetbridgeHeadMigration` const)

Migration files are picked up automatically via `//go:embed migrations` in `atc/db/migration/migration.go:153` — no registration code.

- [ ] **Step 1: Write `1773106100_create_agent_benchmark_cases.up.sql`** (contracts §1.12 DDL verbatim):

```sql
CREATE TABLE agent_benchmark_cases (
    id            SERIAL PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE,
    repo          TEXT NOT NULL,
    prompt        TEXT NOT NULL,
    before_ref    TEXT NOT NULL,
    reference_ref TEXT NOT NULL,
    tags          TEXT[] NOT NULL DEFAULT '{}',
    notes         TEXT NOT NULL DEFAULT '',
    created_by    TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

- [ ] **Step 2: Write `1773106100_create_agent_benchmark_cases.down.sql`:**

```sql
DROP TABLE agent_benchmark_cases;
```

- [ ] **Step 3: Write `1773106101_create_agent_experiments.up.sql`** (contracts §1.12 DDL verbatim):

```sql
CREATE TABLE agent_experiments (
    id           SERIAL PRIMARY KEY,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    matrix       JSONB NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending','running','complete','error')),
    created_by   TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);
```

- [ ] **Step 4: Write `1773106101_create_agent_experiments.down.sql`:**

```sql
DROP TABLE agent_experiments;
```

- [ ] **Step 5: Write `1773106102_create_agent_experiment_runs.up.sql`** (contracts §1.12 DDL + the §1.12.2 additive `ticket_id` column):

```sql
CREATE TABLE agent_experiment_runs (
    id                SERIAL PRIMARY KEY,
    experiment_id     INTEGER NOT NULL REFERENCES agent_experiments (id) ON DELETE CASCADE,
    benchmark_case_id INTEGER NOT NULL REFERENCES agent_benchmark_cases (id) ON DELETE CASCADE,
    workflow_name     TEXT NOT NULL,
    workflow_version  INTEGER NOT NULL,
    repetition        INTEGER NOT NULL DEFAULT 1,
    ticket_id         INTEGER,                 -- join key agent_tickets.id (§1.12.2 addendum)
    pipeline_run_id   INTEGER,                 -- join key pipeline_runs.id
    status            TEXT NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending','running','ok','failed','error')),
    UNIQUE (experiment_id, benchmark_case_id, workflow_name, workflow_version, repetition)
);

CREATE INDEX agent_experiment_runs_experiment ON agent_experiment_runs (experiment_id);
CREATE INDEX agent_experiment_runs_open ON agent_experiment_runs (experiment_id) WHERE status = 'pending';
```

- [ ] **Step 6: Write `1773106102_create_agent_experiment_runs.down.sql`:**

```sql
DROP TABLE agent_experiment_runs;
```

- [ ] **Step 7: Bump the head-migration const** (`atc/db/migration/legacy_upgrade_test.go:37`) to the highest number this workstream lands — but only if it is currently lower (later plans may already have set it higher; keep the larger value):

```go
const jetbridgeHeadMigration = 1773106102
```

- [ ] **Step 8: Verify migrations apply cleanly** against a scratch DB:

```bash
go test ./atc/db/migration/ -run TestMigration -count=1
```
Expected: PASS (the embedded-migration up/down round-trip test exercises the new files).

- [ ] **Step 9: Commit:**

```bash
git add atc/db/migration/migrations/1773106100_* atc/db/migration/migrations/1773106101_* atc/db/migration/migrations/1773106102_* atc/db/migration/legacy_upgrade_test.go
git commit -m "feat(db): agent experiment substrate migrations (benchmark cases, experiments, runs)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: `agent/api/experiments` — domain types, matrix validation, MemoryStore

**Files:**
- Create: `agent/api/experiments/types.go`
- Create: `agent/api/experiments/memory_store.go`
- Test: `agent/api/experiments/types_test.go`

Plain `testing` (matching `agent/api/reviews`, no ATC deps).

- [ ] **Step 1: Write the failing test `agent/api/experiments/types_test.go`:**

```go
package experiments_test

import (
	"testing"

	"github.com/concourse/concourse/agent/api/experiments"
)

func TestValidateMatrixRejectsEmptyCasesAndWorkflows(t *testing.T) {
	if err := (experiments.Matrix{}).Validate(); err == nil {
		t.Fatal("expected error for empty matrix")
	}
	if err := (experiments.Matrix{Cases: []string{"c1"}}).Validate(); err == nil {
		t.Fatal("expected error for no workflows")
	}
	m := experiments.Matrix{
		Cases:       []string{"c1"},
		Workflows:   []experiments.WorkflowRef{{Name: "standard-dev", Version: 3}},
		Repetitions: 0,
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("valid matrix rejected: %v", err)
	}
	if m.EffectiveRepetitions() != 1 {
		t.Fatalf("EffectiveRepetitions = %d, want 1 (zero defaults to 1)", m.EffectiveRepetitions())
	}
}

func TestExpandProducesOneCellPerCombination(t *testing.T) {
	m := experiments.Matrix{
		Cases:       []string{"c1", "c2"},
		Workflows:   []experiments.WorkflowRef{{Name: "wf", Version: 1}, {Name: "wf", Version: 2}},
		Repetitions: 2,
	}
	cells := m.Expand()
	if len(cells) != 8 { // 2 cases * 2 workflows * 2 reps
		t.Fatalf("Expand = %d cells, want 8", len(cells))
	}
	if cells[0].BenchmarkCaseName != "c1" || cells[0].Repetition != 1 {
		t.Fatalf("cell[0] = %+v", cells[0])
	}
}

func TestMemoryStoreClaimPendingIsSingleWinner(t *testing.T) {
	s := experiments.NewMemoryStore()
	exp, _ := s.CreateExperiment(&experiments.Experiment{
		Name: "e1",
		Matrix: experiments.Matrix{
			Cases:     []string{"c1"},
			Workflows: []experiments.WorkflowRef{{Name: "wf", Version: 1}},
		},
	})
	claimed, ok, err := s.ClaimPending()
	if err != nil || !ok || claimed.ID != exp.ID {
		t.Fatalf("first claim: exp=%+v ok=%v err=%v", claimed, ok, err)
	}
	// After the only pending experiment is claimed (marked running), no more.
	_, ok, _ = s.ClaimPending()
	if ok {
		t.Fatal("expected no pending experiments after claim")
	}
}
```

- [ ] **Step 2: Run to see it fail:**

```bash
go test ./agent/api/experiments/
```
Expected: build failure — `package experiments` has no `Matrix`/`Experiment`/`NewMemoryStore`.

- [ ] **Step 3: Write `agent/api/experiments/types.go`** (contracts §2.9):

```go
package experiments

import (
	"encoding/json"
	"fmt"
)

// BenchmarkCase mirrors agent_benchmark_cases (§1.12).
type BenchmarkCase struct {
	ID           int      `json:"id"`
	Name         string   `json:"name"`
	Repo         string   `json:"repo"`
	Prompt       string   `json:"prompt"`
	BeforeRef    string   `json:"before_ref"`
	ReferenceRef string   `json:"reference_ref"`
	Tags         []string `json:"tags"`
	Notes        string   `json:"notes"`
	CreatedBy    string   `json:"created_by"`
	CreatedAt    int64    `json:"created_at"`
}

func (c *BenchmarkCase) Validate() error {
	switch {
	case c.Name == "":
		return fmt.Errorf("name is required")
	case c.Repo == "":
		return fmt.Errorf("repo is required")
	case c.Prompt == "":
		return fmt.Errorf("prompt is required")
	case c.BeforeRef == "":
		return fmt.Errorf("before_ref is required")
	case c.ReferenceRef == "":
		return fmt.Errorf("reference_ref is required")
	}
	return nil
}

// WorkflowRef names a specific workflow-definition version in a matrix.
type WorkflowRef struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
}

// Matrix is the experiment's cross-product spec (agent_experiments.matrix JSONB).
type Matrix struct {
	Cases       []string      `json:"cases"`
	Workflows   []WorkflowRef `json:"workflows"`
	Repetitions int           `json:"repetitions"`
}

func (m Matrix) Validate() error {
	if len(m.Cases) == 0 {
		return fmt.Errorf("matrix.cases must not be empty")
	}
	if len(m.Workflows) == 0 {
		return fmt.Errorf("matrix.workflows must not be empty")
	}
	for _, w := range m.Workflows {
		if w.Name == "" || w.Version <= 0 {
			return fmt.Errorf("matrix.workflows entries need name and version>0")
		}
	}
	return nil
}

// EffectiveRepetitions treats 0 as 1 (default one run per combination).
func (m Matrix) EffectiveRepetitions() int {
	if m.Repetitions <= 0 {
		return 1
	}
	return m.Repetitions
}

// Cell is one materialized matrix combination.
type Cell struct {
	BenchmarkCaseName string
	WorkflowName      string
	WorkflowVersion   int
	Repetition        int
}

// Expand enumerates every (case, workflow, repetition) combination in a
// stable order (case-major, then workflow, then repetition).
func (m Matrix) Expand() []Cell {
	var cells []Cell
	for _, c := range m.Cases {
		for _, w := range m.Workflows {
			for r := 1; r <= m.EffectiveRepetitions(); r++ {
				cells = append(cells, Cell{
					BenchmarkCaseName: c,
					WorkflowName:      w.Name,
					WorkflowVersion:   w.Version,
					Repetition:        r,
				})
			}
		}
	}
	return cells
}

// Status values for agent_experiments.status and agent_experiment_runs.status.
const (
	StatusPending  = "pending"
	StatusRunning  = "running"
	StatusComplete = "complete"
	StatusOK       = "ok"
	StatusFailed   = "failed"
	StatusError    = "error"
)

// ExperimentRun mirrors agent_experiment_runs (§1.12 + §1.12.2 ticket_id).
type ExperimentRun struct {
	BenchmarkCaseName string `json:"benchmark_case_name"`
	WorkflowName      string `json:"workflow_name"`
	WorkflowVersion   int    `json:"workflow_version"`
	Repetition        int    `json:"repetition"`
	TicketID          *int   `json:"ticket_id,omitempty"`
	PipelineRunID     *int   `json:"pipeline_run_id,omitempty"`
	Status            string `json:"status"`
}

// Experiment mirrors agent_experiments (§1.12).
type Experiment struct {
	ID          int             `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Matrix      Matrix          `json:"matrix"`
	Status      string          `json:"status"`
	CreatedBy   string          `json:"created_by"`
	CreatedAt   int64           `json:"created_at"`
	CompletedAt int64           `json:"completed_at,omitempty"`
	Runs        []ExperimentRun `json:"runs,omitempty"`
}

// DeltaColumn is one workflow-version's scorecard within an experiment plus
// its per-metric delta versus the baseline version.
type DeltaColumn struct {
	WorkflowVersion int                `json:"workflow_version"`
	Scorecard       json.RawMessage    `json:"scorecard"`
	Deltas          map[string]float64 `json:"deltas"`
}

// ExperimentDelta is the GetAgentExperimentDelta response.
type ExperimentDelta struct {
	ExperimentID int           `json:"experiment_id"`
	Baseline     int           `json:"baseline"`
	Columns      []DeltaColumn `json:"columns"`
}

//counterfeiter:generate . Store
type Store interface {
	CreateCase(c *BenchmarkCase) (int, error)
	ListCases(repo, tag string) ([]BenchmarkCase, error)
	GetCaseByName(name string) (*BenchmarkCase, bool, error)

	CreateExperiment(e *Experiment) (*Experiment, error)
	GetExperiment(id int) (*Experiment, bool, error)
	ListExperiments() ([]Experiment, error)

	// ClaimPending atomically marks one pending experiment 'running' and
	// returns it (with its matrix expanded into run rows on first claim).
	ClaimPending() (*Experiment, bool, error)
	// LinkRun records a queued ticket/run against one matrix cell.
	LinkRun(experimentID int, cell Cell, ticketID int) error
	// SetRunStatus mirrors the dispatched ticket's terminal state.
	SetRunStatus(experimentID int, cell Cell, pipelineRunID *int, status string) error
	// FinishExperiment marks it complete/error once all cells are terminal.
	FinishExperiment(id int, status string) error

	// ScorecardForTickets returns a scorecard-shaped rollup for one workflow
	// version restricted to a set of ticket ids (an experiment's runs), over
	// agent_run_metrics/agent_cost_ledger/agent_outcomes. Backs the delta
	// view (Task 10) — the scorecards package has no ticket-set-filtered
	// rollup, so this workstream computes it here. Returns raw JSON (for the
	// delta column) + a flat numeric map (for diffing). Empty ticketIDs
	// yields a zero rollup.
	ScorecardForTickets(workflowName string, version int, ticketIDs []int) (json.RawMessage, map[string]float64, error)
}
```

- [ ] **Step 4: Write `agent/api/experiments/memory_store.go`** (test double, mirrors `agent/api/reviews/memory_store.go`):

```go
package experiments

import (
	"encoding/json"
	"sync"
	"time"
)

type MemoryStore struct {
	mu       sync.Mutex
	cases    []BenchmarkCase
	exps     []*Experiment
	runs     map[int][]*ExperimentRun // experimentID -> cells
	nextCase int
	nextExp  int
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{runs: map[int][]*ExperimentRun{}, nextCase: 1, nextExp: 1}
}

func (s *MemoryStore) CreateCase(c *BenchmarkCase) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.ID = s.nextCase
	s.nextCase++
	c.CreatedAt = time.Now().Unix()
	s.cases = append(s.cases, *c)
	return c.ID, nil
}

func (s *MemoryStore) ListCases(repo, tag string) ([]BenchmarkCase, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []BenchmarkCase{}
	for _, c := range s.cases {
		if repo != "" && c.Repo != repo {
			continue
		}
		if tag != "" && !contains(c.Tags, tag) {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func (s *MemoryStore) GetCaseByName(name string) (*BenchmarkCase, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.cases {
		if s.cases[i].Name == name {
			c := s.cases[i]
			return &c, true, nil
		}
	}
	return nil, false, nil
}

func (s *MemoryStore) CreateExperiment(e *Experiment) (*Experiment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.ID = s.nextExp
	s.nextExp++
	e.Status = StatusPending
	e.CreatedAt = time.Now().Unix()
	s.exps = append(s.exps, e)
	return e, nil
}

func (s *MemoryStore) GetExperiment(id int) (*Experiment, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.exps {
		if e.ID == id {
			cp := *e
			for _, r := range s.runs[id] {
				cp.Runs = append(cp.Runs, *r)
			}
			return &cp, true, nil
		}
	}
	return nil, false, nil
}

func (s *MemoryStore) ListExperiments() ([]Experiment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Experiment{}
	for i := len(s.exps) - 1; i >= 0; i-- {
		out = append(out, *s.exps[i])
	}
	return out, nil
}

func (s *MemoryStore) ClaimPending() (*Experiment, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.exps {
		if e.Status == StatusPending {
			e.Status = StatusRunning
			for _, cell := range e.Matrix.Expand() {
				s.runs[e.ID] = append(s.runs[e.ID], &ExperimentRun{
					BenchmarkCaseName: cell.BenchmarkCaseName,
					WorkflowName:      cell.WorkflowName,
					WorkflowVersion:   cell.WorkflowVersion,
					Repetition:        cell.Repetition,
					Status:            StatusPending,
				})
			}
			cp := *e
			return &cp, true, nil
		}
	}
	return nil, false, nil
}

func (s *MemoryStore) LinkRun(experimentID int, cell Cell, ticketID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.find(experimentID, cell); r != nil {
		r.TicketID = &ticketID
		r.Status = StatusRunning
	}
	return nil
}

func (s *MemoryStore) SetRunStatus(experimentID int, cell Cell, pipelineRunID *int, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.find(experimentID, cell); r != nil {
		r.PipelineRunID = pipelineRunID
		r.Status = status
	}
	return nil
}

func (s *MemoryStore) FinishExperiment(id int, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.exps {
		if e.ID == id {
			e.Status = status
			e.CompletedAt = time.Now().Unix()
		}
	}
	return nil
}

// ScorecardForTickets returns a zero rollup in the memory double — the
// metrics tables it aggregates (agent_run_metrics/agent_cost_ledger/
// agent_outcomes) live only in the DB factory (Task 4). ComputeDelta's own
// unit test (Task 10) injects an explicit ScorecardFunc; the real query is
// covered by the atc/db factory test.
func (s *MemoryStore) ScorecardForTickets(workflowName string, version int, ticketIDs []int) (json.RawMessage, map[string]float64, error) {
	return json.RawMessage(`{}`), map[string]float64{}, nil
}

func (s *MemoryStore) find(experimentID int, cell Cell) *ExperimentRun {
	for _, r := range s.runs[experimentID] {
		if r.BenchmarkCaseName == cell.BenchmarkCaseName &&
			r.WorkflowName == cell.WorkflowName &&
			r.WorkflowVersion == cell.WorkflowVersion &&
			r.Repetition == cell.Repetition {
			return r
		}
	}
	return nil
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Run to green:**

```bash
go test ./agent/api/experiments/
```
Expected: PASS.

- [ ] **Step 6: Commit:**

```bash
git add agent/api/experiments/
git commit -m "feat(agent): experiment domain types, matrix expansion, memory store" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: `atc/db.NewAgentExperimentFactory` — squirrel factory implementing `experiments.Store`

**Files:**
- Create: `atc/db/agent_experiment_factory.go`
- Test: `atc/db/agent_experiment_factory_test.go`

Follows the `atc/db/agent_reviews_factory.go` recipe: `psql = sq.StatementBuilder.PlaceholderFormat(sq.Dollar)`, `ON CONFLICT` upserts, `//counterfeiter:generate . AgentExperimentFactory`. Ginkgo (the `atc/db` suite uses the shared template DB; see CLAUDE.md — never `--race`).

- [ ] **Step 1: Write the failing spec `atc/db/agent_experiment_factory_test.go`:**

```go
package db_test

import (
	"github.com/concourse/concourse/agent/api/experiments"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentExperimentFactory", func() {
	var factory db.AgentExperimentFactory

	BeforeEach(func() {
		factory = db.NewAgentExperimentFactory(dbConn)
	})

	It("claims exactly one pending experiment and expands its matrix", func() {
		caseID, err := factory.CreateCase(&experiments.BenchmarkCase{
			Name: "case-a", Repo: "tdmtrader/concourse", Prompt: "fix X",
			BeforeRef: "abc", ReferenceRef: "def",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(caseID).To(BeNumerically(">", 0))

		created, err := factory.CreateExperiment(&experiments.Experiment{
			Name: "exp-a",
			Matrix: experiments.Matrix{
				Cases:       []string{"case-a"},
				Workflows:   []experiments.WorkflowRef{{Name: "wf", Version: 1}, {Name: "wf", Version: 2}},
				Repetitions: 1,
			},
		})
		Expect(err).NotTo(HaveOccurred())

		claimed, ok, err := factory.ClaimPending()
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(claimed.ID).To(Equal(created.ID))
		Expect(claimed.Status).To(Equal(experiments.StatusRunning))

		// Second claim finds nothing (already running).
		_, ok, err = factory.ClaimPending()
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse())

		full, ok, err := factory.GetExperiment(created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(full.Runs).To(HaveLen(2)) // 1 case * 2 workflows * 1 rep
	})

	It("links a ticket to a cell and mirrors its terminal status", func() {
		_, err := factory.CreateCase(&experiments.BenchmarkCase{
			Name: "case-b", Repo: "tdmtrader/concourse", Prompt: "p",
			BeforeRef: "a", ReferenceRef: "b",
		})
		Expect(err).NotTo(HaveOccurred())
		created, err := factory.CreateExperiment(&experiments.Experiment{
			Name: "exp-b",
			Matrix: experiments.Matrix{
				Cases:     []string{"case-b"},
				Workflows: []experiments.WorkflowRef{{Name: "wf", Version: 1}},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		_, _, err = factory.ClaimPending()
		Expect(err).NotTo(HaveOccurred())

		cell := experiments.Cell{BenchmarkCaseName: "case-b", WorkflowName: "wf", WorkflowVersion: 1, Repetition: 1}
		Expect(factory.LinkRun(created.ID, cell, 42)).To(Succeed())
		runID := 99
		Expect(factory.SetRunStatus(created.ID, cell, &runID, experiments.StatusOK)).To(Succeed())

		full, _, _ := factory.GetExperiment(created.ID)
		Expect(full.Runs).To(HaveLen(1))
		Expect(*full.Runs[0].TicketID).To(Equal(42))
		Expect(*full.Runs[0].PipelineRunID).To(Equal(99))
		Expect(full.Runs[0].Status).To(Equal(experiments.StatusOK))
	})
})
```

- [ ] **Step 2: Run to see it fail:**

```bash
ginkgo --focus="AgentExperimentFactory" ./atc/db/
```
Expected: compile failure — `db.NewAgentExperimentFactory` undefined.

- [ ] **Step 3: Write `atc/db/agent_experiment_factory.go`** (squirrel + `ClaimPending` uses `FOR UPDATE SKIP LOCKED` in a tx, following the GC-collector claim idiom):

```go
package db

import (
	"database/sql"
	"encoding/json"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/agent/api/experiments"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

//counterfeiter:generate . AgentExperimentFactory
type AgentExperimentFactory interface {
	experiments.Store
}

func NewAgentExperimentFactory(conn DbConn) AgentExperimentFactory {
	return &agentExperimentFactory{conn: conn}
}

type agentExperimentFactory struct {
	conn DbConn
}

func (f *agentExperimentFactory) CreateCase(c *experiments.BenchmarkCase) (int, error) {
	tags := c.Tags
	if tags == nil {
		tags = []string{}
	}
	err := psql.Insert("agent_benchmark_cases").
		Columns("name", "repo", "prompt", "before_ref", "reference_ref", "tags", "notes", "created_by").
		Values(c.Name, c.Repo, c.Prompt, c.BeforeRef, c.ReferenceRef, pq.Array(tags), c.Notes, c.CreatedBy).
		Suffix("RETURNING id").
		RunWith(f.conn).QueryRow().Scan(&c.ID)
	return c.ID, err
}

func (f *agentExperimentFactory) ListCases(repo, tag string) ([]experiments.BenchmarkCase, error) {
	q := psql.Select("id", "name", "repo", "prompt", "before_ref", "reference_ref", "tags", "notes", "created_by",
		"EXTRACT(EPOCH FROM created_at)::bigint").
		From("agent_benchmark_cases").OrderBy("name ASC")
	if repo != "" {
		q = q.Where(sq.Eq{"repo": repo})
	}
	if tag != "" {
		q = q.Where("$1 = ANY(tags)", tag) // placeholder-index handled by squirrel Dollar via Expr below
	}
	rows, err := q.RunWith(f.conn).Query()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []experiments.BenchmarkCase{}
	for rows.Next() {
		var c experiments.BenchmarkCase
		if err := rows.Scan(&c.ID, &c.Name, &c.Repo, &c.Prompt, &c.BeforeRef, &c.ReferenceRef,
			pq.Array(&c.Tags), &c.Notes, &c.CreatedBy, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (f *agentExperimentFactory) GetCaseByName(name string) (*experiments.BenchmarkCase, bool, error) {
	var c experiments.BenchmarkCase
	err := psql.Select("id", "name", "repo", "prompt", "before_ref", "reference_ref", "tags", "notes", "created_by",
		"EXTRACT(EPOCH FROM created_at)::bigint").
		From("agent_benchmark_cases").Where(sq.Eq{"name": name}).
		RunWith(f.conn).QueryRow().
		Scan(&c.ID, &c.Name, &c.Repo, &c.Prompt, &c.BeforeRef, &c.ReferenceRef,
			pq.Array(&c.Tags), &c.Notes, &c.CreatedBy, &c.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &c, true, nil
}

func (f *agentExperimentFactory) CreateExperiment(e *experiments.Experiment) (*experiments.Experiment, error) {
	matrix, err := json.Marshal(e.Matrix)
	if err != nil {
		return nil, err
	}
	err = psql.Insert("agent_experiments").
		Columns("name", "description", "matrix", "status", "created_by").
		Values(e.Name, e.Description, matrix, experiments.StatusPending, e.CreatedBy).
		Suffix("RETURNING id, status, EXTRACT(EPOCH FROM created_at)::bigint").
		RunWith(f.conn).QueryRow().Scan(&e.ID, &e.Status, &e.CreatedAt)
	return e, err
}

func (f *agentExperimentFactory) GetExperiment(id int) (*experiments.Experiment, bool, error) {
	var e experiments.Experiment
	var matrix []byte
	var completed sql.NullInt64
	err := psql.Select("id", "name", "description", "matrix", "status", "created_by",
		"EXTRACT(EPOCH FROM created_at)::bigint", "EXTRACT(EPOCH FROM completed_at)::bigint").
		From("agent_experiments").Where(sq.Eq{"id": id}).
		RunWith(f.conn).QueryRow().
		Scan(&e.ID, &e.Name, &e.Description, &matrix, &e.Status, &e.CreatedBy, &e.CreatedAt, &completed)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err := json.Unmarshal(matrix, &e.Matrix); err != nil {
		return nil, false, err
	}
	if completed.Valid {
		e.CompletedAt = completed.Int64
	}
	runs, err := f.runsFor(id)
	if err != nil {
		return nil, false, err
	}
	e.Runs = runs
	return &e, true, nil
}

func (f *agentExperimentFactory) runsFor(experimentID int) ([]experiments.ExperimentRun, error) {
	rows, err := psql.Select("bc.name", "er.workflow_name", "er.workflow_version", "er.repetition",
		"er.ticket_id", "er.pipeline_run_id", "er.status").
		From("agent_experiment_runs er").
		Join("agent_benchmark_cases bc ON bc.id = er.benchmark_case_id").
		Where(sq.Eq{"er.experiment_id": experimentID}).
		OrderBy("er.id ASC").
		RunWith(f.conn).Query()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []experiments.ExperimentRun{}
	for rows.Next() {
		var r experiments.ExperimentRun
		var ticket, run sql.NullInt64
		if err := rows.Scan(&r.BenchmarkCaseName, &r.WorkflowName, &r.WorkflowVersion, &r.Repetition,
			&ticket, &run, &r.Status); err != nil {
			return nil, err
		}
		if ticket.Valid {
			t := int(ticket.Int64)
			r.TicketID = &t
		}
		if run.Valid {
			p := int(run.Int64)
			r.PipelineRunID = &p
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (f *agentExperimentFactory) ListExperiments() ([]experiments.Experiment, error) {
	rows, err := psql.Select("id", "name", "description", "matrix", "status", "created_by",
		"EXTRACT(EPOCH FROM created_at)::bigint").
		From("agent_experiments").OrderBy("id DESC").
		RunWith(f.conn).Query()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []experiments.Experiment{}
	for rows.Next() {
		var e experiments.Experiment
		var matrix []byte
		if err := rows.Scan(&e.ID, &e.Name, &e.Description, &matrix, &e.Status, &e.CreatedBy, &e.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(matrix, &e.Matrix)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ClaimPending marks one pending experiment 'running' under FOR UPDATE SKIP
// LOCKED (multi-web-node safe) and expands its matrix into run rows on first
// claim (idempotent: skips rows already present).
func (f *agentExperimentFactory) ClaimPending() (*experiments.Experiment, bool, error) {
	tx, err := f.conn.Begin()
	if err != nil {
		return nil, false, err
	}
	defer Rollback(tx)

	var id int
	err = psql.Select("id").From("agent_experiments").
		Where(sq.Eq{"status": experiments.StatusPending}).
		OrderBy("id ASC").Limit(1).Suffix("FOR UPDATE SKIP LOCKED").
		RunWith(tx).QueryRow().Scan(&id)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	if _, err := psql.Update("agent_experiments").
		Set("status", experiments.StatusRunning).
		Where(sq.Eq{"id": id}).RunWith(tx).Exec(); err != nil {
		return nil, false, err
	}

	// Expand matrix into run rows (once; ON CONFLICT no-op on re-claim).
	var matrix []byte
	if err := psql.Select("matrix").From("agent_experiments").Where(sq.Eq{"id": id}).
		RunWith(tx).QueryRow().Scan(&matrix); err != nil {
		return nil, false, err
	}
	var m experiments.Matrix
	if err := json.Unmarshal(matrix, &m); err != nil {
		return nil, false, err
	}
	for _, cell := range m.Expand() {
		var caseID int
		err := psql.Select("id").From("agent_benchmark_cases").
			Where(sq.Eq{"name": cell.BenchmarkCaseName}).
			RunWith(tx).QueryRow().Scan(&caseID)
		if err != nil {
			return nil, false, err
		}
		if _, err := psql.Insert("agent_experiment_runs").
			Columns("experiment_id", "benchmark_case_id", "workflow_name", "workflow_version", "repetition", "status").
			Values(id, caseID, cell.WorkflowName, cell.WorkflowVersion, cell.Repetition, experiments.StatusPending).
			Suffix("ON CONFLICT (experiment_id, benchmark_case_id, workflow_name, workflow_version, repetition) DO NOTHING").
			RunWith(tx).Exec(); err != nil {
			return nil, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	e, _, err := f.GetExperiment(id)
	return e, true, err
}

func (f *agentExperimentFactory) LinkRun(experimentID int, cell experiments.Cell, ticketID int) error {
	_, err := psql.Update("agent_experiment_runs er").
		Set("ticket_id", ticketID).
		Set("status", experiments.StatusRunning).
		From("agent_benchmark_cases bc").
		Where("bc.id = er.benchmark_case_id").
		Where(sq.Eq{
			"er.experiment_id":    experimentID,
			"bc.name":             cell.BenchmarkCaseName,
			"er.workflow_name":    cell.WorkflowName,
			"er.workflow_version": cell.WorkflowVersion,
			"er.repetition":       cell.Repetition,
		}).RunWith(f.conn).Exec()
	return err
}

func (f *agentExperimentFactory) SetRunStatus(experimentID int, cell experiments.Cell, pipelineRunID *int, status string) error {
	upd := psql.Update("agent_experiment_runs er").
		Set("status", status).
		From("agent_benchmark_cases bc").
		Where("bc.id = er.benchmark_case_id").
		Where(sq.Eq{
			"er.experiment_id":    experimentID,
			"bc.name":             cell.BenchmarkCaseName,
			"er.workflow_name":    cell.WorkflowName,
			"er.workflow_version": cell.WorkflowVersion,
			"er.repetition":       cell.Repetition,
		})
	if pipelineRunID != nil {
		upd = upd.Set("pipeline_run_id", *pipelineRunID)
	}
	_, err := upd.RunWith(f.conn).Exec()
	return err
}

func (f *agentExperimentFactory) FinishExperiment(id int, status string) error {
	_, err := psql.Update("agent_experiments").
		Set("status", status).
		Set("completed_at", sq.Expr("now()")).
		Where(sq.Eq{"id": id}).RunWith(f.conn).Exec()
	return err
}
```

Note: `psql`, `Rollback`, and `pq` (`github.com/lib/pq`) are already available in package `db` (used across `atc/db/*_factory.go`); the `ANY(tags)` filter is applied in Go post-fetch if squirrel's placeholder handling proves awkward — swap the `q.Where("$1 = ANY(tags)", tag)` line for `sq.Expr("? = ANY(tags)", tag)` which the Dollar formatter renumbers correctly.

- [ ] **Step 3b: Add `ScorecardForTickets`** (backs the delta view — Task 10; the scorecards package has no ticket-set-filtered rollup). It aggregates the same tables the wave-4 scorecards `Store.Scorecard` query does (`agent_run_metrics` volume/status split, `agent_cost_ledger` cost+turns, `agent_reviews.proven_count+observation_count` findings, LEFT JOIN `agent_outcomes` for merge/human-touch), but for **one** `version` and filtered to a ticket set, and returns both the raw JSON and a flat numeric map. Follow the scorecards aggregate SQL recipe (plan 13, Task 3/4/5 — `FILTER (WHERE …)`, `LEFT JOIN agent_outcomes`, dark outcome columns when the table is absent) and reuse its numeric field names so delta columns match the standalone scorecard page:

```go
func (f *agentExperimentFactory) ScorecardForTickets(workflowName string, version int, ticketIDs []int) (json.RawMessage, map[string]float64, error) {
	if len(ticketIDs) == 0 {
		raw := json.RawMessage(`{}`)
		return raw, map[string]float64{}, nil
	}
	// One row of aggregate columns over agent_run_metrics / agent_cost_ledger
	// / agent_outcomes, restricted to workflow (name,version) AND
	// ticket_id = ANY($ticketIDs). Column list mirrors scorecards.Scorecard's
	// numeric fields (cost_usd, turns, gate_pass_rate, findings_per_ticket,
	// merge_rate, human_lines_delta, …); LEFT JOIN agent_outcomes so merge
	// columns are nullable/dark until the watcher fills them.
	row := psql.Select(
		`COUNT(DISTINCT m.ticket_id)                                    AS ticket_count`,
		`COALESCE(SUM(c.cost_usd),0)                                    AS cost_usd`,
		`COALESCE(SUM(m.turns),0)                                       AS turns`,
		`AVG((r.proven_count + r.observation_count))                    AS findings_per_ticket`,
		`AVG(CASE WHEN o.merge_state IN ('merged','merged_with_fixes') THEN 1.0 ELSE 0.0 END) AS merge_rate`,
		// … remaining scorecard numeric columns, same expressions as plan 13
	).
		From("agent_run_metrics m").
		LeftJoin("agent_cost_ledger c ON c.ticket_id = m.ticket_id").
		LeftJoin("agent_reviews r ON r.ticket_id = m.ticket_id").
		LeftJoin("agent_outcomes o ON o.ticket_id = m.ticket_id").
		Where(sq.Eq{"m.workflow_name": workflowName, "m.workflow_version": version}).
		Where(sq.Expr("m.ticket_id = ANY(?)", pq.Array(ticketIDs))).
		RunWith(f.conn).QueryRow()
	// Scan into a struct, build the flat map[string]float64 and json.Marshal it.
	// (Full column list + scan mirrors scorecards; elided here for brevity.)
	metrics := map[string]float64{ /* scanned fields */ }
	raw, err := json.Marshal(metrics)
	return raw, metrics, err
}
```

(`json` and `pq` are already imported in the factory file. The `agent_outcomes`/`agent_reviews` LEFT JOINs degrade to nulls if those tables are empty, matching the scorecards dark-column behavior.)

- [ ] **Step 4: Run to green:**

```bash
ginkgo --focus="AgentExperimentFactory" ./atc/db/
```
Expected: PASS.

- [ ] **Step 5: Generate the counterfeiter fake:**

```bash
go generate ./atc/db/... && go build ./atc/db/...
```
Expected: `atc/db/dbfakes/fake_agent_experiment_factory.go` created; build clean.

- [ ] **Step 6: Commit:**

```bash
git add atc/db/agent_experiment_factory.go atc/db/agent_experiment_factory_test.go atc/db/dbfakes/
git commit -m "feat(db): AgentExperimentFactory with SKIP-LOCKED claim and matrix expansion" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: `agent/api/experiments` HTTP handler — benchmark CRUD routes

**Files:**
- Create: `agent/api/experiments/handler.go`
- Test: `agent/api/experiments/handler_test.go`
- Modify: `atc/routes.go` (constants after `GetAgentWorkflowScorecard`; route entries after the scorecard entry)
- Modify: `atc/api/handler.go` (construct handler + register `ListAgentBenchmarkCases`/`CreateAgentBenchmarkCase`)
- Modify: `atc/wrappa/api_auth_wrappa.go` (add the two route names to the `authorized` case group — team-less via `CheckAgentAuthorizationHandler`)
- Modify: `atc/api/accessor/roles.go` (`DefaultRoles`: `ListAgentBenchmarkCases`→viewer, `CreateAgentBenchmarkCase`→member)
- Test: `atc/wrappa/api_auth_wrappa_test.go` ("handles each route" panics on unhandled)

Handler follows `agent/api/reviews/handler.go` (a `Handler{store}` with `http.HandlerFunc` methods; JSON in/out; `accessor`-free — the acting user arrives via an injected func like `reviews.BuildLookupFunc`, but benchmarks need only `created_by`, resolved by the caller in `atc/api/handler.go` from `accessor.GetAccessor(r).Claims().UserName`).

- [ ] **Step 1: Write the failing test `agent/api/experiments/handler_test.go`:**

```go
package experiments_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/concourse/concourse/agent/api/experiments"
)

func newTestHandler() *experiments.Handler {
	return experiments.NewHandler(experiments.NewMemoryStore(), func(r *http.Request) string { return "alice" })
}

func TestCreateBenchmarkCaseValidates(t *testing.T) {
	h := newTestHandler()
	body, _ := json.Marshal(map[string]string{"name": "c1"}) // missing repo/prompt/refs
	req := httptest.NewRequest("POST", "/api/v1/agent/benchmarks", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.CreateBenchmarkCase(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestCreateAndListBenchmarkCase(t *testing.T) {
	h := newTestHandler()
	body, _ := json.Marshal(map[string]any{
		"name": "c1", "repo": "tdmtrader/concourse", "prompt": "fix X",
		"before_ref": "a", "reference_ref": "b", "tags": []string{"db"},
	})
	req := httptest.NewRequest("POST", "/api/v1/agent/benchmarks", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.CreateBenchmarkCase(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var created experiments.BenchmarkCase
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.CreatedBy != "alice" {
		t.Fatalf("created_by = %q, want alice", created.CreatedBy)
	}

	lreq := httptest.NewRequest("GET", "/api/v1/agent/benchmarks?repo=tdmtrader/concourse", nil)
	lrec := httptest.NewRecorder()
	h.ListBenchmarkCases(lrec, lreq)
	var cases []experiments.BenchmarkCase
	_ = json.Unmarshal(lrec.Body.Bytes(), &cases)
	if len(cases) != 1 || cases[0].Name != "c1" {
		t.Fatalf("list = %+v", cases)
	}
}
```

- [ ] **Step 2: Run to see it fail:**

```bash
go test ./agent/api/experiments/ -run TestCreate
```
Expected: build failure — `experiments.NewHandler` / `Handler.CreateBenchmarkCase` undefined.

- [ ] **Step 3: Write `agent/api/experiments/handler.go`:**

```go
package experiments

import (
	"encoding/json"
	"net/http"
)

// UserFunc resolves the acting username from a request (injected by
// atc/api/handler.go from accessor claims; MemoryStore tests pass a stub).
type UserFunc func(*http.Request) string

type Handler struct {
	store    Store
	userName UserFunc
}

func NewHandler(store Store, userName UserFunc) *Handler {
	return &Handler{store: store, userName: userName}
}

func (h *Handler) CreateBenchmarkCase(w http.ResponseWriter, r *http.Request) {
	var c BenchmarkCase
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := c.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, ok, _ := h.store.GetCaseByName(c.Name); ok {
		http.Error(w, "benchmark case name already exists", http.StatusConflict)
		return
	}
	c.CreatedBy = h.userName(r)
	if _, err := h.store.CreateCase(&c); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (h *Handler) ListBenchmarkCases(w http.ResponseWriter, r *http.Request) {
	cases, err := h.store.ListCases(r.URL.Query().Get("repo"), r.URL.Query().Get("tag"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, cases)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
```

- [ ] **Step 4: Run to green:**

```bash
go test ./agent/api/experiments/ -run TestCreate
```
Expected: PASS.

- [ ] **Step 5: Add route constants + entries** in `atc/routes.go` (constants near the other agent routes, entries in the `Routes` slice — place after the wave-4 `GetAgentWorkflowScorecard` entry):

```go
// constants
ListAgentBenchmarkCases  = "ListAgentBenchmarkCases"
CreateAgentBenchmarkCase = "CreateAgentBenchmarkCase"
```
```go
// route entries
{Path: "/api/v1/agent/benchmarks", Method: "GET", Name: ListAgentBenchmarkCases},
{Path: "/api/v1/agent/benchmarks", Method: "POST", Name: CreateAgentBenchmarkCase},
```

- [ ] **Step 6: Wire the handler** in `atc/api/handler.go`: construct `experimentsServer := experimentsapi.NewHandler(db.NewAgentExperimentFactory(dbConn), func(r *http.Request) string { return accessor.GetAccessor(r).Claims().UserName })` alongside the existing `feedbackServer`/`reviewsServer`, add import `experimentsapi "github.com/concourse/concourse/agent/api/experiments"`, and register:

```go
atc.ListAgentBenchmarkCases:  http.HandlerFunc(experimentsServer.ListBenchmarkCases),
atc.CreateAgentBenchmarkCase: http.HandlerFunc(experimentsServer.CreateBenchmarkCase),
```

- [ ] **Step 7: Add the auth-wrappa case** in `atc/wrappa/api_auth_wrappa.go` — both route names go into the `authorized` team-less case group handled by `CheckAgentAuthorizationHandler` (the group the five existing agent-feedback routes already sit in). Add `atc.ListAgentBenchmarkCases, atc.CreateAgentBenchmarkCase,` to that case list.

- [ ] **Step 8: Add role entries** in `atc/api/accessor/roles.go` `DefaultRoles` (a missing entry silently becomes admin-only per the file's own comment):

```go
atc.ListAgentBenchmarkCases:  accessor.Viewer,
atc.CreateAgentBenchmarkCase: accessor.Member,
```

- [ ] **Step 9: Run the wrappa exhaustiveness spec + build:**

```bash
ginkgo --focus="handles each route" ./atc/wrappa/ && go build ./atc/...
```
Expected: PASS (no unhandled-route panic) and clean build.

- [ ] **Step 10: Commit:**

```bash
git add agent/api/experiments/handler.go agent/api/experiments/handler_test.go atc/routes.go atc/api/handler.go atc/wrappa/api_auth_wrappa.go atc/api/accessor/roles.go
git commit -m "feat(agent): benchmark-case CRUD API routes" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6: `fly agent benchmarks` — list/create benchmark cases

**Files:**
- Modify: `fly/commands/internal/displayhelpers/error_handling.go` (add a `Succeedf` stdout helper — see Step 0; consumed by Tasks 6, 11, 20)
- Create: `fly/commands/agent_benchmarks.go`
- Modify: `fly/commands/agent.go` (add `Benchmarks AgentBenchmarksCommand` field to the shared `AgentCommand` struct — additive merge per the credentials wave-1 addendum)
- Modify: `go-concourse/concourse/agent.go` (client methods `ListAgentBenchmarkCases`/`CreateAgentBenchmarkCase` — file created by an earlier agent workstream; append additively)
- Test: `fly/integration/agent_benchmarks_test.go`

Follows the `fly agent costs` recipe (Task 17 of credentials-and-budgets); drives the routes over `target.Client().HTTPClient()` via go-concourse.

- [ ] **Step 0: Add the `Succeedf` stdout helper to `displayhelpers`.** `displayhelpers` today exports only `Failf`/`FailWithErrorf` (both write to `ui.Stderr` and `os.Exit(1)`) — there is no success-to-stdout counterpart, but all three fly commands here (Tasks 6, 11, 20) print a confirmation line the integration tests assert on stdout. Add, in `fly/commands/internal/displayhelpers/error_handling.go`, a mirror of `Failf` that writes to stdout and does not exit:

```go
func Succeedf(message string, args ...any) {
	fmt.Fprintf(os.Stdout, message+"\n", args...)
}
```

(`fmt` and `os` are already imported.) This is a fly-internal helper, additive-only — no contract surface.

- [ ] **Step 1: Write the failing integration spec `fly/integration/agent_benchmarks_test.go`** (recipe: the `fly agent` spec in credentials-and-budgets):

```go
package integration_test

import (
	"net/http"
	"os/exec"

	"github.com/concourse/concourse/agent/api/experiments"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("fly agent benchmarks", func() {
	Describe("list", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(ghttp.CombineHandlers(
				ghttp.VerifyRequest("GET", "/api/v1/agent/benchmarks"),
				ghttp.RespondWithJSONEncoded(http.StatusOK, []experiments.BenchmarkCase{
					{ID: 1, Name: "fix-gc-race", Repo: "tdmtrader/concourse"},
				}),
			))
		})
		It("renders the case table", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "benchmarks")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			Eventually(sess).Should(gexec.Exit(0))
			Expect(sess.Out).To(gbytes.Say("fix-gc-race"))
		})
	})

	Describe("create", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(ghttp.CombineHandlers(
				ghttp.VerifyRequest("POST", "/api/v1/agent/benchmarks"),
				ghttp.RespondWithJSONEncoded(http.StatusOK, experiments.BenchmarkCase{ID: 2, Name: "new-case"}),
			))
		})
		It("posts the case and prints its id", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "benchmarks", "create",
				"--name", "new-case", "--repo", "tdmtrader/concourse",
				"--prompt", "do X", "--before-ref", "a", "--reference-ref", "b")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			Eventually(sess).Should(gexec.Exit(0))
			Expect(sess.Out).To(gbytes.Say("created benchmark case new-case"))
		})
	})
})
```

- [ ] **Step 2: Run to verify it fails:**

```bash
ginkgo ./fly/integration/ --focus="fly agent benchmarks"
```
Expected: `Unknown command` (non-zero exit) — no `benchmarks` subcommand yet.

- [ ] **Step 3: Add the go-concourse client methods** in `go-concourse/concourse/agent.go` (append; mirror the credentials cost-rollup method's `connection.Send` pattern):

```go
func (c *client) ListAgentBenchmarkCases(repo string) ([]experiments.BenchmarkCase, error) {
	var cases []experiments.BenchmarkCase
	params := rata.Params{}
	q := url.Values{}
	if repo != "" {
		q.Set("repo", repo)
	}
	err := c.connection.Send(internal.Request{
		RequestName: atc.ListAgentBenchmarkCases,
		Params:      params,
		Query:       q,
	}, &internal.Response{Result: &cases})
	return cases, err
}

func (c *client) CreateAgentBenchmarkCase(bc experiments.BenchmarkCase) (experiments.BenchmarkCase, error) {
	payload, _ := json.Marshal(bc)
	var out experiments.BenchmarkCase
	err := c.connection.Send(internal.Request{
		RequestName: atc.CreateAgentBenchmarkCase,
		Body:        bytes.NewBuffer(payload),
		Header:      http.Header{"Content-Type": []string{"application/json"}},
	}, &internal.Response{Result: &out})
	return out, err
}
```
Add these method signatures to the `Client` interface in `go-concourse/concourse/client.go` and the `experiments` import.

- [ ] **Step 4: Write `fly/commands/agent_benchmarks.go`:**

```go
package commands

import (
	"fmt"

	"github.com/concourse/concourse/agent/api/experiments"
	"github.com/concourse/concourse/fly/commands/internal/displayhelpers"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/fly/ui"
	"github.com/fatih/color"
)

type AgentBenchmarksCommand struct {
	Repo   string                        `long:"repo" description:"Filter by repo slug"`
	Create AgentBenchmarksCreateCommand `command:"create" description:"Create a benchmark case"`
}

func (c *AgentBenchmarksCommand) Execute(args []string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}
	cases, err := target.Client().ListAgentBenchmarkCases(c.Repo)
	if err != nil {
		return err
	}
	table := ui.Table{Headers: ui.TableRow{
		{Contents: "id", Color: color.New(color.Bold)},
		{Contents: "name", Color: color.New(color.Bold)},
		{Contents: "repo", Color: color.New(color.Bold)},
	}}
	for _, bc := range cases {
		table.Data = append(table.Data, ui.TableRow{
			{Contents: fmt.Sprintf("%d", bc.ID)},
			{Contents: bc.Name},
			{Contents: bc.Repo},
		})
	}
	return table.Render(Fly.PrintTableHeaders)
}

type AgentBenchmarksCreateCommand struct {
	Name         string `long:"name" required:"true" description:"Unique case name"`
	Repo         string `long:"repo" required:"true" description:"Repo slug (owner/name)"`
	Prompt       string `long:"prompt" required:"true" description:"Ticket-style prompt"`
	BeforeRef    string `long:"before-ref" required:"true" description:"Git ref before the change"`
	ReferenceRef string `long:"reference-ref" required:"true" description:"Git ref of the human solution"`
	Tags         []string `long:"tag" description:"Repeatable tag"`
	Notes        string `long:"notes" description:"Freeform notes"`
}

func (c *AgentBenchmarksCreateCommand) Execute(args []string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}
	out, err := target.Client().CreateAgentBenchmarkCase(experiments.BenchmarkCase{
		Name: c.Name, Repo: c.Repo, Prompt: c.Prompt,
		BeforeRef: c.BeforeRef, ReferenceRef: c.ReferenceRef, Tags: c.Tags, Notes: c.Notes,
	})
	if err != nil {
		return err
	}
	displayhelpers.Succeedf("created benchmark case %s (id %d)", out.Name, out.ID)
	return nil
}
```

- [ ] **Step 5: Register on the shared `AgentCommand` struct** (`fly/commands/agent.go`), additive:

```go
Benchmarks AgentBenchmarksCommand `command:"benchmarks" description:"Manage agent benchmark cases"`
```

- [ ] **Step 6: Run to green + full fly-integration:**

```bash
ginkgo ./fly/integration/ --focus="fly agent benchmarks"
```
Expected: PASS.

- [ ] **Step 7: Commit:**

```bash
git add fly/commands/agent_benchmarks.go fly/commands/agent.go go-concourse/concourse/agent.go go-concourse/concourse/client.go fly/integration/agent_benchmarks_test.go
git commit -m "feat(fly): fly agent benchmarks list/create" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 7: Benchmark extraction skill port

The charter's "extraction-skill port mining {ticket prompt, beforeRef, referenceRef} from team repos" is an interactive skill (mining is a human-guided git-archaeology task), not a server component. Port it as a Claude Code skill living in the repo, driving `fly agent benchmarks create`.

**Files:**
- Create: `.claude/skills/extract-benchmark/SKILL.md`
- Create: `ci-agent/gapgen/benchmark_extract.go` (a small helper the skill invokes to compute `before_ref`/`reference_ref` from a merged PR) — reuse `ci-agent/gapgen` (the existing gap-generation package) as the home
- Test: `ci-agent/gapgen/benchmark_extract_test.go`

- [ ] **Step 1: Write the failing test `ci-agent/gapgen/benchmark_extract_test.go`** (plain Go, ci-agent module):

```go
package gapgen

import "testing"

func TestDeriveRefsFromMergeParents(t *testing.T) {
	// A squash-merge commit M with parent P: before_ref = P, reference_ref = M.
	got := DeriveBenchmarkRefs("M", []string{"P"})
	if got.BeforeRef != "P" || got.ReferenceRef != "M" {
		t.Fatalf("got %+v, want before=P reference=M", got)
	}
	// A true merge commit with two parents: before_ref = first parent (base),
	// reference_ref = the merge commit itself.
	got = DeriveBenchmarkRefs("MERGE", []string{"BASE", "TOPIC"})
	if got.BeforeRef != "BASE" || got.ReferenceRef != "MERGE" {
		t.Fatalf("got %+v, want before=BASE reference=MERGE", got)
	}
}
```

- [ ] **Step 2: Run to see it fail:**

```bash
cd ci-agent && go test ./gapgen/ -run TestDeriveRefs
```
Expected: build failure — `DeriveBenchmarkRefs` undefined.

- [ ] **Step 3: Write `ci-agent/gapgen/benchmark_extract.go`:**

```go
package gapgen

// BenchmarkRefs is the {beforeRef, referenceRef} pair for a benchmark case.
type BenchmarkRefs struct {
	BeforeRef    string
	ReferenceRef string
}

// DeriveBenchmarkRefs computes the pre-change and human-solution refs from a
// merge/squash commit and its parents. First parent is always the base
// (pre-change) state; the commit itself is the human solution to compare
// against. Works for both squash merges (1 parent) and true merges (2+).
func DeriveBenchmarkRefs(commit string, parents []string) BenchmarkRefs {
	base := commit + "^" // fallback if parents unknown
	if len(parents) > 0 {
		base = parents[0]
	}
	return BenchmarkRefs{BeforeRef: base, ReferenceRef: commit}
}
```

- [ ] **Step 4: Run to green:**

```bash
cd ci-agent && go test ./gapgen/ -run TestDeriveRefs
```
Expected: PASS.

- [ ] **Step 5: Write `.claude/skills/extract-benchmark/SKILL.md`** (interactive mining recipe):

```markdown
---
name: extract-benchmark
description: Mine a benchmark case ({ticket prompt, before_ref, reference_ref}) from a merged change in a team repo and register it via `fly agent benchmarks create`. Use when the user wants to add a benchmark case, build an eval set, or capture a good/bad example from git history.
---

# Extract Benchmark Case

Turn a real merged change into a reusable benchmark case for agent experiments.

## Steps

1. Ask the user for the repo (owner/name slug) and the merge/PR commit (or find it: `git log --merges --oneline -20`).
2. Derive refs: `before_ref` = first parent of the merge commit, `reference_ref` = the merge commit itself. (The ci-agent helper `gapgen.DeriveBenchmarkRefs` encodes this rule; parents via `git rev-list --parents -n 1 <commit>`.)
3. Draft a ticket-style `prompt` describing the task the human solved, WITHOUT leaking the solution — write it as if filing a ticket before the work: the problem, the acceptance criteria, the target files. Confirm with the user.
4. Choose a short unique `name` (kebab-case, e.g. `fix-gc-container-leak`) and optional `tags`.
5. Register: `fly -t <target> agent benchmarks create --name <name> --repo <repo> --prompt "<prompt>" --before-ref <before> --reference-ref <ref> [--tag <t>]`.
6. Confirm it appears in `fly agent benchmarks --repo <repo>`.

## Guardrails

- The prompt must not describe the diff — an agent given the prompt should reach the solution independently. If the prompt names the exact change, rewrite it as a symptom/requirement.
- `before_ref` must be a state where the repo builds (so the agent starts from green). Verify with the repo's dev-mcp `build()` if in doubt.
- One case = one tractable ticket. Split multi-concern PRs into multiple cases.
```

- [ ] **Step 6: Commit:**

```bash
git add ci-agent/gapgen/benchmark_extract.go ci-agent/gapgen/benchmark_extract_test.go .claude/skills/extract-benchmark/SKILL.md
git commit -m "feat(ci-agent): benchmark-ref derivation helper + extract-benchmark skill" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 8: Experiment CRUD API — create/list/get routes

**Files:**
- Modify: `agent/api/experiments/handler.go` (add `CreateExperiment`/`ListExperiments`/`GetExperiment` methods + a workflow-existence validator seam)
- Test: `agent/api/experiments/handler_test.go` (add experiment cases)
- Modify: `atc/routes.go` (constants `CreateAgentExperiment`/`GetAgentExperiment`/`ListAgentExperiments`; entries)
- Modify: `atc/api/handler.go` (register the three routes)
- Modify: `atc/wrappa/api_auth_wrappa.go` (authorized group)
- Modify: `atc/api/accessor/roles.go` (`CreateAgentExperiment`→member; `GetAgentExperiment`/`ListAgentExperiments`→viewer)

`CreateExperiment` validates the matrix: every case name resolves via `store.GetCaseByName`, and every workflow version exists — the workflow-existence check uses an injected `WorkflowExistsFunc func(name string, version int) (bool, error)` (wired in `atc/api/handler.go` from `db.NewAgentWorkflowsFactory(dbConn).Get`), keeping this package free of `agent/workflow` DB deps.

- [ ] **Step 1: Add failing experiment cases to `agent/api/experiments/handler_test.go`:**

```go
func TestCreateExperimentRejectsUnknownCase(t *testing.T) {
	store := experiments.NewMemoryStore()
	h := experiments.NewHandler(store, func(r *http.Request) string { return "alice" })
	h.SetValidators(func(name string, v int) (bool, error) { return true, nil }) // all workflows ok
	body, _ := json.Marshal(map[string]any{
		"name": "e1",
		"matrix": map[string]any{
			"cases":     []string{"missing-case"},
			"workflows": []map[string]any{{"name": "wf", "version": 1}},
		},
	})
	req := httptest.NewRequest("POST", "/api/v1/agent/experiments", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.CreateExperiment(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for unknown case", rec.Code)
	}
}

func TestCreateExperimentSucceedsAndGet(t *testing.T) {
	store := experiments.NewMemoryStore()
	_, _ = store.CreateCase(&experiments.BenchmarkCase{Name: "c1", Repo: "r", Prompt: "p", BeforeRef: "a", ReferenceRef: "b"})
	h := experiments.NewHandler(store, func(r *http.Request) string { return "alice" })
	h.SetValidators(func(name string, v int) (bool, error) { return true, nil })
	body, _ := json.Marshal(map[string]any{
		"name": "e1",
		"matrix": map[string]any{
			"cases":     []string{"c1"},
			"workflows": []map[string]any{{"name": "wf", "version": 2}},
		},
	})
	req := httptest.NewRequest("POST", "/api/v1/agent/experiments", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.CreateExperiment(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d (%s)", rec.Code, rec.Body.String())
	}
	var created experiments.Experiment
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.Status != experiments.StatusPending {
		t.Fatalf("status = %q, want pending", created.Status)
	}
}
```

- [ ] **Step 2: Run to see it fail:**

```bash
go test ./agent/api/experiments/ -run TestCreateExperiment
```
Expected: build failure — `SetValidators`/`CreateExperiment` undefined.

- [ ] **Step 3: Extend `agent/api/experiments/handler.go`:**

```go
// WorkflowExistsFunc reports whether a workflow-definition version exists.
type WorkflowExistsFunc func(name string, version int) (bool, error)

// SetValidators wires the workflow-existence check (atc/api/handler.go).
func (h *Handler) SetValidators(w WorkflowExistsFunc) { h.workflowExists = w }

// (add `workflowExists WorkflowExistsFunc` field to Handler)

func (h *Handler) CreateExperiment(w http.ResponseWriter, r *http.Request) {
	var e Experiment
	if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := e.Matrix.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for _, name := range e.Matrix.Cases {
		if _, ok, err := h.store.GetCaseByName(name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		} else if !ok {
			http.Error(w, "unknown benchmark case: "+name, http.StatusBadRequest)
			return
		}
	}
	if h.workflowExists != nil {
		for _, wf := range e.Matrix.Workflows {
			if ok, err := h.workflowExists(wf.Name, wf.Version); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			} else if !ok {
				http.Error(w, "unknown workflow version", http.StatusBadRequest)
				return
			}
		}
	}
	e.CreatedBy = h.userName(r)
	created, err := h.store.CreateExperiment(&e)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, created)
}

func (h *Handler) ListExperiments(w http.ResponseWriter, r *http.Request) {
	exps, err := h.store.ListExperiments()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, exps)
}

// GetExperiment expects the :experiment_id path var resolved by the caller
// (atc/api/handler.go extracts it via rata and passes it in the request
// context under experiments.CtxExperimentID; the MemoryStore test passes it
// as a query param for simplicity).
func (h *Handler) GetExperiment(w http.ResponseWriter, r *http.Request) {
	id := experimentIDFrom(r)
	if id <= 0 {
		http.Error(w, "bad experiment id", http.StatusBadRequest)
		return
	}
	e, ok, err := h.store.GetExperiment(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, e)
}
```

Add `experimentIDFrom(r *http.Request) int` reading `atc/api/handler.go`'s injected path var (mirror how `reviews` handler reads `:commit`; for the unit test it reads `r.URL.Query().Get("id")`).

- [ ] **Step 4: Run to green:**

```bash
go test ./agent/api/experiments/ -run TestCreateExperiment
```
Expected: PASS.

- [ ] **Step 5: Add routes/wrappa/roles/handler wiring** exactly as Task 5 did, for constants `CreateAgentExperiment` (POST `/api/v1/agent/experiments`, member), `ListAgentExperiments` (GET `/api/v1/agent/experiments`, viewer), `GetAgentExperiment` (GET `/api/v1/agent/experiments/:experiment_id`, viewer). Wire `experimentsServer.SetValidators(func(name string, v int) (bool, error) { _, ok, err := db.NewAgentWorkflowsFactory(dbConn).Get(name, v); return ok, err })` in `atc/api/handler.go`.

- [ ] **Step 6: Verify wrappa exhaustiveness + build:**

```bash
ginkgo --focus="handles each route" ./atc/wrappa/ && go build ./atc/...
```
Expected: PASS.

- [ ] **Step 7: Commit:**

```bash
git add agent/api/experiments/handler.go agent/api/experiments/handler_test.go atc/routes.go atc/api/handler.go atc/wrappa/api_auth_wrappa.go atc/api/accessor/roles.go
git commit -m "feat(agent): experiment create/list/get API with matrix validation" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 9: `experiment_runner` RunnableComponent — daily-cap-admitted ticket dispatch

**Files:**
- Create: `agent/experiments/runner.go`
- Create: `agent/experiments/runner_suite_test.go`, `agent/experiments/runner_test.go`
- Modify: `atc/component.go:26` (constant `ComponentExperimentRunner = "experiment_runner"`)
- Modify: `atc/atccmd/command.go` (factory + component entry, after the wave-4 dispatch/outcome components)
- Test: `agent/experiments/runner_test.go`

The runner (per the `runlifecycle.Lifecycler` recipe: `Run(ctx) error`, context logger, continue-past-per-item-errors, polling+notify) claims one pending experiment per tick, then for each still-`pending` cell within the day's remaining budget creates a `draft` ticket, transitions it `draft→queued` (dispatcher takes over), and records the cell as `running`. Budget admission via `budget.Checker.GlobalDailyRemaining()`. Terminal-state mirroring (ticket→cell) also runs each tick.

- [ ] **Step 1: Write `agent/experiments/runner_suite_test.go`:**

```go
package experiments_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestExperimentRunner(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Experiment Runner Suite")
}
```

- [ ] **Step 2: Write the failing spec `agent/experiments/runner_test.go`:**

```go
package experiments_test

import (
	"context"

	api "github.com/concourse/concourse/agent/api/experiments"
	"github.com/concourse/concourse/agent/api/experiments/experimentsfakes"
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/api/tickets/ticketsfakes"
	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/budget/budgetfakes"
	"github.com/concourse/concourse/agent/experiments"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Runner", func() {
	var (
		store   *experimentsfakes.FakeStore
		tickets_ *ticketsfakes.FakeStore
		checker *budgetfakes.FakeChecker
		runner  *experiments.Runner
	)

	BeforeEach(func() {
		store = new(experimentsfakes.FakeStore)
		tickets_ = new(ticketsfakes.FakeStore)
		checker = new(budgetfakes.FakeChecker)
		runner = experiments.NewRunner(store, tickets_, checker)

		exp := &api.Experiment{
			ID: 1, Status: api.StatusRunning,
			Matrix: api.Matrix{Cases: []string{"c1"}, Workflows: []api.WorkflowRef{{Name: "wf", Version: 3}}},
			Runs: []api.ExperimentRun{{BenchmarkCaseName: "c1", WorkflowName: "wf", WorkflowVersion: 3, Repetition: 1, Status: api.StatusPending}},
		}
		store.ClaimPendingReturns(exp, true, nil)
		store.GetExperimentReturns(exp, true, nil)
		store.ListExperimentsReturns([]api.Experiment{*exp}, nil)
		store.GetCaseByNameReturns(&api.BenchmarkCase{Name: "c1", Repo: "r", Prompt: "do X"}, true, nil)
		tickets_.CreateReturns(77, nil)
	})

	It("queues a ticket per pending cell when budget remains", func() {
		checker.GlobalDailyRemainingReturns(budget.Remaining{RemainingUSD: 100, Exhausted: false}, nil)

		Expect(runner.Run(context.Background())).To(Succeed())

		Expect(tickets_.CreateCallCount()).To(Equal(1))
		created := tickets_.CreateArgsForCall(0)
		Expect(created.Origin).To(Equal("fly"))
		Expect(created.Repo).To(Equal("r"))
		Expect(*created.WorkflowVersion).To(Equal(3))
		// draft->queued transition on the created ticket
		id, from, to, _ := tickets_.TransitionArgsForCall(0)
		Expect(id).To(Equal(77))
		Expect(from).To(Equal(tickets.StateDraft))
		Expect(to).To(Equal(tickets.StateQueued))
		Expect(store.LinkRunCallCount()).To(Equal(1))
	})

	It("does not queue cells once the daily cap is exhausted", func() {
		checker.GlobalDailyRemainingReturns(budget.Remaining{RemainingUSD: 0, Exhausted: true}, nil)

		Expect(runner.Run(context.Background())).To(Succeed())

		Expect(tickets_.CreateCallCount()).To(Equal(0))
		Expect(store.FinishExperimentCallCount()).To(Equal(0)) // still has pending cells
	})

	It("finishes an experiment when all cells are terminal", func() {
		done := &api.Experiment{
			ID: 2, Status: api.StatusRunning,
			Runs: []api.ExperimentRun{{BenchmarkCaseName: "c1", WorkflowName: "wf", WorkflowVersion: 3, Repetition: 1, Status: api.StatusOK}},
		}
		store.ClaimPendingReturns(nil, false, nil)
		store.ListExperimentsReturns([]api.Experiment{*done}, nil)
		store.GetExperimentReturns(done, true, nil)
		checker.GlobalDailyRemainingReturns(budget.Remaining{RemainingUSD: 100}, nil)

		Expect(runner.Run(context.Background())).To(Succeed())

		Expect(store.FinishExperimentCallCount()).To(Equal(1))
		id, status := store.FinishExperimentArgsForCall(0)
		Expect(id).To(Equal(2))
		Expect(status).To(Equal(api.StatusComplete))
	})
})
```

- [ ] **Step 3: Run to see it fail:**

```bash
ginkgo ./agent/experiments/
```
Expected: build failure — `experiments.Runner`/`NewRunner` undefined; `experimentsfakes` missing.

- [ ] **Step 4: Generate the `experiments.Store` counterfeiter fake** (add the directive to `agent/api/experiments/types.go` — done in Task 3 — then):

```bash
cd agent && go generate ./api/experiments/... 
```
Expected: `agent/api/experiments/experimentsfakes/fake_store.go`.

- [ ] **Step 5: Write `agent/experiments/runner.go`:**

```go
package experiments

import (
	"context"
	"fmt"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"

	api "github.com/concourse/concourse/agent/api/experiments"
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/budget"
)

// Runner is the experiment_runner RunnableComponent: it claims a pending
// experiment, queues one ticket per pending matrix cell while the global
// daily budget allows, mirrors dispatched-ticket terminal state back into
// cells, and finishes experiments once every cell is terminal. Ticket state
// changes go exclusively through tickets.Store.Transition (single-writer).
type Runner struct {
	store   api.Store
	tickets tickets.Store
	budget  budget.Checker
}

func NewRunner(store api.Store, ticketStore tickets.Store, checker budget.Checker) *Runner {
	return &Runner{store: store, tickets: ticketStore, budget: checker}
}

func terminal(status string) bool {
	return status == api.StatusOK || status == api.StatusFailed || status == api.StatusError
}

func (r *Runner) Run(ctx context.Context) error {
	logger := lagerctx.FromContext(ctx).Session("experiment-runner")

	// 1. Claim at most one pending experiment (expands its cells).
	if _, ok, err := r.store.ClaimPending(); err != nil {
		logger.Error("failed-to-claim-experiment", err)
	} else if ok {
		logger.Info("claimed-experiment")
	}

	// 2. Walk running experiments: queue admitted cells, mirror terminals.
	exps, err := r.store.ListExperiments()
	if err != nil {
		logger.Error("failed-to-list-experiments", err)
		return err
	}
	for _, meta := range exps {
		if meta.Status != api.StatusRunning {
			continue
		}
		exp, ok, err := r.store.GetExperiment(meta.ID)
		if err != nil || !ok {
			logger.Error("failed-to-load-experiment", err, lager.Data{"id": meta.ID})
			continue
		}
		r.advance(ctx, logger, exp)
	}
	return nil
}

func (r *Runner) advance(ctx context.Context, logger lager.Logger, exp *api.Experiment) {
	allTerminal := true
	for _, run := range exp.Runs {
		switch run.Status {
		case api.StatusPending:
			allTerminal = false
			rem, err := r.budget.GlobalDailyRemaining()
			if err != nil {
				logger.Error("failed-budget-check", err)
				return
			}
			if rem.Exhausted {
				logger.Info("daily-cap-exhausted-holding", lager.Data{"experiment": exp.ID})
				return // leave the rest pending; resume next tick
			}
			r.queueCell(logger, exp, run)
		case api.StatusRunning:
			allTerminal = false
			r.mirror(logger, exp, run)
		}
	}
	if allTerminal && len(exp.Runs) > 0 {
		status := api.StatusComplete
		if err := r.store.FinishExperiment(exp.ID, status); err != nil {
			logger.Error("failed-to-finish-experiment", err, lager.Data{"id": exp.ID})
			return
		}
		logger.Info("experiment-complete", lager.Data{"id": exp.ID})
	}
}

func (r *Runner) queueCell(logger lager.Logger, exp *api.Experiment, run api.ExperimentRun) {
	bc, ok, err := r.store.GetCaseByName(run.BenchmarkCaseName)
	if err != nil || !ok {
		logger.Error("case-missing", err, lager.Data{"case": run.BenchmarkCaseName})
		return
	}
	version := run.WorkflowVersion
	t := &tickets.Ticket{
		Title:           fmt.Sprintf("experiment %d: %s @ %s v%d", exp.ID, bc.Name, run.WorkflowName, version),
		Body:            bc.Prompt,
		Origin:          "fly",
		Repo:            bc.Repo,
		WorkflowName:    run.WorkflowName,
		WorkflowVersion: &version,
		CreatedBy:       fmt.Sprintf("experiment-%d", exp.ID),
	}
	ticketID, err := r.tickets.Create(t)
	if err != nil {
		logger.Error("failed-to-create-ticket", err)
		return
	}
	// Attribution rides on Ticket.CreatedBy (set above at Create time);
	// TransitionMeta has no By field (ticket-core §2.1.1). No side-band
	// values apply to draft→queued, so the meta is empty.
	if err := r.tickets.Transition(ticketID, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{}); err != nil {
		logger.Error("failed-to-queue-ticket", err, lager.Data{"ticket": ticketID})
		return
	}
	cell := api.Cell{BenchmarkCaseName: run.BenchmarkCaseName, WorkflowName: run.WorkflowName, WorkflowVersion: version, Repetition: run.Repetition}
	if err := r.store.LinkRun(exp.ID, cell, ticketID); err != nil {
		logger.Error("failed-to-link-run", err)
	}
}

func (r *Runner) mirror(logger lager.Logger, exp *api.Experiment, run api.ExperimentRun) {
	if run.TicketID == nil {
		return
	}
	t, ok, err := r.tickets.Get(*run.TicketID)
	if err != nil || !ok {
		return
	}
	status := ""
	switch t.State {
	case tickets.StateMerged, tickets.StateMergedWithFixes, tickets.StateNeedsReview,
		tickets.StateSentBack, tickets.StateAbandoned:
		status = api.StatusOK
	case tickets.StateFailed:
		status = api.StatusFailed
	case tickets.StateErrored:
		status = api.StatusError
	default:
		return // still running/queued
	}
	cell := api.Cell{BenchmarkCaseName: run.BenchmarkCaseName, WorkflowName: run.WorkflowName, WorkflowVersion: run.WorkflowVersion, Repetition: run.Repetition}
	if err := r.store.SetRunStatus(exp.ID, cell, t.PipelineRunID, status); err != nil {
		logger.Error("failed-to-set-run-status", err)
	}
}
```

- [ ] **Step 6: Run to green:**

```bash
ginkgo ./agent/experiments/
```
Expected: PASS.

- [ ] **Step 7: Add the component constant** (`atc/component.go`, after the wave-4 additions):

```go
ComponentExperimentRunner = "experiment_runner"
```

- [ ] **Step 8: Wire the component** in `atc/atccmd/command.go` (after the wave-4 dispatch/outcome-watcher entries; default 10s polling — polling + notify, never notify-only). Construct the factory alongside the other agent factories:

```go
dbExperimentFactory := db.NewAgentExperimentFactory(dbConn)
```
Component entry:
```go
components = append(components, RunnableComponent{
	Component: atc.Component{Name: atc.ComponentExperimentRunner},
	Runnable: experiments.NewRunner(
		dbExperimentFactory,
		db.NewAgentTicketsFactory(dbConn),
		agentBudgetChecker, // the wave-1 budget.Checker already constructed for dispatch
	),
})
```
Add import `"github.com/concourse/concourse/agent/experiments"`. (`agentBudgetChecker` is the `budget.Checker` credentials-and-budgets constructs and dispatch already wires; reuse the same variable.)

- [ ] **Step 9: Build + verify compiles:**

```bash
go build ./atc/... ./agent/...
```
Expected: clean.

- [ ] **Step 10: Commit:**

```bash
git add agent/experiments/ agent/api/experiments/experimentsfakes/ atc/component.go atc/atccmd/command.go
git commit -m "feat(atc): experiment_runner component - daily-cap-admitted ticket dispatch" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 10: Experiment scorecard-delta view — `GetAgentExperimentDelta`

**Files:**
- Create: `agent/api/experiments/delta.go`
- Test: `agent/api/experiments/delta_test.go`
- Modify: `agent/api/experiments/handler.go` (`GetExperimentDelta` method)
- Modify: `atc/routes.go`, `atc/api/handler.go`, `atc/wrappa/api_auth_wrappa.go`, `atc/api/accessor/roles.go` (`GetAgentExperimentDelta`, viewer)

The delta computes, per workflow version in the experiment's matrix, a scorecard-shaped rollup restricted to this experiment's runs, then diffs each against a baseline version. **Why not reuse the scorecards package:** the wave-4 `scorecard-rollup-api` surface exposes exactly one method — `scorecards.Store.Scorecard(workflowName string, versions []int) (*Scorecard, error)` (plan 13) — keyed by `(workflow_name, workflow_version)` with no ticket-set filter, and its only route is `GetAgentWorkflowScorecard?versions=`. It **cannot** restrict a rollup to one experiment's specific tickets, which the delta requires (an experiment reruns the same benchmark cases, so its runs are a strict subset of a version's overall traffic). Rather than add a ticket-scoped method to scorecards (a cross-workstream contract change), this workstream — which already owns §1.12 and reads the prior-wave tables directly (see the M2 analytics lib, Tasks 13–15) — computes the ticket-scoped rollup itself over `agent_run_metrics`/`agent_cost_ledger`/`agent_outcomes` filtered by `ticket_id IN (experiment's ticket ids)`. It is injected via `ScorecardFunc func(workflowName string, version int, ticketIDs []int) (json.RawMessage, map[string]float64, error)`, wired in `atc/api/handler.go` to the experiment store's own `ScorecardForTickets` method (Step 6); the metrics are returned as a `map[string]float64` of the numeric scorecard fields for diffing. Deltas are per-metric `variant − baseline`.

- [ ] **Step 1: Write the failing test `agent/api/experiments/delta_test.go`:**

```go
package experiments_test

import (
	"encoding/json"
	"testing"

	"github.com/concourse/concourse/agent/api/experiments"
)

func TestComputeDeltaAgainstBaseline(t *testing.T) {
	exp := &experiments.Experiment{
		ID: 1,
		Matrix: experiments.Matrix{
			Workflows: []experiments.WorkflowRef{{Name: "wf", Version: 3}, {Name: "wf", Version: 4}},
		},
		Runs: []experiments.ExperimentRun{
			{WorkflowName: "wf", WorkflowVersion: 3, TicketID: ip(10), Status: "ok"},
			{WorkflowName: "wf", WorkflowVersion: 4, TicketID: ip(11), Status: "ok"},
		},
	}
	scorecards := map[int]map[string]float64{
		3: {"cost_usd": 12.0, "merge_rate": 0.5},
		4: {"cost_usd": 9.0, "merge_rate": 0.75},
	}
	fn := func(name string, v int, ticketIDs []int) (json.RawMessage, map[string]float64, error) {
		raw, _ := json.Marshal(scorecards[v])
		return raw, scorecards[v], nil
	}
	delta, err := experiments.ComputeDelta(exp, 3, fn)
	if err != nil {
		t.Fatalf("ComputeDelta: %v", err)
	}
	if delta.Baseline != 3 || len(delta.Columns) != 2 {
		t.Fatalf("delta = %+v", delta)
	}
	// v4 column deltas vs baseline v3
	var v4 *experiments.DeltaColumn
	for i := range delta.Columns {
		if delta.Columns[i].WorkflowVersion == 4 {
			v4 = &delta.Columns[i]
		}
	}
	if v4 == nil || v4.Deltas["cost_usd"] != -3.0 || v4.Deltas["merge_rate"] != 0.25 {
		t.Fatalf("v4 deltas = %+v", v4)
	}
}

func ip(i int) *int { return &i }
```

- [ ] **Step 2: Run to see it fail:**

```bash
go test ./agent/api/experiments/ -run TestComputeDelta
```
Expected: build failure — `experiments.ComputeDelta` undefined.

- [ ] **Step 3: Write `agent/api/experiments/delta.go`:**

```go
package experiments

import "encoding/json"

// ScorecardFunc returns a workflow version's scorecard-shaped rollup (raw JSON
// for display + a flat numeric map for diffing) restricted to a set of ticket
// ids. Wired in atc/api/handler.go to the experiment store's ScorecardForTickets
// method (this workstream computes the ticket-scoped rollup itself over
// agent_run_metrics/agent_cost_ledger/agent_outcomes — the scorecards package
// has no ticket-set-filtered surface). ticketIDs is the set of this
// experiment's dispatched tickets for that version.
type ScorecardFunc func(workflowName string, version int, ticketIDs []int) (json.RawMessage, map[string]float64, error)

// ComputeDelta builds one column per distinct workflow version in the
// experiment, each carrying its scorecard and per-metric delta versus the
// baseline version. Metrics absent from a column contribute no delta.
func ComputeDelta(exp *Experiment, baseline int, fn ScorecardFunc) (*ExperimentDelta, error) {
	// Collect ticket ids per version.
	byVersion := map[int][]int{}
	name := ""
	for _, run := range exp.Runs {
		name = run.WorkflowName
		if run.TicketID != nil {
			byVersion[run.WorkflowVersion] = append(byVersion[run.WorkflowVersion], *run.TicketID)
		} else {
			byVersion[run.WorkflowVersion] = byVersion[run.WorkflowVersion] // ensure key present
		}
	}
	// Baseline metrics.
	_, baseMetrics, err := fn(name, baseline, byVersion[baseline])
	if err != nil {
		return nil, err
	}

	// Distinct versions in matrix order.
	seen := map[int]bool{}
	var versions []int
	for _, w := range exp.Matrix.Workflows {
		if !seen[w.Version] {
			seen[w.Version] = true
			versions = append(versions, w.Version)
		}
	}

	out := &ExperimentDelta{ExperimentID: exp.ID, Baseline: baseline}
	for _, v := range versions {
		raw, metrics, err := fn(name, v, byVersion[v])
		if err != nil {
			return nil, err
		}
		deltas := map[string]float64{}
		for k, val := range metrics {
			if base, ok := baseMetrics[k]; ok {
				deltas[k] = val - base
			}
		}
		out.Columns = append(out.Columns, DeltaColumn{
			WorkflowVersion: v,
			Scorecard:       raw,
			Deltas:          deltas,
		})
	}
	return out, nil
}
```

- [ ] **Step 4: Run to green:**

```bash
go test ./agent/api/experiments/ -run TestComputeDelta
```
Expected: PASS.

- [ ] **Step 5: Add the `GetExperimentDelta` handler method** in `agent/api/experiments/handler.go` (reads `:experiment_id` + `?baseline=`, loads the experiment, calls `ComputeDelta` with the injected `ScorecardFunc` set via `SetScorecardFunc`):

```go
func (h *Handler) SetScorecardFunc(fn ScorecardFunc) { h.scorecard = fn }

func (h *Handler) GetExperimentDelta(w http.ResponseWriter, r *http.Request) {
	id := experimentIDFrom(r)
	exp, ok, err := h.store.GetExperiment(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	baseline := exp.Matrix.Workflows[0].Version
	if q := r.URL.Query().Get("baseline"); q != "" {
		if b, e := strconv.Atoi(q); e == nil {
			baseline = b
		}
	}
	delta, err := ComputeDelta(exp, baseline, h.scorecard)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, delta)
}
```
Add `scorecard ScorecardFunc` field + `"strconv"` import.

- [ ] **Step 6: Add `ScorecardForTickets` to the experiment store and wire it as the `ScorecardFunc`.** The scorecards package has no ticket-set-filtered rollup (its only method is `Scorecard(name, versions)`, keyed by `(workflow_name, workflow_version)` — plan 13), so this workstream computes the ticket-scoped rollup itself. Add to the `experiments.Store` interface (Task 3) and implement in `atc/db` (Task 4 factory, alongside the other squirrel queries):

```go
// ScorecardForTickets returns a scorecard-shaped rollup for one workflow
// version restricted to a set of ticket ids (an experiment's runs), over
// agent_run_metrics / agent_cost_ledger / agent_outcomes. Returns the raw
// JSON (for the delta column display) and a flat map of the numeric fields
// (for diffing). Empty ticketIDs yields a zero rollup (no runs yet).
ScorecardForTickets(workflowName string, version int, ticketIDs []int) (json.RawMessage, map[string]float64, error)
```

The `atc/db` implementation mirrors the scorecards aggregate SQL recipe (plan 13 Task 3/4/5: `FILTER (WHERE …)` for the ok/failed/error split, cost/turns per ticket from `agent_cost_ledger`, findings from `agent_reviews.proven_count+observation_count`, merge/human-touch from a LEFT JOIN on `agent_outcomes`) but adds `AND m.ticket_id = ANY($ticketIDs)` to every table's WHERE and keys on the single `version` rather than a versions CSV. Return the same numeric field names scorecards uses (`cost_usd`, `merge_rate`, `gate_pass_rate`, `findings_per_ticket`, `turns`, …) so delta columns are labeled consistently with the standalone scorecard page. Then in `atc/api/handler.go`: `experimentsServer.SetScorecardFunc(experimentStore.ScorecardForTickets)` (the same `db.NewAgentExperimentFactory(dbConn)` value already constructed for the handler in Task 8, Step 6 — no scorecards handle needed here). Add route constant `GetAgentExperimentDelta` (GET `/api/v1/agent/experiments/:experiment_id/delta`, viewer) with the usual wrappa/roles entries.

- [ ] **Step 7: Verify wrappa + build:**

```bash
ginkgo --focus="handles each route" ./atc/wrappa/ && go build ./atc/... ./agent/...
```
Expected: PASS.

- [ ] **Step 8: Commit:**

```bash
git add agent/api/experiments/delta.go agent/api/experiments/delta_test.go agent/api/experiments/handler.go atc/routes.go atc/api/handler.go atc/wrappa/api_auth_wrappa.go atc/api/accessor/roles.go
git commit -m "feat(agent): experiment scorecard-delta view over the scorecards rollup" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 11: `fly agent experiments` — run/list/show with delta print

**Files:**
- Create: `fly/commands/agent_experiments.go`
- Modify: `fly/commands/agent.go` (`Experiments AgentExperimentsCommand` field)
- Modify: `go-concourse/concourse/agent.go` + `client.go` (`CreateAgentExperiment`/`ListAgentExperiments`/`GetAgentExperiment`/`GetAgentExperimentDelta`)
- Test: `fly/integration/agent_experiments_test.go`

`fly agent experiments run` posts a matrix from `-c case -c case`, `-w name:version`, `-r reps`; `fly agent experiments show <id> [--delta --baseline V]` prints status and the scorecard delta table. The confirmation line uses `displayhelpers.Succeedf` (added in Task 6, Step 0).

- [ ] **Step 1: Write the failing integration spec `fly/integration/agent_experiments_test.go`:**

```go
package integration_test

import (
	"net/http"
	"os/exec"

	"github.com/concourse/concourse/agent/api/experiments"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("fly agent experiments", func() {
	Describe("run", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(ghttp.CombineHandlers(
				ghttp.VerifyRequest("POST", "/api/v1/agent/experiments"),
				ghttp.RespondWithJSONEncoded(http.StatusOK, experiments.Experiment{ID: 5, Name: "e5", Status: "pending"}),
			))
		})
		It("posts the matrix and prints the experiment id", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "experiments", "run",
				"--name", "e5", "-c", "case-a", "-w", "standard-dev:3", "-w", "standard-dev:4", "-r", "2")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			Eventually(sess).Should(gexec.Exit(0))
			Expect(sess.Out).To(gbytes.Say("created experiment e5"))
		})
	})

	Describe("show --delta", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/experiments/5"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, experiments.Experiment{ID: 5, Name: "e5", Status: "complete"}),
				),
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/experiments/5/delta", "baseline=3"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, experiments.ExperimentDelta{
						ExperimentID: 5, Baseline: 3,
						Columns: []experiments.DeltaColumn{
							{WorkflowVersion: 3, Deltas: map[string]float64{}},
							{WorkflowVersion: 4, Deltas: map[string]float64{"cost_usd": -3.0}},
						},
					}),
				),
			)
		})
		It("prints the delta table", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "experiments", "show", "5", "--delta", "--baseline", "3")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			Eventually(sess).Should(gexec.Exit(0))
			Expect(sess.Out).To(gbytes.Say("cost_usd"))
			Expect(sess.Out).To(gbytes.Say("-3"))
		})
	})
})
```

- [ ] **Step 2: Run to verify it fails:**

```bash
ginkgo ./fly/integration/ --focus="fly agent experiments"
```
Expected: `Unknown command` (non-zero exit).

- [ ] **Step 3: Add the go-concourse methods** (`go-concourse/concourse/agent.go`, mirroring Task 6's send pattern) for `CreateAgentExperiment(exp experiments.Experiment)`, `GetAgentExperiment(id int)`, `ListAgentExperiments()`, `GetAgentExperimentDelta(id, baseline int)`; add to the `Client` interface.

- [ ] **Step 4: Write `fly/commands/agent_experiments.go`:**

```go
package commands

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/concourse/concourse/agent/api/experiments"
	"github.com/concourse/concourse/fly/commands/internal/displayhelpers"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/fly/ui"
	"github.com/fatih/color"
)

type AgentExperimentsCommand struct {
	Run  AgentExperimentsRunCommand  `command:"run" description:"Create and queue an experiment matrix"`
	List AgentExperimentsListCommand `command:"list" description:"List experiments"`
	Show AgentExperimentsShowCommand `command:"show" description:"Show one experiment (optionally its scorecard delta)"`
}

type AgentExperimentsRunCommand struct {
	Name        string   `long:"name" required:"true" description:"Experiment name"`
	Description string   `long:"description" description:"Freeform description"`
	Cases       []string `short:"c" long:"case" required:"true" description:"Benchmark case name (repeatable)"`
	Workflows   []string `short:"w" long:"workflow" required:"true" description:"workflow-name:version (repeatable)"`
	Repetitions int      `short:"r" long:"repetitions" default:"1" description:"Runs per combination"`
}

func (c *AgentExperimentsRunCommand) Execute(args []string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}
	var refs []experiments.WorkflowRef
	for _, w := range c.Workflows {
		parts := strings.SplitN(w, ":", 2)
		if len(parts) != 2 {
			return fmt.Errorf("--workflow must be name:version, got %q", w)
		}
		v, err := strconv.Atoi(parts[1])
		if err != nil {
			return fmt.Errorf("bad workflow version in %q: %w", w, err)
		}
		refs = append(refs, experiments.WorkflowRef{Name: parts[0], Version: v})
	}
	out, err := target.Client().CreateAgentExperiment(experiments.Experiment{
		Name:        c.Name,
		Description: c.Description,
		Matrix: experiments.Matrix{
			Cases: c.Cases, Workflows: refs, Repetitions: c.Repetitions,
		},
	})
	if err != nil {
		return err
	}
	displayhelpers.Succeedf("created experiment %s (id %d), status %s", out.Name, out.ID, out.Status)
	return nil
}

type AgentExperimentsListCommand struct{}

func (c *AgentExperimentsListCommand) Execute(args []string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}
	exps, err := target.Client().ListAgentExperiments()
	if err != nil {
		return err
	}
	table := ui.Table{Headers: ui.TableRow{
		{Contents: "id", Color: color.New(color.Bold)},
		{Contents: "name", Color: color.New(color.Bold)},
		{Contents: "status", Color: color.New(color.Bold)},
	}}
	for _, e := range exps {
		table.Data = append(table.Data, ui.TableRow{
			{Contents: fmt.Sprintf("%d", e.ID)}, {Contents: e.Name}, {Contents: e.Status},
		})
	}
	return table.Render(Fly.PrintTableHeaders)
}

type AgentExperimentsShowCommand struct {
	Args struct {
		ID int `positional-arg-name:"experiment-id"`
	} `positional-args:"yes" required:"yes"`
	Delta    bool `long:"delta" description:"Print the scorecard delta across variants"`
	Baseline int  `long:"baseline" description:"Baseline workflow version for the delta"`
}

func (c *AgentExperimentsShowCommand) Execute(args []string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}
	exp, err := target.Client().GetAgentExperiment(c.Args.ID)
	if err != nil {
		return err
	}
	fmt.Printf("experiment %d %q — %s\n", exp.ID, exp.Name, exp.Status)
	if !c.Delta {
		return nil
	}
	delta, err := target.Client().GetAgentExperimentDelta(c.Args.ID, c.Baseline)
	if err != nil {
		return err
	}
	metricKeys := map[string]bool{}
	for _, col := range delta.Columns {
		for k := range col.Deltas {
			metricKeys[k] = true
		}
	}
	headers := ui.TableRow{{Contents: "metric", Color: color.New(color.Bold)}}
	for _, col := range delta.Columns {
		headers = append(headers, ui.TableCell{Contents: fmt.Sprintf("v%d(Δ)", col.WorkflowVersion), Color: color.New(color.Bold)})
	}
	table := ui.Table{Headers: headers}
	for k := range metricKeys {
		row := ui.TableRow{{Contents: k}}
		for _, col := range delta.Columns {
			row = append(row, ui.TableCell{Contents: fmt.Sprintf("%+.2f", col.Deltas[k])})
		}
		table.Data = append(table.Data, row)
	}
	return table.Render(Fly.PrintTableHeaders)
}
```

- [ ] **Step 5: Register on `AgentCommand`** (`fly/commands/agent.go`), additive:

```go
Experiments AgentExperimentsCommand `command:"experiments" description:"Run and compare agent workflow experiments"`
```

- [ ] **Step 6: Run to green:**

```bash
ginkgo ./fly/integration/ --focus="fly agent experiments"
```
Expected: PASS.

- [ ] **Step 7: Commit:**

```bash
git add fly/commands/agent_experiments.go fly/commands/agent.go go-concourse/concourse/agent.go go-concourse/concourse/client.go fly/integration/agent_experiments_test.go
git commit -m "feat(fly): fly agent experiments run/list/show with scorecard delta" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

**Milestone 1 is now separately shippable:** benchmark cases can be mined and stored, an experiment matrix runs as budget-admitted pipeline runs via the dispatcher, and `fly agent experiments show --delta` prints the scorecard delta across workflow versions.

---

## Milestone 2 — Process intelligence

### Task 12: Defect→ticket linking convention + `agent_reviews.defect_link` migration

Calibration's missed-issue rate needs a lightweight way to link an escaped defect back to the ticket/review that should have caught it — "agreed before data accrues" (charter). This is a distinct sign-off note (it touches `agent_reviews`, harvest-step's owned table) plus one additive migration.

**Files:**
- Modify: `docs/superpowers/plans/agentic-platform/00-shared-contracts.md` (insert `### 1.12.1` after §1.12.2, append §11 note)
- Create: `atc/db/migration/migrations/1773106103_add_defect_link_to_agent_reviews.up.sql`
- Create: `atc/db/migration/migrations/1773106103_add_defect_link_to_agent_reviews.down.sql`
- Modify: `atc/db/migration/legacy_upgrade_test.go` (`jetbridgeHeadMigration` → 1773106103 if lower)

- [ ] **Step 1: Insert the §1.12.1 addendum** in the contracts doc (after §1.12.2):

````markdown
### 1.12.1 Defect→ticket linking convention — owner: **process-intel-experiments** (2026-07-08; additive to §1.10, harvest-step signed off; affects: scorecards)

**Missed-issue linking [DECIDED HERE — before data accrues]:** an escaped defect is recorded by adding one additive column to `agent_reviews`:

```sql
ALTER TABLE agent_reviews ADD COLUMN defect_link INTEGER;  -- NULL = no known escaped defect;
                                                           -- else the ticket_id of the FOLLOW-UP ticket
                                                           -- filed for a defect this review should have caught.
CREATE INDEX agent_reviews_defect_link ON agent_reviews (defect_link) WHERE defect_link IS NOT NULL;
```

A human (or the retrospective agent) sets `defect_link` on the review row of the ticket that *shipped* the defect, pointing at the follow-up/bug ticket. **Missed-issue rate** for a workflow version = `count(reviews with defect_link) / count(merged reviews)` over the window. The link is set via `SetAgentReviewDefectLink(reviewBuildID int, repo, commit string, defectTicketID int) error` on `reviews.Store` (additive method; the DB factory updates by the existing `(build_id, repo, commit_sha)` key). No new UI in v1 — it is set through `fly` (Task 20's retrospective can propose it) or a direct API call reusing `SubmitAgentFeedback`'s auth tier; the analytics query reads the column. This is deliberately coarse (small team): one boolean-ish signal per review, not a defect taxonomy.
````

- [ ] **Step 2: Append the §11 note:**

```markdown
- 2026-07-08 (process-intel-experiments, additive to §1.10; harvest-step owner sign-off): added §1.12.1 — defect→ticket linking convention (additive agent_reviews.defect_link column + partial index + reviews.Store.SetAgentReviewDefectLink), defining missed-issue-rate = reviews-with-defect-link / merged-reviews. Set via fly/API, read by calibration analytics. Affects: scorecards.
```

- [ ] **Step 3: Write `1773106103_add_defect_link_to_agent_reviews.up.sql`:**

```sql
ALTER TABLE agent_reviews ADD COLUMN defect_link INTEGER;
CREATE INDEX agent_reviews_defect_link ON agent_reviews (defect_link) WHERE defect_link IS NOT NULL;
```

- [ ] **Step 4: Write `1773106103_add_defect_link_to_agent_reviews.down.sql`:**

```sql
DROP INDEX agent_reviews_defect_link;
ALTER TABLE agent_reviews DROP COLUMN defect_link;
```

- [ ] **Step 5: Bump `jetbridgeHeadMigration`** to `1773106103` (only if currently lower).

- [ ] **Step 6: Verify migrations round-trip:**

```bash
go test ./atc/db/migration/ -run TestMigration -count=1
```
Expected: PASS.

- [ ] **Step 7: Add `SetAgentReviewDefectLink` to `reviews.Store`** + `atc/db/agent_reviews_factory.go` (additive UPDATE by `(build_id, repo, commit_sha)`). Failing test first in `atc/db/agent_reviews_factory_test.go`:

```go
It("sets and reads back a defect link", func() {
	factory := db.NewAgentReviewsFactory(dbConn)
	// (assume an existing review row from the suite's setup helper)
	err := factory.SetAgentReviewDefectLink(existingBuildID, "tdmtrader/concourse", existingCommit, 123)
	Expect(err).NotTo(HaveOccurred())
	// read back via a new store method or a direct query in the suite
})
```
Implementation:

```go
func (f *agentReviewsFactory) SetAgentReviewDefectLink(buildID int, repo, commit string, defectTicketID int) error {
	_, err := psql.Update("agent_reviews").
		Set("defect_link", defectTicketID).
		Where(sq.Eq{"build_id": buildID, "repo": repo, "commit_sha": commit}).
		RunWith(f.conn).Exec()
	return err
}
```
Add the method to the `AgentReviewsFactory` interface + `reviews.Store` and regenerate fakes: `go generate ./atc/db/... && go generate ./agent/api/reviews/...`.

- [ ] **Step 8: Run to green + build:**

```bash
ginkgo --focus="defect link" ./atc/db/ && go build ./atc/... ./agent/...
```
Expected: PASS.

- [ ] **Step 9: Commit:**

```bash
git add docs/superpowers/plans/agentic-platform/00-shared-contracts.md atc/db/migration/migrations/1773106103_* atc/db/migration/legacy_upgrade_test.go atc/db/agent_reviews_factory.go atc/db/agent_reviews_factory_test.go atc/db/dbfakes/ agent/api/reviews/
git commit -m "feat(agent): defect-link convention for missed-issue calibration" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 13: `agent/intel` — finding analytics (findings per repo/version, recurring classes, catches-migrate-leftward)

**Files:**
- Create: `agent/intel/types.go`
- Create: `agent/intel/analytics.go` (the read-only query library + a `Queryer` seam)
- Test: `agent/intel/analytics_test.go`

`agent/intel` is a read-only analytics library. To stay testable without a live DB (matching `agent/api/reviews`' plain-`testing` style) it depends on a narrow `Queryer` interface it defines; the `atc/db` implementation (Task 16) runs the real SQL. Tests inject an in-memory `Queryer`.

- [ ] **Step 1: Write `agent/intel/types.go`** (contracts §2.10):

```go
package intel

// FindingAnalytics answers "findings per ticket per repo & workflow version"
// and "which finding classes recur (automation candidates)" plus the
// catches-migrate-leftward series (spec §10).
type FindingAnalytics struct {
	// PerVersion: findings-per-ticket for each workflow version, with counts.
	PerVersion []VersionFindings `json:"per_version"`
	// Recurring: finding categories ranked by frequency — automation candidates.
	Recurring []RecurringClass `json:"recurring"`
	// Leftward: the catches-migrate-leftward series over time buckets.
	Leftward []LeftwardPoint `json:"leftward"`
}

type VersionFindings struct {
	WorkflowName     string  `json:"workflow_name"`
	WorkflowVersion  int     `json:"workflow_version"`
	TicketCount      int     `json:"ticket_count"`
	FindingCount     int     `json:"finding_count"`
	FindingsPerTicket float64 `json:"findings_per_ticket"`
}

type RecurringClass struct {
	Category      string `json:"category"`      // agent_reviews finding category
	Count         int    `json:"count"`
	DistinctRepos int    `json:"distinct_repos"`
}

type LeftwardPoint struct {
	Bucket            string  `json:"bucket"`             // e.g. "2026-06"
	FindingsPerTicket float64 `json:"findings_per_ticket"`
	EscapedDefects    int     `json:"escaped_defects"`   // reviews with defect_link in the bucket
}

// Calibration answers false-positive and missed-issue rates.
type Calibration struct {
	EvaluatedFindings int     `json:"evaluated_findings"`
	FalsePositiveRate float64 `json:"false_positive_rate"` // (false_positive+noisy+overly_strict)/evaluated
	MergedReviews     int     `json:"merged_reviews"`
	MissedIssues      int     `json:"missed_issues"`       // reviews with defect_link
	MissedIssueRate   float64 `json:"missed_issue_rate"`   // missed/merged
}

// Friction is the 2-3 frozen flight-recorder signatures.
type Friction struct {
	Signatures []FrictionSignature `json:"signatures"`
}

type FrictionSignature struct {
	Name        string  `json:"name"`         // "failing_test_loop" | "tool_error_rate" | "turn_burn"
	Description string  `json:"description"`
	Value       float64 `json:"value"`        // the aggregate metric (rate or count)
	SampleSize  int     `json:"sample_size"`  // number of run-metrics rows contributing
}
```

- [ ] **Step 2: Write the failing test `agent/intel/analytics_test.go`:**

```go
package intel_test

import (
	"testing"

	"github.com/concourse/concourse/agent/intel"
)

// fakeQueryer returns canned aggregates.
type fakeQueryer struct {
	versionFindings []intel.VersionFindings
	recurring       []intel.RecurringClass
	leftward        []intel.LeftwardPoint
}

func (q fakeQueryer) FindingsByVersion(f intel.Filter) ([]intel.VersionFindings, error) {
	return q.versionFindings, nil
}
func (q fakeQueryer) RecurringCategories(f intel.Filter) ([]intel.RecurringClass, error) {
	return q.recurring, nil
}
func (q fakeQueryer) LeftwardSeries(f intel.Filter) ([]intel.LeftwardPoint, error) {
	return q.leftward, nil
}
func (q fakeQueryer) VerdictCounts(f intel.Filter) (map[string]int, error) { return nil, nil }
func (q fakeQueryer) MergedReviewCount(f intel.Filter) (int, error)        { return 0, nil }
func (q fakeQueryer) DefectLinkCount(f intel.Filter) (int, error)          { return 0, nil }
func (q fakeQueryer) FrictionAggregates(f intel.Filter) (loops, toolErr, turnBurn float64, sample int, err error) {
	return 0, 0, 0, 0, nil
}

func TestFindingsComputesPerTicketRate(t *testing.T) {
	q := fakeQueryer{
		versionFindings: []intel.VersionFindings{
			{WorkflowName: "wf", WorkflowVersion: 3, TicketCount: 4, FindingCount: 10},
		},
	}
	a := intel.NewAnalyzer(q)
	got, err := a.Findings(intel.Filter{})
	if err != nil {
		t.Fatalf("Findings: %v", err)
	}
	if len(got.PerVersion) != 1 || got.PerVersion[0].FindingsPerTicket != 2.5 {
		t.Fatalf("PerVersion = %+v", got.PerVersion)
	}
}
```

- [ ] **Step 3: Run to see it fail:**

```bash
go test ./agent/intel/ -run TestFindings
```
Expected: build failure — `intel.NewAnalyzer`/`Filter`/`Queryer` undefined.

- [ ] **Step 4: Write `agent/intel/analytics.go`:**

```go
package intel

// Filter narrows every analytics query.
type Filter struct {
	Repo         string
	WorkflowName string
	SinceUnix    int64
	UntilUnix    int64
}

// Queryer is the read-only aggregate seam. atc/db implements it with real
// SQL over agent_reviews/agent_feedback/agent_run_metrics/agent_cost_ledger/
// agent_outcomes; tests inject an in-memory double.
type Queryer interface {
	FindingsByVersion(Filter) ([]VersionFindings, error)
	RecurringCategories(Filter) ([]RecurringClass, error)
	LeftwardSeries(Filter) ([]LeftwardPoint, error)
	VerdictCounts(Filter) (map[string]int, error)
	MergedReviewCount(Filter) (int, error)
	DefectLinkCount(Filter) (int, error)
	// FrictionAggregates returns the three frozen signature raw values plus
	// the number of run-metrics rows that contributed.
	FrictionAggregates(Filter) (loops, toolErr, turnBurn float64, sample int, err error)
}

type Analyzer struct{ q Queryer }

func NewAnalyzer(q Queryer) *Analyzer { return &Analyzer{q: q} }

func (a *Analyzer) Findings(f Filter) (*FindingAnalytics, error) {
	pv, err := a.q.FindingsByVersion(f)
	if err != nil {
		return nil, err
	}
	for i := range pv {
		if pv[i].TicketCount > 0 {
			pv[i].FindingsPerTicket = float64(pv[i].FindingCount) / float64(pv[i].TicketCount)
		}
	}
	rec, err := a.q.RecurringCategories(f)
	if err != nil {
		return nil, err
	}
	left, err := a.q.LeftwardSeries(f)
	if err != nil {
		return nil, err
	}
	return &FindingAnalytics{PerVersion: pv, Recurring: rec, Leftward: left}, nil
}
```

- [ ] **Step 5: Run to green:**

```bash
go test ./agent/intel/ -run TestFindings
```
Expected: PASS.

- [ ] **Step 6: Commit:**

```bash
git add agent/intel/types.go agent/intel/analytics.go agent/intel/analytics_test.go
git commit -m "feat(agent): intel finding-analytics library (per-version rate, recurring, leftward)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 14: `agent/intel` — calibration (false-positive + missed-issue rates)

**Files:**
- Modify: `agent/intel/analytics.go` (`Calibration` method)
- Test: `agent/intel/analytics_test.go` (calibration cases)

FP rate = `(false_positive + noisy + overly_strict) / evaluated findings` from six-verdict feedback (`ci-agent/schema` verdicts, now in `agent/schema`). Missed-issue rate = `defect_link count / merged review count` (§1.12.1).

- [ ] **Step 1: Add the failing test:**

```go
func TestCalibrationRates(t *testing.T) {
	q := calibQueryer{
		verdicts:      map[string]int{"accurate": 6, "false_positive": 2, "noisy": 1, "overly_strict": 1},
		mergedReviews: 20,
		defectLinks:   3,
	}
	a := intel.NewAnalyzer(q)
	got, err := a.Calibration(intel.Filter{})
	if err != nil {
		t.Fatalf("Calibration: %v", err)
	}
	if got.EvaluatedFindings != 10 {
		t.Fatalf("evaluated = %d, want 10", got.EvaluatedFindings)
	}
	if got.FalsePositiveRate != 0.4 { // (2+1+1)/10
		t.Fatalf("fp rate = %v, want 0.4", got.FalsePositiveRate)
	}
	if got.MissedIssueRate != 0.15 { // 3/20
		t.Fatalf("missed rate = %v, want 0.15", got.MissedIssueRate)
	}
}

// calibQueryer embeds the no-op fakeQueryer and overrides calibration methods.
type calibQueryer struct {
	fakeQueryer
	verdicts      map[string]int
	mergedReviews int
	defectLinks   int
}

func (q calibQueryer) VerdictCounts(intel.Filter) (map[string]int, error) { return q.verdicts, nil }
func (q calibQueryer) MergedReviewCount(intel.Filter) (int, error)        { return q.mergedReviews, nil }
func (q calibQueryer) DefectLinkCount(intel.Filter) (int, error)          { return q.defectLinks, nil }
```

- [ ] **Step 2: Run to see it fail:**

```bash
go test ./agent/intel/ -run TestCalibration
```
Expected: build failure — `Analyzer.Calibration` undefined.

- [ ] **Step 3: Add `Calibration` to `agent/intel/analytics.go`:**

```go
func (a *Analyzer) Calibration(f Filter) (*Calibration, error) {
	verdicts, err := a.q.VerdictCounts(f)
	if err != nil {
		return nil, err
	}
	evaluated := 0
	for _, n := range verdicts {
		evaluated += n
	}
	fp := verdicts["false_positive"] + verdicts["noisy"] + verdicts["overly_strict"]
	merged, err := a.q.MergedReviewCount(f)
	if err != nil {
		return nil, err
	}
	missed, err := a.q.DefectLinkCount(f)
	if err != nil {
		return nil, err
	}
	c := &Calibration{
		EvaluatedFindings: evaluated,
		MergedReviews:     merged,
		MissedIssues:      missed,
	}
	if evaluated > 0 {
		c.FalsePositiveRate = float64(fp) / float64(evaluated)
	}
	if merged > 0 {
		c.MissedIssueRate = float64(missed) / float64(merged)
	}
	return c, nil
}
```

- [ ] **Step 4: Run to green:**

```bash
go test ./agent/intel/ -run TestCalibration
```
Expected: PASS.

- [ ] **Step 5: Commit:**

```bash
git add agent/intel/analytics.go agent/intel/analytics_test.go
git commit -m "feat(agent): intel calibration (false-positive + missed-issue rates)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 15: `agent/intel` — friction mining (2-3 frozen flight-recorder signatures)

**Files:**
- Modify: `agent/intel/analytics.go` (`Friction` method + frozen signature names)
- Test: `agent/intel/analytics_test.go` (friction case)

The charter caps friction to "2-3 high-signal flight-recorder signatures (failing-test loops, tool-error rates)". Freeze exactly three, computed from `agent_run_metrics.event_counts` (§1.8): (1) `failing_test_loop` — mean count of repeated `gate.result`/`tool.result` with failed status per run (approximated as `event_counts["tool.result"]` flagged failed over runs); (2) `tool_error_rate` — `sum(error events) / sum(tool.call events)`; (3) `turn_burn` — 90th-percentile `turns` across runs. The `Queryer.FrictionAggregates` returns the three raw values; the Analyzer wraps them with descriptions.

- [ ] **Step 1: Add the failing test:**

```go
func TestFrictionSignatures(t *testing.T) {
	q := frictionQueryer{loops: 1.5, toolErr: 0.08, turnBurn: 42, sample: 30}
	a := intel.NewAnalyzer(q)
	got, err := a.Friction(intel.Filter{})
	if err != nil {
		t.Fatalf("Friction: %v", err)
	}
	if len(got.Signatures) != 3 {
		t.Fatalf("signatures = %d, want 3", len(got.Signatures))
	}
	byName := map[string]intel.FrictionSignature{}
	for _, s := range got.Signatures {
		byName[s.Name] = s
	}
	if byName["tool_error_rate"].Value != 0.08 || byName["turn_burn"].Value != 42 {
		t.Fatalf("signatures = %+v", got.Signatures)
	}
	if byName["failing_test_loop"].SampleSize != 30 {
		t.Fatalf("sample size not propagated: %+v", byName["failing_test_loop"])
	}
}

type frictionQueryer struct {
	fakeQueryer
	loops, toolErr, turnBurn float64
	sample                   int
}

func (q frictionQueryer) FrictionAggregates(intel.Filter) (float64, float64, float64, int, error) {
	return q.loops, q.toolErr, q.turnBurn, q.sample, nil
}
```

- [ ] **Step 2: Run to see it fail:**

```bash
go test ./agent/intel/ -run TestFriction
```
Expected: build failure — `Analyzer.Friction` undefined.

- [ ] **Step 3: Add `Friction` to `agent/intel/analytics.go`:**

```go
// Frozen friction signature names (charter: exactly 2-3 high-signal ones).
const (
	SigFailingTestLoop = "failing_test_loop"
	SigToolErrorRate   = "tool_error_rate"
	SigTurnBurn        = "turn_burn"
)

func (a *Analyzer) Friction(f Filter) (*Friction, error) {
	loops, toolErr, turnBurn, sample, err := a.q.FrictionAggregates(f)
	if err != nil {
		return nil, err
	}
	return &Friction{Signatures: []FrictionSignature{
		{Name: SigFailingTestLoop, Description: "mean repeated failing test/gate results per run", Value: loops, SampleSize: sample},
		{Name: SigToolErrorRate, Description: "error events per tool call", Value: toolErr, SampleSize: sample},
		{Name: SigTurnBurn, Description: "p90 turns per run", Value: turnBurn, SampleSize: sample},
	}}, nil
}
```

- [ ] **Step 4: Run to green:**

```bash
go test ./agent/intel/ -run TestFriction
```
Expected: PASS.

- [ ] **Step 5: Commit:**

```bash
git add agent/intel/analytics.go agent/intel/analytics_test.go
git commit -m "feat(agent): intel friction mining (failing-test-loop, tool-error-rate, turn-burn)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 16: `atc/db` intel Queryer implementation + analytics API routes

**Files:**
- Create: `atc/db/agent_intel_queryer.go` (implements `intel.Queryer` with real SQL)
- Test: `atc/db/agent_intel_queryer_test.go`
- Create: `agent/api/intel/handler.go` (three GET handlers wrapping `intel.Analyzer`)
- Test: `agent/api/intel/handler_test.go`
- Modify: `atc/routes.go`, `atc/api/handler.go`, `atc/wrappa/api_auth_wrappa.go`, `atc/api/accessor/roles.go` (`GetAgentFindingAnalytics`, `GetAgentCalibration`, `GetAgentFriction`, all viewer)

The Queryer runs read-only aggregate SQL. Findings-per-version joins `agent_run_metrics` (workflow_name/version, ticket_id) to `agent_reviews` findings via ticket_id; recurring categories unnest `agent_reviews.review->'observations'`/`'proven_issues'` `category`; leftward buckets by month; verdicts from `agent_feedback.verdict`; merged from `agent_outcomes.merge_state IN ('merged','merged_with_fixes')`; friction from `agent_run_metrics.event_counts`.

- [ ] **Step 1: Write the failing spec `atc/db/agent_intel_queryer_test.go`** (Ginkgo, real DB — seeds a couple of rows then asserts aggregates):

```go
package db_test

import (
	"time"

	"github.com/concourse/concourse/agent/intel"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentIntelQueryer", func() {
	var q intel.Queryer

	BeforeEach(func() {
		q = db.NewAgentIntelQueryer(dbConn)
	})

	It("computes merged-review and defect-link counts", func() {
		// The suite's agent-review + outcome seed helper inserts:
		//  - 2 merged tickets each with a review row
		//  - 1 of those reviews carries defect_link = <some ticket id>
		merged, err := q.MergedReviewCount(intel.Filter{})
		Expect(err).NotTo(HaveOccurred())
		Expect(merged).To(BeNumerically(">=", 2))

		missed, err := q.DefectLinkCount(intel.Filter{})
		Expect(err).NotTo(HaveOccurred())
		Expect(missed).To(BeNumerically(">=", 1))
	})

	It("returns verdict counts from agent_feedback", func() {
		counts, err := q.VerdictCounts(intel.Filter{})
		Expect(err).NotTo(HaveOccurred())
		Expect(counts).To(HaveKey("accurate"))
	})

	It("applies the SinceUnix/UntilUnix window to the counts", func() {
		// Backdate a merged review + defect_link + feedback verdict to a fixed
		// old instant, then confirm an all-time filter counts them but a window
		// that starts AFTER them excludes them — proving applyWindow/the raw
		// $since/$until binds actually reach the SQL (the routes advertise
		// ?since/until and the handler parses them; before this fix the SQL
		// ignored the window and every metric was all-time).
		old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		seedWindowedIntelRow(dbConn, old) // suite helper below: inserts 1 merged review + defect_link + 1 'accurate' feedback dated `old`

		allTimeMerged, err := q.MergedReviewCount(intel.Filter{})
		Expect(err).NotTo(HaveOccurred())

		// A window opening one second after the backdated row must not see it.
		since := old.Add(time.Second).Unix()
		windowedMerged, err := q.MergedReviewCount(intel.Filter{SinceUnix: since})
		Expect(err).NotTo(HaveOccurred())
		Expect(windowedMerged).To(BeNumerically("<", allTimeMerged),
			"since-window must exclude the backdated merged review")

		windowedMissed, err := q.DefectLinkCount(intel.Filter{SinceUnix: since})
		Expect(err).NotTo(HaveOccurred())
		allTimeMissed, err := q.DefectLinkCount(intel.Filter{})
		Expect(err).NotTo(HaveOccurred())
		Expect(windowedMissed).To(BeNumerically("<", allTimeMissed))

		windowedVerdicts, err := q.VerdictCounts(intel.Filter{SinceUnix: since})
		Expect(err).NotTo(HaveOccurred())
		allTimeVerdicts, err := q.VerdictCounts(intel.Filter{})
		Expect(err).NotTo(HaveOccurred())
		Expect(windowedVerdicts["accurate"]).To(BeNumerically("<", allTimeVerdicts["accurate"]))
	})
})
```

(The seed helper is the existing `atc/db` agent-review test fixture; extend it in this task to insert one `agent_outcomes` merged row + one `defect_link` + one `agent_feedback` verdict, guarded so other specs are unaffected. Add a second helper `seedWindowedIntelRow(conn, at time.Time)` that inserts a merged review + `defect_link` + one `accurate` feedback row with `created_at`/`occurred_at` forced to `at` — the windowing spec above uses it to prove the `[since, until)` bound reaches every metric. Add the `time` import to the spec file.)

- [ ] **Step 2: Run to see it fail:**

```bash
ginkgo --focus="AgentIntelQueryer" ./atc/db/
```
Expected: compile failure — `db.NewAgentIntelQueryer` undefined.

- [ ] **Step 3: Write `atc/db/agent_intel_queryer.go`** (the SQL implementation; each method builds one squirrel query with the `Filter` applied as `WHERE` clauses; `since`/`until` compared against `created_at`/`occurred_at`):

```go
package db

import (
	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/agent/intel"
)

func NewAgentIntelQueryer(conn DbConn) intel.Queryer {
	return &agentIntelQueryer{conn: conn}
}

type agentIntelQueryer struct{ conn DbConn }

func applyWorkflow(b sq.SelectBuilder, alias string, f intel.Filter) sq.SelectBuilder {
	if f.WorkflowName != "" {
		b = b.Where(sq.Eq{alias + ".workflow_name": f.WorkflowName})
	}
	return b
}

// applyWindow bounds a query to the half-open interval [since, until) on the
// given timestamp column (e.g. "m.created_at", "r.created_at", "occurred_at").
// SinceUnix/UntilUnix are epoch seconds; 0 means unbounded on that end. Mirrors
// the GetAgentCostRollup precedent (occurred_at >= $since [AND < $until]) so
// every analytics metric respects the ?since=&until= route params instead of
// silently reporting all-time. to_timestamp() converts epoch seconds to the
// TIMESTAMPTZ the created_at/occurred_at columns store.
func applyWindow(b sq.SelectBuilder, tsColumn string, f intel.Filter) sq.SelectBuilder {
	if f.SinceUnix > 0 {
		b = b.Where(sq.GtOrEq{tsColumn: sq.Expr("to_timestamp(?)", f.SinceUnix)})
	}
	if f.UntilUnix > 0 {
		b = b.Where(sq.Lt{tsColumn: sq.Expr("to_timestamp(?)", f.UntilUnix)})
	}
	return b
}

func (q *agentIntelQueryer) FindingsByVersion(f intel.Filter) ([]intel.VersionFindings, error) {
	// findings = proven_issues + observations counts on reviews joined to
	// run-metrics rows carrying workflow_name/version + ticket_id.
	b := psql.Select(
		"m.workflow_name",
		"m.workflow_version",
		"COUNT(DISTINCT m.ticket_id) AS ticket_count",
		"COALESCE(SUM(r.proven_count + r.observation_count), 0) AS finding_count",
	).
		From("agent_run_metrics m").
		LeftJoin("agent_reviews r ON r.ticket_id = m.ticket_id").
		Where("m.ticket_id IS NOT NULL").
		Where("m.workflow_version IS NOT NULL").
		GroupBy("m.workflow_name", "m.workflow_version").
		OrderBy("m.workflow_name", "m.workflow_version")
	b = applyWorkflow(b, "m", f)
	b = applyWindow(b, "m.created_at", f)
	if f.Repo != "" {
		b = b.Where(sq.Eq{"r.repo": f.Repo})
	}
	rows, err := b.RunWith(q.conn).Query()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []intel.VersionFindings{}
	for rows.Next() {
		var v intel.VersionFindings
		if err := rows.Scan(&v.WorkflowName, &v.WorkflowVersion, &v.TicketCount, &v.FindingCount); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (q *agentIntelQueryer) RecurringCategories(f intel.Filter) ([]intel.RecurringClass, error) {
	// Unnest finding categories out of the stored review JSON.
	// $2/$3 bound the window on r.created_at (0 = unbounded on that end),
	// mirroring the GetAgentCostRollup since/until precedent.
	const raw = `
SELECT obj->>'category' AS category, COUNT(*) AS n, COUNT(DISTINCT r.repo) AS repos
FROM agent_reviews r,
     LATERAL jsonb_array_elements(COALESCE(r.review->'observations','[]'::jsonb)
                                  || COALESCE(r.review->'proven_issues','[]'::jsonb)) AS obj
WHERE ($1 = '' OR r.repo = $1)
  AND ($2 = 0 OR r.created_at >= to_timestamp($2))
  AND ($3 = 0 OR r.created_at <  to_timestamp($3))
GROUP BY obj->>'category'
ORDER BY n DESC
LIMIT 20`
	rows, err := q.conn.Query(raw, f.Repo, f.SinceUnix, f.UntilUnix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []intel.RecurringClass{}
	for rows.Next() {
		var c intel.RecurringClass
		if err := rows.Scan(&c.Category, &c.Count, &c.DistinctRepos); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (q *agentIntelQueryer) LeftwardSeries(f intel.Filter) ([]intel.LeftwardPoint, error) {
	// $1/$2 bound both CTEs to the [since, until) window (0 = unbounded on
	// that end), per the GetAgentCostRollup since/until precedent — without
	// this the leftward series ran over all history regardless of ?since/until.
	const raw = `
WITH per_month AS (
  SELECT to_char(m.created_at, 'YYYY-MM') AS bucket,
         COUNT(DISTINCT m.ticket_id)      AS tickets,
         COALESCE(SUM(r.proven_count + r.observation_count),0) AS findings
  FROM agent_run_metrics m
  LEFT JOIN agent_reviews r ON r.ticket_id = m.ticket_id
  WHERE m.ticket_id IS NOT NULL
    AND ($1 = 0 OR m.created_at >= to_timestamp($1))
    AND ($2 = 0 OR m.created_at <  to_timestamp($2))
  GROUP BY 1
),
defects AS (
  SELECT to_char(created_at,'YYYY-MM') AS bucket, COUNT(*) AS escaped
  FROM agent_reviews
  WHERE defect_link IS NOT NULL
    AND ($1 = 0 OR created_at >= to_timestamp($1))
    AND ($2 = 0 OR created_at <  to_timestamp($2))
  GROUP BY 1
)
SELECT p.bucket,
       CASE WHEN p.tickets>0 THEN p.findings::float/p.tickets ELSE 0 END,
       COALESCE(d.escaped,0)
FROM per_month p LEFT JOIN defects d ON d.bucket = p.bucket
ORDER BY p.bucket`
	rows, err := q.conn.Query(raw, f.SinceUnix, f.UntilUnix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []intel.LeftwardPoint{}
	for rows.Next() {
		var p intel.LeftwardPoint
		if err := rows.Scan(&p.Bucket, &p.FindingsPerTicket, &p.EscapedDefects); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (q *agentIntelQueryer) VerdictCounts(f intel.Filter) (map[string]int, error) {
	b := psql.Select("verdict", "COUNT(*)").
		From("agent_feedback").GroupBy("verdict")
	b = applyWindow(b, "created_at", f)
	rows, err := b.RunWith(q.conn).Query()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var v string
		var n int
		if err := rows.Scan(&v, &n); err != nil {
			return nil, err
		}
		out[v] = n
	}
	return out, rows.Err()
}

func (q *agentIntelQueryer) MergedReviewCount(f intel.Filter) (int, error) {
	var n int
	b := psql.Select("COUNT(*)").
		From("agent_reviews r").
		Join("agent_outcomes o ON o.ticket_id = r.ticket_id").
		Where(sq.Eq{"o.merge_state": []string{"merged", "merged_with_fixes"}}).
		Where("r.ticket_id IS NOT NULL")
	b = applyWindow(b, "r.created_at", f)
	err := b.RunWith(q.conn).QueryRow().Scan(&n)
	return n, err
}

func (q *agentIntelQueryer) DefectLinkCount(f intel.Filter) (int, error) {
	var n int
	b := psql.Select("COUNT(*)").From("agent_reviews").
		Where("defect_link IS NOT NULL")
	b = applyWindow(b, "created_at", f)
	err := b.RunWith(q.conn).QueryRow().Scan(&n)
	return n, err
}

func (q *agentIntelQueryer) FrictionAggregates(f intel.Filter) (loops, toolErr, turnBurn float64, sample int, err error) {
	// $2/$3 bound the window on created_at (0 = unbounded on that end), per the
	// GetAgentCostRollup since/until precedent — friction signatures respect
	// ?since/until instead of aggregating every run ever recorded.
	const raw = `
SELECT
  COALESCE(AVG( (event_counts->>'gate.result')::int ),0)         AS loops,
  CASE WHEN COALESCE(SUM((event_counts->>'tool.call')::int),0)>0
       THEN COALESCE(SUM((event_counts->>'error')::int),0)::float
            / SUM((event_counts->>'tool.call')::int)
       ELSE 0 END                                                 AS tool_err,
  COALESCE(percentile_cont(0.9) WITHIN GROUP (ORDER BY turns),0)  AS turn_burn,
  COUNT(*)                                                        AS sample
FROM agent_run_metrics
WHERE ($1 = '' OR workflow_name = $1)
  AND ($2 = 0 OR created_at >= to_timestamp($2))
  AND ($3 = 0 OR created_at <  to_timestamp($3))`
	err = q.conn.QueryRow(raw, f.WorkflowName, f.SinceUnix, f.UntilUnix).Scan(&loops, &toolErr, &turnBurn, &sample)
	return
}
```

Note: `agent_reviews.proven_count`/`observation_count` are existing denormalized columns (`reviews.StoredReview`); `event_counts->>'gate.result'` etc. read the §1.8 JSONB. `psql`/`DbConn.Query`/`QueryRow` are the package-`db` idioms used throughout `*_factory.go`. Every method applies the `Filter`'s `SinceUnix`/`UntilUnix` window (via `applyWindow` for squirrel builders, or `$since/$until` binds in the raw-SQL methods) against `created_at`/`occurred_at` — mirroring `GetAgentCostRollup`'s half-open `[since, until)` semantics (0 = unbounded on that end). Without this the routes advertise `?since=&until=` and the handler parses them, but the SQL ignored them and every metric was all-time.

- [ ] **Step 4: Run to green:**

```bash
ginkgo --focus="AgentIntelQueryer" ./atc/db/
```
Expected: PASS.

- [ ] **Step 5: Write the analytics HTTP handler `agent/api/intel/handler.go`:**

```go
package intel

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/concourse/concourse/agent/intel"
)

type Handler struct{ analyzer *intel.Analyzer }

func NewHandler(q intel.Queryer) *Handler { return &Handler{analyzer: intel.NewAnalyzer(q)} }

func filterFrom(r *http.Request) intel.Filter {
	q := r.URL.Query()
	f := intel.Filter{Repo: q.Get("repo"), WorkflowName: q.Get("workflow_name")}
	if s := q.Get("since"); s != "" {
		f.SinceUnix, _ = strconv.ParseInt(s, 10, 64)
	}
	if u := q.Get("until"); u != "" {
		f.UntilUnix, _ = strconv.ParseInt(u, 10, 64)
	}
	return f
}

func (h *Handler) GetFindings(w http.ResponseWriter, r *http.Request) {
	out, err := h.analyzer.Findings(filterFrom(r))
	writeOrErr(w, out, err)
}
func (h *Handler) GetCalibration(w http.ResponseWriter, r *http.Request) {
	out, err := h.analyzer.Calibration(filterFrom(r))
	writeOrErr(w, out, err)
}
func (h *Handler) GetFriction(w http.ResponseWriter, r *http.Request) {
	out, err := h.analyzer.Friction(filterFrom(r))
	writeOrErr(w, out, err)
}

func writeOrErr(w http.ResponseWriter, v any, err error) {
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
```

- [ ] **Step 6: Write `agent/api/intel/handler_test.go`** (plain Go, in-memory Queryer double — reuse the `fakeQueryer` shape from Task 13 or a local minimal one) asserting `GetCalibration` returns 200 and a body with `false_positive_rate`. Run:

```bash
go test ./agent/api/intel/
```
Expected: PASS.

- [ ] **Step 7: Add routes/handler/wrappa/roles.** Constants `GetAgentFindingAnalytics`/`GetAgentCalibration`/`GetAgentFriction`; entries GET `/api/v1/agent/analytics/findings`, `/calibration`, `/friction`; wire `intelServer := intelapi.NewHandler(db.NewAgentIntelQueryer(dbConn))` in `atc/api/handler.go` (import `intelapi "github.com/concourse/concourse/agent/api/intel"`); register the three `http.HandlerFunc`s; add all three to the `authorized` team-less wrappa group; add `accessor.Viewer` role entries.

- [ ] **Step 8: Verify wrappa + build:**

```bash
ginkgo --focus="handles each route" ./atc/wrappa/ && go build ./atc/... ./agent/...
```
Expected: PASS.

- [ ] **Step 9: Commit:**

```bash
git add atc/db/agent_intel_queryer.go atc/db/agent_intel_queryer_test.go agent/api/intel/ atc/routes.go atc/api/handler.go atc/wrappa/api_auth_wrappa.go atc/api/accessor/roles.go
git commit -m "feat(agent): intel Queryer SQL impl + findings/calibration/friction analytics API" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 17: Minimal Elm analytics view

**Files:**
- Create: `web/elm/src/AgentIntel/AgentIntel.elm` (a single page rendering findings/calibration/friction tables + the experiment delta)
- Create: `web/elm/src/Concourse/AgentIntel.elm` (decoders for `FindingAnalytics`/`Calibration`/`Friction`/`ExperimentDelta`)
- Test: `web/elm/tests/AgentIntelTests.elm`
- Modify: `web/elm/src/Routes.elm` (add an `AgentIntel` route `/agent/intel`), `web/elm/src/Message/Effects.elm` + `Message/Callback.elm` (fetch effects) — follow the `AgentReviews/AgentReviews.elm` page wiring exactly

Minimal, per charter ("minimal Elm views") — read-only tables, no charts library (CSP-safe: charts would need inline SVG we hand-draw; v1 is tables with counts, honoring the small-team "present counts, resist overreading" risk note).

- [ ] **Step 1: Write the failing decoder test `web/elm/tests/AgentIntelTests.elm`:**

```elm
module AgentIntelTests exposing (all)

import Concourse.AgentIntel as AI
import Expect
import Json.Decode as Decode
import Test exposing (Test, describe, test)


all : Test
all =
    describe "AgentIntel decoders"
        [ test "decodes calibration" <|
            \_ ->
                """{"evaluated_findings":10,"false_positive_rate":0.4,"merged_reviews":20,"missed_issues":3,"missed_issue_rate":0.15}"""
                    |> Decode.decodeString AI.calibrationDecoder
                    |> Result.map .falsePositiveRate
                    |> Expect.equal (Ok 0.4)
        , test "decodes friction signatures" <|
            \_ ->
                """{"signatures":[{"name":"turn_burn","description":"p90","value":42,"sample_size":30}]}"""
                    |> Decode.decodeString AI.frictionDecoder
                    |> Result.map (\f -> List.length f.signatures)
                    |> Expect.equal (Ok 1)
        ]
```

- [ ] **Step 2: Run to see it fail:**

```bash
cd web/elm && npx elm-test tests/AgentIntelTests.elm
```
Expected: compile failure — `Concourse.AgentIntel` module missing.

- [ ] **Step 3: Write `web/elm/src/Concourse/AgentIntel.elm`** (decoders mirroring the §2.10 JSON):

```elm
module Concourse.AgentIntel exposing
    ( Calibration
    , Friction
    , FrictionSignature
    , calibrationDecoder
    , frictionDecoder
    )

import Json.Decode as Decode exposing (Decoder)


type alias Calibration =
    { evaluatedFindings : Int
    , falsePositiveRate : Float
    , mergedReviews : Int
    , missedIssues : Int
    , missedIssueRate : Float
    }


calibrationDecoder : Decoder Calibration
calibrationDecoder =
    Decode.map5 Calibration
        (Decode.field "evaluated_findings" Decode.int)
        (Decode.field "false_positive_rate" Decode.float)
        (Decode.field "merged_reviews" Decode.int)
        (Decode.field "missed_issues" Decode.int)
        (Decode.field "missed_issue_rate" Decode.float)


type alias FrictionSignature =
    { name : String
    , description : String
    , value : Float
    , sampleSize : Int
    }


type alias Friction =
    { signatures : List FrictionSignature }


frictionDecoder : Decoder Friction
frictionDecoder =
    Decode.map Friction
        (Decode.field "signatures" (Decode.list signatureDecoder))


signatureDecoder : Decoder FrictionSignature
signatureDecoder =
    Decode.map4 FrictionSignature
        (Decode.field "name" Decode.string)
        (Decode.field "description" Decode.string)
        (Decode.field "value" Decode.float)
        (Decode.field "sample_size" Decode.int)
```

- [ ] **Step 4: Run to green:**

```bash
cd web/elm && npx elm-test tests/AgentIntelTests.elm
```
Expected: PASS.

- [ ] **Step 5: Write `web/elm/src/AgentIntel/AgentIntel.elm`** — a page module patterned on `AgentReviews/AgentReviews.elm`: `init` fires `FetchAgentCalibration`/`FetchAgentFriction` effects, `update` stores the decoded records, `view` renders two tables (calibration key/value rows with counts beside rates; friction signatures with sample sizes). Keep it under ~150 lines — no charts, per the minimal-view constraint. Wire the route `/agent/intel` in `Routes.elm`, the two `Effects` (HTTP GETs to `/api/v1/agent/analytics/calibration` and `/friction`) and their `Callback` handlers exactly as `AgentReviews` wires its fetch. (The full module body follows the `AgentReviews.elm` structure verbatim; substitute the calibration/friction decoders and table rendering.)

- [ ] **Step 6: Build the Elm bundle to confirm it compiles into the page:**

```bash
cd web && yarn build
```
Expected: build succeeds (bundle regenerated).

- [ ] **Step 7: Commit:**

```bash
git add web/elm/src/AgentIntel/ web/elm/src/Concourse/AgentIntel.elm web/elm/tests/AgentIntelTests.elm web/elm/src/Routes.elm web/elm/src/Message/Effects.elm web/elm/src/Message/Callback.elm web/public/
git commit -m "feat(web): minimal agent intel view (calibration + friction tables)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 18: Retrospective workflow definition + intel-context materializer

The retrospective is an `agent:` step run (not new execution machinery): a workflow definition whose single agent step reads the intel snapshot and files `origin:retrospective` proposal tickets via platform-mcp's `read_ticket`/`submit_spec` tools + `CreateAgentTicket`. This task lands (a) the seed workflow YAML and (b) a materializer (`RenderIntelMarkdown`) that renders the intel snapshot as markdown. That markdown is delivered to the agent as the retrospective ticket's **spec body** (Task 19 submits it via `tickets.Store.SubmitSpec`), which the agent reads through platform-mcp `read_ticket` in default `spec_delivery: mcp` mode — NOT as an `intel.md` workspace file (default mcp mode materializes no spec/plan files; contracts §3.2). The renderer just produces the markdown bytes; the trigger owns delivery.

**Files:**
- Create: `agent/retrospective/workflow.go` (the embedded seed definition YAML + `SeedDefinition() []byte`)
- Create: `agent/retrospective/context.go` (`RenderIntelMarkdown(FindingAnalytics, Calibration, Friction) []byte` — the snapshot markdown Task 19 delivers as the ticket's spec body)
- Test: `agent/retrospective/context_test.go`
- Create: `docs/agentic/retrospective-workflow.yml` (human-readable copy of the seed, imported via `fly agent workflows import`)

The proposal format is template-shaped (charter: "template-shaped proposals (lint rule, prompt amendment, dev-mcp gate, workflow edit)") — the prompt instructs the agent to file one ticket per proposal, each spec body following a fixed template.

- [ ] **Step 1: Write the failing test `agent/retrospective/context_test.go`:**

```go
package retrospective_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/intel"
	"github.com/concourse/concourse/agent/retrospective"
)

func TestRenderIntelMarkdownIncludesSignatures(t *testing.T) {
	md := retrospective.RenderIntelMarkdown(
		&intel.FindingAnalytics{
			Recurring: []intel.RecurringClass{{Category: "nil-deref", Count: 4, DistinctRepos: 2}},
		},
		&intel.Calibration{FalsePositiveRate: 0.4, MissedIssueRate: 0.1, EvaluatedFindings: 10},
		&intel.Friction{Signatures: []intel.FrictionSignature{{Name: "turn_burn", Value: 42, SampleSize: 30}}},
	)
	s := string(md)
	if !strings.Contains(s, "nil-deref") || !strings.Contains(s, "4") {
		t.Fatalf("recurring class not rendered:\n%s", s)
	}
	if !strings.Contains(s, "false-positive") || !strings.Contains(s, "0.4") {
		t.Fatalf("calibration not rendered:\n%s", s)
	}
	if !strings.Contains(s, "turn_burn") {
		t.Fatalf("friction not rendered:\n%s", s)
	}
}

func TestSeedDefinitionParses(t *testing.T) {
	if len(retrospective.SeedDefinition()) == 0 {
		t.Fatal("empty seed definition")
	}
}
```

- [ ] **Step 2: Run to see it fail:**

```bash
go test ./agent/retrospective/
```
Expected: build failure — package missing.

- [ ] **Step 3: Write `agent/retrospective/context.go`:**

```go
package retrospective

import (
	"fmt"
	"strings"

	"github.com/concourse/concourse/agent/intel"
)

// RenderIntelMarkdown produces the process-intelligence snapshot markdown the
// retrospective agent reads. Task 19's trigger delivers this as the retrospective
// ticket's spec body (via tickets.Store.SubmitSpec), so the agent reaches it
// through platform-mcp read_ticket in default spec_delivery: mcp mode — it is
// NOT written to an intel.md workspace file (default mcp mode materializes no
// spec/plan files; contracts §3.2). Deterministic; no LLM. Sections: recurring
// finding classes (automation candidates), calibration, friction signatures.
func RenderIntelMarkdown(fa *intel.FindingAnalytics, cal *intel.Calibration, fr *intel.Friction) []byte {
	var b strings.Builder
	b.WriteString("# Process Intelligence Snapshot\n\n")

	b.WriteString("## Recurring finding classes (automation candidates)\n\n")
	if fa != nil {
		for _, c := range fa.Recurring {
			b.WriteString(fmt.Sprintf("- **%s** — caught %d times across %d repos\n", c.Category, c.Count, c.DistinctRepos))
		}
	}
	b.WriteString("\n## Catches migrating leftward\n\n")
	if fa != nil {
		for _, p := range fa.Leftward {
			b.WriteString(fmt.Sprintf("- %s: %.2f findings/ticket, %d escaped defects\n", p.Bucket, p.FindingsPerTicket, p.EscapedDefects))
		}
	}
	b.WriteString("\n## Calibration\n\n")
	if cal != nil {
		b.WriteString(fmt.Sprintf("- false-positive rate: %.2f (over %d evaluated findings)\n", cal.FalsePositiveRate, cal.EvaluatedFindings))
		b.WriteString(fmt.Sprintf("- missed-issue rate: %.2f (%d/%d merged reviews)\n", cal.MissedIssueRate, cal.MissedIssues, cal.MergedReviews))
	}
	b.WriteString("\n## Friction signatures\n\n")
	if fr != nil {
		for _, s := range fr.Signatures {
			b.WriteString(fmt.Sprintf("- **%s**: %.2f (%s; n=%d)\n", s.Name, s.Value, s.Description, s.SampleSize))
		}
	}
	return []byte(b.String())
}
```

- [ ] **Step 4: Write `agent/retrospective/workflow.go`** (embedded seed; uses the §6 grammar; single agent step with platform sidecar; funded by platform credential — no `budget.ticket_usd` override needed since dispatch funds retrospective tickets from the platform credential per §1.13):

```go
package retrospective

import _ "embed"

//go:embed seed_workflow.yml
var seedWorkflow []byte

// SeedDefinition returns the retrospective workflow's YAML (schema §6). Import
// it via `fly agent workflows import` or the retrospective trigger's
// ensure-imported step. Name: "retrospective".
func SeedDefinition() []byte { return seedWorkflow }
```

- [ ] **Step 5: Write `agent/retrospective/seed_workflow.yml`** (embedded; a linear single-agent workflow that reads the snapshot via platform-mcp `read_ticket` — the trigger delivers it as the ticket's spec — and files proposals):

```yaml
schema_version: 1
name: retrospective
description: read process-intelligence snapshot, file origin:retrospective improvement tickets

defaults:
  model: claude-sonnet-4-5
  max_turns: 60

budget:
  ticket_usd: 5.0
  judge_usd: 0

sidecars:
  platform:
    image: ghcr.io/tdmtrader/mcp-platform:0.1.0
    role: platform

prompts:
  retrospect: |
    You are running the platform's retrospective. Read the ticket via
    platform-mcp: call read_ticket — its spec is the process-intelligence
    snapshot (recurring review findings, calibration rates, and friction
    signatures over the recent window). Read it from the returned spec's
    body_md.

    For each HIGH-SIGNAL, ACTIONABLE improvement (a recurring finding class that
    a lint rule or dev-mcp gate could catch; a prompt amendment that would cut a
    friction signature; a workflow-definition edit), file ONE ticket via
    platform-mcp: call submit_spec with a body in EXACTLY this template:

      ## Proposal type
      lint-rule | prompt-amendment | dev-mcp-gate | workflow-edit

      ## Evidence
      <the snapshot numbers from read_ticket's spec that justify this — e.g. "caught 4 times in review across 2 repos">

      ## Concrete change
      <the specific rule/prompt/gate/edit to make>

      ## Expected effect
      <which metric this should move: findings/ticket down, false-positive rate down, friction signature down>

    Do NOT propose vague or speculative changes. If the data does not support a
    concrete change, file nothing. You cannot merge anything — these are tickets
    for a human to review.

steps:
- agent: retrospect
  prompt: retrospect
  sidecars: [platform]
  budget_slice_usd: 5.0
  inputs: [workspace]

hitl:
  ask_timeout: park
  ask_timeout_seconds: 0

gate_policy:
  gates: []
  on_gate_failure: needs_review
```

- [ ] **Step 6: Write `docs/agentic/retrospective-workflow.yml`** — a byte-identical copy of the embedded seed (the human-editable source imported via fly; the embed is the fallback the trigger auto-imports).

- [ ] **Step 7: Run to green:**

```bash
go test ./agent/retrospective/
```
Expected: PASS.

- [ ] **Step 8: Commit:**

```bash
git add agent/retrospective/ docs/agentic/retrospective-workflow.yml
git commit -m "feat(agent): retrospective seed workflow + intel-context materializer" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 19: `retrospective_trigger` component — manual + recurring cadence

The charter: "manual-triggered cadence first, then recurring". Implement one trigger that (a) ensures the `retrospective` workflow is imported+live, (b) on demand or on a configurable interval, gathers the intel snapshot over a bounded trailing window (30 days — charter "mine a month"; the analytics SQL honors `Filter.SinceUnix`/`UntilUnix` per Task 16), and (c) files ONE `origin:retrospective` ticket that delivers the rendered intel snapshot as the ticket's **versioned spec** (via `tickets.Store.SubmitSpec`) so the dispatched retrospective agent reaches it through platform-mcp `read_ticket` in default `spec_delivery: mcp` mode — the ticket is then queued through `Transition` for the wave-4 dispatcher to render+run. (The snapshot is NOT stuffed into `Ticket.Body`: `read_ticket` returns the latest spec via `LatestSpec`, not the raw body, so a body-only snapshot would never reach the agent — contracts §3.2.) Recurring cadence is a web-node flag `--agent-retrospective-interval` (0 = manual only). A manual trigger arrives via `fly agent retrospective run` (Task 20) which POSTs a "run now" that the trigger honors on its next tick (or immediately, via a DB flag row).

**Files:**
- Create: `agent/retrospective/trigger.go`
- Create: `agent/retrospective/trigger_suite_test.go`, `agent/retrospective/trigger_test.go`
- Modify: `atc/component.go` (constant `ComponentRetrospectiveTrigger = "retrospective_trigger"`)
- Modify: `atc/atccmd/command.go` (flag `--agent-retrospective-interval` + component entry, interval-driven)
- Test: `agent/retrospective/trigger_test.go`

The trigger reuses `intel.Analyzer` (Task 13-15), the ticket store, and the workflow store. "Run now" is a request flag: the trigger writes to a small `agent_retrospective_requests`-free design — instead the manual path is the trigger's `RunOnce(ctx)` method, invoked directly by the fly-facing API handler (Task 20) rather than through the interval. The interval path calls the same `RunOnce`.

- [ ] **Step 1: Write `agent/retrospective/trigger_suite_test.go`:**

```go
package retrospective_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestRetrospectiveTrigger(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Retrospective Trigger Suite")
}
```

- [ ] **Step 2: Write the failing spec `agent/retrospective/trigger_test.go`:**

```go
package retrospective_test

import (
	"context"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/api/tickets/ticketsfakes"
	"github.com/concourse/concourse/agent/intel"
	"github.com/concourse/concourse/agent/retrospective"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflow/workflowfakes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// stubQueryer returns minimal non-empty intel.
type stubQueryer struct{}

func (stubQueryer) FindingsByVersion(intel.Filter) ([]intel.VersionFindings, error) {
	return []intel.VersionFindings{{WorkflowName: "wf", WorkflowVersion: 1, TicketCount: 2, FindingCount: 6}}, nil
}
func (stubQueryer) RecurringCategories(intel.Filter) ([]intel.RecurringClass, error) {
	return []intel.RecurringClass{{Category: "nil-deref", Count: 4, DistinctRepos: 2}}, nil
}
func (stubQueryer) LeftwardSeries(intel.Filter) ([]intel.LeftwardPoint, error) { return nil, nil }
func (stubQueryer) VerdictCounts(intel.Filter) (map[string]int, error) {
	return map[string]int{"accurate": 5, "false_positive": 5}, nil
}
func (stubQueryer) MergedReviewCount(intel.Filter) (int, error) { return 10, nil }
func (stubQueryer) DefectLinkCount(intel.Filter) (int, error)   { return 1, nil }
func (stubQueryer) FrictionAggregates(intel.Filter) (float64, float64, float64, int, error) {
	return 1, 0.1, 30, 12, nil
}

var _ = Describe("Trigger", func() {
	var (
		ticketStore *ticketsfakes.FakeStore
		wfStore     *workflowfakes.FakeStore
		trig        *retrospective.Trigger
	)

	BeforeEach(func() {
		ticketStore = new(ticketsfakes.FakeStore)
		wfStore = new(workflowfakes.FakeStore)
		wfStore.LiveReturns(&workflow.Definition{Name: "retrospective", Version: 1, Live: true}, true, nil)
		ticketStore.CreateReturns(55, nil)
		trig = retrospective.NewTrigger(stubQueryer{}, ticketStore, wfStore, "retrospective-agent")
	})

	It("files one queued retrospective ticket delivering the intel snapshot as the spec", func() {
		Expect(trig.RunOnce(context.Background(), "manual")).To(Succeed())

		Expect(ticketStore.CreateCallCount()).To(Equal(1))
		created := ticketStore.CreateArgsForCall(0)
		Expect(created.Origin).To(Equal("retrospective"))
		Expect(created.WorkflowName).To(Equal("retrospective"))

		// The snapshot is delivered as the ticket's VERSIONED SPEC (so
		// platform-mcp read_ticket surfaces it in default mcp mode), NOT the
		// raw Ticket.Body — read_ticket returns LatestSpec, not the body.
		Expect(ticketStore.SubmitSpecCallCount()).To(Equal(1))
		specTicketID, spec := ticketStore.SubmitSpecArgsForCall(0)
		Expect(specTicketID).To(Equal(55))
		Expect(spec.Body).To(ContainSubstring("nil-deref")) // intel snapshot in the spec

		id, from, to, _ := ticketStore.TransitionArgsForCall(0)
		Expect(id).To(Equal(55))
		Expect(from).To(Equal(tickets.StateDraft))
		Expect(to).To(Equal(tickets.StateQueued))
	})

	It("auto-imports the seed workflow when none is live", func() {
		wfStore.LiveReturns(nil, false, nil)
		wfStore.ImportReturns(&workflow.Definition{Name: "retrospective", Version: 1}, nil)

		Expect(trig.RunOnce(context.Background(), "manual")).To(Succeed())
		Expect(wfStore.ImportCallCount()).To(Equal(1))
	})
})
```

- [ ] **Step 3: Run to see it fail:**

```bash
ginkgo ./agent/retrospective/
```
Expected: build failure — `retrospective.Trigger`/`NewTrigger`/`workflowfakes` missing.

- [ ] **Step 4: Ensure `workflow.Store` has a counterfeiter directive** (workflow-store landed it; if `workflowfakes` is absent, `go generate ./agent/workflow/...`).

- [ ] **Step 5: Write `agent/retrospective/trigger.go`:**

```go
package retrospective

import (
	"context"
	"fmt"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/intel"
	"github.com/concourse/concourse/agent/workflow"
)

// Trigger files a retrospective ticket: it ensures the retrospective workflow
// is live (auto-importing the embedded seed if not), gathers the intel
// snapshot, and creates ONE origin:retrospective ticket whose body carries the
// rendered snapshot, queued through Transition for the dispatcher to run.
type Trigger struct {
	analyzer   *intel.Analyzer
	tickets    tickets.Store
	workflows  workflow.Store
	createdBy  string
}

func NewTrigger(q intel.Queryer, ticketStore tickets.Store, wf workflow.Store, createdBy string) *Trigger {
	return &Trigger{
		analyzer:  intel.NewAnalyzer(q),
		tickets:   ticketStore,
		workflows: wf,
		createdBy: createdBy,
	}
}

// RunOnce files one retrospective ticket. cause is recorded in the ticket
// title ("manual" or "scheduled").
func (t *Trigger) RunOnce(ctx context.Context, cause string) error {
	logger := lagerctx.FromContext(ctx).Session("retrospective-trigger")

	if _, ok, err := t.workflows.Live("retrospective"); err != nil {
		return err
	} else if !ok {
		if _, err := t.workflows.Import("retrospective", SeedDefinition(), t.createdBy); err != nil {
			return fmt.Errorf("importing retrospective seed: %w", err)
		}
		logger.Info("imported-retrospective-seed")
	}

	// Mine a bounded trailing window (30 days), not all history — the charter
	// ("mine a month") and the Step-3 prose both call for a recent window, and
	// the analytics SQL now honors Filter.SinceUnix/UntilUnix (Task 16). An
	// empty intel.Filter{} would report all-time and drown recent signal.
	f := intel.Filter{SinceUnix: time.Now().Add(-30 * 24 * time.Hour).Unix()}
	fa, err := t.analyzer.Findings(f)
	if err != nil {
		return err
	}
	cal, err := t.analyzer.Calibration(f)
	if err != nil {
		return err
	}
	fr, err := t.analyzer.Friction(f)
	if err != nil {
		return err
	}
	snapshot := RenderIntelMarkdown(fa, cal, fr)

	ticketID, err := t.tickets.Create(&tickets.Ticket{
		Title:        fmt.Sprintf("Retrospective (%s)", cause),
		Body:         "Process-intelligence retrospective — the intel snapshot is the ticket's spec (read it via platform-mcp read_ticket).",
		Origin:       "retrospective",
		Repo:         "tdmtrader/concourse",
		WorkflowName: "retrospective",
		CreatedBy:    t.createdBy,
	})
	if err != nil {
		return err
	}
	// Deliver the intel snapshot as the ticket's VERSIONED SPEC (not Ticket.Body):
	// the retrospective agent runs in default `spec_delivery: mcp` mode, so it
	// reaches the snapshot only through platform-mcp `read_ticket`, which returns
	// the latest spec via tickets.Store.LatestSpec (contracts §3.2). Putting the
	// snapshot in Ticket.Body would NOT surface it to the agent — read_ticket
	// returns the spec, not the raw body. SubmitSpec is the single spec writer
	// (contracts §2.1); it stores the snapshot as spec version 1.
	if _, err := t.tickets.SubmitSpec(ticketID, tickets.Spec{
		Title:       fmt.Sprintf("Retrospective (%s)", cause),
		Body:        string(snapshot),
		SubmittedBy: t.createdBy,
	}); err != nil {
		return fmt.Errorf("submitting retrospective spec: %w", err)
	}
	// Attribution rides on Ticket.CreatedBy (set above at Create time);
	// TransitionMeta has no By field (ticket-core §2.1.1).
	if err := t.tickets.Transition(ticketID, tickets.StateDraft, tickets.StateQueued,
		tickets.TransitionMeta{}); err != nil {
		return err
	}
	logger.Info("filed-retrospective-ticket", lager.Data{"ticket": ticketID, "cause": cause})
	return nil
}

// Run implements the RunnableComponent interface for the scheduled cadence.
func (t *Trigger) Run(ctx context.Context) error {
	return t.RunOnce(ctx, "scheduled")
}
```

The import block above already carries `code.cloudfoundry.org/lager/v3` (for `lager.Data`) and `time` (for the 30-day trailing window).

- [ ] **Step 6: Run to green:**

```bash
ginkgo ./agent/retrospective/
```
Expected: PASS.

- [ ] **Step 7: Add the component constant** (`atc/component.go`):

```go
ComponentRetrospectiveTrigger = "retrospective_trigger"
```

- [ ] **Step 8: Wire the interval-driven component + flag** in `atc/atccmd/command.go`. Add the flag to the command struct group (near the other agent flags): `AgentRetrospectiveInterval time.Duration `long:"agent-retrospective-interval" description:"How often to run the retrospective (0 = manual only)"``. Only register the component when the interval > 0:

```go
if cmd.AgentRetrospectiveInterval > 0 {
	components = append(components, RunnableComponent{
		Component: atc.Component{Name: atc.ComponentRetrospectiveTrigger},
		Runnable: retrospective.NewTrigger(
			db.NewAgentIntelQueryer(dbConn),
			db.NewAgentTicketsFactory(dbConn),
			db.NewAgentWorkflowsFactory(dbConn),
			"retrospective-agent",
		),
		Interval: cmd.AgentRetrospectiveInterval,
	})
}
```
Add import `"github.com/concourse/concourse/agent/retrospective"`. Store a shared `retrospectiveTrigger` variable so Task 20's API handler can call `RunOnce` for the manual path (assign the `NewTrigger(...)` to a package-scope-reachable field on the command, constructed unconditionally so manual runs work even when the interval is 0).

- [ ] **Step 9: Build:**

```bash
go build ./atc/... ./agent/...
```
Expected: clean.

- [ ] **Step 10: Commit:**

```bash
git add agent/retrospective/trigger.go agent/retrospective/trigger_suite_test.go agent/retrospective/trigger_test.go atc/component.go atc/atccmd/command.go
git commit -m "feat(atc): retrospective_trigger component (manual + scheduled cadence)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 20: `RunAgentRetrospective` route + `fly agent retrospective run`

**Files:**
- Create: `agent/api/intel/retrospective_handler.go` (a `POST` handler calling the shared trigger's `RunOnce(ctx, "manual")`)
- Modify: `atc/routes.go`, `atc/api/handler.go`, `atc/wrappa/api_auth_wrappa.go`, `atc/api/accessor/roles.go` (`RunAgentRetrospective` POST `/api/v1/agent/retrospective`, member)
- Create: `fly/commands/agent_retrospective.go`
- Modify: `fly/commands/agent.go` (`Retrospective AgentRetrospectiveCommand` field)
- Modify: `go-concourse/concourse/agent.go` + `client.go` (`RunAgentRetrospective()`)
- Test: `fly/integration/agent_retrospective_test.go`

The `RunAgentRetrospective` route (POST `/api/v1/agent/retrospective`, member) is registered in the contract by Task 1 (the §1.12.2 addendum + §4.2 fifth row + §11 amendment) — this task only wires it. The fly confirmation line uses `displayhelpers.Succeedf` (added in Task 6, Step 0).

The handler holds a reference to the unconditionally-constructed `*retrospective.Trigger` from Task 19 (injected in `atc/api/handler.go` — the command passes it into `NewHandler`). It runs synchronously and returns `{"filed": true}` (the ticket then flows through dispatch).

- [ ] **Step 1: Write the failing integration spec `fly/integration/agent_retrospective_test.go`:**

```go
package integration_test

import (
	"net/http"
	"os/exec"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("fly agent retrospective run", func() {
	BeforeEach(func() {
		atcServer.AppendHandlers(ghttp.CombineHandlers(
			ghttp.VerifyRequest("POST", "/api/v1/agent/retrospective"),
			ghttp.RespondWithJSONEncoded(http.StatusOK, map[string]bool{"filed": true}),
		))
	})
	It("triggers a retrospective and confirms it was filed", func() {
		flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "retrospective", "run")
		sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())
		Eventually(sess).Should(gexec.Exit(0))
		Expect(sess.Out).To(gbytes.Say("retrospective filed"))
	})
})
```

- [ ] **Step 2: Run to verify it fails:**

```bash
ginkgo ./fly/integration/ --focus="fly agent retrospective"
```
Expected: `Unknown command` (non-zero exit).

- [ ] **Step 3: Write `agent/api/intel/retrospective_handler.go`:**

```go
package intel

import (
	"context"
	"encoding/json"
	"net/http"
)

// RetrospectiveRunner is the seam over *retrospective.Trigger (avoids an
// import cycle: agent/api/intel must not import agent/retrospective, which
// imports intel — so the command wires a func).
type RetrospectiveRunner func(ctx context.Context, cause string) error

type RetrospectiveHandler struct{ run RetrospectiveRunner }

func NewRetrospectiveHandler(run RetrospectiveRunner) *RetrospectiveHandler {
	return &RetrospectiveHandler{run: run}
}

func (h *RetrospectiveHandler) Run(w http.ResponseWriter, r *http.Request) {
	if err := h.run(r.Context(), "manual"); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"filed": true})
}
```

- [ ] **Step 4: Wire the route + handler.** In `atc/api/handler.go` construct `retrospectiveHandler := intelapi.NewRetrospectiveHandler(retrospectiveTrigger.RunOnce)` (the command passes the trigger in). Add route constant `RunAgentRetrospective` (POST `/api/v1/agent/retrospective`), register `http.HandlerFunc(retrospectiveHandler.Run)`, add to the authorized wrappa group, add `accessor.Member` role entry.

- [ ] **Step 5: Add go-concourse `RunAgentRetrospective()`** (`agent.go` + `client.go` interface):

```go
func (c *client) RunAgentRetrospective() error {
	return c.connection.Send(internal.Request{
		RequestName: atc.RunAgentRetrospective,
	}, nil)
}
```

- [ ] **Step 6: Write `fly/commands/agent_retrospective.go`:**

```go
package commands

import (
	"github.com/concourse/concourse/fly/commands/internal/displayhelpers"
	"github.com/concourse/concourse/fly/rc"
)

type AgentRetrospectiveCommand struct {
	Run AgentRetrospectiveRunCommand `command:"run" description:"File a retrospective ticket now"`
}

type AgentRetrospectiveRunCommand struct{}

func (c *AgentRetrospectiveRunCommand) Execute(args []string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}
	if err := target.Client().RunAgentRetrospective(); err != nil {
		return err
	}
	displayhelpers.Succeedf("retrospective filed as a queued ticket")
	return nil
}
```

- [ ] **Step 7: Register on `AgentCommand`** (`fly/commands/agent.go`):

```go
Retrospective AgentRetrospectiveCommand `command:"retrospective" description:"Run the process retrospective"`
```

- [ ] **Step 8: Run to green + build:**

```bash
ginkgo ./fly/integration/ --focus="fly agent retrospective" && ginkgo --focus="handles each route" ./atc/wrappa/ && go build ./atc/... ./agent/...
```
Expected: PASS.

- [ ] **Step 9: Commit:**

```bash
git add agent/api/intel/retrospective_handler.go atc/routes.go atc/api/handler.go atc/wrappa/api_auth_wrappa.go atc/api/accessor/roles.go fly/commands/agent_retrospective.go fly/commands/agent.go go-concourse/concourse/agent.go go-concourse/concourse/client.go fly/integration/agent_retrospective_test.go
git commit -m "feat(fly): fly agent retrospective run + manual trigger route" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

**Milestone 2 is now separately shippable:** finding analytics, calibration, and friction read the tables prior waves fill; the retrospective files template-shaped `origin:retrospective` improvement tickets into the same human-merged queue on a manual or scheduled cadence.

---

## Execution notes

### Running this workstream's tests

Per CLAUDE.md — PostgreSQL must be running (`pg_isready`); never use `--race` (parallel compilation breaks).

- **Go unit packages (no DB):** `go test ./agent/api/experiments/ ./agent/intel/ ./agent/api/intel/ ./agent/retrospective/` and `cd ci-agent && go test ./gapgen/`. Fast (<10s each).
- **atc/db factory + queryer (DB):** `ginkgo --focus="AgentExperimentFactory" ./atc/db/` and `ginkgo --focus="AgentIntelQueryer" ./atc/db/`. The `atc/db` suite uses the shared template DB — if you see `database "testdb_template" already exists`, another run is in flight; wait.
- **Component specs (Ginkgo, fakes only):** `ginkgo ./agent/experiments/ ./agent/retrospective/`.
- **Route exhaustiveness:** `ginkgo --focus="handles each route" ./atc/wrappa/` after every route-adding task (5, 8, 10, 12, 16, 20) — the "handles each route" spec panics on any route missing from the auth switch.
- **fly integration (builds the fly binary against a ghttp mock ATC):** `ginkgo ./fly/integration/ --focus="fly agent"` covers benchmarks/experiments/retrospective. The mock ATC version must match `versions.go` (currently `0.1.0`).
- **Elm:** `cd web/elm && npx elm-test tests/AgentIntelTests.elm`; full bundle `cd web && yarn build`.
- **Migrations:** `go test ./atc/db/migration/ -run TestMigration -count=1` after Tasks 2 and 12 (up/down round-trip on the embedded files).
- **Whole-workstream sweep before calling it done:** `make test-unit` (includes atc + fly + go-concourse Ginkgo suites) + `go build ./... && cd agent && go build ./... && cd ../ci-agent && go build ./...`.

### Live verification on theborg (recommended before shipping each milestone)

Both milestones exercise real dispatch → jetbridge pods, so a live smoke test on theborg (kube-context `theborg` → https://theborg.home:6443) validates the end-to-end path fakes cannot:

- **M1:** create two live workflow versions of `standard-dev`, mine one benchmark case from a real merged PR via the `extract-benchmark` skill, then `fly -t theborg agent experiments run --name smoke -c <case> -w standard-dev:N -w standard-dev:M -r 1`. Watch the dispatcher render two runs (`fly runs -p <rendered-template>`), confirm they complete, then `fly agent experiments show <id> --delta` prints a non-empty delta. **Use a THROWAWAY namespace** (NOT `cicd`/`concourse` — those are live workloads); set `--agent-daily-budget-usd` to a small value first and confirm over-cap cells stay `pending` (the load-bearing cap behavior). `t.Cleanup`-style: delete the experiment's rendered pipelines and the throwaway namespace after.
- **M2:** with a few weeks of real `agent_reviews`/`agent_feedback`/`agent_outcomes` rows present, `fly -t theborg agent retrospective run` and confirm one `origin:retrospective` ticket appears with the intel snapshot in its body and dispatches. Verify the filed proposal tickets are human-gated (they land as ordinary tickets; nothing auto-merges — the scope-out guarantee).
- The retrospective/experiment pods are platform-funded (§1.13): confirm the `agent-platform-credential` secret exists in the target namespace before running, and that ledger rows land with `source='retrospective'` (experiments' agent steps use the per-run user credential; the retrospective agent step is platform-funded).

### Rollback notes for the risky diffs

- **Migrations 1773106100–103 are additive** (three new tables + one nullable column + indexes). Down-migrations drop exactly what they created; no existing table is altered destructively. The `agent_reviews.defect_link` column (Task 12) is nullable with no default change to existing rows — rollback is a clean `DROP COLUMN`. **Merge-order hazard:** the migrator is version-pointer based; if this branch's `1773106100–103` deploys before a lower-numbered wave-4 migration (dispatch/outcomes `1773106090`+), that lower migration will never apply. Coordinate merge order so all lower-numbered migrations land first, or hold the deploy until every wave-1-4 migration is merged. Keep `jetbridgeHeadMigration` at the highest landed number.
- **`experiment_runner` and `retrospective_trigger` are independent RunnableComponents.** Disabling either (removing its entry from the `components` slice in `atc/atccmd/command.go`, or setting `--agent-retrospective-interval 0`) stops that behavior with zero effect on any other component or on run creation. The experiment runner only *creates and queues tickets*; if it misbehaves, tickets sit `queued` and can be manually abandoned via `fly` — nothing is pushed or merged.
- **The retrospective can only file tickets** (scope-out: no auto-apply). The worst-case failure is noise (low-value proposal tickets), mitigated by the template-shaped-proposal prompt and the "file nothing if the data doesn't support a concrete change" guardrail; a human triages every proposal. To halt entirely: `--agent-retrospective-interval 0` and stop calling `fly agent retrospective run`.
- **The analytics API and Elm view are read-only** over existing tables — no write path, so a query bug surfaces as a wrong number on a dashboard, never as data corruption. Reverting `agent/intel` + `agent/api/intel` + the Elm page removes the surface with no schema impact.
- **The experiment runner's budget admission is the load-bearing spend control** (charter risk: "experiment batches multiply spend"). If `budget.Checker.GlobalDailyRemaining()` ever returns a wrong (too-high) remaining, batches could overspend — the `--agent-daily-budget-usd` cap is the backstop; set it conservatively on theborg and verify the "over-cap cells stay pending" spec (Task 9) is green before enabling large matrices.

### Milestone split at execution time

Per the charter and scaffolding decision 8, M1 (Tasks 1–11) and M2 (Tasks 12–20) are separately shippable. If executing as two forge tracks: M1 depends only on wave-4 dispatch/pipeline-runs/scorecards; M2 additionally depends on the `agent_reviews.defect_link` column (Task 12) and reads `agent_outcomes`/`agent_feedback` populated by real runs — M2's analytics are meaningful only once M1 (or normal dispatch) has produced run history, so land M1 first and let data accrue before trusting M2's numbers (charter risk: "friction signatures need run volume to mean anything — ship the queries, expect tuning").

---

## §11 Amendment log (this plan)

- **2026-07-09 (design-review fixes F9, F10 — no contract-name changes; all names stay identical to 00-shared-contracts.md):**
  - **F9 (analytics window never applied):** the routes advertise `?since=&until=`, `intel.Filter` carries `SinceUnix`/`UntilUnix`, and the handler parses them, but Task 16's Queryer SQL ignored the window (every metric was all-time) and Task 19's retrospective trigger passed an empty `intel.Filter{}` (mined all history) — contradicting the Step-3 prose and the charter's "mine a month". Fix: added an `applyWindow(b, tsColumn, f)` helper mirroring the existing `applyWorkflow`, and called it in every squirrel Queryer method (`FindingsByVersion` on `m.created_at`, `VerdictCounts` on `created_at`, `MergedReviewCount` on `r.created_at`, `DefectLinkCount` on `created_at`) against `created_at`/`occurred_at`; bound `$since/$until` into the three raw-SQL methods (`RecurringCategories`, `LeftwardSeries`, `FrictionAggregates`), following the `GetAgentCostRollup` half-open `[since, until)` precedent (0 = unbounded, `to_timestamp()` for the epoch-seconds→TIMESTAMPTZ conversion). Task 19's trigger now builds a bounded trailing-30-day filter (`SinceUnix: time.Now().Add(-30*24*time.Hour).Unix()`) instead of `intel.Filter{}`. Added a Task-16 Ginkgo spec ("applies the SinceUnix/UntilUnix window to the counts") that backdates a merged review + `defect_link` + `accurate` feedback to 2020 and asserts a since-window excludes them while an all-time filter counts them (new suite helper `seedWindowedIntelRow`; `time` import added to the spec).
  - **F10 (retrospective intel delivery seam):** Task 18 built `RenderIntelMarkdown` as "the read-only intel.md workspace input" and the seed prompt said "Read intel.md in your workspace," but nothing produced an `intel.md` file — Task 19 put the snapshot in `Ticket.Body` (which is NOT the versioned Spec that `read_ticket`/`LatestSpec` returns; in default `spec_delivery: mcp` mode no file is materialized). Fix: (a) amended the seed prompt to "Read the ticket via platform-mcp: call `read_ticket` — its spec is the snapshot … `body_md`"; (b) Task 19's trigger now delivers the snapshot via `tickets.Store.SubmitSpec(ticketID, tickets.Spec{Title, Body: snapshot, SubmittedBy})` (spec version 1) between `Create` and `Transition`, with a lightweight pointer `Ticket.Body`, so `read_ticket` surfaces it (contracts §3.2); (c) corrected Task 18's framing (intro, `context.go` file line, `RenderIntelMarkdown` doc comment) and Step-5's seed description to describe spec-delivery rather than a non-existent `intel.md` workspace file. Updated the Task-19 spec ("files one queued retrospective ticket delivering the intel snapshot as the spec") to assert `SubmitSpecCallCount() == 1` and that the submitted `spec.Body` contains the snapshot, instead of checking `created.Body`. Contract-visible names used verbatim from ticket-core/platform-mcp: `tickets.Store.SubmitSpec`, `tickets.Spec{Title, Body, SubmittedBy}`, `read_ticket`, `submit_spec`, `spec_delivery: mcp`.
