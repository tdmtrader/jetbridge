# Compact Pipeline Templates and Numbered Runs Design

## 1. Purpose

Add pipeline templates and numbered, parameterized runs to Concourse without
creating a second scheduler, checker, or retention system.

A template is a base pipeline configuration that declares run parameters and
optional shell retention. Starting a run materializes an ordinary instanced
pipeline at `{run: N}`. While active, that payload uses normal Concourse
checking, scheduling, build execution, and resource semantics. A small durable
run header supplies numbering, lifecycle, history, and ownership after the
payload is reclaimed.

This design replaces the implementation on
`claude/pipeline-templates-numbered-runs-705e46`. That branch is a behavioral
reference, not a code source. The implementation starts from commit
`cb6b1ef3c4244651a4ff29d790f08514588e5b71` and must not copy its initialization
ledger, frozen retention policy, reverse ownership link, or broad UI.

## 2. Design rule

Normal Concourse behavior wins unless numbered runs require a distinct durable
identity.

The feature-specific seams are limited to:

1. Template validation, run parameter validation, and materialization.
2. Atomic run-number allocation and creation.
3. Durable run ownership and lifecycle locking.
4. Run completion, manual reopen, and payload reclamation.
5. Run collection APIs, Fly commands, and the lean UI.
6. Team-partition event routing and template-scoped task-cache identity.
7. Applying ordinary build-log retention across builds of the same logical job
   in multiple runs.

The feature does not introduce bespoke resource initialization, retries,
version eligibility, historical-policy reconstruction, or a second scheduling
queue.

## 3. Product scope

### 3.1 Included

- `template: true` on a base pipeline.
- Typed run parameters: `string`, `number`, `bool`, and typed `enum`.
- Required values, server-side defaults, descriptions, and enum values.
- Reserved `((run))` and `((run_id))` interpolation.
- Monotonic run numbers local to a template.
- A globally unique durable run ID.
- One disposable pipeline payload per unreclaimed run.
- Normal resource checking and passed-constraint scheduling.
- Entry builds created atomically with the run.
- Run status derived from job builds.
- Manual trigger/rerun as the only terminal-to-running transition.
- `run_retention.keep_last` and `run_retention.ttl_days` for payload shells.
- Existing job build-log policies applied across numbered runs.
- Run list/detail/create APIs and Fly support.
- A lean runs page and compact live/completed/reclaimed context.

### 3.2 Deferred

- Search grammar, facets, and jump-to-run.
- Re-run parameter prefill and convenience chips.
- Fly command previews in the web UI.
- Dashboard recent-run strips.
- Previous/next run probing.
- Automatic form opening.
- A reclaimed historical DAG or jobs that produced no builds.
- An explicit purge-history operation.

### 3.3 Explicitly removed from the prior implementation

- Frozen initial resource checks and `pipeline_run_resource_inits`.
- Retry budgets, leases, claims, exhaustion, and initialization diagnostics.
- Resource-version mutation locks and scope-wide advisory locks.
- Frozen per-run build-log policy manifests.
- Per-run log watermarks and per-template retention walks/generations.
- Durable `pipeline_run_jobs` rows.
- Reclaim summary JSON and a reclaim state machine.
- Inferring run identity from `template` plus `{run: N}`.

An input that never resolves leaves the run running because terminal build
evidence for an expected job is missing, as in an ordinary Concourse pipeline.
It does not create a placeholder build and does not auto-error.

## 4. Configuration

The additive config shape is:

```yaml
template: true

params:
- name: environment
  type: enum
  values: [staging, production]
  required: true
- name: dry_run
  type: bool
  default: false

run_retention:
  keep_last: 20
  ttl_days: 30
```

Template-only fields are rejected on ordinary pipelines. A template must have
at least one entry job, defined as a job with no `passed:` constraint on any
input. Parameter names are unique and cannot be `run` or `run_id`. Defaults and
enum members must match the declared scalar type. Retention values must be
positive and bounded so PostgreSQL timestamp arithmetic cannot overflow.

Run request values are validated server-side. Unknown names and missing
required values are errors. Defaults are applied before values are stored or
interpolated. Number and Boolean strings are accepted from Fly and coerced;
JSON API values retain their scalar types. Enum comparison is type-aware.

