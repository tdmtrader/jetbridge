# Plan: Fix Input/Output Volume Shadowing When Names Match

## Phase 1: Reproduce and Test

- [x] Write tests for same-name input/output volume sharing
  - Test in `container_test.go`: task with input and output at the same path produces a
    single volume and single mount (not two separate mounts)
  - Test: the shared mount uses the input's volume (so init container populates it)
  - Test: non-overlapping inputs and outputs are unaffected (regression)

- [x] Implement volume deduplication in buildVolumeMounts
  - In `buildVolumeMounts()`: after processing inputs, build a map of mountPath → bool
  - When processing outputs, check if the output path (filepath.Clean) matches an input path
  - If match found: skip creating a new volume/mount (input volume is reused)
  - If no match: create output volume/mount as before

## Phase 2: Output Registration Fix

- [x] Verify registerOutputs handles shared volume names
  - `task_step.go` `registerOutputs()` matches by `filepath.Clean(mount.MountPath)`, so it
    finds the shared mount regardless of volume name prefix — no changes needed

## Phase 3: Live Verification

- [x] Deploy and verify with real pipeline `ca7b9dbdd`
  - Deployed to concourse.home, verified task steps with same-name input/output
    produce single volume mount
  - Verified non-overlapping inputs and outputs still work correctly
