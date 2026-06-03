# Spec: Artifact Daemon Resilience

**Track ID:** `artifact_daemon_resilience_20260425`
**Type:** feature

## Overview

The artifact daemon stores step outputs and resource cache aliases on a single
node's hostPath. When that node is lost — preempted GCP spot, kernel panic, OOM
kill of the kubelet, hardware fault — every artifact it held becomes
unrecoverable, forcing affected builds to fail and rerun even though the data
itself was nowhere else in the cluster.

This track makes the artifact daemon resilient to single-node loss by:

1. Asynchronously mirroring step output data to peer daemons after each step
   completes (configurable replication factor, default RF=2 = local + 1 peer).
2. Falling back to peer-probe reads on the ATC side when the recorded
   producer node is unreachable.
3. Using GCP spot preemption notifications to flush any unmirrored artifacts
   before the node terminates.

Resource caches benefit incidentally — once the underlying step output is
mirrored, the existing `RegisterAlias` flow successfully registers the alias
on every peer (path validation passes because the data is now there). Task
caches (`caches:` field) intentionally do not participate; cache miss on
node loss is acceptable.

## Problem

- Producer node loss (spot preemption or crash) makes step outputs and
  resource cache hits unavailable, breaking downstream consumers and
  forcing build reruns.
- The daemon's existing peer probe (`PeerResolver.Probe` in
  `cmd/artifact-daemon/peers.go`) finds artifacts on peers, but no peer
  has them — every artifact lives on the producer node only.
- The ATC's direct read path (`DaemonSetVolume.StreamOut` →
  `NodeIPResolver`) targets the recorded `nodeName` and fails hard when
  the node is gone; there is no peer-fallback in the ATC.
- Resource cache hits on lost producers degrade further than necessary:
  even when the underlying data could be served by another node, the
  `/register` alias was only ever accepted on the producer's daemon
  (path validation rejects the alias everywhere else).

## Requirements

1. Step outputs (`steps/{handle}/{output}/`) MUST be mirrored to up to
   `replicas - 1` peer daemons after the producer step completes.
2. Resource cache aliases (`/register`) MUST end up registered on every
   peer that received the mirrored data.
3. Task caches (`caches:` field, hostPath under `cacheHostPath`) MUST NOT
   be mirrored — they never flow through the daemon's HTTP API and a
   cache miss is acceptable.
4. ATC `DaemonSetVolume.StreamOut` MUST fall back to a peer-probe when
   the recorded producer node is unreachable (connection error,
   `ErrNodeNameIsIP`, or HTTP 4xx/5xx).
5. Mirroring MUST be best-effort and asynchronous — failures MUST NOT
   break the producing step's foreground flow.
6. Replication factor MUST be configurable per deployment:
   - `0` → mirroring disabled.
   - `N` (positive int) → mirror to up to `N-1` peers; if fewer peers
     exist than requested, mirror to all available peers without error.
   - `-1` (or string `"all"` in Helm) → mirror to every live peer.
   - **Default = `2`** (local + 1 peer copy).
7. Peer selection (when `N-1 < live peers`) MUST be deterministic per
   artifact key (consistent hashing) so that subsequent reads can probe
   the same set of peers without coordination.
8. On GCP spot preemption notice, the daemon MUST attempt to flush any
   unmirrored step outputs to live peers before the kubelet terminates
   it (best-effort within the ~30s preemption window).
9. The mirror trigger from ATC MUST be best-effort — failure to reach
   the daemon MUST NOT fail the step.
10. Behavior MUST be backwards compatible at the data level: existing
    artifacts on disk continue to work; the only behavioral change is
    that new step outputs gain a peer copy.

## Technical Approach

### Phase 1 — ATC-side read fallback (foundation)

`DaemonSetVolume.StreamOut` today reads from
`{recordedNodeIP}:{port}/artifacts/{key}`. If the node is gone, the
read fails with a connection error or `ErrNodeNameIsIP` (see the
recently-merged `fix_cache_locator_pod_ip_poisoning_20260423` track).

