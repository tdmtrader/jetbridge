# JetBridge runtime

The Kubernetes execution plane that replaces Concourse's Garden and
BaggageClaim workers: the web node talks to the Kubernetes API directly and
every step becomes a pod. It includes the per-node artifact daemon that holds
step outputs and resource caches. Durable tree storage is not this
context; it is [Hangar](../../../hangar/CONTEXT.md).

## Language

### Execution

**JetBridge**:
The fork and its Kubernetes runtime. Used for the whole product and, when
qualified, for this plane.
_Avoid_: K8s runtime, K8s backend

**Synthetic worker**:
The one worker record per namespace that the web node registers and
heartbeats, standing in for every node of the cluster. Named after the
namespace, not the web node.
_Avoid_: K8s worker, K8s-backed worker

**Registrar**:
The component that writes and heartbeats the synthetic worker.

**Step pod**:
The pod that runs one step's container, its init containers and its
sidecars.
_Avoid_: task pod (a step pod may be running a check or a get)

**Pause pod**:
A step pod started with a trap-and-sleep command so the web can exec the real
command into it, and `fly intercept` can exec a shell later.

**Pause-pod replacement**:
Replacing a pause pod at most once if it dies before the step's command
started. Never after the command has begun, never when a capture holds the
source, never when the pod's own init container failed. A failed
replacement still spends the one attempt.

**Supervisor**:
The in-pod shell wrapper a command runs under so a web restart resumes the
run and replays its log instead of starting over.
_Avoid_: task supervisor, supervisor script

**Exit journal**:
The start and exit records an exact execution's in-pod wrapper writes: the
supervisor's for a task, the resource session's for a check, get or put. A
resource session also journals the command's stdout and stderr, so a closed
exec stream never reaches the command and a recovered outcome keeps the
resource's answer. The signed start names where it is, so cancellation can interrupt and recover the
command after the web is gone. Its start is claimed with one atomic creation,
so of any racing claimants exactly one owns it. Only in-pod scripts write it;
recovery reads it and never runs the command again.
_Avoid_: outcome file

**Undelivered start**:
A signed start whose command never claimed its exit journal: the exec dial
failed, or the node's answer was lost and nothing was sent. Whoever finds one
closes it in the Pod by claiming the start and journaling the stopped exit
(143 for a task, 130 for a resource command). A delivery that claimed first
is left alone; one that arrives after never runs its producer.

**Stopped delivery**:
The one exec sent in place of the wrapper when the stop must precede the
command and may not be a separate request: it writes the stop, closes the
start and reports the journal's exit, and never names the producer. Used when
the Run could not retain the start.

**Exact execution**:
An execution whose lifecycle (admitted, started, outcome) is durably
recorded before any in-memory result is believed. The classification
vocabulary is Hangar's execution control; the runtime applies it.

**Unresolved outcome**:
An exact execution whose producer ran but whose fate nobody can prove yet.
Deliberately not a failure and not terminal.

**Lost outcome**:
An exact execution whose fate can no longer be proven. Terminal, and a
failure.

**Control init**:
The first init container of a capture-selected step pod. It takes the source
hold before any writer runs and is the only container holding a warrant.
_Avoid_: capture control init, hold init container

**Reaper**:
The periodic sweep that reports live pods, deletes pods marked destroying or
left behind by builds that are no longer running, cleans cache volumes, and
asks the artifact daemon to drop their artifacts.
_Avoid_: GC sweep loop, k8s worker reaper (upstream's build reaper is a
different thing)

**Sidecar**:
A service container declared on a task step and run in the step pod's
network namespace. See core for its config shape.

**Live tier**:
Tests that run against the deployed cluster under a namespaced service
account.
_Avoid_: live suite

**Scope guard**:
The static test that fails any live-tier test reaching a cluster-scoped
Kubernetes accessor.

### Artifacts and caches

