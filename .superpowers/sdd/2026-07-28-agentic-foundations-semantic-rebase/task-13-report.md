# Task 13 implementation report

## Implemented so far

- Persisted source admissions derive their only selection evidence from a
  completed, non-aborted successful `admit` build at the registered pipeline
  config version. Manual admission creates exactly one ordinary manual build;
  automatic admission claims only persisted scheduler input mappings.
- Capture operation identity is canonical, independent of admission ID, and
  sealed Hangar snapshot refs become the sole source inputs used by the
  workflow binder. Runs persist the ready admission ID; retry, replay, and
  resume reuse the same sealed bindings.
- Source-bearing execution uses the opaque `workflow.ExecutionEnvelope` after
  a trusted source-bound render. It accepts neither caller source snapshots
  nor a live source lookup. Promotion validation uses synthetic refs only for
  renderability validation; they are never persisted or passed to execution.
- Promotion now creates one paused ordinary source-selection pipeline in the
  same transaction as the live workflow swap. Registration persists frozen
  source declarations (source name, resource name, snapshot type), drains the
  previous revision, and rejects a repeated activation whose declarations
  differ. A source-free promotion drains the active source pipeline.
- The source lifecycle reconciler unpauses active revisions; draining
  revisions wait for scheduler work, pause in one pass, and physically archive
  only in a later pass after no selecting/capturing admissions remain. Physical
  archive precedes registry archival.
- Experiment `Start` performs a durable, idempotent manual source admission
  once for every distinct immutable definition/source-config identity. Its
  locked transaction then verifies that every admission is ready and exact,
  stores the association before it creates any cells, and refuses missing,
  duplicate, wrong-team, wrong-definition, wrong-hash, or changed
  associations. Candidate/evaluator claims fail closed unless the linked
  source admission exactly matches the workflow run they allocate.
- The experiment runner and evaluator pass that private association through
  the experiment adapter only. The ordinary public `workflowrun.BindRequest`
  remains source-admission-free; a private binder entry point allows an
  experiment child to load an already-ready admission but cannot create a
  manual source build. Source-free experiment children preserve their normal
  behavior.
- ATC command composition now creates exactly one `resourcecapture.Capturer`.
  The resource-capture API and workflow source-capture coordinator share it;
  the normal workflow binder receives the manual-capable source admitter, and
  experiment execution receives a ready-only admitter. The experiment API's
  `Start` store receives the manual source preparer, while the backend runs
  both the source-pipeline lifecycle and automatic successful-build
  reconciler.
- Public pipeline config, rename, pause/unpause/archive/destroy, manual job
  builds/reruns, one-off started builds, and pipeline-run creation all reject
  registered source pipelines. API handlers return conflict responses. The
  ordinary abandoned-pipeline cleaner and idle pauser explicitly exclude them.

## Tests and verification

Passed:

- `go test ./agent/workflowrun -run '^(TestSourcePipelineLifecycle|TestWorkflowTargetRendererValidatesSourceWorkflowForPromotionWithoutRuntimeAdmission)' -count=1`
- `go test ./agent/workflowrun ./agent/experiment -count=1`
- `go test ./atc/atccmd -count=1`
- `go test ./atc/db -run '^$' -count=1`
- `go test ./atc/api/configserver ./atc/api/jobserver ./atc/api/pipelineserver ./atc/api/runserver -run '^$' -count=1`
- `git diff --check`

Added focused DB coverage for atomic source-pipeline promotion, frozen
declaration repeat checks, lifecycle unpause/pause/archive, promotion rollback
when registry activation fails, public manual-build/config guards, and the
source-admission association proof for candidate and evaluator allocation
(nil/wrong identity rejected; exact ready identity retained).

The serial DB command below could not execute any spec because fixed port 5434
was already occupied by external PostgreSQL PID 36839 (cwd
`/private/var/folders/fh/2y32x6hj159cvb0bk26t3kp00000gn/T/concourse-pg-runner/postgres1532805609`):