Add a new method on `DaemonClient`:

```go
ProbeStepArtifact(ctx context.Context, key string) (daemonIP string, found bool, err error)
```

Mirrors the existing `ProbeResourceCache` — concurrent HEAD against
`/artifacts/steps/{key}` on every daemon IP discovered via
EndpointSlices. Returns the first IP that responds 200.

Update `DaemonSetVolume.StreamOut` to:

1. Try the recorded node (current behavior).
2. On connection error / `ErrNodeNameIsIP` / HTTP 4xx/5xx, call
   `ProbeStepArtifact` and retry from the discovered IP.
3. If the probe finds nothing, return today's "not found" error
   unchanged.

This is plumbing — no behavior change today (no peer ever has it). It
becomes useful the moment Phase 2 lands.

### Phase 2 — Async background mirror

**New daemon endpoint:** `POST /mirror`

Body: `{"key": "handle/output"}` (the key under `steps/`).

Behavior: enqueues an async mirror job. Returns 202 Accepted
immediately. Endpoint is mTLS-protected (uses the existing
`requireClientCert` middleware path).

**Mirror worker pool** (new `cmd/artifact-daemon/mirror.go`):

- Bounded goroutine pool (configurable, default 4 concurrent jobs).
- For each job:
  1. List peer IPs via existing `PeerResolver.peerIPs`.
  2. Apply RF policy: pick `min(RF-1, len(peers))` peers using
     consistent hashing on the artifact key.
  3. For each chosen peer: tar the local
     `{storagePath}/steps/{key}/` directory and PUT to
     `{scheme}://{peerIP}:{port}/stream-in/{key}` (TLS reuses the
     existing peer client cert plumbing).
  4. Log per-peer outcome (success / failure / unreachable). No
     retries — best effort.
- Track per-key mirror status in memory (which peers acked) for use by
  Phase 3 evacuation.

**Daemon-side trigger from `handleStreamIn`:**

After the existing `s.registry.Register(key, dest)` call, enqueue a
mirror job for the same key. This covers fly uploads and any future
ATC stream-in flows.

**ATC-side triggers:**

The mirror trigger fires unconditionally whenever `daemonClient` is
configured (no separate feature flag — RF=0 at the daemon level is the
disable switch).

- `RecordOutputs` (`atc/worker/jetbridge/storage_daemonset.go:386`):
  after `registerDaemonAlias` succeeds, call new
  `daemonClient.TriggerMirror(ctx, daemonIP, daemonKey)` for the
  producer's daemon. Best-effort — log on error, do not fail the
  step.
- `RegisterResourceCache` (`storage_daemonset.go:523`):
  call `TriggerMirror(ctx, daemonIP, daemonKey)` BEFORE
  `RegisterAlias` so peers have the data when the alias broadcast
  arrives.

**Peer selection (consistent hashing):**

```
sortedPeers := sort(peerIPs)            // stable order
hash := fnv64(key)
start := hash % len(sortedPeers)
chosen := sortedPeers[start : start + (RF-1)]   // wraps around
```

Same key always lands on the same subset of peers (until membership
changes), so reads can probe in the same hash order.

**Configuration (daemon flags):**

```
--mirror-replicas int         (default 2; 0 = disabled, -1 = all peers)
--mirror-concurrency int      (default 4)
--mirror-timeout duration     (default 5m per-peer per-job)
```

**Helm values** (`deploy/chart/values.yaml`):

```yaml
artifactDaemon:
  mirror:
    # 0 = disabled, N = local + (N-1) peers, "all" = every peer
    replicas: 2
    concurrency: 4
    timeout: "5m"
```

The chart template translates string `"all"` → `-1` for the daemon
flag.

### Phase 3 — Preemption-triggered evacuation

**New file:** `cmd/artifact-daemon/preemption.go`.

GCP exposes preemption status at:

```
http://metadata.google.internal/computeMetadata/v1/instance/preempted
```

