# Hangar exact tree storage

Hangar is an opt-in storage path for immutable filesystem-tree task
inputs. It publishes canonical trees under tree refs containing an
opaque scope, a SHA-256 logical-content digest, and an immutable object generation. Task
inputs name that complete reference; Hangar never substitutes a newer
generation or different content.

Hangar supports native Google Cloud Storage and a dedicated persistent-disk
service. Both implement the same immutable, exact-generation contract. The
artifact daemon's fail-open resource cache uses the same object interface and
the same two backends, but always against its own bucket or disk namespace:
the cache, input and output namespaces are three different places, and the
daemon and web refuse to start when any two coincide. A GCS emulator is useful
for tests; the disk service is the supported deployment option when no GCS is
available.

## Enablement

Hangar requires the artifact DaemonSet, artifact-daemon TLS, a configured GCS bucket or disk
namespace, positive content and entry limits, a whole-second
warrant TTL from `1s` through `900s`, and a private absolute scratch path
disjoint from the artifact hostPath:

```yaml
artifactDaemon:
  tls:
    source: existingSecret
    existingSecret: concourse-artifact-daemon-tls
  hangar:
    enabled: true
    webEnabled: false
    store: gcs
    bucket: concourse-hangar
    prefix: production
    allowGeneratedKey: false
    scratchPath: /var/concourse/hangar-scratch
    maxContentBytes: 10737418240
    maxEntries: 100000
    capabilityTTL: 900s
```

Hangar names its own storage. With Hangar enabled, `artifactDaemon.hangar.store`
(`gcs` or `disk`) and `hangar.bucket` are required, and the chart refuses a
render without them; `hangar.prefix` is optional, and `hangar.endpoint`
overrides the GCS endpoint for an emulator. Hangar shares nothing with the
resource-cache store. Its per-operation timeout is the daemon binary's fixed
5m default. On GKE, grant the artifact-daemon ServiceAccount bucket access with
Workload Identity. There is no separate Hangar cloud credential block and task
Pods never receive bucket credentials.

The chart creates a private, transient `emptyDir` mounted only in the daemon at
`scratchPath`. It is used for complete canonicalization and verification, not
durable storage. Size its ephemeral-storage request/limit and node capacity for
the largest admitted canonical tree plus compressed/object-transfer staging.
Lower `maxContentBytes` and `maxEntries` to fit the node budget. The scratch
path must not equal, contain, or sit beneath `artifactDaemon.hostPath`.

## The Hangar key

One raw 32-byte key, `hangar.key`, signs every warrant -- materialization,
read and control -- and every artifact daemon verifies against it. Name a
Secret holding it in `artifactDaemon.hangar.keySecret`; the web reads it
through `--kubernetes-hangar-key` and the daemon through `--hangar-key`.
Without `keySecret`, with chart-managed artifact-daemon TLS, the same Secret
also contains `hangar.key`. That mode requires the explicit
`hangar.allowGeneratedKey: true` opt-in and is supported only for live Helm
install/upgrade, where `lookup` can read and preserve the existing Secret.
Helm generates 32 cryptographically random bytes only when the key is absent;
it does not reuse a TLS private key.

Offline renderers and GitOps controllers must not use generated keys. `lookup`
has no reliable live Secret in those modes, so both `hangar.key` and the
chart-generated TLS CA/certificates would change between renders. Leave
`allowGeneratedKey: false` and set `artifactDaemon.tls.existingSecret` to an
operator-managed Secret containing all TLS materials and `hangar.key`.

With `artifactDaemon.tls.existingSecret`, the operator must add all normal TLS
entries plus `hangar.key`, whose decoded value must be **exactly 32 raw bytes**.
The chart mounts that selected key read-only into only the web and daemon
containers. A missing entry prevents the Pods from starting; a wrong-length
entry is rejected by both binaries at startup.

