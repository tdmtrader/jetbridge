# Conductor Growth Experience (CGX)

**Track:** `daemonset_artifact_cache_20260326`
**Purpose:** Log observations during implementation for continuous improvement analysis.

---

## Frustrations & Friction

<!-- Log moments of frustration, confusion, or repeated attempts -->

---

## Patterns Observed

### Good Patterns (to encode)

- [2026-03-26] The artifact helper optimization track (prerequisite) revealed key codebase facts that inform this track's design. Document these here for future reference.

### Anti-Patterns (to prevent)

- [2026-03-26] When filtering uploads by `containerSpec.Outputs`, get/put steps were missed because they use `Dir` as their implicit output (no explicit `Outputs` declared). The fix checks `metadata.Type` — get/put steps upload Dir, task/check steps don't. Any new upload filtering logic in the DaemonSet track must account for this same distinction. See commit `ad434e07f`.

---

## Missing Capabilities

<!-- Tools, commands, or features that would have helped -->

---

## Insights & Suggestions

### Key codebase facts for implementors

**Volume types per step:**
- `buildVolumeMountsForSpec` (worker.go:116) creates `*Volume` objects for Dir, every Input, every Output, and every Cache. These go into `container.volumes`.
- `buildVolumeMounts` (container.go) creates K8s pod volumes/mounts and also populates `cacheEntries` (only in `CacheStoreArtifact` mode).
- Both lists must be considered when deciding what to upload/mount.

**Output filtering (implemented in optimization track):**
- `container.outputPaths()` returns the set of paths to upload. For task steps: `containerSpec.Outputs`. For get/put steps: `containerSpec.Dir` (implicit output). For check steps: empty (no artifacts).
- The `fakeExecExecutor` in `volume_test.go` is thread-safe (mutex added in optimization track) — needed for concurrent upload tests.

**Upload flow (current, post-optimization):**
- `uploadOutputsToArtifactStore` → filters to output paths → parallel `errgroup` → calls `uploadArtifact()` per volume
- `uploadArtifact()` runs a two-phase shell script (tar to /tmp, mv to PVC), captures file count/size/timings, records span attributes and OTel metrics
- `uploadCachesToArtifactStore` → only runs for `CacheStoreArtifact` mode → parallel `sync.WaitGroup`

**StreamOut callers:**
- `Streamer.StreamFile` (worker/streamer.go) — used by `set_pipeline`, `load_var`, `file:` directives
- `artifactserver/get.go` — HTTP API for artifact downloads
- These currently exec tar in the artifact-helper sidecar. DaemonSet mode replaces with HTTP GET.

**Reaper GC flow:**
- `Reaper.Run` (reaper.go:62) lists pods, marks missing containers, destroys stale ones
- `cleanupArtifactStoreEntries` (reaper.go:235) removes artifact tars from PVC for destroyed containers — this is the hook for DaemonSet HTTP DELETE cleanup
- `cleanupCacheVolumes` (reaper.go:185) handles PVC cache subdirectory cleanup

**Node scheduling:**
- Pod spec is built in `buildPod` (container.go:342). Affinity rules go in `pod.Spec.Affinity`.
- The pod's scheduled node is available from `pod.Spec.NodeName` after scheduling (used by `podEventTracker` for `pod.scheduled` event).

**Spot node behavior:**
- hostPath data is lost on spot preemption (node VM is deleted)
- Caches lost = cold start on next build (acceptable per spec)
- Artifacts lost = build fails, retries from producing step (same as PVC loss)

---

## Improvement Candidates

<!-- Concrete suggestions for new/modified extensions -->
