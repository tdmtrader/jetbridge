# Forge Growth Experience (CGX)

**Track:** `build_survival_across_web_restart_20260704`
**Purpose:** Log observations during implementation for continuous improvement analysis.

---

## Origin

- [2026-07-04] Scoped from the verify-upgrade settle-timer discussion
  (commit 1127c59301). The user's architectural instinct — "a web upgrade
  should not kill task pods; the new web should take over running jobs" —
  prompted a root-cause investigation.
- **Scoping research overturned the initial hypothesis.** The first analysis
  blamed `jetbridge/process.go`'s delete-pod-on-ctx-cancel branch. Reading the
  wiring showed that branch is direct mode only (no PodExecutor — tests);
  production is always exec mode (command.go:1416) and `execProcess` never
  deletes pods on cancel. The true root cause is DB-level: fork commit
  `8b5476828f` made (a) engine drain finish all in-flight builds as Errored
  (engine.go:225-227) and (b) the tracker's deferred safety net error ANY
  still-running build whose Run() returned (tracker.go:99-106) — sweeping up
  lock-contention (surge rollouts!), drain, and exec.Retriable resume paths.
  Pod deletion (`failed-to-get-pod`) is downstream: errored build → container
  GC → reaper. Lesson: verify which code path production actually takes
  before designing a fix around a plausible-looking branch.
- The Retriable interaction (engine.go:230-234 returns without finishing;
  safety net then errors the build) probably explains the "single-node
  contention → transient errored builds" failure mode previously attributed
  to resource pressure and bandaged with `attempts: 2`.

## Frustrations & Friction

- [2026-07-04] busybox portability bite: `kill -0 ""` exits 0 on busybox
  (bash errors), so the supervisor's liveness check silently never started
  the command on real task images while all local/macOS script tests passed.
  Lesson: any POSIX-sh script destined for arbitrary task images must be
  smoke-tested on busybox (a 30s kubectl-exec on theborg) before trusting
  local sh results.
- [2026-07-04] Live-test debugging trap: sharing one bytes.Buffer between
  ProcessIO Stdout and Stderr races (SPDY writes them from separate
  goroutines) and corrupted output into NUL bytes that looked like a file
  sparseness bug. Production is safe (event writers / TTY merge); added
  syncBuffer to the live test.

## Good Patterns

- [2026-07-04] Phase 1 TDD went cleanly: both old-contract tests
  (engine "finishes as errored on release", tracker
  TestTrackFinalizesOrphanedBuild) encoded the buggy behavior explicitly,
  so rewriting them to the new contract WAS the red phase — failures were
  crisp ("expected 0 Finish calls, got 1"). Engine-side Retriable coverage
  already existed (engine_test.go "when the error is retryable") and needed
  no changes; only the tracker side was breaking that contract in prod.
- [2026-07-04] Note: `gofmt -l atc/engine/` flags pre-existing drift in
  build_step_delegate{,_test}.go — not touched, left alone.

## Decisions

- Keep the 8b5476828f check-leak fix by SCOPING (not reverting): the
  inFlightChecks map only tracks in-memory check builds, so drain-finish and
  the safety net apply to `Name() == db.CheckBuildName` only.
- Task-step-only supervisor for true resume; get/put/check keep re-exec
  semantics (stdin protocol, short-lived, results recovered via annotation).
- Settle timer and `attempts: 2` stay until this soaks through release cycles.

## Evidence (Phase 3 close-out, 2026-07-05)

- **Live runtime test** (`TestLiveTaskResume`, theborg throwaway ns): web1's
  TTY exec severed mid-command → pod + command survived → fresh worker
  attached, replayed output with exactly one start marker (resumed, not
  restarted), real exit code 4, exit-status annotation recorded. PASS 24s.
- **Before/after drain comparison in the release pipeline**: previous
  rollout (self-upgrade/172, old drain code outgoing) errored the in-flight
  verify-upgrade/104 in the 23s restart window; next rollout
  (self-upgrade/173, FIXED web outgoing) — verify-upgrade/114 ran through
  the same window and succeeded. k8s-live-tests/570 green (validated the
  hijack/command-hash fix in the daemon-equipped CI env); release/46
  succeeded.
- **Direct e2e on the live cluster**: `resume-e2e/long-task` #1
  (busybox, 180s sleep) triggered; `kubectl rollout restart
  deploy/concourse-web -n cicd` issued 2s after task start (01:14:49);
  old replica gone by 01:15:20; build ran through the restart and
  SUCCEEDED with output "e2e-start ... e2e-done" exactly once each — no
  failed-to-get-pod, no restart, no duplicate execution.
- **Regression posture**: settle timer (1127c59301) and `attempts: 2` are
  now belt-and-suspenders; candidates for removal after a few release
  cycles (deliberately NOT removed in this track, per spec).
- Post-release verify-upgrade churn (116+ expecting next-rc after
  release/46) is the known ArgoCD :latest / version-label quirk —
  pre-existing, unrelated to this track.
