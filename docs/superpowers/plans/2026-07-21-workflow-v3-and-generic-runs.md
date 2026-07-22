# Workflow Version 3 and Generic Runs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Import a workflow as a versioned typed function backed by an ordinary visible Concourse DAG, then invoke it generically by binding named snapshots without requiring a ticket, repository, workspace, or implicit harvest step.

**Architecture:** Keep legacy workflow configs intact and add a strict tagged version-3 definition. The v3 compiler reuses `atc.Step` and Concourse validation, adds a signature/type-flow pass, expands named capabilities, and emits an immutable one-job template. A generic binder owns admission, idempotency, durable workflow-run provenance, snapshot authorization, template creation, and the underlying pipeline run. The ticket path calls this binder only for v3 definitions and retains its v1/v2 renderer as compatibility code.

**Tech Stack:** Go, workflow source compiler, ATC config/plan types, PostgreSQL stores, HTTP API, Fly CLI, Concourse pipeline templates/runs.

## Global Constraints

- The snapshot core plan through `load_snapshot:` must be complete before binder execution tasks begin.
- Version 3 embeds ordinary Concourse step grammar. Do not introduce agent-only equivalents of `do`, `in_parallel`, hooks, retry, timeout, or across.
- One invocation target maps to one immutable template name containing its target-config hash prefix. Full workflows and extracted `function_id` nodes from the same definition use distinct names and can never update each other's template between bind validation and run creation.
- Durable `agent_workflow_runs` are authoritative history; `pipeline_runs` are execution linkage and may be deleted.
- V1/v2 imports and dispatch remain byte-compatible unless a focused compatibility test says otherwise.

---

### Task 1: Introduce a tagged version-3 definition model

**Files:**
- Modify: `agent/workflow/config.go`
- Modify: `agent/workflow/definition.go`
- Modify: `agent/workflow/parse.go`
- Create: `agent/workflow/function_config.go`
- Create: `agent/workflow/parse_v3_test.go`
- Modify: `agent/workflow/parse_v2_test.go`
- Modify: `agent/workflow/hash_test.go`

- [ ] Write tests for the exact v3 example in the program plan, JSON/YAML round trip, signature order, stable typed-node `function_id`, `{type, optional}` input configs, output `from`, capabilities, resources/resource types/prototypes/var sources, and ordinary nested ATC steps.
- [ ] Write strict-error tests for unknown top-level, signature, capability, output, and embedded-step fields; missing/duplicate ports; invalid type refs; invalid signature version; multiple jobs; legacy-only fields; and missing `from` mappings.
- [ ] Prove stored v1/v2 bytes still parse with the legacy behavior and schema 3 is no longer rejected by the v2 test.
- [ ] Run `go test ./agent/workflow -run 'Test.*V3|TestParse' -count=1` and confirm failure.
- [ ] Add `CompiledDefinition{SchemaVersion, Name, Description, Legacy, Function}` while retaining `Definition.Config` as the compatibility accessor for v1/v2 call sites.
- [ ] Define `FunctionConfig` with `SignatureVersion`, ordered `Inputs`, ordered workflow `Outputs`, named `Capabilities`, `Plan []atc.Step`, and allowed Concourse declarations.
- [ ] Parse v3 in two strict passes: strict function envelope decoding, then an `atc.UnmarshalConfig` wrapper for the embedded plan/declarations so `Step.UnmarshalJSON` remains authoritative.
- [ ] Re-run tests and commit `feat(workflow): parse strict typed function definitions`.

### Task 2: Compile source assets and named capabilities into literal plans

**Files:**
- Modify: `agent/workflow/compile.go`
- Modify: `agent/workflow/compile_test.go`
- Modify: `agent/workflow/manifest.go`
- Modify: `agent/workflow/manifest_test.go`
- Modify: `atc/steps.go`
- Modify: `atc/sidecar.go`

