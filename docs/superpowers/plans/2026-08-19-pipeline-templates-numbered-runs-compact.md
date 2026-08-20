# Compact Pipeline Templates and Numbered Runs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add compact, numbered, parameterized executions of template pipelines while leaving checking, scheduling, builds, logs, and ordinary pipeline behavior under normal Concourse ownership.

**Architecture:** A durable `pipeline_runs` header owns one disposable ordinary pipeline payload through `pipelines.pipeline_run_id`. Run-specific code is limited to atomic creation, durable build/cache identity, lifecycle locking, reclaim, collection APIs, and lean presentation; existing Concourse components handle checking, scheduling, execution, and retention policy calculation.

**Tech Stack:** Go, PostgreSQL migrations and transactions, Ginkgo v2/Gomega, Elm 0.19, go-concourse, Fly.

**Spec:** `docs/superpowers/specs/2026-08-19-pipeline-templates-numbered-runs-compact-design.md`

## Global Constraints

- Work only in the isolated `codex/pipeline-templates-numbered-runs-compact` worktree based on `cb6b1ef3c4244651a4ff29d790f08514588e5b71`; do not copy implementation files from the rejected branch.
- Follow strict red-green-refactor. Before every production edit, name the break, add a real behavior test with literal expectations, run it, and observe the intended failure.
- Run database-backed packages sequentially with `ginkgo`, never plain `go test ./...`; do not run two PostgreSQL suites concurrently.
- Classify a base by `template=true`, a payload by non-null `pipeline_run_id`, and an ordinary pipeline by neither. Never infer run identity from instance vars or names.
- Use the universal lock order `template row -> durable run row -> payload/job/build rows`; no transaction may acquire a template row after a run row.
- Unresolvable inputs remain pending under normal Concourse scheduling. Do not add an initialization ledger, retry state machine, lifecycle sweep, refusal markers, frozen versions, or a historical DAG.
- Keep ordinary cache keys and event routing byte-for-byte unchanged. A run uses one discriminated cache identity and team-event routing from build birth.
- A payload is reclaimed exactly when its child pipeline is absent. Reclaim never deletes or copies retained build events.
- Public template viewers receive structural run data only. Params, schema, and config hash require template authorization; payload access remains its own authorization decision.
- Existing ordinary pipeline APIs, URLs, event partitions, and task-cache behavior must remain compatible.
- New production files stay below 500 lines. Total handwritten shipping code is 5,000-7,000 added lines, tests 7,000-9,000, and this plan plus the spec stays below 1,500 lines.
- Database structural guards must prove they matched real rows; repository scans must fail on zero matches and must not assert an exact file count.
- Preserve unrelated user changes. Each task owns only its listed files and must adapt to already-committed preceding tasks.

## Shared interfaces

```go
// atc/run_params.go
type RunParams map[string]any
type ParamSchema struct {
	Name string
	Type ParamType
	Required bool
	Default any
	Values []any
	Description string
}
type RunRetentionConfig struct { KeepLast *int; TTLDays *int }
type RunIdentity struct { Number int; ID int }

// atc/run_config.go
type RunMaterialization struct {
	Config Config
	CanonicalJSON []byte
	EntryJobNames []string
	ExpectedJobNames map[string]bool
	PolicyKeyByJobName map[string]string
}

// atc/task_cache_identity.go
type TaskCacheIdentity struct {
	JobID int
	TeamID int
	TemplatePipelineID int
	RunJobName string
}
```

---

### Task 1: Template configuration and validation

**Files:**
- Modify: `atc/config.go`, `atc/config_test.go`
- Modify: `atc/configvalidate/validate.go`, `atc/configvalidate/validate_test.go`
- Modify: `atc/api/configserver/save.go` and focused tests
- Create: `atc/db/run_retention_test.go`
- Create: `atc/run_params.go`, `atc/run_params_test.go`

**Interfaces:**
- Consumes: existing strict `atc.Config` decoding and `configvalidate.Validate`.
- Produces: `Config.Template bool`, `Config.Params []ParamSchema`, `Config.RunRetention *RunRetentionConfig`, effective `ValidateTemplateConfig(Config) error`, declaration `ValidateTemplateDeclaration(PipelineRef, Config) error`, named `MaxRunRetentionKeepLast`/`MaxRunRetentionTTLDays`, and the shared types above.

- [ ] **Step 1: Write the failing configuration matrix**

Add literal table entries proving strict YAML/JSON round trips the three fields; keep-only and TTL-only work; an empty policy, explicit zero, non-template use, instanced templates, no-entry templates, duplicate/reserved names, wrong default/enum scalar types, and over-bound retention fail. At the TTL bound, execute the actual PostgreSQL cutoff expression successfully. Include legal pinned versions and interpolated job/task/cache identities.

```go
Entry("reserved run_id", templateWith(ParamSchema{Name: "run_id", Type: ParamTypeString}), "parameter name run_id is reserved"),
Entry("ordinary pipeline", Config{Params: []ParamSchema{{Name: "env", Type: ParamTypeString}}}, "params are only valid on templates"),
Entry("dynamic identities", dynamicTemplateConfig(), ""),
```

- [ ] **Step 2: Run the tests and observe RED**

Run sequentially: `ginkgo --focus='Template configuration|Template parameter schema|Run retention configuration' ./atc ./atc/configvalidate ./atc/api/configserver`, then `ginkgo --focus='Run retention cutoff bound' ./atc/db`.

Expected: compile failure for the new config fields and `ValidateTemplateConfig`.

- [ ] **Step 3: Implement one reusable validator**

Add the exact shared fields and scalar enum `string|number|bool|enum`. Declaration validation enforces template-only fields and a non-instanced ref, then calls the reusable effective shape validator; v4 may call shape validation while `template=false`. Require at least one entry, at least one declared retention dimension, and named positive bounds proven against cutoff arithmetic. Invoke declaration validation in config save after parsing `PipelineRef`, returning 400 before persistence.

- [ ] **Step 4: Verify GREEN and regression safety**

Run `gofmt` on the listed Go files, then run `ginkgo ./atc ./atc/configvalidate ./atc/api/configserver` and `ginkgo --focus='Run retention cutoff bound' ./atc/db` sequentially.

Expected: both package suites pass.

- [ ] **Step 5: Commit**