All normal config strings may interpolate run params. `((run))` is the local
number and `((run_id))` is the durable `pipeline_runs.id`; both override
same-named user input. Unresolved variables remain for normal runtime var
sources.

Step-level version pins, interpolated job/step names, and interpolated cache
paths remain legal. Their behavior follows normal Concourse: changing an
identity-bearing value may create a distinct displayed job or cache scope. A
run job also retains its unmaterialized job name as a policy key so dynamic
display names still share the template job's current log policy.

Materialization changes only three things:

1. Resolve run params, `run`, and `run_id`.
2. Set the persisted payload config's `template` field to false.
3. Clear `trigger: true` on gets without `passed:` so external resource
   versions cannot start extra builds inside a run. Triggered gets with
   `passed:` retain their normal downstream scheduling behavior.

## 5. Data model and identity

### 5.1 Pipeline columns

Add to `pipelines`:

- `template BOOLEAN NOT NULL DEFAULT false`
- persisted parameter schema
- nullable `run_retention_keep_last` and `run_retention_ttl_days`
- `last_run_number INTEGER NOT NULL DEFAULT 0`
- `pipeline_run_id BIGINT NULL REFERENCES pipeline_runs(id) ON DELETE RESTRICT`

`pipeline_run_id` has a partial unique index for non-null values. A same-row
check requires a run payload to be non-template and instanced. The same check
requires every base template to have null instance vars: instanced templates
are not addressable by the run collection API and are rejected at config save.
Once non-null, the ownership value is immutable.

Classification is exact:

- `template = true` and `pipeline_run_id IS NULL`: base template.
- `pipeline_run_id IS NOT NULL`: numbered-run payload.
- otherwise: ordinary pipeline, including an ordinary instance whose vars
  happen to contain a `run` key.

Normal pipeline lists, dashboard queries, and sidebar queries exclude payloads
with `pipeline_run_id IS NOT NULL`. Direct lookup by the `{run: N}` pipeline
reference remains available while the payload exists.

Fly and API clients that operate on a template's run collection reject a
`PipelineRef` containing instance vars rather than dropping them.

### 5.2 Durable run header

Create `pipeline_runs` with:

- `id BIGSERIAL PRIMARY KEY`
- `template_pipeline_id INTEGER NOT NULL REFERENCES pipelines(id) ON DELETE RESTRICT`
- `number INTEGER NOT NULL`
- `params JSONB NOT NULL`
- `status` in `running|succeeded|failed|errored|aborted`
- `created_by`, `created_at`, and nullable `completed_at`
- nullable `reclaim_retry_after`
- immutable materialized-config hash
- unique `(template_pipeline_id, number)`

The hash is lowercase hexadecimal SHA-256 over
`"run-instance-config/v1\x00" || canonicalJSON(materializedConfig)`. Canonical
JSON uses the typed config's deterministic `encoding/json` representation,
which sorts string map keys and preserves array order and scalar types.

The header has no physical `instance_pipeline_id`. Normal run queries left
join the unique child and hydrate its ID; creation hydrates the new payload ID
directly in `RunCreation` before `BeforeCommit`. The compatibility accessor
returns only that in-memory value and never opens a query on another connection,
which could not see the caller's uncommitted payload. Payload presence means
unreclaimed; payload absence means reclaimed.

A deferred constraint-trigger function requires every `running` header to own
exactly one payload at commit. Attach it as an initially deferred constraint
trigger to relevant `pipeline_runs` inserts/status updates and
`pipelines.pipeline_run_id` inserts/updates/deletes. The partial unique index
and foreign key enforce at most one payload in all states. This permits
header-first insertion within creation and terminal payload deletion within
reclamation without allowing a committed active orphan.

The template foreign key is restrictive: templates with durable run history
cannot be destroyed through the ordinary pipeline delete path. They may be
archived. A future explicit per-template purge can define deletion of headers,
detached builds, and team event rows as one deliberate operation.

### 5.3 Jobs and builds

Add to `jobs`:

- `run_expected BOOLEAN NOT NULL DEFAULT false`
- `run_policy_key TEXT NULL`

