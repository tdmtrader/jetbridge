# Spec: DaemonSet Artifact Cache

## Overview

The current artifact store PVC architecture tars step outputs to a shared PVC (often GCS Fuse-backed), then extracts them via init containers on the next step. Production traces show this upload phase takes 30-40s per step — often longer than the step itself — due to SPDY exec overhead, sequential uploads, and cloud storage write latency.

This track introduces an alternative artifact backend: a DaemonSet running on each node with a lightweight HTTP file server and local hostPath storage. Artifacts are stored on the node that produced them and served to other nodes on demand. Combined with soft scheduling affinity, most artifacts are read locally (zero network transfer). When a step lands on a different node, artifacts are pulled over the pod network at wire speed (~40ms for 50MB on 10Gbps).

This is an **alternative mode** alongside the existing PVC backend, selectable via configuration (`ArtifactBackend: "daemonset"`).

## Requirements

### DaemonSet artifact-daemon

1. A Go binary (`cmd/artifact-daemon/`) that:
   - Mounts a hostPath volume at a configurable path (e.g., `/var/concourse/artifacts`)
   - Runs an HTTP server with endpoints:
     - `GET /artifacts/{key}` — stream the artifact tar file
     - `PUT /artifacts/{key}` — receive and store an artifact tar file
     - `DELETE /artifacts/{key}` — remove an artifact
     - `HEAD /artifacts/{key}` — existence check
     - `GET /healthz` — liveness/readiness probe
   - Minimal resource footprint (50m CPU, 64Mi memory request)

2. DaemonSet K8s manifest (added to Helm chart) that:
   - Runs on every node (with configurable tolerations)
   - Mounts hostPath for artifact storage
   - Exposes the HTTP server via a headless Service for per-pod DNS
   - Labels its node `concourse.dev/artifact-cache=ready` on startup (via init or on-start hook)
   - Removes the node label on shutdown (preStop hook)

### ATC artifact location tracking

3. The ATC maintains an in-memory mapping of `artifact-key → node-name` for the DaemonSet backend. When a step produces an output, the ATC records which node it ran on. When a downstream step needs that artifact, the ATC provides the source node.

4. This mapping is ephemeral — lost on ATC restart. On restart, in-flight builds retry from the producing step (same behavior as today when a PVC is lost).

### Step scheduling with soft affinity

5. When creating a step pod, if the step's inputs come from a known node, the ATC adds a `preferredDuringSchedulingIgnoredDuringExecution` node affinity for that node. This biases the scheduler toward co-location without blocking if the node is full.

6. A hard `requiredDuringSchedulingIgnoredDuringExecution` node affinity ensures step pods only land on nodes with the `concourse.dev/artifact-cache=ready` label (i.e., nodes with a healthy DaemonSet pod).

### Artifact upload (step → local DaemonSet)

7. The step pod mounts the same hostPath as the DaemonSet. After step completion, the main container (via SPDY exec) writes the tar directly to the hostPath — no HTTP needed for local writes, no sidecar needed. The upload is `tar cf /artifacts/<key> -C <output-path> .` to a local hostPath mount.

8. **The artifact-helper sidecar is eliminated in DaemonSet mode.** Uploads are direct hostPath writes from the main container. StreamOut (for `set_pipeline`/`load_var`/`file:`) goes through the DaemonSet HTTP server. This means one fewer container per pod, faster pod startup, and lower resource overhead.

### Artifact fetch (cross-node DaemonSet → step)

9. Init containers in step pods fetch artifacts. Two paths:
   - **Local path** (same node): `tar xf /artifacts/<key> -C <dest>` — read from hostPath, sub-second.
   - **Remote path** (different node): HTTP GET from the source node's DaemonSet pod, piped to `tar xf - -C <dest>`.

10. The init container determines which path to use based on metadata injected by the ATC (environment variable or annotation indicating source node vs current node).

### StreamOut for set_pipeline / load_var / file:

11. When the ATC needs to read a file from a build artifact (for `set_pipeline`, `load_var`, `file:` directives), it HTTP GETs from the DaemonSet pod on the source node. This replaces the current pattern of exec'ing tar in the artifact-helper sidecar.