Run: `git add atc/config.go atc/config_test.go atc/run_params.go atc/run_params_test.go atc/configvalidate atc/api/configserver atc/db/run_retention_test.go && git commit -m "feat(atc): validate pipeline templates"`

### Task 2: Run params, materialization, and expected jobs

**Files:**
- Create: `atc/run_config.go`, `atc/run_config_test.go`
- Modify: `atc/run_params.go`, `atc/run_params_test.go`

**Interfaces:**
- Consumes: `ParamSchema`, `RunParams`, `ValidateTemplateConfig(Config)`.
- Produces: `ValidateRunParams([]ParamSchema, RunParams) (RunParams, error)`, `MaterializeRunConfig(Config, RunIdentity, RunParams) (RunMaterialization, error)`.

- [ ] **Step 1: Write failing pure behavior tests**

Use hand-written configs to prove defaults precede storage/interpolation; string number/Boolean values coerce and JSON/Fly numbers normalize to `float64`; typed scalars remain typed; unknown, missing, and type-aware enum values fail; reserved values win; runtime vars remain unresolved; the input is unchanged; payload `template` is false; only unpassed triggers clear; dynamic names retain source policy keys; colliding materialized job names fail; and the fixed point includes entry/reachable jobs but excludes manual-only branches and disconnected cycles.

```go
Expect(result.PolicyKeyByJobName).To(Equal(map[string]string{
	"deploy-staging": "deploy-((environment))",
}))
Expect(result.ExpectedJobNames).To(Equal(map[string]bool{
	"entry": true, "deploy-staging": true,
}))
```

- [ ] **Step 2: Run the focused tests and observe RED**

Run: `ginkgo --focus='Run parameter validation|Run config materialization|Run expected jobs' ./atc`

Expected: compile failure for `ValidateRunParams` and `MaterializeRunConfig`.

- [ ] **Step 3: Implement one-pass materialization**

Use the normal vars resolver once with normalized params plus authoritative `run` and `run_id`. Marshal the typed config with `encoding/json` for deterministic canonical JSON. Compute entries, policy-key mapping, and expected fixed point from that same materialized graph.

- [ ] **Step 4: Verify GREEN**

Run: `gofmt -w atc/run_params.go atc/run_params_test.go atc/run_config.go atc/run_config_test.go && ginkgo ./atc`

Expected: the full `atc` suite passes.

- [ ] **Step 5: Commit**

Run: `git add atc/run_params.go atc/run_params_test.go atc/run_config.go atc/run_config_test.go && git commit -m "feat(atc): materialize numbered runs"`

### Task 3: Durable schema, models, and exact classification

**Files:**
- Create: `atc/db/migration/migrations/1773105505_add_pipeline_template_runs.up.sql`, `atc/db/migration/migrations/1773105505_add_pipeline_template_runs.down.sql`
- Create: `atc/db/migration/migrations/1773105506_add_pipeline_run_build_identity.up.sql`, `atc/db/migration/migrations/1773105506_add_pipeline_run_build_identity.down.sql`
- Create: `atc/db/migration/pipeline_template_runs_test.go`, `atc/pipeline_run.go`, `atc/db/pipeline_run.go`, `atc/db/pipeline_run_test.go`
- Modify: `atc/pipeline.go`, `atc/db/pipeline_ref.go`, `atc/db/pipeline.go`, `atc/db/pipeline_factory.go`, `atc/db/job.go`, `atc/db/build.go`, `atc/db/resource.go`, `atc/db/resource_type.go`, `atc/db/prototype.go`, and focused tests.

**Interfaces:**
- Consumes: Task 1 config fields.
- Produces: `RunStatus`, durable `PipelineRun` accessors, shared `PipelineRef.PipelineRunID() (int, bool)`, base-template ID/current ref, `Pipeline.PipelineRunID() (int, bool)`, `Job.RunExpected()`, `Job.RunPolicyKey()`, and build run identity accessors. `InstancePipelineID()` is hydrated by a left join or creation memory, never a query.

- [ ] **Step 1: Write failing migration and query tests**

Prove header-plus-child commits; a running orphan fails at deferred commit; a second child fails; ownership, job policy keys, and build labels are immutable; run-only job metadata on an ordinary pipeline fails; run build labels are all-present or all-absent; terminal detached child deletion succeeds; populated feature down raises an actionable error; empty down succeeds; ordinary `{run:N}` instances remain ordinary; and normal lists exclude payloads but retain templates and ordinary instances.

```go
id, found := pipeline.PipelineRunID()
Expect(found).To(BeTrue())
Expect(id).To(Equal(runID))
_, found = ordinaryRunShaped.PipelineRunID()
Expect(found).To(BeFalse())
```

- [ ] **Step 2: Run the tests and observe RED**

Run sequentially: `ginkgo --focus='Pipeline template run schema' ./atc/db/migration`, then `ginkgo --focus='PipelineRun|run payload classification|run build identity' ./atc/db`.

Expected: missing migrations/types first; after migration loading, invariant assertions fail until queries are changed.

- [ ] **Step 3: Implement the owner-header schema**

Add pipeline `template`, persisted params, two retention integers, `last_run_number`, and `pipeline_run_id`; the durable header fields from spec §5.2; job `run_expected/run_policy_key`; and build `pipeline_run_id/run_job_name/run_job_key`. Add partial unique/reclaim indexes, row checks, immutable triggers, deferred running-child triggers, restrictive template FK, and one shared empty-feature down guard invoked by every migration in this series. Scan run/base identity and current base ref through shared `pipelineRef` for Pipeline, Job, Resource, ResourceType, Prototype, and check paths; classify every list explicitly.

- [ ] **Step 4: Verify GREEN**

Run sequentially: `gofmt -w atc/pipeline_run.go atc/db/pipeline_run.go atc/db/pipeline_run_test.go atc/db/pipeline.go atc/db/pipeline_factory.go atc/db/job.go atc/db/build.go`, `ginkgo ./atc/db/migration`, then `ginkgo ./atc/db`.

Expected: both database suites pass.

- [ ] **Step 5: Commit**

Run: `git add atc/pipeline.go atc/pipeline_run.go atc/db atc/db/migration && git commit -m "feat(db): add durable pipeline run ownership"`

### Task 4: Atomic run creation

**Files:**
- Create: `atc/db/pipeline_run_factory.go`, `atc/db/pipeline_run_factory_test.go`, `atc/db/pipeline_run_errors.go`
- Modify: `atc/db/team.go`, `atc/db/job.go`, `atc/db/build.go`

