# Experiments and Workflow-First UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make stochastic workflows empirically improvable and operationally legible: compare compatible versions against pinned snapshot fixtures and evaluators, then center the product UI on workflows, durable runs, snapshot lineage, projections, and experiments.

**Architecture:** Experiments are durable scheduling records over the generic binder, not a parallel executor. Fixtures are named input-port-to-snapshot bindings. Variants are immutable compatible full-workflow targets or extracted internal `function_id` targets. Each repetition creates an ordinary durable workflow run; a pinned evaluator workflow consumes mapped fixture inputs and candidate outputs and emits `measurements/v1`. Rollups compute distributions from immutable cells and invocation telemetry. Elm reuses the existing pipeline DAG/build, review, ticket, cost, and diff components under workflow/run/snapshot detail routes.

**Tech Stack:** Go/PostgreSQL experiment scheduler, generic workflow binder, run metrics, HTTP/Fly, Elm 0.19.

## Global Constraints

- Do not implement the historical `step_kind` benchmark tables, restore runner, stub executor, primary-metric switch, or ticket/build/plan identity model.
- Experiments never mutate live promotion. Promotion is a separate authorized human action.
- A cell pins fixture snapshots, candidate definition, evaluator definition, repetition index, and budget before execution.
- Report distributions and failures, not only a winner. Preserve invalid outputs and platform errors as measured outcomes.
- “Preserve invalid outputs” means retaining the contract-failure record, validation diagnostics, telemetry, and an optional access-controlled short-lived quarantine archive. Invalid bytes never receive a typed snapshot row, lineage edge, or workflow output binding.
- Operational and experimental workflow runs share execution/storage but remain filterable views.

---

### Task 1: Persist experiments, variants, fixtures, and cells

**Files:**
- Create: `atc/db/migration/migrations/1773106110_create_agent_experiments.up.sql`
- Create: `atc/db/migration/migrations/1773106110_create_agent_experiments.down.sql`
- Create: `agent/experiment/types.go`
- Create: `agent/experiment/types_test.go`
- Create: `atc/db/agent_experiments_factory.go`
- Create: `atc/db/agent_experiments_factory_test.go`
- Modify: `atc/db/migration/legacy_upgrade_test.go`

- [ ] Write contract tests for experiment states, full-workflow versus internal-node targets, compatible target signatures, variant/control rules, normal versus negative-control fixtures, negative-control metric assertions, fixture port completeness, evaluator mappings, repetition bounds, budgets, and immutable started definitions.
- [ ] Write DB tests for create/update-before-start, atomic start freeze, cell matrix allocation, unique fixture/variant/repetition, restart-safe claims, cancellation, and complete history after workflow/pipeline deletion.
- [ ] Run focused tests and confirm failure.
- [ ] Create `agent_experiments`, `agent_experiment_variants`, `agent_experiment_fixtures`, `agent_experiment_fixture_bindings`, `agent_experiment_control_assertions`, `agent_experiment_cells`, and `agent_experiment_evaluations`. Each variant stores target kind (`workflow` or `function`), definition ID, optional stable `function_id`, and frozen target signature hash. Each fixture stores role `normal` or `negative_control`; assertions store metric name, comparator (`lt`, `lte`, `gt`, `gte`, or `between`), and the required one or two numeric thresholds.
- [ ] Store fixture bindings as normalized rows `(fixture_id, port_name, snapshot_id)`, not opaque repo bundles. Creating a binding atomically creates a non-expiring `fixture` retention claim; deleting an unstarted fixture releases it, while start freezes both binding and claim for experiment history. Store evaluator mapping as normalized `(evaluator_port, source_direction, source_port)` rows.
- [ ] Allocate all cells in the start transaction and use `FOR UPDATE SKIP LOCKED` claims.
- [ ] Advance `jetbridgeHeadMigration` to `1773106110` and pass legacy-to-head plus down/up migration coverage.
- [ ] Re-run tests and commit `feat(experiment): persist snapshot-based experiment matrices`.