**Artifact daemon**:
The per-node DaemonSet pod that stores step outputs and resource caches on a
host path, mirrors them to peers and serves them to init containers. The
authoritative artifact store. It is its own binary; the signed capability
it accepts from init containers is a shared value type, owned by neither
side.
_Avoid_: daemon (alone), DaemonSet (the deployment shape, not the thing),
artifact-cache

**Artifact key**:
The name an artifact is registered under in a daemon: a step handle or a
resource cache key.

**Fetch init container**:
The init container that asks the daemon where a step's inputs live and
stages them into the step's volume.
_Avoid_: artifact-init, artifact-fetch

**Cleanup init container**:
The init container that clears a reused handle's stale host-path data,
after asking the source ledger whether it may.
_Avoid_: cleanup-stale

**Helper image**:
The image the artifact init containers run.

**Stream in / stream out**:
The two halves of moving one volume's tarball into a node's store and out
of it, node to node.

**Mirror**:
Replicating a streamed-in artifact to a deterministic subset of peer
daemons.

**Peer**:
Another node's artifact daemon.

**Sweeper**:
The daemon's TTL reaper of node-local step outputs. Resource caches are
never swept from the node.

**Maintenance sweep**:
The daemon's pass over the durable tier that deletes objects past their
retention class's age.
_Avoid_: sweep (alone)

**Containment**:
The rule that extraction writes and symlink targets stay inside the daemon's
storage root. A violation is refused, and the archive is answerable for it.

**Refusal**:
A daemon turning a caller away for a reason the caller owns (a contained
path, a held source) or for load. A contained path or held source is named
as such; an overloaded node is refused without saying why. Distinct from the
daemon failing.

**Resource cache key**:
The node-local alias a resource cache is registered and probed under.
Prefers the content key.

**Content key**:
The content-addressed name of a resource cache, and the name it is stored
under durably.
_Avoid_: durable key

**Retention class**:
The prefix a durable object is stored under, which names the age after
which the maintenance sweep deletes it.

**Durable tier**:
The daemon's long-term object-store home for artifacts worth keeping; today
only resource caches go there. Every path fails open: a miss, timeout or
corrupt object means "not here".

**Warm**:
Asking a daemon to pull a resource cache from the durable tier and register
it node-locally.
_Avoid_: durable restore

**Warm owner**:
The daemons a key ranks highest on, tried in order, so concurrent builds
converge on one warm and a dead first choice does not block.

**Warm negative cache**:
The short suppression of repeated warms for a key whose last warm did not
register anything, whether it failed or found nothing.

**Task cache identity**:
What a step must carry for its task caches to live on the node and survive
the pod. Without it caches are per-pod.

**Cache store**:
The backend for task caches: node-local host path, per-pod empty directory,
or detected from the cluster when unset.

**Artifact locator**:
The web's in-memory map from artifact key to the node holding it, used for
scheduling affinity. Lost on restart by design; the daemon is the
authority.

**Node IP resolver**:
The cache of node name to internal IP.

### brine

brine is the behavioral test runner for this plane. Its nouns are test
vocabulary, listed here because the team uses them daily.

**Behavioral contract**:
The suite of feature files describing what JetBridge must do, as distinct
from Go unit tests.

**Feature**:
One feature file describing one behaviour family.

**Scenario**:
One named chain of steps inside a feature; the unit tags and counts attach
to.

**Step** (brine):
One registered sentence in a scenario. Every step is a transition from one
named domain state to the next.
_Avoid_: phrase, definition (those are the registry's words for the pattern
and its body)

**Domain state**:
The typed value carried between brine steps, one type per set of reachable
assertions.

**Draft**:
A domain state describing a thing not yet run, which refinement steps
extend in any order.

**Chain walk**:
Static verification that every scenario's state path resolves, without
running anything.

**Pending feature**:
A feature held to every guard except execution because its production code
does not exist yet.

**Adapter**:
The binary hosting this repo's step registry that speaks the brine protocol.

**Execution document**:
The single artifact brine hands the adapter, holding the parsed features and
one directive per scenario.
