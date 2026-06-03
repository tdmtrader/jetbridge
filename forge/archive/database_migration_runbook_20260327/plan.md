# Implementation Plan: database_migration_runbook

## Phase 1: Research & Schema Analysis

- [x] Task: Catalog the full migration delta between upstream v8.0.1 and JetBridge HEAD — list every migration file, what it changes, and whether it's destructive or additive c315b2c06
- [x] Task: Identify which tables contain Garden-specific data (workers, containers, volumes) that becomes dead weight after migration to K8s runtime c315b2c06
- [x] Task: Document the migration engine behavior — how Concourse discovers and applies pending migrations on startup, and how to run them independently c315b2c06

## Phase 2: Pre-flight Validation Script

- [x] Task: Write a shell script (`migrate-preflight.sh`) that connects to the source DB, reads the `schema_migrations` table, determines current version, and reports the migration gap c315b2c06
- [x] Task: Add version-path validation — map known Concourse releases to expected migration numbers, flag if source version is unrecognized or too old (< v6.x) c315b2c06
- [x] Task: Add basic data integrity checks — row counts on key tables, check for orphaned records, verify no in-progress migrations c315b2c06
- [x] Task: Phase 2 Manual Verification d6f302b9e

## Phase 3: Migration Runbook Document

- [x] Task: Write the runbook document covering: prerequisites, backup procedure (pg_dump flags and options), migration execution steps, and monitoring guidance c315b2c06
- [x] Task: Add version-specific migration paths — instructions for v7.x (need intermediate migrations), v8.0.0 (minor gap), and v8.0.1 (minimal gap) c315b2c06
- [x] Task: Document the md5→sha256 migration specifically — what it rehashes, expected duration for large instances, and how to verify completion c315b2c06
- [x] Task: Write rollback procedure — pg_restore steps, verification, and how to restart the old Concourse version against the restored DB c315b2c06
- [x] Task: Phase 3 Manual Verification d6f302b9e

## Phase 4: Post-Migration Validation & Cleanup

- [x] Task: Write post-migration SQL validation queries — row counts, schema version check, sample data spot-checks on builds/jobs/pipelines c315b2c06
- [x] Task: Write cleanup SQL for Garden-era stale data — worker records, container records, volume records that reference Garden workers c315b2c06
- [x] Task: Document what happens on first JetBridge boot against the migrated DB — K8s worker registration, component initialization, expected log output c315b2c06
- [x] Task: Phase 4 Manual Verification d6f302b9e

## Phase 5: Testing & Polish

- [x] Task: Create a test fixture (pg_dump or SQL script) representing a realistic legacy Concourse DB and run the full migration + validation flow against it 3f975f63e
- [x] Task: Review and polish all documents for clarity, accuracy, and completeness 3f975f63e
- [x] Task: Phase 5 Manual Verification d6f302b9e

---