- [ ] Add manifest tests showing v3 prompt files, system prompt files, context files, and skill trees are content-hashed and compiled without runtime source reads.
- [ ] Add capability tests for exact contract versions, mandatory OCI digest references, rejection of tag-only images, valid sidecar configs, duplicate sidecar names, reserved names, missing references, and provider-neutral custom contracts.
- [ ] Test that agent nodes receive resolved prompt/system/context/skills and literal sidecars while tasks can use ordinary inline/file sidecars unchanged.
- [ ] Run focused compile tests and confirm failure.
- [ ] Reuse the v2 safe path resolver and source manifest canonical hash. Add v3 asset fields only to agent step authoring, then erase authoring-only capability names after expansion.
- [ ] Define `Capability{Contract string, Sidecar atc.SidecarConfig}`. Validate contract as a type reference and require `image@sha256:<64 lowercase hex>`; v3 never executes a mutable tag.
- [ ] Preserve `dev-mcp` as a normal seeded capability contract; do not special-case `dev`, `platform`, or `gateway` roles in the compiler.
- [ ] Re-run tests and commit `feat(workflow): compile versioned capabilities and agent assets`.

### Task 3: Type-check snapshot flow through the ordinary DAG

**Files:**
- Create: `agent/workflow/typecheck.go`
- Create: `agent/workflow/typecheck_test.go`
- Modify: `agent/workflow/compile.go`
- Modify: `atc/configvalidate/validate.go`
- Modify: `atc/configvalidate/validate_test.go`

- [ ] Write sequence tests for workflow inputs, stable unique `function_id` values, typed task/agent outputs, downstream exact matches, optional ports, and public output mappings.
- [ ] Write composition tests for `do`, `in_parallel`, `try`, retry, timeout, `on_success`, `on_failure`, `on_error`, `on_abort`, and `ensure` using their actual artifact visibility semantics.
- [ ] Write rejection tests for use-before-produce, type mismatch, untyped workflow output, duplicate parallel producers, conditional required output, typed output escaping `across`, and undeclared public input/output.
- [ ] Run `go test ./agent/workflow -run TypeCheck -count=1` and confirm failure.
- [ ] Implement a recursive environment transformer over concrete `atc.StepConfig` types. Sequence mutates one environment; parallel branches start from copies and merge unique outputs; wrappers propagate only outputs guaranteed on the success path.
- [ ] Run ordinary `configvalidate.Validate` after function-specific compilation so images, resources, sidecars, and standard step rules are not duplicated.
- [ ] Annotate the concrete producer of each public workflow output with retention `workflow`, the public port name, the definition ID field, and the workflow-run interpolation token.
- [ ] Re-run tests and commit `feat(workflow): type-check snapshot flow across Concourse DAGs`.

### Task 4: Persist schema and signature metadata with workflow versions

**Files:**
- Create: `atc/db/migration/migrations/1773106101_add_workflow_schema_signature.up.go`
- Create: `atc/db/migration/migrations/1773106101_add_workflow_schema_signature.down.sql`
- Modify: `atc/db/agent_workflows_factory.go`
- Modify: `atc/db/agent_workflows_factory_test.go`
- Modify: `agent/workflow/memory_store.go`
- Modify: `agent/workflow/memory_store_test.go`
- Modify: `atc/db/migration/legacy_upgrade_test.go`

- [ ] Write DB tests for importing/listing/getting v1/v2/v3 definitions, indexed schema/signature fields, same-signature compatibility, incompatible signature promotion warning data, and content-hash idempotency.
- [ ] Write migration tests that backfill real existing seed definitions as `(schema_version 1|2, signature_version 0)` and reject malformed stored source without partially migrating.
- [ ] Run focused workflow factory specs and confirm failure.
- [ ] Add non-null `schema_version` and `signature_version` columns and indexes. Backfill in a Go migration because extracting YAML safely in SQL is not reliable.
- [ ] Populate fields from the compiled definition at import and include them in summaries/API presentations without recompiling list rows.
- [ ] Advance `jetbridgeHeadMigration` to `1773106101` and pass legacy-to-head plus down/up migration coverage.
- [ ] Re-run tests and commit `feat(db): index workflow schema and signature versions`.

### Task 5: Compile immutable workflow templates

**Files:**
- Create: `agent/workflow/render.go`
- Create: `agent/workflow/render_test.go`
- Create: `agent/workflow/extract.go`
- Create: `agent/workflow/extract_test.go`
- Modify: `agent/dispatch/render.go`
- Modify: `agent/dispatch/render_test.go`
- Modify: `atc/config.go`
- Modify: `atc/config_test.go`

