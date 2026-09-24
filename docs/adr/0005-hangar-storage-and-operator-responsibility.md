---
status: accepted
date: 2026-09-24
---

# Hangar storage has one immutable contract; operators own infrastructure policy

Hangar supports GCS and a single-owner persistent-disk service through explicit
create-absent, current/exact stat, exact read and bounded list operations.
Exact deletion is a separate capability linked only by the reclaimer. Shared
tree verification and output algorithms do not depend on a cloud SDK.

GCS IAM and lifecycle attestation are removed from runtime admission. Periodic
policy inspection cannot prevent administrator deletion and adds a process,
permissions and stale-evidence failures to every deployment. Operators own
bucket isolation, credentials, lifecycle configuration and recovery. Actual
unexpected absence and runtime authorization failures remain durable findings
that block new admission until explicitly reconciled. Claims, read leases and
generation-conditioned reclamation remain authoritative.

Disk storage uses one process and one block-backed PVC, with immutable blobs
and a transactional bbolt index. The index supplies durable generations and
pending deletion records. Storage identity is explicitly initialized; ordinary
startup refuses an empty or differently identified volume. Four fixed roles
use distinct credentials over verified TLS. There is no general IAM engine,
replication layer or provider-specific implementation of the output protocol.

## Consequences

- ADR-0002's fail-closed invariant is unchanged; its bucket-only description
  now includes a disk namespace. Resource caches remain separate.
- Deployment becomes simpler and runtime cloud identities need no IAM or
  lifecycle read permissions. Infrastructure configuration is an operator
  prerequisite, not something Hangar certifies.
- Disk deployments accept single-owner availability, bounded transfer
  concurrency, capacity monitoring and coordinated backups. NFS, multi-writer
  storage and online provider migration are outside this backend.
- Restores must preserve index/blob consistency and must never reuse issued
  generations. Changing provider or store identity does not retarget old refs.
- Historical policy evidence is retained for audit. Wire/state-machine names
  used by daemon cohort activation remain compatible; cohort attestation is
  separate from the removed storage-policy process.
