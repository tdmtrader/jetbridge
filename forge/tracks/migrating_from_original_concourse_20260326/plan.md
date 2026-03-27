# Implementation Plan: Migrating from original concourse

## Phase 1: Database Migration Runbook [checkpoint: 4ab0d3297]

Child track: `database_migration_runbook_20260327`

- [x] Task: Complete all implementation tasks (schema analysis, pre-flight script, runbook, validation, cleanup, test fixture) c315b2c06
- [x] Task: Replace manual verifications with automated integration tests in atc/db/migration/legacy_upgrade_test.go covering: pre-flight script, garden cleanup SQL, migration idempotency, and rollback path 4ab0d3297

## Phase 2: Pipeline Migration Guide [checkpoint: 3bd2b0bff]

Child track: `pipeline_migration_guide_20260327`

- [x] Task: Complete all implementation tasks and manual verifications 3bd2b0bff

---