Both are meaningful only for jobs in a run payload and disappear with it. The
policy key is the effective config's unmaterialized job name and is immutable.

Add to `builds`:

- `pipeline_run_id BIGINT NULL REFERENCES pipeline_runs(id) ON DELETE RESTRICT`
- `run_job_name TEXT NULL`
- `run_job_key TEXT NULL`

Every run job build is stamped from its actual joined job and pipeline rows,
never caller-provided identity. A check requires the run ID, job name, and job
key to be either all present or all absent. All three fields are immutable.
Index `(pipeline_run_id, run_job_key, id DESC)` where `run_job_key IS NOT NULL`.

Entry, scheduler-created, manual, and rerun job builds carry both values.
Resource/resource-type/prototype checks retain their normal ownership and do
not participate in run status or cross-run job retention. Pipeline-scoped
one-offs against run payloads are refused: they have no durable job identity,
and admitting one would either make completion ignore live work or require a
second retained-build ownership form.

Before payload deletion, retained job builds have ephemeral `job_id` and
`pipeline_id` cleared. Their run ID, materialized job name, policy key, status,
rerun relationship, drain state, and team ownership survive.

These denormalized labels are sufficient because the approved reclaimed view
does not reconstruct jobs that never built or a historical DAG. The
materialized name is presentation; the unmaterialized key joins current
template policy to all dynamic names produced from that job. Renaming the
unmaterialized template job starts a new retention group. Logs for a key absent
from the current template are kept; if that key later reappears, its current
policy applies to the older builds too.

## 6. Creation transaction

`CreateRunInTx` is the single supported creation seam. It must:

1. Use only the caller-supplied transaction and never commit it.
2. Lock and re-read the base template before using identity or config.
3. Read one transaction-consistent effective config, including an optional v4
   override. When present, the override is authoritative for parameter schema,
   materialization, expected jobs, and entry jobs; it need not set
   `template:true` because the locked base row establishes template identity.
4. Validate template shape and run params before allocating a number.
5. Increment `last_run_number` under the template lock. If an ordinary
   pipeline already occupies `{run: N}`, continue until the first free number.
6. Allocate the durable run ID before materialization.
7. Materialize and hash the effective config.
8. Insert the run header.
9. Save the disposable payload with `pipeline_run_id`, including normal jobs,
   resources, resource types, and prototypes.
10. Compute and persist `jobs.run_expected` and `jobs.run_policy_key`.
11. Create pending builds for every entry job in the same transaction.
12. Invoke `BeforeCommit` last, after all rows and requested builds are
    queryable through the caller's transaction.

Any error rolls back the number, header, payload, jobs, and builds. Sequence
IDs may have gaps; committed run numbers do not, except for deliberately
skipped occupied `{run: N}` references.

`RunCreation` exposes the durable run, materialized config, canonical JSON and
hash, entry-job names, and entry builds. `AfterRunCreated` contains only
idempotent post-commit notifications. Ordinary component polling is the crash
recovery path.

Template pause is a hold on creation. Creating a run from a paused or archived
template returns a conflict and allocates no number.

## 7. Checking and scheduling

Base templates are excluded from Lidar, periodic checks, and scheduling by the
explicit `template` flag. Active payloads are non-template pipelines and take
the ordinary paths.

Entry builds are manual pending builds created with the run. Their resources
are checked normally. Missing or invalid resource versions leave the build
pending. Downstream jobs use the normal scheduler and passed constraints.
There is no frozen check set and no run-specific retry component.

The expected-job set prevents a successful false completion without tracking
mutable input debt. It is computed once from the materialized graph:

1. Every entry job is expected.
2. Repeatedly add a downstream job when at least one passed-constrained input
   has `trigger: true` and every job named by all of its passed constraints is
   already expected.
3. Stop at the fixed point.

Disconnected cycles and downstream branches whose passed inputs are entirely
manual remain non-expected.

The scheduler's existing `schedule_requested` and `last_scheduled` timestamps
remain the only transient scheduling debt. A resolved request must create its
pending build before, or atomically with, advancing `last_scheduled`. A newer
concurrent request remains visible because the scheduler advances only to the
token it observed.

