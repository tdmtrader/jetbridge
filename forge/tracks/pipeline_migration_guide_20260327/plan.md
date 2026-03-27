# Implementation Plan: pipeline_migration_guide

## Phase 1: Feature Inventory & Examples Research [checkpoint: b4b4bc9fc]

- [x] Task: Audit the JetBridge codebase for all pipeline-facing config changes — grep for new YAML fields in atc/task.go, atc/config.go, atc/resource_types.go, and related files; document the exact schema for each b4b4bc9fc
- [x] Task: Collect real-world pipeline examples from the repo (test fixtures, integration tests, CI pipelines) that demonstrate JetBridge features in use b4b4bc9fc
- [x] Task: Identify Garden-era patterns that break or degrade on K8s runtime — privileged containers, host volume mounts, docker-in-docker, btrfs cache assumptions b4b4bc9fc
- [x] Task: Phase 1 Manual Verification b4b4bc9fc

## Phase 2: Tier 1 & Tier 2 Guide Sections [checkpoint: b4b4bc9fc]

- [x] Task: Write Tier 1 (zero-change benefits) — document what improves just by deploying JetBridge: K8s-native execution, OTEL tracing, notification-driven scheduling, faster image resolution b4b4bc9fc
- [x] Task: Write Tier 2 (simple additions) — ephemeral_storage section with before/after YAML, explaining when and why to set storage quotas b4b4bc9fc
- [x] Task: Write Tier 2 — scratch_paths section with before/after YAML, showing replacement of task cache hacks for temp storage b4b4bc9fc
- [x] Task: Write Tier 2 — cache backend selection section with comparison table (hostpath vs pvc vs artifact vs emptydir) and when-to-use guidance b4b4bc9fc
- [x] Task: Phase 2 Manual Verification b4b4bc9fc

## Phase 3: Tier 3 Guide Sections [checkpoint: b4b4bc9fc]

- [x] Task: Write Tier 3 — inline sidecars section with before/after YAML showing docker-in-docker replacement, database service sidecars, and log forwarder patterns b4b4bc9fc
- [x] Task: Write Tier 3 — native registry-image resolution section explaining the short-circuit optimization and how to structure resource types to benefit b4b4bc9fc
- [x] Task: Write Tier 3 — DaemonSet artifact backend section with performance characteristics, when to enable, and pipeline patterns that benefit most (high fan-out, large artifacts) b4b4bc9fc
- [x] Task: Write Tier 3 — direct image references for custom resource types with before/after YAML b4b4bc9fc
- [x] Task: Phase 3 Manual Verification b4b4bc9fc

## Phase 4: Tier 4 & Agent Prompt [checkpoint: b4b4bc9fc]

- [x] Task: Write Tier 4 (architecture rethink) — catalog Garden-specific patterns and their K8s-native replacements: privileged containers → K8s security contexts, host mounts → hostPath/PVC, docker-compose tasks → sidecars, btrfs caches → cache backends b4b4bc9fc
- [x] Task: Write the agent prompt template — a structured prompt that takes a pipeline YAML as input and outputs tier-by-tier migration recommendations with concrete YAML diffs b4b4bc9fc
- [x] Task: Test the agent prompt against 2+ real pipeline YAMLs and refine based on output quality b4b4bc9fc
- [x] Task: Phase 4 Manual Verification b4b4bc9fc

## Phase 5: Polish & Validation [checkpoint: 3bd2b0bff]

- [x] Task: Add a quick-reference migration cheatsheet — single-page summary of all features, their YAML syntax, and one-line "when to use" guidance b4b4bc9fc
- [x] Task: Review all examples for correctness against actual JetBridge config parsing code b4b4bc9fc
- [x] Task: Final editorial pass for clarity, consistency, and completeness 3bd2b0bff
- [x] Task: Phase 5 Manual Verification 3bd2b0bff

---