**Interfaces:**
- Consumes: `ValidateTemplateConfig`, `ValidateRunParams`, `MaterializeRunConfig`, Task 3 models.
- Produces:

```go
type RunParams struct { Vars atc.RunParams } // db.RunParams wraps atc.RunParams
type RunCreationOpts struct {
	Config *atc.Config
	BeforeCommit func(Tx, RunCreation) error
}
type RunCreation struct {
	Run PipelineRun
	Config atc.Config
	CanonicalJSON []byte
	ConfigHash string
	EntryJobs []string
	EntryBuilds []Build
}
type PipelineRunFactory interface {
	CreateRun(context.Context, Pipeline, RunParams, string) (RunCreation, error)
	CreateRunInTx(context.Context, Tx, Pipeline, RunParams, string, RunCreationOpts) (RunCreation, error)
	AfterRunCreated(context.Context, RunCreation) error
	GetRun(Pipeline, int) (PipelineRun, bool, error)
	GetRunByID(int) (PipelineRun, bool, error)
	Runs(Pipeline, Page) ([]PipelineRun, Pagination, error)
}
```

- [ ] **Step 1: Write failing transaction tests**

Cover invalid/paused/archived/non-template/instanced inputs allocating nothing; concurrent monotonic numbers; occupied `{run:N}` skip; run ID available to `((run_id))`; SHA-256 over `"run-instance-config/v1\x00" + canonicalJSON`; authoritative v4 override; entry builds and job flags in the caller transaction; `BeforeCommit` visibility/error rollback; hydrated child accessor; and idempotent post-commit notification.

- [ ] **Step 2: Run and observe RED**

Run: `ginkgo --focus='PipelineRunFactory' ./atc/db`

Expected: compile failure for `PipelineRunFactory`.

- [ ] **Step 3: Implement the single creation seam**

Lock/re-read the template, validate before numbering, allocate header ID before materialization, insert header then child and ordinary graph, stamp expected/policy jobs, create entry builds, call `BeforeCommit` last, and leave commit ownership to the caller. `CreateRun` wraps begin/commit and calls notification only after commit.

- [ ] **Step 4: Verify GREEN**

Run: `gofmt -w atc/db/pipeline_run_factory.go atc/db/pipeline_run_factory_test.go atc/db/pipeline_run_errors.go atc/db/team.go atc/db/job.go atc/db/build.go && ginkgo ./atc/db`

Expected: database suite passes with the one-connection callback test.

- [ ] **Step 5: Commit**

Run: `git add atc/db && git commit -m "feat(db): create pipeline runs atomically"`

### Task 5: Ordinary checking, scheduling, and unified build admission

**Files:**
- Create: `atc/db/pipeline_run_lock.go`, `atc/db/pipeline_run_admission_test.go`
- Modify: `atc/db/check_factory.go`, `atc/db/pipeline_factory.go`, `atc/db/job_factory.go`, `atc/db/job.go`, `atc/db/build.go`, `atc/db/pipeline.go`, and focused tests
- Modify: `atc/scheduler/scheduler.go`, `atc/scheduler/scheduler_test.go`, `atc/scheduler/buildstarter_test.go`

**Interfaces:**
- Consumes: hydrated `pipelineRef.pipelineRunID`, run status, expected/policy job fields.
- Produces: canonical `lockPipelineRun(Tx, int) (PipelineRun, error)`, payload-to-run resolution, one job-build insert seam that derives run labels from joined rows, and `Job.ConsumeScheduleRequest(time.Time, bool) error` for an atomic observed-token advance/completion hook.

- [ ] **Step 1: Write failing behavior and race tests**

Prove base templates never check or schedule; payloads follow ordinary Lidar/passed scheduling; unresolved entry inputs stay pending and later version arrival starts them normally; entry, scheduler, manual, and rerun builds receive identical DB-derived labels; checks stay unstamped; one-offs conflict; start rechecks live/running state; schedule consumption never loses a newer token; and independent admission/terminal transactions serialize to one valid outcome.

- [ ] **Step 2: Run and observe RED**

Run sequentially: `ginkgo --focus='template checking|run build admission|run build identity' ./atc/db`, then `ginkgo --focus='run payload|pending build before schedule consumption' ./atc/scheduler`.

Expected: base rows enter old paths and run labels/race assertions fail.

- [ ] **Step 3: Implement the minimal shared seam**

Replace overloaded template predicates with explicit base/payload predicates. Route every non-check job build insertion through the run lock when `pipeline_run_id` is present; stamp from the locked job/pipeline join. Advance `last_scheduled` only to the observed token after a build exists or the pass proves none can be made.

- [ ] **Step 4: Verify GREEN**

Run `gofmt` on the listed Go files, then run `ginkgo ./atc/db` and `ginkgo ./atc/scheduler` sequentially.

Expected: both suites pass and ordinary pipeline expectations are unchanged.

- [ ] **Step 5: Commit**

Run: `git add atc/db atc/scheduler && git commit -m "feat(scheduler): admit run builds through durable ownership"`

### Task 6: Completion and manual reopen

**Files:**
- Create: `atc/db/pipeline_run_lifecycle.go`, `atc/db/pipeline_run_lifecycle_test.go`
- Modify: `atc/db/build.go`, `atc/db/job.go`, `atc/db/pipeline.go`, and focused tests
- Modify: `atc/scheduler/scheduler.go`, `atc/scheduler/runner.go`, and focused tests

**Interfaces:**
- Consumes: Task 5 admission lock and scheduler observation.
- Produces: internal `attemptRunCompletion(Tx, int) (bool, error)`; atomic observed-token consumption; manual trigger/rerun reopen transaction.

- [ ] **Step 1: Write failing lifecycle tests**

Cover active builds, outstanding schedules, and zero-build corruption blocking completion; rerun-aware latest status severity `errored > aborted > failed > succeeded`; successful expected-job coverage; quiescent red completion with absent downstream jobs; finish locking the run before build/downstream mutation; last unresolved request settling; atomic dormancy pause; user-pause attribution; direct-unpause versus terminalization; atomic reopen with stale-request discard; reclaimed refusal; and both outcomes of finish/admission/completion races.

```go
Expect(run.Status()).To(Equal(RunStatusSucceeded))
Expect(payload.Paused()).To(BeTrue())
Expect(payload.PausedBy()).To(Equal("run-completed"))
```