Every scheduler pass that consumes a request for a run job invokes the run
completion predicate. The predicate is stateless and no-ops unless the run is
genuinely quiescent, so it is not gated on whether the pass created a build --
a pass that finished its own build in flight would otherwise clear the last
schedule debt with nobody left to notice quiescence. This lets a quiescent
failed run settle promptly; successful coverage still keeps an unresolved
expected job running.

## 8. Lifecycle and concurrency

### 8.1 Lock order

The universal order is:

```text
template row -> durable run row -> payload/job/build rows
```

Most lifecycle operations do not need the template and begin at the run row.
No transaction may acquire a template row after holding a run row.

The durable run-row lock is used by run job build admission, defensive build
start, build finish/completion, manual reopen, and reclamation. Ordinary
pipeline builds perform only the nullable run-identity check.

### 8.2 Build admission

All run job creation paths use one admission seam:

1. Hydrate the payload's durable run ID.
2. Lock the run before job/build mutation.
3. Require `status = running` and a live payload.
4. Insert the build with its run ID and job name.

`Build.Start` repeats the terminal/live check defensively. Because completion
holds the same lock and refuses active builds, no job build can appear or start
after terminalization. This removes lifecycle-refused flags and terminal wedge
healing.

### 8.3 Completion

Build finish requests downstream schedules before invoking completion in the
same run-locked transaction. Completion requires:

- the run is `running` and owns a payload;
- no pending or started run job build exists;
- no active, unpaused payload job has `schedule_requested > last_scheduled`;
- at least one run job build exists.

Status is the worst rerun-aware latest terminal build per job:
`errored > aborted > failed > succeeded`.

If the aggregate is succeeded, every `run_expected` job must have a terminal
job build. Missing evidence leaves the run running. A failed, errored, or
aborted aggregate may complete once quiescent even when failed prerequisites
prevented expected downstream jobs from running.

Terminalization writes status and `completed_at` and pauses the payload with
internal `run-completed` attribution in the same transaction. The UI never
presents this as a user pause. If the payload is already user-paused,
terminalization preserves that attribution rather than overwriting it.

There is no lifecycle sweep. Every transaction that can remove the last
completion blocker—job build finish and consumption of a scheduling request
without a build—invokes the predicate. A newly discovered path must first add
that same hook and a crash-recovery test rather than introduce a scanning work
queue.

### 8.4 Manual reopen

Manual trigger and rerun are the only terminal-to-running doors. They lock the
run, require a live payload, clear `completed_at`, set status to running,
discard scheduling requests accumulated while the payload was completed,
create the requested build, and unpause atomically.

Direct unpause of an automatically completed payload returns conflict. Normal
pause/unpause remains valid while the run is active. A reclaimed run cannot be
reopened.

Generic set, rename, archive, and destroy operations refuse run payloads.
Reclamation is their only ordinary delete path. Pipeline-scoped one-off
creation against a run payload also returns conflict. Resource and prototype
checks keep their normal behavior and are disposable with the payload.

## 9. Events and task caches

Every non-check run job build uses the team's build-event partition from birth.
It continues using that partition after payload reclamation. No per-payload
event table is created. Check and ordinary build routing is unchanged.

This makes reclaim a metadata operation: it never copies events and never
deletes logs.

Ordinary task-cache keys remain byte-for-byte unchanged. Cache identity becomes
one end-to-end discriminated value:

- ordinary: the existing job ID;
- run: team ID, durable base-template pipeline ID, and materialized job name.

The same value flows through step metadata, runtime volumes, DB task-cache
find/create, worker task caches, JetBridge hostPath keys, initialization, and
clear-cache. Per-run IDs and ephemeral run job IDs never enter it.

`task_caches.job_id` becomes nullable and the table gains nullable
`template_pipeline_id REFERENCES pipelines(id) ON DELETE CASCADE` and
`run_job_name`. A check requires exactly one identity form. Separate partial
unique indexes preserve the ordinary
`(job_id, step_name, path)` key and add the run
`(template_pipeline_id, run_job_name, step_name, path)` key. Worker caches
continue to reference the resulting task-cache row.

Clearing a live run job clears the shared cache for that materialized job
across runs. Clearing a literal-named base-template job addresses the same
scope. A base job whose name still contains interpolation cannot identify one
materialized scope and returns a typed conflict directing the caller to a live
run job. Run cache rows remain valid while their base template exists; team
purge removes them.

