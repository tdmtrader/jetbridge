# Implementation Plan: Daemon-Mediated Artifact Resolution

## Phase 1: Daemon /resolve and /register endpoints

Add the new endpoints to the artifact-daemon HTTP server. These are additive — existing endpoints (`GET/PUT/DELETE/HEAD /artifacts/<key>`) remain unchanged.

- [x] Add in-memory artifact registry to daemon server (map[key]localPath, mutex-protected) e6332ed2d
- [x] Add startup filesystem scan: walk hostPath, register all `steps/<handle>/` directories as known artifacts e6332ed2d
- [x] Implement `POST /register` endpoint: accepts `{key, localPath}`, adds to registry e6332ed2d
- [x] Implement `POST /resolve` endpoint: accepts `{key, dest}`, copies local artifact to dest via `cp -a` e6332ed2d
- [x] Add structured JSON logging to /resolve: log key, dest, source path, resolution method (local/peer), duration e6332ed2d
- [x] Write tests for daemon registry, /register, /resolve (local path) e6332ed2d
- [ ] Phase 1 verification: daemon starts, scans hostPath, serves /resolve for local artifacts

---

## Phase 2: Peer discovery and cross-node resolution

Enable the daemon to find and fetch artifacts from peer daemons when the artifact is not local.

- [x] Add EndpointSlice-based peer discovery: query `discovery.k8s.io/v1` EndpointSlices for the artifact-daemon headless service 61d1cd0fc
- [x] Implement peer probe: `HEAD /artifacts/<key>` against each peer IP, return first 200 61d1cd0fc
- [x] Implement peer fetch: `GET /artifacts/<key>` from the peer that responded, stream to dest path 61d1cd0fc
- [x] Add retry logic in Go for transient peer errors (connection refused, timeout) — 3 retries with backoff 61d1cd0fc
- [x] Integrate peer fetch into `/resolve`: local miss → peer discovery → peer fetch → write to dest 61d1cd0fc
- [x] Add structured logging for peer resolution: peers checked, peer selected, fetch duration 61d1cd0fc
- [x] Write tests for peer discovery and cross-node /resolve 61d1cd0fc
- [ ] Phase 2 verification: daemon serves /resolve for artifacts on other nodes

---

## Phase 3: Simplify key scheme

Replace the vestigial `"artifacts/<handle>.tar"` key with flat volume handles throughout the system.

- [x] Change `ArtifactKey(handle)` to return the handle directly (no `artifacts/` prefix, no `.tar` suffix) c81d366e7
- [x] Update daemon /register and /resolve to use flat keys c81d366e7
- [x] Update daemon filesystem scan to map flat keys to disk paths c81d366e7
- [x] Update `recordOutputLocations` to register with flat keys c81d366e7
- [x] Update `buildArtifactInitContainers` to pass flat keys to init containers c81d366e7
- [x] Update `DaemonSetVolume` to use flat keys c81d366e7
- [x] Update reaper GC cleanup to use flat keys in DELETE requests c81d366e7
- [x] Ensure no key conflicts: validate keys are valid filesystem-safe strings c81d366e7
- [x] Update all tests for new key format c81d366e7
- [ ] Phase 3 verification: end-to-end artifact flow works with flat keys

---

## Phase 4: Simplify init containers

Replace the multi-branch bash fetch script with a single daemon call.

- [x] Replace `daemonSetFetchCommand()` with a simple `wget --post-data` to local daemon `/resolve` c81d366e7
- [x] Remove `resolveDaemonPodIP()` from container.go (daemon handles peer discovery) c81d366e7
- [x] Remove `SOURCE_NODE`, `SOURCE_DAEMON_IP`, `MY_NODE_NAME` env vars from init containers c81d366e7
- [x] Keep `ArtifactDaemonPort` env var for init container → local daemon communication c81d366e7
- [x] Update `buildArtifactInitContainers` to produce the simplified init container spec c81d366e7
- [x] Remove `daemonSetFetchCommand` (the 60-line bash script generator) c81d366e7
- [x] Update all tests that assert on init container commands/env vars c81d366e7
- [ ] Phase 4 verification: init containers use single-call fetch, builds pass

---

## Phase 5: ATC-side simplification

Simplify the ATC's role: locator becomes a hint, registration calls the daemon.

- [x] After `recordOutputLocations`, call daemon `POST /register` to register artifacts with the local daemon c81d366e7
- [x] Change `ArtifactLocator` usage: retain for `buildAffinity` scheduling hints only c81d366e7
- [x] Remove locator lookup from `buildArtifactInitContainers` (daemon resolves, not ATC) c81d366e7
- [x] Simplify `buildArtifactInitContainers`: no `artifactLocate()` call, no `loc.HostDir` usage — just pass key and dest to init container c81d366e7
- [x] Update `DaemonSetVolume.StreamOut` to use flat key in daemon URL c81d366e7
- [x] Update tests for ATC-side changes c81d366e7
- [ ] Phase 5 verification: builds work with ATC restart mid-pipeline (daemon registry survives)

---

## Phase 6: Re-enable resource caching

With daemon-mediated resolution, cached volumes can be served directly.

- [~] Change `SkipResourceCache()` to return `false`
- [ ] Ensure `LookupVolume` for cached resource returns a volume with the correct key
- [ ] Daemon serves cached volumes via /resolve (they exist on hostPath from previous builds)
- [ ] Verify cache hit flow: get step finds cache → no pod created → downstream task init container calls daemon → daemon serves from local disk
- [ ] Add test: cached resource version is served without re-executing resource script
- [ ] Add test: cache hit followed by daemon-mediated fetch in downstream step
- [ ] Measure performance improvement: compare pipeline duration with/without cache hits
- [ ] Phase 6 verification: cache hits work end-to-end, pipeline is faster for repeated versions

---

## Phase 7: Cleanup and hardening

Remove dead code, add observability, harden edge cases.

- [ ] Remove old `daemonSetFetchCommand` and related helper functions if not done in Phase 4
- [ ] Remove `resolveDaemonPodIP` and EndpointSlice lookup from container.go (moved to daemon)
- [ ] Add daemon health check endpoint (`GET /healthz`) for K8s liveness/readiness probes
- [ ] Add Prometheus metrics to daemon: resolve_requests_total, resolve_duration_seconds, peer_fetch_total
- [ ] Handle daemon pod restart: re-scan hostPath, re-populate registry
- [ ] Handle node drain: artifacts on drained node are fetched from peers by consumers on other nodes
- [ ] Run full CI pipeline: build-and-vet, unit-tests, k8s-runtime-tests, k8s-live-tests
- [ ] Phase 7 verification: clean CI, no dead code, observability in place

---
