# Agentic Foundations Semantic Rebase Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> `superpowers:subagent-driven-development` to implement this plan task by task.

**Goal:** Reconstruct approved ranks 1–5, 7, 9, and 13 on the cleaned
Jetbridge v3 branch without restoring the deleted v1 architecture.

**Architecture:** Treat `codex/agentic-platform-foundations` as an executable
behavioral reference, not a patch source. Re-author database migrations above
the Jetbridge head, port additive domain packages behind tests, and adapt every
ATC/runtime/deployment seam to the current schema-v3, snapshot-keyed,
single-terminalizer design.

**Tech Stack:** Go, PostgreSQL, Ginkgo/Gomega, Concourse ATC and scheduler,
Kubernetes, GCS-compatible object storage, zstd, MCP, Elm, and Helm.

## Global constraints

- Work only in `.worktrees/agentic-platform-rebase` on
  `codex/agentic-platform-rebase`; do not modify the old foundations worktree.
- Read
  `docs/superpowers/specs/2026-07-28-agentic-foundations-semantic-rebase-design.md`
  before every task. Its trust boundaries override the old implementation.
- Use `git show codex/agentic-platform-foundations:<path>` for behavioral
  reference. Copying an additive package is permitted only after a failing
  target-side test and an API/schema comparison.
- Preserve target migrations `1773106128`–`1773106138` byte-for-byte. Use the
  appended `1773106139`–`1773106147` block below. If the target advances before
  integration, renumber the entire unpublished block above the new head.
- Keep `workflow.yaml`, schema-v3-only execution, snapshot-keyed review and
  feedback identity, runtime-only dispatch mode, and the workflow-run
  reconciler as the sole terminalizer.
- Do not restore the root `agent/devmcp` client, the v1 ci-agent phase runner,
  `agent/functions/gates`, `agent/functions/repositoryvalidate`, the epistemic
  layer, static-selector exposure, candidate port policy, or generic MCP
  role/port authority.
- Run PostgreSQL-backed suites serially. They share the fixed 5434–5442 test
  range and collide if parallel Ginkgo invocations start databases together.
- Each task follows red/green/refactor, ends with `gofmt`/`git diff --check`,
  records focused verification, and is independently reviewed before the next
  task starts.

## Migration allocation

| Version | Task | Purpose |
| --- | --- | --- |
| `1773106139` | 1 | Direct snapshot team ownership |
| `1773106140` | 2 | Remove agent principals |
| `1773106141` | 5 | Authoritative validation provenance |
| `1773106142` | 12 | Workflow resource-source admissions |
| `1773106143` | 12 | Selecting-build pipeline-config provenance |
| `1773106144` | 15 | Checkpoint objects, generations, effects, and events |
| `1773106145` | 15 | Durable execution attempts |
| `1773106146` | 18 | Per-attempt metric attribution |
| `1773106147` | 18 | Per-attempt transcripts |

## Behavioral references

The following files exist on `codex/agentic-platform-foundations` and describe
the prior implementation in detail:

- `docs/superpowers/plans/2026-07-26-snapshot-ownership.md`
- `docs/superpowers/plans/2026-07-26-remove-agent-principals.md`
- `docs/superpowers/plans/2026-07-26-dev-capability-validation.md`
- `docs/superpowers/plans/2026-07-26-output-builder.md`
- `docs/superpowers/plans/2026-07-26-hangar.md`
- `docs/superpowers/plans/2026-07-26-workflow-resource-sources.md`
- `docs/superpowers/plans/2026-07-26-publisher-in-atc.md`
- `docs/superpowers/plans/2026-07-26-agent-preemption-recovery.md`

They are evidence for behavior and tests. Their migration numbers, deleted
packages, task renderer, chart structure, and current-API assumptions are not
authoritative.

---

### Task 1: Make direct team ownership the only snapshot authorization model

**Files:**

- Create:
  `atc/db/migration/migrations/1773106139_snapshot_direct_team_ownership.{up,down}.sql`
- Create: `atc/db/migration/snapshot_direct_team_ownership_test.go`
- Modify: `atc/db/agent_snapshots_factory.go` and its tests.
- Modify every current snapshot consumer found by
  `rg 'agent_snapshot_grants|snapshot_grants' atc agent`.
