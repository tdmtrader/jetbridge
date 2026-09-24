# Hangar: relationships and invariants

Vocabulary: [`hangar/CONTEXT.md`](../../hangar/CONTEXT.md).

## Two halves

```
Strict input:  task ──materialization warrant──▶ daemon ──materializes──▶ tree ref
Output plane:  task output ──capture──▶ handoff ──publish──▶ tree ref ──claim──▶ consumer
```

Hangar knows nothing about what a tree is for. Its packages never name a
run, workflow, ticket, agent, anvil or playbook; the base execution
protocol also never names a build, job, check or capture; the coordinator
never names a build, job or check. A test scans exported fields, wire
spellings and imports for those words.

## Tree refs

A tree ref is scope, digest and generation, all three. Scope is filled in by
the control plane and never accepted from a caller. Hangar never
substitutes a newer generation or different content. A strict input names a
complete tree ref and fails closed on absence, corruption, conflict,
authorization, limits or infrastructure.

## Capture handoff

```
unresolved ──▶ resolved ──▶ registered
     │                          │
     ├──▶ cancelled             └──▶ (release source) ──▶ done
     └──▶ failed
```

Every next step is decided from durable facts alone; the coordinator holds
no lock across the network and no memory between calls.

- A handoff starts with a **predeclaration**: id, source hold, execution,
  activation epoch, output, capture deadline.
- Before anything writes, the **source placement** (locator, incarnation,
  directory) and the **hold acknowledgement** are recorded.
- The **disposition** is arbitrated by one row and is permanently one of
  capture, no capture, or cancelled before reservation. An unwon arbiter is
  an absence, not a fourth value.
- A capture disposition records a **reservation** and a **producer
  checkpoint** at the producer's completion, before any object exists, so
  recovery can correlate an object that may exist.
- Past the **irreversible publish point** cancellation is unavailable; an
  unresolved state there is corruption.
- A no-capture disposition releases the source in two crash-recoverable
  halves: recorded **release intent**, then daemon acknowledgement. Until
  both, the source stays held and destructive cleanup is forbidden.
- A tree ref is durable only once a verified **publication receipt** is
  registered.

## Source incarnation, hold and ledger

A source incarnation is four facts and no path: execution, node, handle
generation, output. A recreated pod is a new incarnation and may not
write. The **source hold** is provisional and non-authorizing: it prevents
cleanup, replacement and reuse while a capture is pending. The **seal**
fences and drains every writer so the published tree is exactly what the
producer left.

The **source ledger** answers every destructive request one of four ways:
unmanaged (may destroy), held, sealed, unavailable. Only unmanaged permits
destruction. An unknown writer state is held; an unreadable ledger is
unavailable; both refuse.

## Activation

An **activation epoch** is the single authority on whether the plane is in
service. It has two **facets**, base and output, each moving through
initial, attesting, attested, enabled, draining, disabled. Disabled is
terminal; rotation creates a new epoch. Output may leave initial only once
base is attested. Every transition compares a revision and refuses a stale
one. Draining goes output first, then base. Unresolved runtime integrity
findings (unexpected absence or authorization failure) block admission. IAM
and lifecycle configuration belong to the operator, not an attestation loop.

## Claims, leases and reclamation

```
Consumer ──claim──▶ tree ref ◀──read lease── reader
                       │
      reclaim admission ──▶ reclaim delete ──▶ reclaim finalization
```

- A **claim** is opaque and idempotent; Hangar never interprets one.
- A **read lease** is the reader's protection over one generation. It
  outlives the last claim and refuses reclaim admission while active. It
  closes by database-clock expiry, swept in bounded batches; a lease renewed
  between candidate read and write is not closed. The daemon gives it back
  when a read ends, except after an unavailable answer, which the reader
  retries under the same warrant: that lease stays until its term ends.
- **Reclamation** is admission (the decision) then delete (the act) then
  finalization (the record), each its own operation kind.

## Operation kinds and leases

Eight kinds: capture recovery, no-capture release, inventory, adoption,
reclaim admission, reclaim delete, reclaim finalization, read-lease
cleanup. Only three contend and hold an **operation
lease**: inventory, reclaim admission, reclaim delete.
The rest run unleased for stated reasons: recovery and release are the web
node's own pass; adoption runs inside inventory's lease; finalization
records an object already gone; read-lease cleanup is clock-driven. Only
reclaim delete is woken by notification.

**Inventory** sweeps the bucket and gives every readable object a
committed disposition. **Adoption** takes ownership of an object carrying
this epoch's marker with no lifecycle row; an unmarked object or a foreign
epoch's object is never adopted and becomes **debt**, which also records
any stretch a pass did not reach. Debt keeps one bad object from starving
the keys after it.

## Roles

Three output storage roles, each its own binary and credentials:
publisher, inventory, reclaimer. Strict inputs use a separate storage identity. Delete exists only on the
reclaimer and only against a tree ref with a precondition. No interface
accepts a storage location; scope is the only namespace a caller can name.

## Storage backends

GCS and disk implement explicit immutable object operations. Tree verification,
publication, inventory and reclamation share the same algorithms. Disk uses a
single PVC owner, bbolt generation/index transactions and synced immutable
blobs. Fixed namespace-scoped credentials over verified TLS separate storage
roles. Only the reclaimer client exposes exact deletion; only the storage
service links the local index. See [ADR-0005](../adr/0005-hangar-storage-and-operator-responsibility.md).
