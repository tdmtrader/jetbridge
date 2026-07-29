# Task 12 — workflow resource-source admissions

## Status

**HUMAN REVIEW REQUIRED** after exhausting the two-round review budget.
Three of four round-1 High findings were addressed. The remaining
load-bearing issue is that `RenderedFunction.Config` remains a public bare
configuration and `BindExecutionParams` is opt-in, so the execution path does
not yet guarantee use of the exact validated source refs. Source-capture and
retry/reuse orchestration remains Task 13 and is dependency-deferred.

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
   and idempotent replay/conflict. The sandbox attempt could not create System
   V shared memory, but the identical host-access rerun executed and passed
   both focused specs. Package compilation, workflow tests, and
   migration-package compilation also passed.

Verification for this correction:

- `go test ./agent/workflow -count=1` — passed.
- `go test ./atc/db -run '^$' -count=1` — passed.
- `go test ./atc/db/migration -run '^$' -count=1` — passed.
- `ginkgo --procs=1 --focus='workflow resource-source admission persistence' ./atc/db`
  — host-access rerun passed 2/2 focused specs after the sandbox
  shared-memory restriction.
- `git diff --check` — passed.

## Independent review

- Round 1 found four High blockers: optional exact execution binding,
  caller-asserted admission provenance, archive/create race, and absent DB
  lifecycle/provenance coverage.
- The single correction pass addressed provenance derivation, row-lock
  serialization, and DB acceptance coverage.
- Round 2 found those three items addressed, with no new High breakage.
- Residual High: `RenderedFunction.Config` is still directly available while
  `BindExecutionParams` is optional. The test proves the binder rejects
  substitution only when a caller chooses to invoke it; no launch seam
  requires it.
- Status: **HUMAN REVIEW REQUIRED** under `HUMAN-REVIEW-002`. Task 13 must not
  build on this execution boundary until the launch contract is chosen.

## Reopened iteration 1 — mandatory opaque launch envelope

The user-approved choice (c) is implemented: `RenderedFunction.Config` remains
the declarative/preflight view, but the Binder is now the sole production
constructor/caller of `workflow.ExecutionEnvelope`, and both
`workflowrun.PipelineRunCreator` and `db.PipelineRunFactory` require that
opaque type rather than a raw parameter map.

- Trusted rendering retains private canonical config bytes, target hash, and
  exact bound source refs. Envelope construction injects the selected snapshot
  IDs, rejects substitutions, and copies params defensively. The DB opens it
  only after it locks the durable workflow run and verifies the canonical
  durable config plus template hash; zero and mismatched envelopes fail closed.
- The public `Config` and `TargetConfigHash` can no longer alter an already
  created envelope. `RenderedFunction.Clone` now preserves private authority
  explicitly because `copystructure` drops unexported fields. The trusted
  runtime-image renderer refreshes that private authority after its config/hash
  mutation.
- Source-bearing params are treated as envelope-owned during Binder parameter
  preparation, then injected before schema validation. Render validation now
  accepts and verifies those exact bound source parameter names. Task 13 still
  owns obtaining/capturing those refs and making source-bearing flows
  operational.

### RED / GREEN evidence

- RED: `GOCACHE=/private/tmp/concourse-task12-envelope-gocache go test
  ./agent/workflow -run TestRenderFunctionWithBoundSourcesRemovesLiveResourceReads
  -count=1` failed with `rendered.ExecutionEnvelope undefined` before the
  envelope existed.
- GREEN: the same focused workflow test passed after implementation; it proves
  exact source-ID injection, substituted-ID rejection, and immunity to later
  public Config/hash mutation.
- GREEN: `GOCACHE=/private/tmp/concourse-task12-envelope-gocache go test
  ./agent/workflow ./agent/workflowrun -count=1` passed.
- GREEN: `GOCACHE=/private/tmp/concourse-task12-envelope-gocache go test
  ./atc/db -run '^$' -count=1` passed. Focused DB coverage now includes valid
  source-free envelope execution and forged/canonical-mismatch rejection.
- PostgreSQL focus not run locally: `pg_isready` returned `/tmp:5432 - no
  response`. The required host command is `ginkgo --procs=1 --focus='PipelineRunFactory'
  ./atc/db` (or the narrower envelope spec).