- [ ] Write renderer tests showing each public input becomes an initial `load_snapshot:` step parameterized by quoted-decimal `((snapshot_<port>))`, followed by the exact authored DAG, with workflow output annotations and no harvest.
- [ ] Write extraction tests that select one explicit stable `function_id`, lexicographically order map-backed input/output port names before signature hashing, retain only compiled prompt/context/skills and digest-pinned inline capabilities, and render a one-node harness with public snapshot boundaries.
- [ ] Reject extraction for any enclosing wrapper; any untyped input/output; task `file:` config; image artifacts; sidecar file references; prior `get`/`load_var`/set-var artifacts; resource-produced params; or other runtime dependency not declared as a typed snapshot input. Inline task configs with immutable image resources and agent nodes whose every artifact dependency is typed are extractable.
- [ ] Test target-specific deterministic template names, stable canonical target-config/signature hash, full-workflow versus extracted-node noncollision, number/boolean/string run parameter schemas, entry-job behavior, and rejection of unsafe names/hash collisions.
- [ ] Confirm v1/v2 ticket rendering retains `get repo`, file-delivered ticket assets, and compatibility harvest behavior.
- [ ] Run focused render tests and confirm failure.
- [ ] Implement `workflow.RenderFunction(target)` returning `{TemplateName, TargetSignature, Config, TargetConfigHash, InputParamNames}`. Full workflow names are `agent-workflow-<slug>-v<version>-<first12-targethash>`; extracted node names are `agent-function-<slug>-v<version>-<function-id>-<first12-targethash>`.
- [ ] Add one string template param per snapshot input and quoted-decimal interpolation fields for workflow run ID. Validate every nonzero value with `^[1-9][0-9]*$`. Required input params have no default. Optional input params default to `"0"`; their `load_snapshot:` step is marked optional and becomes a successful no-op for `"0"`, leaving the artifact absent for explicitly optional consumers.
- [ ] Make `RenderFunction` accept either the full workflow target or an extracted node target and return the target signature/hash together with the parameterized template.
- [ ] Move legacy renderer comments/names under an explicit `RenderLegacyTicket` entry point; do not delete it.
- [ ] Re-run tests and commit `feat(workflow): render immutable function templates`.

### Task 6: Implement generic workflow-run binding and admission

**Files:**
- Create: `agent/workflowrun/binder.go`
- Create: `agent/workflowrun/binder_test.go`
- Create: `agent/workflowrun/types.go`
- Create: `agent/workflowrun/workflowrunfakes/`
- Modify: `atc/db/agent_workflow_runs_factory.go`
- Modify: `atc/db/agent_workflow_runs_factory_test.go`
- Modify: `atc/db/pipeline_run_factory.go`
- Modify: `atc/db/pipeline_run_factory_test.go`
- Modify: `atc/db/job.go`
- Modify: `atc/db/job_test.go`
- Modify: `agent/dispatch/dispatch.go`

- [ ] Write binder tests for live/explicit definition resolution, full-workflow and `FunctionID` node targets, exact input coverage, type mismatch, durable team authorization, IDs above `2^53`, pinned snapshots, unavailable content, budget policy, immutable origin, idempotency key reuse/conflict, template save failure, pipeline run failure, and durable error status.
- [ ] Add concurrency tests proving one team/idempotency key creates one workflow run/template/pipeline run and an immutable definition template cannot drift between validation and execution. Add a no-gap admission test proving the instance pipeline is durably marked as workflow-owned before its first entry build becomes externally triggerable.
- [ ] Run `go test ./agent/workflowrun -count=1` and confirm failure.
- [ ] Implement `Binder.BindAndCreate(ctx, BindRequest)` with narrow interfaces for definition resolution, node extraction, snapshot authorization, durable run store, immutable template saver, pipeline-run creator, budget admission, and run-secret attachment.
- [ ] Insert the durable workflow run, input bindings, and non-expiring `workflow-run-input` retention claims in one transaction before external execution creation, using status `admitting`; transition to `running` only after pipeline linkage and concrete instance-config persistence succeed. Claims remain with durable run history and are released only by an explicit future workflow-history retention operation, never merely at terminal status. Persist admission failures as `errored` with a bounded reason so the idempotency key remains truthful.
- [ ] Materialize template params as quoted decimal strings from bound snapshot IDs and the durable workflow-run ID. Extend the pipeline-run creation seam with `CreateRunForWorkflowRun(workflowRunID, ...)`, returning the exact post-interpolation instance `atc.Config`. In its database transaction, create the pipeline run and instance pipeline and link that instance to the preallocated durable workflow run before triggering entry jobs. Trigger the initial entry jobs through a package-private pipeline-run path; ordinary `Job.CreateBuild`, `Job.RerunBuild`, and `Pipeline.CreateStartedBuild` paths never receive this bypass. Persist the canonical instance JSON/hash before reporting admission success. Label this `instance_config`, not the execution plan. A restart reconciler backfills it from the saved instance pipeline if the process stops between pipeline creation and provenance persistence.
- [ ] Re-run tests and commit `feat(agent): bind snapshots to durable workflow runs`.