### Task 2: Schedule candidate cells through the generic binder

**Files:**
- Create: `agent/experiment/runner.go`
- Create: `agent/experiment/runner_test.go`
- Create: `agent/experiment/runnerfakes/`
- Modify: `atc/atccmd/command.go`
- Modify: `atc/atccmd/command_test.go`

- [ ] Write tests for deterministic claim order, configured concurrency, repetition creation, cell idempotency keys, origin metadata, per-cell/global budget admission, cancellation, binder failure, and process restart.
- [ ] Prove every cell calls `workflowrun.Binder` with the exact same fixture snapshot IDs, pinned candidate version, and target `FunctionID` (empty for a whole workflow).
- [ ] Run `go test ./agent/experiment -run Runner -count=1` and confirm failure.
- [ ] Implement the runner as a bounded polling component. Cell idempotency key is `experiment:<id>:fixture:<id>:variant:<id>:rep:<n>:candidate`. Internal-node variants use the node-extraction harness from the workflow plan and the same durable workflow-run model.
- [ ] Record candidate durable workflow-run ID and transition cell states without depending on ephemeral pipeline rows.
- [ ] Add `--agent-experiment-runner-enabled`, interval, and max-concurrency flags plus health reporting.
- [ ] Re-run tests and commit `feat(experiment): execute variants as ordinary workflow runs`.

### Task 3: Run pinned evaluator workflows and collect measurements

**Files:**
- Create: `agent/experiment/evaluator.go`
- Create: `agent/experiment/evaluator_test.go`
- Modify: `agent/experiment/runner.go`
- Modify: `atc/db/agent_experiments_factory.go`
- Modify: `agent/snapshot/contracts/measurements.go`

- [ ] Write tests that wait for candidate terminal status, map fixed inputs and sealed outputs into evaluator ports, reject incompatible evaluator signatures, and invoke the pinned evaluator through the binder.
- [ ] Test malformed candidate output, missing output, evaluator failure, invalid measurements, deterministic negative-control assertion pass/fail for every comparator, missing asserted metrics, cancellation, and restart after candidate/evaluator completion.
- [ ] Run focused tests and confirm failure.
- [ ] Use evaluator idempotency key `experiment:<id>:cell:<id>:evaluator`. Map sources only from the frozen fixture or candidate output binding; never query a mutable ticket or current live definition.
- [ ] Store the evaluator workflow-run and `measurements/v1` snapshot ID. Cell terminal status distinguishes valid measurement, candidate contract failure, candidate platform failure, evaluator failure, negative-control assertion failure, and canceled. A negative control passes only when every stored assertion evaluates true against a valid measurements snapshot.
- [ ] Re-run tests and commit `feat(experiment): evaluate outputs with pinned workflow functions`.

### Task 4: Compute scorecard distributions and production comparisons

**Files:**
- Create: `agent/experiment/scorecard.go`
- Create: `agent/experiment/scorecard_test.go`
- Create: `agent/api/experiments/scorecard.go`
- Create: `agent/api/experiments/scorecard_test.go`
- Modify: `atc/db/agent_experiments_factory.go`
- Modify: `atc/db/agent_run_metrics_factory.go`

- [ ] Write tests for count, mean, median, min/max, standard deviation, percentile intervals, paired bootstrap confidence intervals, win/tie/loss paired comparisons, invalid/platform error rates, cost, latency, token use, variance, and generic workflow-outcome human-intervention counts.
- [ ] Test metric direction, missing values, unequal repetition counts, negative controls, and separation of fixture vs operational outcome data.
- [ ] Run focused tests and confirm failure.
- [ ] Compute rollups at read time from immutable cell measurement snapshots and existing run metrics; add materialized cache rows only after profiling demonstrates need.
- [ ] Join operational outcomes by workflow run/output snapshot identity, not ticket or repository commit alone.
- [ ] Return every metric distribution and cell drill-down; do not choose one hard-coded primary metric.
- [ ] Freeze recommendation policy: each compared variant needs at least five valid paired repetitions and at least 80% valid coverage of expected cells; compute a deterministic 95% paired bootstrap interval over 10,000 resamples seeded by `(experiment ID, metric, variant)`; label a directional winner only when the interval excludes zero, its platform-error rate is no more than five percentage points worse than control, and every configured negative control passes. Otherwise render “insufficient evidence” with the failed conditions.
- [ ] Re-run tests and commit `feat(experiment): explain quality cost latency and variance`.

