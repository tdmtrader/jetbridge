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

## Key references
- `atc/exec/check_step.go:162-167, 254-262` — FK guards
- `atc/db/errors.go` — `IsForeignKeyViolation` (errors.As + SQLSTATE string fallback)
- `atc/db/resource_config_scope.go:91-149, 298-331` — SaveVersions / saveResourceVersion
- `atc/lidar/scanner.go:224, 399` — native-path SaveVersions (unguarded, logs+returns)
- Prior track: `forge/tracks/resource_config_scope_gc_race_20260408/`
