# Hangar

The durable result plane: immutable filesystem trees published under tree
refs, and the protocol by which a task's output becomes one. It has one
consumer, the pipeline Run, which composes with it through a caller-owned
transaction and an opaque identity. Includes the web's capture driver,
reclaim pass and orphan sweep in `atc/hangaroutput`.

## Language

### Exact trees

**Hangar**:
The opt-in tier for immutable tree inputs and results, addressed by exact
reference and failing closed.
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
plane and never accepted from a caller.
_Avoid_: prefix, bucket path

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

**Strict input**:
A task input naming a complete tree ref. It fails closed on absence,
corruption, conflict, authorization, limits or infrastructure failure.
_Avoid_: exact input, immutable input, Hangar input

**Materialization**:
The daemon opening, capturing and verifying a tree into its scratch space
for one task.
_Avoid_: download, restore (those are the durable tier's)

**Materialization receipt**:
The local, read-only record that a materialization completed for one exact
tree ref, checked before the task starts.
_Avoid_: receipt (alone)

**Warrant**:
A short-lived, signed, attenuated authorization for exactly one target,
issued by the web and verified by the daemon. Always qualified by what it
authorizes: a materialization warrant or a read warrant.
_Avoid_: grant (that is the agentic context's word), capability, token

**Materialization warrant**:
The warrant bound to one tree ref, handle, volume and expiry that lets the
daemon materialize that tree for that task.
_Avoid_: input token

**Read warrant**:
The warrant bound to one read lease that lets a reader open a generation
under that lease.
_Avoid_: download token

**Warrant key**:
The secret the web signs one kind of warrant with and the daemon verifies
against. One key per warrant kind. Never present in a task pod.
_Avoid_: capability key

**Scratch path**:
The daemon's private transient space where canonicalization and
verification happen.
_Avoid_: temp dir, work dir

### Output plane

**Output plane**:
The half of Hangar that turns a task's declared output into durable,
claimable content. A part of the artifact daemon on each node and of the
web; not a process of its own.
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
A capture row whose step has not been sealed: the producer may still be
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
A capture row that cannot publish: no marker on its node, a collision, or a
deadline passed.
_Avoid_: errored, lost

**Coordinator**:
The web's capture driver: six compare-and-set steps over one capture row,
holding no lock across the network and nothing in memory between calls.
_Avoid_: controller, worker

**Capture deadline**:
The bound on a pending capture. A capture row still pending past it fails.
_Avoid_: seal deadline, timeout

**Step marker**:
The one file beside a step directory that is the only node-local capture
state: held while a capture may need the directory, sealed once its
producer has exited, released as a tombstone once the capture row is
terminal. An unreadable marker refuses every destructive path.
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
Clearing a terminal capture row's step marker on its node, then stamping
the row released. Retried until the node acknowledges; a node that is gone
is released without acknowledgement, and the row says so.
_Avoid_: release intent, unhold

**Announcement**:
The step event a watcher of an execution sees about a capture, derived from
its capture row's state and reason.
_Avoid_: notification

**Source ledger**:
The artifact daemon's view of its step markers, consulted before
destroying anything. It answers one of four ways: unmanaged (may destroy),
held, sealed, or unavailable. Only unmanaged permits destruction;
unavailable is never read as "probably fine".
_Avoid_: capture ledger

**Object marker**:
The ownership metadata written once at object creation and never updated:
which store, which digest, which marker version.
_Avoid_: label, tag

**Lifecycle**:
The record that one published generation is managed by this plane, from
publication until reclamation finishes.
_Avoid_: registration, inventory

**Claim**:
A consumer's opaque, idempotent hold on a tree ref. Hangar never interprets
or reacquires one.
_Avoid_: reference, pin

**Read lease**:
A reader's protection over one generation. It outlives the last claim and
refuses reclamation while active.
_Avoid_: lease (alone)

**Reclamation**:
Deleting a published generation nobody claims, leases or is about to
publish: admission is the decision, delete the act, finalization the
record. Only the web reclaims.
_Avoid_: garbage collection, purge

**Orphan sweep**:
The web's periodic listing of the output namespace that deletes, by exact
generation, an old object marked for this store with no lifecycle and no
capture about to register it, and counts every other object it cannot
account for.
_Avoid_: inventory, adoption, garbage collection

**In service**:
Whether the output plane admits new work: one row the web writes from its
configuration and every admission reads. Out of service, nothing new is
admitted and what is in flight finishes.
_Avoid_: activation epoch, activation, enabled (alone)

**Residue**:
What a drain still waits on: pending or publishing capture rows, open
claims, live read leases, unfinished reclaim jobs. A drain is finished when
the plane is out of service and every count is zero at once.
_Avoid_: debt, backlog

**Integrity finding**:
The durable record of an unexpected object absence or runtime
authorization failure. An open finding blocks new admission until an
operator resolves it.
_Avoid_: at risk, policy violation

**Principal**:
The storage identity a process holds: the node daemon publishes; the web
lists and deletes; strict inputs have their own. Delete exists only in the
web.
_Avoid_: role, persona

**Control-key generation**:
The number a node's control keys and the capabilities signed with them are
minted under. Rotating keys raises it; it gates nothing else and the output
scope does not derive from it.
_Avoid_: activation epoch, epoch (alone)

**Execution control**:
The base protocol (classify, observe finish, request a source-preserving
stop, may cleanup) that capture extends. A read of the node's stored,
signed start sits beside it for a control plane that never retained one;
it admits and signs nothing.
_Avoid_: lease control

### Deployment

**Disk store**:
The persistent-volume store one cluster runs for strict inputs and the
output plane, each in a namespace of its own. Initialized exactly once; its
store ID is its identity.
_Avoid_: local store, PVC store

**Cache namespace**:
The bucket or disk namespace the runtime's fail-open durable tier uses. It
is one of three namespaces -- cache, input, output -- and no two may be
the same; a process refuses to start otherwise.
_Avoid_: shared bucket

**Bootstrap inventory**:
The one declared list of Secrets a Hangar deployment needs: each entry's
name, material, the purpose of each key and its consumers. The bootstrap
creates an absent entry and never replaces, rotates or deletes one.
_Avoid_: inventory (alone)
