# Snapshot Core and Typed Boundaries Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make declared task and agent outputs authoritative immutable values: validate all required typed ports, store canonical content durably, atomically record lineage, and materialize snapshots as ordinary build artifacts.

**Architecture:** A new `agent/snapshot` package owns type references, contracts, canonical archives, metadata-store interfaces, content-store interfaces, and the batch sealer. PostgreSQL implements metadata transactions. Jetbridge daemon pods store replicated content-addressed tar archives outside the ephemeral step namespace. Task and agent execution collect candidates, call one sealer, and atomically publish sealed artifacts to the build repository. A new `load_snapshot:` ATC step restores a sealed archive into the artifact namespace for downstream functions.

**Tech Stack:** Go, PostgreSQL migrations/factories, ATC exec/runtime, Jetbridge artifact-daemon HTTP, Ginkgo/Gomega and standard Go tests.

## Global Constraints

- Follow the contracts in [the program plan](2026-07-21-agentic-functions-program.md).
- Keep legacy untyped output behavior unchanged for ordinary pipelines and workflow schema versions 1/2.
- Do not use `worker_artifacts` or build artifact handles as canonical snapshot identity.
- Do not publish any typed output to `build.Repository` before the complete seal batch commits.
- Preserve flight/cost/metrics ingestion when semantic output sealing fails.

---

### Task 0: Mark the abandoned ticket-centric roadmap as historical

**Files:**
- Modify: `docs/superpowers/specs/2026-07-19-agent-bench-design.md`
- Modify: `docs/superpowers/plans/agentic-platform/00-shared-contracts.md`
- Modify: `docs/superpowers/plans/agentic-platform/ROADMAP.md`
- Modify: every file under `docs/superpowers/plans/agentic-platform/bench/`
- Modify: `docs/superpowers/plans/agentic-platform/15-platform-home.md`

- [ ] Add a consistent top banner naming the 2026-07-21 approved design and program plan as authoritative while preserving historical text.
- [ ] Remove active migration-range reservations that conflict with `1773106100+` and label “assumed LANDED” dependencies as unimplemented historical assumptions.
- [ ] Add the explicit keep/supersede matrix: fixtures, repetitions, evaluators, controls, and scorecards remain; `step_kind`, ticket/build/plan keys, restore runner/stub, and the primary-metric switch are superseded.
- [ ] Run `rg -n 'assumed LANDED|17731061|step_kind|primaryMetric' docs/superpowers` and require every remaining historical match to be within an explicit superseded block.
- [ ] Commit `docs(agentic): supersede ticket-centric implementation roadmap`.

### Task 1: Define type, manifest, lineage, and storage contracts

**Files:**
- Create: `agent/snapshot/types.go`
- Create: `agent/snapshot/types_test.go`
- Create: `agent/snapshot/store.go`
- Create: `agent/snapshot/snapshotfakes/fake_metadata_store.go`
- Create: `agent/snapshot/snapshotfakes/fake_content_store.go`

