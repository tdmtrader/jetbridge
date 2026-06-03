# Plan: Fix Sidecar Working Directory Bug

## Phase 1: Fix and Test

### Tasks

- [x] Write tests for sidecar working directory inheritance
  - Add test: sidecar inherits main container's dir when `WorkingDir` is empty
  - Add test: sidecar keeps its own `WorkingDir` when explicitly set
  - File: `atc/worker/jetbridge/container_test.go`

- [x] Implement the fix in `buildSidecarContainers()`
  - Update `buildSidecarContainers()` signature to accept `defaultDir string`
  - Apply `defaultDir` when `sc.WorkingDir` is empty
  - Update call site in `buildPod()` to pass `dir` (line 423)
  - File: `atc/worker/jetbridge/container.go`

- [x] Verify existing tests pass
  - Run `ginkgo ./atc/worker/jetbridge/` to confirm no regressions
