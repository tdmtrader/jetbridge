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

**Step pod grant**:
An operator-set mapping from a build's authoritative owner (an exact
team/pipeline/job of a non-instanced pipeline, or a one-off build of team
`main`) to the step pod identity its pods run under. A step pod grant names a
ServiceAccount, declares `privileged=true`, or both; the step pod identity
carries that declaration, and the privilege gate reads it under the
`granted` privilege mode only (see **Privilege gate**).
Only web's configuration sets one; check builds, instanced pipelines and other
teams' builds match none, and nothing a pipeline or API caller supplies can
select one.
_Avoid_: grant (alone; that is the agentic context's MCP authorization),
service account mapping

**Trusted reader**:
An identity allowed to read a bootstrap inventory Secret, create a pod that
mounts one, or reach the node: the bootstrap inventory's consumers, web, the
cluster administrators the operator lists, and every identity a step pod grant
gives privilege or `pods create`. No pipeline can join it through a
ServiceAccount, a capability or a host namespace. Privilege is gated only
under the `granted` privilege mode (see **Privilege gate**); under `any`, the
default, a task that sets `privileged:`, or a step whose resource type does,
runs privileged without a step pod grant.

**Privilege gate**:
The rule that a step pod holds privilege only where the operator allowed it.
No step configuration can add a capability or join a host namespace: nothing
that builds a step pod sets one, and
`TestNoStepPodCodeSetsACapabilityOrAHostNamespace` keeps it so. A step pod's
security context follows the task's `privileged:` or, for a get, put or
check, its resource type's `privileged:`, and whether that is admitted is the
privilege mode's. Under the `granted` privilege mode the step pod admission
refuses, at creation, a pod that is privileged, adds a capability or joins a
host namespace in any container or init container unless its step pod
identity's grant declares `privileged=true`; the refusal names the owner and
the grant it lacks, and no Pod or Job is made. A check holds no step pod grant,
so a privileged check is refused in that mode. Sidecars are never privileged.
Reusing a live pod and resuming a step Job after a web restart do not
re-admit privilege yet: a pod created before the mode changed keeps running.

**Privilege mode**:
The operator's choice of what the privilege gate enforces, one value per web
(`--kubernetes-step-privilege`, `kubernetes.stepPrivilege`): `any`, the
default, admits privilege as the task or resource type asks, whatever step pod
grants declare, so a deployment that already set `privileged=true` on a grant
sees no change on upgrade; `granted` admits it only under a step pod grant
that declares `privileged=true`. The mode is set explicitly, never inferred
from a grant. `granted` with no privileged grant is legal: nothing runs
privileged.
_Avoid_: privilege flag, strict mode

**Default step pod identity**:
What a step pod outside every step pod grant runs as: the configured
ServiceAccount, with no Kubernetes permissions and no API token mounted. A
privilege-only grant (one that names no ServiceAccount) keeps this account and
its token-less state; only a grant's own ServiceAccount carries a token.

**Step runtime**:
The operator's choice of how a step's command reaches its pod:
`exec`, a pause pod the web execs into, or `job`, a step Job whose pod runs
the command from its own spec under the launcher. One value per web
(`--kubernetes-step-runtime`); a Run-owned exact execution runs on `exec`
in either.
_Avoid_: exec mode, job mode (as nouns), runtime flag

**Step Job**:
Under the `job` step runtime, the suspended `batch/v1` Job that is one step
execution: its pod template carries the command, so admission control sees
it; unsuspending it starts the step; its pod's terminated status is the
exit. Its controller never retries (`backoffLimit: 0`, a disrupted pod fails
the Job), the web deletes it on every path on which the command's exit was
not read, and a step pod it owns is only ever deleted through it.
_Avoid_: task Job, step pod job

**Launcher**:
The static binary a step Job's pod runs as its main container's entrypoint:
it fetches the step envelope, runs the command in its own process group,
forwards the pod's termination signal to that group, writes a resource
command's answer to the answer directory and exits with the command's code.
The launcher init copies it from the jetbridge image into an emptyDir.
_Avoid_: entrypoint, emissary, wrapper, supervisor (that is the exec
runtime's in-pod script)

**Step envelope**:
What a step Job's launcher fetches from the web before it runs the command:
the bytes the command reads on stdin, a resource command's request with its
interpolated source and params. It lives in the web's memory and the pod's,
never in a Kubernetes object; the pod presents a projected ServiceAccount
token bound to itself, with an audience the API server does not accept, and
the web verifies it with a TokenReview and hands each envelope to one pod.
_Avoid_: credential bundle, stdin secret

**Answer directory**:
Under the `job` step runtime, the directory on the node's artifact host
path, `steps/<handle>/answer`, where the launcher writes a check, get or
put's stdout, and the web reads it back through the artifact daemon. The
container log is the command's stderr.
_Avoid_: result file, stdout file

**Pause pod**:
The `exec` step runtime's step pod: started with a trap-and-sleep command so
the web can exec the real command into it, and `fly intercept` can exec a
shell later. A capture-selected
task's pause pod is deleted gracefully once the node acknowledges its outcome,
so the capture's seal sees it terminate; its result is then recovered from the
node's ledger.

**Pause-pod replacement**:
Replacing a pause pod at most once if it dies before the step's command
started. Never after the command has begun, never when a capture holds the
source, never when the pod's own init container failed. A failed
replacement still spends the one attempt. An exact execution's start names
the replacement, not the Pod admission saw, and a replacement on a node other
than the admitted one is refused before the start.

**Relocation**:
Moving an exact execution to another node when its pause pod was never bound
to the node it was admitted on: Unschedulable past a short grace, nothing
exec'd. The pod is deleted with a precondition on the unbound observation --
the proof it never ran -- and the Run's admission, with a capturing
execution's pending capture row, is re-pointed at a node chosen without the
ones already tried; the identity is kept. A start or closure retained under
the identity forbids it, in the database as well as here. A refused move puts
the pod back and spends the step's relocations. The old node keeps a
never-started execution record that nothing starts, because every start is
checked against the admission's node.
_Avoid_: rescheduling, pod migration (a pod is never moved; it is replaced)

**Supervisor**:
The in-pod shell wrapper a command runs under so a web restart resumes the
run and replays its log instead of starting over. The command runs in a
session of its own, so the exec session's hangup never reaches it or its
children (ADR-0006); only pod deletion or an exact stop record ends it early.
_Avoid_: task supervisor, supervisor script

**Exit journal**:
The start and exit records an exact execution's in-pod wrapper writes: the
supervisor's for a task, the resource session's for a check, get or put. A
resource session also journals the command's stdout and stderr, so a closed
exec stream never reaches the command and a recovered outcome keeps the
resource's answer. The node's start names where it is, so cancellation can interrupt and recover the
command after the web is gone. Its start is claimed with one atomic creation,
so of any racing claimants exactly one owns it. Only in-pod scripts write it;
recovery reads it and never runs the command again.
_Avoid_: outcome file

**Undelivered start**:
A node-acknowledged start whose command never claimed its exit journal: the exec dial
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
hold before any writer runs and holds the step's control warrant.
_Avoid_: capture control init, hold init container

**Managed-input init**:
The init container, one per tree input, that presents a read warrant to the
node's artifact daemon, which materializes that tree ref into the step's
volume (Hangar's managed read), and checks the materialization receipt
before the step starts. The only other container holding a warrant.
_Avoid_: strict init, materialize init

**Reaper**:
The periodic sweep that reports live pods, deletes pods marked destroying or
left behind by builds that are no longer running (and, gracefully, finished
capture-selected pause pods whose build still runs), and asks the artifact
daemon to drop their step directories. It never drops one a resource cache
resolves into; that is left to the sweeper.
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
authoritative artifact store, and the only daemon on a node: Hangar's
output plane, which serves input publications, managed reads and captures,
is a part of it. It is its own
binary; the signed capability it accepts from init containers is a shared
value type, owned by neither side.
_Avoid_: daemon (alone), DaemonSet (the deployment shape, not the thing),
artifact-cache

**Artifact key**:
The name an artifact is registered under in an artifact daemon: a volume
handle the web registers as an alias (`<handle>-dir`, `-input-N`,
`-output-<name>`), the `<container handle>/<volume>` path the artifact daemon
finds on disk, or a resource cache key.

**Fetch init container**:
The init container that asks the daemon where a step's inputs live and
stages them into the step's volume.
_Avoid_: artifact-init, artifact-fetch

**Cleanup init container**:
The init container that clears a reused handle's stale host-path data. Where
the output plane is enabled it first asks the source ledger and clears only on
an unmanaged answer; held, or no answer at all, is a refusal.
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
The daemon's TTL reaper of node-local step outputs, including resource-cache
aliases backed by expired step directories. A step directory the source ledger
does not answer unmanaged is never swept, and neither is one whose step pod is
still running on the node: each pass that sees the pod alive restarts its TTL.

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
The content-addressed name of a resource cache, stable across database row
replacement.
_Avoid_: durable key

**Task cache identity**:
What a step must carry for its task caches to live on the node and survive
the pod. Without it caches are per-pod.

**Cache store**:
The backend for task caches: node-local host path or per-pod empty directory.
When unset, the web picks the host path if an artifact daemon or a cache host
path is configured, and an empty directory otherwise. A step without a task
cache identity always gets the empty directory.

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

**Adapter**:
The binary hosting this repo's step registry that speaks the brine protocol.

**Execution document**:
The single artifact brine hands the adapter, holding the parsed features and
one directive per scenario.
