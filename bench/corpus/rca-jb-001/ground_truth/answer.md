# Ground truth — why every web rollout errors all in-flight builds

**Source of truth.** This answer is the RCA the humans actually produced, one
minute after the fix landed, in
`forge/tracks/build_survival_across_web_restart_20260704/spec.md` §"Root cause
(verified 2026-07-04 — supersedes the earlier hypothesis)" and `cgx.md`
§"Origin", both added by commit `03ef35b23559bac4d221b220e6a27ae1ee9daf04`
(2026-07-04T22:36:36-07:00). The fix that implements it is
`7c59cbbfa6ed5921d324793f2a03bb1ee4d12aa3`. Everything below is reproduced or
paraphrased from those documents plus the code they cite; `file:line` references
are as of the pre-state commit `1127c59301e2f865b4d2420e909ae5344e05661f`.

---

## 1. The root cause is DB-level, not runtime-level

Builds are not dying because their pods are taken away. They are being **marked
`errored` in the database by the web that is shutting down** (and, in the surge
window, by the web that is starting up). Pod deletion happens afterwards, as a
consequence.

Two mechanisms, both introduced by the same fork commit
`8b5476828f334d0ada0885d2bdbe59694e0e9a6b` (2026-04-10,
*"fix(check): prevent in-flight check tracking leak that permanently blocks
automatic resource checks"*):

### Mechanism A — engine drain finishes in-flight builds as errored

`atc/engine/engine.go:224-227`:

```go
	select {
	case <-b.release:
		logger.Info("releasing")
		b.finish(logger.Session("finish"), fmt.Errorf("build released during drain"), false)
```

On SIGTERM the ATC drains; `b.release` closes for every in-flight build; each one
is finished with a non-nil error, and `engineBuild.finish`
(`atc/engine/engine.go:255-272`) maps non-nil-and-not-`context.Canceled` to
`atc.StatusErrored`. Note that the error text goes only to the lager log
(`logger.Info("errored", …)`), which is why the *build* event stream shows no
error message — matching the reported symptom.

Upstream Concourse does not do this: it logs `releasing` and returns, leaving the
build `started` so that the next ATC's build tracker re-attaches to it. The
unconditional `b.finish` is fork-local, added by `8b5476828f` as its "Fix 2".

### Mechanism B — the tracker's deferred safety net errors any orphaned build

`atc/builds/tracker.go:93-107` (the `IsRunning()` branch is lines 99-106):

```go
		defer func() {
			err := util.DumpPanic(recover(), "tracking build %d", build.ID())
			if err != nil {
				logger.Error("panic-in-tracker-build-run", err)
				build.Finish(db.BuildStatusErrored)
			} else if build.IsRunning() {
				// Build exited Run() without calling Finish (e.g. lock
				// acquisition error, engine drain). Finalize it so that
				// in-flight check tracking is cleared and the resource
				// is not permanently blocked from future checks.
				logger.Info("finalizing-orphaned-build", loggerData)
				build.Finish(db.BuildStatusErrored)
			}
		}()
```

Any build whose `Run()` returns while the build is still running gets errored.
This is `8b5476828f`'s "Fix 1". It is a second, independent path to the same
outcome, and it is the one that fires on webs that are *not* draining.

## 2. Why the mechanisms exist (what a fix must not break)

`8b5476828f` was fixing a real production defect: the `inFlightChecks`
`sync.Map` in `atc/db/check_factory.go` deduplicates in-memory check builds by
`ResourceConfigScopeID`, and entries were only removed inside
`onFinishBuild.Finish()` (`atc/db/check_factory.go:256-268`). Any path that left
`engineBuild.Run()` without calling `Finish()` orphaned the entry and
**permanently blocked all future automatic checks for that resource until ATC
restart**. Its two fixes guaranteed `Finish()` always ran.

So the correct fix is a **narrowing, not a revert**. Reverting `8b5476828f`
restores the check-scheduling outage it fixed.

## 3. Blast radius — three legitimate resume paths are swept up

Mechanism B catches every early return from `engineBuild.Run()`, including three
paths that upstream deliberately leaves running so a later tracker cycle
resumes them:

1. **Tracking lock not acquired** (`atc/engine/engine.go:120-125`, the
   `if !acquired { logger.Debug("build-already-tracked"); return }` branch).
   During a surge rollout two webs are alive at once; the new web's tracker sees
   a started build, fails `AcquireTrackingLock` because the old web holds it,
   returns early — and the safety net then errors a build that the *other* web
   is actively running. This is a second, independent way a rollout kills builds.
2. **Engine drain** — Mechanism A already errors the build; the safety net is
   the backstop for the same event.
3. **`exec.Retriable` step errors** (`atc/engine/engine.go:230-234`): transient
   Kubernetes API failures are wrapped as `TransientError`
   (`atc/worker/jetbridge/errors.go`) → `exec.Retriable`, and the engine
   intentionally `return`s *without* finishing so the tracker retries later. The
   safety net errors the build instead.

Item 3 is very likely the true cause of the **"single-node contention →
intermittent errored builds"** symptom that is currently bandaged with
`attempts: 2` (`c2a7d3bfcbf5992e9cee012aa959bd7b621c6066`). It is not (or not
only) node pressure: it is transient K8s API errors that should have been
retried being converted into errored builds. The evidence bundle's suspicion
that both symptoms share a mechanism is correct.

## 4. The standing hypothesis is refuted

The theory that `jetbridge`'s process-wait deletes the task pod on context
cancellation is **wrong for production**.

- The delete-on-cancel branch is in `Process.Wait`
  (`atc/worker/jetbridge/process.go:96-107`, whose doc comment at line 58-61 says
  *"If the context is cancelled, the Pod is deleted"*). `Process` is
  **direct mode**.
- Direct mode only runs when no `PodExecutor` is configured:
  `atc/worker/jetbridge/container.go:159-174` returns `newExecProcess(...)`
  whenever `c.executor` is set and only falls through to `newProcess(...)` in
  the *"Fallback direct mode: only used when no executor is configured (e.g.
  tests that don't set up SPDY)"* branch.
- Production always configures one:
  `atc/atccmd/command.go:1416` — `factory.K8sExecutor =
  jetbridge.NewSPDYExecutor(k8sClientset, k8sRestConfig)`.
- `execProcess.Wait` deliberately never deletes pods on cancellation
  (`atc/worker/jetbridge/process.go:743-745`: *"The pause Pod is intentionally
  NOT deleted on context cancellation. Pod cleanup is handled by the GC system
  (reaper), which enables fly hijack to exec into the still-running pod"*).

An RCA that stops at `process.go` has found a real code path that production
never takes.

## 5. Correct causal ordering of the observed pod errors

```
SIGTERM to web
  → engine drain closes b.release (and/or tracker safety net fires)
  → build.Finish(BuildStatusErrored) — build is now errored in the DB
  → container GC marks that build's containers `destroying`
  → jetbridge Reaper deletes the corresponding pods
     (atc/worker/jetbridge/reaper.go, FindDestroyingContainers → Pods().Delete)
  → any web still attached to one of those pods logs
    `container-attach.failed-to-get-pod` and the step surfaces
    `attach: pod "<name>" not found` (atc/worker/jetbridge/container.go:254-256)
```

> Curator's note: the humans' spec quotes this symptom as
> `container-attach: failed-to-get-pod` / *"pod deleted externally"*. The second
> string is emitted only by `(*Process).pollUntilDone`
> (`atc/worker/jetbridge/process.go:178`), which is **direct mode** — the same
> mode the decoy lives in, and therefore not reachable in production. The exposed
> evidence bundle drops it for that reason. Do not require it of a submission,
> and treat a submission that notices the inconsistency as correct.

Pod deletion is **downstream of** the errored status, exactly as the evidence
bundle's ordering observation ("the build is already marked errored when these
lines appear") suggested. It is a consequence, not the cause.

## 6. Recommended direction (what the humans did)

The `inFlightChecks` map only ever tracks **in-memory check builds** — the
`onFinishBuild` wrapper is applied only there (`atc/db/check_factory.go:163`).
So both mechanisms can be scoped to check builds without losing the leak fix:

- `atc/engine/engine.go` `<-b.release`: finish only when
  `b.build.Name() == db.CheckBuildName`; job builds are left `started` so the
  next web's tracker re-attaches (upstream behavior).
- `atc/builds/tracker.go` safety net: keep the panic branch for all builds;
  scope the `IsRunning()` branch to `build.Name() == db.CheckBuildName`.

That is exactly commit `7c59cbbfa6ed5921d324793f2a03bb1ee4d12aa3`
(see `ground_truth/reference.diff`): 19 changed source lines across two files,
one added conjunct per site, plus rewritten specs.

Deliberately *not* changed by the fix, and correctly out of scope for this RCA:
making a task command that was mid-execution resume inside its pod without
re-running from the start (Phase 2 of the same track), and removing the
`attempts: 2` / settle-timer bandaids (left until the fix soaked).

## 7. Secondary finding recorded by the humans (bonus, not required)

Even with builds surviving the restart, "resume" today is really "re-run": for
an exec-mode task pod whose command is mid-flight, the new web's `Attach()`
finds no exit-status annotation, errors, and `attachOrRun` falls through to
`Run()`, which reuses the still-running pause pod and re-execs the task command
from scratch — while the original process may still be alive inside the pod (a
dropped SPDY session does not kill an exec'd process). That risks duplicate
concurrent execution in the same workspace. An analysis that notices this is
ahead of the request, not wrong.
