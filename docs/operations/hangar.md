# Hangar-backed agent snapshot storage

Hangar is the durable authority for agentic snapshot archives. Each object is
addressed by the SHA-256 of its uncompressed canonical tar archive and is
stored by Hangar as a verified, immutable zstd representation. The
artifact-daemon host path is only a node-local, on-use cache.

## Configure production GCS

Enable agent snapshots and point the daemon at a dedicated bucket. The daemon
uses application-default credentials when `artifactDaemon.hangar.endpoint` is
empty. Bind the daemon ServiceAccount to only the bucket permissions required
to create, inspect, read, and generation-delete Hangar objects; do not mount a
long-lived JSON key into the pod.

```yaml
agentSnapshots:
  enabled: true

artifactDaemon:
  serviceAccount:
    annotations:
      iam.gke.io/gcp-service-account: concourse-hangar@project.iam.gserviceaccount.com
  hangar:
    bucket: concourse-agent-snapshots
  networkPolicy:
    enabled: true
    hangarEgressTo:
      - ipBlock:
          cidr: 198.51.100.32/32 # operator-specific GCS Private Service Connect address
```

When daemon egress policy is enabled, `hangarEgressTo` is required and the
chart permits only these explicit destinations on TCP 443. Keep this list to
the private GCS endpoint or emulator service. Credentials are never chart
arguments or generated Secrets.

The bucket is required whenever `agentSnapshots.enabled` is true. The daemon
does not acknowledge a snapshot PUT until Hangar has verified and committed
the canonical bytes. A cache miss reads the generation-pinned object through
Hangar, validates the digest again, and atomically re-populates the local
cache. Agentic snapshots are intentionally excluded from peer mirroring.

## Long-lived emulator

For a non-production integration environment, set
`artifactDaemon.hangar.endpoint` to the emulator's explicit GCS-compatible
JSON endpoint. The endpoint deliberately selects the unauthenticated emulator
client; it must not be set in production. Give the emulator persistent storage
and expose it only through a narrowly selected `hangarEgressTo` destination.

```yaml
artifactDaemon:
  hangar:
    bucket: concourse-hangar-test
    endpoint: https://fake-gcs.ci.example/storage/v1/
```

The `hangar.scratchPath` default is a daemon-local directory under
`artifactDaemon.hostPath`. It is temporary compression/read-verification
workspace, not durable content. Size it for one maximum snapshot transfer per
active daemon operation and keep it on local disk rather than tmpfs.

## Monitoring and recovery

Scrape the artifact-daemon ServiceMonitor. The existing
`artifact_daemon_snapshot_operations_total`,
`artifact_daemon_snapshot_bytes_total`, and
`artifact_daemon_snapshot_duration_seconds` metrics report PUT, GET, HEAD,
and DELETE outcomes. Alert on sustained error outcomes or a sharp rise in GET
latency; either can indicate a Hangar credential, egress, or object-integrity
problem.

To exercise recovery, delete only the daemon's local `snapshots/` cache on a
test node, then read a known retained snapshot. The read must succeed and
recreate the digest-addressed local file from Hangar. Do not delete bucket
objects to test cache loss: that tests irreversible retention deletion rather
than mirror recovery.

## Adoption and rollback

Pre-Hangar snapshots can remain on a daemon host path. With Hangar enabled,
the daemon adopts a valid legacy local snapshot by committing its exact
canonical bytes before serving it. Existing daemon-node location rows remain
readable during this transition; new successful writes record a single
`hangar-v1` location rather than node replicas.

Before rolling back this release, stop new agentic snapshot writes, wait for
or explicitly expire retained agentic snapshots, and verify no live
`hangar-v1` location remains. Retain the bucket and daemon Hangar flags until
that check is complete. Removing the bucket configuration first makes a
previous release unable to restore cache-loss snapshots. Bucket deletion is a
separate, destructive retention operation and requires an explicit recovery
plan.
