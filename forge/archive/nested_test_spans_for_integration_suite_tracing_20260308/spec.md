# Spec: Nested test spans for integration suite tracing

**Track ID:** `nested_test_spans_for_integration_suite_tracing_20260308`
**Type:** feature

## Overview

The OTel test tracing pipeline emits a single flat `test.run` span per test case. When a test takes 22 seconds, there's no way to tell whether 18s was waiting for a build, 2s was setting a pipeline, or 1s was a fly login. This track adds nested child spans so that each major operation within a test (set-pipeline, trigger-job, watch-build, etc.) appears as a child span under the parent `test.run` span, giving immediate visibility into where time is spent.

## Current State

- `emitTestSpan()` in `testhelpers/otel/otel.go` creates a single span *retroactively* in `ReportAfterEach` after the test finishes
- No active span context exists during test execution, so helpers like `setAndUnpausePipeline`, `triggerJob`, `waitForBuildAndWatch` cannot produce child spans
- Result: Grafana shows one bar per test with duration and pass/fail, but zero internal breakdown

## Requirements

1. **Live parent span** — Start a `test.run` span *before* the test runs (in `BeforeEach`), store it in a context, and finalize it in `ReportAfterEach` with status/attributes. This provides an active parent for child spans.
2. **Child spans for test helpers** — Instrument the shared test helpers to create child spans under the active parent:
   - `setAndUnpausePipeline()` → `fly.set-pipeline` + `fly.unpause-pipeline`
   - `triggerJob()` → `fly.trigger-job`
   - `waitForBuildAndWatch()` → `fly.watch` (usually the longest operation)
   - `fly.Run()` / `fly.Start()` → `fly.exec` with command args as attributes
   - `newMockVersion()` → `fly.check-resource`
3. **Implicit context threading** — Use a package-level `var testCtx context.Context` set in `BeforeEach` so helpers pick it up automatically. This avoids changing every individual test file.
4. **Ginkgo `By()` events** — Add OTel span events for `By()` steps so timestamped markers appear within the parent span.
5. **No-op when tracing disabled** — All instrumentation must be zero-cost when `OTEL_EXPORTER_OTLP_ENDPOINT` / `OTLP_HTTP_ENDPOINT` are not set.
6. **Suites in scope** — `topgun/k8s/integration/` (primary), `testflight/`, `topgun/k8s/`. Changes to `testhelpers/otel/` shared helper benefit all three.

## Target Trace Structure

```
test.run: "Smoke runs a simple task pipeline end-to-end" (12s)
├── fly.set-pipeline (1.2s)
├── fly.unpause-pipeline (0.3s)
├── fly.trigger-job (0.5s)
├── fly.watch [waitForBuildAndWatch] (9.8s)
└── fly.builds (0.2s)
```

## Acceptance Criteria

- [ ] Each integration test produces a parent `test.run` span with child spans for major operations
- [ ] `waitForBuildAndWatch` appears as a distinct child span (this is typically the longest operation)
- [ ] `fly.Run` / `fly.Start` calls produce child spans with command name as an attribute
- [ ] Ginkgo `By()` steps appear as span events on the parent
- [ ] Tracing remains opt-in and zero-cost when env vars are not set
- [ ] Verified in Grafana/Tempo: nested spans visible with correct parent-child relationships

## Out of Scope

- Server-side span nesting (Concourse server already emits its own spans)
- Cross-process trace context propagation through fly CLI
- Custom Grafana dashboards
- Testflight-specific fixture changes (mock resource type issues)
