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