- Modify: `atc/db/migration/legacy_upgrade_test.go`,
  `docs/migration/migrate-preflight.sh`.

**Steps:**

1. Add a failing migration test for fresh install, upgrade from 6138, ambiguous
   legacy grants, zero-grant rows, exact team/digest identity, and rollback.
2. Re-author the old ownership migration against the 6138 schema. Backfill only
   when exactly one distinct team owns each snapshot; otherwise fail closed.
3. Add failing factory tests proving cross-team reads are denied without a
   join table and same bytes may be independently owned by two teams.
4. Replace all live grant inserts/joins with direct `team_id` predicates,
   including captures, reviews, feedback, publications, runs, outcomes, waits,
   and experiments.
5. Run, serially:
   `go test ./atc/db/migration -run 'SnapshotDirect|LegacyUpgrade' -count=1`,
   then `go test ./atc/db -run 'Agent|Snapshot|ResourceCapture' -count=1`.
6. Require `rg 'agent_snapshot_grants|snapshot_grants' atc agent` to return
   only immutable historical migration text or explicit upgrade fixtures.

### Task 2: Remove principals while preserving ordinary human team actions

**Files:**

- Create:
  `atc/db/migration/migrations/1773106140_drop_agent_principals.{up,down}.sql`
  and `atc/db/migration/drop_agent_principals_test.go`.
- Delete live principal API/auth/store/Fly/go-concourse/Elm surfaces found by
  `rg 'AgentPrincipal|agent_principals|cap1|agent principal'`.
- Modify: `atc/routes.go`, `atc/wrappa/`, `atc/api/`, `agent/api/tickets/`,
  `atc/atccmd/command.go`, current review/feedback routes, and affected tests.
- Modify the migration head/preflight files advanced in Task 1.

**Steps:**

1. Add failing route tests proving authenticated team users can read workflows,
   submit human reviews/feedback, and cannot cross a team boundary.
2. Add failing migration tests, then drop the principal table, routes, token
   verifier, wrappers, CLI/client methods, UI controls, and generated fakes.
3. Do not expose retired agent mutation endpoints as human endpoints. Keep
   sealed outputs and server-owned operations authoritative.
4. Run `go test ./atc/wrappa ./atc/api ./agent/api/tickets ./go-concourse/concourse -count=1`,
   focused Fly integration, and affected Elm tests.
5. Require the live-code residue search for
   `cap1|AgentPrincipal|agent_principals|agent principal` to be empty apart
   from migrations, changelog/design history, and negative residue tests.

### Task 3: Extract one retained dev-capability execution core

**Files:**

- Create: `ci-agent/devmcp/core.go`, `profile.go`, `validate.go` and tests.
- Create: `ci-agent/cmd/dev-capability/main.go` and tests.
- Modify: `ci-agent/devmcp/{config,runner,tools}.go`,
  `ci-agent/cmd/dev-mcp/main.go`, and tests.
- Modify: `deploy/Dockerfile.mcp-dev-concourse` and its image test.

**Steps:**

1. Add failing parity tests around the retained MCP commands: exact working
   directory, timeout, exit status, stdout/stderr, affected/full profiles,
   retries, and malformed repo configuration.
2. Extract resolution and execution into a transport-neutral `devmcp.Core`;
   keep the current MCP wire surface as a thin untrusted adapter.
3. Add a deterministic CLI whose machine result distinguishes pass, test
   failure, and infrastructure/configuration error with exact exit codes
   `0/1/2`, retaining complete logs.
4. Ensure the candidate cannot replace the configured binary, profile, or
   trusted config bytes. Do not create a root-module dev-MCP client.
5. Run `cd ci-agent && go test ./devmcp ./cmd/dev-mcp ./cmd/dev-capability -count=1`,
   then `make test-dev-mcp`.

### Task 4: Compile immutable dev-validation authority into schema-v3 workflows

**Files:**

- Create: `agent/workflow/dev_validation.go` and tests.
- Modify current `agent/workflow/{function_config,parse,compile,render,hash,typecheck}.go`
  and tests.