### Task 5: Expose experiment APIs and Fly commands

**Files:**
- Create: `agent/api/experiments/handler.go`
- Create: `agent/api/experiments/handler_test.go`
- Create: `agent/api/experiments/types.go`
- Create: `agent/api/experiments/route_registration_test.go`
- Modify: `atc/routes.go`
- Modify: `atc/api/handler.go`
- Modify: `atc/wrappa/api_auth_wrappa.go`
- Modify: `atc/wrappa/reject_archived_wrappa.go`
- Create: `fly/commands/agent_experiments.go`
- Create: `fly/integration/agent_experiments_test.go`
- Modify: `fly/commands/agent.go`

- [ ] Write API tests for create, add/remove variants, add fixtures, evaluator mapping, dry-run validation, start, cancel, list/detail, cell detail, scorecard, and promote-link authorization. Fixture request tests cover `role`, normalized assertions, one-threshold versus two-threshold comparators, finite numbers, duplicate metric assertions, role/assertion mismatch, and path-specific validation errors.
- [ ] Write Fly tests for `experiments create|add-variant|add-fixture|validate|start|cancel|list|show|cells|scorecard` with JSON and table output. Cover repeated `--input`, `--role`, and `--assert` flags plus malformed assertions before any request is sent.
- [ ] Run focused tests and confirm failure.
- [ ] Implement narrow handlers over experiment store/runner validation and existing workflow promotion. The scorecard may recommend but never auto-promotes.
- [ ] Freeze the add-fixture API body as `{label, role, inputs, assertions}`. `role` is `normal` or `negative_control`; `inputs` maps port names to quoted-decimal snapshot-ID strings; each assertion is `{metric, comparator, thresholds}`, where `lt`, `lte`, `gt`, and `gte` require exactly one finite numeric threshold and `between` requires exactly two ascending finite thresholds and is inclusive at both bounds. Normal fixtures reject assertions; negative controls require at least one assertion; duplicate metrics within a fixture are rejected.
- [ ] Support fixture bindings as repeated `--input port=snapshot-id` and variants as `label=workflow@version` or `label=workflow@version#function-id`; require one explicit control label and exact quoted-decimal snapshot ID parsing. `experiments add-fixture EXPERIMENT LABEL` accepts `--role normal|negative-control` and repeatable `--assert metric=lt:value`, `metric=lte:value`, `metric=gt:value`, `metric=gte:value`, or `metric=between:lower:upper`, normalizing `negative-control` to the API's `negative_control` value.
- [ ] Re-run tests and commit `feat(agent): manage controlled workflow experiments`.

### Task 6: Add snapshot and workflow-run Elm domain models

**Files:**
- Modify: `web/elm/src/Concourse/Agent.elm`
- Create: `web/elm/src/Concourse/Snapshot.elm`
- Create: `web/elm/src/Concourse/WorkflowRun.elm`
- Create: `web/elm/src/Concourse/Experiment.elm`
- Modify: `web/elm/src/Api/Endpoints.elm`
- Modify: `web/elm/src/Message/Effects.elm`
- Modify: `web/elm/src/Message/Callback.elm`
- Create: `web/elm/tests/SnapshotDecoderTests.elm`
- Create: `web/elm/tests/WorkflowRunDecoderTests.elm`
- Create: `web/elm/tests/ExperimentDecoderTests.elm`

