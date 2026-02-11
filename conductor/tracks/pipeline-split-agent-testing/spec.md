# Spec: Pipeline Split — Separate Agent Testing

## Overview

Split the single `deploy/borg-pipeline.yml` into two independent pipelines:

1. **Primary pipeline** (`deploy/borg-pipeline.yml`) — Tests core JetBridge functionality: build, vet, unit tests, K8s runtime tests, K8s live tests, image build, deploy, and promote-to-main.
2. **Agent pipeline** (`deploy/agent-pipeline.yml`) — Tests all five CI agent binaries produce structurally valid output. Validates JSON schemas and exit codes, not model output quality.

Both pipelines share the same git resource (repo) but run independently. The primary pipeline gates deployments; the agent pipeline provides visibility into agent health.

## Requirements

1. **Extract agent jobs from primary pipeline**: Remove `ci-agent-review`, `agent-fix`, `agent-plan`, `agent-qa`, and `agent-implement` from `deploy/borg-pipeline.yml`.
2. **Simplify primary pipeline gating**: `promote-to-main` should only require `passed: [deploy]` (no agent dependency).
3. **Create agent pipeline** (`deploy/agent-pipeline.yml`) with:
   - Shared resource: same git repo resource definition (with `((github-pat))`)
   - A `build-agents` job that compiles all five agent binaries to validate they build
   - Individual jobs for each agent: `test-agent-review`, `test-agent-fix`, `test-agent-plan`, `test-agent-qa`, `test-agent-implement`
   - Each agent job: builds the binary, runs it with minimal/synthetic input, validates the output JSON against the schema's `.Validate()` rules
4. **Agent test validation criteria** (structural, not quality):
   - Binary compiles successfully
   - Binary exits without crash (exit 0 or known exit codes)
   - Output JSON file exists at expected path
   - Output JSON is valid (parseable, passes `schema.Validate()`)
   - For review: `review.json` has `schema_version`, `summary`
   - For fix: `fix-report.json` has `schema_version`, `metadata.repo`, `metadata.base_commit`
   - For plan/implement: `results.json` has `status` in {pass,fail,error,abstain}, `summary`, `confidence` 0-1, at least one artifact
   - For QA: `qa.json` has results with valid `status` enum, `score.max > 0`
5. **Write a Go validation tool** (`ci-agent/cmd/validate-output/main.go`) that reads an output directory and validates JSON files against schemas. Used by pipeline jobs to fail on invalid output.
6. **Set both pipelines on the cluster** — primary as `jetbridge`, agent as `jetbridge-agents`.

## Acceptance Criteria

- [ ] Primary pipeline has no agent jobs; deploys independently
- [ ] Agent pipeline validates all 5 agents produce structurally valid output
- [ ] `validate-output` tool exists and can validate review.json, fix-report.json, results.json, qa.json
- [ ] Both pipelines set successfully on the cluster via `fly set-pipeline`
- [ ] Agent pipeline jobs install Claude CLI and use `((anthropic-api-key))`, `((agent-model))` credentials
- [ ] Agent failures do not block deploy or promote-to-main

## Out of Scope

- Evaluating model output quality (score thresholds, issue accuracy, etc.)
- Agent pipeline triggering deploys or blocking promotions
- Multi-branch or PR-specific agent runs
- Agent pipeline deployment of agent Docker images
