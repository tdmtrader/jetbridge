# Spec: Resolve resource_config_scope FK-violation leak in custom-resource-type checks

**Track ID:** `resource_config_scope_fk_leak_fix_20260530`
**Type:** bugfix
**Supersedes / continues:** `resource_config_scope_gc_race_20260408`

## Overview

The K8s behavioral spec **"runs a pipeline with custom resource types"**
(`topgun/k8s_behavioral/e2e_scenarios_test.go:468`) fails intermittently
(builds #99 and #100, ~2 of last 7 nightly runs). `fly check-resource` on a
registry-image-backed custom resource type exits 2 because the check errors with:

```
save versions: ERROR: insert or update on table "resource_config_versions"
violates foreign key constraint "resource_config_versions_resource_config_scope_id_fkey"
(SQLSTATE 23503)
```

This is the GC race the prior track (`resource_config_scope_gc_race_20260408`,
commits `59c43a31ff` + `9bb8537a6a`) set out to fix: GC deletes a
`resource_config_scope` mid-check, so the subsequent `INSERT` into
`resource_config_versions` fails its FK. The prior fix added a guard in
`atc/exec/check_step.go` (`SaveVersions` line 254-261 and `PointToCheckedConfig`
line 162-167) that calls `db.IsForeignKeyViolation(err)` and, on a hit, finishes
the check gracefully (`delegate.Finished(logger, false)`, `return false, nil`).

## What the 2026-05-30 investigation established

Empirically (throwaway `cmd/fkrepro`, faithful `sql.Open("pgx", ...)` +
`tx.QueryRow(INSERT ... RETURNING).Scan()` against local Postgres):

- The real production FK error is a clean `*pgconn.PgError`; `err.Error()`
  contains `"SQLSTATE 23503"`.
- `errors.As(err, &pgconn.PgError)` returns **true**.
- `db.IsForeignKeyViolation(err)` returns **true** — both raw and after
  `fmt.Errorf("save versions: %w", err)` wrapping.

So **the detection helper is NOT broken**, contradicting the initial hypothesis.
Additional facts ruled out:

- `connectionRetryingDriver` wraps only `Open()` (connection establishment),
  not queries — so production query errors have the same shape as the repro.
- The behavioral build #100 ran a freshly-built binary: `build-kind-runner` #176
  pushed a fresh image to `registry.home` ("succeeded"), and the behavioral task
  recompiles `./cmd/concourse` from that image's `/src`. The guard commits are
  ancestors of `origin/jetbridge`; `git diff origin/jetbridge HEAD` is empty for
  `check_step.go`, `resource_config_scope.go`, `errors.go`.
- The FK constraint is `ON DELETE CASCADE`, **not** deferrable, so the violation
  surfaces at the `INSERT` statement (in `saveResourceVersion`), not at
  `tx.Commit()`.
- The only FK-violation surface that propagates to the build is `SaveVersions`
  (guarded) and `PointToCheckedConfig` (guarded). The `UpdateScopeLastCheck*Time`
  calls are UPDATEs on the scope row itself (delete → 0 rows, no FK error). The
  native `lidar/scanner.go:224,399` `SaveVersions` only logs `failed-to-save-versions`
  and returns (does not propagate to a build).

## The remaining contradiction

Statically the guard should catch the #100 failure, and the helper provably does.
Yet the build still errored on the guarded `save versions:` path. The unresolved
question is **why the deployed guard did not fire**, which the static analysis
cannot answer. Leading hypotheses, in priority order:

1. **Deployed-binary / image-propagation mismatch** — the KinD behavioral harness
   may have run a stale `concourse-local:latest` (image load/caching), so the
   guard was not actually present at runtime. This is consistent with the broader
   `registry.home` image-staleness observed in the daemon (the published image
   rejects `--mirror-*` flags it should support).
2. **A genuine runtime path that bypasses the guard** under real concurrency that
   the synthetic unit test (which injects a bare `&pgconn.PgError`) never exercises.

## Requirements

1. Reproduce the failure against a **real** database in the **real** check path
   (not a synthetic injected error) — drive `check_step` (or the check flow) with
   a `resource_config_scope` deleted mid-check, and observe whether the guard fires.
2. Determine definitively whether the behavioral failure is (a) a deployed-binary
   staleness/image-propagation issue or (b) a code path that bypasses the guard.
3. Whichever it is, make the fix effective end-to-end: the custom-resource-type
   behavioral spec must pass reliably.
4. Close the remaining FK-race gaps for defense in depth: the native
   `lidar/scanner.go` `SaveVersions` path and any other scope-referencing INSERT
   reachable during a check.
5. Add a real (non-synthetic) regression test for `IsForeignKeyViolation` and for
   the check-step graceful-handling path.

## Acceptance Criteria

- [ ] An integration test (real Postgres) triggers a genuine 23503 through the
      production connection path and asserts `IsForeignKeyViolation` returns true
      (locks in the proven behavior; guards against regressions in wrapping).
- [ ] An integration/behavioral test drives the real check flow with a
      concurrently-deleted scope and asserts the check finishes gracefully
      (build not `errored`).
- [ ] Root cause of the deployed leak is identified and documented in `cgx.md`
      (stale-binary vs code-path-bypass), with the corresponding fix applied.
- [ ] Native `lidar/scanner.go` `SaveVersions` FK handling is consistent and
      documented (silent drop is intentional, or guarded).
- [ ] `topgun/k8s_behavioral` "runs a pipeline with custom resource types" and
      "6.1 single custom type backed by registry-image" pass ≥3 consecutive
      triggered runs.
- [ ] `topgun/k8s_behavioral/FAILURES.md` updated to reflect the resolved state.

## Out of Scope

- Redesigning GC ordering or scope lifecycle.
- The chart/image `--mirror-*` mismatch (tracked separately) — except insofar as
  diagnosing image-propagation reliability overlaps with hypothesis (1).
- Adding test-side retry wrappers around `fly check-resource` as the primary fix
  (acceptable only as belt-and-suspenders after the real fix lands).
