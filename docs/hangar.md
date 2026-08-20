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

Node-IP TLS encrypts the init-to-daemon request, but the current init client
does not verify the daemon's server identity. Do not describe this path as
server-authenticated mTLS. The independently verified, read-only local receipt
is the outcome proof that the exact requested tree was committed on the node.

NetworkPolicy remains off by default. When the artifact-daemon policy is
enabled it permits the GKE metadata endpoint `169.254.169.254/32` on TCP 80 and
988 for Workload Identity refresh. Policy enforcement and metadata routing are
CNI/environment-dependent; non-GKE clusters may require different egress. It
is defense in depth and does not change the receipt or TLS identity model.

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
