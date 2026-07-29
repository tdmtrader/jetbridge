# Agentic foundations semantic rebase — deferred item catalog

This catalog holds nonblocking findings and improvements intentionally excluded
from the current bounded semantic-rebase task.

Only Critical, High, or genuinely blocking findings should interrupt the active
task. Add everything else here with enough evidence for a later human or agent
to evaluate independently.

## Entry template

### DEFERRED-NNN — Short title

- Task/area:
- Classification:
- Status: Deferred
- Evidence:
- Why it is nonblocking:
- Suggested follow-up:

## Open items

### HUMAN-REVIEW-001 — Task 6 private Secret lifecycle races

- Task/area: Task 6, Jetbridge protected validation mounts
- Classification: Blocking; Human Review Required
- Status: Resolved on 2026-07-29 in `5f87d2671c`; independently accepted
- Evidence:
  1. If `Pods.Create` commits server-side but returns a client timeout or
     transport error, the current error path immediately deletes the exact
     pre-created Secret even though the committed Pod may already reference it.
  2. Owner-bound orphan reaping reads a Secret, confirms the old Pod is absent,
     and then deletes only by name. A replacement Secret with the same name can
     be deleted between those operations.
  3. Milestone verification found a zero-mount regression:
     `make test-unit` ran all 121 suites but failed only Jetbridge. A focused
     `go test -json ./atc/worker/jetbridge -count=1` reproduced 179 failures
     out of 380 specs, and every failure had the same terminal cause:
     `create [pause ]pod: cannot bind incomplete private task mounts to pod`.
     `bindPrivateMountSecrets` requires a nonempty Pod UID even when both the
     created Secret list and `PrivateFileMounts` are empty. Kubernetes fake
     clients used by the ordinary, non-private-mount runtime tests do not
     synthesize that UID, so unrelated Pod behavior cannot run.
- Resolution:
  1. Ambiguous Pod-create errors now use a fresh exact-name read and delete
     authority only when it proves the Pod absent. A found or inconclusive
     result is retained for conservative ownerless reaping; the code does not
     adopt the Pod or need a positive UID/reference match to authorize deletion.
  2. Owner-bound reaper deletion now carries the observed Secret UID
     precondition, matching the existing ownerless cleanup path.
  3. Private-mount binding is a no-op when no private mounts were requested,
     before the Pod UID check; every nonempty mount set retains the UID and
     exact-count requirements.
  4. Focused committed-Pod/failed-response, zero-mount, and replacement-race
     regressions cover the three corrections.
  The restored ordinary path then exposed three
  stale typed-interruption assertions, corrected in `477c4d554a`. Focused
  regressions and a fresh host-access full Jetbridge package passed 381/381;
  two independent blocking-only reviews reported no remaining finding.

### DEFERRED-001 — Clean unpublished dev-capability log staging directories

- Task/area: Task 3, retained dev-capability core
- Classification: Minor cleanup
- Status: Deferred
- Evidence: A failed multi-log staging operation can leave an unpublished
  hidden temporary directory in the task filesystem.
- Why it is nonblocking: The directory is not published as a validation log and
  does not affect result integrity or subsequent task authority.
- Suggested follow-up: Add best-effort cleanup around failed multi-log staging
  after the integration checkpoint.

### DEFERRED-002 — Broader private-mount primitive hardening

- Task/area: Task 6, Jetbridge private file mounts
- Classification: Defense-in-depth
- Status: Deferred
- Evidence: The runtime primitive is currently introduced for one fixed
  server-owned validation mount. Future callers may need a more general policy
  for naming, quotas, audit events, or cross-runtime support.
- Why it is nonblocking: The current acceptance boundary has a single fixed
  server-owned caller and rejects author-supplied configuration.
- Suggested follow-up: Revisit only when a second legitimate private-file
  consumer or another runtime implementation is introduced.

### DEFERRED-003 — Durable whole-tree output commit journal

- Task/area: Task 8, managed output-builder content payload commits
- Classification: Medium hardening
- Status: Deferred
- Evidence: The Task 8 correction now backs up and rolls back record/content
  on ordinary publication errors, then installs `record.json` last. It still
  does not persist/fsync a recovery journal across an abrupt host crash between
  individual directory operations.
- Why it is nonblocking: Normal successful and rejected writes are covered by
  focused tests; the final sealer always independently reopens the resulting
  tree and rejects malformed or changed bytes.
- Suggested follow-up: Adopt a durable, fsynced output-tree transaction and
  recovery marker before adding producer workloads that rely on multi-file
  output updates surviving abrupt node loss.

### DEPENDENCY-001 — Task 9 waits for the protected-mount trust boundary

- Task/area: Task 9, output-builder execution wiring
- Classification: Blocking dependency; task not started
- Status: Resolved on 2026-07-29; Task 9 is accepted through `9375fae0b8`
- Evidence: The current runtime has no independent server-owned file seam for
  a managed sidecar. `runtime.PrivateFileMount` is the protected mechanism,
  but Jetbridge projects it only into the main container. Task 9 must extend
  that same Secret lifecycle to the output-builder sidecar. The ambiguous
  Pod-create cleanup and replacement-reaper races can remove live authority,
  while the zero-mount Pod-UID regression prevents ordinary agent execution
  and its package-wide verification.
