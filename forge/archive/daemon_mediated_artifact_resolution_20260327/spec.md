# Spec: Daemon-Mediated Artifact Resolution

**Track ID:** `daemon_mediated_artifact_resolution_20260327`
**Type:** feature

## Overview

The current DaemonSet artifact system has three fragility points:

1. **In-memory ArtifactLocator** is the sole source of truth for artifact locations. Lost on ATC restart, no fallback — downstream steps fail with "artifact location unknown".

2. **Three fetch paths in init containers** — a 60-line bash script with local `cp -a`, remote `wget`, and unknown-node branches, each with their own retry logic, error handling, and failure modes. The unknown-node path silently tries local and fails without attempting remote.

3. **Brittle path resolution** — artifact paths flow through four string transformations (producer record → locator store → consumer TrimPrefix → daemon filepath.Join). Mismatches between output subdirectory names and recorded paths cause silent failures (e.g., `/dir/<output>` vs `<output>`).

This track replaces the multi-path init container fetch logic with a single call to the **local artifact-daemon**, which becomes the sole authority on artifact resolution, location, and delivery.

## Why

- A bug where producer recorded `/dir/<output>` but consumer expected `<output>` wasted hours of debugging — the string surgery in `daemonSetFetchCommand` is inherently fragile
- ATC restarts during pipelines cause unrecoverable failures with no fallback
- `SkipResourceCache() = true` forces every get step to re-execute, wasting time on slow resources — this is only needed because the locator-based system can't serve cached volumes
- Init container bash scripts are hard to test, hard to log, and have inconsistent retry behavior across branches
- Future storage optimizations (reflink, ZFS clone, bind mounts) would require changing every init container — with daemon-mediated resolution, only the daemon changes

## Design

### New flow

```
Init container → POST local daemon /resolve {key, dest}
                      │
                      ├─ Artifact on local disk? → cp to dest → 200
                      │
                      └─ Not local? → HEAD peer daemons
                                      → GET from peer that has it
                                      → write to dest → 200
```

### Init container (simplified)

```bash
wget --post-data='{"key":"${KEY}","dest":"${DST}"}' \
  http://localhost:${PORT}/resolve
```

One call. No retries (daemon handles transient peer errors internally). No branching. If daemon returns error, build fails fast.

### Daemon registry

The daemon scans its hostPath at startup and maintains a local registry of known artifacts. When a step completes, the ATC calls `POST /register {key, localPath}` to register the artifact explicitly (belt-and-suspenders with the filesystem scan).

### Peer discovery

When the local daemon doesn't have an artifact, it queries peer daemons via EndpointSlice IPs: `HEAD /artifacts/<key>`. First 200 response → proxy `GET` from that peer. Retries for transient network errors happen in Go, not bash.

### Key simplification

Replace `ArtifactKey("handle") → "artifacts/<handle>.tar"` with flat volume handle as the key. The daemon maps keys to disk paths internally — no path surgery in init containers or the ATC.

### Resource cache re-enablement

With daemon-mediated resolution, `SkipResourceCache()` can return `false`. Cached volumes exist on disk at their hostPath location. The daemon can serve them directly — no locator entry needed. The ArtifactLocator becomes a scheduling hint only (used by `buildAffinity` to prefer co-located nodes).

## Requirements

1. Daemon exposes `POST /resolve` endpoint that accepts `{key, dest}` and copies/fetches the artifact to the destination path
2. Daemon exposes `POST /register` endpoint for explicit artifact registration by the ATC
3. Daemon scans hostPath at startup to populate its local artifact registry
4. Daemon discovers peers via EndpointSlice API for cross-node resolution
5. Init containers make a single HTTP call to the local daemon — no branching, no retries, no path manipulation
6. Artifact keys are flat volume handles — no `artifacts/<handle>.tar` wrapping
7. `SkipResourceCache()` returns `false` — resource cache hits are served by the daemon
8. ArtifactLocator is retained for scheduling affinity hints only, not for fetch resolution
9. All fetch decisions and errors are logged in the daemon (structured JSON), not in init container bash
10. GC cleanup calls `DELETE /artifacts/<key>` on the daemon that owns the artifact

## Acceptance Criteria

- [ ] Init container fetch script is ≤5 lines of shell with zero branching
- [ ] ATC restart mid-pipeline does not break downstream artifact fetching
- [ ] Resource cache hits avoid re-executing the resource script
- [ ] Cross-node artifact fetch works without ArtifactLocator entries
- [ ] Artifact daemon logs show clear resolution path for every fetch (local vs peer)
- [ ] `go test ./atc/worker/jetbridge/...` passes
- [ ] `go test ./cmd/artifact-daemon/...` passes
- [ ] Live cluster validation: multi-step pipeline with get→task→put across nodes

## Out of Scope

- Reflink/ZFS/btrfs copy optimizations (future enhancement, only daemon changes needed)
- Persistent artifact registry across daemon restarts (filesystem scan is sufficient)
- Artifact replication/caching on consumer nodes (peer proxy is sufficient for now)
- Changes to the Helm chart or DaemonSet K8s manifests (existing deployment works)
