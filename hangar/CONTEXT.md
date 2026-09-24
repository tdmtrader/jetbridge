# Hangar

The product-neutral durable result plane: immutable filesystem trees
published under tree refs, and the protocol by which a task's output
becomes one. Hangar does not know what a tree is for. Its packages, and the
control-plane coordinator in `atc/hangaroutput`, must not use the words run,
workflow, ticket, agent, anvil or playbook; the coordinator must also not say
build, job or check. A test enforces this.

## Language

### Exact trees

**Hangar**:
The opt-in, bucket-backed tier for immutable tree inputs, addressed by exact
reference and failing closed.
_Avoid_: exact tree storage, durable storage (that is the runtime's
fail-open resource-cache tier)

**Tree**:
A canonical filesystem tree: a bytewise POSIX namespace with deterministic
archive bytes.

**Tree ref**:
The complete reference to one tree: scope, digest and generation.
Hangar never substitutes a newer generation or different content.
_Avoid_: exact ref, exact reference, exact tree

**Scope**:
The opaque namespace component of a tree ref, filled in by the control
plane and never accepted from a caller.

**Digest**:
The logical-content digest of a canonical tree.

**Generation**:
The object-store generation pinned in a tree ref.

**Canonicalization**:
Turning an archive stream into a tree under the entry and content limits.

**Strict input**:
A task input naming a complete tree ref. It fails closed on absence,
corruption, conflict, authorization, limits or infrastructure failure.
_Avoid_: exact input, immutable input, Hangar input

**Materialization**:
The daemon opening, capturing and verifying a tree into its scratch space
for one task.

**Materialization receipt**:
The local, read-only record that a materialization completed for one exact
tree ref, checked before the task starts.
_Avoid_: receipt (alone)

**Warrant**:
A short-lived, signed, attenuated authorization for exactly one target,
issued by the web and verified by the daemon. Always qualified by what it
authorizes.
_Avoid_: grant (that is the agentic context's word), capability, token

**Materialization warrant**:
The warrant bound to one tree ref, handle, volume and expiry that lets the
daemon materialize that tree for that task.

**Read warrant**:
The warrant bound to one read lease that lets a reader open a generation
under that lease.

**Warrant key**:
The secret the web signs one kind of warrant with and the daemon verifies
against. One key per warrant kind. Never present in a task pod.
_Avoid_: capability key

**Scratch path**:
The daemon's private transient space where canonicalization and
verification happen.

### Output plane

**Output plane**:
The half of Hangar that turns a task's declared output into durable,
claimable content.
_Avoid_: durable output publication

**Capture**:
Selecting one declared output of one finished task for publication. A
captured task gives up post-completion hijack.

**Handoff**:
One capture's unit of work across the two systems that own it, advanced one
transition at a time.

**Transition**:
What a coordinator may do next, chosen from durable facts alone: one bounded
step, wait for an outcome, or nothing.

**Coordinator**:
The control-plane half that performs the one transition just decided,
holding no lock across the network and no memory between calls.

**Disposition**:
The permanently exclusive branch a handoff took: capture, no capture, or
cancelled before reservation.

**Source incarnation**:
The identity of the directory a capture protects: which execution, on which
node, which generation of the handle, which output. Four facts and no path;
the daemon derives the location. A recreated pod is a new incarnation and
may not write.

**Source hold**:
The provisional, non-authorizing hold on a source incarnation that prevents
cleanup, replacement and reuse while a capture is pending.
_Avoid_: source lease, hold (alone)

**Writer ticket**:
One admitted write capability over a source incarnation, issued and closed
entirely inside Hangar. Not a product ticket.

**Seal**:
Fencing and draining every writer over a source incarnation so the
published tree is exactly what the producer left.

**Seal deadline**:
The database-clock bound on completing a seal.

**Reservation**:
The record made at the producer's completion checkpoint, before any object
is created, so recovery can correlate an object that may exist.

**Producer checkpoint**:
The idempotency identity for a handoff's publish stage, derived from durable
facts rather than minted.

**Publication receipt**:
The signed, challenge-bound proof the daemon returns for one capture. A
tree ref is not durable until a verified publication receipt is registered.
_Avoid_: receipt (alone)

**Receipt key ring**:
The control plane's versioned verification material for publication
receipts. Rotation adds an epoch rather than replacing a key; a retired key
stays on the ring and verifies nothing.

**Release intent**:
The caller's recorded half of releasing a source incarnation. The daemon's
acknowledgement is the other half.

**Announcement**:
The only thing a watcher of an execution sees about a capture: its kind,
disposition and reason.

**Activation epoch**:
The single durable authority on whether the output plane is in service.
Not a node label, handshake or chart value.
_Avoid_: epoch (alone)

**Facet**:
One of the two activation state machines on an epoch: base and output. Each
moves through initial, attesting, attested, enabled, draining and disabled.
Output may leave initial only once base is attested.

**At risk**:
The durable integrity finding recorded after unexpected object absence or
runtime authorization failure. It is not an activation state.

**Claim**:
A consumer's opaque, idempotent hold on a tree ref. Hangar never interprets
or reacquires one.

**Read lease**:
A reader's fenced protection over one generation. It outlives the last
claim and refuses reclamation while active.

**Operation lease**:
The fenced, database-clock lease a controller must hold for the operation
kinds that contend: inventory, reclaim admission and reclaim delete. The other kinds run without one.

**Operation kind**:
One of the eight durable units of background work: capture recovery,
no-capture release, inventory, adoption, reclaim admission, reclaim delete,
reclaim finalization and read-lease cleanup.

**Pass**:
One bounded execution of a controller's work for one operation kind.

**Inventory**:
The resumable sweep over the bucket that gives every object it can read a
committed disposition and records debt for every object it cannot.

**Inventory debt**:
The typed record of what inventory owes: an object it could not classify
(unmanaged, unreadable, foreign) or a stretch it did not reach before budget
or failure. Debt keeps one bad object from starving the keys after it.

**Adoption**:
Taking ownership of an object inventory found that carries this epoch's
marker but has no lifecycle row. An unmarked object is never adopted; it is
debt. A foreign epoch's object is never adopted either.

**Object marker**:
The ownership metadata written once at object creation and never updated.

**Reclamation**:
Deleting a published object. Admission is the decision; delete is the act;
each has its own lease.

**Role**:
One storage-permission persona with its own binary and credentials:
publisher, inventory or reclaimer. Strict inputs have a separate identity.

**Source ledger**:
The output daemon's durable record of source paths, which the artifact
daemon consults before destroying anything. It answers one of four ways:
unmanaged (may destroy), held, sealed, or unavailable. Only unmanaged
permits destruction; unavailable is never read as "probably fine".
_Avoid_: capture ledger

**Execution control**:
The product-neutral protocol (classify, observe finish, request a
source-preserving stop, may cleanup) that capture extends. A read of the
node's stored, signed start sits beside it for a control plane that never
retained one; it admits and signs nothing.
