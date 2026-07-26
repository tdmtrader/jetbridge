# Ground truth — what the humans actually did

Terminal artifact: `7c59cbbfa6ed5921d324793f2a03bb1ee4d12aa3`
"fix(engine): release in-flight job builds on drain instead of erroring them"
(4 files, +73 / -14; 2 source files, 2 test files).

## Root cause

A fork-local commit, `8b5476828f` ("fix(check): prevent in-flight check tracking
leak that permanently blocks automatic resource checks", 2026-04-10), fixed a
real production defect: `checkFactory`'s `inFlightChecks` `sync.Map` — keyed by
`ResourceConfigScopeID`, cleaned up only inside `onFinishBuild.Finish()` — leaked
whenever `engineBuild.Run()` took an early-return path that skipped `Finish()`.
A leaked entry silently blocked every future *automatic* check for that resource
until the ATC restarted.

That fix used two mechanisms, both unscoped:

1. `atc/engine/engine.go`, the `<-b.release` (engine drain) select case, began
   calling `b.finish(..., "build released during drain", false)` →
   `Finish(BuildStatusErrored)`. Upstream Concourse only logs `releasing` and
   returns, deliberately leaving the build `started`.
2. `atc/builds/tracker.go`, `trackBuild`'s deferred func, began calling
   `build.Finish(db.BuildStatusErrored)` for **any** build still `IsRunning()`
   after `Run()` returned.

Between them these swept up three legitimate *leave-running-and-resume* paths:

- **engine drain** — the SIGTERM path itself (web rollout / `self-upgrade`);
- **tracking lock held by another web** — during a surge rollout the new web's
  tracker sees the started build, fails `AcquireTrackingLock`, returns early —
  and the safety net then errors a build the *other* web is actively running;
- **`exec.Retriable` step errors** — transient K8s API errors are wrapped as
  `TransientError` → `exec.Retriable`; `engineBuild.Run()`'s `<-done` branch
  intentionally returns without finishing so the build is retried later, and the
  safety net errored it instead.

The observed pod-deletion cascade is downstream: errored build → container GC
marks containers `destroying` → the jetbridge reaper deletes the pods → any web
still attached sees `failed-to-get-pod`.

## The key insight

`inFlightChecks` — the map the earlier fix protects — only ever tracks **in-memory
check builds**, because the `onFinishBuild` wrapper is applied only to them. So
the earlier fix does not need to reach job builds at all. Scoping both mechanisms
to `Name() == db.CheckBuildName` keeps the leak fixed and restores upstream
resume semantics for job builds.

## The change

`atc/engine/engine.go` — `<-b.release` case:

```go
case <-b.release:
	logger.Info("releasing")

	// In-memory check builds cannot resume across a restart, so finalize
	// them to clear in-flight check tracking. Job builds are left in
	// "started" so the next web's build tracker re-attaches to them.
	if b.build.Name() == db.CheckBuildName {
		b.finish(logger.Session("finish"), fmt.Errorf("build released during drain"), false)
	}
```

`atc/builds/tracker.go` — deferred safety net in `trackBuild`:

```go
} else if build.IsRunning() && build.Name() == db.CheckBuildName {
	// ... finalize orphaned check build ...
	logger.Info("finalizing-orphaned-check-build", loggerData)
	build.Finish(db.BuildStatusErrored)
}
```

The panic branch (`build.Finish(db.BuildStatusErrored)` on `DumpPanic` returning
non-nil) is untouched and still applies to every build.

No changes were needed for the `exec.Retriable` behavior itself — the engine's
early return on `<-done` was already correct; removing the tracker's blanket
safety net is what restores retry.

## Tests written with the fix

- `atc/engine/engine_test.go` — the existing "when the build is released" context
  splits into `Context("when this is a job build")` → `FinishCallCount() == 0`
  and `Context("when this is an in-memory check build")` (sets
  `fakeBuild.NameReturns(db.CheckBuildName)`) → `FinishCallCount() == 1` with
  `db.BuildStatusErrored`.
- `atc/builds/tracker_test.go` — `TestTrackFinalizesOrphanedBuild` becomes two
  tests: `TestTrackDoesNotFinalizeReleasedJobBuild` (name `"42"`, asserts
  `FinishCallCount() == 0`) and `TestTrackFinalizesOrphanedCheckBuild` (name
  `db.CheckBuildName`, asserts one `Finish(BuildStatusErrored)`). The existing
  leak-regression test `TestTrackOrphanedInMemoryCheckCleansUpInFlightTracking`
  gains `fakeBuild.NameReturns(db.CheckBuildName)` so it still exercises the
  cleanup path.

Both packages run on counterfeiter fakes only — no PostgreSQL.

## Scope note

This is Phase 1 of the `build_survival_across_web_restart_20260704` track. Phase 2
(a POSIX-sh supervisor making a mid-flight task exec resumptive inside its pod)
is deliberately out of scope for this case and is excluded from the task.
