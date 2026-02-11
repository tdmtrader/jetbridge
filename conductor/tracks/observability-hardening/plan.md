# Plan: Observability Hardening

## Phase 1: OTel Foundation — Meter Provider & Sampling

**Goal:** Establish the OTel metrics SDK alongside existing tracing, add sampling config.

- [x] Write tests for OTel Meter Provider initialization
- [x] Implement OTel Meter Provider in `tracing/` with configurable exporters
- [x] Write tests for OTLP metrics exporter wiring
- [x] Implement OTLP gRPC metrics exporter (`--metrics-otlp-address` flag)
- [x] Write tests for GCP Cloud Monitoring exporter wiring
- [x] Implement GCP Cloud Monitoring exporter (`--metrics-gcp-project-id` flag)
- [x] Write tests for trace sampling configuration
- [x] Implement configurable sampling (`--tracing-sampling-strategy`, `--tracing-sampling-rate`)
- [x] Wire Meter Provider and sampling into `atc/atccmd/command.go` startup

**Checkpoint:** `go test ./tracing/...` passes, meter provider initializes with OTLP or GCP exporter, sampling is configurable.

## Phase 2: HTTP Trace Middleware

**Goal:** Every API request creates a span with W3C Trace Context propagation.

- [x] Write tests for HTTP trace middleware span creation `186a5ba3f`
- [x] Implement `otelhttp` middleware wrapping in `atc/wrappa/` or API handler setup `186a5ba3f`
- [x] Write tests for W3C Trace Context extraction from incoming headers `b7f64972e`
- [x] Wire middleware into the API handler chain in `atc/api/handler.go` / `atc/atccmd/command.go` `8efca89b3`

**Checkpoint:** `curl -H "traceparent: ..." /api/v1/info` creates a child span under the incoming trace; visible in OTLP collector.

## Phase 3: Build & Step Span Enrichment

**Goal:** Full trace tree from build → steps → hooks.

- [ ] Write tests for per-step child spans (task, get, put, check)
- [ ] Enrich existing step spans in `atc/exec/{task,get,put,check}_step.go` with state-transition span events
- [ ] Write tests for hook execution spans
- [ ] Add spans for on_success, on_failure, on_error, ensure hooks in `atc/exec/`
- [ ] Write tests for step duration OTel histogram instrument
- [ ] Emit step duration as OTel histogram alongside existing metric events

**Checkpoint:** A pipeline build produces a trace tree: build → get → task → put with hook spans; step duration histogram appears in OTLP metrics.

## Phase 4: K8s Pod Lifecycle Spans

**Goal:** Detailed instrumentation of pod execution in JetBridge runtime.

- [ ] Write tests for pod phase transition spans
- [ ] Add spans in `atc/worker/jetbridge/` for pod phase transitions (Pending → Running → Succeeded/Failed)
- [ ] Write tests for init container and sidecar lifecycle spans
- [ ] Instrument init container completion and sidecar startup in container/process handling
- [ ] Write tests for PVC bind and image pull spans
- [ ] Add spans for PVC binding status and image pull events with duration attributes

**Checkpoint:** `go test ./atc/worker/jetbridge/...` passes; pod lifecycle trace visible as child spans under container.run.

## Phase 5: Database & Secret Lookup Spans

**Goal:** Visibility into data-layer and credential operations.

- [ ] Write tests for database operation spans
- [ ] Add spans around key DB operations (build creation, resource version checks, lock acquisition) in `atc/db/`
- [ ] Write tests for secret/credential lookup spans
- [ ] Instrument Vault, SSM, Secrets Manager, CredHub lookups in `atc/creds/` with path, duration, cache hit/miss attributes

**Checkpoint:** `go test ./atc/db/...` and `go test ./atc/creds/...` pass; credential lookups appear as spans in traces.

## Phase 6: OTel Metrics Bridge & Exemplars

**Goal:** Key existing metrics emitted as OTel instruments; Prometheus exemplars for trace correlation.

- [ ] Write tests for OTel instrument emission of core metrics
- [ ] Emit build duration, HTTP response time, K8s pod startup, container/volume counts as OTel instruments in `atc/metric/`
- [ ] Write tests for Prometheus exemplar attachment
- [ ] Attach trace ID exemplars to Prometheus histograms where spans exist
- [ ] Verify backward compatibility: existing `--prometheus-bind-port` still works unchanged

**Checkpoint:** OTel metrics visible in OTLP collector; Prometheus `/metrics` endpoint includes exemplars with trace IDs; all existing metric tests pass.