The web process signs short-lived warrants with the configured `capabilityTTL`
window, and the daemon accepts none that lives longer than 15 minutes. Chart
values use positive whole-second syntax; the default and maximum are `900s`
(15 minutes), and `1s` is the minimum. Shorter values reduce replay exposure.
Task Pod specs contain only attenuated warrants bound to one tree ref, handle,
volume, and expiry. Anyone who can read Pod specs during that window can see
those warrants, but the long-lived signing key is never placed in a task Pod
command, environment, or volume.

## Runtime and failure semantics

After validating TLS, storage access, the key, limits, and private scratch, each
daemon adds `concourse.dev/hangar-v1=ready` to its node. Strict tasks require
that label as well as the existing artifact-cache readiness label. A task init
container requests the exact tree and the task and all sidecars receive the
result as a read-only input. The local materialization receipt records the exact
scope, digest, and generation; the init verifies it before the task starts.

Hangar is fail-closed. Absence, authorization failure, immutable-write
conflict, limit rejection, corruption, and infrastructure failure remain
distinct daemon outcomes and every non-success prevents task startup. This is
deliberately different from durable resource caches, where unavailable or bad
durable data degrades to a cache miss because the bytes can be reproduced.

Materialization is bounded node-wide: a daemon runs at most four
materialization requests at once, and refuses further ones with `503` rather
than queueing them. The init container retries `503` a bounded number of times,
so ordinary bursts pass; the bound exists because the route is not
mTLS-protected, and each item is a whole tree open, capture, and verified copy
into the daemon's scratch volume. A refusal is counted in
`artifact_daemon_refusals_total` with `reason="overloaded"`: the daemon turned
the caller away, which is what that counter measures. Store and infrastructure
failures answer with the same `503` and the same body, deliberately, but are
NOT counted there and are not called refusals — they are the daemon failing,
not the client, and counting both made a bucket outage read as a wave of bad
requests. They are logged as `hangar-unavailable` with the same bounded route
label.

Node-IP TLS encrypts the init-to-daemon request, but the current init client
does not verify the daemon's server identity. Do not describe this path as
server-authenticated mTLS. The independently verified, read-only materialization receipt
is the outcome proof that the exact requested tree was committed on the node.

NetworkPolicy remains off by default. When the artifact-daemon policy is
enabled it permits both current GKE metadata profiles for Workload Identity
refresh: standard/Calico on GKE 1.21 and later uses
`169.254.169.252/32` on TCP 987 and 988, while Dataplane V2 uses
`169.254.169.254/32` on TCP 80 and 8080. The obsolete pre-1.21 loopback profile
is not included. Policy enforcement and metadata routing are
CNI/environment-dependent; non-GKE clusters may require different egress, and
operators must validate the policy against their cluster. It is defense in
depth and does not change the materialization receipt or TLS identity model.

## Rollout and downgrade

Roll out daemon support before allowing web nodes to emit strict inputs:

1. Set `hangar.enabled: true` while leaving `hangar.webEnabled: false`. Upgrade
   the DaemonSet and wait for its rollout plus
   `concourse.dev/hangar-v1=ready` on every eligible node.
2. Set `hangar.webEnabled: true` to let web nodes sign and emit strict inputs.

For downgrade, reverse the order: set `hangar.webEnabled: false`, then wait for
pending/running strict init containers and tasks to drain before setting
`hangar.enabled: false` or downgrading the DaemonSet. This avoids scheduling
new exact inputs onto nodes that cannot materialize them.

To disable without changing resource-cache behavior, set
`artifactDaemon.hangar.enabled=false` and follow the downgrade order. Existing
immutable objects remain stored. Strict-input objects have no automatic
reclaimer; output reclamation follows claims, and is the web's.

The daemon container is explicitly UID 0, non-privileged, unable to escalate,
under `RuntimeDefault`, and drops all capabilities except `DAC_OVERRIDE`. A
normal exact destination is kubelet-created and root-owned, so no `FOWNER` is
needed. A custom non-root daemon image or pre-chowned destination fails closed
rather than widening capabilities. Container-level root settings are not
applied to task or init containers.