- [ ] **Step 2: Run and observe RED**

Run sequentially: `ginkgo --focus='Pipeline run lifecycle|run completion|manual run reopen' ./atc/db`, then `ginkgo --focus='schedule.*run completion' ./atc/scheduler`.

Expected: lifecycle function is absent and terminal transitions do not occur.

- [ ] **Step 3: Implement the stateless predicate**

Make `Build.Finish` acquire the durable run before changing the build or requesting downstream schedules. Under that lock, query blockers/latest terminals, enforce expected coverage only for green, then set header status/completion and internal pause atomically. `ConsumeScheduleRequest` advances only the observed token and invokes completion on no-build. Direct unpause locks the run; reopen only inside manual/rerun admission and clears completion, stale requests, and internal pause before inserting the build.

- [ ] **Step 4: Verify GREEN**

Run `gofmt` on listed files, then `ginkgo ./atc/db` and `ginkgo ./atc/scheduler` sequentially.

Expected: both suites pass without a lifecycle component or scan.

- [ ] **Step 5: Commit**

Run: `git add atc/db atc/scheduler && git commit -m "feat(db): complete and reopen pipeline runs"`

### Task 7: Discriminated task-cache persistence and clearing

**Files:**
- Create: `atc/task_cache_identity.go`, `atc/task_cache_identity_test.go`
- Create: `atc/db/migration/migrations/1773105507_add_run_task_cache_identity.up.sql`, `atc/db/migration/migrations/1773105507_add_run_task_cache_identity.down.sql`
- Modify: `atc/db/task_cache.go`, `atc/db/task_cache_factory.go`, `atc/db/worker_task_cache.go`, `atc/db/worker_task_cache_factory.go`, `atc/db/volume.go`, `atc/db/job.go`, `atc/db/task_cache_lifecycle.go`, `atc/db/team.go`, and focused tests

**Interfaces:**
- Consumes: hydrated base-template ID and materialized job name from a job.
- Produces: `atc.TaskCacheIdentity.Validate() error`; `TaskCacheFactory.Find(atc.TaskCacheIdentity, string, string) (UsedTaskCache, bool, error)`; `FindOrCreate(atc.TaskCacheIdentity, string, string) (UsedTaskCache, error)`; `UsedTaskCache.Identity() atc.TaskCacheIdentity`; `CreatedVolume.InitializeTaskCache(atc.TaskCacheIdentity, string, string) error`; `Job.TaskCacheIdentity() (atc.TaskCacheIdentity, error)`.

- [ ] **Step 1: Write failing persistence tests**

Prove exactly one identity form; existing ordinary rows/uniqueness remain unchanged; different ephemeral run jobs with the same template/materialized name find one row; different templates/names do not; live run job and literal base job clear that shared row; interpolated base job gets the typed conflict; payload deletion does not collect run caches; team purge does; and populated down refuses.

- [ ] **Step 2: Run and observe RED**

Run sequentially: `ginkgo --focus='run task cache schema' ./atc/db/migration`, then `ginkgo --focus='Task cache identity|Clear task cache|Run task cache lifecycle' ./atc ./atc/db`.

Expected: missing migration/type and old job-ID-only uniqueness failures.

- [ ] **Step 3: Implement both identity forms**

Make `task_caches.job_id` nullable; add `template_pipeline_id REFERENCES pipelines(id) ON DELETE CASCADE` and `run_job_name`, an exact-one check, and two partial unique indexes. Hydrate a run job/build's base-template ID through `pipeline_runs`, then pass the value object through DB lookup/init/clear. Preserve ordinary SQL and keys; derive a base-template cache scope only for a literal job name.

- [ ] **Step 4: Verify GREEN**

Run `gofmt` on changed Go files, then `ginkgo ./atc`, `ginkgo ./atc/db/migration`, and `ginkgo ./atc/db` sequentially.

Expected: all suites pass.

- [ ] **Step 5: Commit**

Run: `git add atc/task_cache_identity.go atc/task_cache_identity_test.go atc/db && git commit -m "feat(db): persist shared run task caches"`

### Task 8: Cache propagation and build-event routing

**Files:**
- Create: `atc/db/migration/migrations/1773105508_skip_run_payload_event_partitions.up.sql`, `atc/db/migration/migrations/1773105508_skip_run_payload_event_partitions.down.sql`, `atc/db/migration/pipeline_run_events_test.go`
- Modify: `atc/db/build.go` and focused tests
- Modify: `atc/engine/builder.go`, `atc/exec/step_metadata.go`, `atc/exec/task_step.go`, `atc/runtime/types.go`, `atc/runtime/runtimetest/volume.go`, and focused tests
- Modify: `atc/worker/jetbridge/storage.go`, `atc/worker/jetbridge/storage_daemonset.go`, `atc/worker/jetbridge/container.go`, `atc/worker/jetbridge/volume.go`, `atc/worker/jetbridge/volume_daemonset.go`, and focused tests

**Interfaces:**
- Consumes: `atc.TaskCacheIdentity` and stamped build run identity.
- Produces: `Build.TaskCacheIdentity() (atc.TaskCacheIdentity, bool)`; `exec.StepMetadata.TaskCacheIdentity *atc.TaskCacheIdentity`; `runtime.ContainerSpec.TaskCacheIdentity *atc.TaskCacheIdentity`; `runtime.Volume.InitializeTaskCache(context.Context, atc.TaskCacheIdentity, string, string, bool) error`; JetBridge `CacheVolume(string, atc.TaskCacheIdentity, string, string) corev1.Volume`.

- [ ] **Step 1: Write failing end-to-end tests**

Assert engine-to-task-to-volume identity equality; ordinary JetBridge key bytes remain `job-<id>-...`; run ID and ephemeral job ID never affect a run key; template/name changes do; daemonset/direct hostPath agree; each mount resolves to a pod volume; no cache identity becomes a `BUILD_*` variable; inserting a payload creates no `pipeline_build_events_<id>` table; populated down refuses; run job events use the team partition before/after reclaim; check and ordinary routes stay unchanged.

- [ ] **Step 2: Run and observe RED**

Run sequentially: `ginkgo --focus='run payload event partition' ./atc/db/migration`, `ginkgo --focus='Task cache identity propagation|registers cache volumes' ./atc/engine ./atc/exec`, `ginkgo --focus='Task cache identity|CacheVolume|InitializeTaskCache' ./atc/worker/jetbridge`, then `ginkgo --focus='run build events' ./atc/db`.

