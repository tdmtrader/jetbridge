# Spec: Resource History Preservation Across Config Changes

## Overview

When a Concourse resource's `type:` or `source:` parameters change, the resource loses all version history. This happens because versions are stored against a `resource_config_scope`, and any config change creates a new scope — the old scope (and all its versions) is CASCADE-deleted.

This makes routine operational changes destructive: switching a custom resource type to a built-in equivalent, adding or removing an auth parameter, or correcting a typo in source config all wipe the resource's version history. Build input/output records become unresolvable in the UI, pinned versions disappear, and operators lose the ability to roll back to a known-good version.

## Goals

1. **Prevent accidental history destruction** by soft-deleting resource config scopes instead of hard-deleting them.
2. **Enable explicit history migration** via a new `fly copy-resource-versions` command that backfills versions from an old scope into a new one.
3. **Preserve build history continuity** so that builds which referenced old versions continue to display correctly in the pipeline UI after migration.
4. **Allow version pinning to survive** config changes when the pinned version content exists in the new scope.

## Non-Goals

- Automatic/implicit history migration during `set-pipeline` (too much hidden magic, risky with global resources).
- Source parameter normalization or "non-semantic param" declarations (separate concern, can be a future track).
- Merging version histories from unrelated resources (only same-resource lineage).
- Migrating `next_build_inputs` FK references (self-heals on next scheduler tick).

## Requirements

### R1: Soft-Delete Resource Config Scopes

When a resource's config changes and a new scope is created, the old scope must NOT be CASCADE-deleted. Instead:

1. Add a `deprecated_at` timestamp column to `resource_config_scopes`.
2. When `findOrCreateResourceConfigScope()` would delete an old scope (in `atc/db/resource_config.go` lines 197-209), set `deprecated_at = now()` instead.
3. The resource row (`resources.resource_config_scope_id`) updates to the new scope as before.
4. Deprecated scopes and their versions remain queryable until GC cleans them up.

### R2: GC for Deprecated Scopes

Deprecated scopes must be cleaned up after a configurable grace period:

1. Add a new GC collector (or extend `ResourceConfigCollector`) that deletes scopes where `deprecated_at < now() - grace_period`.
2. Default grace period: 30 days (configurable via ATC flag `--deprecated-scope-grace-period`).
3. CASCADE delete still applies — when the scope row is deleted, its versions are removed.

### R3: `fly copy-resource-versions` Command

A new fly CLI command that copies version history from a deprecated scope to the resource's current scope:

```
fly -t <target> copy-resource-versions \
  --pipeline <pipeline> \
  --resource <resource>
```

Behavior:
1. Looks up the resource's current `resource_config_scope_id` (the target).
2. Finds deprecated scopes previously associated with this resource (via `resource_id` + `deprecated_at IS NOT NULL`).
3. If multiple deprecated scopes exist, lists them and asks the user to pick (or accepts `--from-scope <id>`).
4. Copies `resource_config_versions` rows from the source scope to the target scope:
   - Preserves `version`, `version_md5`, `version_sha256`, `metadata`, `check_order`, `span_context`.
   - Uses `ON CONFLICT (resource_config_scope_id, version_sha256) DO NOTHING` to skip duplicates.
5. Reports count of versions copied.

### R4: API Endpoint

New ATC API endpoint to support the fly command:

```
PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/copy-versions
Body: { "from_scope_id": <int> }
```

- Requires pipeline `operator` role (same as `check-resource`).
- Returns 200 with `{ "versions_copied": <count> }`.
- Returns 404 if source scope doesn't exist or isn't deprecated.
- Returns 409 if source scope belongs to a different resource.

### R5: Build History Continuity

After copying versions, existing build input/output records must resolve correctly:

- Build records reference versions by `(resource_id, version_digest)`.
- The UI resolves versions via `resources.resource_config_scope_id = versions.resource_config_scope_id`.
- Since copied versions have matching digests in the new scope, existing build records resolve without modification.

### R6: Pin Preservation

- `resource_pins` stores `(resource_id, version_json)` — no scope reference.
- After version copy, the pinned version content exists in the new scope, so pin resolution works.
- No changes needed to pin logic.

## Technical Approach

### Key Files

| Area | File | Change |
|------|------|--------|
| DB migration | `atc/db/migration/migrations/` | Add `deprecated_at` column to `resource_config_scopes` |
| Scope lifecycle | `atc/db/resource_config.go` | Soft-delete instead of hard-delete in `findOrCreateResourceConfigScope()` |
| Version copy | `atc/db/resource_config_scope.go` | New `CopyVersionsFrom(sourceScope)` method |
| GC | `atc/gc/resource_config_collector.go` | Collect deprecated scopes past grace period |
| API handler | `atc/api/resourceserver/copy_versions.go` | New endpoint handler |
| API routes | `atc/routes.go` | Register new route |
| Go client | `go-concourse/concourse/team.go` | `CopyResourceVersions()` method |
| Fly command | `fly/commands/copy_resource_versions.go` | New command |
| Fly registration | `fly/commands/fly.go` | Register command struct |

### Version Copy SQL

```sql
INSERT INTO resource_config_versions
  (resource_config_scope_id, version, version_md5, version_sha256, metadata, check_order, span_context)
SELECT $target_scope_id, version, version_md5, version_sha256, metadata, check_order, span_context
FROM resource_config_versions
WHERE resource_config_scope_id = $source_scope_id
ON CONFLICT (resource_config_scope_id, version_sha256) DO NOTHING
```

## Acceptance Criteria

1. Changing a resource's `type:` or `source:` in a pipeline does NOT delete the old scope's versions.
2. `fly copy-resource-versions` successfully copies versions from a deprecated scope to the current scope.
3. After copying, the pipeline UI shows correct build inputs/outputs for historical builds.
4. Pinned versions survive a config change + version copy.
5. Deprecated scopes are GC'd after the grace period.
6. Unit tests cover: soft-delete logic, version copy, GC collection, API authorization.
7. Integration test covers: full set-pipeline + copy-versions + verify build history flow.

## Out of Scope

- Source parameter normalization / non-semantic param stripping (future track).
- `set-pipeline --preserve-resource-history` flag (may add later, this track establishes the primitives).
- Cross-resource version copying (only same-resource deprecated scopes).
- Automatic migration of `next_build_inputs` FK references (scheduler self-heals).
