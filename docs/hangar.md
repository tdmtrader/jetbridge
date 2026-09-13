# Hangar exact tree storage

Hangar is an opt-in, GCS-backed path for immutable filesystem-tree task
inputs. It publishes canonical trees under exact references containing an
opaque scope, a SHA-256 logical-content digest, and a GCS generation. Task
inputs name that complete reference; Hangar never substitutes a newer
generation or different content.

This first slice supports native Google Cloud Storage only. The resource-cache
S3-compatible and filesystem stores do not yet satisfy the strict Hangar
contract and are unsupported for Hangar.

## Enablement

Hangar requires the artifact DaemonSet, artifact-daemon TLS, a native GCS
durable store and bucket, positive content and entry limits, a whole-second
grant TTL from `1s` through `900s`, and a private absolute scratch path
disjoint from the artifact hostPath:

```yaml
artifactDaemon:
  enabled: true
  tls:
    enabled: true
    existingSecret: concourse-artifact-daemon-tls
  durable:
    store: gcs
    bucket: concourse-hangar
    prefix: production
    timeout: 5m
  hangar:
    enabled: true
    webEnabled: false
    allowGeneratedKey: false
    scratchPath: /var/concourse/hangar-scratch
    maxContentBytes: 10737418240
    maxEntries: 100000
    capabilityTTL: 900s
```

Hangar reuses `durable.bucket`, `prefix`, `endpoint`, and `timeout`. On GKE,
grant the artifact-daemon ServiceAccount bucket access with Workload Identity
and leave `durable.existingSecret` empty. There is no separate Hangar cloud
credential block and task Pods never receive bucket credentials.

The chart creates a private, transient `emptyDir` mounted only in the daemon at
`scratchPath`. It is used for complete canonicalization and verification, not
durable storage. Size its ephemeral-storage request/limit and node capacity for
the largest admitted canonical tree plus compressed/object-transfer staging.
Lower `maxContentBytes` and `maxEntries` to fit the node budget. The scratch
path must not equal, contain, or sit beneath `artifactDaemon.hostPath`.

## Capability key

With chart-managed artifact-daemon TLS, the same Secret also contains a
separate `hangar.key`. This mode requires the explicit
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

The web process signs short-lived grants and the daemon verifies the same
configured `capabilityTTL`. Chart values use positive whole-second syntax; the
default and maximum are `900s` (15 minutes), and `1s` is the minimum. Shorter
values reduce replay exposure. Task Pod specs contain only attenuated grants
bound to one exact reference, handle, volume, and expiry. Anyone who can read
Pod specs during that window can see those grants, but the long-lived signing
key is never placed in a task Pod command, environment, or volume.

## Runtime and failure semantics

After validating TLS, GCS access, the key, limits, and private scratch, each
daemon adds `concourse.dev/hangar-v1=ready` to its node. Strict tasks require
that label as well as the existing artifact-cache readiness label. A task init
container requests the exact tree and the task and all sidecars receive the
result as a read-only input. The local completion receipt records the exact
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
server-authenticated mTLS. The independently verified, read-only local receipt
is the outcome proof that the exact requested tree was committed on the node.

NetworkPolicy remains off by default. When the artifact-daemon policy is
enabled it permits both current GKE metadata profiles for Workload Identity
refresh: standard/Calico on GKE 1.21 and later uses
`169.254.169.252/32` on TCP 987 and 988, while Dataplane V2 uses
`169.254.169.254/32` on TCP 80 and 8080. The obsolete pre-1.21 loopback profile
is not included. Policy enforcement and metadata routing are
CNI/environment-dependent; non-GKE clusters may require different egress, and
operators must validate the policy against their cluster. It is defense in
depth and does not change the receipt or TLS identity model.

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
immutable GCS objects remain inert; lifecycle and reclamation for Hangar trees
are outside this first slice.

The daemon container is explicitly UID 0, non-privileged, unable to escalate,
under `RuntimeDefault`, and drops all capabilities except `DAC_OVERRIDE`. A
normal exact destination is kubelet-created and root-owned, so no `FOWNER` is
needed. A custom non-root daemon image or pre-chowned destination fails closed
rather than widening capabilities. Container-level root settings are not
applied to task or init containers.

## Durable output publication (contracts only, not enabled)