Expected: raw job IDs diverge across runs and events choose payload partitions.

- [ ] **Step 3: Carry one opaque value**

Replace raw job-ID cache arguments end-to-end without adding parallel run fields. Branch exactly once when rendering DB/hostPath keys. Route stamped non-check builds to the team event table and replace the pipeline-insert trigger so it skips rows with `pipeline_run_id`; preserve ordinary table creation.

- [ ] **Step 4: Verify GREEN**

Run `gofmt` on listed files, then run `ginkgo ./atc/db/migration`, `ginkgo ./atc/engine`, `ginkgo ./atc/exec`, `go test ./atc/runtime/...`, `ginkgo ./atc/worker/jetbridge`, and `ginkgo ./atc/db` sequentially.

Expected: all suites pass; every tested mount has a matching volume.

- [ ] **Step 5: Commit**

Run: `git add atc/db atc/engine atc/exec atc/runtime atc/worker/jetbridge && git commit -m "feat(worker): share run task cache identity"`

### Task 9: Atomic reclamation, mutation guards, and team purge

**Files:**
- Create: `atc/db/migration/migrations/1773105509_guard_run_payload_deletion.up.sql`, `atc/db/migration/migrations/1773105509_guard_run_payload_deletion.down.sql`, `atc/db/migration/pipeline_run_delete_guard_test.go`
- Create: `atc/db/pipeline_run_reclaim.go`, `atc/db/pipeline_run_reclaim_test.go`
- Create: `atc/gc/pipeline_run_reclaimer.go`, `atc/gc/pipeline_run_reclaimer_test.go`
- Modify: `atc/db/pipeline_lifecycle.go`, `atc/db/pipeline.go`, `atc/db/team.go`, focused tests, `atc/component.go`, `atc/atccmd/command.go`

**Interfaces:**
- Consumes: terminal header, run lock, team-event build ownership, run cache FK.
- Produces:

```go
type PipelineRunReclaimLifecycle interface {
	ReclaimCandidateRunIDs(limit int) ([]int, error)
	DestroyReclaimableRun(runID int) (bool, error)
	DeferRunReclaim(runID int, retryAt time.Time) error
}
```

- [ ] **Step 1: Write failing reclaim and purge tests**

Cover absent policy, independent `keep_last OR ttl_days`, indexed bounded candidates, template-then-run recheck, policy withdrawal/reopen/admission races, active job-build blocker versus disposable checks, atomic detach/delete rollback, retained labels/rerun/drain/team, five-minute retry fairness, payload absence, structural delete rejection, generic set/rename/archive/destroy conflicts, template archive, template destroy restriction, and complete team purge/event cleanup.

- [ ] **Step 2: Run and observe RED**

Run sequentially: `ginkgo --focus='payload delete guard' ./atc/db/migration`, `ginkgo --focus='Pipeline run reclamation|payload delete guard|team purge.*runs' ./atc/db`, then `ginkgo --focus='PipelineRunReclaimer' ./atc/gc`.

Expected: missing lifecycle/reclaimer and unrestricted payload deletion.

- [ ] **Step 3: Implement bounded reclaim**

Use separate number/age candidate queries, union/dedupe IDs, and skip future `reclaim_retry_after`. Recheck policy under template then canonical run locks, detach builds, and delete child in one transaction. After rollback on candidate error, persist exactly `now()+5m` separately and continue the batch. Add a `BEFORE DELETE` guard with transaction-local team-purge bypass; team delete removes run builds, caches, payloads, headers, then the ordinary team.

- [ ] **Step 4: Verify GREEN**

Run `gofmt`, then `ginkgo ./atc/db/migration`, `ginkgo ./atc/db`, `ginkgo ./atc/gc`, and `go test ./atc/atccmd -run '^$'` sequentially.

Expected: suites pass; an injected reclaim error leaves the original child/build links.

- [ ] **Step 5: Commit**

Run: `git add atc/db atc/gc atc/atccmd && git commit -m "feat(gc): reclaim completed run payloads"`

### Task 10: Cross-run build-log retention

**Files:**
- Create: `atc/db/pipeline_run_logs.go`, `atc/db/pipeline_run_logs_test.go`
- Modify: `atc/db/pipeline.go`, `atc/db/job.go`, `atc/db/build.go`, and focused tests
- Modify: `atc/gc/build_log_collector.go`, `atc/gc/build_log_collector_test.go`

**Interfaces:**
- Consumes: retained `run_job_key`, current base jobs, existing `BuildLogRetentionCalculator` and `first_logged_build_id`.
- Produces: `Pipeline.ChronoRunBuilds(string, Page) ([]BuildForAPI, Pagination, error)` and `Pipeline.DeleteRunBuildEventsByBuildIDs([]int) error`.

- [ ] **Step 1: Write failing collector tests**

Prove multiple runs share one policy-key budget, including history larger than `batchSize`; dynamic names group by source key and reclaimed rows display `run_job_name`; current tightening deletes and loosening cannot restore; absent keys keep logs and reappearing keys resume policy; count/days/min-success/running/drained match ordinary behavior; reclaimed and late-drained builds remain discoverable; team events are deleted; paused templates suspend; cursor never regresses or skips; ordinary collector behavior is unchanged.

- [ ] **Step 2: Run and observe RED**

Run sequentially: `ginkgo --focus='Run build log query|team-partition run events' ./atc/db`, then `ginkgo --focus='Build Log Collector.*numbered runs|Build Log Collector' ./atc/gc`.

Expected: collector sees only ephemeral job IDs and cannot find reclaimed builds.

- [ ] **Step 3: Add a narrow query adapter**

Extract one retention-decision walk from `reapLogsOfJob` and reuse it for ordinary and run histories; do not duplicate the count/day/success/drain algorithm. Accumulate all query pages before applying one cross-run budget, use the base job cursor/config, hydrate detached display names from `run_job_name`, delete only team-partition rows, and keep unknown policy keys untouched.

- [ ] **Step 4: Verify GREEN**

Run `gofmt`, then `ginkgo ./atc/db` and `ginkgo ./atc/gc` sequentially.

Expected: full suites pass, including ordinary retention scenarios.

- [ ] **Step 5: Commit**

Run: `git add atc/db atc/gc && git commit -m "feat(gc): retain run logs by current template policy"`