Dynamic identity-bearing values may intentionally form distinct caches, as
they would after changing an ordinary pipeline config. The broader hostPath
cache reclamation gap is pre-existing and outside this feature.

## 10. Shell reclamation

No declared `run_retention` means keep all payloads. Each declared dimension is
an independent reclaim condition:

- `keep_last`: terminal run numbers older than the newest retained window;
- `ttl_days`: terminal runs completed before the current age cutoff.

A run matching either declared condition is eligible. Candidate queries are
bounded and indexed independently by template/number and
template/completion-time so a TTL-only policy does not scan full history.

For each candidate, one transaction:

1. Locks the base template for shared current-policy inspection.
2. Locks the run.
3. Rechecks current policy, terminal status, payload presence, and the absence
   of pending/started non-check builds.
4. Clears ephemeral `job_id` and `pipeline_id` from retained run job builds.
5. Deletes the payload, cascading its disposable jobs/resources/checks.
6. Commits.

A `BEFORE DELETE` database guard protects payload deletion beneath the
friendly application errors. Outside an explicit team purge, a payload may be
deleted only when its owner is terminal and every retained run job build is
already detached from both job and pipeline. This makes the ordered reclaimer
shape structural rather than conventional.

Payload absence is the only reclaimed marker. A rollback leaves the original
shape. An individual candidate error records a fixed five-minute
`reclaim_retry_after` and continues the batch. Candidate queries skip an
unelapsed deadline, preventing a permanently failing oldest run from
monopolizing every bounded batch without adding attempts or exponential state.

Team destruction is the one authorized purge operation. Its transaction marks
a local privileged purge context, deletes the team's run builds, payloads, and
headers in dependency order, then performs ordinary team deletion; normal team
event-partition cleanup removes all associated logs. The payload delete guard
recognizes only this transaction-local context. Team deletion must therefore
succeed normally rather than expose the run-history foreign key as a 500.

## 11. Build-log retention

The existing build-log collector remains the authority. For a base template,
each current template job's unmaterialized name becomes the logical policy key
across all run builds whose `run_job_key` matches.

The collector:

- reads the job's current config and existing global default/max settings;
- uses the base job's existing `first_logged_build_id` cursor;
- reads matching run builds newest-to-oldest across live and reclaimed runs;
- applies the existing count, days, minimum-success, running, and drain rules;
- deletes matching team-partition event rows;
- advances the existing cursor exactly as for an ordinary job.

There is no frozen historical policy. Tightening current policy can delete old
logs; loosening cannot restore them. Pausing a template suspends its log
collection just as pausing an ordinary pipeline does. A config update racing a
collector page has the same consistency semantics as the existing ordinary
collector; this feature does not add a separate serializable policy protocol.

Reclamation never deletes logs. A late drain update continues to work through
the retained build row and is discovered by the ordinary cursor walk.

## 12. API, authorization, and Fly

Add:

- `POST /api/v1/teams/:team/pipelines/:pipeline/runs`
- `GET /api/v1/teams/:team/pipelines/:pipeline/runs`
- `GET /api/v1/teams/:team/pipelines/:pipeline/runs/:number`

Lists are newest-first and keyset-paginated, with a default limit of 50.

Base-template pipeline responses include `template`, `last_run_number`, the
authorized parameter schema, and `can_create_run`. The capability reports
write permission only. The client combines it with existing paused/archived
fields: unauthorized viewers see no form, while an authorized writer sees a
disabled form and the creation-hold reason. The lean dashboard fetches no run
status summary or recent-run window per card.

A real run payload response includes nullable `run_number` and
`run_template_ref`. These fields, not `template` or instance vars, are
authoritative and let a direct payload URL canonicalize after a template
rename. A run record includes durable ID, number, status, creator/timestamps,
team/template identity, authorized
params, `reclaimed`, and optional `instance_ref`. `instance_ref` is present
only while the payload exists and the viewer may enter it. It is read from the
actual child pipeline rather than synthesized from the template's current
name, so renaming a template does not break links to older live payloads.

