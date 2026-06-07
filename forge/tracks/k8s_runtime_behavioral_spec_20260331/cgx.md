# CGX — K8s Runtime Behavioral Specification

## Discovery Notes

### Spec Pattern
- Used same format as exec_step_behavioral_spec (requirement IDs, MUST language, coverage matrix)
- 87 requirements across 9 sections covering the entire JetBridge runtime
- Complements the existing storage behavioral spec (48 requirements) for complete coverage

### Coverage Findings
- Pod Naming, GC, and Worker Registration have 100% full coverage
- Observability events (span events, metrics) are the weakest area (0% full, 90% partial)
- Sidecar log streaming is the most critical gap (SC-07, SC-11) — core user-visible behavior with no tests
- Pod Execution has good test infrastructure but many tests are implicit rather than explicitly asserting specific behaviors

### Key Architectural Observations
- Exec mode vs direct mode is the fundamental split in the runtime — different cleanup, different streaming, different failure handling
- The pause pod pattern enables hijack but introduces complexity in GC (exit-status annotation for fast cleanup)
- Transient error handling (3-error threshold) is a critical resilience mechanism that prevents cascading failures
- Event deduplication via podEventTracker is essential for trace quality but has no dedicated tests

## Implementation Notes

### good-pattern
- [2026-06-07] OE span-event tests (OE-01/05/07/08) are *characterization tests*: the production code already emits the events, so the test passes immediately and locks in the contract (fails only on regression). Correct framing for a docs/coverage-backfill track — don't fake a Red phase by breaking prod code.
- [2026-06-07] All exec-mode lifecycle span events land on the `k8s.exec-process.wait-for-running` span via `emitPodLifecycleEvents` (process.go:594) + the inline `pod.phase.*` emission (process.go:1092). The established harness (spanRecorder + stage pod status via fake clientset initial-sync snapshot, then a 20ms-delayed goroutine transition to PodRunning so Wait() returns) drives all of them — reused verbatim from OE-02/04/06.

### good-pattern (attribute assertions)
- [2026-06-07] Asserting span *event attributes* (node.name, container.name, pod.phase) — not just event names — caught real coverage value: iterate `span.Events()`, match `e.Name`, then range `e.Attributes` comparing `string(kv.Key)` and `kv.Value.AsString()`. No extra import needed (attribute.KeyValue methods suffice).

### good-pattern (OE-10 metrics — two layers)
- [2026-06-07] K8s runtime metrics live in TWO layers: in-process `metric.Metrics.*` (Monitor; `Counter.Delta()` swaps-to-0, `Gauge.Max()` swaps-max-to-(-1) — both self-resetting reads, so reset-at-spec-start is race-free given Ginkgo's serial-within-process execution) AND OTel instruments via `metric.Record*`/`metric.Metrics` that are NO-OPs until `InitOTelMetrics()` binds them to a meter provider. The OTel `concourse.k8s.pod_failures` Int64Counter had ZERO test coverage anywhere.
- [2026-06-07] To assert an OTel counter through the runtime: `sdkmetric.NewManualReader()` → `NewMeterProvider(WithReader)` → `otel.SetMeterProvider` → `metric.InitOTelMetrics()` BEFORE driving the failure, then `reader.Collect()` and match `metricdata.Sum[int64]` data points by `Attributes.Value("reason")`. Pattern lifted from atc/metric/otel_metrics_test.go.

### missing-capability
- [2026-06-07] No Forge MCP server connected this session — all status/task/checkpoint ops done via manual plan.md edits + git notes (workflow's documented fallback). Worked fine but loses metadata.json↔tracks.md auto-sync.