Long-poll with `?wait_for_change=true` and header
`Metadata-Flavor: Google`. Returns the new value (`TRUE`) when the VM
is about to be preempted (~30s warning).

On preempt:

1. Stop accepting new mirror jobs (drain mode).
2. For every step directory under `{storagePath}/steps/` whose
   in-memory mirror status is missing or partial, synchronously fan
   out PUTs to live peers up to the RF.
3. Total time-budget = 25s (leave 5s slack); per-peer timeout = 5s.
4. Best-effort — if we can't push a particular artifact in time, it
   stays unmirrored and a build that depends on it will rerun. That's
   acceptable per the track's failure-mode budget.

**Configuration (daemon flags):**

```
--preemption-watch    (bool, default false)
--preemption-budget   (duration, default 25s)
```

**Helm values:**

```yaml
artifactDaemon:
  preemption:
    enabled: false       # set true on GCP spot deployments
    budget: "25s"
```

### Why these triggers, not inotify

We considered watching the filesystem with inotify on the daemon side,
which would avoid touching the ATC. Rejected because:

- "Done writing" is hard to detect from filesystem events alone.
  Producer pods write incrementally, and we'd need debouncing /
  quiescence detection that's brittle and racy.
- The ATC already has a clean step-completion hook (`RecordOutputs` /
  `RegisterResourceCache`), and adding one additional best-effort
  HTTP call is much simpler than running an inotify state machine.
- Inotify on a hostPath shared with arbitrary user pods has nasty
  edge cases (permission, recursion limits, watch overflow under
  pressure).

The tradeoff: ATC must change. The change is small (~30 lines across
two call sites) and the value is high.

## Acceptance Criteria

### Phase 1 — read fallback

- [ ] `DaemonClient.ProbeStepArtifact(ctx, "handle/output")` returns
      the IP of any daemon whose `HEAD /artifacts/steps/handle/output`
      returns 200, or `("", false, nil)` if none.
- [ ] `DaemonSetVolume.StreamOut`: when the recorded node returns
      connection refused, falls back to `ProbeStepArtifact` and reads
      from the discovered IP.
- [ ] `DaemonSetVolume.StreamOut`: when `ProbeStepArtifact` finds
      nothing, returns the original "not found" error unchanged.
- [ ] `DaemonSetVolume.StreamOut` happy path (recorded node alive): no
      probe, no behavior change vs. today.

### Phase 2 — mirror

- [ ] `POST /mirror {"key":"h/o"}` enqueues a job and returns 202
      immediately.
- [ ] Mirror worker tars `{storagePath}/steps/h/o/` and PUTs to the
      hash-selected peers' `/stream-in/h/o`.
- [ ] After mirror completes, the peer's filesystem has
      `{peerStoragePath}/steps/h/o/`, registry has the entry, and
      `GET /artifacts/steps/h/o` returns 200 on the peer.
- [ ] `mirrorReplicas=0`: no mirror jobs ever scheduled.
- [ ] `mirrorReplicas=2` with 1 available peer: mirrors to that 1 peer
      without error.
- [ ] `mirrorReplicas=2` with 0 peers (single-node cluster): logs a
      debug message and is a no-op (no error).
- [ ] `mirrorReplicas=-1` (`"all"`): mirrors to every live peer.
- [ ] Same key mirrored twice picks the SAME subset of peers (hash
      determinism).
- [ ] Peer down during mirror: job logs failure, succeeds for
      reachable peers, no retry.
- [ ] `handleStreamIn` schedules a mirror job after extraction.
- [ ] `RecordOutputs` calls `daemonClient.TriggerMirror` after
      `registerDaemonAlias`.
- [ ] `RegisterResourceCache` calls `TriggerMirror` BEFORE
      `RegisterAlias`. Verified by ordering test.
- [ ] Trigger call failures (daemon unreachable, 5xx) do NOT propagate
      out of `RecordOutputs` / `RegisterResourceCache`.