- [ ] Write tests for valid type references, invalid grammar/version zero, canonical string rendering, exact quoted-decimal `SnapshotID`/`WorkflowRunID` JSON round trips above `2^53`, unique port validation, retention claim ordering, and JSON round trips.
- [ ] Run `go test ./agent/snapshot -run 'Test(TypeRef|Port|Manifest)' -count=1` and confirm the package is missing/fails.
- [ ] Implement `SnapshotID` and `WorkflowRunID` as validated `int64` wrappers that marshal only as quoted base-10 strings, plus `TypeRef`, `Port`, `Snapshot`, `Location`, `Production`, `Grant`, `RetentionClaim`, `LineageEdge`, `SnapshotRef`, `CandidateOutput`, `SealedOutput`, `SealRequest`, `SealCommit`, `MetadataStore`, `ContentStore`, and `DigestLockManager` exactly as frozen in the program plan. `DigestLockManager.AcquireMany(ctx, sortedUniqueDigests)` returns a `DigestLease` whose `Close` releases session-scoped locks; callers must keep that lease alive across external content-store I/O.
- [ ] Make `MetadataStore.CommitSealBatch(SealCommit)` the only mutation that exposes new snapshots; include `StageUpload` with `lease_expires_at`, staged-attempt reads/removal, team-authorized `GetAuthorized`, `ListAuthorized`, actor-scoped `Pin`/`Unpin`, lifecycle claim scans, content-state transitions, and location repair methods. Document and test that `StageUpload`, `CommitSealBatch`, and orphan deletion are called only while the matching `DigestLease` is held.
- [ ] Make `ContentStore.Put(ctx, digest, io.Reader) ([]Location, error)`, `Open(ctx, Snapshot) (io.ReadCloser, error)`, `Exists`, `DeleteLocation`, and broadcast `DeleteAll(digest)` independent of PostgreSQL. `DeleteAll` makes a staged upload recoverable even when a crash occurred before locations were recorded.
- [ ] Generate or hand-maintain counterfeiter-compatible fakes following existing `agent/*fakes` style.
- [ ] Re-run the focused test and commit `feat(snapshot): define immutable snapshot contracts`.

### Task 2: Canonicalize and safely extract filesystem trees

**Files:**
- Create: `agent/snapshot/archive.go`
- Create: `agent/snapshot/archive_test.go`
- Create: `agent/snapshot/testdata/`

- [ ] Write table-driven tests proving identical trees with different mtimes/uid/gid and input tar ordering produce the same digest and canonical bytes.
- [ ] Add hostile archive tests for absolute/traversal/duplicate paths, hard links, devices, sockets, FIFOs, setuid/setgid, escaping symlinks, oversized content, too many entries, truncated tar, and context cancellation.
- [ ] Add a round-trip test covering empty directories, executable files, UTF-8 paths, and safe relative symlinks.
- [ ] Run `go test ./agent/snapshot -run 'TestCanonical|TestExtract' -count=1` and confirm failure.
- [ ] Implement `Canonicalizer.Capture(ctx, rawTar)` using a private temporary directory, safe extraction, sorted `filepath.WalkDir`, slash-normalized headers, `uid/gid=0`, empty owner names, Unix epoch time, normalized `0644/0755` plus executable bits, and SHA-256 over the emitted canonical tar.
- [ ] Return `CapturedTree{Root, ArchivePath, Digest, ByteSize, FileCount}` whose `Close` removes all temporary data. Close files immediately inside walks; do not defer per-file closes inside loops.
- [ ] Re-run focused tests and commit `feat(snapshot): canonicalize safe filesystem trees`.

### Task 3: Implement authoritative built-in validators

**Files:**
- Create: `agent/snapshot/contracts/registry.go`
- Create: `agent/snapshot/contracts/registry_test.go`
- Create: `agent/snapshot/contracts/review.go`
- Create: `agent/snapshot/contracts/review_test.go`
- Create: `agent/snapshot/contracts/repository.go`
- Create: `agent/snapshot/contracts/repository_test.go`
- Create: `agent/snapshot/contracts/workitem.go`
- Create: `agent/snapshot/contracts/workitem_test.go`
- Create: `agent/snapshot/contracts/logbundle.go`
- Create: `agent/snapshot/contracts/measurements.go`
- Create: `agent/snapshot/contracts/engineering.go`
- Create: `agent/snapshot/contracts/engineering_test.go`
- Create: `agent/snapshot/contracts/audit.go`
- Create: `agent/snapshot/contracts/audit_test.go`
- Create: `agent/snapshot/contracts/interaction.go`
- Create: `agent/snapshot/contracts/interaction_test.go`
- Modify: `agent/schema/review.go`
- Modify: `agent/schema/review_test.go`

