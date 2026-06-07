# Spec: JetBridge Storage & Artifact Behavioral Specification

**Track ID:** `jetbridge_storage_behavioral_spec_20260330`
**Type:** docs

## Overview

The JetBridge storage and artifact layer has accumulated bugs during the transition from in-memory artifact locator to daemon-mediated artifact resolution. Root cause: behavioral contracts were never formally specified, so refactoring had no safety net beyond integration tests that covered happy paths.

This track produces a comprehensive behavioral specification for the storage/artifact subsystem, covering every observable contract from the artifact daemon HTTP API through volume streaming to container orchestration. Each requirement is tagged with a unique ID for direct traceability to test cases.

## Why

- Repeated bugs during storage layer refactoring (path mismatches, missing registrations, silent failures)
- No specification means no way to know what tests are missing
- Daemon-mediated artifact resolution (Phases 1-5) is implemented but Phase 6 (resource caching) and Phase 7 (hardening) remain — the spec prevents further regressions
- Future storage optimizations (reflink, ZFS, bind mounts) need a contract to implement against

## Scope

**In scope:**
- Artifact daemon HTTP API (all 7 endpoints)
- Artifact registry, alias store, persistence
- Peer discovery and cross-node artifact resolution
- Volume types (DeferredVolume, DaemonSetVolume, StubVolume)
- Volume streaming (StreamIn/StreamOut)
- Container artifact orchestration (init containers, volume mounts, output recording)
- Volume mount overlap/dedup logic
- Resource caching flow (target state: SkipResourceCache=false)
- Scheduling affinity for artifact co-location
- TTL sweeper and artifact lifecycle
- Crash recovery (ATC restart, daemon restart, pod restart)

**Out of scope:**
- Pod execution (process.go, executor.go) — well tested
- Worker registration/reaping — separate concern
- Exec step logic (get/put/task steps) — well tested at 185+ cases
- Database container/volume state machines — separate layer
- Image resolution — separate concern

---

## Section 1: Artifact Daemon HTTP API

### DA-01: GET /artifacts/<key> — Directory artifact
When the artifact at `<storagePath>/steps/<key>` is a directory, the daemon MUST:
- Return HTTP 200 with `Content-Type: application/x-tar`
- Stream a tar archive of the directory contents on-the-fly
- Preserve symlinks as tar TypeSymlink entries (zero size, linkname set)
- Include regular files with correct headers (size, mode, name)
- Skip directory entries in the tar stream (only files and symlinks)

### DA-02: GET /artifacts/<key> — File artifact (legacy tar)
When the artifact at `<storagePath>/steps/<key>` is a regular file, the daemon MUST:
- Return HTTP 200 with `Content-Type: application/octet-stream`
- Serve the file contents directly (no re-tarring)

### DA-03: GET /artifacts/<key> — Not found
When no artifact exists at the resolved path, the daemon MUST return HTTP 404.

### DA-04: PUT /artifacts/<key>
The daemon MUST:
- Create parent directories (mode 0755) if they don't exist
- Stream the request body to `<storagePath>/steps/<key>` (file, not directory)
- Return HTTP 201 on success
- Return HTTP 500 on mkdir or write failure

### DA-05: DELETE /artifacts/<key>
The daemon MUST:
- Remove the artifact path recursively (directory or file)
- Return HTTP 204 on success
- Return HTTP 500 if removal fails

### DA-06: HEAD /artifacts/<key>
The daemon MUST:
- Return HTTP 200 if the artifact path exists (no body)
- Return HTTP 404 if the artifact path does not exist
- Return HTTP 500 on stat errors

### DA-07: POST /register — Artifact alias registration
Request: `{key: string, local_path: string}`
The daemon MUST:
- Return HTTP 400 if `key` or `local_path` is empty
- Return HTTP 404 if `local_path` does not exist on disk (stat check)
- Register the alias in both the in-memory registry and alias persistence store
- Return HTTP 201 on success

