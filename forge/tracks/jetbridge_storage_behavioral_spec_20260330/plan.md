# Implementation Plan: JetBridge Storage & Artifact Behavioral Specification

## Phase 1: Artifact Daemon HTTP API Tests

Write test cases for every daemon endpoint (DA-01 through DA-11).

- [ ] Write tests for GET /artifacts/<key> — directory artifact tarring (DA-01)
- [ ] Write tests for GET /artifacts/<key> — file artifact serving (DA-02)
- [ ] Write tests for GET /artifacts/<key> — not found (DA-03)
- [ ] Write tests for PUT /artifacts/<key> — file upload and parent dir creation (DA-04)
- [ ] Write tests for DELETE /artifacts/<key> — recursive removal (DA-05)
- [ ] Write tests for HEAD /artifacts/<key> — existence check (DA-06)
- [ ] Write tests for POST /register — validation, stat check, alias registration (DA-07)
- [ ] Write tests for POST /resolve — local alias, filesystem fallback, auto-registration (DA-08, DA-09)
- [ ] Write tests for POST /resolve — structured logging (DA-10)
- [ ] Write tests for GET /healthz (DA-11)
- [ ] Phase 1 verification: all daemon endpoint behaviors specified and tested

---

## Phase 2: Registry & Alias Store Tests

Write test cases for registry operations and alias persistence (AR-01 through AR-07).

- [ ] Write tests for two-map design: Register vs RegisterAlias behavior (AR-01)
- [ ] Write tests for thread safety: concurrent Register/Lookup/Remove (AR-02)
- [ ] Write tests for startup filesystem scan: normal, empty, missing steps dir, partial failures (AR-03)
- [ ] Write tests for alias persistence: atomic writes, temp file cleanup on failure (AR-04)
- [ ] Write tests for alias loading: missing file, stale entry filtering, valid entries (AR-05)
- [ ] Write tests for non-fatal persistence: persistAliases errors logged not propagated (AR-06)
- [ ] Write tests for Remove and RemoveByPath: alias cleanup, persistence trigger (AR-07)
- [ ] Phase 2 verification: all registry and alias store behaviors specified and tested

---

## Phase 3: Peer Discovery & Cross-Node Resolution Tests

Write test cases for peer discovery, probe, fetch, and tar extraction (PD-01 through PD-05).

- [ ] Write tests for peer IP discovery: EndpointSlice query, self-exclusion, API failures (PD-01)
- [ ] Write tests for sequential peer probe: first 200 wins, unreachable peers logged (PD-02)
- [ ] Write tests for peer fetch with retry: 3 attempts, exponential backoff, error propagation (PD-03)
- [ ] Write tests for tar extraction path traversal defense: ".." rejection, symlink preservation (PD-04)
- [ ] Write tests for self-exclusion: myPodIP never probed (PD-05)
- [ ] Write integration test: POST /resolve with peer fallback (mock peer HTTP server) (DA-08 + PD-*)
- [ ] Phase 3 verification: all peer discovery behaviors specified and tested

---

## Phase 4: Volume Types & Streaming Tests

Write test cases for DeferredVolume, DaemonSetVolume, StubVolume streaming (VT-01 through VT-10).

- [ ] Write tests for DeferredVolume creation and pod binding (VT-01)
- [ ] Write tests for DeferredVolume StreamIn: decompression, tar exec, error cases (VT-02)
- [ ] Write tests for DeferredVolume StreamOut: goroutine pipe, compression, error propagation (VT-03)
- [ ] Write tests for path resolution: "." and "" → mountPath, relative paths (VT-04)
- [ ] Write tests for StubVolume limitations: no StreamIn/Out, no-op cache init (VT-05)
- [ ] Write tests for DaemonSetVolume StreamOut: HTTP fetch, retry, 404 handling, timeout (VT-06)
- [ ] Write tests for DaemonSetVolume StreamIn rejection (VT-07)
- [ ] Write tests for DaemonSetVolume compression behavior (ignored) (VT-08)
- [ ] Write tests for cache initialization delegation across volume types (VT-09)
- [ ] Write tests for volume handle identity across types (VT-10)
- [ ] Phase 4 verification: all volume streaming behaviors specified and tested

---

## Phase 5: Container Artifact Orchestration Tests

Write test cases for init containers, volume mounts, output recording, and affinity (CO-01 through CO-12).

- [ ] Write tests for init container generation: daemon resolve command, HOST_IP, PORT (CO-01)
- [ ] Write tests for artifact key resolution: locator hit, locator miss fallback (CO-02)
- [ ] Write tests for cleanup init container: reused container, fresh container, check type skip (CO-03)
- [ ] Write tests for volume mount construction: dir, inputs, outputs, caches, scratch (CO-04)
- [ ] Write tests for input/output overlap dedup: shared volume, output-named subdir (CO-05)
- [ ] Write tests for volume storage backend selection: hostPath vs emptyDir (CO-06)
- [ ] Write tests for cache volume storage: explicit config, auto-detect, stable keys (CO-07)
- [ ] Write tests for scratch path volumes: always emptyDir (CO-08)
- [ ] Write tests for output recording and daemon registration: locator + POST /register (CO-09)
- [ ] Write tests for scheduling affinity: hard label, soft node preference (CO-10)
- [ ] Write tests for volume naming convention: sequential indices (CO-11)
- [ ] Write tests for relative path resolution against Dir (CO-12)
- [ ] Phase 5 verification: all container orchestration behaviors specified and tested

---

## Phase 6: Resource Caching Flow Tests

Write test cases for the target-state resource caching flow (RC-01 through RC-05).

- [ ] Write tests for SkipResourceCache returning false (RC-01)
- [ ] Write tests for cached volume lookup: DaemonSetVolume wrapping, source node resolution (RC-02)
- [ ] Write tests for cache hit short-circuit: no pod, no exec, artifact registered with fromCache=true (RC-03)
- [ ] Write tests for daemon serving cached volumes: alias lookup, filesystem fallback (RC-04)
- [ ] Write tests for cache invalidation: GC, TTL sweeper, missing disk path (RC-05)
- [ ] Phase 6 verification: resource caching end-to-end flow specified and tested

---

## Phase 7: Lifecycle & Resilience Tests

Write test cases for sweeper, restart recovery, and crash scenarios (LR-01 through LR-06).

- [ ] Write tests for TTL sweeper: steps cleanup, artifacts cleanup, caches excluded, registry sync (LR-01)
- [ ] Write tests for daemon restart recovery: scan, alias load, node labeling (LR-02)
- [ ] Write tests for ATC restart mid-pipeline: locator lost, daemon filesystem fallback resolves (LR-03)
- [ ] Write tests for container reuse crash recovery: cleanup init, fresh dir (LR-04)
- [ ] Write tests for node drain: peer discovery serves artifacts from remaining nodes (LR-05)
- [ ] Write tests for daemon crash during fetch: init container failure, daemon recovery (LR-06)
- [ ] Phase 7 verification: all lifecycle and resilience behaviors specified and tested

---

## Phase 8: Gap Analysis & Coverage Report

Review spec against existing tests and produce a coverage matrix.

- [ ] Map each requirement ID to existing test files/cases (if any)
- [ ] Identify requirements with zero test coverage
- [ ] Identify requirements with partial coverage (happy path only)
- [ ] Produce coverage matrix: requirement ID × test status (covered/partial/missing)
- [ ] Prioritize missing tests by risk (daemon resolution > caching > lifecycle)
- [ ] Phase 8 verification: coverage matrix complete, priorities documented

---
