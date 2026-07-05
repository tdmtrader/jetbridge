# Plan: In-flight builds survive web restarts

**Track:** `build_survival_across_web_restart_20260704`

## Phase 0: Reproduce and pin the root cause

- [ ] Task 0.1: Write a reproduction note from live evidence. On theborg, pull
      the web logs / build events from a recent upgrade-window failure (or
      trigger one in a sandbox team: start a long build, restart web) and
      confirm the sequence: `releasing` / `finalizing-orphaned-build` log lines
      → build errored → reaper pod deletion → `failed-to-get-pod`. Record in
      cgx.md. (If live evidence is unavailable, the unit repro in Phase 1
      suffices — do not block.)

## Phase 1: Drain & tracker semantics (atc/engine, atc/builds)

- [x] 7c59cbbfa6 Task 1.1: Write failing tests for engine drain behavior
      (`atc/engine/engine_test.go`): on `Drain()` with a running **job** build,
      the build is NOT finished (stays running, no `Finish` call on the fake);
      with a running **in-memory check** build (`Name() == db.CheckBuildName`),
      `Finish(Errored)` IS called. Cover the existing "build released during
      drain" expectations and update them to the new contract.
- [x] 7c59cbbfa6 Task 1.2: Implement the engine change (engine.go `<-b.release` case):
      finish check builds only; log-and-return for job builds.
- [x] 7c59cbbfa6 Task 1.3: Write failing tests for the tracker safety net
      (`atc/builds/tracker_test.go`):
      (a) job build whose Run() returns while still running (lock not acquired
      / drain / Retriable) is NOT finished by the tracker;
      (b) in-memory check build in the same situation IS finished (check-leak
      regression guard);
      (c) panic during Run still finishes any build as errored.
- [x] 7c59cbbfa6 Task 1.4: Implement the tracker change (tracker.go deferred func): scope
      the `IsRunning()` branch to `build.Name() == db.CheckBuildName`.
- [x] 7c59cbbfa6 Task 1.5: Confirm Retriable resume end-to-end at unit level: engine test
      that a job build whose step errors with `exec.Retriable` returns without
      finishing AND (with 1.4) is not errored by the tracker — i.e. remains
      running for the next tracker cycle.
- [x] 7c59cbbfa6 Task 1.6: Run `ginkgo ./atc/engine/ ./atc/builds/` and `go vet ./...`;
      commit Phase 1 (`fix(engine): release in-flight job builds on drain
      instead of erroring them`).

## Phase 2: Resumable task exec (atc/worker/jetbridge)

- [ ] Task 2.1: Design the supervisor script as a Go constant + builder
      (`supervisor.go`): POSIX-sh only; state dir `/tmp/concourse-task-<id>`;
      pid/log/exit files; alive-check via `kill -0`; tail-and-wait branch;
      replay branch; propagate real exit code. Document busybox compatibility
      assumptions next to `pauseCommand` in container.go.
- [ ] Task 2.2: Write failing unit tests: `execProcess.Wait` for a
      `ContainerTypeTask` container wraps the command in the supervisor
      (assert on the command passed to the fake `PodExecutor`); get/put/check
      containers exec the raw command unchanged; exit-status annotation still
      written from the supervisor's exit code.
- [ ] Task 2.3: Implement supervisor wrapping in `execProcess.Wait` /
      `newExecProcess` (task-type only, exec mode only).
- [ ] Task 2.4: Write failing unit test for reattach flow: `Attach` on an
      exec-mode task container with no exit annotation still errors into
      `attachOrRun`'s `Run()` fallback (unchanged contract), and `Run()` on an
      existing Running pod re-execs the SAME supervisor command (idempotent by
      design). Verify `container_test.go` expectations still hold.
- [ ] Task 2.5: Run `ginkgo ./atc/worker/jetbridge/` full suite; fix fallout
      (behavioral_runtime_spec_test.go asserts on exec commands in several
      specs). Commit Phase 2 (`feat(jetbridge): resumable task exec via
      in-pod supervisor`).

## Phase 3: Live + end-to-end verification

- [ ] Task 3.1: Write live test `live_task_resume_test.go`
      (`//go:build live`, plain Go test, pattern from
      live_sidecar_logstream_test.go): throwaway namespace on theborg; start a
      long task (`sleep 90; echo done >> /tmp/marker`) through Worker →
      Container → execProcess; cancel the first Wait's context mid-run;
      construct a fresh Container (same handle/pod name, simulating the new
      web) and Attach/Run; assert: command executed exactly once (marker has
      one line), final exit status correct, pod not deleted during the window.
      Run: `KUBECONFIG=~/.kube/config K8S_TEST_NAMESPACE=<sandbox-ns> go test
      -tags live -run '^TestLiveTaskResume$' -v -count=1 -timeout 10m
      ./atc/worker/jetbridge/`.
- [ ] Task 3.2: E2E on theborg: deploy the branch image to a sandbox install
      (or piggyback on the release pipeline's self-upgrade), start a build
      with a ≥2-minute task, `kubectl rollout restart` the web deployment
      mid-build, and verify the build succeeds with no `failed-to-get-pod`
      events. Record evidence (build URL, timings) in cgx.md.
- [ ] Task 3.3: Regression sweep: `make test-unit` and
      `make test-fly-integration` green; note in cgx.md whether the
      verify-upgrade settle timer and `attempts: 2` are now removable
      (do NOT remove in this track).
- [ ] Task 3.4: Update memory (`project_jetbridge_release_pipeline.md` root
      cause refinement) and close out: conventional commits squashed onto
      `jetbridge` per workflow.md.

## Checkpoints

- After Phase 1: unit-green checkpoint — safe to ship alone (fixes the
  DB-level erroring even before runtime resume lands; reattach falls back to
  re-exec as today).
- After Phase 3: live evidence required before marking the track complete.
