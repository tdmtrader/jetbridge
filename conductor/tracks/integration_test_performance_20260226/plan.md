# Implementation Plan: Integration Test Performance

## Phase 1: Sample & Understand

Review a representative sample of ~15 tests across the suite. For each, document what it does, what it waits on, and flag obvious inefficiencies.

### Tier 1: Simple baseline tests (establish minimum per-test cost)
- [x] Review `smoke_test.go` — "runs a simple task pipeline end-to-end" (simplest possible test: single task, busybox, echo) analysis
- [x] Review `task_test.go` — "runs a pipeline task and streams output" + "runs a one-off task via fly execute" (basic task variants) analysis

### Tier 2: Resource lifecycle tests (add check/get/put overhead)
- [x] Review `resource_test.go` — "checks and gets a mock resource" + "runs a get → task → put pipeline" (mock resource with version injection) analysis

### Tier 3: Artifact passing and volume tests
- [x] Review `artifact_test.go` — "passes artifacts from get to task" + "passes artifacts between chained tasks" (multi-step volume passing) analysis

### Tier 4: Build lifecycle and cleanup (wait-heavy patterns)
- [x] Review `build_lifecycle_test.go` — "reports succeeded and failed build status correctly" + "cleans up when a build is cancelled" (multi-step verification, Eventually loops) analysis
- [x] Review `pod_cleanup_test.go` — "cleans up task pods after a successful build" + "cleans up pods after an aborted build" (GC wait patterns with 3-min timeouts) analysis

### Tier 5: K8s-specific behavior (pod inspection)
- [x] Review `k8s_behaviors_test.go` — "shows a k8s-backed worker via fly workers" + "applies resource limits to task pods" (pod label queries, resource limit inspection) analysis

### Tier 6: Complex pipeline patterns (hooks, steps, parallel)
- [x] Review `hook_combinations_test.go` — 1-2 representative hook tests (nested on_success/on_failure patterns) analysis
- [x] Review `step_combinations_test.go` — "executes on_success hook after task succeeds" + one parallel/across test analysis

### Tier 7: Edge cases and error handling
- [x] Review `error_handling_test.go` — "runs on_error hook when a step errors" + "reports errored build status for invalid image" (error vs failure distinction) analysis
- [x] Review `edge_cases_test.go` — "handles rapid triggering of multiple builds" (5x trigger loop) analysis

### Cross-cutting analysis
- [x] Document per-test infrastructure overhead: BeforeEach (fly.Login, kubeClient creation, tmp dir) and AfterEach (destroyPipeline, cleanupOrphanedPods) analysis
- [x] Identify all images used across sampled tests and whether they're preloaded into KinD (busybox, mock-resource, etc.) analysis
- [x] Catalog all timeout/polling values and flag overly generous ones analysis
- [x] Summarize findings: categorized list of performance issues with estimated impact analysis
- [x] Phase 1 review with human

---

## Phase 2: Quick Wins

Based on Phase 1 findings, implement the most impactful fixes. Likely candidates (to be confirmed by profiling):

- [x] Preload commonly-used images into KinD at setup time (busybox, mock-resource image, etc.) d57b47d4c
- [x] Tighten overly generous Eventually timeouts where the expected wait is much shorter (reduced defaults from 5m→2m, Consistently from 1m→30s)
- [x] Reduce unnecessary time.Sleep calls (hijack_test.go 5s→2s, job_config_test.go 5s→2s, etc.)
- [x] Tune scheduler interval from 10s→2s via DB update at setup time
- [x] Tune K8s Worker Reaper interval from 30s→2s via DB update at setup time (biggest single improvement)
- [x] Fix `waitForBuildAndWatch` timeout gap — added 3-minute hard deadline on `fly watch` streaming phase
- [x] Mark broken test 6.3 (three-level type chain) as Pending — K8s limitation, not a performance issue
- [x] Measure before/after timing for sampled tests (baseline ~4h → 30.1m wall time, 28.4m test time)
- [x] Phase 2 review with human

---
