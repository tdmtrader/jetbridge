# Spec: In-flight builds survive web restarts (drain-aware build release + resumable task exec)

**Track:** `build_survival_across_web_restart_20260704`
**Type:** bugfix
**Created:** 2026-07-04

## Overview (the WHY)

Every rolling restart of `concourse-web` — including the release pipeline's own
`self-upgrade` step — errors all in-flight builds. The current mitigations are
symptomatic: `attempts: 2` on release-chain jobs (commit c2a7d3bfcb) and the
verify-upgrade settle timer (commit 1127c59301) merely avoid or retry around the
window in which builds die. The architectural intent of the runtime is the
opposite: task pods are independent K8s objects (no OwnerReferences), the
exit-status/resource-result pod annotations exist specifically so a new web can
recover step state after a restart, and upstream Concourse's engine is designed
to *release* in-flight builds on shutdown so the next ATC's build tracker
re-attaches. A web upgrade should be invisible to running builds.

## Root cause (verified 2026-07-04 — supersedes the earlier hypothesis)

The initial hypothesis ("jetbridge's `process.go` deletes task pods on context
cancellation during SIGTERM") is **wrong for production**. `Process.Wait`'s
delete-on-cancel branch belongs to direct mode, which only runs when no
`PodExecutor` is configured (tests). Production always runs exec mode
(`factory.K8sExecutor = jetbridge.NewSPDYExecutor(...)`, atc/atccmd/command.go:1416),
and `execProcess.Wait` deliberately never deletes pods — the reaper owns cleanup.

The real mechanism is **DB-level, in the fork's engine/tracker changes** from
commit `8b5476828f` (in-flight check tracking leak fix):

1. **Drain finishes builds as Errored** (atc/engine/engine.go:225-227). On
   SIGTERM the engine's `<-b.release` case calls
   `b.finish(..., "build released during drain", false)` → `Finish(StatusErrored)`.
   Upstream just logs "releasing" and returns, leaving the build `started` so
   the next ATC resumes it.
2. **Tracker safety net errors any still-running build whose `Run()` returned**
   (atc/builds/tracker.go:99-106). This sweeps up three legitimate
   leave-running-and-resume paths:
   - *Lock not acquired* — during a surge rollout (`maxSurge:1`) the new web's
     tracker sees the started build, fails `AcquireTrackingLock` because the old
     web holds it, returns early — and then errors a build another web is
     actively running.
   - *Engine drain* — the release path above.
   - *`exec.Retriable` errors* (atc/engine/engine.go:230-234) — transient K8s
     API errors are wrapped as `TransientError` (jetbridge/errors.go) →
     `exec.Retriable`, and the engine intentionally returns without finishing so
     the tracker retries later. The safety net errors the build instead. This is
     the likely true cause of the "single-node contention → transient errored
     builds" failure mode currently bandaged with `attempts: 2`.
3. **Downstream pod deletion.** Once the build is errored in the DB, container
   GC marks its containers destroying and the Reaper deletes the pods
   (reaper.go:146-166). Any web still attached to a pod then observes
   `container-attach: failed-to-get-pod` / "pod deleted externally" — the
   symptom recorded in the release-pipeline failure notes. Pod deletion is a
   consequence, not the cause.

The check-leak fix itself remains necessary: the `inFlightChecks` map
(atc/db/check_factory.go) leaks when an in-memory check build exits `Run()`
without `Finish()`. But that map only ever tracks **in-memory check builds**
(the `onFinishBuild` wrapper is only applied to them), so the safety net and
drain-finish must be scoped to check builds instead of all builds.

Secondary gap (why "resume" today is only "re-run"): for an exec-mode task pod
whose command is mid-flight, the new web's `Attach()` finds no exit-status
annotation, errors, and `attachOrRun` falls through to `Run()`, which reuses the
running pause pod and **re-execs the task command from scratch** — while the
original process may still be alive inside the pod (a dropped SPDY session does
not kill the exec'd process). That risks duplicate concurrent execution in the
same workspace.

## Requirements (the WHAT)

1. On web graceful shutdown (engine drain), in-flight **job builds** must be
   left in `started` status (released, not finished) so a subsequent web's
   build tracker resumes them.
2. On engine drain, **in-memory check builds** must still be finalized so
   `inFlightChecks` cleanup runs (preserves the 8b5476828f fix; checks are
   cheap and lidar recreates them).
3. The tracker safety net must no longer error job builds that exit `Run()`
   while still running (lock contention, drain, Retriable); it must still
   finalize orphaned in-memory check builds and still error any build on panic.
4. `exec.Retriable` step errors on job builds must again result in the build
   being re-tracked and resumed, not errored.
5. A task step that was mid-exec when the web restarted must be resumed by the
   new web **without starting a second concurrent copy of the command**: if the
   original process is still running in the pod, the runtime must wait for it
   and report its real exit status; only if it is genuinely gone may the
   command be restarted.
6. Genuine build aborts must keep today's behavior: prompt pod deletion /
   cleanup, build marked aborted.
7. The behavior must be verified live: a build running a long task survives a
   web rollout and completes successfully, with correct exit status, on a real
   cluster.

## Technical approach (the HOW)

### Phase 1 — engine/tracker drain semantics
- `atc/engine/engine.go` `<-b.release` case: finish only when
  `b.build.Name() == db.CheckBuildName`; for job builds, log and return
  (upstream behavior).
- `atc/builds/tracker.go` deferred safety net: keep the panic branch for all
  builds; scope the `IsRunning()` branch to check builds
  (`build.Name() == db.CheckBuildName`).
- Unit tests via existing suites (`atc/engine`, `atc/builds` with
  counterfeiter fakes): drain leaves job build running / finalizes check build;
  lock-not-acquired and Retriable returns do not error job builds; panic still
  errors; check-build orphan still finalized (leak regression guard).

### Phase 2 — resumable task exec (jetbridge)
- Wrap the task-step exec command (exec mode, `ContainerTypeTask` only) in a
  small POSIX-sh supervisor that makes re-exec idempotent and resumptive:
  - state dir inside the pod (survives web restarts because the pod survives),
    e.g. `/tmp/concourse-task-<processID>/`: `pid`, `log`, `exit` files.
  - fresh start: record pid, run command with stdout/stderr teed to `log`,
    write exit code to `exit` on completion, exit with same code.
  - re-exec while original alive (`pid` exists, process alive): do NOT restart;
    tail `log` and wait for `exit`, then exit with the recorded code.
  - re-exec after completion (`exit` exists): replay log tail, exit with code.
  - `sh` is already a hard runtime dependency (pause command); the supervisor
    must restrict itself to POSIX sh + busybox-safe utilities.
- Non-task steps (get/put/check use the stdin/stdout resource protocol) keep
  current re-exec semantics — out of scope for true resume; they are short and
  their results are already recovered via the resource-result annotation.
- Unit tests with fake clientset assert command wrapping and unchanged
  behavior for non-task steps; supervisor script behavior gets live coverage
  (fake clientset cannot exec).

### Phase 3 — live + end-to-end verification
- New live test (plain Go, `//go:build live`, throwaway namespace on theborg,
  NOT `cicd`/`concourse`): start a long-running task via the real Worker/
  Container/execProcess, sever the first exec (cancel ctx / drop the session),
  attach again with a fresh worker instance (simulating the new web), assert
  the command ran exactly once (marker file) and the build-side result is the
  real exit status.
- End-to-end on theborg: trigger a long-running build in the sandbox team,
  `kubectl rollout restart deployment` of web mid-build, assert the build
  completes successfully. The release pipeline's own self-upgrade →
  verify-upgrade chain is the ongoing regression canary.

## Acceptance criteria

1. Unit: engine drain leaves job builds `started`, finalizes in-memory check
   builds; tracker safety net no longer errors running job builds on
   lock-contention/drain/Retriable returns; panic path and check-leak guard
   still covered. `ginkgo ./atc/engine/ ./atc/builds/` green.
2. Unit: jetbridge suite green (`ginkgo ./atc/worker/jetbridge/`); task-step
   exec commands are supervisor-wrapped in exec mode; get/put/check commands
   unchanged.
3. Live test proves single-execution resume across a simulated web restart on
   theborg.
4. E2E: a build with a ≥2-minute task survives `kubectl rollout restart` of
   concourse-web and succeeds; no `failed-to-get-pod` errors in its events.
5. `go vet ./...` clean; no regressions in `make test-unit`.

## Out of scope

- True mid-flight resume for get/put/check steps (stdin protocol; re-exec
  semantics retained).
- Log de-duplication on reattach (full log replay from the supervisor's log
  file is acceptable).
- Removing the verify-upgrade settle timer and `attempts: 2` bandaids — leave
  in place until this fix has soaked through several release cycles; removal
  is a follow-up cleanup.
- Multi-web HA scheduling improvements beyond restoring upstream release
  semantics.
- Hijack/TTY exec paths (unchanged).
