# Hangar

The durable result plane: immutable filesystem trees published under tree
refs, and the protocol by which a task's output becomes one. It has one
consumer, the pipeline Run, which composes with it through a caller-owned
transaction and an opaque identity. Includes the web's capture driver,
reclaim pass and orphan sweep in `atc/hangaroutput`.

## Language

### Exact trees

**Hangar**:
The durable result plane for a Run's inputs and results: immutable trees
addressed by exact reference and failing closed. A Run input enters as an
input publication and reaches a task as a managed read; a Run result enters
as a capture.
_Avoid_: exact tree storage, durable storage (that is the runtime's
fail-open resource-cache tier)

**Tree**:
A canonical filesystem tree: a bytewise POSIX namespace with deterministic
archive bytes.
_Avoid_: snapshot, volume (that is the runtime's)

**Tree ref**:
The complete reference to one tree: scope, digest and generation.
Hangar never substitutes a newer generation or different content.
_Avoid_: exact ref, exact reference, exact tree

**Scope**:
The opaque namespace component of a tree ref, filled in by the control
plane and never accepted from a caller. It is H(domain, tenant, store),
and that is the only derivation.
_Avoid_: prefix, bucket path, legacy scope, scope v1

**Digest**:
The logical-content digest of a canonical tree.
_Avoid_: hash, checksum (of an archive's bytes)

**Generation**:
The object-store generation pinned in a tree ref. A store never reuses one.
_Avoid_: version, revision

**Canonicalization**:
Turning an archive stream or a step directory into a tree under the entry
and content limits.
_Avoid_: normalization, packing

**Managed read**:
A task's read of one published tree ref out of the output namespace: the
web takes a reader's claim and mints a read warrant; the step pod's
managed-input init presents it to the node's artifact daemon, which
materializes the tree. The only way a tree reaches a task. It fails closed
on absence, corruption, conflict, authorization, limits or infrastructure
failure.
_Avoid_: exact input, immutable input, Hangar input

**Materialization**:
The daemon opening, verifying and copying a published tree into one task's
step volume under a read warrant, through its scratch space.
_Avoid_: download, restore (those are the durable tier's)

**Materialization receipt**:
The read-only `.hangar-materialized` record a managed read leaves in the
step volume, naming the exact tree ref it materialized; the managed-input
init checks it before the task starts.
_Avoid_: receipt (alone)

**Warrant**:
A short-lived, signed, attenuated authorization for exactly one target,
issued by the web with the one Hangar key and verified by the daemon;
qualified by one of three purposes: a read warrant (a managed read), or a
control warrant (execution control or output capture). A route admits only
its own purpose.
_Avoid_: grant (that is the agentic context's word), capability, token, facet

**Read warrant**:
The warrant bound to one reader's claim that lets a reader open that
generation on one node while the claim lasts; single-use by claim on the
node. A managed read carries one.
_Avoid_: download token, input token

**Warrant key**:
The one raw 32-byte secret, `hangar.key`, the web signs every warrant with
and every artifact daemon verifies against. Never present in a task pod.
The daemon holds no signing key of its own: there is no node control key.
_Avoid_: capability key, materialization key, control key

**Scratch path**:
The daemon's private transient space where canonicalization and
verification happen.
_Avoid_: temp dir, work dir

### Output plane

**Output plane**:
The half of Hangar that turns a task's declared output into durable,
claimable content. A part of the artifact daemon on each node and of the
web; not a process of its own. The daemon mounts it when exact execution
control is configured.
_Avoid_: durable output publication, output daemon

**Capture**:
Selecting one declared output of one finished task for publication. A
captured task gives up post-completion hijack.
_Avoid_: upload, snapshot

**Capture row**:
The one durable record of a capture, keyed by execution and output. It is
pending, publishing, published, discarded or failed, and moves only by
compare-and-set.
_Avoid_: handoff, capture record

**Pending**:
A capture row whose digest is not yet written: the producer may still be
running.
_Avoid_: unresolved

**Publishing**:
A capture row whose digest is written and whose object may already exist.
Cancellation can no longer discard it.
_Avoid_: resolved, reserved

**Published**:
A capture row whose generation is written: its tree ref is durable,
registered and claimed.
_Avoid_: registered, committed

**Discarded**:
A pending capture row that will never publish: its Run was cancelled, its
build aborted, or its producer failed or was stopped.
_Avoid_: cancelled, no capture

**Failed**:
A capture row that cannot publish: no marker on its node, a collision, a
deadline passed, or its sealed step is gone from its node.
_Avoid_: errored, lost

**Coordinator**:
The web's capture driver: it drives each capture row through six steps,
each committed as an insert, a compare-and-set or a stamp.
_Avoid_: controller, worker

**Capture deadline**:
The bound on a pending capture. A capture row still pending past it fails.
_Avoid_: seal deadline, timeout

**Step marker**:
The one JSON file beside a step directory that is the only node-local
capture state: held, sealed, or released (a tombstone). The file is the
record; nothing wraps it.
_Avoid_: control record, source incarnation, writer ticket

**Source hold**:
A step marker in the held state, written by the control init before the
step's first container starts.
_Avoid_: source lease, hold (alone)

**Seal**:
Turning a held marker sealed, waiting until every container of that exact
Pod has terminated, and canonicalizing the directory, so the published tree
is exactly what the producer left.
_Avoid_: freeze, drain

**Release**:
Clearing a terminal capture row's step marker on its node and stamping the
row released.
_Avoid_: release intent, unhold

**Announcement**:
The step event a watcher of an execution sees about a capture, derived from
its capture row's state and reason.
_Avoid_: notification

**Source ledger**:
The artifact daemon's answer, from its step markers, to whether a step
directory may be destroyed: unmanaged, held, sealed or unavailable. Only
unmanaged permits destruction.
_Avoid_: capture ledger

**Input publication**:
A Run input a Run uploads, staged on a node under a reservation id and
then published as a tree ref in the output namespace. The reservation id is its correlation handle
on the wire (`hangar-output-reservation-id`), not a capture reservation.
_Avoid_: upload (alone), reservation (alone)

**Object marker**:
The ownership metadata written once at object creation and never updated:
which store, which scope, which digest. An object carrying none of it is
unmanaged; one carrying it malformed is corrupt.
_Avoid_: label, tag

**Tree lock**:
The database lock on one scope and digest that a capture moving to
publishing, the reclaim pass and an orphan verdict each take, so none
interleaves with another over the same tree.
_Avoid_: dedup lock

**Lifecycle**:
The record that one published generation is managed by this plane. It is
registered, or reclaimed; a reclaimed generation never resurrects.
_Avoid_: registration, inventory

**Claim**:
A consumer's or a reader's opaque, idempotent hold on a tree ref; the one
refcount. A consumer's lasts until released; a reader's also expires, on
the database clock. Hangar never interprets or reacquires one. A Run's
result binds as another Run's input while its Run is succeeded and its
claim is live, nothing more.
_Avoid_: reference, pin, read lease, lease

**Reclamation**:
The web's one pass over registered generations: one that no live claim, no
pending or publishing capture and no unregistered input publication names
is deleted by its exact generation and stamped reclaimed, in one
transaction under the tree lock. Only the web reclaims.
_Avoid_: garbage collection, purge, reclaim admission, reclaim job

**Orphan sweep**:
The web's periodic pass that deletes an old object marked for this store
that no lifecycle or capture accounts for, and counts every other object
it cannot account for.
_Avoid_: inventory, adoption, garbage collection

**In service**:
Whether the output plane admits new work. Out of service, nothing new is
admitted and what is in flight finishes.
_Avoid_: activation epoch, activation, enabled (alone)

**Residue**:
What a drain still waits on: pending or publishing capture rows, terminal
capture rows not yet released, open claims. Releases no node acknowledged
are reported beside it, not counted in it.
_Avoid_: debt, backlog

**Integrity finding**:
The durable record of an unexpected object absence (a read found a
registered generation missing) or a runtime authorization failure. An open finding blocks new admission until an
operator resolves it.
_Avoid_: at risk, policy violation

**Principal**:
The storage identity a process holds: the node daemon publishes; the web
lists and deletes. Delete exists only in the web.
_Avoid_: role, persona

**Execution control**:
The base protocol by which the web learns and settles one exact
execution's fate on its node; capture extends it. Every call carries a
control warrant of the route's purpose. The node answers over
the mTLS channel the web reached it on; an acknowledgement is that answer,
and the channel, not a signature, is what makes it the node's. There is no
node control key and no key generation.
_Avoid_: lease control, signed acknowledgement, control-key generation

### Deployment

**Disk store**:
The persistent-volume store one cluster runs for the output plane's
namespace, with optionally a second, cache-only instance for the runtime's
fail-open tier. Initialized exactly once; its store ID is its identity.
_Avoid_: local store, PVC store

**Cache namespace**:
The bucket or disk namespace the runtime's fail-open durable tier uses. It
is one of two namespaces -- cache and output -- which may not be the same;
a process refuses to start otherwise.
_Avoid_: shared bucket

**Bootstrap inventory**:
The one declared list of Secrets a Hangar deployment needs: each entry's
name, material, the purpose of each key and its consumers. It holds CAs,
leaves, bundles, symmetric keys and tokens; there is no key ring. The
bootstrap creates an absent entry and never replaces, rotates or deletes
one.
_Avoid_: inventory (alone), key ring