- [ ] Write registry tests for exact version lookup, unsupported type errors, duplicate registration, and validation context input lookup.
- [ ] Expand `ReviewOutput.Validate` tests to cover exact `1.0.0`, score ranges, threshold consistency, nested severity/category checks, unique finding IDs, safe file/test paths, positive line ranges, and test-summary totals.
- [ ] Define and test `change.json`, `work-item.json`, `measurements.json`, engineering request/report, audit/diagnosis, question, and human-answer Go contracts with strict JSON decoding (`DisallowUnknownFields`) and one trailing-JSON rejection.
- [ ] Test `repository-change/v1` for `git-tree`, `patch`, and `bundle` against a temporary base Git repository; verify a full base SHA and required full result-tree SHA, require a full result commit SHA only for commit-bearing `git-tree` and `bundle` representations, prove patches by their resulting tree, and verify base/result relationship, payload digest, safe paths, `git apply --check`, and `git bundle verify`.
- [ ] Run `go test ./agent/snapshot/contracts ./agent/schema -count=1` and confirm failures.
- [ ] Implement a registry containing exactly 17 named contracts: `opaque/v1`, `repository/v1`, `repository-change/v1`, `review/v1`, `work-item/v1`, `log-bundle/v1`, `measurements/v1`, `upgrade-request/v1`, `upgrade-report/v1`, `validation-report/v1`, `gate-results/v1`, `database-snapshot/v1`, `deployment-snapshot/v1`, `audit-findings/v1`, `diagnosis/v1`, `question/v1`, and `human-answer/v1`.
- [ ] Keep validators free of network access. Resolve declared base snapshots through the supplied `ValidationContext` content reader.
- [ ] Re-run focused tests and commit `feat(snapshot): enforce built-in snapshot contracts`.

### Task 4: Add durable snapshot and workflow-run tables

**Files:**
- Create: `atc/db/migration/migrations/1773106100_create_agent_snapshots_and_workflow_runs.up.sql`
- Create: `atc/db/migration/migrations/1773106100_create_agent_snapshots_and_workflow_runs.down.sql`
- Create: `atc/db/agent_snapshots_factory.go`
- Create: `atc/db/agent_snapshots_factory_test.go`
- Create: `atc/db/agent_snapshot_digest_locker.go`
- Create: `atc/db/agent_snapshot_digest_locker_test.go`
- Create: `atc/db/agent_workflow_runs_factory.go`
- Create: `atc/db/agent_workflow_runs_factory_test.go`
- Create: `atc/db/agent_snapshot.go`
- Create: `atc/db/agent_workflow_run.go`
- Modify: `atc/db/migration/legacy_upgrade_test.go`

- [ ] Write migration assertions and DB factory tests for manifest deduplication, staged uploads with explicit lease expiry, staged-to-committed consumption, per-production source metadata, multiple productions of the same value, multiple locations, ordered lineage, durable team grants, denied cross-team reads, independent retention claims/pins, effective retention derivation, content-state transitions that preserve manifests, and immutable-row rejection.
- [ ] Write atomicity tests: a two-output commit with one invalid foreign reference inserts nothing; concurrent commits for the same digest converge; retrying the same `(build, plan, attempt, port)` is idempotent; a different snapshot for that key conflicts.
- [ ] Test workflow run/input/output rows survive deletion of the linked instance pipeline, template pipeline, and build.
- [ ] Run `ginkgo --focus='AgentSnapshotsFactory|AgentWorkflowRunsFactory' ./atc/db/` and observe the missing migration/factory failure.
- [ ] Implement the exact tables listed in the program plan. Use `BIGSERIAL` snapshot/workflow-run IDs, `agent_snapshot_grants`, `agent_snapshot_retention_claims`, JSONB for intrinsic versus per-production source metadata, copied rendered config, and origin reference, explicit CHECK constraints, and indexed workflow/type/status/time columns.
- [ ] Implement `db.AgentSnapshotsFactory` as the `snapshot.MetadataStore` adapter and a `db.DigestLockManager` backed by a dedicated `*sql.Conn`. Derive a stable signed 64-bit advisory-lock key from the digest (a hash collision only conservatively serializes unrelated digests), acquire unique digests in lexical order, keep the connection checked out for the lease lifetime, and release all locks before closing it. Upsert content identity and locations, then insert every production/lineage/run binding inside one transaction.
- [ ] Keep workflow-run persistence methods in a separate factory even though the migration is shared.
- [ ] Set `jetbridgeHeadMigration` to `1773106100` and run both legacy-to-head and down/up migration tests in this commit; each later migration task advances the same head constant.
- [ ] Re-run focused DB tests and commit `feat(db): persist snapshots and durable workflow runs`.