## Durable output publication

The output plane captures selected successful outputs, registers exact tree
refs, and protects them with claims: a consumer's hold lasts until released,
a reader's expires. The node's artifact daemon publishes; the web reclaims,
through two components of its own -- `hangar_reclaim` (an unclaimed
generation is deleted and stamped reclaimed) and `hangar_orphan_sweep` --
which share one PostgreSQL advisory lock across web replicas. It remains opt-in: `hangarOutput.executionControl.enabled` renders the
daemon's `--execution-control`, which mounts the output plane (the base
execution-control protocol, and with `hangarOutput.enabled` the capture
extension), verifying every warrant with the Hangar key it already holds; and
`hangarOutput.webEnabled` puts the plane **in service**. Configure the Hangar
key and mutual TLS as described in `deploy/chart/values.yaml`. Changing the storage
selector does not bypass these gates.

### In service, and the drain

In service is one database row, `hangar_enabled`. The web writes it at
startup from `hangarOutput.webEnabled`, and every admission that needs the
output plane -- a new capture, a Run that declares results or takes inputs, an
input upload -- reads it `FOR SHARE` in its own transaction and is refused
while it says false. Nothing else is walked or attested (see
[ADR-0009](adr/0009-one-node-daemon-one-capture-row.md)): there is no
control-key generation, no node control key and nothing signed by the
node; the daemon is trusted over the mTLS channel the web reaches it on,
and an acknowledgement is its answer on that channel.

To remove the output plane (or replace its daemons):

1. Set `hangarOutput.webEnabled: false` and roll the web. New admission stops;
   captures already pending carry on to completion, and the reclaim pass keeps
   reclaiming what consumers release. Execution start is admission too: a running multi-step Run
   whose next step needs the output plane fails at that step during a drain.
   Drain when no Run you care about is mid-flight, or let them finish first.