- Add promotion/pinning helpers only in the current workflow/image pipeline
  seams; do not revive v1 phase-runner or static-selector packages.

**Steps:**

1. Add failing parse/typecheck/hash tests for human-authored named profiles,
   exact config bytes, pinned image digest, fixed tool path, immutable base
   inputs, and rejected candidate overrides.
2. Compile this authority into the frozen workflow revision and rendered
   validation task. Hash every authority-bearing field.
3. Prove normal agent capabilities and interactive dev-MCP remain independent
   from the authoritative profile.
4. Run `go test ./agent/workflow -count=1` and schema-v3 golden tests.

### Task 5: Add validation/v1 revision 3 and seal-time provenance

**Files:**

- Create:
  `agent/snapshot/contracts/schemas/validation.v1.rev3.json`.
- Modify: `agent/snapshot/contracts/{validation,record_schema}.go`,
  schema history/bump tests, `agent/snapshot/validator.go`, sealer request
  authority, and tests.
- Create migration
  `1773106141_freeze_authoritative_validation_provenance.{up,down}.sql`
  plus migration tests; advance head/preflight pointers.

**Steps:**

1. Add failing schema/history tests for revision 3 containing exact candidate,
   base, profile, config, image, toolchain, workflow-revision, attempts, and
   complete-log digests.
2. Keep prior revisions readable but make only rev3 server-attested success
   eligible for current gates.
3. Extend seal admission with server-derived validation authority. Reject
   record-supplied or source-metadata authority, missing logs, altered
   identifiers, and successful-looking agent-authored records.
4. Run `go test ./agent/snapshot/... -count=1` and the focused migration suite.

### Task 6: Execute validation in a fresh hermetic non-agent task

**Files:**

- Create: `agent/functions/devvalidate/` and tests.
- Modify the current function/task runner and ATC task rendering seams only
  where they still exist after v3 cleanup.
- Modify: `atc/exec/task_step.go`, `atc/task.go`, `atc/steps.go`, and tests.

**Steps:**

1. Add failing tests proving an exact read-only candidate is copied into fresh
   scratch; protected config/tooling remains outside it; all attempts and full
   logs are retained.
2. Render a non-agent task with no model, publisher, Kubernetes, output-builder,
   or generic capability credentials and with an immutable validator image.
3. Strictly decode the deterministic CLI result and submit a rev3 candidate
   through the server-owned validation authority from Task 5.
4. Prove candidate `dev-mcp.yml`, `PATH`, symlinks, and result JSON cannot
   redefine or forge validation.
5. Run `go test ./agent/functions/devvalidate ./atc/exec -count=1`.

### Task 7: Gate review and Publish on the exact current validation

**Files:**

- Create: `agent/workflow/validation_gate.go` and tests.
- Modify current workflow type flow/rendering and
  `atc/exec/{agent_step,await_snapshot_step,publish_snapshot_step}.go` as
  applicable, plus tests.
- Modify v3 seed workflows under `agent/workflow/seeds/`.

**Steps:**

1. Add failing structural tests: a review/Publish consumer must be dominated by
   a validation of the exact candidate after the last mutating node.
2. Add runtime tests rejecting stale candidate, profile, config, image,
   toolchain, workflow revision, rebase, or non-success attestation.
3. Enforce both structural and runtime gates without adding a generic retired
   gates package.
4. Run `go test ./agent/workflow ./atc/exec -count=1` and focused Fly workflow
   integration.

### Task 8: Implement the managed output-builder core

**Files:**

- Create: `agent/outputbuilder/` core, authority, CLI/MCP adapters, and tests.
- Add raw-JSON codecs only where required by current built-in record contracts.
- Create: `cmd/agent-output/main.go` and tests.
- Modify: `deploy/agent-runner/Dockerfile` and image tests.

**Steps:**

1. Add failing tests for immutable mount-bound authority, declared input/output
   limits, content roots, atomic writes, schema errors, undeclared outputs,
   path traversal, symlinks, and cancellation.
2. Implement one builder used by CLI and fixed-loopback MCP. It may describe,
   write, and prevalidate candidates but never seal or mint authority.
