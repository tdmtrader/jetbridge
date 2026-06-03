# Implementation Plan: Fix hostPath Reuse and Enable Resource Caching

## Phase 1: Clean hostPath on container reuse

Fix the "destination path already exists" bug by ensuring the hostPath directory is empty when a container handle is reused.

- [x] In `Container.Run()`, before creating the pod, add a cleanup step that empties the hostPath steps directory for this handle (`steps/<handle>/`) a66e51eb7
- [x] Implement cleanup as a dedicated init container (`cleanup-stale`) that runs `rm -rf /artifacts/steps/<handle>/*` with the hostPath volume mounted writable a66e51eb7
- [x] Only add the cleanup init container when the DB container was in "created" state (reuse path), not on fresh creation a66e51eb7
- [x] Write test: simulate container reuse (FindOrCreateContainer finds existing handle), verify hostPath is cleaned a66e51eb7
- [x] Write test: fresh container creation does NOT add the cleanup init container a66e51eb7
- [ ] Phase 1 verification: ATC restart mid-build → get step retries successfully

---

## Phase 2: Register volume handle aliases in daemon

Enable the daemon to resolve volume handles by registering them as aliases for disk paths.

- [x] In `recordOutputLocations`, after recording in the ArtifactLocator, call daemon `POST /register` for each output volume with `{key: <volume-handle>, local_path: <disk-path>}` 488a869e0
- [x] Determine the daemon's address: use the node IP from `fetchPodNodeName` + hostPort (same pattern as init containers) 488a869e0
- [x] Add an HTTP client to `execProcess` for calling the daemon's /register endpoint 488a869e0
- [x] Handle registration failures gracefully (log warning, don't fail the build — daemon filesystem fallback still works) 488a869e0
- [x] Write test: after recordOutputLocations, daemon registry contains volume handle alias 488a869e0
- [x] Write test: /resolve with a volume handle key returns the correct artifact data 488a869e0
- [ ] Phase 2 verification: volume handle resolves via daemon after step completes

---

## Phase 3: Enable resource caching

With hostPath cleanup and daemon aliases in place, enable cache hits.

- [x] Change `SkipResourceCache()` to return `false` 2c87d6747
- [x] Verify `buildArtifactInitContainers` graceful fallback works: when locator has no entry, use volume handle as daemon key (already implemented in Phase 5 of daemon track) 2c87d6747
- [x] Write test: cache hit flow end-to-end — get step finds cache, returns volume, downstream task resolves via daemon 8cb6d84cc
- [x] Write test: cache miss flow still works correctly (fresh get, recordOutputLocations, downstream resolve) 8cb6d84cc
- [x] Write test: second build with same resource version hits cache and is faster 8cb6d84cc
- [ ] Phase 3 verification: CI pipeline shows "found existing resource cache" for repeated versions

---

## Phase 4: Edge cases and hardening

- [x] Test: cache hit + ATC restart → downstream step still resolves (daemon alias survives within daemon lifecycle) ca84d2ff1
- [x] Test: cache hit + daemon restart → daemon filesystem scan rediscovers the data ca84d2ff1
- [x] Test: concurrent builds with same resource version — one runs, other hits cache ca84d2ff1
- [x] Verify GC doesn't clean up cached volumes that are still referenced ca84d2ff1
- [x] Remove any remaining workaround comments about SkipResourceCache 8cb6d84cc
- [ ] Phase 4 verification: full CI pipeline green, no regressions

---
