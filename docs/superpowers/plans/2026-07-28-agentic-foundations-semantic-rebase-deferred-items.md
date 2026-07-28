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
