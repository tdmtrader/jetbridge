# Spec: Fix resource_config_scope GC Race Condition

## Overview

The GC's `CleanUnreferencedConfigs` collector races with active check builds. When GC deletes a `resource_configs` row, the `ON DELETE CASCADE` on `resource_config_scopes` destroys the scope. If a check is concurrently running `SaveVersions` or `SetResourceConfigScope` against that scope, the FK constraint violation crashes the check build.

This is reproducible in the K8s behavioral test suite — the `"runs a pipeline with custom resource types"` test fails every run with:

```
save versions: ERROR: insert or update on table "resource_config_versions"
violates foreign key constraint "resource_config_versions_resource_config_scope_id_fkey" (SQLSTATE 23503)
```

## Root Cause Analysis

### The Race Window

1. **Check acquires scope:** `check_step.go:136` — `FindOrCreateScope(resourceConfig)` returns a scope ID in one transaction.
2. **Check points resource:** `check_step.go:144` — `PointToCheckedConfig(scope)` links the resource to the scope.
3. **Check runs:** `check_step.go:189` — `runCheck()` executes the actual resource check, getting versions.
4. **GC deletes config:** Between steps 2-4, `CleanUnreferencedConfigs` in `resource_config_factory.go:266-274` deletes the `resource_config` row. The `ON DELETE CASCADE` FK from `resource_config_scopes` (migration `1548261635`, line 5) cascades, destroying the scope.
5. **Check saves versions:** `check_step.go:214` — `scope.SaveVersions()` tries to INSERT into `resource_config_versions` referencing the now-deleted scope → FK violation.

### Why Custom Resource Types Are Vulnerable

Custom resource types create a chain: `registry-image` → `custom-mock` → `custom-res`. Each level has its own `resource_config` and scope. When pipelines are rapidly created and destroyed (as in behavioral tests), the intermediate configs become unreferenced and eligible for GC cleanup.

Additionally, `FindOrCreateScope` (resource_config.go:197-209) explicitly deletes old scopes when a resource's config changes — this can race with concurrent checks from the lidar scanner.

### Existing Precedent

The codebase already handles this exact pattern in two places:
- `resource_config_factory.go:276-279`: `CleanUnreferencedConfigs` silently swallows FK violations when a reference is created concurrently.
- `resource_cache_lifecycle.go:149-152`: `CleanUselessResourceCaches` treats FK violations as expected edge cases.

## Requirements

1. `SaveVersions` FK violations must not crash check builds — the check should log a warning and allow the next check cycle to retry with a fresh scope.
2. `SetResourceConfigScope` FK violations must not crash check builds — same treatment.
3. The fix must follow the existing codebase pattern of treating FK violations as transient/expected.
4. Unit tests must cover both FK violation paths.
5. The behavioral test `"runs a pipeline with custom resource types"` must pass reliably.

## Technical Approach

**Handle FK violations in `check_step.go` as non-fatal errors.**

In `check_step.go`, wrap `SaveVersions` (line 214) and `PointToCheckedConfig` (line 144) with FK violation detection. When a `pgerrcode.ForeignKeyViolation` (SQLSTATE 23503) is detected:
- Log a warning ("scope deleted during check, will retry on next cycle")
- Return `false, nil` (check did not succeed, but no error to propagate)

This is consistent with the existing patterns in `resource_config_factory.go` and `resource_cache_lifecycle.go`.

**Why not retry in-place?** Retrying would require re-creating the scope, re-running the check, and re-saving versions — essentially a full check cycle restart. The scheduler already triggers checks periodically, so the next cycle will naturally retry with a fresh scope. The simpler approach is more robust and lower risk.

## Acceptance Criteria

- [ ] FK violations from `SaveVersions` are handled gracefully (logged, not propagated as error)
- [ ] FK violations from `PointToCheckedConfig` are handled gracefully
- [ ] Existing unit tests in `check_step_test.go` continue to pass
- [ ] New unit tests cover both FK violation scenarios
- [ ] K8s behavioral test suite passes with 0 failures from this race condition
- [ ] No regressions in `make test-unit`

## Out of Scope

- Redesigning the GC collector's scope cleanup strategy
- Adding row-level locking between checks and GC
- Changing the FK cascade behavior in migrations
- Fixing the `FindOrCreateScope` internal DELETE race (separate, less impactful issue)
