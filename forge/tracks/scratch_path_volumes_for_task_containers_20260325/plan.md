# Implementation Plan: Scratch Path Volumes for Task Containers

## Phase 1: Task Config & Runtime Types (TDD)

- [x] Task: Write tests for TaskScratchConfig parsing and validation in `atc/task_test.go` — valid scratch_paths parsed, empty path rejected, round-trip serialization
- [x] Task: Add `ScratchPaths []TaskScratchConfig` to `TaskConfig` and `TaskScratchConfig` type in `atc/task.go` to make tests pass
- [x] Task: Add `ScratchPaths []string` to `ContainerSpec` in `atc/runtime/types.go`
- [x] Task: Implement wiring in `atc/exec/task_step.go` containerSpec() (parallel to caches mapping at ~line 534)

## Phase 2: K8s Runtime Volume Creation (TDD)

- [x] Task: Write unit tests for scratch volume creation in `atc/worker/jetbridge/container_test.go` — verify emptyDir volumes, mount paths, relative path resolution, and no cache entry contamination
- [x] Task: Implement scratch volume creation in `buildVolumeMounts()` in `atc/worker/jetbridge/container.go` — plain emptyDir volumes with relative path resolution matching cache behavior

## Phase 3: Behavioral / Integration Tests

- [x] Task: Add testflight behavioral test in `testflight/scratch_paths_test.go` — pipeline with a task declaring scratch_paths, verify the path is writable and ephemeral (not preserved across tasks in same build)
- [x] Task: Add K8s integration test in `topgun/k8s/integration/task_advanced_test.go` — inline pipeline with scratch_paths, verify scratch is ephemeral across builds

## Phase 4: Verification

- [x] Task: Run `ginkgo ./atc/` — 178/178 passed, 0 failures
- [x] Task: Run `ginkgo ./atc/worker/jetbridge/` — 343/343 passed, 0 failures
- [x] Task: `go vet ./testflight/ ./topgun/k8s/integration/` — clean
- [ ] Task: Phase 4 Manual Verification — deploy and confirm buildkitd overlayfs works with scratch_paths config

---