- [ ] After a get step on node A with `mirrorReplicas=2`: a
      cross-node task on node B can read the resource cache hit even
      after node A is removed from the cluster (behavioral test).
- [ ] After a task on node A with `mirrorReplicas=2`: a downstream
      task scheduled on node B reads the step output via peer
      fallback even after node A is removed (behavioral test).

### Phase 3 — preemption

- [ ] Preemption watcher polls metadata server with
      `?wait_for_change=true` and `Metadata-Flavor: Google`.
- [ ] On `TRUE` response, drain mode flips on (no new mirror jobs).
- [ ] Unmirrored step directories are pushed to live peers under the
      preemption budget.
- [ ] Watcher disabled by default; enables only with
      `--preemption-watch`.
- [ ] If the metadata endpoint is unreachable, the watcher logs and
      continues retrying without crashing the daemon.

### Cross-cutting

- [ ] `make test-unit`, `ginkgo ./atc/worker/jetbridge/...`,
      `cd ci-agent && go test ./...`, and the daemon's
      `go test ./cmd/artifact-daemon/...` all pass.
- [ ] `helm template deploy/chart` renders without errors with both
      `mirror.replicas: 0` and `mirror.replicas: 2`.
- [ ] No regression in `daemonset_artifact_security_20260408` track
      (mTLS still enforced on `/mirror` and on peer PUTs).

## Out of Scope

- Resource cache or task cache mirroring (resource cache aliases get
  mirrored incidentally as part of step output mirror; task caches
  are explicitly excluded).
- Synchronous (blocking) mirror modes — async only.
- Retry queue or persistent mirror state across daemon restarts —
  per-key tracking is in-memory only.
- Backfill to peers that join the cluster after a mirror occurred.
- AWS spot / generic K8s preemption signals — Phase 3 is GCP-only.
  AWS spot can be added in a follow-up that swaps the metadata source.
- External object storage (GCS / S3) — explicitly off the table per
  user constraints.
- Operational metrics / alerting beyond structured log lines —
  metrics can be a follow-up track.
- Mirror garbage collection alignment — peers' existing TTL sweeper
  handles their copies independently. We accept that a long-running
  artifact may be swept on one peer before another; the read fallback
  still finds whichever copies remain.
- Encrypting at rest — mirror uses the existing TLS-in-transit
  pipeline; on-disk encryption is unchanged.

## Risks

1. **Mirror amplification under write-heavy load.** RF=2 doubles
   network I/O on every step output. The bounded worker pool caps
   parallelism but does not cap throughput. Mitigated by sane defaults
   (concurrency=4) and the option to disable via `replicas=0`.
2. **Tar-during-active-write race.** Producer writes complete before
   `RecordOutputs`, so this is theoretical, but the daemon could
   start tarring a step dir while the OS is still flushing. Step
   outputs are write-once, so any partial read would simply be missing
   data — not corrupt — and the read fallback would mask it. No
   special locking.
3. **Hash-based peer selection on membership change.** If a peer
   disappears between a mirror and a read, the hash points to a
   missing peer. Mitigation: read fallback (Phase 1) probes ALL
   daemons concurrently, not just the hash-selected subset.
4. **Phase 3 preemption budget.** If a node holds a lot of large
   unmirrored data and gets preempted, we can't flush everything in
   25s. Acceptable — that's the rerun budget per the track's risk
   stance.

## Test Strategy

- **Unit tests** — Per package: `mirror.go` worker pool, hash peer
  selection, `ProbeStepArtifact`, `StreamOut` fallback, preemption
  watcher (use `httptest` to fake metadata).
- **Integration / behavioral** — Extend
  `cmd/artifact-daemon/behavioral_cross_node_test.go` with two new
  scenarios:
  1. `Mirror_AfterStepCompletion_ReplicaServesAfterProducerDeath`
  2. `Mirror_ResourceCacheHit_SurvivesProducerDeath`
- **Helm rendering** — Smoke-test the new values render correctly
  with `replicas: 0`, `replicas: 2`, and `replicas: "all"`.
