# Spec: Fix in-flight check tracking leak that permanently blocks automatic resource checks

## Overview

Automatic resource checks (lidar scanner) stop running after 3-8 hours of
continuous ATC operation. Manual checks triggered from the UI continue to work.
The ATC must be restarted to recover. The root cause is a leaked entry in the
`inFlightChecks` sync.Map that is never cleaned up when an in-memory check
build exits `engineBuild.Run()` via an early-return path that skips `Finish()`.

## Problem

`checkFactory.TryCreateCheck()` uses a `sync.Map` (`inFlightChecks`) keyed by
`ResourceConfigScopeID` to deduplicate in-memory check builds. The entry is
added before the build is sent to the tracker and is only removed inside
`onFinishBuild.Finish()`.

However, `engineBuild.Run()` has several early-return paths that never call
`Finish()`:

1. **Lock acquisition error** (engine.go ~line 117) — a transient DB error
   during `AcquireTrackingLock()` causes `Run()` to return immediately.
2. **Engine drain/release** (engine.go ~line 224) — when the component runner
   drains the engine, the `<-b.release` case fires and the build returns
   without calling `Finish()`.

In both cases the `inFlightChecks` entry is orphaned. Every subsequent scanner
cycle finds the entry via `LoadOrStore()`, logs `skipped-in-memory-check-already-in-flight`,
and returns without creating a check. The resource never receives another
automatic check until the ATC process restarts (which clears the in-memory map).

Manual checks bypass `inFlightChecks` because `manuallyTriggered=true` skips
the dedup guard entirely.

## Requirements

1. `inFlightChecks.Delete(scopeID)` must execute for every in-memory check
   build regardless of how `engineBuild.Run()` exits.
2. The fix must not cause double-Finish side effects (idempotent cleanup).
3. Existing check deduplication behavior must be preserved — the fix should not
   allow duplicate check pods.
4. No new interfaces required; the fix should be surgical.

## Technical Approach

### Fix 1 — Tracker safety net (tracker.go)

In the tracker goroutine that runs each build, add a deferred check after
`Run()` returns: if the build is still running (`IsRunning() == true`), call
`build.Finish(BuildStatusErrored)`. This triggers `onFinishBuild.Finish()` →
`inFlightChecks.Delete(scopeID)`. The `IsRunning()` guard prevents double-Finish
when the build completed normally.

### Fix 2 — Engine release path (engine.go)

In the `<-b.release` select case, call `b.finish()` so that draining the engine
properly finalizes in-flight builds instead of silently abandoning them.

Both fixes provide defense-in-depth: Fix 2 handles the drain case explicitly,
and Fix 1 catches any other early-return path (current or future).

## Acceptance Criteria

1. A test demonstrates that `inFlightChecks` is cleaned up when a check build
   exits without the engine calling `Finish()`.
2. All existing `atc/builds` and `atc/engine` tests pass.
3. All existing `atc/db` tests pass.

## Out of Scope

- Adding metrics/logging for orphaned check cleanup (could be a follow-up).
- Changing the deduplication mechanism itself (sync.Map is appropriate).
- Fixing DB-backed (toDB=true) check builds (they are not affected).
