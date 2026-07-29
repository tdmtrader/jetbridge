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
- Status: Automatic iteration stopped after the configured review budget
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
- Why it is cataloged: These are blocking rather than ordinary deferred
  improvements, but Task 6 has exhausted its two-round review budget and must
  not enter another automatic correction loop.
- Suggested human-approved follow-up:
  1. On ambiguous Pod-create errors, reconcile the exact Pod name, UID, and
     Secret volume reference before deleting; retain/reap conservatively when
     the outcome cannot be proven.
  2. Give the owner-bound reaper deletion the observed Secret UID precondition,
     matching the existing ownerless cleanup path.
  3. Make private-mount binding a no-op when no private mounts were requested,
     before requiring a Pod UID; retain the UID requirement whenever at least
     one private mount exists. Add a focused zero-mount fake-client regression.
  4. Add focused regressions for a committed-Pod/failed-response fake and a
     read/delete replacement race.

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
- Status: Deferred until `HUMAN-REVIEW-001` is resolved
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
- Suggested follow-up: Resolve `HUMAN-REVIEW-001`, rerun the full Jetbridge
  suite, then implement Task 9 using a narrowly selected private mount for the
  managed builder sidecar. Do not expose private mounts to arbitrary sidecars.

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
- Status: Human Review Required after review round 2
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

### DEPENDENCY-002 — Task 13 waits for mandatory exact-source launch binding

- Task/area: Task 13, workflow source capture/retry/reuse runtime
- Classification: Blocking dependency; task not started
- Status: Deferred until `HUMAN-REVIEW-002` is resolved
- Evidence: Task 13 must bind captured snapshots into workflow execution and
  reuse them across retry/replay. The only binder is currently optional, so
  wiring runtime orchestration now would institutionalize the bypass.
- Suggested follow-up: Resolve `HUMAN-REVIEW-002`, add launch-seam substitution
  coverage, then implement capture/reuse against the mandatory envelope.

### DEPENDENCY-003 — Task 14 waits for exact validation gates

- Task/area: Task 14, direct in-ATC publication
- Classification: Blocking dependency; task not started
- Status: Deferred until Tasks 6–7 are accepted
- Evidence: The approved dependency order requires exact validation gates
  before removing the publisher gateway. Task 6 remains Human Review Required,
  Task 7 is consequently unstarted, and publication must not consume an
  untrusted or stale validation boundary.
- Suggested follow-up: Resolve Task 6, complete and accept Task 7, then
  implement Task 14 without restoring the external gateway.

### HUMAN-REVIEW-003 — Use real database time for checkpoint mutation fences

- Task/area: Task 15, checkpoint/effect attempt-fence authority
- Classification: High; stale-writer correctness boundary
- Status: Human Review Required after review round 2
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
  are also complete. Host DB behavior passed 27/27 and migrations passed 2/2.
- Why human review is required: Task 15 exhausted its two-round review budget.
  The remaining code change is narrow, but the agreed policy prohibits another
  automatic correction/review cycle.
- Proposed human-approved repair:
  1. Evaluate lease expiry with `clock_timestamp()` rather than transaction
     `now()`, preferably inside the final authorizing mutation predicate.
  2. Add a concurrency regression that starts while the lease is valid, waits
     on the locked head/attempt row until wall-clock expiry, and proves the
     mutation returns `ErrStaleFence`.
  3. Rerun the 27-spec checkpoint/attempt DB focus, 2 migration specs,
     recursive checkpoint packages, and one final scoped review.

### DEPENDENCY-004 — Tasks 16–18 wait for accepted checkpoint fence authority

- Task/area: Tasks 16–18, capture, restore/provider resume, telemetry, and
  retention
- Classification: Blocking dependency; tasks not started
- Status: Deferred until `HUMAN-REVIEW-003` is resolved
- Evidence: Task 16 commits captured workspace generations and journals
  effects through Task 15's mutation fence. Task 17 restores those committed
  generations into fresh attempts. Task 18 attributes recovery metrics and
  releases retained sources. Building any of these on a fence that can remain
  valid under stale transaction time would institutionalize a stale-writer
  race at the recovery boundary.
- Suggested follow-up: Resolve and accept Task 15, then implement Tasks 16, 17,
  and 18 in dependency order with their existing bounded review budgets.

### DEPENDENCY-005 — Task 19 waits for unresolved Human Review Required tracks

- Task/area: Task 19, final upgrade/end-to-end/residue proof
- Classification: Blocking dependency; task not started
- Status: Deferred
- Evidence: Tasks 6, 12, and 15 remain Human Review Required. Tasks 7, 9,
  13–14, and 16–18 are consequently unstarted or dependency-deferred. Task 19
  cannot truthfully prove complete upgrade, recovery, publication, or
  repository-wide acceptance while those boundaries remain unresolved.
- Suggested follow-up: Resolve the three Human Review Required entries,
  complete their dependent tasks, then run Task 19 once as the final milestone
  rather than repeatedly running broad suites against a knowingly incomplete
  branch.
