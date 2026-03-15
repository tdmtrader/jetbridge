# Spec: OTel observability pipeline for Grafana and test suites

**Track ID:** `otel_observability_pipeline_for_grafana_and_test_suites_20260305`
**Type:** feature

## Overview

Concourse has comprehensive OpenTelemetry instrumentation (traces, metrics, log correlation) implemented in the `observability-hardening` track, but it has never been deployed against a real observability backend. This track connects the instrumentation to the Grafana LGTM stack running on the `theborg` K8s cluster (Tempo for traces, Loki for logs, Prometheus for metrics) and adds per-test-case tracing to long-running integration/E2E test suites so developers can see exactly where time is spent (e.g., "75s of a 90s test was waiting on pod readiness").

## Requirements

1. **Connectivity verification** -- A standalone test that sends a trace, metric, and log to the `theborg` monitoring stack and verifies they arrive in Tempo, Prometheus, and Loki via their query APIs.
2. **Server-side tracing enabled** -- The KinD-based integration test suite (`topgun/k8s/integration/`) deploys Concourse with `--tracing-otlp-address` and `--otel-metrics-otlp-address` pointing at Tempo/Alloy, so server-side spans (HTTP, scheduler, build, steps, K8s pods, DB) are emitted during test runs.
3. **Test envelope spans** -- Each Ginkgo `It` block in the long-running suites gets a wrapping span with attributes: test name, suite name, duration, pass/fail status. This uses Ginkgo's `ReportAfterEach` mechanism.
4. **Correlation by pipeline name** -- Test envelope spans include the unique pipeline name as an attribute, allowing time-window + pipeline-name correlation with server-side traces in Tempo.
5. **Test suites in scope** -- `topgun/k8s/integration/` (primary, self-contained KinD), `testflight/` (external ATC), `topgun/k8s/` (external K8s). Ginkgo unit tests are NOT in scope for per-test tracing.
6. **Opt-in activation** -- Test tracing is activated via an environment variable (e.g., `OTEL_EXPORTER_OTLP_ENDPOINT`) so it doesn't affect CI runs that don't want tracing overhead.
7. **Helm chart configuration** -- The deploy/chart values support OTLP endpoint configuration for production deployments against Grafana.

## Technical Approach

### Architecture
- **No fly CLI bypass** -- Tests continue using `fly` as the primary interface. Trace context does NOT propagate from test process to server process.
- **Two independent trace streams** -- Test envelope spans and server-side spans are correlated by pipeline name + time window, not parent-child linking.
- **Shared OTel helper** -- A reusable package (e.g., `testhelpers/otel`) provides `InitTestTracing()` and `ReportAfterEach` wiring so each suite gets tracing with minimal boilerplate.

### Grafana Stack (theborg cluster, `monitoring` namespace)
| Component | Endpoint |
|-----------|----------|
| Tempo (traces) | `tempo.monitoring.svc:4317` (gRPC OTLP) |
| Loki (logs) | `loki.monitoring.svc:3100` |
| Prometheus | `prometheus-kube-prometheus-prometheus.monitoring.svc:9090` |
| Alloy (collector) | DaemonSet, ports 4317/4318 (OTLP gRPC/HTTP) |
| Grafana UI | `grafana.home` |

### Key Files
- `tracing/` -- Existing OTel tracer/exporter infrastructure
- `atc/metric/` -- Existing OTel metrics instruments
- `testflight/suite_test.go` -- Testflight suite setup
- `topgun/k8s/integration/integration_suite_test.go` -- K8s integration suite setup
- `topgun/k8s/k8s_suite_test.go` -- Topgun K8s suite setup
- `deploy/chart/` -- Helm chart values

## Acceptance Criteria

- [ ] Connectivity test passes: trace visible in Tempo, metric queryable in Prometheus, log entry in Loki
- [ ] K8s integration tests emit server-side traces (visible in Tempo with build/step/pod spans)
- [ ] Test envelope spans appear in Tempo with test name, suite, duration, and pipeline name attributes
- [ ] Testflight and topgun/k8s suites have opt-in test tracing via env var
- [ ] Helm chart supports `--tracing-otlp-address` and `--otel-metrics-otlp-address` configuration
- [ ] All tracing is opt-in; no impact on CI runs without the env var set

## Out of Scope

- Bypassing fly CLI for direct HTTP trace context propagation
- Custom Grafana dashboards (manual creation by user)
- Ginkgo unit test tracing (only long-running integration/E2E suites)
- AWS X-Ray / Azure Monitor exporters
- OTel Logs SDK bridge (existing lager log correlation is sufficient)
- Distributed profiling (pyroscope, Cloud Profiler)