2. Watch the residue until it reaches zero:

   ```sh
   fly -t <target> hangar-status
   ```

   It prints whether the plane is in service and counts the pending and
   publishing captures, captures whose step marker is not yet released, open
   claims (consumers' and readers' alike) and open integrity findings, and
   lists each open finding with its id. The plane is drained when
   it says `out of service, drained`. Open claims belong to consumers, and most
   are Run results: a finished Run keeps its result claims until the Run is
   reclaimed under its retention, so `open claims` may not reach zero on its
   own. Read it against the other counts: once pending, publishing and
   unreleased captures are all zero, what remains is retained results (a
   reader's claim lapses within the operation timeout plus five minutes). Removing the daemon then strands
   no capture, but those results stay unreadable until the plane returns; wait
   for retention (or reclaim the Runs) if they must stay readable.
3. Only then remove the node daemons' output plane (`hangarOutput.enabled`).

The same numbers are published continuously as
`concourse_hangar_output_enabled` and
`concourse_hangar_output_plane_inventory{kind=...}` (with `kind="residue"` the
drain's total).

### Reclamation and the orphan sweep

The reclaim pass reclaims a generation only once its publication grace has
elapsed, no live claim names it, and no pending or publishing capture -- or
unregistered input publication -- names its tree. In one transaction under
the tree lock it deletes that exact generation conditionally and stamps the
lifecycle reclaimed once the store answered: a confirmed delete, an object
already absent and another generation at the key all stamp; a refused
delete records a `runtime_principal_denied` finding instead; a delete that
did not answer is retried next pass.

The orphan sweep lists the output namespace under the deployment prefix. It
deletes an object only when its marker names **this** store, it has no
lifecycle row, nothing pending could still register it, and it is older than
twice the capture deadline -- and then only its exact listed generation. Every
other object is counted by class (`foreign`, `unmarked`, `young`, `registered`,
`protected`, ...), logged, and published as
`concourse_hangar_output_orphan_sweep_objects{class=...}`; nothing outside the
web's own store is ever deleted.

### The ordinary-task cost of a durable capture

Selecting an output for capture is not free, and the cost is intentional and
visible rather than hidden.

Once sealing starts for a capture-selected task, that task can no longer be
hijacked. Its sidecars and any active hijack sessions are terminated at the
seal boundary, and no recovered or replacement pod may remount the output for
writing — a recreated pod is a new Pod UID, and a new Pod UID gets a typed
refusal rather than a race with the capture. This is the price of the tree
being exactly what the producer left: a writer admitted after the seal would
make "exact" untrue, and there is no way to have both.

Capture applies only after the producer's main command exits *successfully*.
A failed or cancelled producer is not eligible and follows existing task
semantics unchanged. Capturing from a non-successful producer is a separate
future contract, not a flag on this one.

Ordinary tasks and unselected outputs keep their current behaviour exactly.
Resource-cache routes, the fail-open durable cache tier, and the original
`concourse.dev/hangar-v1` strict-input capability are untouched, and the output
plane deliberately does not reuse any of them.

### Storage configuration belongs to the operator

Hangar does not read, attest or manage GCS IAM and lifecycle configuration.
There is no policy-attestor process and no periodic policy evidence to refresh.
Runtime identities need their object permissions, not IAM or lifecycle read
permissions. Provision the bucket and permissions outside the chart:

| Identity | Object operations |
| --- | --- |
| Strict-input daemon | create, get in the strict-input bucket |
| Artifact daemon, resource cache (`--durable-store=gcs`) | create, get, list, delete in the cache bucket **only** |
| Output publisher/materializer (artifact daemon) | create, get in the output bucket |
| Web (reclaim pass and orphan sweep) | list, get, delete in the output bucket |

The artifact daemon's strict-input and resource-cache roles are one Pod and so
one workload identity. Grant each as a binding on its own bucket. The daemon's
delete must be granted on the cache bucket alone: never a project-wide role
such as `roles/storage.objectAdmin`, which would give the node daemon delete
over the strict-input and output buckets too. The node daemon holds no delete
on any exact tree.

Keep output objects in a dedicated bucket, separate from strict inputs and
resource caches. Do not configure lifecycle deletion, external cleanup or
retention rules that prevent Hangar's admitted deletes. GCS `get` covers both
metadata and body access; the web's reclaim code deliberately exposes only
metadata operations, but IAM cannot separate those reads. Never give the
publisher delete permission: replacing an existing GCS object requires both
create and delete. The node daemon and the web are separate workload
identities, which keeps the delete on the web alone.
These are deployment requirements, not properties that Hangar can prove by
periodically inspecting a bucket policy.

Actual unexpected absence and runtime authorization failures remain durable
integrity findings (`out_of_band_absence`, `runtime_principal_denied`). They
block new capture, claims, managed read warrants and the reclaim pass;
releases and diagnosis remain possible. Repair the cause and investigate lost content before resolving one
finding, as an admin, by the id `fly hangar-status` lists (resolving one
already resolved succeeds and changes nothing):

```sh
fly -t <target> hangar-status --resolve-finding <id>
```

Resolution does not restore bytes, change generations or make a missing
reference readable, and a resolved finding cannot be reopened. Historical
findings remain in `hangar_integrity_findings` for audit.

## Persistent disk without GCS

The disk backend uses one `hangar-store` process owning one PVC. Immutable blob
files and a bbolt index live together under `/data/store`; the index assigns
monotonic generations, persists creation metadata and journals exact deletes.
Uploads are synced before their index transaction commits. Restart removes
uncommitted uploads and completes pending deletes. Missing or corrupt committed
content fails closed. A second owner is refused by the index lock.

This is a single-owner local-filesystem service. Use a block-backed CSI volume
with working file locks, atomic filesystem operations, fsync and `fsGroup`
support. NFS, shared multi-writer storage, replicas and automatic failover are
not supported. The Deployment uses one replica and `Recreate`; replacement
may interrupt storage until the volume reattaches. `ReadWriteOncePod` is an
option where the CSI driver supports it.

Provision two Secrets in the release namespace:

- A TLS Secret with `tls.crt`, `tls.key`, `ca.crt`. The server certificate must
  include `<rendered-storage-Service-name>.<namespace>.svc` in its DNS SANs.
  Use the name from `helm template`, which accounts for long release names.
- A credential Secret with four distinct random tokens, each at least 32
  characters, under `input`, `publisher`, `inventory`, `reclaimer`, plus
  `server.json`, a JSON object mapping those same four names to the same tokens.
  For example, `openssl rand -hex 32` generates a suitable token. Only the
  server receives `server.json`; clients receive their own token and CA. The
  web holds two of them: `inventory` (list and stat, for the orphan sweep) and
  `reclaimer` (stat and exact delete).

The storage link verifies the TLS server identity and the initialized store
ID on every request. Its fixed roles have the permissions above, except the
web's `inventory` and `reclaimer` roles cannot read object bodies. There is no general IAM
engine, overwrite API or unconditional delete API.

The artifact daemon's fail-open resource cache (`--durable-store=disk`) runs on
a **dedicated** `hangar-store` instance with its own PVC, outside the chart:
`hangar-store --cache-namespace=<ns>` on a disk of its own, initialized with
its own store ID. That instance serves the cache namespace only: it refuses
`--input-namespace` or `--output-namespace` beside it, and its `server.json`
names exactly one credential, `cache`, which may create, stat, read, list and
exact-delete inside the cache namespace and nothing else. The cache never
shares a process, index lock or concurrency slot with the strict input and
output store, so cache churn cannot stall a strict read or publish.

First provision the disk service, retaining your existing artifact-daemon
TLS/key configuration and leaving client features disabled:

```yaml
hangarStorage:
  disk:
    enabled: true
    storeID: production-hangar-01
    size: 100Gi
    # storageClass: your-block-storage-class
    initialize: true
    tls:
      existingSecret: hangar-store-tls
    credentials:
      existingSecret: hangar-store-credentials
artifactDaemon:
  hangar:
    enabled: false
    webEnabled: false
    store: disk
    bucket: inputs
```

Initialization is explicit and only for a new, empty store. With
`initialize: true`, the chart stops the storage Deployment and runs a one-shot
initialization Job. Wait for that Job to succeed, then upgrade with
`initialize: false` and `artifactDaemon.hangar.enabled: true` to start the
service and its strict-input clients. Enable web emission after the daemon
rollout as described above. Keep `initialize` false thereafter;
a normal restart refuses an uninitialized or wrong-identity volume. The Job
and chart-created PVC are retained on removal. `existingClaim` can select a
pre-provisioned PVC instead. Do not rerun initialization on a replacement
volume under an existing identity.

For durable outputs, add `hangarOutput.store: disk` and
`hangarOutput.bucket: outputs` to the existing enabled output-plane
configuration. The input and output logical namespaces must differ; their
names are lowercase scope identifiers, not filesystem paths. The chart wires
the internal HTTPS endpoint, identity and role credentials automatically.
Resource-cache storage remains independently configured; GCS is unnecessary
when both Hangar planes use disk and resource caches use a non-GCS backend.

`maxObjectBytes` defaults to 16 GiB and `maxConcurrent` to four. These bound
individual transfers and concurrent work; they do not reserve free space.
Size and monitor the PVC, including temporary uploads, index growth and retained
strict inputs. Full disk returns an infrastructure failure. Output objects
become reclaimable only once every claim on them is released or expired.

Back up the whole storage directory while the owner is stopped or using a
consistent volume snapshot. The index and blobs are one unit. Restore it in
coordination with the control-plane database while the daemons and the web's
reclaim components are stopped. Never roll back the generation counter beneath references already
issued, clone a live store into two owners, or reuse its ID for an empty volume.
There is no online GCS-to-disk migration: the store (provider, store ID and
namespace) participates in the output scope, so changing backends requires a
drain and a new output configuration. Existing references are not retargeted.