Run creation requires the template team's write role. Because a supplied
parameter value is interpolated verbatim into the materialized payload config,
which is then saved and credential-evaluated as an ordinary pipeline, creating a
run carries set-pipeline-equivalent trust: `CreatePipelineRun` may never be
assigned a weaker role than `SaveConfig`, and the ATC refuses such an RBAC
config at startup. List/detail access is based on the durable template. Public
viewers may see structural identity and status but not params, parameter schema,
or config hash. Payloads retain normal pipeline authorization and do not
silently inherit template publicity.

Fly adds:

- `fly run-pipeline -p NAME [-v NAME=STRING] [--json-var NAME=JSON]`
  with the existing `--team` selector;
- `fly runs -p NAME [-c COUNT] [--json]` with the existing `--team` selector,
  rendering the newest page of number, params, status, start, and duration, or
  the whole run records as JSON.

Both commands reject instanced pipeline flags. The authorized creation response
always includes the new payload's actual `instance_ref`; `run-pipeline`
prints its ordinary payload URL without a synthesized fallback. Run IDs and
numbers may have gaps as described in the creation contract.

The pipeline presenter matrix is a protocol invariant:

- base template: `template=true`, no `run_number`;
- real run payload: `template=false`, `run_number=N`;
- ordinary run-shaped instance: neither field.

Directly loading a real payload route canonicalizes to the explicit pretty run
route. The UI does this only when the presenter supplies both `run_number` and
the current base `run_template_ref`; it never infers from instance vars or a
possibly stale payload name.

## 13. Lean web UI

Routes are explicit:

- `/teams/:team/pipelines/:pipeline/runs`
- `/teams/:team/pipelines/:pipeline/runs/:number`

Legacy ordinary pipeline URLs containing `{run: N}` remain ordinary routes.

The runs page fetches the template and one run page. It provides an accessible
typed form to writers and a newest-first table showing run number, textual
status, params, duration/age, and creator. Paused or archived templates retain
the form values but disable submission with the hold reason. A `409` creation
race retains entered values, shows the server message, refetches the template,
and disables another known-invalid submission. A row uses the pretty
`/runs/N` link only when the server supplies `instance_ref`; the ref controls
accessibility, not the link target. Reclaimed or unauthorized numbers have no
link semantics, styling, or tab stop.

Pagination uses the existing `from`, `to`, and `limit` query parameters and
round-trips them through route construction for browser back/forward behavior.

The run-detail route fetches the durable run first and fetches a live payload
only through the returned `instance_ref`. It never rebuilds the payload ref
from the template's current name. The existing pipeline page accepts an
optional explicit run context:

- live: normal pipeline page plus number/status/params/all-runs context;
- completed and unreclaimed: normal page, terminal context, no pause control;
- unreclaimed but inaccessible: compact record stating that the payload is
  unavailable to the viewer, without claiming it was reclaimed;
- reclaimed: compact durable record without a deleted pipeline subpage.

Template/history failures show an error with retry instead of indefinite
loading. Private-template authorization follows the existing login/forbidden
flow. A missing durable run is not found. Payload authorization renders the
inaccessible state; a transient payload failure renders retry without changing
the durable run's reclaimed truth.

Templates have one labelled dashboard card and sidebar row linking to runs.
The neutral card shows `last_run_number`, or “no runs” before the first run,
plus paused/archived state. It does not synthesize status from run history.
The base template row remains selected throughout its runs list/detail routes.
Run payloads have no card or sidebar row, including during refresh races.
Status always has text in addition to color.

The minimum Elm boundaries are:

- `Concourse.PipelineRun`: decoder and status conversion;
- `PipelineRuns.PipelineRuns`: page state, fetch, update, pagination, and view;
- `PipelineRuns.RunForm`: pure initialization/coercion/validation;
- `PipelineRuns.Styles`: page-specific styles using shared primitives;
- `Views.RunContext`: live/completed/reclaimed context;
- small adaptations to routes, dashboard/sidebar, and `Pipeline.Pipeline`.

Forms use labels, native submit behavior, disabled pending state, and an
initially mounted `aria-live` error region. Labels use `for`/input IDs;
descriptions and validation hints use `aria-describedby`; submission sets
`aria-busy`; validation moves focus to the first invalid field or server error.
History uses semantic table headers with `scope`, and run links have names such
as “run #42.” Actions are buttons; navigation is anchors; pagination has
accessible names and visible keyboard focus.