### Task 11: Wire models and real presenters

**Files:**
- Modify: `atc/pipeline.go`, `atc/pipeline_test.go`, `atc/pipeline_run.go`
- Create: `atc/api/present/pipeline_run.go`, `atc/api/present/pipeline_run_test.go`
- Modify: `atc/api/present/pipeline.go`, `atc/api/present/pipelines.go`, and focused tests
- Modify: `atc/api/pipelineserver/get.go`, `list.go`, `list_all.go`, and focused tests

**Interfaces:**
- Consumes: Task 3 models and actual child ref/publicity.
- Produces: `Pipeline.Template *bool`, `Pipeline.RunNumber *int`, `Pipeline.RunTemplateRef *PipelineRef`, `Pipeline.ParamsSchema *[]ParamSchema`, `Pipeline.LastRunNumber *int`, `Pipeline.CanCreateRun *bool`; presence-bearing authorized `PipelineRun.Params` and `ConfigHash`; `CreatePipelineRunRequest{Vars map[string]any}`.

- [ ] **Step 1: Write the failing presenter matrix**

Assert exact JSON for base `template:true,last_run_number:0,can_create_run:false,params_schema:[]`, payload `template:false`/number/current base ref, and ordinary `{run:N}` with template-only fields absent. Prove public omission versus authorized empty run params/schema/hash, a renamed base with old child name, actual child ref only when enterable, viewer/operator false versus member/owner/admin true capability independent of pause/archive, and distinct reclaimed versus live-inaccessible records. Use real DB models; never force `template=true` on payload fixtures.

- [ ] **Step 2: Run and observe RED**

Run: `ginkgo --focus='Pipeline presenter matrix|Pipeline run presenter' ./atc ./atc/api/present ./atc/api/pipelineserver`

Expected: wire fields and presenter are missing.

- [ ] **Step 3: Implement explicit presentation options**

Use pointer fields to distinguish absent from false. Pass separate `authorizedForParams`, `canCreateRun`, and `canEnterPayload` decisions into presenters; derive reclaimed solely from child absence and never synthesize `instance_ref`.

- [ ] **Step 4: Verify GREEN**

Run `gofmt`, then `ginkgo ./atc`, `ginkgo ./atc/api/present`, and `ginkgo ./atc/api/pipelineserver` sequentially.

Expected: presenter matrix passes exactly.

- [ ] **Step 5: Commit**

Run: `git add atc/pipeline.go atc/pipeline_test.go atc/pipeline_run.go atc/api/present atc/api/pipelineserver && git commit -m "feat(api): present pipeline run identity"`

### Task 12: Run REST API and go-concourse client

**Files:**
- Create: `atc/api/pipelinerunserver/server.go`, `create.go`, `list.go`, `get.go`, and focused tests
- Create: `atc/api/pipeline_runs_test.go`
- Modify: `atc/routes.go`, `atc/api/handler.go`, `atc/api/api_suite_test.go`, `atc/api/accessor/roles.go`, `atc/wrappa/api_auth_wrappa.go`, `atc/wrappa/reject_archived_wrappa.go`, `atc/atccmd/command.go`, and focused tests
- Modify with focused error tests: `atc/api/configserver/save.go`, `atc/api/pipelineserver/build.go`, `delete.go`, `archive.go`, `rename.go`, `unpause.go`, `atc/api/jobserver/create_build.go`, `rerun_build.go`, `clear_task_cache.go`
- Create: `go-concourse/concourse/pipeline_runs.go`, `go-concourse/concourse/pipeline_runs_test.go`
- Modify: `go-concourse/concourse/team.go`, `jobs_test.go`, `fly/commands/clear_task_cache_test.go`

**Interfaces:**
- Consumes: `PipelineRunFactory`, Task 11 presenters.
- Produces:

```go
CreatePipelineRun(pipelineName string, vars map[string]any) (atc.PipelineRun, error)
PipelineRuns(pipelineName string, page Page) ([]atc.PipelineRun, Pagination, error)
PipelineRun(pipelineName string, number int) (atc.PipelineRun, bool, error)
```

- [ ] **Step 1: Write failing route and client tests**

Assert POST body/path/201 and committed actual ref plus factory-error mapping; list newest-first/default 50 with `from/to/limit` and pagination headers; durable detail/404; public redaction/private denial/private-payload ref omission; archived GET and 409 POST; non-template/paused/archived 409; invalid params 400; fresh ephemeral requests after reclaim get 404 while hydrated races get 409; set/rename/archive/delete/one-off/direct-unpause conflicts map to 409; ambiguous interpolated-base cache clear maps to actionable 409 through API/client/Fly while literal/run clears work; structural switches match nonzero; client preserves typed params, found Boolean, and error bodies.

- [ ] **Step 2: Run and observe RED**

Run sequentially: `ginkgo --focus='Pipeline Runs API' ./atc/api`, `ginkgo --focus='Pipeline run routes' ./atc/wrappa ./atc/api/accessor`, then `ginkgo --focus='Pipeline runs client' ./go-concourse/concourse`.

Expected: route constants and client methods are absent.

- [ ] **Step 3: Implement the collection surface**

Inject `PipelineRunFactory` through `api.NewHandler` and ATC wiring; register POST/list/detail once. Authorize POST as member plus ordinary team access/archive rejection; authorize GET as viewer through durable template access and allow archived history. Map shared typed domain errors to 400/409/404 in all listed handlers. Make the client accept base names, encode keyset pages, and preserve scalar JSON types.

- [ ] **Step 4: Verify GREEN**

Run `gofmt`, then `ginkgo ./atc/api`, `ginkgo ./atc/wrappa`, `ginkgo ./atc/api/accessor`, `ginkgo ./go-concourse/concourse`, `ginkgo --focus='ClearTaskCache' ./fly/commands`, and `go test ./atc/atccmd -run '^$'` sequentially.

Expected: all suites pass and existing pipeline routes remain unchanged.

- [ ] **Step 5: Commit**

Run: `git add atc/routes.go atc/api atc/wrappa atc/atccmd/command.go go-concourse/concourse fly/commands/clear_task_cache_test.go && git commit -m "feat(api): expose pipeline run collections"`

### Task 13: Fly `run-pipeline` and `runs`

