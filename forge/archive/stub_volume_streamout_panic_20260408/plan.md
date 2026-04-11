# Plan: Stub Volume StreamOut Panic on Daemon Cache Hit

## Phase 1: Defensive Guards (prevent panic)

### Task 1.1
- [x] Add nil-executor guard to `Volume.StreamOut` (`atc/worker/jetbridge/volume.go:205`)
  - Return `fmt.Errorf("cannot stream out: volume %s has no executor (stub volume from daemon cache)", v.handle)` when `v.executor == nil`

### Task 1.2
- [x] Add nil-executor guard to `Volume.StreamIn` (`atc/worker/jetbridge/volume.go:154`)
  - Return same-style error when `v.executor == nil`

### Task 1.3
- [x] Write unit tests for nil-executor guards (`atc/worker/jetbridge/volume_test.go`)
  - Test that `NewStubVolume(...).StreamOut(...)` returns error, not panic
  - Test that `NewStubVolume(...).StreamIn(...)` returns error, not panic

## Phase 2: Return DaemonSetVolume from cache hit (fix the bug)

### Task 2.1
- [x] Modify `FindDaemonResourceCache` (`atc/worker/jetbridge/worker.go:341-371`) to return a `DaemonSetVolume` instead of a `NewStubVolume`
  - Added `NewDaemonSetVolumeFromIP` constructor that accepts a direct daemon IP
  - Updated `daemonURL` to use `sourceIP` when set (bypasses node resolver)
  - Updated `StreamOut` and `StreamIn` guards to check both `sourceNode` and `sourceIP`

### Task 2.2
- [x] Verify `DaemonSetVolume` satisfies `runtime.Volume` interface
  - Already had compile-time check: `var _ runtime.Volume = (*DaemonSetVolume)(nil)`

### Task 2.3
- [x] Write unit tests for `FindDaemonResourceCache` returning a `DaemonSetVolume`
  - Test that returned volume is `*DaemonSetVolume` with correct handle and source
  - Test `NewDaemonSetVolumeFromIP` StreamOut via mock HTTP server
  - Test handle/source accessors
  - Test error on empty IP

## Phase 3: Validation

### Task 3.1
- [x] Run existing test suites to verify no regressions
  - `go build ./...` — clean
  - Ginkgo focused tests (StubVolume, FindDaemonResourceCache) — 6/6 passed
  - Behavioral tests (TestVT*) — all passed
  - DaemonSet backend + probe tests — all passed