- [ ] Write decoder/encoder/route tests for manifests, locations, lineage, durable workflow runs, input/output bindings, projections, experiment matrix, cells, and scorecards, using quoted-decimal snapshot/workflow-run IDs greater than `2^53` and rejecting numeric JSON IDs.
- [ ] Run `yarn test` from the repository root and confirm the new decoder tests fail for the expected missing types.
- [ ] Implement domain modules with explicit remote-data errors and timestamp/status helpers. Do not overload `Concourse.PipelineRun` with durable workflow semantics.
- [ ] Add authenticated effects for workflow/snapshot/experiment endpoints and preserve existing polling cancellation behavior.
- [ ] Re-run Elm tests and commit `feat(web): decode workflows snapshots and experiments`.

### Task 7: Reframe `/agent` as a workflow dashboard

**Files:**
- Modify: `web/elm/src/Agent/Agent.elm`
- Modify: `web/elm/tests/AgentPageTests.elm`
- Modify: `web/elm/src/SideBar/SideBar.elm`
- Modify: `web/elm/tests/SideBarTests.elm`

- [ ] Write view tests for workflow cards/rows grouped by stable name with live version, signature, latest operational state, queued/running counts, recent success/error, experiment status, cost, and “needs attention”.
- [ ] Test empty/loading/stale/error/polling states and links to workflow details.
- [ ] Run Elm tests and confirm failure.
- [ ] Make workflows the primary section. Move credentials, principals, dispatcher health, and global spend into a clearly labeled operations/admin section without deleting functionality.
- [ ] Keep tickets and reviews as complementary navigation entries.
- [ ] Re-run tests and commit `feat(web): center agent dashboard on workflows`.

### Task 8: Add workflow detail and durable run detail routes

**Files:**
- Modify: `web/elm/src/Routes.elm`
- Modify: `web/elm/tests/RoutesTests.elm`
- Create: `web/elm/src/AgentWorkflow/AgentWorkflow.elm`
- Create: `web/elm/src/AgentWorkflowRun/AgentWorkflowRun.elm`
- Create: `web/elm/tests/AgentWorkflowPageTests.elm`
- Create: `web/elm/tests/AgentWorkflowRunPageTests.elm`
- Modify: `web/elm/src/Main.elm`

- [ ] Add/test `/agent/workflows/:name` and `/agent/workflows/:name/runs/:workflowRunId` routes with percent-safe workflow names.
- [ ] Test workflow detail signature, version timeline/live promotion, source hash, operational/experiment filters, outcomes/cost, experiment links, and start-run input binding form.
- [ ] Test run detail exact definition/template/concrete-plan hashes, origin, Concourse build/DAG link, input/output snapshots, lineage edges, active human wait/questions, replica state, telemetry, contract-failure diagnostics, errors, generic outcome/human interventions, review projection, and repository-change diff.
- [ ] Run Elm tests and confirm failure.
- [ ] Reuse ordinary Concourse build/DAG links rather than drawing a second plan renderer. Embed existing `Build.AgentReview` and the bounded diff renderer selected by snapshot type.
- [ ] Make every snapshot ID clickable to snapshot detail and every underlying pipeline run explicitly labeled execution detail.
- [ ] Re-run tests and commit `feat(web): inspect workflow versions runs and lineage`.

### Task 9: Add snapshot detail and reusable type projections

**Files:**
- Create: `web/elm/src/AgentSnapshot/AgentSnapshot.elm`
- Create: `web/elm/src/AgentSnapshot/Projection.elm`
- Create: `web/elm/src/AgentSnapshot/RepositoryChange.elm`
- Create: `web/elm/tests/AgentSnapshotPageTests.elm`
- Modify: `web/elm/src/Routes.elm`
- Modify: `web/elm/src/Main.elm`
- Modify: `web/elm/src/AgentTickets/AgentTicket.elm`
- Modify: `web/elm/tests/AgentTicketPageTests.elm`

