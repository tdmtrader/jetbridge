# Implementation Plan: Resolve resource_config_scope FK-violation leak

## Phase 0: Diagnosis (DONE — 2026-05-30)

- [x] Confirm behavioral failures are the FK race on the guarded `save versions:`
      path (#100) and a sibling custom-type spec (#99).
- [x] Reproduce the real FK error shape via `sql.Open("pgx") + tx.QueryRow().Scan()`:
      result = clean `*pgconn.PgError`, `errors.As` true, `IsForeignKeyViolation`
      true (raw + wrapped). Detection helper is NOT broken.
- [x] Rule out: retrying-driver query wrapping (wraps only Open), stale source
      (origin == HEAD for the 3 files), deferred FK (constraint is immediate
      ON DELETE CASCADE), unguarded Update* calls (UPDATE on scope row → no FK).

## Phase 1: Reproduce in the real check path (RED)

- [~] Write integration test `atc/db/errors_test.go` (real Postgres via
      postgresrunner): create a `resource_config_scope`, delete it, call
      `scope.SaveVersions(...)`, assert the returned error satisfies
      `db.IsForeignKeyViolation`. Locks in the proven behavior.
- [ ] Write integration test exercising `atc/exec` check-step graceful handling:
      drive the check flow (real `resourceConfigScope`) with the scope deleted
      before `SaveVersions`, assert `check_step.Run` returns `(false, nil)` and
      `delegate.Finished(false)` — NOT an `errored` build. Replace/augment the
      synthetic `&pgconn.PgError` injection in `check_step_test.go:737,769`.
- [ ] If both pass locally (expected), the code is correct → pivot to Phase 2a
      (environment). If either fails, a real code-path bypass is found → Phase 2b.

## Phase 2a: Environment / deployed-binary hypothesis

- [ ] Add a one-line startup or check-path log assertion that the FK guard build
      is present (e.g., log `scope-deleted-during-check` already exists — confirm
      it appears in web logs during a reproduced race), to prove at runtime which
      binary is deployed.
- [ ] Audit the behavioral harness image path: confirm `concourse-local:latest`
      built in the task is the image actually loaded into the KinD/K3s cluster
      (no stale layer reuse). Cross-check with the `registry.home` staleness
      finding. File/fix any image-propagation gap.
- [ ] Re-run the behavioral spec against a confirmed-fresh deploy.

## Phase 2b: Code-path bypass hypothesis (only if Phase 1 reproduces a leak)

- [ ] Identify the exact statement/path that leaks the FK error in the real flow.
- [ ] Apply the minimal guard so the error is detected and handled gracefully.

## Phase 3: Close remaining FK-race gaps (defense in depth)

- [ ] Decide and document native-path policy: `lidar/scanner.go:224,399`
      `SaveVersions` currently logs `failed-to-save-versions` and returns. Confirm
      silent-drop is acceptable for native type resolution, or add explicit
      `IsForeignKeyViolation` handling + a debug log, and a test.
- [ ] Audit all scope-referencing INSERTs reachable during a check for the same
      race; guard any that can propagate to a build.

## Phase 4: Verify

- [ ] `make test-unit` + focused `ginkgo ./atc/db/ ./atc/exec/` green.
- [ ] Trigger `k8s-e2e`; confirm "runs a pipeline with custom resource types" and
      "6.1 single custom type backed by registry-image" pass ≥3 consecutive runs.
- [ ] Update `topgun/k8s_behavioral/FAILURES.md` and close the superseded
      `resource_config_scope_gc_race_20260408` track.
