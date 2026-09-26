# JetBridge runtime: relationships and invariants

Vocabulary: [`atc/worker/jetbridge/CONTEXT.md`](../../atc/worker/jetbridge/CONTEXT.md).

## Shape

```
Web node ──registers──▶ Synthetic worker (one per namespace)
Web node ──creates────▶ Step pod ──mounts──▶ Artifact daemon (one per node)
Step pod: [control init] [cleanup init] [fetch init] ──▶ step container + sidecars
```

- The **synthetic worker** is one record per namespace, named after it,
  heartbeated every thirty seconds by writing to the store directly. It
  reports how many pods carry its label. Placement is Kubernetes's; the web
  expresses only affinity for the node holding a step's inputs.
- A **step pod** runs one step. Its init containers run in order: control
  init (only when a capture is selected), cleanup, fetch. Sidecars share its
  network namespace.

## Container states

The store's container record moves `creating → created → destroying`, with
`failed` reachable from creating. Pod phase is a separate fact the runtime
maps onto that record: a running pod is reused, a terminal one replaced, a
looked-up one never recreated.

## Pause pod and replacement

A pause pod is created first and the step's command is exec'd into it; the
supervisor makes the command survive a web restart. The command runs detached
in a session of its own, so the hangup of a dead web's exec session reaches
neither it nor anything it starts, and a new web re-execs the same supervisor
to take it over or read its recorded exit
([ADR-0006](../adr/0006-supervised-commands-leave-the-exec-session.md)). Under
exact execution
control the supervisor, and for a check, get or put the resource session,
keeps an exit journal the Run's signed start names, so a lost web can be
recovered from and a Run can interrupt the command. The journal's start is
claimed atomically (an `O_EXCL` create), so of a delivery and a late second
one -- or a delivery and the closing of an undelivered start -- exactly one
owns it and the producer runs at most once. A start that no delivery claimed
is closed in the Pod as stopped by recovery or cancellation; nothing sends
the command again. Replacement of a dead pause pod happens at most once, and
never when:

- the command has already started (the step, not the pod phase, decides);
- a capture holds the pod's source incarnation;
- the pod's own init container failed (kept for diagnosis);
- the one replacement was already spent, even by a failed attempt.

## Artifact lifecycle

```
step output ──register──▶ artifact key ──mirror──▶ peers
                             │
                             ├── read refreshes age
                             └── sweeper deletes after TTL (steps only)
resource cache ──register──▶ content key ──▶ durable tier (fail-open) ──warm──▶ any node
```

- The daemon's registry is the node's source of truth. Keys from scanning
  the host path are recoverable and not persisted; aliases the web registers
  are persisted.
- Registering, remapping or replacing a key over a held source is refused
  after asking the source ledger. An unreadable ledger refuses too.
- Mirroring fans out after local data settles, to a bounded number of
  peers. Zero disables it; a negative count means every peer.
- The sweeper deletes step outputs older than the TTL and never touches
  resource caches. The durable timeout must be shorter than the TTL.
- The maintenance sweep is the only thing that deletes from the durable
  tier, by retention class age.
- Warms are tried against the top-ranked owners in order so concurrent
  builds converge and a dead first choice does not block. Any warm that
  registers nothing is suppressed briefly, miss or failure alike.

## Task cache identity

A task cache lives on the node and survives the pod only when the step
carries a complete identity: a job, or a template plus run job name plus
team. Exactly one of those; anything else is per-pod.

## Exact execution

Executions whose outcome matters are recorded before they are believed:
admitted, started, outcome. The classification vocabulary belongs to
Hangar's execution control; the runtime applies it. An unresolved outcome
is not a failure and not terminal; a lost one is both. A stall of the
ledger or the Run's witness is unresolved, never the step's own timeout.

A Run retains the node's signed start before any outcome is recorded. When
it could not -- its database was down, or cancellation closed admission
first -- Run cancellation reads the start from the node that signed it,
retains it, and interrupts and closes the execution from the Pod's journal.
Aborting a Run build is scoped to that build and never cancels its Run. An
aborted build that cannot finish over an open execution or an unsettled
output handoff records a build closure. The cancellation worker converges it
with Run cancellation's own operations, lease and node protocol, restricted
to that build's handoffs, holds, captures and executions: classify first, ask
for a source-preserving stop of an executing producer, release a hold or
settle a capture only on the node's exact finish or stop evidence, and finish
the build aborted once its execution is closed. It never touches another
build, scheduler debt, candidates or terminal publication. The closure closes
with its last operation; until then the build stays unfinished, the Run keeps
running, and neither terminal publication may proceed. A node that cannot
answer leaves the closure open. Run cancellation accepted meanwhile takes
over without repeating what the closure completed.