### DA-08: POST /resolve — Local artifact resolution
Request: `{key: string, dest: string}`
The daemon MUST:
- Return HTTP 400 if `key` or `dest` is empty
- Attempt resolution in this order:
  1. Registry alias lookup (`registry.Lookup(key)`)
  2. Filesystem fallback (`<storagePath>/steps/<key>` directory check)
  3. Peer discovery (if PeerResolver configured)
- On first successful resolution, copy artifact to `dest` via `cp -a src/. dest/.`
- Return HTTP 200 with `{status: "ok", source: "<path>", method: "local|filesystem_fallback|peer", duration: "<ms>"}`
- Return HTTP 404 with `{status: "not_found"}` if all resolution methods fail
- Return HTTP 500 with `{status: "error", error: "<msg>"}` on copy/fetch failure

### DA-09: POST /resolve — Filesystem fallback auto-registration
When an artifact is found via filesystem fallback (`<storagePath>/steps/<key>` exists as directory), the daemon MUST auto-register it in the in-memory registry so subsequent lookups use the fast path.

### DA-10: POST /resolve — Structured logging
Every /resolve call MUST produce a structured log entry containing: key, dest, resolution method (local/filesystem_fallback/peer), source path or peer IP, duration, and error (if any).

### DA-11: GET /healthz
The daemon MUST return HTTP 200 with body "ok" for liveness/readiness probes.

---

## Section 2: Artifact Registry & Alias Store

### AR-01: Two-map design
The registry maintains two maps:
- `entries`: all known artifacts (aliases + scan results). Used for lookup.
- `aliases`: subset explicitly registered via POST /register. Persisted to disk.

A `Register(key, path)` call adds to `entries` only. A `RegisterAlias(key, path)` call adds to both `entries` and `aliases` and triggers persistence.

### AR-02: Thread safety
All registry operations (Register, RegisterAlias, Lookup, Remove, RemoveByPath) MUST be thread-safe via RWMutex. Read operations (Lookup, Len, Keys) use read locks; write operations use write locks.

