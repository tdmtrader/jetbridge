# Spec: Integration Test Performance

**Track ID:** `integration_test_performance_20260226`
**Type:** refactor

## Overview

The K8s behavioral integration tests (`topgun/k8s/integration/`) take ~4 hours to run 126 tests sequentially against a KinD cluster. At ~1.9 min/test average, there are likely obvious inefficiencies — image pulls, generous timeouts, unnecessary waits, per-test overhead — that can be addressed without changing what the tests validate.

This track takes a minimal first pass: sample representative tests, understand where time goes, and fix low-hanging fruit.

## Motivation

- **Primary pain point:** Local dev iteration speed. 4 hours for an integration suite means developers avoid running it.
- **Root cause unknown:** These tests were AI-generated and haven't been profiled. Before optimizing broadly, we need to understand the baseline.
- **Not switching to mocks:** Integration tests are inherently slower than unit tests, but minutes-per-test suggests fixable overhead, not fundamental limits.

## Requirements

1. Sample 10-20 tests across the suite's test files, covering the range of test patterns (simple tasks, resources, hooks, K8s-specific, cleanup, error handling)
2. For each sampled test, document: what it does, what pods/images it uses, what it waits for, and any obvious inefficiencies
3. Identify and categorize performance issues (image pulls, timeout padding, redundant setup, polling intervals, etc.)
4. Implement quick wins that reduce per-test time without changing test behavior
5. Measure before/after timing for the sampled tests

## Acceptance Criteria

- [x] 10-20 tests sampled and documented with timing analysis
- [x] Performance issues categorized with estimated impact
- [x] Quick wins implemented (at least the most impactful ones)
- [x] Before/after timing comparison for sampled tests
- [x] No tests broken or behavioral changes introduced

## Out of Scope

- Full suite profiling or optimization of all 126 tests
- Ginkgo parallelism (may be a follow-up)
- Switching to mocks or fake K8s APIs
- Restructuring the test infrastructure (TestMain, KinD lifecycle)
- Optimizing the one-time setup cost (cluster creation, Helm deploy)