### Task 5: Store replicated content in the durable daemon namespace

**Files:**
- Create: `atc/worker/jetbridge/snapshot_content_store.go`
- Create: `atc/worker/jetbridge/snapshot_content_store_test.go`
- Modify: `atc/worker/jetbridge/daemon_client.go`
- Modify: `atc/worker/jetbridge/daemon_client_test.go`
- Modify: `cmd/artifact-daemon/server.go`
- Modify: `cmd/artifact-daemon/server_test.go`
- Modify: `cmd/artifact-daemon/sweeper_test.go`
- Modify: `cmd/artifact-daemon/metrics.go`
- Modify: `atc/atccmd/command.go`
- Modify: `atc/atccmd/command_test.go`

- [ ] Add daemon tests proving an authenticated PUT/GET/HEAD at `/artifacts/snapshots/sha256/<digest>.tar` is immutable: identical bytes are idempotent and different bytes for an existing key return `409 Conflict`.
- [ ] Prove snapshot archives survive daemon restart, TTL sweeps, and deletion of `/artifacts/steps/<producer>`.
- [ ] Test content-store deterministic node ordering, replication-factor behavior, partial replica failure, minimum one acknowledged replica, recorded locations, read fallback, hash verification on read, and context cancellation.
- [ ] Run the focused daemon and Jetbridge tests and confirm they fail.
- [ ] Harden `artifactPath` with a clean-relative-path guard before exposing the durable namespace.
- [ ] Implement immutable daemon writes and snapshot byte/operation metrics. Do not route snapshot archives through `/stream-in` or the step registry.
- [ ] Implement `jetbridge.SnapshotContentStore` using the existing TLS-aware daemon discovery/client. Verify the uploaded archive digest locally and at read completion.
- [ ] Add ATC flags `--agent-snapshot-enabled`, `--agent-snapshot-replication-factor` (default 2), `--agent-snapshot-max-bytes`, and `--agent-snapshot-max-files`; validate positive bounds.
- [ ] Re-run focused tests and commit `feat(jetbridge): store replicated immutable snapshot content`.

### Task 6: Make build artifact publication atomic and snapshot-aware

**Files:**
- Modify: `atc/exec/build/repository.go`
- Modify: `atc/exec/build/repository_test.go`
- Modify: `atc/exec/build/buildfakes/fake_repository.go` or generated equivalent
- Modify: `atc/exec/retry_step_test.go`

- [ ] Write tests for batch registration all-or-none visibility, snapshot metadata lookup, duplicate-name rejection, local-scope copying, concurrent readers, and attempt replacement rules.
- [ ] Add a regression test in which retry attempt one prepares one output then fails; attempt two must not see the partial first-attempt artifact or snapshot reference.
- [ ] Run `go test ./atc/exec/build -count=1` and the focused retry specs; confirm failure.
- [ ] Replace per-entry `sync.Map` publication with a mutex-protected copy-on-write map and `RegisterArtifacts(map[ArtifactName]ArtifactEntry) error`. Extend `ArtifactEntry` with `Snapshot *snapshot.SnapshotRef`.
- [ ] Retain `RegisterArtifact` as a compatibility wrapper over a one-entry batch.
- [ ] Ensure local scopes receive immutable copies and merge atomically only on the existing scope-success path.
- [ ] Re-run tests and commit `refactor(exec): publish artifact batches atomically`.

### Task 7: Carry typed port declarations through ATC config and plans

**Files:**
- Modify: `atc/task.go`
- Modify: `atc/steps.go`
- Modify: `atc/plan.go`
- Modify: `atc/builds/planner.go`
- Modify: `atc/step_validator.go`
- Modify: `atc/public_plan.go`
- Modify: `atc/public_plan_test.go`
- Modify: `atc/steps_test.go`
- Modify: `atc/builds/planner_test.go`