3. Add parity tests proving the final sealer independently reopens and rejects
   a candidate the builder did not or could not authorize.
4. Run `go test ./agent/outputbuilder ./cmd/agent-output ./agent/snapshot/... -count=1`.

### Task 9: Wire output-builder authority into agent execution

**Files:**

- Create: `atc/exec/output_builder_authority.go` and tests.
- Modify: `atc/exec/{agent_step,record_authority_env}.go`,
  `atc/worker/jetbridge/container.go`, `agent/runner/runner.go`, and tests.
- Add integration/e2e tests at current agent-step and runner seams.

**Steps:**

1. Add failing tests showing ATC derives authority from frozen execution facts,
   writes it as a protected file, and does not place its content or credentials
   in prompts/environment/MCP configuration.
2. Inject the managed sidecar and private fixed-loopback client configuration
   only into agent nodes that declare typed outputs.
3. Ensure cleanup removes private config and that validation tasks from Task 6
   never receive this facility.
4. Prove builder success does not bypass post-step sealing.
5. Run `go test ./atc/exec ./atc/worker/jetbridge ./agent/runner -count=1`
   plus runner Dockerfile/chart security tests.

### Task 10: Implement immutable bounded Hangar storage

**Files:**

- Create: `agent/hangar/{types,keys,gcs}.go`, fakes, and tests.
- Modify `go.mod`/`go.sum` only for the supported GCS and zstd dependencies.
- Add an emulator-backed integration test and Make target.

**Steps:**

1. Add failing unit tests for canonical SHA-256 keys, immutable create,
   idempotent identical put, collision/corruption rejection, byte limits,
   streaming zstd, cancellation, and verified reads.
2. Implement the backend against GCS semantics, with no provider-specific
   behavior leaking into callers.
3. Test against the in-cluster-compatible GCS emulator, including concurrent
   writers, truncated objects, and deletion of all node-local cache.
4. Run `go test ./agent/hangar -count=1` and `make test-hangar-integration`.

### Task 11: Make the artifact daemon a Hangar-backed local mirror

**Files:**

- Modify: `cmd/artifact-daemon/`, `atc/worker/jetbridge/snapshot_content_store.go`,
  `agent/snapshot/{store,lifecycle}.go`, snapshot DB/API/UI projections, and
  tests.
- Modify current `atc/atccmd` composition.
- Add current-style Helm emulator, credentials, egress, metrics, alert, and
  security tests; do not restore removed generic PVC flags.
- Create: `docs/operations/hangar.md`.

**Steps:**

1. Add failing tests that PUT commits durable bytes before success and GET
   verifies/read-through-restores from Hangar after total mirror loss.
2. Store one canonical durable location while safely adopting pre-Hangar
   snapshots; preserve current archive, symlink, signing, and secret-mount
   rules.
3. Keep agentic objects out of proactive peer mirroring. The local daemon may
   retain an on-use cache, but a miss reads from Hangar rather than searching
   other nodes.
4. Add production GCS and long-lived emulator modes, with narrow daemon egress,
   bounded credentials, persistence, metrics, and rollback documentation.
5. Run affected Go suites, Helm tests/lint, and the daemon-cache-loss
   integration test.

### Task 12: Define and persist exact workflow resource-source admissions

**Files:**

- Create: `agent/workflow/resource_source*.go` and tests.
- Modify current schema-v3 parse/typecheck/hash/render code and tests.
- Create migrations
  `1773106142_create_workflow_resource_source_admissions.{up,down}.sql` and
  `1773106143_capture_build_pipeline_config_provenance.{up,down}.sql`.
- Create current DB factories for standing source pipelines, admissions, and
  selecting builds plus tests; advance head/preflight pointers.

**Steps:**

1. Add failing grammar/type tests for declared ordinary Concourse resources,
   compatible `resource_type/source/check_every/webhook` configuration, and
   snapshot output types.
2. Render one team/workflow/revision-owned standing admission pipeline.
   Workflow runs consume only the selected snapshot and never run an
   independent “latest” lookup.
3. Persist the exact selecting build, pipeline config/version, resource version,
   captured snapshot, owner, type, and frozen workflow revision.