**Files:**
- Create: `fly/commands/internal/flaghelpers/json_variable_pair_flag.go`, `json_variable_pair_flag_test.go`
- Create: `fly/commands/run_pipeline.go`, `fly/commands/run_pipeline_test.go`, `fly/commands/runs.go`, `fly/commands/runs_test.go`
- Modify: `fly/commands/fly.go`

**Interfaces:**
- Consumes: Task 12 go-concourse methods.
- Produces: `run-pipeline -p NAME [-v NAME=STRING] [--json-var NAME=JSON] [--team]`; `runs -p NAME [-c COUNT] [--team]`. Local narrow client interfaces, `io.Writer`, selected target/team URL data, and injected `now func() time.Time` keep tests deterministic without faking `concourse.Team`.

- [ ] **Step 1: Write failing command tests**

Assert `-v` stays string, strict `--json-var` normalizes numbers to `float64` and survives client marshal/server decode/validation, JSON value wins duplicate names, instanced refs and `-c <= 0` make zero calls, non-default team plus escaped returned instance vars form the ordinary payload URL with no fallback, `runs -c N` sends `Page{Limit:N}`, and an injected clock fixes running/completed durations. Assert API errors pass through unchanged.

- [ ] **Step 2: Run and observe RED**

Run sequentially: `ginkgo --focus='JSON variable pair' ./fly/commands/internal/flaghelpers`, then `ginkgo --focus='RunPipelineCommand|RunsCommand' ./fly/commands`.

Expected: flags and commands are unregistered.

- [ ] **Step 3: Implement the two thin commands**

Reject `PipelineFlag.InstanceVars` and non-positive count before target/client calls, merge variables with typed JSON precedence, call the base-name client methods, and render only the selected target/team plus server-returned `PipelineRef.QueryParams()`.

- [ ] **Step 4: Verify GREEN**

Run `gofmt`, then `ginkgo ./fly/commands/internal/flaghelpers` and `ginkgo ./fly/commands`.

Expected: both suites pass.

- [ ] **Step 5: Commit**

Run: `git add fly/commands && git commit -m "feat(fly): create and list pipeline runs"`

### Task 14: Elm wire model, routes, and effects

**Files:**
- Create: `web/elm/src/Concourse/PipelineRun.elm`
- Modify: `web/elm/src/Concourse.elm`, `web/elm/src/Routes.elm`, `web/elm/src/Api/Endpoints.elm`
- Modify: `web/elm/src/Message/Effects.elm`, `web/elm/src/Message/Callback.elm`
- Create/modify: `web/elm/tests/PipelineRunTests.elm`, `RoutesTests.elm`, `ApiEndpointsTests.elm`, `SerializationTests.elm`, `Data.elm`

**Interfaces:**
- Consumes: Task 11/12 JSON and keyset pagination.
- Produces:

```elm
type alias PipelineRun =
    { id : Int, number : Int, status : BuildStatus
    , params : Dict String JsonValue, createdBy : Maybe String
    , createdAt : Posix, completedAt : Maybe Posix
    , reclaimed : Bool, instanceRef : Maybe PipelineIdentifier }

type Route
    = PipelineRuns { id : PipelineIdentifier, page : Maybe Pagination.Page }
    | PipelineRun { template : PipelineIdentifier, number : Int }
```

Also add `ParamType = StringParam | NumberParam | BoolParam | EnumParam`, a `ParamSchema` record carrying name/type/required/default/values/description, `FetchPipelineRuns PipelineIdentifier Pagination.Page`, `FetchPipelineRun PipelineIdentifier Int`, and `CreatePipelineRun PipelineIdentifier InstanceVars`. Pipeline fields are `template : Maybe Bool`, `runNumber : Maybe Int`, `runTemplateRef : Maybe PipelineIdentifier`, `paramsSchema : List ParamSchema`, `lastRunNumber : Maybe Int`, and `canCreateRun : Bool`.

- [ ] **Step 1: Write failing decoder/route/effect tests**

Prove `/runs` and `/runs/N` round-trip including `from/to/limit`; ordinary `?vars=run:N` stays ordinary; the three-row pipeline matrix and authoritative base ref decode; run `"running"` maps to `BuildStatusStarted` and terminal strings map exactly; actual `instance_ref` is optional; numeric/Boolean params remain typed; pre-feature cached pipelines default every new field safely and new caches round-trip; and each effect builds the exact endpoint/method/body.

- [ ] **Step 2: Run and observe RED**

Run: `cd web/elm && ../../node_modules/.bin/elm-test tests/RoutesTests.elm tests/PipelineRunTests.elm tests/ApiEndpointsTests.elm tests/SerializationTests.elm`

Expected: Elm compile failures for route, model, and effect constructors.

- [ ] **Step 3: Implement explicit identity**

Extend `Concourse.Pipeline` with the exact nullable protocol fields above, add the focused decoder module and custom run-status conversion, and add route/effect constructors. This task only decodes identity; Task 16 owns canonical navigation. Keep its handwritten production delta at or below 350 lines.

- [ ] **Step 4: Verify GREEN**

Run the focused command again, then `make test-elm` and `git diff --numstat HEAD -- web/elm/src | awk '{added += $1} END {print added+0}'`.

Expected: all Elm tests pass and the Task 14 production delta is at most 350 added lines.

- [ ] **Step 5: Commit**

Run: `git add web/elm && git commit -m "feat(web): decode and route pipeline runs"`

### Task 15: Elm run history and typed creation form

**Files:**
- Create: `web/elm/src/PipelineRuns/PipelineRuns.elm`, `web/elm/src/PipelineRuns/RunForm.elm`, `web/elm/src/PipelineRuns/Styles.elm`
- Modify: `web/elm/src/Message/Message.elm`, `web/elm/src/SubPage/SubPage.elm`, `web/elm/src/Application/Application.elm`
- Create/modify: `web/elm/tests/PipelineRunsTests.elm`, `RunFormTests.elm`, `SubPageTests.elm`, `Data.elm`

**Interfaces:**
- Consumes: Task 14 models/effects and existing `RemoteData`, pager, page shell, sidebar, breadcrumbs, status colors, and duration formatter.
- Produces:

```elm
PipelineRuns.init : { id : PipelineIdentifier, page : Maybe Pagination.Page } -> ( Model, List Effect )
RunForm.init : List ParamSchema -> Model
RunForm.set : String -> String -> Model -> Model
type alias ValidationError = { fieldId : Maybe String, message : String }
RunForm.encode : List ParamSchema -> Model -> Result ValidationError InstanceVars
```

