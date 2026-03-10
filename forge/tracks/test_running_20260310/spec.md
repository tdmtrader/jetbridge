# Spec: Test Running

**Track ID:** `test_running_20260310`
**Type:** refactor

## Overview

Test running is unreliable and confusing. There are 95 test suites with no unified runner, inconsistent parallelism settings, and no clear documentation. Sometimes tests take 5 minutes, sometimes hours, depending on which suites get included. Multiple unit suites fail due to stale code references and missing infrastructure, and integration suites have never been verified end-to-end.

## Requirements

1. **Fix all broken unit tests** — The 4 failing suites (`atc/api`, `atc/db`, `cmd/concourse`, `testhelpers/otel`) must either pass or be properly gated behind build tags/env vars.
2. **Verify all integration tests** — Run and fix fly integration (576 specs), ATC integration (real Postgres + ATC), K8s integration (KinD), and K8s behavioral (parallel KinD clusters).
3. **Create a unified test runner** — A Makefile with clear targets (`test-unit`, `test-integration`, `test-k8s`, `test-all`) that handles setup/teardown.
4. **Improve test self-containment** — Each suite should handle its own prerequisites or fail fast with a clear error message.
5. **Document the test suite** — A `TESTING.md` at the repo root and `CLAUDE.md` agent instructions explaining how to run each tier of tests, prerequisites, and expected timing.

## Technical Approach

### Broken test fixes:
- `atc/api`: Remove stale `Platform` field from `worker.Spec` struct literal in `artifacts_test.go:94`
- `cmd/concourse`: Tests reference removed `--tsa-host-key` flags from legacy TSA removal — delete or rewrite these tests
- `testhelpers/otel`: Connectivity tests require external Tempo/Loki — gate behind `OTEL_CONNECTIVITY_TEST=1` env var or build tag
- `atc/db`: Fix resource_cache_factory_test (FindOrCreate now auto-creates), fix worker_cache_test (SQL GROUP BY excludes 0-count workers)

### Integration test verification:
- `fly/integration`: Self-contained (mock HTTP), verify 576 specs pass after version fix (0.1.0)
- `atc/integration`: Requires PostgreSQL, launches real ATC process per test, parallelizable via port offsets
- `topgun/k8s/integration`: Single shared KinD cluster, Helm deploy, NOT parallelizable (~30 min)
- `topgun/k8s_behavioral`: Per-process KinD clusters, fully parallelizable (~2-3 hrs with 4 procs)

### Unified runner:
- Makefile at repo root with tiered targets
- Each target declares its own prerequisites and skips gracefully if missing
- Parallelism settings baked into each target based on suite requirements

### Documentation:
- `TESTING.md` covering all tiers, prerequisites, expected timing, and common issues
- `CLAUDE.md` with agent-oriented instructions including timing expectations

## Acceptance Criteria

- [x] All unit test suites pass (79 suites, ~3 min)
- [x] `make test-unit` runs all unit tests with correct parallelism
- [x] `make test-ci-agent` runs ci-agent module tests
- [x] `make test-fly-integration` verified (576 specs pass)
- [x] `make test-integration` verified (ATC integration passes)
- [x] `make test-k8s-integration` verified (K8s integration passes)
- [x] `make test-k8s-behavioral` verified (K8s behavioral passes)
- [x] `TESTING.md` exists with clear instructions for each test tier
- [x] `CLAUDE.md` exists with agent instructions including timing
- [x] Each test tier is self-contained (handles its own setup/teardown or fails fast)

## Out of Scope

- CI pipeline changes
- Writing new tests for untested code
- Refactoring test helper libraries