4. Add lifecycle tests for atomic activate/drain/archive and safe pipeline
   ownership.
5. Run `go test ./agent/workflow ./atc/db -count=1` serially.

### Task 13: Capture once and reuse source admissions across retry/replay

**Files:**

- Create: `agent/workflowrun/source_{admission,manual_admission,build_reconciler,pipeline_lifecycle}.go`
  and tests.
- Modify: `agent/resourcecapture/`, `agent/workflowrun/{types,binder}.go`,
  experiment composition, `atc/atccmd`, DB interfaces/fakes, and tests.
- Add DB/Fly/Topgun integration coverage.

**Steps:**

1. Add failing tests for scheduler-selected automatic admission and a manual
   start selecting the current version through the same Concourse path.
2. Capture from the persisted selecting build, enforce team/type ownership,
   and mark an admission ready only after the Hangar-backed snapshot is sealed.
3. Bind the ready admission to the first run and reuse the exact binding for
   retries, replays, experiments, and later nodes.
4. Prove workflow revision upgrades create/update standing pipelines without
   per-dispatch pipeline creation.
5. Run affected unit/DB/Fly suites and focused Kubernetes behavior.

### Task 14: Replace the publisher gateway with direct in-ATC publication

**Files:**

- Create/adapt: `agent/publisher/{policy,credentials,mounted_file,snapshot_inspector}.go`
  and `agent/publisher/directgit/`, with tests.
- Modify: `atc/atccmd/agent_publisher.go`, command composition,
  `atc/exec/publish_snapshot_step.go`, and tests.
- Modify current Helm secret/config/image conventions and tests.
- Delete gateway transport/runtime only after direct publication is green.

**Steps:**

1. Add failing tests for destination-scoped policy, narrow mounted credentials,
   server-derived actor/operation key, exact change reinspection, lease and
   idempotency, fresh private Git directory, and credential redaction.
2. Implement direct-to-trunk rebase-and-push in ATC. A changed trunk requires
   rebase, authoritative revalidation, and policy-driven renewed approval;
   never merge.
3. Bind publication to the exact current successful validation from Task 7 and
   current human review when required.
4. Remove HTTP/TLS/multipart gateway code, flags, service, chart resources, and
   tests. Assert publisher secrets are absent from agent pods.
5. Run `go test ./agent/publisher/... ./atc/exec ./atc/atccmd -count=1`,
   publisher Helm tests, and gateway residue searches.

### Task 15: Add durable checkpoint and attempt data models

**Files:**

- Create: `agent/checkpoint/{types,archive,attempt,recovery_policy}.go`, fakes,
  and tests.
- Create migrations
  `1773106144_create_agent_run_checkpoints_and_events.{up,down}.sql` and
  `1773106145_create_agent_run_attempts.{up,down}.sql`.
- Create DB factories for checkpoint heads/objects/generations/effects/events
  and attempts, plus tests; advance head/preflight pointers.
- Modify runtime interruption classification and owner metadata.

**Steps:**

1. Add failing tests for exact-node interruption classification, monotonic
   generations, staged/committed state, append-only events, effect fencing,
   and fresh attempt identity.
2. Re-author the schema against the current workflow-run model. A checkpoint
   references immutable Hangar content and becomes recoverable only after the
   commit transaction.
3. Record “begun but not known complete” external effects as ambiguous and
   require manual review rather than replay.
4. Run checkpoint, migration, DB, and runtime unit suites serially.

### Task 16: Capture committed safe-boundary workspaces through Hangar

**Files:**

- Create/adapt: checkpoint archive endpoints in `cmd/artifact-daemon/`.
- Create: `agent/provider/adapter.go`, fake adapter, runner checkpoint control,
  ATC checkpoint control/coordinator/upload code, and tests.
- Modify: `agent/runner`, `atc/exec/agent_step.go`, current daemon client,
  worker container/quiescence code, and ATC composition.

**Steps:**

1. Add failing tests for a provider-declared safe boundary, bounded pause,
   process/workspace quiescence, filesystem archive validation, upload, and
   atomic generation commit.
2. Make elapsed time the tunable default trigger; also support completion,
   explicit platform boundary, and best-effort preemption-notice triggers.