- Why it is blocking: Building Task 9 on the unresolved primitive would make
  a new security boundary depend on code already marked Human Review Required,
  and would add a second consumer before the primitive's lifecycle is trusted.
- Resolution/follow-up: `HUMAN-REVIEW-001` is resolved. Task 9 reuses the
  private Secret lifecycle only for its fixed managed builder, binds exact
  typed projections to canonical authority and physical volume sources, and
  passed the full focused Jetbridge/runtime checkpoint plus final round-3
  blocking review.

### DEFERRED-004 — Run the Hangar emulator target in an emulator-capable environment

- Task/area: Task 10, emulator-backed GCS integration verification
- Classification: Environmental verification gap
- Status: Resolved on 2026-07-28
- Evidence: `make test-hangar-integration` was attempted once in the sandbox
  and once with approved host access on 2026-07-28. Both stopped at the target
  prerequisite with `ERROR: a running Docker daemon is required`. The exact
  target was then run against a temporary fake-gcs-server deployment on Borg:
  `CONCOURSE_HANGAR_TEST_GCS_ENDPOINT=http://127.0.0.1:54443/storage/v1/
  make test-hangar-integration`. The production adapter suite passed all
  immutable/idempotent, concurrent-writer, corruption, truncation, and
  complete-local-loss recovery cases.
- Resolution: The temporary namespace `codex-hangar-it-019fa137` and its
  deployment/service were deleted after the run; a follow-up namespace lookup
  returned no object.

### DEFERRED-005 — Reclaim unreachable Hangar objects after split persistence failure

- Task/area: Task 11, artifact-daemon durable PUT and location persistence
- Classification: Retention/cost hardening
- Status: Deferred
- Evidence: The daemon can successfully commit an immutable Hangar object and
  then fail before ATC durably records the corresponding canonical location.
  Legacy cleanup is intentionally cache-only, so this narrow failure window
  can leave an unreachable object.
- Why it is nonblocking: The published snapshot is not acknowledged as
  durable and no reachable content is lost or corrupted. Immutable orphan
  bytes consume storage but do not violate the Task 11 read/write authority
  boundary.
- Suggested follow-up: Add an inventory/reconciliation job that compares
  Hangar objects with durable snapshot locations and removes only objects that
  remain unreferenced beyond a conservative grace period.

### DEFERRED-006 — Decide whether Hangar egress policy is mandatory

- Task/area: Task 11, artifact-daemon NetworkPolicy
- Classification: Deployment hardening
- Status: Deferred
- Evidence: Selected TCP/443 peers constrain Hangar egress when the optional
  artifact-daemon egress policy is enabled. Operators can disable that policy,
  in which case this chart does not enforce narrow Hangar egress.
- Why it is nonblocking: This follows the chart's existing opt-in egress
  policy posture and does not weaken clusters that enable it. Making it
  mandatory would be a broader deployment-compatibility decision.
- Suggested follow-up: Evaluate mandatory egress enforcement for a hardened
  chart profile, including DNS and emulator endpoint requirements, before
  changing the default.

### HUMAN-REVIEW-002 — Make exact resource-source bindings mandatory at launch

- Task/area: Task 12, source-bearing workflow execution contract
- Classification: High; load-bearing correctness boundary
- Status: Resolved on 2026-07-29 in `875d604026`; independently accepted
- Evidence: The correction added a private exact-reference envelope and
  `BindExecutionParams`, which injects snapshot IDs and rejects substitutions.
  However, `RenderedFunction.Config` remains publicly available and no current
  launch path is required to call the binder. A caller can therefore submit
  the bare parameterized config with a different `snapshot_*` value.
- Addressed in the correction: admission rows are now derived from selecting
  build inputs and frozen declarations; archive/create serialize on the owner
  row; focused lifecycle/provenance DB specs pass.
- Why human review is required: Making the binder mandatory changes the
  cross-component render/launch contract. The bounded task has exhausted its
  one correction pass, and Task 13 would make this interface operational.
- Proposed choices:
  1. Make the exact bound envelope the only exported launch artifact for
     source-bearing workflows and hide bare `Config`.
  2. Keep the current render type but change the sole execution seam to accept
     `RenderedFunction` and always call `BindExecutionParams`, rejecting raw
     config submission.
  3. Split declarative rendering from an explicit server-owned launch type,
     with construction possible only after exact source admission.
- Required verification: prove every current launch entry point rejects a
  substituted snapshot ID and that source-free workflows retain their current
  behavior.
