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
- Public pipeline config, rename, pause/unpause/archive/destroy, manual job
  builds/reruns, one-off started builds, and pipeline-run creation all reject
  registered source pipelines. API handlers return conflict responses. The
  ordinary abandoned-pipeline cleaner and idle pauser explicitly exclude them.

## Tests and verification

Passed:

- `go test ./agent/workflowrun -run '^(TestSourcePipelineLifecycle|TestWorkflowTargetRendererValidatesSourceWorkflowForPromotionWithoutRuntimeAdmission)' -count=1`
- `go test ./atc/api/configserver ./atc/api/jobserver ./atc/api/pipelineserver ./atc/api/runserver -run '^$' -count=1`
- `git diff --check`

Added focused DB coverage for atomic source-pipeline promotion, frozen
declaration repeat checks, lifecycle unpause/pause/archive, promotion rollback
when registry activation fails, and public manual-build/config guards.

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