3. Keep the artifact daemon node-local, but require it to package and commit
   bytes to Hangar before ATC advances the checkpoint head.
4. Measure requested-to-quiesced, archive, upload, total checkpoint latency,
   skipped/failed captures, and lost work since the prior commit.
5. Run checkpoint/daemon/runner/exec unit and integration suites.

### Task 17: Restore into a fresh attempt with capability-gated provider resume

**Files:**

- Create/adapt: worker checkpoint restore, retention, and recovery code.
- Create: provider-specific Claude adapter and contract tests.
- Modify: `agent/runner`, `atc/runtime`, `atc/worker/jetbridge`,
  `atc/exec/agent_step.go`, and tests.

**Steps:**

1. Add failing tests proving restore uses only the latest committed generation,
   verifies Hangar bytes, creates a fresh attempt, and never restores a live
   process claim.
2. Use native provider resume only when a pinned harness capability contract
   proves it compatible with the checkpoint. Otherwise restore the workspace
   and start a new session with bounded reconstruction guidance.
3. The resume prompt must explicitly say processes, sockets, credentials,
   mounts, and other ephemeral state may need reconstruction.
4. Implement checkpoint zero/fallback and manual-review behavior for corrupt,
   missing, incompatible, or effect-ambiguous checkpoints.
5. Run recovery/runner/worker/exec suites, including provider contract tests.

### Task 18: Attribute recovery telemetry and retention to attempts

**Files:**

- Create migrations
  `1773106146_attribute_metrics_to_agent_run_attempts.{up,down}.sql` and
  `1773106147_persist_agent_run_attempt_transcripts.{up,down}.sql`.
- Modify metric/transcript stores, APIs, current workflow-run projections,
  checkpoint retention, daemon/ATC metrics, Helm alerts, and tests.
- Create: `docs/operations/agent-checkpoint-recovery.md`.

**Steps:**

1. Add failing upgrade/data tests preserving legacy metrics/transcripts while
   assigning every new row to an exact execution attempt.
2. Expose counts and durations for interruptions, successful/failed resumes,
   checkpoint-zero fallbacks, lost-work intervals, ambiguous effects, archive
   and restore time, and retained bytes.
3. Implement retention without deleting the committed head or objects needed
   by an active/recoverable attempt.
4. Document guarantees: filesystem only, committed boundaries only, fresh
   processes, at-least-once control with fenced external effects.
5. Run affected API/DB/metrics/daemon/Helm suites.

### Task 19: Prove upgrade, end-to-end behavior, and v3 residue constraints

**Files:**

- Add or update focused integration/live tests and operator documentation only.
- Modify no production behavior unless a failing proof exposes a defect; fix
  such a defect test-first in its owning package.

**Steps:**

1. Prove both fresh install and upgrade from exactly 6138 to the embedded head;
   assert preflight and `migrator.SupportedVersion()` equal that head.
2. Run serially: `make test-unit`, `make test-dev-mcp`,
   `make test-fly-integration`, `make test-integration`, and `helm lint deploy/chart`.
3. Run focused Hangar emulator, source-admission, publisher, checkpoint,
   output-builder, validation, Fly, Elm, Helm, and Kubernetes behavioral tests.
4. On Borg, when configured without uploading repository source or persisting
   a source-derived image, prove daemon-cache-loss restoration and
   environment-gated agent recovery after pod/node loss.
5. Require residue searches to show no live
   `agent_snapshot_grants`, `agent_principals`, `cap1`, publisher gateway,
   root `agent/devmcp`, v1 ci-agent runner, retired gate/function authority, or
   duplicate terminalizer.
6. Run `gofmt` on changed Go files, `git diff --check`, schema-history checks,
   migration checksum/head checks, and a whole-branch independent review.

## Completion criteria

- Every task has an implementer report and independent reviewer verdict in the
  ignored SDD workspace.
- Every discovered defect is either fixed with a regression test or explicitly
  recorded as an external environmental constraint; there are no silent skips.
- The branch is a clean, reviewable semantic transplant rooted at the approved
  Jetbridge commit. The old feature branch and main Jetbridge worktree remain
  unchanged.