### Task 7: Reconcile workflow-run completion and outputs

**Files:**
- Create: `atc/db/migration/migrations/1773106103_reconcile_workflow_run_completion.up.sql`
- Create: `atc/db/migration/migrations/1773106103_reconcile_workflow_run_completion.down.sql`
- Create: `agent/workflowrun/reconciler.go`
- Create: `agent/workflowrun/reconciler_test.go`
- Modify: `atc/db/agent_workflow_runs_factory.go`
- Modify: `atc/db/agent_workflow_runs_factory_test.go`
- Modify: `atc/atccmd/command.go`
- Modify: `atc/atccmd/command_test.go`
- Modify: `atc/builds/planner.go`
- Modify: `atc/builds/planner_test.go`
- Modify: `atc/db/build.go`
- Modify: `atc/db/build_test.go`

- [ ] Write tests mapping underlying pipeline status to durable run status, backfilling instance-config provenance after an admission crash, capturing the first admitted build's actual `atc.Plan` and resolved resource/image/capability dependencies after planning, waiting for all required output bindings, treating missing required outputs as contract failure, keeping terminal states monotonic when unexpected later pipeline activity appears, and surviving deleted pipeline/template rows.
- [ ] Test idempotent restart behavior and bounded polling batches.
- [ ] Run focused reconciler tests and confirm failure.
- [ ] Add a planning hook keyed by server-verified workflow-run/build linkage. After Concourse assigns plan IDs and resolves plan-time dependency identities, persist `planned_build_id`, canonical actual-plan JSON/hash, and resolved dependency JSON on the durable run. A run cannot become terminal until this provenance is captured or a planner error is recorded.
- [ ] Implement a component using the existing pipeline-run lifecycle polling pattern. `succeeded` requires successful execution plus every required public output binding; malformed/missing outputs become `failed`, platform/storage failures become `errored`. Terminal durable states and output bindings never reopen or change; later underlying builds are recorded as anomalies and ignored.
- [ ] Advance `jetbridgeHeadMigration` to `1773106103` with down/up and legacy-to-head coverage. Persist only the selected entry build's copied terminal outcome/reconciliation schedule plus deduplicated later-build anomalies; deletion of ephemeral build/pipeline rows must not erase copied provenance.
- [ ] Add `--agent-workflow-run-reconciler-interval` with a positive default and component health reporting.
- [ ] Re-run tests and commit `feat(agent): reconcile durable workflow run outcomes`.

### Task 8: Add workflow-run APIs and Fly commands

**Files:**
- Create: `agent/api/workflowruns/handler.go`
- Create: `agent/api/workflowruns/handler_test.go`
- Create: `agent/api/workflowruns/types.go`
- Create: `agent/api/workflowruns/route_registration_test.go`
- Modify: `atc/routes.go`
- Modify: `atc/api/handler.go`
- Modify: `atc/wrappa/api_auth_wrappa.go`
- Modify: `atc/wrappa/reject_archived_wrappa.go`
- Modify: `atc/api/present/pipeline_run.go`
- Modify: `atc/api/jobserver/create_build.go`
- Modify: `atc/api/jobserver/rerun_build.go`
- Modify: `atc/api/pipelineserver/build.go`
- Modify: `atc/api/jobs_test.go`
- Modify: `atc/api/pipelines_test.go`
- Modify: `atc/db/job.go`
- Modify: `atc/db/job_test.go`
- Modify: `atc/db/pipeline.go`
- Modify: `atc/db/pipeline_test.go`
- Modify: `fly/commands/agent_workflows.go`
- Modify: `fly/integration/agent_workflows_test.go`