## 14. Error behavior

- Invalid template config or params: `400` with a stable actionable message.
- Creating from a non-template, paused template, or archived template: `409`.
- Generic mutation of a run payload: `409`.
- Direct unpause of a completed run: `409`.
- A trigger/rerun request that already hydrated a run ID and loses a reclaim
  race: `409`. A fresh request after payload deletion follows the normal
  ephemeral pipeline/job route and returns `404`.
- Missing template/run/payload: normal `404` at the corresponding durable or
  ephemeral endpoint.
- Creation callback failure: caller transaction rolls back completely.
- Collector/reclaimer candidate failure: log and continue the bounded batch.

## 15. Testing strategy

All behavior changes follow red-green-refactor. Database-backed tests run with
Ginkgo, never plain `go test`.

The minimum matrix is:

1. Config and param validation, materialization, reserved vars, step pins, and
   dynamic identity values.
2. Migration upgrade, empty-feature down, populated-feature down refusal, and
   structural invariants that assert they matched real rows/files.
3. Creation rollback, number concurrency/collisions, v4 `BeforeCommit`, and
   post-commit notification recovery.
4. Base-template exclusion versus ordinary payload checking/scheduling.
5. Expected-job fixed point: entry, reachable trigger chain, manual branch,
   disconnected cycle, unresolved input, and later version arrival.
6. Completion aggregation and coverage for success/failure/error/abort/rerun.
7. Choreographed build admission versus completion, reopen, and reclaim using
   independent database connections.
8. Atomic reclaim, the structural payload-delete guard, retained build
   identity, payload absence, check cleanup, rollback, and team purge.
9. Cross-run current-policy log retention, count/days/minimum-success, paused
   template, reclaimed builds, and undrained/late-drained builds.
10. Event routing and end-to-end ordinary/run cache identity, persistence,
    initialization, clearing, and compatibility.
11. Real-presenter matrix for ordinary/template/run-shaped/run payloads,
    authorization, pagination, creation conflicts, and optional
    `instance_ref`; fixtures must not force `template=true` on payloads.
12. Fly command request and output.
13. Elm route round trips, decoders, typed form table, creation, pagination,
    row linking, run-context states, dashboard/sidebar identity, error states,
    and accessibility attributes.

Tests should assert public behavior and database invariants rather than mirror
private helpers. Concurrency tests must prove both lock order outcomes.

## 16. Size and structure guardrails

The implementation is budgeted at:

- shipping handwritten code: 5,000-7,000 added lines;
- tests: 7,000-9,000 added lines;
- this spec plus implementation plan: fewer than 1,500 lines;
- migrations: one coherent feature series, without migrations of the rejected
  implementation.

No single new production file should exceed 500 lines. Files near that size
must have one clear responsibility; orchestration, policy, query, and view code
do not accumulate in one module.

If a subsystem exceeds its range or requires a second durable state machine,
implementation pauses for design review. A newly discovered race is first
tested against the shared lifecycle lock or normal Concourse transaction
ordering; it is not answered by adding a feature-specific ledger by default.

## 17. Compatibility decisions

- The feature is unshipped and starts from `core`; there is no rolling upgrade
  from the rejected branch schema and no dual-read/write compatibility code.
- The feature migration can move down only while no template, run header,
  run-stamped build, or run-scoped task cache exists. Its down migration checks
  that condition and raises an actionable error otherwise; reclaimed ownership
  and team-partition logs cannot be converted back losslessly.
- Existing ordinary pipeline API shapes, cache keys, and event routing remain
  unchanged.
- A run payload's persisted `template` value and `GET config` value are false;
  explicit `run_number` carries identity.
- Run numbers are monotonic but not dense when `{run: N}` is preoccupied.
- Durable run IDs are sequence-backed and may gap on rollback.
- Current template policies are retroactive; deleted logs are irreversible.
- Reclaimed runs cannot be reopened and expose no historical DAG in the lean
  version.
- Templates with durable runs are archiveable but not destroyable until a
  separate purge-history contract exists.