- [ ] **Step 1: Write failing page/form tests**

Cover template plus newest 50 fetch, route-driven pagination, explicit empty start action without auto-open, loading/error-retry/private/not-found states, no unauthorized form, authorized paused/archived editable values with submit-only hold, string/number/Boolean/typed-enum encoding, pending disable and `aria-busy`, first-invalid-field versus server-error focus, labels/`aria-describedby`/mounted live errors, semantic scoped headers, named pagination and “run #N” links with native focus, 400/409 value retention plus template refresh, 201 navigation, links only with `instanceRef`, and inert numbers with no href/role/tab stop/link styling. Assert no search, facets, jump, prefill, Fly preview, or auto-open control.

- [ ] **Step 2: Run and observe RED**

Run: `cd web/elm && ../../node_modules/.bin/elm-test tests/RunFormTests.elm tests/PipelineRunsTests.elm tests/SubPageTests.elm`

Expected: missing page/form modules.

- [ ] **Step 3: Implement the lean page**

Keep coercion exclusively in `RunForm`; reuse the existing pager and page chrome. Keep `PipelineRuns.elm <= 400`, `RunForm.elm <= 150`, and `Styles.elm <= 150` lines. Mount one `aria-live` error region from initialization and preserve model values through conflicts.

- [ ] **Step 4: Verify GREEN**

Run the focused command, `make test-elm`, `wc -l web/elm/src/PipelineRuns/*.elm`, and `git diff --numstat HEAD -- web/elm/src | awk '{added += $1} END {print added+0}'`.

Expected: tests pass, each file meets its cap, and the Task 15 production delta is at most 750 added lines.

- [ ] **Step 5: Commit**

Run: `git add web/elm && git commit -m "feat(web): add pipeline run history and form"`

### Task 16: Elm run context, template chrome, and whole-feature verification

**Files:**
- Create: `web/elm/src/Views/RunContext.elm`
- Modify: `web/elm/src/Pipeline/Pipeline.elm`, `Pipeline/Styles.elm`, `web/elm/src/Views/TopBar.elm`
- Modify: `web/elm/src/SubPage/SubPage.elm`, `web/elm/src/Application/Application.elm`
- Modify: `web/elm/src/Dashboard/Pipeline.elm`, `Dashboard.elm`, `Dashboard/Group.elm`, `Dashboard/Group/Models.elm`, `Dashboard/Models.elm`
- Modify: `web/elm/src/SideBar/Pipeline.elm`, template grouping in `web/elm/src/Concourse.elm`
- Create/modify: `web/elm/tests/RunContextTests.elm`, `PipelineCardTests.elm`, `SideBarFeature.elm`, `TopBarTests.elm`, `PipelineGroupingTests.elm`
- Generate once: `web/public/elm.min.js`

**Interfaces:**
- Consumes: Task 14 run identity and Task 15 history route.
- Produces: `Views.RunContext.Context = Live PipelineRun Pipeline | Completed PipelineRun Pipeline | RecordOnly PipelineRun | Reclaimed PipelineRun`; `Pipeline.initRun : { template : PipelineIdentifier, number : Int } -> (Model, List Effect)`; existing pipeline view accepts `Maybe Context`.

- [ ] **Step 1: Write failing context/chrome tests**

Prove detail fetches durable header then its returned actual ref; header 404 is Not Found, 401 uses the login flow, and network/5xx retries; payload direct URLs canonicalize from `runNumber + runTemplateRef`, including renamed bases; ordinary run-shaped instances stay stock; live context uses normal page; completed hides pause/internal reason; reclaimed has no pipeline subpage; live-inaccessible is not called reclaimed and does not retry known 401; child 404 refetches the header to resolve a reclaim race while network/5xx retries; templates render one neutral labelled card/row linked to history with count/“no runs” plus pause/archive and no synthetic status; mixed refresh payloads render none; base stays selected on list and detail; dashboard sends no recent-run fetch; every status has text.

- [ ] **Step 2: Run and observe RED**

Run: `cd web/elm && ../../node_modules/.bin/elm-test tests/RunContextTests.elm tests/PipelineCardTests.elm tests/SideBarFeature.elm tests/TopBarTests.elm tests/PipelineGroupingTests.elm`

Expected: missing run-context states and payloads still appear in chrome.

- [ ] **Step 3: Implement by adapting existing views**

Initialize the pretty route as header fetch -> reclaimed/record-only, or returned `instanceRef` fetch -> live/completed; on child 404 refetch the header once. Parameterize the existing pipeline page/card/footer/row; do not create parallel dashboard components. Keep `RunContext.elm <= 200` lines, this task below 475 lines, and total Elm production below 1,575 lines. Use explicit markers/refs for every branch.

- [ ] **Step 4: Run complete verification**

Run sequentially:

```bash
(cd web/elm && ../../node_modules/.bin/elm-test tests/RunContextTests.elm tests/PipelineCardTests.elm tests/SideBarFeature.elm tests/TopBarTests.elm tests/PipelineGroupingTests.elm)
yarn run build-elm
make test-elm
make test-unit
git diff --check
git diff --numstat cb6b1ef3c4244651a4ff29d790f08514588e5b71
git diff --numstat HEAD -- web/elm/src | awk '{added += $1} END {print added+0}'
git diff --numstat cb6b1ef3c4244651a4ff29d790f08514588e5b71 -- web/elm/src | awk '{added += $1} END {print added+0}'
```

Expected: focused and full suites pass; bundle builds once; no whitespace errors; Task 16 is at most 475 and total Elm production at most 1,575 added lines; handwritten shipping/test totals and every new production file remain within Global Constraints. If local PostgreSQL or the macOS-incompatible Kubernetes tier is unavailable, record that exact environmental limitation and run every unaffected tier; do not substitute plain `go test` for DB packages.

- [ ] **Step 5: Commit the final UI and generated bundle**

Run: `git add web/elm web/public/elm.min.js && git commit -m "feat(web): show numbered run context"`

- [ ] **Step 6: Request final review and inspect compactness**

Run the `superpowers:requesting-code-review` workflow against the spec, then compare `git diff --stat cb6b1ef3c4244651a4ff29d790f08514588e5b71...HEAD` with the rejected branch. Reject duplicate identity derivation, second lifecycle state machines, per-template dashboard fetches, or a new production file over 500 lines before declaring completion.