- Resolution: The user selected option 3. Declarative `RenderedFunction`
  remains available for preflight, while the sole production Binder-to-DB
  workflow launch path now requires an opaque `workflow.ExecutionEnvelope`.
  Trusted rendering privately retains the exact canonical config, rendered
  authority hash, and selected source refs; envelope construction injects
  those refs and rejects substitutions. Public config/hash mutation and bare
  structs cannot mint authority, cloning explicitly preserves private
  authority, and the DB opens parameters only after locking and matching the
  durable canonical config and template hash. Source-free workflow and compile
  suites passed; host PostgreSQL specs passed for both valid creation and
  zero/canonical-mismatched rejection. Independent blocking-only review found
  no remaining issue.

### DEPENDENCY-002 — Task 13 waits for mandatory exact-source launch binding

- Task/area: Task 13, workflow source capture/retry/reuse runtime
- Classification: Blocking dependency; task not started
- Status: Resolved on 2026-07-29; Task 13 is unblocked but not started
- Evidence: Task 13 must bind captured snapshots into workflow execution and
  reuse them across retry/replay. The only binder is currently optional, so
  wiring runtime orchestration now would institutionalize the bypass.
- Resolution/follow-up: `HUMAN-REVIEW-002` is resolved by the mandatory opaque
  launch envelope and substitution coverage in `875d604026`. Implement
  capture/reuse against that boundary without adding a second launch API.

### DEPENDENCY-003 — Task 14 waits for exact validation gates

- Task/area: Task 14, direct in-ATC publication
- Classification: Resolved ordering dependency; task not started
- Status: **Resolved** on 2026-07-29; Task 14 is unblocked
- Evidence: The approved dependency order requires exact validation gates
  before removing the publisher gateway. Tasks 6 and 7 are accepted; Task 7's
  runtime gate reopens and verifies the exact current authoritative
  validation/v1 record before any publisher side effect.
- Resolution/follow-up: Implement Task 14 against the accepted Task 7 boundary
  without restoring the external gateway.

### HUMAN-REVIEW-003 — Use real database time for checkpoint mutation fences

- Task/area: Task 15, checkpoint/effect attempt-fence authority
- Classification: High; stale-writer correctness boundary
- Status: **Resolved** by the user-authorized correction and final scoped
  review
- Evidence: `requireAttemptFence` calls `checkpointDatabaseNow`, which executes
  `SELECT now()`. PostgreSQL fixes `now()` at transaction start. A transaction
  can therefore begin while its fence is live, block on the checkpoint
  head/current-attempt lock until after real lease expiry, and still pass the
  stale transaction timestamp before committing a checkpoint mutation or
  beginning an effect.
- Addressed in the correction: every public staged mutation and effect begin
  now carries an exact `FenceClaim`; staged/effect rows persist the token;
  attempt/token/state checks are serialized in the mutation transaction;
  `CommitEffect` can close only the exact already-authorized effect. Exact
  same-head recovery-source validation/pinning and recursive fake compilation
  are also complete.
- Resolution: commit `aafd924521` changes the database-authoritative time
  helper to `clock_timestamp()`. Because `requireCurrentCheckpointFence`
  acquires the current-attempt row lock before calling `requireAttemptFence`,
  the expiry sample now occurs after any lock wait.
- Regression: the new PostgreSQL spec records the mutation transaction's
  backend PID, observes its ungranted transaction lock while the fence is live,
  waits until database wall time crosses expiry, releases the attempt row, and
  requires `ErrStaleFence`. It failed against `SELECT now()` because `Begin`
  succeeded, then passed after the correction.
- Final evidence: checkpoint/attempt DB focus passed 28/28, migration focus
  passed 2/2, recursive checkpoint/fake tests and affected-package compile
  checks passed, and the single authorized Terra review reported no blocking
  findings.

### DEPENDENCY-004 — Tasks 16–18 wait for accepted checkpoint fence authority

- Task/area: Tasks 16–18, capture, restore/provider resume, telemetry, and
  retention
- Classification: Resolved ordering dependency
- Status: **Resolved**; Tasks 16–18 are accepted
- Evidence: Task 16 commits captured workspace generations and journals
  effects through Task 15's mutation fence. Task 17 restores those committed
  generations into fresh attempts. Task 18 attributes recovery metrics and
  releases retained sources. Building any of these on a fence that can remain
  valid under stale transaction time would institutionalize a stale-writer
  race at the recovery boundary.
- Resolution: Task 16 accepted bounded committed capture; Task 17 accepted
  fresh-attempt restore and capability-gated resume; Task 18 accepted
  exact-attempt telemetry, retention, alerts, and operations guidance. Their
  final commits are `91d8e47fb0`, `1f6e9f67d0`, and `986f1e591d`.

### DEPENDENCY-005 — Task 19 waits for unresolved Human Review Required tracks

- Task/area: Task 19, final upgrade/end-to-end/residue proof
- Classification: Blocking dependency; task not started
- Status: Deferred
- Evidence: Tasks 6, 7, 9, 12, and 16–18 are accepted, but Tasks 13 and 14
  remain unstarted. Task 19 still cannot truthfully prove source capture,
  publication, or repository-wide acceptance while those implementations are
  absent.
- Suggested follow-up: Complete Tasks 13 and 14, then run Task 19 once as the
  final milestone rather than repeatedly running broad suites against a
  knowingly incomplete branch.