- [ ] Test `/agent/snapshots/:id` manifest/type/digest/retention/replicas, producer invocation, inputs, downstream consumers, pin actions, download, and projection rendering.
- [ ] Test repository change file stats/unified diff/truncation and review findings/feedback through reusable projection components.
- [ ] Test ticket detail links its captured input revision and typed output snapshots; legacy attempts retain external compare fallback.
- [ ] Run Elm tests and confirm failure.
- [ ] Implement a type-to-projection dispatch that is passive UI logic only. Unknown types render manifest/download without failure.
- [ ] Reuse existing review components and add the bounded server diff view; do not recompute Git diffs in Elm.
- [ ] Re-run tests and commit `feat(web): render typed snapshot lineage and projections`.

### Task 10: Add experiment laboratory views

**Files:**
- Create: `web/elm/src/AgentExperiments/AgentExperiments.elm`
- Create: `web/elm/src/AgentExperiment/AgentExperiment.elm`
- Create: `web/elm/src/AgentExperiment/Scorecard.elm`
- Create: `web/elm/tests/AgentExperimentsPageTests.elm`
- Create: `web/elm/tests/AgentExperimentPageTests.elm`
- Modify: `web/elm/src/Routes.elm`
- Modify: `web/elm/src/Main.elm`
- Modify: `web/elm/src/SideBar/SideBar.elm`

- [ ] Test experiment list status/workflow/control/variant/fixture progress and attention states.
- [ ] Test detail matrix, pinned signatures/evaluator, per-cell status, repetitions, negative controls, metric distributions, variance/error/cost/latency, operational comparison, cancel, and explicit promotion link.
- [ ] Test the frozen minimum-five/80%-coverage/95%-bootstrap/error-rate/negative-control recommendation policy and prove scorecards expose raw cells whenever they render “insufficient evidence”.
- [ ] Run Elm tests and confirm failure.
- [ ] Implement `/agent/experiments` and `/agent/experiments/:id` as the laboratory view, distinct from workflow operational runs but linked both ways.
- [ ] Use compact tables/sparklines already available in the project; add no charting dependency unless existing Elm primitives cannot render percentile bars.
- [ ] Re-run tests and commit `feat(web): compare workflow variants in experiments`.

### Task 11: Verify historical roadmap supersession and migration ownership

**Files:**
- Modify: `docs/superpowers/plans/2026-07-21-experiments-and-workflow-ui.md`

- [ ] Confirm Snapshot Task 0 banners remain present and name the approved 2026-07-21 design/program as authoritative.
- [ ] Run `rg -n 'assumed LANDED|17731061|step_kind|primaryMetric' docs/superpowers` and inspect every remaining historical match for an explicit superseded label.
- [ ] Confirm `jetbridgeHeadMigration` is `1773106110` and the complete migration walk includes every migration from `1773106100` through `1773106110`.
- [ ] Commit any drift correction as `docs(agentic): preserve superseded roadmap boundary`.

### Task 12: Full verification and product acceptance

**Files:**
- Create: `topgun/k8s_behavioral/agentic_workflows_test.go`
- Modify: `docs/superpowers/plans/2026-07-21-experiments-and-workflow-ui.md`

- [ ] Run formatting and generated fakes; `go test ./agent/snapshot/... ./agent/workflow ./agent/workflowrun ./agent/experiment/... ./agent/projection/... ./agent/publisher/... ./agent/workflowwait -count=1`; `make test-fly-integration`; `yarn test`; `yarn build-elm`; focused DB specs named in the earlier workstreams; `make test-integration`; and `ginkgo --focus='agentic workflows' ./topgun/k8s_behavioral` against the prepared behavioral cluster.
- [ ] Run the seven program acceptance scenarios from the program plan and retain command/output evidence in the final handoff.
- [ ] Run `make test-ci-agent`, `make test-unit`, `make test-integration`, and `make test-fly-integration` when PostgreSQL and sandbox networking are available; classify environment failures separately from product failures.
- [ ] Inspect the final `git diff`, search for unresolved template markers, advisory `output_schema` use in v3, implicit v3 harvest, and new ticket-coupled experiment identities.
- [ ] Mark completed checkboxes and commit `test(agentic): verify workflow function product loop`.
