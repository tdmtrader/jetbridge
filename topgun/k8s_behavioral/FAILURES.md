# K8s Behavioral Integration Test Failures

Tests that do not pass against the current Concourse K8s (JetBridge) runtime are documented here.
Each entry includes the test ID, failure description, root cause analysis, and proposed fix.

## Full Suite Run Results

**Current (verified 2026-05-31, kind-runner v35, CI build k8s-e2e/k8s-behavioral-tests/103; #102 identical):**

```
Ran 298 of 304 Specs in 1743 seconds (~29 min)
298 Passed | 0 Failed | 1 Pending | 5 Skipped
```

The suite is **green** across two consecutive runs (#102, #103). The DaemonSet
artifact-cache architecture resolved the former artifact-streaming failures
(Categories E/F/H below), so those entries are **historical** and predate the
current runtime. The `runs a pipeline with custom resource types` spec that flaked
intermittently (builds #99/#100/#101) now passes reliably in #103. Two
complementary fixes closed it: (1) the `resource_config_scope` FK-violation guards
in `atc/exec/check_step.go` (handle `SaveVersions`/`PointToCheckedConfig` FK
violations from the GC race as non-fatal), and (2) bumping the kind-runner tag
(v33→v35) so the worker pulls fresh code instead of serving the FK guards' April
fix by a stale cached image. The guards now actually run in CI, and the spec passes.

<details>
<summary>Historical run (stale, pre-DaemonSet-artifact-cache)</summary>

```
Ran 259 of 316 Specs in 7193 seconds (~2 hours)
233 Passed | 26 Failed | 53 Pending | 4 Skipped
```

The categories below describe these historical failures, most rooted in the
deprecated PVC-backed artifact store (the ATC could not read task artifacts).
</details>

---

## Category E: JetBridge Runtime Limitations (set_pipeline / load_var)

These 15 tests fail because `set_pipeline:` and `load_var` steps require the ATC (web pod)
to read artifact files produced by task steps. In JetBridge, task artifacts are stored in
PVC-backed volumes attached to build pods, which the ATC cannot access directly.

**Root Cause**: The ATC needs to resolve `set_pipeline:` config files and `load_var` file
contents from the artifact store. In the Garden runtime, the ATC streams these from the
worker container. In JetBridge, the artifact store is a PVC mounted on the build pod, and
the ATC has no mechanism to read from it.

**Proposed Fix**: Implement an artifact streaming endpoint in the JetBridge sidecar that
allows the ATC to fetch file contents from the artifact store PVC.

| # | Test | File |
|---|------|------|
| 1 | 10.1: creates a new pipeline from a task output file | `set_pipeline_load_var_test.go` |
| 2 | 10.2: updates an existing pipeline with new jobs | `set_pipeline_load_var_test.go` |
| 3 | 10.3: interpolates vars into set_pipeline | `set_pipeline_load_var_test.go` |
| 4 | 10.4: uses var_files with set_pipeline | `set_pipeline_load_var_test.go` |
| 5 | 10.5: set_pipeline with instance_vars creates instanced pipeline | `set_pipeline_load_var_test.go` |
| 6 | 10.6: set_pipeline with team sets pipeline in another team | `set_pipeline_load_var_test.go` |
| 7 | 10.7: set_pipeline: self updates the current pipeline | `set_pipeline_load_var_test.go` |
| 8 | 10.8: loads a plain string value | `set_pipeline_load_var_test.go` |
| 9 | 10.9: loads a JSON file and accesses nested keys | `set_pipeline_load_var_test.go` |
| 10 | 10.10: loads a YAML file and accesses nested keys | `set_pipeline_load_var_test.go` |
| 11 | 10.11: loads a raw (unformatted) file preserving whitespace | `set_pipeline_load_var_test.go` |
| 12 | 10.12: reveal: true shows the loaded value in build output | `set_pipeline_load_var_test.go` |
| 13 | 10.13: loaded var is usable in subsequent task params | `set_pipeline_load_var_test.go` |
| 14 | 10.14: loaded var is usable in put params | `set_pipeline_load_var_test.go` |
| 15 | 10.15: chains multiple load_var steps in sequence | `set_pipeline_load_var_test.go` |

---

## Category F: Artifact Store Access Failures

These tests fail because JetBridge cannot serve artifact contents to the ATC for
non-task operations (implicit get after put, skip_download, fly execute uploads, etc.).

### 5.2: loads task config from a file in a get step artifact
- **File**: `task_step_test.go:48`
- **Failure**: Build fails — mock resource `get` step produces `task.yml` but the task
  step cannot resolve `file: task-repo/task.yml` from the artifact store.
- **Cause**: Same artifact streaming limitation as Category E. The `file:` directive in
  a task step needs to read from a get step's output, which is in the PVC.
- **Duration**: ~20s

### 7.8: put with get_params passes params to implicit get
- **File**: `put_step_test.go:231`
- **Failure**: The implicit get after a put step fails because get_params aren't
  properly forwarded or the implicit get can't access artifact data.
- **Cause**: Implicit get after put requires artifact store coordination.
- **Duration**: ~20s

### Get Steps: skips artifact download with skip_download: true
- **File**: `get_step_test.go:508`
- **Failure**: Fails immediately (0.2s) — the `skip_download` field may not be
  recognized at the step level in JetBridge, or the pipeline config is rejected.
- **Cause**: Needs investigation — may be a config parsing issue or JetBridge
  not implementing `skip_download` on get steps.
- **Duration**: ~0.2s

### Fly execute: maps inputs with -i
- **File**: `fly_cli_test.go:32`
- **Failure**: `fly execute` fails with "500 Internal Server Error" when uploading
  inputs via `-i my-input=<path>`.
- **Cause**: `fly execute` uploads local files to the ATC, which then needs to
  stream them to the worker. JetBridge doesn't support this upload pathway.
- **Duration**: ~0.1s

---

## Category G: Pod Lifecycle Issues

### 11.19: pods are cleaned up after aborted build
- **File**: `k8s_infrastructure_test.go:549`
- **Failure**: After aborting a build, the task pod is not cleaned up within 3 minutes.
- **Cause**: JetBridge pod cleanup after abort may not be fully implemented or has
  a longer grace period than the test expects.
- **Duration**: ~184s (times out on Eventually)

### Resource Checking: cleans up check pods after completion
- **File**: `resource_checking_test.go:298`
- **Failure**: Check pods are not cleaned up after the check completes. Pod has
  `concourse.ci/exit-status: "0"` annotation but persists.
- **Cause**: JetBridge doesn't reap completed check pods. The artifact-helper sidecar
  keeps the pod alive with `sleep 86400`.
- **Duration**: ~182s (times out on Eventually)

---

## Category H: E2E Scenarios Using set_pipeline

### runs a self-updating pipeline via set_pipeline
- **File**: `e2e_scenarios_test.go:203`
- **Failure**: Same root cause as Category E — `set_pipeline:` step can't read config.
- **Duration**: ~19s

### runs a dynamically generated pipeline
- **File**: `e2e_scenarios_test.go:229`
- **Failure**: Same root cause as Category E — `set_pipeline:` step can't read config.
- **Duration**: ~20s

---

## Category I: Behavioral Differences

### 8.5: fails fast with fail_fast: true (in_parallel)
- **File**: `composite_steps_test.go:191`
- **Failure**: `in_parallel` with `fail_fast: true` does not abort remaining steps
  quickly enough, or the abort behavior differs from Garden runtime.
- **Cause**: JetBridge may not propagate abort signals to parallel pods as quickly
  as Garden does to parallel containers.
- **Duration**: ~5s

### Resource Checking: clears versions and rediscovers them
- **File**: `resource_checking_test.go:722`
- **Failure**: `fly clear-resource-cache` hangs (blocks indefinitely). Had to be
  killed externally for the test suite to continue.
- **Cause**: `fly clear-resource-cache` may be waiting for a response that
  JetBridge never sends (pod/container lifecycle issue).
- **Duration**: ~1.2s (after external kill)

### Resource Checking: tracks version causality via API
- **File**: `resource_checking_test.go:846`
- **Failure**: Version causality tracking via the API doesn't return expected
  relationships. The `resource_causality` feature flag is disabled.
- **Cause**: Feature flag `resource_causality` is `false` in the Helm deployment.
  Test should skip when the feature flag is disabled.
- **Duration**: ~20s

---

## Previously Fixed Issues (Test Quality)

### ~~Category A: Definite Failures (YAML/Schema Bugs)~~ ALL FIXED
- Sidecar Env — changed YAML to list-of-objects format
- Get skip_download — moved to step-level field

### ~~Category B: Tests That Pass But Don't Test What They Claim~~ ALL FIXED
- Sidecar Reserved Name — changed to `main` (actually reserved)
- Sidecar File-Based — rewrote as inline config
- K8s Secrets Credential Manager — added real assertions
- var_sources — renamed to match actual behavior

### ~~Category C: Fragile Tests (Runtime-Dependent)~~ ALL FIXED
- OOM Test — fixed command for physical memory consumption
- Sidecar Localhost Connectivity — nc loop
- Cross-Pipeline Pod Interference — pipeline label filter
- Mock Resource Image — pre-pull via crictl

### ~~Category D: Flaky Assertion Patterns~~ ALL FIXED
- Sequential gbytes.Say — reviewed, no change needed
- Eventually Timeouts — EVENTUALLY_TIMEOUT env var

### ~~Additional Runtime Failures~~ ALL FIXED
- Test 5.2 — simplified file path (still fails due to Category F root cause)
- Test 5.15 — changed to -v flag for inline config

---

## Summary

| Category | Count | Status |
|----------|-------|--------|
| E: JetBridge set_pipeline/load_var | 15 | Runtime limitation — needs artifact streaming |
| F: Artifact store access | 4 | Runtime limitation — needs artifact streaming |
| G: Pod lifecycle | 2 | JetBridge doesn't reap completed/aborted pods |
| H: E2E using set_pipeline | 2 | Same root cause as Category E |
| I: Behavioral differences | 3 | Various JetBridge behavioral gaps |
| **Active Failures** | **26** | |
| Previously Fixed (test quality) | 15 | All resolved |

### Root Cause Distribution

| Root Cause | Tests Affected |
|------------|----------------|
| Artifact streaming (ATC can't read PVC) | 21 (E + F + H) |
| Pod cleanup not implemented | 2 (G) |
| Behavioral differences | 3 (I) |
| **Total** | **26** |