- [ ] Write config round-trip tests for additive `input_types` and `output_types` maps on `task:` and `agent:` steps, retaining legacy `inputs`, `outputs`, and advisory `output_schema` decoding.
- [ ] Test rejection when a typed name is absent from the corresponding inputs/outputs, a type reference is invalid, or an output type is redeclared inconsistently.
- [ ] Test planner and public-plan preservation of type maps, workflow output port names, retention, workflow definition ID, and string workflow-run ID.
- [ ] Run the focused ATC tests and confirm failures.
- [ ] Add `SnapshotInputs map[string]SnapshotInputConfig`, `SnapshotOutputs map[string]SnapshotOutputConfig`, and `Capabilities []string` to authoring steps. `SnapshotInputConfig` contains `Type` and `Optional`; `SnapshotOutputConfig` contains `Type`, `Optional`, retention-claim source, `WorkflowPort`, `WorkflowDefinitionID`, and quoted-decimal `WorkflowRunID`.
- [ ] At execution, a missing required typed input is an error; a missing optional typed input is allowed and is not mounted. A present optional input is type-checked exactly like a required input.
- [ ] Carry resolved declarations into `TaskPlan` and `AgentPlan`. Keep `OutputSchema` untouched for v1/v2 compatibility and do not treat it as a type declaration.
- [ ] Re-run tests and commit `feat(atc): carry typed snapshot ports through plans`.

### Task 8: Seal task and agent outputs before registration

**Files:**
- Create: `agent/snapshot/sealer.go`
- Create: `agent/snapshot/sealer_test.go`
- Modify: `atc/exec/task_step.go`
- Modify: `atc/exec/task_step_test.go`
- Modify: `atc/exec/agent_step.go`
- Modify: `atc/exec/agent_step_test.go`
- Modify: `atc/engine/step_factory.go`
- Modify: `atc/engine/step_factory_test.go`
- Modify: `atc/atccmd/command.go`

- [ ] Write pure sealer tests for required/optional outputs, unknown types, validation failure, upload failure, DB failure/orphan content, deduplicated values, input lineage, all-required atomicity, lexically ordered digest-lock acquisition, and lock release on every failure path.
- [ ] Add TaskStep and AgentStep tests: absent optional typed inputs are neither mounted nor treated as missing, absent required typed inputs fail before execution, present inputs must carry the exact snapshot type, missing/invalid one-of-many outputs yield no typed repository entries or DB commit, nonzero process exit never seals, telemetry still ingests, a valid batch publishes all artifacts with snapshot refs, and legacy untyped paths retain behavior.
- [ ] Run the focused tests and observe failure.
- [ ] Implement `snapshot.BatchSealer`: collect every candidate stream, canonicalize and validate all, acquire one PostgreSQL session advisory lock per unique digest in lexical digest order, and hold the locks across stage-row creation, upload, and the metadata commit. Under those locks, durably stage each digest before upload, upload all, call one metadata commit that consumes the stages and creates team/principal grants plus per-binding retention claims, and return sealed artifacts backed by their candidate volume for same-build consumption. A competing sealer that finds the digest already committed reuses its verified locations rather than rewriting it. Release every lock with `defer`, including cancellation and partial failure paths.
- [ ] Add `WithOutputSealer` options to TaskStep and AgentStep and inject them from the core step factory only when snapshots are enabled.
- [ ] Refactor `registerOutputs` into candidate collection plus one atomic publication. Missing typed mounts are errors; untyped missing mounts retain current warnings.
- [ ] Refactor task/agent input satisfaction to consult `SnapshotInputConfig`: skip an absent optional artifact, preserve required missing-input errors, and type-check every present snapshot reference before creating volume mounts.
- [ ] Parse and verify workflow-run linkage server-side before setting durable run output bindings. Do not trust plan IDs without the workflow-run/build association check.
- [ ] Re-run focused tests and commit `feat(exec): validate and seal typed outputs atomically`.

