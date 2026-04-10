# Plan: Fix in-flight check tracking leak

## Phase 1: Tracker safety net

- [x] Task 1.1: Add deferred Finish call in tracker goroutine for orphaned builds
  - File: `atc/builds/tracker.go`, function `trackBuild`
  - After `Run()` returns, check `build.IsRunning()` — if true, call `build.Finish(db.BuildStatusErrored)`
  - This ensures `onFinishBuild.Finish()` always runs, clearing `inFlightChecks`

- [x] Task 1.2: Fix engine release path to call finish
  - File: `atc/engine/engine.go`, function `engineBuild.Run()`
  - In the `<-b.release` case (line 224), call `b.finish()` instead of just logging

## Phase 2: Test validation

- [x] Task 2.1: Write test proving inFlightChecks is cleaned up on early exit
  - File: `atc/builds/tracker_test.go`
  - Create an in-memory check build wrapped in `onFinishBuild`
  - Use a fake engine whose `Run()` returns without calling `Finish()`
  - Verify the cleanup function was called after `trackBuild` completes

- [x] Task 2.2: Run existing test suites
  - `ginkgo ./atc/builds/` — tracker tests
  - `ginkgo ./atc/engine/` — engine tests
  - `ginkgo ./atc/db/` — check factory tests
