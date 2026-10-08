# Hangar: relationships and invariants

Vocabulary: [`hangar/CONTEXT.md`](../../hangar/CONTEXT.md). The decision
behind this shape is [ADR-0009](../adr/0009-one-node-daemon-one-capture-row.md).

## Two halves

```
Strict input:  task ──materialization warrant──▶ artifact daemon ──materializes──▶ tree ref
Output plane:  task output ──capture row + step marker──▶ publish ──▶ tree ref ──claim──▶ Run
```

Both halves live in two processes: the artifact daemon on each node, and the
web. `hangar-store` serves the disk store, and a bootstrap Job creates the
Secrets the deployment needs. Nothing else runs. Hangar is a
leaf: `hangar/` imports nothing from core, and the web composes with it
through a caller-owned transaction and opaque identities.

## Tree refs

A tree ref is scope, digest and generation, all three. Scope is derived by
the control plane as H(domain, tenant, store), and that is the only
derivation; it is never accepted from a caller. Hangar never substitutes a
newer generation or different content. A strict input names a complete tree ref and fails closed on
absence, corruption, conflict, authorization, limits or infrastructure.

## Storage

One object interface (create-absent, current and exact stat, exact open,
list) with two backends, GCS and the disk store. Exact delete is a separate
interface constructed only by the web's reclaim pass and orphan sweep, and by
the durable cache tier over its own namespace.

Three namespaces: **cache** (the runtime's fail-open tier), **input** (strict
inputs) and **output** (captures). No two may be the same bucket or disk
namespace; the daemon and the web refuse to start otherwise. Retention class
is a key prefix.

## The capture row

```
             ┌──▶ discarded
             │
pending ──▶ publishing ──▶ published
   │            │
   └────────────┴──▶ failed
```

One row per (execution, output), the only durable capture state. Every move
is a compare-and-set; a replayed commit whose answer was lost succeeds, and
anything else is a conflict. Identity and a written digest never change.
`released_at` is a one-way stamp on a terminal row.

The coordinator in the web drives each row in six steps, holding no
database lock across a network call and nothing in memory between calls:

1. **Step start.** The web inserts a pending row naming the node (the Pod
   UID is written with the digest at publishing);
   the step's control init writes a held step marker before the first
   container starts.
2. **Seal.** After the step exits the daemon flips the marker to sealed,
   waits until every container of that exact Pod UID has terminated (it
   never deletes one), canonicalizes the directory into scratch and answers
   the digest.
3. **CAS pending → publishing**, writing the digest before any object can
   exist, under the tree lock (below). A cancelled Run, aborted build, failed or stopped producer is
   instead CAS pending → discarded.
4. **Publish.** The daemon creates the object for that digest, absent-only,
   with an object marker naming this store. On a precondition failure it
   stats the existing object and compares marker and digest: a match joins
   that generation, anything else is a collision and the row fails.
5. **CAS publishing → published**, writing the generation; the same
   transaction registers the generation's lifecycle and takes the capture's
   claim, and the Run sees the announcement.
6. **Release.** For every terminal row with no `released_at`, the daemon
   clears the marker, then the web stamps `released_at`. Retried every pass;
   the sweeper refuses the directory until then. A row whose node is gone is
   released without acknowledgement after a margin, and the row records it.

No marker is never "capture what is there": a capture asked of a node that
did not hold the step fails, and no empty tree is published.

## The step marker

One JSON file per step directory, and nothing around it. It is replaced by
writing a temp file, fsyncing it, renaming it over the old name and fsyncing
the directory, so a reader sees the whole new record or the whole old one. It says `held`, `sealed` or
`released` (a tombstone, so no hold is taken for that step again), with the
execution, output, node and Pod UID. The daemon's source ledger reads the
markers and answers every destructive request unmanaged, held, sealed or
unavailable. Only unmanaged permits destruction. The reader decodes a marker
leniently and has one rule for what it does not understand: a state it does
not know is held, and a file it cannot decode makes the whole ledger
unavailable. The output plane's own ledgers read the same way: a record that
does not decode refuses the one execution it names.

## Recovery

Recovery reads the row, because the marker kept the directory:

