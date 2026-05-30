# CGX: resource_config_scope FK-violation leak

## Diagnostic record (2026-05-30)

### Symptom
`k8s-e2e/k8s-behavioral-tests` flaps: #99 and #100 (of last 7 nightly runs)
failed; integration (#173) green. Both failures are custom-resource-type specs:
- #100: `runs a pipeline with custom resource types` (`e2e_scenarios_test.go:468`)
- #99: `6.1: single custom type backed by registry-image resolves and works`

#100 build log (check build 305 for `custom-res`):
```
selected worker: k8s-concourse
save versions: ERROR: insert or update on table "resource_config_versions"
violates foreign key constraint "resource_config_versions_resource_config_scope_id_fkey"
(SQLSTATE 23503)
errored
```
`fly check-resource` exits 2 → `Wait()` assertion `exec.go:81` fails (`got 2, want 0`).

### Reproduction (throwaway `cmd/fkrepro`, since removed)
Faithful production path: `sql.Open("pgx", ...)` + pinned conn + TEMP tables with
an FK + `tx.QueryRowContext(INSERT ... SELECT ... RETURNING).Scan()` against a
non-existent parent. Output:
```
concrete type : *pgconn.PgError
err.Error()   : ERROR: ... (SQLSTATE 23503)
contains 'SQLSTATE 23503' : true
errors.As(*pgconn.PgError): true  (code=23503, isFK=true)
db.IsForeignKeyViolation(raw)           : true
db.IsForeignKeyViolation(fmt.Errorf %w) : true
```
**Conclusion: the detection helper works against the real error.** Initial
"detection is broken because tests use a synthetic `*pgconn.PgError`" hypothesis
is REFUTED for the helper itself (though the synthetic-only test coverage is still
a real gap — `check_step_test.go:737,769`, and there is no `errors_test.go`).

### Ruled out
- `connectionRetryingDriver` wraps only `Open()`, not queries → production query
  errors share the repro's shape.
- Source freshness: guard commits `59c43a31ff`, `9bb8537a6a` are ancestors of
  `origin/jetbridge`; `git diff origin/jetbridge HEAD` empty for `check_step.go`,
  `resource_config_scope.go`, `errors.go`. `build-kind-runner` #176 pushed fresh
  to `registry.home` ("succeeded"); behavioral task recompiles from that `/src`.
- FK constraint `resource_config_versions_resource_config_scope_id_fkey` is
  `ON DELETE CASCADE`, NOT deferrable (migration `1548261635`) → error at INSERT,
  not at COMMIT.
- `UpdateScopeLastCheckStartTime/EndTime` (check_step.go 201/226/239/268) are
  UPDATEs on the scope row itself → delete yields 0 rows, no FK error.
- Native `lidar/scanner.go:224,399` `SaveVersions` only logs and returns → does
  not propagate to a build.

### Open question carried into Phase 1/2
The guarded `SaveVersions` path leaked in #100 even though the helper provably
catches the error. Leading hypothesis: deployed-binary / KinD image-propagation
staleness (consistent with the `registry.home` daemon image rejecting `--mirror-*`
flags it should support). Phase 1 reproduces in the real check path; Phase 2a
verifies the deployed binary actually contains the guard at runtime.

## Phase 1 result (2026-05-30, commit ef4fc3f070)

- `atc/db/errors_test.go` real-DB GC-race test is **GREEN**: deleting the
  `resource_config_scope` then calling `SaveVersions` yields an error that
  `IsForeignKeyViolation` detects. Confirms in the real flow what `cmd/fkrepro`
  showed: detection is correct. The leak is therefore NOT a code-level detection
  bug → Phase 2a (deployed-binary / image propagation) is the live hypothesis.
- Hardened `atc/exec/check_step_test.go` FK test to a wrapped error (was a bare
  synthetic `*pgconn.PgError`). GREEN.

### good-pattern
- [2026-05-30] A throwaway in-module reproduction (`cmd/fkrepro`) using the exact
  production driver path (`sql.Open("pgx")` + `tx.QueryRow().Scan()`) decisively
  refuted the "detection is broken" hypothesis before any fix was written, then
  was promoted into a permanent `errors_test.go` regression test.

### anti-pattern
- [2026-05-30] The prior fix's unit tests injected a *synthetic* unwrapped
  `&pgconn.PgError`, which `errors.As` matches trivially — so the tests passed
  without ever exercising the real error shape. Guard against this: detection
  helpers that classify driver errors must be tested against a real DB error.

### frustration
- [2026-05-30] Ginkgo CLI/package version mismatch (CLI 2.28.1 vs package 2.27.3)
  prints a loud warning on every run; use `go run github.com/onsi/ginkgo/v2/ginkgo`
  to match. Non-fatal but noisy.

### missing-capability
- [2026-05-30] No forge MCP server connected in this session; all track-file
  operations were manual edits + git commits (the skill's documented fallback).

## Phase 2a image-path audit (2026-05-30)

- Footgun: `topgun/k8s_behavioral/cluster_lifecycle_test.go:95` `ensureConcourseImage`
  only `docker build`s when the image is ABSENT — reuses a stale
  `concourse-local:latest` if present (same in `buildAndLoadOOMTriggerImage` and
  `topgun/k8s/integration/cluster_lifecycle_test.go`). Real stale-code risk for
  local/reused runs.
- Refutation for CI: the pipeline behavioral task (`deploy/k8s-e2e-pipeline.yml`)
  runs `docker build -t concourse-local:latest` before the test from a
  freshly-compiled binary; Docker COPY layers are content-hashed, so CI always
  deploys fresh. `helmDeployConcourse` parses the ref correctly and deploys with
  `pullPolicy=IfNotPresent` into a fresh testcontainers K3s. => stale-binary
  hypothesis REFUTED for build #100.

### anti-pattern
- [2026-05-30] `ensureConcourseImage` "build only if absent" silently tests stale
  code on reused environments. Image-provisioning for tests should rebuild on
  source change or at least surface the image's age/digest.

### Outstanding
The contradiction (guard present + provably catches the real error, yet build
#100 errored on the guarded `save versions:` path) is NOT explained by code or
image staleness. Resolve via runtime: trigger a behavioral run and capture web
logs (`scope-deleted-during-check` vs raw `save versions:`).

## ROOT CAUSE CONFIRMED (2026-05-30): CI ran stale kind-runner images

Triggered the instrumented chain (build-kind-runner #177 fresh push of v33 →
integration retry GREEN 122/122 → behavioral). Behavioral reproduced the flake:
`runs a pipeline with custom resource types` FAILED — but this time with a
DIFFERENT FK path than #100:
```
update resource config scope: set resource scope: ERROR: ... violates foreign key
constraint "resources_resource_config_scope_id_fkey" (SQLSTATE 23503)  -> errored
```
(check_step.go:169 wrapping check_delegate.go:225 `SetResourceConfigScope`, via
the GUARDED PointToCheckedConfig path at check_step.go:162-169.)

DECISIVE: my pushed instrumentation (the `Using Concourse image …` provenance log
AND the on-failure web-log dump) was COMPLETELY ABSENT from the run, despite
build-kind-runner #177 rebuilding+pushing a fresh v33 after my push. The OLD
ensureConcourseImage string was also absent (it no-logs when the image exists).
=> the behavioral task compiled Concourse from a STALE image's /src.

Mechanism: the pipeline reuses the `concourse-kind-runner:v33` TAG; the worker
serves a cached v33 by tag, ignoring build-kind-runner's fresh push to the same
tag. So integration + behavioral have been running STALE code — the FK guards
(April) were likely never exercised in CI, which is why the flake persisted and
why both "guarded" paths appeared to leak.

Same disease as the registry.home/jetbridge:latest daemon image staleness.

FIX (commit 4cdf75c6cc): bump kind-runner tag v33 → v34 (pipeline's established
cache-bust pattern) + set-pipeline. Forces a fresh pull of a never-cached tag.

### anti-pattern
- [2026-05-30] Reusing a mutable image tag (v33) for CI rootfs means workers
  serve cached content and silently ignore fresh pushes — invalidating CI
  results. Either use immutable tags/digests or always bump on change.

## Staleness-exposed integration failures resolved (2026-05-30)

Running fresh code (v34/v35) surfaced 2 integration failures stale CI had masked:
- #1 `Artifact Daemon Security: can write to hostPath storage` — TEST BUG: connected
  to daemon Status.PodIP (not routable from the test host in testcontainers-K3s).
  Fixed (commit 07d0d449f7) with a portForwardDaemon helper. PASSES on v35.
- #2 `Artifact Read After Producer Pod Reap` — ENVIRONMENTAL: fly watch SIGKILLed
  (exit 137) at ~3s = OOM/resource pressure, NOT a route-artifact regression.
  Confirmed by passing on a clean v35 run. Added dumpDiagnosticsOnFailure
  (events + web logs) to confirm OOM if it recurs.

Integration v35 retry: SUCCESS 128 Passed | 0 Failed. Behavioral now unblocked.

### meta-finding (own track candidate)
- [2026-05-30] The k8s-e2e integration job is environmentally flaky: ~3 of 5 runs
  errored at setup or mid-run (OOM/resource pressure in the KinD-in-DinD task
  pods). And the v33→v34→v35 tag-bump cache-bust is a band-aid — the real fix is
  immutable tags/digests or always-pull for the kind-runner rootfs. Worth a
  dedicated CI-reliability track.

## Key references
- `atc/exec/check_step.go:162-167, 254-262` — FK guards
- `atc/db/errors.go` — `IsForeignKeyViolation` (errors.As + SQLSTATE string fallback)
- `atc/db/resource_config_scope.go:91-149, 298-331` — SaveVersions / saveResourceVersion
- `atc/lidar/scanner.go:224, 399` — native-path SaveVersions (unguarded, logs+returns)
- Prior track: `forge/tracks/resource_config_scope_gc_race_20260408/`