```
go test ./atc/db -count=1 -ginkgo.focus='(atomically owns source pipelines|unpauses active source pipelines|does not make a workflow live when source-pipeline activation fails|rejects a public manual build for a source-owned pipeline)'
```

The Ginkgo `BeforeSuite` failed before running specs with `could not bind IPv4
address "127.0.0.1": Address already in use`; no source, Git, or cluster data
was changed to clear that external contention. This command remains a Task 13
checkpoint rerun once port 5434 is free.

## Focused DB high-port attempt

On 2026-07-29, one serial rerun of all six Task 13 source-pipeline and
experiment-association specs was attempted on verified-free loopback port
25433. It used a temporary Go `-overlay` replacement of
`atc/postgresrunner/ginkgo.go`; the replacement changed only the test runner
port expression from `5433 + GinkgoParallelProcess()` to
`25432 + GinkgoParallelProcess()`.

```
go test -overlay=.superpowers/sdd/2026-07-28-agentic-foundations-semantic-rebase/task-13-postgresrunner-overlay.json ./atc/db -count=1 -ginkgo.focus='(atomically owns source pipelines and preserves frozen declarations on exact promotion repeats|unpauses active source pipelines and physically archives drained pipelines only after a pause pass|does not make a workflow live when source-pipeline activation fails|rejects a public manual build for a source-owned pipeline without scheduling it|persists one prepared source admission per definition and binds every claimed child to it|keeps a draft unchanged when prepared source admissions do not cover the locked registry)'
```

The attempt did not complete within its bounded completion window and produced
no spec output. Exact process evidence after more than two minutes was:

```
47171 ... go test -overlay=... ./atc/db -count=1 -ginkgo.focus=(...)
47215 47171 ... /var/folders/.../go-build.../db.test -test.timeout=10m0s ...
47226 47215 ... postgres -k /tmp -D /var/folders/.../concourse-pg-runner/postgres1397346673 -h 127.0.0.1 -p 25433
```

The isolated `go test` and `db.test` PIDs (47171 and 47215) were interrupted
after this single attempt; their temporary postgres child exited, as confirmed
by no listener on port 25433. The temporary replacement and overlay were then
removed. Tracked production code and the external PostgreSQL process on port
5434 (PID 36839) were not touched. No further infrastructure retries were
performed.

## Review round 1 correction

- Added the source-only transactional ownership guard for public job
  pause/unpause and resource pin/unpin, enable/disable, and `ClearVersions`
  mutations. It checks the durable
  `agent_workflow_resource_source_pipelines` registration inside the mutation
  transaction and returns `ErrAgentWorkflowResourceSourceImmutable` before
  changing scheduler-selection state. Comments, caches, reads, and other
  unrelated resource operations remain outside this correction.
- Added HTTP conflict mapping for each corresponding job/resource mutation
  handler. Public callers now receive 409 rather than 500 for this durable
  ownership conflict.
- Added focused DB regressions for pausing/unpausing an owned admit job and
  all five source-resource mutations, plus API regressions for the seven 409
  surfaces. The DB tests compile but were not executed: this correction did
  not retry the known occupied-port/high-port database infrastructure.

Passed for this correction:

- `go test ./atc/api -run '^TestAPI$' -ginkgo.focus='server-owned source-selection pipeline' -count=1` (7 specs)
- `go test ./atc/db -run '^$' -count=1`
- `go test ./atc/api -run '^TestAPI$' -count=1`
- `go test ./atc/atccmd -run '^$' -count=1`
- `go test ./agent/workflowrun ./agent/experiment -count=1`
- `git diff --check`

## Review outcome

- Review round 1 found one valid Important authority hole in the lower-level
  scheduler-selection mutations. Its second migration observation was
  dismissed as a false positive: the reviewed `1773106148` down migration
  already drops `agent_experiments_id_team_key`.
- Commit `d84dbaeb93` corrected the confirmed authority hole.
- Fresh scoped Terra review round 2 found the issue addressed, found no new
  Critical, High, Important, or acceptance-blocking issue, and returned
  **PASS**.
- Status: **Accepted in review round 2 of at most 3**.