### Task 9: Add the `load_snapshot:` materialization step

**Files:**
- Create: `atc/exec/load_snapshot_step.go`
- Create: `atc/exec/load_snapshot_step_test.go`
- Modify: `atc/steps.go`
- Modify: `atc/plan.go`
- Modify: `atc/builds/planner.go`
- Modify: `atc/engine/builder.go`
- Modify: `atc/engine/step_factory.go`
- Modify: `atc/step_recursor.go`
- Modify: `atc/step_validator.go`
- Modify: all compile-enforced `StepVisitor` implementations and generated fakes named by compiler errors
- Create: `agent/snapshot/artifact.go`
- Create: `agent/snapshot/artifact_test.go`

- [ ] Write syntax/planner tests for `load_snapshot: <artifact-name>`, quoted-decimal `id`, `type`, `optional`, and quoted-decimal `workflow_run_id` fields with run-parameter interpolation, including IDs above `2^53`.
- [ ] Write exec tests for authorized load, denied team, missing required snapshot, optional `id: "0"` no-op, type mismatch, workflow-run binding mismatch, replica fallback, corrupt content, and atomic artifact registration with snapshot metadata.
- [ ] Run targeted ATC tests and capture the expected visitor compile failures.
- [ ] Add `LoadSnapshotStep`, `LoadSnapshotPlan`, `VisitLoadSnapshot`, planner support, public plan redaction, and core factory wiring.
- [ ] Implement a read-only snapshot archive artifact whose `StreamOut` supports raw and requested compression and verifies content digest. Register it under the declared artifact name.
- [ ] Update every exhaustive visitor rather than adding default behavior that silently ignores the new core step.
- [ ] Re-run targeted tests and commit `feat(atc): materialize durable snapshots in build plans`.

### Task 10: Expose snapshot create/read/pin APIs and Fly commands

**Files:**
- Create: `agent/api/snapshots/handler.go`
- Create: `agent/api/snapshots/handler_test.go`
- Create: `agent/api/snapshots/types.go`
- Create: `agent/api/snapshots/route_registration_test.go`
- Create: `atc/db/migration/migrations/1773106102_add_snapshot_upload_occurrences.up.sql`
- Create: `atc/db/migration/migrations/1773106102_add_snapshot_upload_occurrences.down.sql`
- Modify: `agent/snapshot/sealer.go`
- Modify: `agent/snapshot/store.go`
- Modify: `atc/db/agent_snapshots_factory.go`
- Modify: `atc/db/agent_snapshots_factory_test.go`
- Modify: `atc/db/migration/legacy_upgrade_test.go`
- Modify: `atc/routes.go`
- Modify: `atc/api/handler.go`
- Modify: `atc/wrappa/api_auth_wrappa.go`
- Modify: `atc/wrappa/reject_archived_wrappa.go`
- Create: `fly/commands/agent_snapshots.go`
- Create: `fly/integration/agent_snapshots_test.go`
- Modify: `fly/commands/agent.go`

- [ ] Write handler tests for bounded tar upload, validation errors, durable team/principal grants, denied guessed IDs, list filters, manifest detail, archive download, independent actor pin/unpin claims, quoted-decimal IDs above `2^53`, immutable identity, and unavailable storage.
- [ ] Write Fly integration tests for `agent snapshots create|list|show|download|pin|unpin`, including `--type`, `--from`, and JSON output.
- [ ] Run focused tests and confirm missing routes/commands.
- [ ] Implement POST/GET/list/content/pin endpoints with existing user/team auth conventions. Upload passes through the same canonicalizer, validator, content store, and commit path as step outputs, using an `upload` production occurrence with source metadata, creating the uploader's team grant and retention claim atomically.
- [ ] Represent upload provenance as a first-class non-build production occurrence, enforce team-scoped idempotency in migration `1773106102`, and advance `jetbridgeHeadMigration` with down/up and legacy-to-head coverage.
- [ ] Stream Fly uploads as deterministic tar without loading the directory into memory. Refuse symlink escapes client-side and rely on server validation authoritatively.
- [ ] Re-run focused tests and commit `feat(agent): manage immutable snapshots through API and fly`.

