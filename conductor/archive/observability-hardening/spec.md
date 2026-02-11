# Spec: Observability Hardening

## Overview

Unify and extend Concourse's instrumentation to provide complete OpenTelemetry-native observability — traces, metrics, and logs — across the full request lifecycle (HTTP request → scheduler → build engine → K8s pod execution). Ensure dual-target compatibility: local Grafana stack (Tempo/Mimir/Loki) and GCP Cloud Operations (Cloud Trace + Cloud Monitoring).

## Requirements

1. **HTTP trace middleware** — Add `otelhttp` middleware to the API server so every incoming request creates a span with method, route, status code, and duration. Extract W3C Trace Context from incoming headers.
2. **OTel Metrics migration** — Add an OTel Meter Provider alongside the existing custom emitter system. Key metrics (build duration, step duration, HTTP response time, K8s pod startup, container/volume counts) should be emitted as OTel instruments (counters, histograms, gauges). Existing Prometheus emitter continues to work unchanged.
3. **GCP Cloud Monitoring exporter** — Add a Google Cloud Monitoring metrics exporter (`opentelemetry-operations-go/exporter/metric`) so OTel metrics can flow to GCP alongside existing Cloud Trace spans. Configurable via `--metrics-gcp-project-id`.
4. **OTLP metrics exporter** — Add OTLP gRPC metrics exporter so OTel metrics flow to any OTLP-compatible collector (Grafana Alloy, Grafana Agent, OTel Collector). Uses same `--tracing-otlp-address` endpoint or a separate `--metrics-otlp-address`.
5. **Build & step span enrichment** — Add child spans for each pipeline step (task/get/put/check) under the parent build span. Add span events for step state transitions (pending → running → succeeded/failed). Add hook execution spans (on_success, on_failure, on_error, ensure).
6. **K8s pod lifecycle spans** — Add spans for pod phase transitions (Pending → Running → Succeeded/Failed), init container execution, sidecar startup, PVC bind, and image pull. Add span events for pod conditions (Scheduled, Initialized, ContainersReady).
7. **Database query tracing** — Add spans around key database operations (build creation, resource version checks, lock acquisition) with query type attributes.
8. **Secret/credential lookup spans** — Instrument Vault, SSM, Secrets Manager, and CredHub lookups with spans showing lookup path, duration, and cache hit/miss.
9. **Trace-to-metrics exemplars** — Where Prometheus histograms are emitted, attach trace ID exemplars so Grafana can link from a metric to the originating trace.
10. **Sampling configuration** — Add configurable trace sampling (always-on, probability-based, rate-limited) via `--tracing-sampling-strategy` and `--tracing-sampling-rate`.

## Acceptance Criteria

- `curl /api/v1/builds` creates an HTTP span visible in Grafana Tempo or GCP Cloud Trace
- A pipeline build produces a trace tree: HTTP request → scheduler → build → step(s) → K8s pod ops
- `--metrics-otlp-address` sends OTel metrics to a local Grafana Mimir via OTLP
- `--metrics-gcp-project-id` sends OTel metrics to GCP Cloud Monitoring
- Existing `--prometheus-bind-port` and `--tracing-otlp-address` continue to work unchanged
- Sampling can be set to 10% without code changes (env var or flag)
- All new spans have appropriate error status and attributes
- `go test ./tracing/...` and `go test ./atc/metric/...` pass

## Out of Scope

- AWS X-Ray / Azure Monitor exporters (can follow same pattern later)
- Log correlation with OTel Logs SDK (lager structured logging is sufficient for now)
- Custom Grafana dashboards (user creates their own)
- Distributed profiling (GCP Cloud Profiler, pyroscope)
- Agent step-specific instrumentation (separate track)