### AR-03: Startup filesystem scan
On daemon startup, `ScanHostPath(storagePath)` MUST:
- List `<storagePath>/steps/` directory
- For each `<handle>/` subdirectory, list its children
- Register each child as `<handle>/<output>` → `<stepsDir>/<handle>/<output>`
- If steps directory doesn't exist: log and return nil (not an error)
- If a per-handle ReadDir fails: log error and continue (don't abort scan)

### AR-04: Alias persistence (atomic writes)
AliasStore.Save() MUST:
- Marshal aliases to indented JSON
- Write to a temp file (`<path>.tmp`)
- Rename temp to final path (atomic on POSIX)
- Clean up temp file on rename failure

### AR-05: Alias loading with stale entry filtering
AliasStore.Load() MUST:
- Return empty map (not error) if the aliases file doesn't exist
- For each loaded entry, stat the local_path
- Discard entries where the path no longer exists
- Return only valid entries

### AR-06: Non-fatal persistence
Registry.persistAliases() errors MUST be logged but NOT propagated. Alias persistence is best-effort — the daemon can recover from filesystem scan on restart.

### AR-07: Remove and RemoveByPath
- `Remove(key)` deletes from both `entries` and `aliases`, persists if alias removed
- `RemoveByPath(dirPath)` scans all entries; removes those with path prefix matching `dirPath`; persists once if any aliases removed

---

## Section 3: Peer Discovery & Cross-Node Resolution

### PD-01: Peer IP discovery
PeerResolver.peerIPs() MUST:
- Query `discovery.k8s.io/v1` EndpointSlices filtered by service label
- Extract IP addresses from all endpoints
- Exclude the local pod's IP (myPodIP)
- Return nil (not error) on K8s API failures (graceful degradation)
- Return nil if clientset is nil (non-K8s environment)

### PD-02: Sequential peer probe
PeerResolver.Probe() MUST:
- Send `HEAD /artifacts/steps/<key>` to each peer sequentially
- Return the IP of the first peer that responds with HTTP 200
- Return ("", false) if no peer responds with 200
- Log unreachable peers at Debug level (not Error)

### PD-03: Peer fetch with retry
PeerResolver.Fetch() MUST:
- Send `GET /artifacts/steps/<key>` to the specified peer
- Retry up to 3 times with exponential backoff (1s, 2s between attempts)
- Extract tar stream to destination directory
- Return error after 3 failed attempts
- NOT retry request creation errors (return immediately)

### PD-04: Tar extraction path traversal defense
extractTarToDir() MUST:
- Check `filepath.Rel(destDir, target)` for each entry
- Skip entries with ".." prefix or resolution errors
- Preserve symlinks (TypeSymlink → os.Symlink)
- Create parent directories for files as needed
- Handle TypeDir, TypeReg, and TypeSymlink; skip other types

### PD-05: Self-exclusion
The PeerResolver MUST never probe or fetch from itself (myPodIP filtering).

---

## Section 4: Volume Types & Streaming

### VT-01: DeferredVolume creation and binding
- Created with `NewDeferredVolume(handle, workerName, executor, namespace, containerName, mountPath)`
- Pod name is NOT set at creation time
- `SetPodName(podName)` MUST be called before StreamIn/StreamOut
- After SetPodName, StreamIn and StreamOut execute tar commands via SPDY exec in the pod

### VT-02: DeferredVolume StreamIn
StreamIn MUST:
- Decompress the incoming stream if compression is non-nil and not RawEncoding
- Execute `tar xf - -C <resolvedPath>` in the pod via SPDY exec
- Pipe the (decompressed) reader to stdin of the tar command
- Return error on decompressor creation failure or exec failure

### VT-03: DeferredVolume StreamOut
StreamOut MUST:
- Execute `tar cf - -C <mountPath> .` (for path "." or "") or `tar cf - -C <mountPath> <path>` in the pod
- Run exec in a background goroutine writing to a pipe
- Compress the output if compression is requested
- Close the compressor before closing the pipe writer (flush data)
- Return the pipe reader immediately (caller reads asynchronously)
- Propagate exec errors to the reader via pw.CloseWithError

### VT-04: DeferredVolume path resolution
- Path "." and "" MUST both resolve to the volume's mountPath
- Other paths MUST resolve to `filepath.Join(mountPath, path)`

### VT-05: StubVolume limitations
- Created with `NewStubVolume(handle, workerName, mountPath)`
- Has no executor and no dbVolume
- StreamIn and StreamOut MUST fail (no exec capability)
- InitializeResourceCache/TaskCache MUST return nil (no-op)
- Used only as a placeholder for resource cache tracking

### VT-06: DaemonSetVolume StreamOut (HTTP fetch)
StreamOut MUST:
- Validate sourceNode is not empty (return error with key context if empty)
- Resolve node IP via NodeIPResolver
- Send GET to `http://<nodeIP>:<port>/artifacts/<key>`
- Retry up to 3 times with 2-second fixed backoff between attempts
- Return HTTP response body as the reader (caller must close)
- Return specific error for HTTP 404 (mentioning sourceNode and key)
- Return error with status code for other non-200 responses

### VT-07: DaemonSetVolume StreamIn rejection
StreamIn MUST always return an error ("use hostPath writes"). DaemonSetVolume is read-only.

### VT-08: DaemonSetVolume compression behavior
DaemonSetVolume does NOT handle compression (unlike DeferredVolume). The compression parameter is ignored — data is served as-is from the daemon.

### VT-09: Volume cache initialization delegation
All volume types that have a non-nil dbVolume MUST delegate InitializeResourceCache, InitializeStreamedResourceCache, and InitializeTaskCache to the underlying dbVolume. If dbVolume is nil, these methods MUST return nil (no-op).

### VT-10: Volume Handle identity
- DeferredVolume: `Handle()` returns `dbVolume.Handle()` if dbVolume exists, else internal handle
- DaemonSetVolume: `Handle()` returns the handle passed at construction
- StubVolume: `Handle()` returns the handle passed at construction

---

## Section 5: Container Artifact Orchestration

### CO-01: Init container generation (daemon resolve)
`buildArtifactInitContainers()` MUST:
- Return nil if ArtifactDaemonHostPath is not configured
- Create one init container per input that has an Artifact
- Each init container runs `daemonResolveCommand(key, hostDest)`
- The command POSTs to `http://${HOST_IP}:${PORT}/resolve` with `{key, dest}` JSON body
- HOST_IP comes from K8s downward API (status.hostIP)
- PORT comes from config.ArtifactDaemonPort (default 7780)

### CO-02: Init container artifact key resolution
For each input artifact:
- Compute key via `ArtifactKey(artifact.Handle())`
- Look up in ArtifactLocator: if found, use `loc.HostDir` as the daemon key
- If not found in locator, use the computed key as fallback (daemon filesystem scan will find it)

### CO-03: Cleanup init container
`buildCleanupInitContainer()` MUST:
- Return nil if container is NOT being reused (fresh creation)
- Return nil if ArtifactDaemonHostPath is not configured
- Return nil if container type is Check
- Otherwise, create init container that runs:
  - `rm -rf <hostPath>/steps/<handle>` (remove stale data)
  - `mkdir -p <hostPath>/steps/<handle>` (recreate empty dir)

### CO-04: Volume mount construction
`buildVolumeMounts()` MUST create volumes and mounts for:
- **Dir**: one volume at the container's working directory
- **Inputs**: one volume per input, mounted at `DestinationPath`
- **Outputs**: one volume per output, BUT skip outputs that overlap with input paths (dedup)
- **Caches**: one volume per cache path
- **Scratch paths**: one volume per scratch path (always emptyDir)

### CO-05: Input/output volume overlap dedup
When an input path and output path are the same:
- Only ONE volume is created (the input volume)
- The hostPath subdirectory is named after the OUTPUT name (not input name)
- This ensures the daemon key (based on hostPath subdirectory) matches what `recordOutputLocations` registers

### CO-06: Volume storage backend selection
- If ArtifactDaemonHostPath is configured: dir, input, and output volumes use hostPath at `<daemonPath>/steps/<handle>/<subdir>/`
- If ArtifactDaemonHostPath is NOT configured: dir, input, and output volumes use emptyDir

### CO-07: Cache volume storage selection
Cache volumes MUST use:
- Explicit `config.CacheStore` if set ("hostpath" or "emptydir")
- Auto-detect: HostPath if ArtifactDaemonHostPath configured AND caches > 0; else EmptyDir
- HostPath caches use stable keys: `stableCacheKey(jobID, stepName, cachePath)` for deterministic naming across builds
- EmptyDir caches are ephemeral (lost on pod termination)

### CO-08: Scratch path volumes
Scratch paths MUST always use emptyDir volumes. They are ephemeral by design and MUST NOT persist across pod restarts.

### CO-09: Output recording and daemon registration
After a step completes, `recordOutputLocations()` MUST:
- For each output volume, calculate the daemon key as `<handle>/<output-subdir>`
- Record in ArtifactLocator: `key → (nodeName, daemonKey)` for scheduling affinity
- Call daemon `POST /register` with `{key: volumeHandle, local_path: <hostPath>}` to register the alias
- Registration errors MUST be logged but NOT fail the build

### CO-10: Scheduling affinity
`buildAffinity()` MUST:
- Return nil if ArtifactDaemonHostPath is not configured
- Set hard affinity: node MUST have label `concourse.dev/artifact-cache=ready`
- Set soft affinity (weight 100): prefer the node with the most input artifacts (via `preferredInputNode()`)
- `preferredInputNode()` counts artifacts per node using ArtifactLocator lookups and returns the node with highest count

### CO-11: Volume naming convention
Volumes MUST be named with sequential indices:
- `dir-0`, `input-0`, `input-1`, ..., `output-0`, ..., `cache-0`, ..., `scratch-0`, ...
- `volumeNameForMountPath()` maps mount paths back to volume names (used by init container generation)

### CO-12: Relative path resolution
- Cache paths that are relative MUST be resolved against the container's Dir
- Scratch paths that are relative MUST be resolved against the container's Dir
- Output paths are normalized via `filepath.Clean`

---

## Section 6: Resource Caching Flow (Target State)

### RC-01: SkipResourceCache returns false
`Worker.SkipResourceCache()` MUST return `false`. This enables the get step to check for cached resource versions before executing the resource script.

### RC-02: Cached volume lookup
When a get step finds a cached resource version:
- The step calls `pool.FindResourceCacheVolumeOnWorker()` or `worker.LookupVolume(handle)`
- The worker returns a DaemonSetVolume wrapping the cached volume's DB record
- The DaemonSetVolume's source node comes from ArtifactLocator (if available)

### RC-03: Cache hit short-circuit
When a cache hit occurs:
- The get step MUST NOT create a pod or execute the resource script
- The cached volume is registered in the artifact repository with `fromCache=true`
- Downstream steps receive the cached artifact via the repository

### RC-04: Daemon serving cached volumes
When a downstream step's init container calls `/resolve` for a cached artifact:
- The daemon looks up the key in its alias registry (registered via POST /register after original get)
- If the alias maps to a valid disk path, the daemon copies it to the destination
- If the alias is missing (e.g., daemon restarted), the filesystem scan at startup recovers it

### RC-05: Cache invalidation
Cached volumes are subject to:
- Database-driven GC (volume/container lifecycle)
- TTL sweeper cleanup (if the hostPath data ages out)
- The daemon MUST NOT serve artifacts whose disk path no longer exists

---

## Section 7: Lifecycle & Resilience

### LR-01: TTL sweeper
The sweeper MUST:
- Run on a configurable interval
- Sweep `<storagePath>/steps/` directories: remove those with ModTime before (now - TTL)
- Sweep `<storagePath>/artifacts/` files (legacy): remove those with ModTime before cutoff
- NOT sweep `<storagePath>/caches/` (database-driven GC only)
- Call `registry.RemoveByPath()` after removing a directory to clean registry entries
- Log errors but continue sweeping (graceful degradation)

### LR-02: Daemon restart recovery
On daemon restart, the daemon MUST:
- Scan hostPath to re-populate the in-memory registry (AR-03)
- Load persisted aliases from aliases.json with stale filtering (AR-05)
- Re-label the K8s node with `concourse.dev/artifact-cache=ready`
- Resume serving /resolve requests

### LR-03: ATC restart mid-pipeline
When the ATC restarts during a pipeline execution:
- The in-memory ArtifactLocator is lost
- Downstream steps' init containers MUST still resolve artifacts via the daemon
- The daemon's /resolve endpoint uses filesystem fallback (DA-09) to find artifacts not in the registry
- Builds MUST NOT fail due to lost ArtifactLocator entries

### LR-04: Container reuse (crash recovery)
When a container handle is reused after a crash:
- The cleanup init container (CO-03) removes stale hostPath data
- A fresh directory is created for the new pod
- Volume handles from the DB are still valid for lookups

### LR-05: Node drain handling
When a node is drained:
- Artifacts on the drained node are NOT migrated proactively
- Consumer pods on other nodes resolve artifacts via peer discovery (PD-02, PD-03)
- The daemon on the remaining peer serves the artifact via GET

### LR-06: Daemon pod crash during fetch
If the artifact daemon pod crashes while serving a /resolve or GET request:
- The init container receives a network error
- The build fails (no retry at init container level — daemon handles internal retries)
- The daemon recovers via restart (LR-02) and subsequent builds succeed

---

## Acceptance Criteria

- [ ] Every requirement (DA-01 through LR-06) has at least one corresponding test case
- [ ] All test cases pass with `go test ./atc/worker/jetbridge/... ./cmd/artifact-daemon/...`
- [ ] Test cases cover both happy paths and error/edge cases for each requirement
- [ ] Resource caching flow (RC-01 through RC-05) is validated end-to-end
- [ ] Cross-node resolution (PD-01 through PD-05) is validated with mocked peers
- [ ] No regressions in existing test suites

## Out of Scope

- Writing the actual test code (separate implementation track)
- Pod execution, process management, SPDY executor internals
- Worker registration and reaping
- Database container/volume state machines
- Image resolution logic
- Helm chart or DaemonSet K8s manifest changes
- Reflink/ZFS/btrfs copy optimizations