- [ ] Write route/auth tests and handler tests for create, list by workflow/status/origin, detail, cancel, retry with same inputs/new idempotency key and `retry_of_workflow_run_id`, and output manifests. Exercise all three existing manual execution endpoints against linked workflow instance pipelines: create job build, rerun job build, and create started one-off pipeline build. Assert each returns `409 Conflict` before any build row or scheduling request exists, while the same endpoints retain current behavior for ordinary pipelines.
- [ ] Write direct DB tests proving `Job.CreateBuild`, `Job.RerunBuild`, and `Pipeline.CreateStartedBuild` return typed `ErrWorkflowRunOwnedPipeline` without inserting a build for workflow-owned instances. Prove the package-private initial-entry-build path is usable only by `PipelineRunFactory.CreateRunForWorkflowRun`, and that automatic non-manual Concourse scheduling remains unchanged.
- [ ] Write Fly integration tests for `fly agent workflows run`, `runs`, and `run`, using repeated `--input name=snapshot-id`, `--version`, `--idempotency-key`, `--json`, and wait/follow behavior.
- [ ] Run focused API/Fly tests and confirm failure.
- [ ] Implement `POST /api/v1/agent/workflows/:workflow_name/runs`, list, and detail routes. Responses expose `workflow_run_id` and `pipeline_run_id` as distinct fields.
- [ ] Keep ordinary pipeline-run API behavior unchanged for ordinary pipelines. Put the authoritative ownership check in the DB transaction used by `Job.CreateBuild`, `Job.RerunBuild`, and `Pipeline.CreateStartedBuild`, map `ErrWorkflowRunOwnedPipeline` to `409 Conflict` in the three existing API handlers, and retain handler checks only as early diagnostics. The retry endpoint always creates a new durable run and immutable output bindings. Cancellation delegates to the linked execution when present and records the durable requested/terminal state.
- [ ] Re-run tests and commit `feat(agent): invoke and inspect workflow functions`.

### Task 9: Seed concrete version-3 workflows

**Files:**
- Create: `agent/workflow/seeds/code-review-v3/workflow.yml`
- Create: `agent/workflow/seeds/code-review-v3/prompts/review.md`
- Create: `agent/workflow/seeds/small-fix-v3/workflow.yml`
- Create: `agent/workflow/seeds/small-fix-v3/prompts/implement.md`
- Create: `agent/workflow/seeds/version-upgrade-v3/workflow.yml`
- Create: `agent/workflow/seeds/version-upgrade-v3/prompts/upgrade.md`
- Modify: `agent/workflow/seed_test.go`

- [ ] Add seed tests that compile each manifest, validate its exact signature, render the plan, and prove no implicit ticket/workspace/harvest requirement.
- [ ] Define code review as `(before repository/v1, after repository/v1) -> review/v1`.
- [ ] Define small fix as `(repository/v1, work-item/v1) -> (repository-change/v1, report opaque/v1)` with explicit tests/review and no publisher.
- [ ] Define version upgrade as `(repository/v1, upgrade-request/v1) -> (repository-change/v1, upgrade-report/v1)`; register strict request/report contracts in snapshot core.
- [ ] Run seed tests and commit `feat(workflow): seed concrete typed engineering workflows`.

### Task 10: Verify generic workflow vertical slice

**Files:**
- Create: `atc/db/agent_workflow_run_integration_test.go`
- Create: `agent/workflowrun/e2e_test.go`
- Modify: `docs/superpowers/plans/2026-07-21-workflow-v3-and-generic-runs.md`

- [ ] Exercise import, promote, snapshot bind, template materialization, DAG execution, typed seal, durable output binding, completion reconciliation, and history after template deletion.
- [ ] Prove a compatible prompt-only version can be promoted while prior version runs remain grouped and exact.
- [ ] Prove an incompatible signature version cannot be substituted into a retry or experiment cell expecting the old signature.
- [ ] Run formatting and generated fakes; `go test ./agent/workflow ./agent/workflowrun ./agent/api/workflowruns -count=1`; `ginkgo --focus='AgentWorkflowsFactory|AgentWorkflowRunsFactory|agent workflow run' ./atc/db/`; `make test-fly-integration`; and `make test-integration`.
- [ ] Mark completed checkboxes and commit `test(workflow): verify generic typed workflow runs`.