12. A new `DaemonSetVolume` type implements `runtime.Volume` with a `StreamOut` that HTTP GETs from the DaemonSet.

### GC and cleanup

13. The ATC's existing reaper drives artifact cleanup. When the reaper destroys containers, it calls HTTP DELETE on the DaemonSet for each artifact key associated with the destroyed container. This mirrors the existing `cleanupArtifactStoreEntries` pattern.

14. The DaemonSet also runs a local TTL-based sweep (e.g., delete artifacts older than 2 hours) as a safety net for artifacts the ATC missed (crash, network partition). TTL is configurable.

### Task caches

15. Task caches use the same DaemonSet hostPath. Cache keys are stable across builds (keyed by jobID + stepName + path). On the same node, caches are read directly from hostPath. On a different node, caches are pulled from the DaemonSet on the node that last wrote the cache.

16. Soft affinity for cache-warm nodes: when a job has a known cache location, the ATC adds a scheduling preference for that node.

### Configuration

17. New config fields on the K8s runtime:
    - `ArtifactBackend` — `"pvc"` (default, current behavior) or `"daemonset"`
    - `ArtifactDaemonPort` — HTTP port for the DaemonSet (default: 8080)
    - `ArtifactDaemonHostPath` — hostPath for artifact storage (default: `/var/concourse/artifacts`)
    - `ArtifactDaemonTTL` — safety-net TTL for artifact cleanup (default: 2h)
    - `ArtifactDaemonService` — K8s headless Service name for DaemonSet DNS (default: `artifact-daemon`)

## Technical Approach

### DaemonSet binary

A standalone Go binary in `cmd/artifact-daemon/` with:
- `net/http` server (no framework needed for 4 endpoints)
- hostPath as the storage backend — artifact keys map to file paths
- Structured logging via `lager`
- Health check endpoint
- Graceful shutdown with node label cleanup

### ATC integration

- New `DaemonSetVolume` type in `atc/worker/jetbridge/` implementing `runtime.Volume`
- `StreamOut` → HTTP GET from DaemonSet
- `StreamIn` → HTTP PUT to DaemonSet (or direct hostPath write)
- Artifact location map in the `Worker` or a dedicated `ArtifactLocator` component
- Pod builder adds soft node affinity when artifact source node is known
- Pod builder adds hard node affinity for `concourse.dev/artifact-cache=ready` label
- Init container commands branch on local vs remote based on ATC-injected metadata

### Helm chart

- New DaemonSet template in `deploy/chart/`
- Headless Service for per-pod DNS
- Configurable hostPath, port, tolerations, resource limits
- Node labeling via an init container or startup script in the DaemonSet pod

## Acceptance Criteria

- [ ] `artifact-daemon` binary builds and runs, serving artifacts over HTTP
- [ ] DaemonSet deploys via Helm chart on all nodes, labels nodes on startup
- [ ] Step pods with `ArtifactBackend: "daemonset"` upload outputs to local hostPath
- [ ] Downstream steps on the same node read artifacts from hostPath (local path, sub-second)
- [ ] Downstream steps on a different node fetch artifacts via HTTP from the source node's DaemonSet
- [ ] Soft node affinity biases scheduling toward artifact-source nodes
- [ ] Hard node affinity prevents scheduling on nodes without the DaemonSet
- [ ] `set_pipeline`, `load_var`, and `file:` directives read artifacts via DaemonSet HTTP
- [ ] Reaper cleans up artifacts via HTTP DELETE when containers are destroyed
- [ ] DaemonSet TTL sweep cleans up orphaned artifacts
- [ ] Task caches work via DaemonSet hostPath with cross-node pull
- [ ] Existing PVC backend continues to work unchanged when `ArtifactBackend: "pvc"`
- [ ] All existing tests pass; new tests cover DaemonSet artifact flow

## Out of Scope

- mTLS or authentication for the DaemonSet HTTP server (pod network trust is sufficient for now)
- Local SSD support (future optimization — swap hostPath for local SSD mount)
- Write-through to cloud storage for cache durability across spot preemptions
- Multi-cluster artifact sharing
- Compression on the HTTP transfer path (evaluate after telemetry)
