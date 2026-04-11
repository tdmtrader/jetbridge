# Spec: Stub Volume StreamOut Panic on Daemon Cache Hit

## Overview (WHY)

When a get step's resource cache is served by the daemon artifact cache, `FindDaemonResourceCache` returns a **stub volume** (`NewStubVolume`) that has no `PodExecutor`. This stub is registered in the build's artifact repository as the artifact for that get step.

Downstream steps that call `StreamFile` on the artifact (e.g., `load_var`, `set_pipeline`, `task_step` reading `metadata.json` or sidecar configs) invoke `StreamOut` on the stub volume. `StreamOut` unconditionally dereferences `v.executor` (which is `nil`), causing a **nil pointer panic** that crashes the build.

The bug is intermittent because it only triggers when:
1. The daemon cache produces a hit (stub volume), AND
2. A downstream step calls `StreamFile`/`StreamOut` on that artifact directly (rather than consuming it as an init-container-fetched input)

Normal task/put inputs that flow through `BuildFetchInitContainers` never call `StreamOut` — the daemon resolves them via its HTTP `/resolve-batch` endpoint in an init container. So most cache hits succeed. The panic only occurs on the `StreamFile` path.

## Affected Code Paths

| Step type | Calls `StreamFile` on artifact? | Panics on stub? |
|-----------|-------------------------------|-----------------|
| `load_var` | Yes — reads file from artifact | **Yes** |
| `set_pipeline` | Yes — reads pipeline YAML from artifact | **Yes** |
| `task_step` (sidecar config) | Yes — reads sidecar JSON from artifact | **Yes** |
| `task_step` (image metadata) | Yes — reads `metadata.json` from image artifact | **Yes** |
| `task_step` (task config file) | Yes — reads task YAML from artifact | **Yes** |
| Normal task/put input | No — uses `BuildFetchInitContainers` | No |

## Call Chain

```
load_var_step.go:138  →  streamer.StreamFile(ctx, art, filePath)
  → streamer.go:24    →  artifact.StreamOut(ctx, path, compression)
    → volume.go:240   →  v.executor.ExecInPod(...)  // PANIC: v.executor is nil
```

## Root Cause

`NewStubVolume` (`volume.go:87-93`) creates a `Volume` with `executor = nil`. The `StreamOut` method (`volume.go:205-261`) has no guard for nil executor — it directly calls `v.executor.ExecInPod()` in a goroutine, causing a nil pointer dereference.

The `HasExecutor()` method exists (`volume.go:129`) but is never checked in `StreamOut` or by callers before calling `StreamOut`.

## Requirements

1. **R1:** `Volume.StreamOut` must not panic when called on a stub volume with no executor. It must return a descriptive error.
2. **R2:** `Volume.StreamIn` must also guard against nil executor (same pattern, `volume.go:190`).
3. **R3:** When a daemon cache hit produces a stub volume and a downstream step needs to `StreamFile` from it, the system must either:
   - (a) Stream the file via the daemon's HTTP endpoint (preferred — `DaemonSetVolume.StreamOut` already works), OR
   - (b) Return a clear error indicating the artifact is not directly streamable from a daemon cache stub.
4. **R4:** Tests must cover the nil-executor guard in `StreamOut` and `StreamIn`.
5. **R5:** Tests must cover the `StreamFile` → daemon-cached-artifact path end-to-end.

## Acceptance Criteria

- [ ] Calling `StreamOut` on a stub volume returns an error, not a panic.
- [ ] Calling `StreamIn` on a stub volume returns an error, not a panic.
- [ ] `load_var` step reading from a daemon-cached get step works correctly (either via daemon streaming or clear error).
- [ ] `set_pipeline` step reading from a daemon-cached get step works correctly.
- [ ] `task_step` reading image `metadata.json` from a daemon-cached get step works correctly.
- [ ] All existing unit tests pass.
- [ ] New tests cover the stub volume guard paths.

## Out of Scope

- Refactoring the artifact repository to distinguish streamable vs non-streamable artifacts at the type level (good future work but separate track).
- Changing how normal task/put inputs consume daemon-cached artifacts (the init container path works fine).
- Performance optimization of daemon cache lookups.

## Technical Approach

### Option A: Nil-executor guard + DaemonSetVolume upgrade (Recommended)

1. Add nil-executor guards in `StreamOut` and `StreamIn` that return errors instead of panicking.
2. When `FindDaemonResourceCache` gets a hit, return a `DaemonSetVolume` (which can stream via HTTP) instead of a bare `NewStubVolume`. The `DaemonSetVolume` already has a working `StreamOut` implementation that fetches via HTTP from the daemon pod. This requires knowing the daemon IP at cache-hit time (which `FindDaemonResourceCache` already has — it's in `daemonIP`).
3. This makes both the init-container path AND the direct `StreamFile` path work correctly for daemon cache hits.

### Option B: Nil-executor guard only (Minimal)

1. Add nil-executor guards in `StreamOut` and `StreamIn`.
2. Accept that `StreamFile` on daemon-cached artifacts will return an error, which propagates as a build error.
3. This prevents the panic but doesn't fix the underlying functionality gap.

**Recommendation:** Option A — it fully solves the user-facing bug (cached get steps work correctly with `load_var`, `set_pipeline`, etc.) rather than just converting a panic to an error.
