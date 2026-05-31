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

- [x] Add a one-line startup or check-path log assertion that the FK guard build
      is present. RESOLVED (obsolete as written): the provenance log from
      ded0ca4ae7 (`Using Concourse image … created=<ts>`) already proves at
      runtime which binary is deployed; that was the decisive signal for the
      staleness root cause. There is no longer a race to observe the guard firing
      on — once fresh code runs, the spec passes with no FK violation (#102/#103).
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
- [x] Re-ran the chain on fresh code (had to bump v34→v35 again, since the worker
      re-caches each tag). RESULT: behavioral build #102 (v35) = SUCCESS,
      298 Passed | 0 Failed. The `runs a pipeline with custom resource types` spec
      PASSED with no FK error. My instrumentation confirmed fresh code ran
      (`Using Concourse image … created=2026-05-30T21:02:24`). => CI image
      staleness was the entire root cause; the April FK guards work once actually
      deployed.
- [x] ded0ca4ae7 (low-risk improvement) `ensureConcourseImage` now honors
      `CONCOURSE_REBUILD_IMAGE=1` and always logs the deployed image id + created
      time, so a stale-binary deploy is diagnosable from the next CI run's output.
      (Sibling `buildAndLoadOOMTriggerImage` + integration suite have the same
      reuse pattern — left as follow-ups; not on the FK path.)
- [x] Re-run the behavioral spec against a confirmed-fresh deploy. DONE: two
      consecutive green runs on fresh code — behavioral #102 AND #103 (v35),
      298 Passed | 0 Failed; "runs a pipeline with custom resource types" passes
      in both (verified 2026-05-31 via `fly -t home watch`).

## Phase 2b: Code-path bypass hypothesis (only if Phase 1 reproduces a leak)

- [x] N/A — Phase 1 established the code-level detection + guards are correct
      (real-DB `errors_test.go` GREEN); the leak was environmental (image
      staleness), not a code-path bypass. No statement leaks the FK error in the
      real flow once fresh code is deployed.

## Phase 3: Close remaining FK-race gaps (defense in depth)

- [x] Decide and document native-path policy: `lidar/scanner.go` `SaveVersions`
      (and `SetResourceConfigScope`) on both native paths (`resolveResourceType`,
      `resolveResource`). DECISION: do NOT silently drop at `Error` level — that
      logs a benign GC race as a false-positive error. Added explicit
      `db.IsForeignKeyViolation` handling that demotes the race to `Debug`
      (`scope-deleted-before-version-save` / `scope-deleted-during-version-save`),
      mirroring the `atc/exec/check_step.go` guard convention. Non-FK errors keep
      the existing `Error` log. Covered by 12 new specs in
      `atc/lidar/scanner_test.go` (FK→Debug, no Error; SetResourceConfigScope FK
      skips SaveVersions; non-FK→Error preserved). All 43 lidar specs green.
- [x] Audit all scope-referencing INSERTs reachable during a check for the same
      race. CONCLUSION: the only FK surfaces that propagate to a build/scan are
      `SaveVersions` (resource_config_versions FK) and `SetResourceConfigScope`
      (resources/resource_types FK). Both are now guarded on BOTH the build path
      (`check_step.go:162-170,254-262`) and the native scan path
      (`scanner.go` resolveResourceType + resolveResource). `UpdateScopeLastCheck*Time`
      are UPDATEs on the scope row → delete yields 0 rows, no FK error (per cgx).

## Phase 4: Verify

- [x] Focused `ginkgo ./atc/db/ ./atc/exec/` green (Phase 1, ef4fc3f070).
- [x] Triggered `k8s-e2e` on fresh code: behavioral #102 AND #103 (v35) both
      SUCCESS 298/0; "runs a pipeline with custom resource types" PASSES in both.
      (2 consecutive green runs as of 2026-05-31; a 3rd would fully confirm the
      flake is eliminated vs masked by the guard's graceful finish — the one
      remaining nice-to-have, accrues with normal nightly runs.)
- [x] Updated `topgun/k8s_behavioral/FAILURES.md` to reflect the green suite +
      the staleness root cause (cites #103).
- [x] Closed the superseded `resource_config_scope_gc_race_20260408` track
      (completed 2026-05-31, commit 1ac2631301).
- [x] Phase 3 defense-in-depth: native `lidar/scanner.go` FK handling + 12 specs
      (this track's lidar commit).

## Conclusion (2026-05-30)

ROOT CAUSE: CI image staleness, NOT the FK code. The worker served a cached
kind-runner image by tag, so the April FK-violation guards never executed in CI —
making the custom-resource-type behavioral spec flake. The fix shipped correctly
in April; it just never ran. Confirmed by: (1) atc/db regression test proving
IsForeignKeyViolation detects the real error; (2) behavioral #102 on fresh v35
code passing 298/0.

FIXES LANDED: kind-runner tag bump v33→v35 (cache-bust) + FK regression tests +
behavioral web-log instrumentation + integration daemon-security port-forward fix
+ integration failure diagnostics.

FOLLOW-UPS (own tracks): (a) CI reliability — replace mutable-tag cache-bust with
immutable tags/digests or always-pull, and address integration job OOM/resource
pressure; (b) optional: ≥3 consecutive green behavioral runs to fully confirm the
flake is eliminated.