### Task 11: Implement retention, orphan GC, and replica repair

**Files:**
- Create: `agent/snapshot/lifecycle.go`
- Create: `agent/snapshot/lifecycle_test.go`
- Modify: `atc/db/agent_snapshots_factory.go`
- Modify: `atc/db/agent_snapshots_factory_test.go`
- Modify: `atc/worker/jetbridge/snapshot_content_store.go`
- Modify: `atc/worker/jetbridge/snapshot_content_store_test.go`
- Modify: `atc/atccmd/command.go`
- Modify: `atc/atccmd/command_test.go`

- [ ] Write tests for staged-upload orphan grace, two sealers with the same digest, GC racing an upload and `CommitSealBatch`, lock acquisition failure, expired and unexpired sibling stages, expired intermediate claims, permanent workflow/fixture claims, independent pins, physical deletion after the last active retention claim even while grants/lineage remain, idempotent location deletion, and database/content-store failure retry. Use barriers in the race tests to prove GC cannot delete between upload and commit and that a stage which starts after GC's recheck cannot publish until deletion completes and it has re-uploaded.
- [ ] Write replica tests that detect missing recorded nodes, copy from a verified source until the configured factor is met, prune stale location rows only after failed verification, and never delete the last readable copy.
- [ ] Run focused lifecycle tests and confirm failure.
- [ ] Implement a bounded restart-safe lifecycle component with `--agent-snapshot-gc-interval`, `--agent-snapshot-orphan-grace-period`, and `--agent-snapshot-repair-interval` positive duration flags. Inject the same `DigestLockManager` used by the sealer; never approximate locking with an in-process mutex.
- [ ] Derive effective retention only from active retention claims; grants and historical references authorize/describe but do not retain bytes. When no claim remains, delete physical locations, preserve snapshots/productions/lineage/grants forever, and atomically mark content `expired`. Expired manifests return metadata normally and content requests return `410 Gone`; a later pin is rejected until content is recaptured. Partial location deletion remains retryable.
- [ ] Recover staged uploads using the same digest-scoped PostgreSQL session advisory lock as `BatchSealer`. Hold it across the final database recheck, `DeleteAll(digest)` on live daemons, and stage-row cleanup. If the digest became committed, remove only stale stage rows. If any sibling stage has an unexpired lease, leave all bytes and rows untouched. Otherwise delete the shared digest bytes and all expired sibling stages before releasing the lock. Stage creation and `CommitSealBatch` must take the same lock, so no sealer can upload or commit between GC's recheck and deletion.
- [ ] Re-run tests and commit `feat(snapshot): enforce retention and repair durable replicas`.

### Task 12: Verify snapshot vertical slice

**Files:**
- Create: `atc/exec/snapshot_integration_test.go`
- Create: `atc/worker/jetbridge/live_snapshot_test.go`
- Modify: `docs/superpowers/plans/2026-07-21-snapshot-core-and-typed-boundaries.md`

- [ ] Add an integration test that loads an input snapshot, runs a typed task producing two outputs, seals them, loads one in a later build, and verifies exact lineage.
- [ ] Add a Jetbridge live test that deletes the producer pod/step directory, restarts daemon state, and still streams the snapshot from a replica.
- [ ] Run `gofmt` on changed Go files and regenerate changed fakes.
- [ ] Run package tests for `agent/snapshot`, validators, snapshot API, `atc/exec/build`, task/agent/load snapshot steps, Jetbridge content store, and artifact daemon.
- [ ] Run focused DB specs with the repository's self-hosted `atc/postgresrunner` outside the managed loopback sandbox, then run `make test-integration`.
- [ ] Mark every completed checkbox, record environment-only exclusions in the program plan, and commit `test(snapshot): verify durable typed snapshot lifecycle`.
