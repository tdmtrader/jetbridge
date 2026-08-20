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
durable store and bucket, positive content and entry limits, a positive grant
TTL no greater than 15 minutes, and a private absolute scratch path disjoint
from the artifact hostPath:

```yaml
artifactDaemon:
  enabled: true
  tls:
    enabled: true
  durable:
    store: gcs
    bucket: concourse-hangar
    prefix: production
    timeout: 5m
  hangar:
    enabled: true
    scratchPath: /var/concourse/hangar-scratch
    maxContentBytes: 10737418240
    maxEntries: 100000
    capabilityTTL: 15m
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
separate `hangar.key`. Helm reuses the existing value on upgrade and generates
32 cryptographically random bytes only when the key is absent. It does not
reuse a TLS private key.

With `artifactDaemon.tls.existingSecret`, the operator must add all normal TLS
entries plus `hangar.key`, whose decoded value must be **exactly 32 raw bytes**.
The chart mounts that selected key read-only into only the web and daemon
containers. A missing entry prevents the Pods from starting; a wrong-length
entry is rejected by both binaries at startup.

The web process signs short-lived grants and the daemon verifies the same
configured `capabilityTTL`. The default and maximum are 15 minutes; shorter
positive values reduce replay exposure. Task Pod specs contain only attenuated
grants bound to one exact reference, handle, volume, and expiry. Anyone who can
read Pod specs during that window can see those grants, but the long-lived
signing key is never placed in a task Pod command, environment, or volume.

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

`networkPolicy.enabled` remains off by default. When enabled, policy
enforcement depends on the cluster CNI and the configured selectors/rules; it
is defense in depth and does not change the receipt or TLS identity model.

## Rollout and downgrade

Roll out daemon support before allowing web nodes to emit strict inputs:

1. Upgrade the DaemonSet with Hangar configuration and wait until eligible
   nodes carry `concourse.dev/hangar-v1=ready`.
2. Upgrade/enable the web deployment so it can issue strict task inputs.

For downgrade, reverse the order: first stop web emission and wait for strict
tasks to drain, then disable or downgrade the DaemonSet. This avoids scheduling
new exact inputs onto nodes that cannot materialize them.

To disable without changing resource-cache behavior, set
`artifactDaemon.hangar.enabled=false` and follow the downgrade order. Existing
immutable GCS objects remain inert; lifecycle and reclamation for Hangar trees
are outside this first slice.
