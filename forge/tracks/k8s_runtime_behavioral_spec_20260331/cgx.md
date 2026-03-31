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
