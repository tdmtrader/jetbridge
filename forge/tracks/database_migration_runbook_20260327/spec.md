# Spec: database_migration_runbook

**Track ID:** `database_migration_runbook_20260327`
**Type:** docs

## Overview

Create a comprehensive runbook for migrating historical data from a legacy Concourse instance (v7.x or v8.x) to JetBridge Edition. The schema delta from upstream v8.0.1 is small (incremental migrations, not a rewrite), so the approach is direct database migration rather than export/import ETL.

The runbook should be usable by an operator with Concourse and PostgreSQL experience, and also by an AI agent assisting with migration.

## Context

JetBridge is forked from Concourse v8.0.1. The schema changes are:
- `1773105500` — drop `component.interval` and `component.last_ran`
- `1773105501` — drop `component.paused`
- `1773104944` — simplify worker cache triggers
- `1666754000` — add `invalid_since` to worker resource caches
- `1747084615` — switch md5 to sha256 for signing keys
- `1746768931` — add signing keys table

Core tables (builds, jobs, pipelines, resources, teams) are structurally unchanged. Worker records become irrelevant (K8s workers replace Garden workers).

## Requirements

1. Pre-flight validation script that checks source Concourse version, database connectivity, and migration path viability
2. Step-by-step runbook covering: backup, version-gap migrations (if source < v8.0.1), JetBridge migration application, and post-migration validation
3. Post-migration validation queries that verify row counts and data integrity on key tables (builds, jobs, pipelines, resources, teams, resource_configs)
4. Documented rollback procedure (restore from pg_dump)
5. Guidance on cleaning up stale Garden worker records and container/volume rows that no longer apply in K8s runtime
6. Version-specific notes for common source versions (v7.x, v8.0.0, v8.0.1)

## Acceptance Criteria

- [ ] Pre-flight script exists and validates source version, DB connectivity, and migration path
- [ ] Runbook covers full migration lifecycle: pre-flight → backup → migrate → validate → cleanup
- [ ] Post-migration validation queries confirm data integrity
- [ ] Rollback procedure documented and tested against a sample migration
- [ ] Stale data cleanup guidance for Garden-era worker/container/volume rows
- [ ] Runbook tested against a pg_dump from a v7.x or v8.x Concourse instance (or realistic fixture)

## Out of Scope

- Full export/import ETL tooling (schema is compatible enough for direct migration)
- Pipeline YAML migration (covered by separate `pipeline_migration_guide` track)
- Helm chart or infrastructure migration (deployment topology is separate concern)
- Migration from Concourse versions older than v6.x
