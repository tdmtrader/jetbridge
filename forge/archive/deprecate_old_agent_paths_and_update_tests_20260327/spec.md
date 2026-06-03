# Spec: Deprecate Old Agent Paths and Update Tests

**Track ID:** `deprecate_old_agent_paths_and_update_tests_20260327`
**Type:** refactor

## Overview

The ci-agent module has two parallel architectures: the OLD approach with 5 separate binaries (`ci-agent-review`, `ci-agent-fix`, `ci-agent-plan`, `ci-agent-implement`, `ci-agent-qa`), each with dedicated entry points, orchestrator packages, and adapter packages; and the NEW unified approach with a single `ci-agent --phase <path.yaml>` binary backed by `phaserunner/`, `phaseconfig/`, and declarative phase YAML configs.

The old binaries, orchestrators, wrapper scripts, and supporting infrastructure are no longer in use. They add maintenance burden, confuse onboarding, and bloat the Docker image. This track removes all old agent code paths and updates all tests, pipelines, Dockerfiles, and documentation to reference only the unified phase-based approach.

## Requirements

1. **Remove old binary entry points** — Delete all 5 old `cmd/ci-agent-*` directories and their `main.go` files.
2. **Remove wrapper scripts** — Delete `ci-agent/scripts/ci-agent-*` backward-compatibility wrappers.
3. **Remove compiled binaries** — Delete any checked-in old binaries from the `ci-agent/` directory.
4. **Remove old orchestrator packages** — Delete `ci-agent/orchestrator/`, `ci-agent/plan/`, `ci-agent/fix/`, `ci-agent/implement/` and all their sub-packages (adapters, renderers, confidence, etc.).
5. **Update CI task YAMLs** — Rewrite `ci/tasks/ci-agent-*.yml` to build and invoke the unified `ci-agent` binary with `--phase phases/<name>.yaml`.
6. **Update agent pipeline** — Rewrite `deploy/agent-pipeline.yml` to build/test the unified binary instead of 5 separate binaries.
7. **Update Dockerfile** — Rewrite `deploy/Dockerfile.ci-agent` to build and install only the unified `ci-agent` binary plus phase configs.
8. **Update integration tests** — Rewrite `ci-agent/integration/` tests to exercise the phase runner path instead of invoking old binaries.
9. **Update documentation** — Update `forge/product.md` and any other docs that reference old binary names to describe the unified approach only.
10. **Preserve shared packages** — Keep `ci-agent/schema/`, `ci-agent/tracing/`, `ci-agent/adapter/`, `ci-agent/envconfig/`, `ci-agent/llm/` and other packages used by the phase runner.

## Technical Approach

- Deletions first (phases 1-2): remove old code before updating references, so the compiler surfaces any missed imports.
- Pipeline/Docker/task updates (phase 3): rewrite to unified binary invocations.
- Test updates (phase 4): rewrite integration tests to use `ci-agent --phase` and verify phase runner output schemas.
- Docs last (phase 5): update all human-readable references.

## Acceptance Criteria

- [ ] No `cmd/ci-agent-*` directories exist (only `cmd/ci-agent/` and `cmd/validate-output/` remain).
- [ ] No `ci-agent/scripts/` directory exists.
- [ ] No `ci-agent/orchestrator/`, `ci-agent/plan/`, `ci-agent/fix/`, `ci-agent/implement/` directories exist.
- [ ] `go build ./cmd/ci-agent/` succeeds with no references to deleted packages.
- [ ] `go test ./...` in `ci-agent/` passes (phaserunner, phaseconfig, schema, integration tests).
- [ ] All `ci/tasks/ci-agent-*.yml` files invoke `ci-agent --phase` instead of old binaries.
- [ ] `deploy/agent-pipeline.yml` builds and tests only the unified binary.
- [ ] `deploy/Dockerfile.ci-agent` produces an image with only the `ci-agent` binary and phase configs.
- [ ] No documentation references old binary names as current/active (archived track docs are fine).

## Out of Scope

- Changes to the phase runner itself (`phaserunner/`, `phaseconfig/`, `phases/*.yaml`).
- Changes to the agent feedback API (`atc/api/agentfeedback/`).
- Changes to the `validate-output` command.
- Modifications to archived Forge track docs (`forge/archive/`).
- Adding new agent capabilities or phase types.
