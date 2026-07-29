# Task 12 — workflow resource-source admissions

## Status

Implemented in `2d160f6c89` and pending independent review. Source-capture
and retry/reuse orchestration is deliberately not included; it is Task 13.

## Behavior

- Adds the schema-v3 `resource_sources` grammar. Each declaration names one
  ordinary Concourse resource, typed snapshot output, optional trigger, and
  ordinary Concourse `version` selection. `passed` constraints are rejected.
- Requires a one-to-one resource/source mapping, preserves source types in
  typed flow, prevents shadowing, and rejects direct rendering until exact
  sealed source snapshot references are bound.
- Renders a non-template, team/workflow-definition/revision owned standing
  pipeline with the single ordinary `admit` job. The source-only custom
  resource-type closure is deterministic and excludes task-only types.
- Adds 1773106142 tables for ownership, admissions, and exact selected
  bindings; 1773106143 stores the owning pipeline config version on every new
  build. A DB factory atomically activates/drains/archives source ownership
  and persists only successful `admit` build selections pinned to the
  registered configuration revision.

## Files

- `agent/workflow/{function_config,parse,typecheck,render,hash}.go`
- `agent/workflow/resource_source*.go`
- `atc/db/agent_workflow_resource_source_admissions_factory.go`
- `atc/db/migration/migrations/1773106142_*`
- `atc/db/migration/migrations/1773106143_*`
- `atc/db/migration/legacy_upgrade_test.go`
- `docs/migration/migrate-preflight.sh`

## Coverage

Adapted foundations behavioral coverage in
`agent/workflow/resource_source_test.go`: ordinary pinned selection grammar,
stable `passed` refusal, deterministic standing-admit rendering, and
snapshot-only rendering with exact bound source references. No artificial RED
was manufactured, per the integration test budget. No new DB test was added:
the current DB suite is PostgreSQL-backed and its focused normal harness is
blocked before any test executes.

## Verification

- `GOCACHE=/private/tmp/task12-go-cache go test ./agent/workflow -count=1`
  — passed.
- `GOCACHE=/private/tmp/task12-go-cache go test ./agent/workflow ./atc/db -run '^$' -count=1`
  — passed.
- `GOCACHE=/private/tmp/task12-go-cache ginkgo --procs=1 --focus='workflow resource source|build pipeline config|Legacy Database Upgrade' ./atc/db/migration`
  — the sandbox attempt was blocked in postgresrunner BeforeSuite by denied
  System V shared memory. The identical host-access rerun passed 17/17 focused
  migration and legacy-upgrade specs.

## Migration paths

`1773106141 -> 1773106142 -> 1773106143`; both
`jetbridgeHeadMigration` and `JETBRIDGE_VERSION` now equal `1773106143`.
The 6142/6143 migrations are new files authored against the current v3 schema;
no foundations migration file was copied verbatim.

## Self-review and concerns

- Exact selecting-build provenance is checked against source pipeline, team,
  completed success state, `admit` job, and captured pipeline config version.
- Lifecycle transitions are scoped by team/pipeline and archive refuses
  in-flight admissions.
- Runtime source capture/finalization/retry/reuse is intentionally deferred to
  Task 13. The factory therefore persists capture operation keys and snapshot
  slots but does not create snapshots itself.
- The host-access migration checkpoint resolved the PostgreSQL shared-memory
  prerequisite. The new DB factory compiles, but no dedicated factory
  behavioral test was added; independent review must decide whether the
  existing integration boundary is sufficient for Task 12.

## Deferred observations

None beyond the already planned Task 13 runtime composition.

## Commits

`2d160f6c89 feat(workflow): persist resource-source admissions`

## Fix round 1

This is the sole correction pass for the four High review findings.
Implementation commit: `f9de627f40 fix(workflow): harden source admission bindings`.

1. `RenderedFunction` now retains the exact validated source references in a
   private execution envelope. `BindExecutionParams` injects their snapshot
   IDs and rejects any supplied value that differs, so no caller can replace a
   validated source after rendering. The focused workflow regression first
   failed because the envelope method did not exist, then passed with both
   injection and substitution-rejection assertions.
2. `CreateCaptured` no longer accepts caller-supplied binding values. It locks
   and reads frozen source declarations, verifies the selecting build is a
   successful `admit` build for the captured config version, and derives each
   source/resource/version/type from that build's recorded inputs. It rejects
   missing, extra, ambiguous, or declaration-mismatched inputs. Idempotency
   now also rejects a reused key whose immutable admission identity differs.
3. Archive locks its scoped source-pipeline owner row before it rechecks
   in-flight admissions and changes state. Create locks that same row before
   verifying active authority and deriving bindings, serializing an archive
   against a new capture admission.
4. Added serial DB behavioral coverage for team-scoped lifecycle ownership,
   drain/archive rejecting later create, selecting-build-derived binding/type,
   and idempotent replay/conflict. The narrow suite was attempted once but its
   PostgreSQL BeforeSuite could not create System V shared memory in this
   sandbox (`shmget: Operation not permitted`), so it ran zero specs. Package
   compilation passed, as did workflow tests and migration-package compilation.

Verification for this correction:

- `go test ./agent/workflow -count=1` — passed.
- `go test ./atc/db -run '^$' -count=1` — passed.
- `go test ./atc/db/migration -run '^$' -count=1` — passed.
- `ginkgo --procs=1 --focus='workflow resource-source admission persistence' ./atc/db`
  — blocked before specs by the sandbox PostgreSQL shared-memory restriction.
- `git diff --check` — passed.
