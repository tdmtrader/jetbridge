# Spec: Migrating from Original Concourse

**Track ID:** `migrating_from_original_concourse_20260326`
**Type:** docs

## Overview

Umbrella track for all migration documentation needed to move from a legacy Concourse instance to JetBridge Edition. Covers both historical data migration and pipeline modernization.

This track is complete when both child tracks are complete:
1. `database_migration_runbook_20260327` — Database migration runbook, pre-flight script, validation queries
2. `pipeline_migration_guide_20260327` — Pipeline migration guide with tiered recommendations and agent prompt

## Requirements

1. Database migration path documented and tested for v7.x and v8.x source versions
2. Pipeline migration guide with concrete YAML before/after examples
3. Agent-friendly prompt template for automated pipeline analysis
4. Pre-flight validation tooling for database migration

## Acceptance Criteria

- [ ] Database migration runbook complete with all manual verifications passed
- [ ] Pipeline migration guide complete (all phases checkpointed)
- [ ] Both child tracks marked complete

## Out of Scope

- Automated migration tooling (CLI tool that auto-migrates pipelines)
- Helm chart or infrastructure migration
- Concourse versions older than v6.x