- **publishing** with a digest: stat the store for that digest; complete
  step 5 onto what is there, or repeat step 4. When the store holds nothing
  and the sealed directory is gone from its node, fail.
- **pending** past its capture deadline: fail. A publishing row past the
  deadline plus a margin: fail.

## Claims and reclamation

```
Run ──claim──▶ tree ref ◀──claim (expiring)── reader (read warrant)
                  │
   reclaim pass: hold ──▶ delete exact generation ──▶ stamp reclaimed
```

- A **claim** is the one refcount. It is opaque and idempotent; Hangar never
  interprets one. A consumer's claim (a Run's result binding, an input
  publication, a capture's own) lasts until released. A reader's claim also
  carries an expiry: the read's materialization timeout plus five minutes,
  on the database clock, so an abandoned read pins nothing for long. The
  web takes it in the consumer's transaction, mints the read warrant over
  the committed row (claim id, ref, destination, node, acquired-at,
  expires-at, nothing from the mint), and releases it when the read ends.
  The node daemon verifies the warrant and keeps it single-use per claim.
- A **lifecycle** is registered or reclaimed. Registration is an insert that
  does nothing on conflict and refuses a row already stamped reclaimed, so a
  reclaimed generation never resurrects.
- The **tree lock** is a transaction-scoped advisory lock on (scope,
  digest). A capture's move to publishing, the reclaim pass and an orphan
  verdict all take it, so none of them interleaves with a capture
  deduplicating onto the same generation.
- **Reclamation** is one pass in the web under a PostgreSQL advisory lock.
  It selects registered generations past publication grace that no live
  claim, no pending or publishing capture and no unregistered input
  publication names, and for each, in one transaction: takes the tree lock
  and the lifecycle row, rechecks those exclusions, deletes the exact
  generation, and stamps `reclaimed_at` once the store answered. The locks
  are held across the delete on purpose: a claimant waits on the row and
  then finds the generation reclaimed. Confirmed, already absent and a
  generation conflict (the exact generation is gone; the object at the key
  is another's) all stamp; unauthorized records a principal-denial finding
  and leaves the row registered; a timeout or infrastructure failure rolls
  back and the next pass retries the delete.
- The **orphan sweep** runs in the web under the same lock. It lists the
  output namespace from the start and deletes, by the exact listed
  generation, only an object whose marker names this store, with no
  lifecycle, nothing pending or publishing that could register it, and
  older than twice the capture deadline. The marker's store is the bucket,
  the deployment prefix and the scope, so two installs sharing a bucket
  never delete each other's objects. A foreign-marked or unmarked object is
  counted and never touched. The sweep judges one listed page at a time,
  re-judges only that page's orphans under the tree lock in the transaction
  that deletes them, and stops at a per-pass duration budget; failed passes
  are counted and alerted on.

## In service and drain

The output plane is in service when the `hangar_enabled` row says so. The
web writes it at startup from its configuration; every admission (Run
creation, capture start, exact-execution start, input upload) takes it FOR
SHARE. An open integrity finding also blocks admission until an operator
resolves it (`fly hangar-status --resolve-finding`): a runtime authorization
failure, or an unexpected absence -- a managed read that finds a registered
generation missing records one, and the read fails closed.

Drain: turn the plane out of service; in-flight captures finish; `fly
hangar-status` reports the residue (pending and publishing captures,
terminal captures not yet released, open claims) and, beside it, releases
no node acknowledged. A node's acknowledgement is its answer on the mTLS
channel the web reached it on; nothing is signed, and there is no key
generation to put in or take out of service.
Remove the plane when every residue count is zero at once.

## Trust

Step pods are untrusted. Every pod-originated daemon call carries a warrant,
and every web-originated control call a control warrant; all are signed with
the one Hangar key, which never enters a task pod; a route admits only its
own purpose; no callable accepts a bare string or a caller-chosen scope (`hangar/output/architecture_test.go`). The node daemon
is inside the trusted computing base, reached over mTLS from the web only:
an acknowledgement is the node's answer on that channel, not a signed
statement, and the node holds no control key. The daemon holds no database
credential and no delete credential over the input or output namespace,
and never calls the web; every off-node route needs an mTLS client
certificate. The reader verifies a tree against the digest in the row.
