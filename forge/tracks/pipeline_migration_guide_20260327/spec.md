# Spec: pipeline_migration_guide

**Track ID:** `pipeline_migration_guide_20260327`
**Type:** docs

## Overview

Create a comprehensive pipeline migration guide that helps users (and AI agents) rebuild existing Concourse pipelines to leverage JetBridge Edition features. Existing pipelines are backward compatible and will run unchanged, but won't use new capabilities without modification.

The guide should be structured in tiers of increasing complexity, with concrete YAML examples and before/after diffs for each feature.

## Context

JetBridge adds these pipeline-relevant features on top of upstream Concourse v8.0.1:

- **Ephemeral storage quotas** — `ephemeral_storage` in `container_limits`/`container_requests`
- **Scratch paths** — `scratch_paths` for ephemeral emptyDir volumes on tasks
- **Task cache backends** — `hostpath`, `pvc`, `artifact`, `emptydir` selection
- **Inline sidecars** — define sidecar containers directly in pipeline YAML
- **Native registry-image resolution** — short-circuit get steps for K8s runtime
- **DaemonSet artifact backend** — node-local artifact caching for high-throughput pipelines
- **Direct image references** for custom resource types
- **OpenTelemetry tracing** — automatic, no pipeline changes needed

## Requirements

1. Tiered migration guide (zero-change → simple additions → restructuring → architecture rethink)
2. Concrete YAML before/after examples for every feature
3. Decision framework: "when should I use feature X?" guidance for each capability
4. Common migration patterns — e.g., replacing docker-in-docker with sidecars, replacing task cache hacks with scratch_paths
5. Agent-friendly format — structured enough that an AI agent can analyze a pipeline and suggest specific changes
6. Compatibility notes — what Garden-era patterns don't work on K8s and what replaces them

## Acceptance Criteria

- [ ] Tier 1 (zero-change) section documents benefits users get just by deploying JetBridge
- [ ] Tier 2 (simple additions) section covers ephemeral_storage, scratch_paths, and cache backend selection with YAML examples
- [ ] Tier 3 (restructuring) section covers sidecars, native image resolution, DaemonSet artifacts, and direct image references with YAML examples
- [ ] Tier 4 (architecture rethink) section covers Garden-specific patterns and their K8s-native replacements
- [ ] Each feature has a "when to use" decision guide
- [ ] At least 5 concrete before/after pipeline YAML examples
- [ ] Agent prompt template included — a structured prompt an AI agent can use to analyze any pipeline and suggest migration changes
- [ ] Guide tested by applying recommendations to at least 2 real-world pipeline examples

## Out of Scope

- Database migration (covered by `database_migration_runbook` track)
- Helm chart configuration or cluster setup
- Writing a CLI tool that auto-migrates pipelines (guide is manual/agent-assisted)
- Upstream Concourse feature documentation (only JetBridge-specific features)
