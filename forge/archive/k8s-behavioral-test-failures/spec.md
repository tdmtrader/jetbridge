# Spec: K8s Behavioral Test Failures

**Type:** bugfix
**Created:** 2026-02-15
**Updated:** 2026-02-15

## Overview

26 of 263 active K8s behavioral integration tests (`topgun/k8s_behavioral/`) fail when run against the JetBridge Concourse runtime. The failures cluster into 3 root causes, with 81% (21/26) caused by a single architectural gap: the ATC cannot read artifact files from PVC-backed volumes on build pods. This track addresses each failure group systematically, starting with the highest-impact root cause.

**Current pass rate:** 233/263 active specs (88.6%), plus 53 pending, 4 runtime skips
**Target pass rate:** 263/263 active specs (100%)

## Full Suite Run Results

```
Ran 259 of 316 Specs in 7193 seconds (~2 hours)
233 Passed | 26 Failed | 53 Pending | 4 Skipped
```

## Failure Categories

| Category | Count | Root Cause |
|----------|-------|------------|
| E: set_pipeline / load_var | 15 | Artifact streaming (ATC can't read PVC) |
| F: Artifact store access | 4 | Artifact streaming (ATC can't read PVC) |
| H: E2E using set_pipeline | 2 | Artifact streaming (ATC can't read PVC) |
| G: Pod lifecycle | 2 | Pod cleanup not implemented |
| I: Behavioral differences | 3 | Various JetBridge behavioral gaps |

## Requirements

### R1: Implement artifact streaming from build pods to ATC (21 failures)

The ATC needs to read file contents from PVC-backed artifact stores on build pods for:
- `set_pipeline:` step — reads pipeline config YAML from task output
- `load_var` step — reads variable files from task output
- `file:` directive in task steps — reads task config from get step artifacts
- `fly execute -i` — uploads local inputs to the worker
- Implicit get after put — `get_params` forwarding
- `skip_download: true` — metadata-only get step

**Implementation approach:** Add an artifact streaming HTTP endpoint to the JetBridge artifact-helper sidecar that allows the ATC to fetch file contents from the PVC. The ATC's `StreamFile` and `StreamVolume` interfaces need a JetBridge-specific implementation that calls this endpoint.

### R2: Implement pod cleanup for completed/aborted builds (2 failures)

JetBridge doesn't clean up:
- Build pods after abort — pod persists indefinitely after build cancellation
- Check pods after completion — pod has `exit-status: "0"` annotation but the artifact-helper sidecar's `sleep 86400` keeps it alive

**Implementation approach:** Add a pod finalizer/reaper that watches for completed and aborted builds, and terminates their associated pods by sending SIGTERM to the artifact-helper sidecar.

### R3: Fix behavioral differences (3 failures)

- **8.5: fail_fast in in_parallel** — Abort signals don't propagate to parallel pods quickly enough
- **3.16: fly clear-resource-cache** — Command hangs indefinitely; the fly process never exits
- **Version causality** — Feature flag `resource_causality` is disabled; test should skip when flag is off

## Acceptance Criteria

- [ ] All 263 active specs pass against the JetBridge K8s runtime
- [ ] No test hangs or timeouts beyond EVENTUALLY_TIMEOUT
- [ ] Full suite completes in under 3 hours with `--ginkgo.timeout=4h`
- [ ] `go test ./topgun/k8s_behavioral/ -count=1 -v -timeout 240m --ginkgo.timeout=4h` exits 0

## Out of Scope

- The 53 `Pending` specs (marked `PIt()`) — intentionally deferred for future work
- Performance optimization of suite execution time
- Adding new test coverage beyond fixing existing failures
- Custom resource type chain image resolution (all 27 are PIt)
- Caching / PVC volume management (all 5 are PIt)
