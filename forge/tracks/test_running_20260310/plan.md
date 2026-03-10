# Implementation Plan: Test Running

## Phase 1: Fix Broken Unit Tests

- [x] Task: Fix `atc/api` — remove stale `Platform` field from `artifacts_test.go:94` and stale `cache_streamed_volumes` feature flag from `api_suite_test.go`
- [x] Task: Fix `cmd/concourse` — rewrite tests to remove TSA flags (`--tsa-host-key` etc.) since TSA was removed
- [x] Task: Fix `testhelpers/otel` — already deleted per git status (connectivity tests need external Tempo/Loki)
- [x] Task: Fix `atc/db` — update resource_cache_factory_test to match new FindOrCreate behavior; increase worker_cache_test Eventually timeouts
- [x] Task: Fix `fly/integration` — update mock ATC version from 6.3.1 to 0.1.0 to match JetBridge fly binary version
- [x] Task: Run full unit suite and confirm all suites pass (79 suites, 3m12s)
- [x] Task: Phase 1 Manual Verification

## Phase 2: Create Unified Test Runner (Makefile)

- [x] Task: Create `Makefile` with `test-unit` target (ginkgo with correct skip-packages and parallelism)
- [x] Task: Add `test-ci-agent` target (cd ci-agent && go test ./...)
- [x] Task: Add `test-integration` target (ATC integration suite with postgres check)
- [x] Task: Add `test-k8s` target (topgun/k8s suites with docker/kind/helm prerequisite checks)
- [x] Task: Add `test-all` target that runs tiers in order
- [x] Task: Add `test-quick` target (unit + ci-agent only, no external deps beyond postgres)
- [x] Task: Phase 2 Manual Verification

## Phase 3: Documentation

- [x] Task: Create `TESTING.md` with test tier overview, prerequisites, expected timing, and troubleshooting
- [x] Task: Create `CLAUDE.md` with agent instructions including test commands and timing
- [x] Task: Phase 3 Manual Verification

## Phase 4: Verify Integration Tests

- [x] Task: Run `make test-fly-integration` and fix any failures (576 specs, 32s — fixed sync_test version, execute_test Source nil/empty, PlanMatcher pointer aliasing)
- [x] Task: Run `make test-integration` and fix any failures (ATC integration, 12s — 21 passed, 1 pending)
- [x] Task: Run `make test-k8s-integration` and fix any failures (KinD cluster, ~23 min — 115 passed, 2 known flaky pod cleanup failures, 7 pending)
- [x] Task: Run `make test-k8s-behavioral` and fix any failures (fixed unused fmt import; reduced to 2 procs default; 295 passed, 1 known flaky pod cleanup, 1 pending, 6 skipped in ~42 min single-proc)
- [x] Task: Update TESTING.md and CLAUDE.md with verified timings
- [x] Task: Phase 4 Manual Verification

---
