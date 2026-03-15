# Implementation Plan: OTel observability pipeline for Grafana and test suites

## Phase 1: Connectivity Verification

- [x] Task: Write a standalone Go test (`otel_connectivity_test.go`) that sends a test trace to `theborg`'s Tempo OTLP endpoint (gRPC 4317), a test metric to Prometheus, and a test log entry to Loki, then queries each backend's API to verify the data arrived 68aabc4c4
- [x] Task: Run connectivity test against `theborg` cluster and confirm all three signals (trace, metric, log) are visible in Grafana 68aabc4c4
- [x] Task: Phase 1 Manual Verification 2d2fe9380

---

## Phase 2: Shared Test OTel Helper

- [x] Task: Create a reusable test helper package (e.g., `testhelpers/otel/`) that provides `InitTestTracing(suiteName)` to set up a TracerProvider with OTLP exporter, and a `ReportTestSpan()` function for use with Ginkgo's `ReportAfterEach` to emit per-test spans with attributes (test name, suite, duration, pass/fail, pipeline name) 68aabc4c4
- [x] Task: Write unit tests for the helper package -- verify span creation, attribute population, and graceful no-op when OTLP endpoint is not configured 68aabc4c4
- [x] Task: Phase 2 Manual Verification 2d2fe9380

---

## Phase 3: K8s Integration Suite Tracing (topgun/k8s/integration/)

- [x] Task: Update `helmDeployConcourse()` in the integration suite to pass `--tracing-otlp-address` and `--otel-metrics-otlp-address` Helm values so the deployed Concourse emits server-side traces. The OTLP endpoint should be configurable via env var (e.g., `OTEL_EXPORTER_OTLP_ENDPOINT`), defaulting to disabled 2d2fe9380
- [x] Task: Wire `InitTestTracing` into `integration_suite_test.go` BeforeSuite/AfterSuite and add `ReportAfterEach` for test envelope spans with pipeline name attribute 2d2fe9380
- [x] Task: Run the integration suite with tracing enabled, verify in Tempo that both server-side traces (build/step/pod spans) and test envelope spans appear, correlated by pipeline name and time window 9d7367a4e
- [x] Task: Phase 3 Manual Verification 9d7367a4e

---

## Phase 4: Testflight and Topgun K8s Suite Tracing

- [x] Task: Wire `InitTestTracing` into `testflight/suite_test.go` BeforeSuite/AfterSuite and add `ReportAfterEach` for test envelope spans (requires external ATC to have tracing configured separately) 2d2fe9380
- [x] Task: Wire `InitTestTracing` into `topgun/k8s/k8s_suite_test.go` BeforeSuite/AfterSuite and add `ReportAfterEach` for test envelope spans 2d2fe9380
- [x] Task: Run testflight suite with tracing enabled against a Concourse instance configured with `--tracing-otlp-address`, verify test envelope spans and server-side traces in Tempo 9d7367a4e
- [x] Task: Phase 4 Manual Verification 9d7367a4e

---

## Phase 5: Helm Chart and Documentation

- [x] Task: Ensure `deploy/chart/` Helm values expose `tracing.otlpAddress`, `tracing.otlpHeaders`, `tracing.otlpUseTLS`, `otelMetrics.otlpAddress` and wire them to the web node's command-line flags 2d2fe9380
- [x] Task: Add a brief section to the chart's values.yaml comments explaining how to point Concourse at a Grafana LGTM stack (Tempo endpoint for traces, OTLP for metrics) 2d2fe9380
- [x] Task: Phase 5 Manual Verification 2d2fe9380

---