A second plane is being built beside the strict-input one: turning a task's
declared ordinary output into durable content a consumer can protect for as
long as it needs it. Nothing described in this section is enabled, or usable,
or reachable from any route. What exists today is the product-neutral contract:
`hangar/executioncontrol` and `hangar/output`, with their language-neutral wire
fixtures under each package's `testdata/protocol-v1`. Storage, persistence,
workers, chart identities and activation come later, each behind its own gate.

The contract is documented here rather than in a design note because two of the
things it says are easy to lose, and both of them are limits rather than
features.

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

### The lifetime promise is conditional, and says so

Hangar can serialize its own publishers, claimants, readers and reclaimers. It
cannot prevent a cloud administrator, or a changed bucket lifecycle rule, from
deleting an object out of band.

So the promise is stated conditionally. Activation requires an authoritative
whole-bucket lifecycle-policy read proving no Delete rule, refreshed at least
every 15 minutes and recorded with the bucket identity, metageneration, policy
hash and observation time. That is a **bounded-staleness trust check, not
prevention**: a functioning monitor detects a policy change within the refresh
window, and does not stop a deletion inside it.

On a stale, failed, unreadable or unsafe check — or an unexpected exact absence,
or a platform-principal mismatch — Hangar enters a durable `at-risk` state,
marks affected refs at risk in status and read outcomes, and alerts. From that
point it blocks new captures, claim acquires, managed-output grants, orphan
adoption and reclaim admission. Releases and diagnosis stay possible, existing
claims and read leases stay recorded, and already-admitted conditional delete
work may finish. Recovery needs a fresh safe attestation *and* reconciliation
of the violation: re-attesting alone never rewrites an out-of-band absence as
normal reclamation.

The enforceable half of the promise covers the isolated JetBridge principals
and the provider configuration Hangar can inspect. Everything outside that —
organization- and project-level credentials, and administrator behaviour — is
outside it, and failure of that precondition is typed and visible rather than
silently accepted.

### Storage profiles: native GCS only, in a bucket of its own

Only the strict native-GCS profile supports output capture. The
S3-compatible and filesystem stores cannot advertise this capability, and this
is a refusal rather than a to-do: the plane depends on generation-conditioned
deletes, exact-generation metadata stats and immutable-at-creation object
metadata, and a profile that emulates those has emulated the one property the
lifetime promise rests on.

Output capture uses a **dedicated** native-GCS bucket containing only
Hangar-managed output-plane objects. It is never the durable cache bucket and
never the strict-input bucket. The reason is a fact about GCS IAM rather than a
preference: `storage.objects.get` authorizes both metadata and body reads, and
`storage.objects.list` covers the whole bucket with no caller-visible prefix
boundary. There is no permission that grants metadata-only access, and none
that scopes a list to a prefix. Prefix-only isolation inside a shared bucket is
therefore not a substitute, and configuring one is an activation failure rather
than a supported alternative. Trust domains needing IAM isolation get separate
output buckets.

The bucket, its opaque scope and its key prefix are derived from authenticated
deployment context alone. No task, consumer, path parameter or receipt can
select or broaden them, and no API in `hangar/output` accepts a bucket, object
key, absolute path, hostPath or caller-chosen scope — a guard in
`hangar/output/architecture_test.go` fails the test suite if one appears.

### External responsibility

The chart does not create cloud buckets, lifecycle rules or GCP IAM, and will
not. An operator or external infrastructure provisions the dedicated output
bucket, the cloud principals, the Workload Identity bindings and the bucket
IAM; the chart renders explicit identities and refuses activation until the
attestor proves the externally provisioned policy.

Four distinct identities are required because a Kubernetes service account is
Pod-wide, so any output permission added to an existing daemon would also be
granted to that daemon's cache and strict-input identity:

- the publisher/materializer daemon may create and get objects, never list or
  delete;
- inventory may list and get bucket-wide, never create or delete;
- the reclaimer may get and delete, never create or list; and
- the policy attestor may read bucket lifecycle and IAM, and has no object
  access at all.

Shared service accounts, shared Workload Identity principals, task credentials,
or granting delete to the daemon or cache identity are activation failures. No
runtime principal holds lifecycle or IAM mutation authority: policy
administration stays an operator trust root, which is the same boundary the
conditional promise above describes.
