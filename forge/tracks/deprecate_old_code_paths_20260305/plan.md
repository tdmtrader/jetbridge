# Implementation Plan: Deprecate Old Code Paths

## Phase 1: Dead Code Removal

- [x] Remove dead `StreamingVolume` and `WaitingForStreamedVolume` methods from `BuildStepDelegate` interface (`atc/exec/build_step_delegate.go`), implementations (`atc/engine/build_step_delegate.go`), and all fakes/mocks
- [x] Remove corresponding `event.StreamingVolume` and `event.WaitingForStreamedVolume` event types if unused
- [x] Remove deprecated `LidarScannerInterval` and `Syslog.DrainInterval` config flags from `atc/atccmd/command.go`
- [x] Remove worker randomization/shuffling in `pool.go` (lines ~117-122) — meaningless with single K8s worker
- [x] Task: Phase 1 Manual Verification

---

## Phase 2: Simplify Worker Selection

- [x] Inline or remove `Factory` interface in `atc/worker/factory.go` — replace with direct K8s worker construction
- [x] Simplify `FindOrSelectWorker` in `atc/worker/pool.go` — remove polling loop, fail fast if worker unavailable
- [x] Remove `isWorkerCompatibleAndRunning` platform/tags/resource-type filtering (`pool.go` lines ~377-417) — always passes for K8s
- [x] Simplify `isWorkerVersionCompatible` — remove semver comparison logic (`pool.go` lines ~345-375), just check version is not nil
- [x] Collapse `EnableGlobalResources` branching in `pool.go` (lines ~188-214) — always use global scope
- [x] Simplify `worker.Spec` in `atc/worker/spec.go` — remove `Platform`, `Tags`, `ResourceType` fields that are always satisfied; update all callers in `atc/exec/` steps
- [x] Update tests for all pool.go changes — remove tests for multi-worker selection, add tests for fast-fail behavior
- [x] Task: Phase 2 Manual Verification

---

## Phase 3: Simplify Streaming & Volume Locality

- [x] Inline `Streamer` abstraction (`atc/worker/streamer.go`) — move `streamThroughATC` logic directly into pool or remove separate type
- [x] Remove `FindResourceCacheVolume` cross-worker volume locality logic in `pool.go` — no cross-worker cache optimization needed
- [x] Remove `FindResourceCacheVolumeOnWorker` and related worker-iteration logic
- [x] Update tests for streaming and volume lookup simplification
- [x] Task: Phase 3 Manual Verification

---

## Phase 4: Simplify GC Model

- [x] Simplify container collector two-phase destruction — K8s Reaper deletes pods directly, reduce DB staging overhead in `atc/gc/container_collector.go`
- [x] Simplify check container pooling — remove or increase `maxCheckContainersPerResource` constant, pods are cheap in K8s
- [x] Make GC K8s-aware: handle `creating`-state containers that failed (detect via K8s API in Reaper rather than relying solely on `markContainerAsFailed`)
- [x] Update GC tests for simplified model
- [x] Task: Phase 4 Manual Verification

---
