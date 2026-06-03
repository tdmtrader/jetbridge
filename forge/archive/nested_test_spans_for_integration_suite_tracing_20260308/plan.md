# Implementation Plan: Nested test spans for integration suite tracing

## Phase 1: Live Parent Span and Context Threading

- [x] Task: Refactor `testhelpers/otel/otel.go` to start a `test.run` span in a new `StartTestSpan()` hook (called from `BeforeEach`) that stores the span and context in package-level vars (`testCtx`, `testSpan`). Move status/attribute finalization from `emitTestSpan` into a `FinalizeTestSpan()` called from `ReportAfterEach`.
- [x] Task: Export a `TestContext()` function that returns the current `testCtx` (or `context.Background()` when tracing is disabled) for use by test helpers.
- [x] Task: Wire `StartTestSpan()` into `topgun/k8s/integration/integration_suite_test.go` BeforeEach and verify the parent span is created and finalized correctly.
- [x] Task: Phase 1 Manual Verification — run a single smoke test, confirm a `test.run` span appears in Tempo with correct duration and status.

---

## Phase 2: Instrument Fly Helpers with Child Spans

- [x] Task: Add a `StartSpan(name, attrs...)` helper to `testhelpers/otel/` that creates a child span under `testCtx` and returns a `func()` end callback. No-op when tracing is disabled.
- [x] Task: Instrument `topgun/k8s/integration/` helpers: wrap `setAndUnpausePipeline`, `setPipeline`, `triggerJob`, `waitForBuildAndWatch`, `newMockVersion`, and `destroyPipeline` with child spans using `StartSpan`.
- [x] Task: Instrument the `FlyCli.Run()` method in `topgun/fly.go` to emit a `fly.<command>` child span with `fly.args` attributes.
- [x] Task: Phase 2 Manual Verification — run Smoke + Build Lifecycle tests, confirm child spans appear nested under the parent `test.run` span in Tempo.

---

## Phase 3: Ginkgo By() Events and Testflight/Topgun Wiring

- [x] Task: Add a `By(text)` helper that wraps Ginkgo's `By()` and adds an OTel span event with the step description to the current parent span.
- [x] Task: Wire `StartTestSpan()` into `testflight/suite_test.go` BeforeEach and instrument testflight helpers (`setAndUnpausePipeline`, `waitForBuildAndWatch`, `newMockVersion`) with child spans.
- [x] Task: Wire `StartTestSpan()` into `topgun/k8s/k8s_suite_test.go` BeforeEach and instrument with `FinalizeTestSpan`.
- [x] Task: Phase 3 Manual Verification — run full 117-test integration suite, verify in Grafana that nested spans are visible with correct parent-child hierarchy. All 117 tests passed with 0 failures. Server-side build spans also flowing via NodePort OTLP endpoint.

---
