---
status: accepted
date: 2026-10-07
supersedes: the cohort, receipt and storage-role parts of ADR-0005
amends: ADR-0002 (consequence: namespace separation)
---

# One node daemon, one capture row

Hangar had grown to seven binaries, two daemons per node, five storage
backends, twenty-two tables, an output daemon with thirty routes and an
eight-kind leased controller, for one consumer (a pipeline Run's inputs and
results) on one cluster. Its activation cohort was frozen when it was
attested, so a node added later (the second k3s agent) was refused, with no
operator path to rotate it: the design carried a live bug.

We collapsed it:

- **One daemon.** The artifact daemon on each node serves the resource
  cache, strict-input materialization (amended by ADR-0010: gone; a Run
  input is an input publication read back as a managed read), input
  publication, result reads, the base execution-control protocol and the
  capture routes (seal, publish, release, stat). Nothing else listens on a node. The output
  daemon, the activation command, the inventory and the reclaimer binaries
  are gone.
- **One storage interface, two backends, three namespaces** (amended by
  ADR-0010: two, cache and output). GCS and the disk store behind one
  object interface; exact delete is a separate interface constructed only
  in the web (and by the cache tier over its own namespace). Cache, input
  and output are three buckets or disk namespaces, and a process refuses
  to start with any two equal (amended by ADR-0010: the input namespace is
  gone; the inequality is cache ≠ output).
- **A capture is one row and one marker.** `hangar_captures` moves pending →
  publishing → published (or discarded, failed) by compare-and-set, driven
  by the web in six steps; the node keeps one step marker file per step
  directory (held, sealed, released). The digest and the publishing state
  are written before any object can exist, so recovery is a stat.
- **No receipts.** The daemon is inside the trusted computing base, reached
  over mTLS from the web only; it returns a digest and the web writes the
  row. The reader verifies the tree against the row's digest. A signed,
  challenge-bound receipt and its key ring proved nothing the mTLS channel
  and the reader's check do not.
- **No activation epochs, cohort or attestation.** In service is one row
  the web writes from its configuration and every admission reads FOR
  SHARE, plus node readiness labels; there is no cohort to attest and so no
  frozen cohort. This decision originally kept a control-key generation,
  recorded on every lifecycle, claim, input publication and Run input
  under the spelling `activation_epoch`; ADR-0010 removed it, with the
  node control key ring and acknowledgement signing it alone justified.
- **Reclaim and the orphan sweep run in the web**, under one advisory lock,
  admission excluding pending and publishing captures, claims and live
  read leases, and the sweep marker-gated to this store (bucket, prefix and
  scope) and bounded by a per-pass budget. A per-tree advisory lock orders
  a capture's move to publishing against reclaim and the sweep, since the
  CAS alone cannot see a dedup onto the same generation.
- **Product neutrality is dropped.** Hangar has one consumer. The
  vocabulary scan is gone; the import-leaf rule and the rule that no
  callable takes a bare string or a caller-chosen scope stay.

## Consequences

- Amended by ADR-0010 (control-key generation removed): the daemon is
  trusted over mTLS, so an acknowledgement is its answer on that channel;
  there is no control key, key ring or generation, and no scope v1.
- Integrity findings remain the one stop on admission, and a managed read
  that finds a registered generation missing records one.
- Node daemons hold publisher credentials only; the web holds list and
  delete over the output namespace. The daemon has no database credential
  and never calls the web.
- Accepted losses:
  - A cache object whose archive is well-formed but whose content is wrong
    is no longer healed by the next producer's upload (puts are
    create-absent, and only an object at fault on restore is expired); it is
    served until its retention class expires it.
  - There is no lifetime audit of a capture's transitions; the row holds
    its current state, reason and timestamps only.
  - Down migrations past this change are lossy: epochs, receipts, handoff
    history and dead cancellation-queue rows are not reconstructed.
  - (Amended by ADR-0010.) Rotating the control-key generation made
    earlier Run results unbindable as Run inputs; with the generation
    gone, a result binds while its Run is succeeded and its claim is live.
  - Nothing audits registered generations against the store: an object
    lost out of band surfaces only when a read records an absence finding
    or a reclaim delete finds it gone.
  - Output history from the handoff era is refused, not migrated:
    migration 1789793153 stops on a database that still holds any.
    (Amended by ADR-0010.) The old scope derivation, which hashed the
    control-key generation, is removed with it; a scope is H(domain,
    tenant, store) and nothing else.
- ADR-0005's storage contract (create-absent, exact operations, the disk
  store's single owner, operators owning infrastructure policy, integrity
  findings blocking admission) stands; its cohort attestation, receipts and
  four storage roles do not.
- ADR-0002 stands: the cache fails open, Hangar fails closed, and the two
  share no read path. The separation is a namespace inequality checked at
  startup, not an import ban.
