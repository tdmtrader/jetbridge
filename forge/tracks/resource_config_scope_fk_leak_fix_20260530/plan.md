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

## Phase 1: Reproduce in the real check path (RED) [checkpoint: 4fd5e9e01c]

- [x] ef4fc3f070 Write integration test `atc/db/errors_test.go` (real Postgres via
      postgresrunner): create a `resource_config_scope`, delete it, call
      `scope.SaveVersions(...)`, assert the returned error satisfies
      `db.IsForeignKeyViolation`. Locks in the proven behavior. RESULT: GREEN
      (7/7 specs) — detection works against the real DB error.
- [x] ef4fc3f070 Harden `atc/exec` check-step FK handling coverage: the existing
      `check_step_test.go` test injected a *bare* synthetic `&pgconn.PgError`;
      now injects a wrapped error. RESULT: GREEN (4/4 FK specs). (Full real-DB
      check-step integration deferred — atc/exec uses fakes; the real-DB
      detection is covered by `errors_test.go`.)
- [x] Both passed → the code-level detection + guard are correct → pivot to
      Phase 2a (environment / deployed-binary), NOT Phase 2b (code bypass).

## Phase 2a: Environment / deployed-binary hypothesis

- [ ] Add a one-line startup or check-path log assertion that the FK guard build
      is present (e.g., log `scope-deleted-during-check` already exists — confirm
      it appears in web logs during a reproduced race), to prove at runtime which
      binary is deployed.
- [x] Audit the behavioral harness image path. FINDINGS: (1) footgun —
      `ensureConcourseImage` (topgun/k8s_behavioral/cluster_lifecycle_test.go:95)
      only builds when the image is ABSENT, silently reusing a stale
      `concourse-local:latest` if present (same in `buildAndLoadOOMTriggerImage`
      + integration suite). Real reliability bug for local/reused envs. (2) BUT
      this does NOT explain CI #100: the pipeline behavioral task runs
      `docker build -t concourse-local:latest` BEFORE the test (fresh binary,
      content-hashed COPY layer), so CI deploys fresh. Stale-binary hypothesis
      REFUTED for CI. helmDeployConcourse parses the ref correctly
      (splitImageRef) and deploys with IfNotPresent into a fresh K3s container.
- [x] e9de3901fe Instrument the behavioral harness to dump concourse-web logs on
      spec failure (was only showing fly client output) so the guard log is
      visible in CI output.
- [x] DECISIVE RUNTIME RESULT: pushed + ran instrumented chain. Behavioral
      reproduced the flake, but my instrumentation was ABSENT => CI compiled from
      a STALE kind-runner image. ROOT CAUSE = worker serves cached
      `concourse-kind-runner:v33` by tag, ignoring fresh pushes. The FK guards
      were never actually tested in CI. (Details in cgx.md.)
- [x] 4cdf75c6cc FIX the staleness: bump kind-runner tag v33 → v34 + set-pipeline
      (forces fresh pull). The real cache-bust.
- [~] Re-run the v34 chain (build-kind-runner → integration → behavioral) to test
      the CURRENT code for the first time. Expected: my instrumentation now
      present; the run reveals whether the guards fire on the real error.
- [x] ded0ca4ae7 (low-risk improvement) `ensureConcourseImage` now honors
      `CONCOURSE_REBUILD_IMAGE=1` and always logs the deployed image id + created
      time, so a stale-binary deploy is diagnosable from the next CI run's output.
      (Sibling `buildAndLoadOOMTriggerImage` + integration suite have the same
      reuse pattern — left as follow-ups; not on the FK path.)
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
