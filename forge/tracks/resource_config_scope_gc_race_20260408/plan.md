# Plan: Fix resource_config_scope GC Race Condition

## Phase 1: Add FK Violation Helper

- [x] Create `IsForeignKeyViolation(err)` helper in `atc/db/errors.go`
  - Unwraps error chain via `errors.As`, checks for `*pgconn.PgError` with code `pgerrcode.ForeignKeyViolation`

## Phase 2: Handle FK Violations in Check Step

- [x] Handle FK violation from `SaveVersions` in `atc/exec/check_step.go`
  - Catch FK violation from `scope.SaveVersions()`
  - Log info: "scope-deleted-during-check"
  - Call `delegate.Finished(logger, false)` and return `false, nil` (non-fatal)
  - Write unit test in `atc/exec/check_step_test.go`

- [x] Handle FK violation from `PointToCheckedConfig` in `atc/exec/check_step.go`
  - Catch FK violation from `delegate.PointToCheckedConfig(scope)`
  - Log info: "scope-deleted-before-check"
  - Return `false, nil` (non-fatal)
  - Write unit test in `atc/exec/check_step_test.go`

## Phase 3: Verify

- [x] Run focused FK violation tests — 4/4 passed
- [ ] Run K8s behavioral test suite — "runs a pipeline with custom resource types" must pass
