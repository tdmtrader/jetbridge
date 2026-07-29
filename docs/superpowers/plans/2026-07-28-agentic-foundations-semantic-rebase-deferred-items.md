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
