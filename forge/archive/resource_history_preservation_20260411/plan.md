# Plan: Resource History Preservation Across Config Changes

## Phase 1: Database Migration — Soft-Delete Infrastructure

### [x] 1.1 Write migration: add `deprecated_at` to `resource_config_scopes` 137b778ba9
- New migration file in `atc/db/migration/migrations/`
- `ALTER TABLE resource_config_scopes ADD COLUMN deprecated_at TIMESTAMP WITH TIME ZONE`
- Add index: `CREATE INDEX ON resource_config_scopes (deprecated_at) WHERE deprecated_at IS NOT NULL`
- Down migration drops the column

### [x] 1.2 Update `findOrCreateResourceConfigScope()` to soft-delete 137b778ba9
- File: `atc/db/resource_config.go` lines 197-209
- Replace `DELETE FROM resource_config_scopes WHERE resource_id = X AND resource_config_id != Y` with `UPDATE ... SET deprecated_at = now()`
- Also set `resource_id = NULL` on the deprecated scope (so it no longer blocks the unique constraint)
- Add test: scope transition marks old scope deprecated instead of deleting it

### [x] 1.3 Exclude deprecated scopes from active queries 137b778ba9
- Ensure `findOrCreateResourceConfigScope()` ignores deprecated scopes when checking for existing
- Add `WHERE deprecated_at IS NULL` to scope lookup queries
- Verify resource check scheduling skips deprecated scopes

## Phase 2: Version Copy Mechanism

### [x] 2.1 Write `CopyVersionsFrom()` on ResourceConfigScope 137b778ba9
- File: `atc/db/resource_config_scope.go`
- Method signature: `CopyVersionsFrom(sourceScopeID int) (int, error)` returning count copied
- SQL: INSERT ... SELECT with ON CONFLICT DO NOTHING
- Preserve: version, version_md5, version_sha256, metadata, check_order, span_context
- Write unit tests: copy with duplicates, copy from empty scope, copy preserves check_order

### [x] 2.2 Write `DeprecatedScopes()` on Resource 63f8ac81bd
- File: `atc/db/resource.go`
- Method: `DeprecatedScopes() ([]ResourceConfigScope, error)`
- Query: `SELECT * FROM resource_config_scopes WHERE resource_id = $1 AND deprecated_at IS NOT NULL` — wait, resource_id is NULLed in 1.2
- Alternative: Track provenance. Add `deprecated_from_resource_id` column in migration, set during soft-delete. Query on that.
- Update migration in 1.1 to also add `deprecated_from_resource_id INT REFERENCES resources(id) ON DELETE SET NULL`
- Write unit tests: returns deprecated scopes, empty when none exist

## Phase 3: GC for Deprecated Scopes

### [x] 3.1 Add `--deprecated-scope-grace-period` ATC flag 63f8ac81bd
- File: `atc/atccmd/command.go`
- Default: 30 days (`720h`)
- Wire into GC component configuration

### [x] 3.2 Extend GC to collect deprecated scopes 63f8ac81bd
- File: `atc/gc/resource_config_collector.go` (or new `deprecated_scope_collector.go`)
- Query: `DELETE FROM resource_config_scopes WHERE deprecated_at IS NOT NULL AND deprecated_at < now() - $grace_period`
- CASCADE handles version deletion
- Write unit test: scopes within grace period preserved, expired ones deleted

## Phase 4: API Endpoint

### [x] 4.1 Register route 4aa24cc346
- File: `atc/routes.go`
- Add: `PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/resources/:resource_name/copy-versions`
- Handler name: `CopyResourceVersions`

### [x] 4.2 Implement API handler 4aa24cc346
- File: `atc/api/resourceserver/copy_versions.go` (new)
- Parse request body: `{ "from_scope_id": int }`
- Validate: source scope exists, is deprecated, belongs to this resource (via `deprecated_from_resource_id`)
- Call `scope.CopyVersionsFrom(fromScopeID)`
- Return `{ "versions_copied": count }`
- Require pipeline `operator` role (same as CheckResource)
- Write unit tests: happy path, scope not found, scope not deprecated, wrong resource, auth

## Phase 5: Fly CLI Command

### [x] 5.1 Add `CopyResourceVersions()` to go-concourse client 897de8877c
- File: `go-concourse/concourse/team.go`
- Method calls PUT endpoint from Phase 4
- Returns versions copied count

### [x] 5.2 Implement `fly copy-resource-versions` command 897de8877c
- File: `fly/commands/copy_resource_versions.go` (new)
- Register in `fly/commands/fly.go`
- Flags: `--resource PIPELINE/RESOURCE` (required), `--from-scope ID` (optional)
- Behavior without `--from-scope`: list deprecated scopes, prompt user to pick
- Behavior with `--from-scope`: copy directly
- Print result: "Copied N versions from scope X to current scope Y"

### [x] 5.3 Write fly integration test cc824f6ee2
- File: `fly/integration/copy_resource_versions_test.go` (new)
- Test against mock ATC: successful copy, scope not found error, unauthorized

## Phase 6: End-to-End Validation

### [x] 6.1 Integration test: full flow cc824f6ee2
- Set pipeline with resource A (type: custom-registry-image, source with useGoogleAuth)
- Trigger check, accumulate versions
- Run a build that uses resource A as input
- Set pipeline again with resource A (type: registry-image, source without useGoogleAuth)
- Verify old scope is deprecated (not deleted)
- Call copy-versions API
- Verify: versions exist in new scope, build history resolves, pin works

### [x] 6.2 Integration test: GC respects grace period 63f8ac81bd
- Create deprecated scope
- Run GC with short grace period — verify NOT collected (within period)
- Advance time past grace period
- Run GC — verify collected

## Phase 7: MCP Server Integration

### [x] 7.1 Add list_deprecated_scopes and copy_resource_versions MCP tools 74de487c53
- File: `atc/api/mcpserver/tools.go`
- list_deprecated_scopes: Lists soft-deleted scopes for a resource
- copy_resource_versions: Copies versions with ownership validation
- Tests cover: happy paths, empty results, invalid scope
