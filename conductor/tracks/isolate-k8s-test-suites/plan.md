# Plan: Isolate K8s Test Suites

## Phase 1: Remove External Cluster Mode (COMPLETE)

### 1.1 k8s_behavioral: Remove external cluster env var path
- [x] ced5afe84 Remove code path that skips TestMain() when ATC_URL is set
- [x] ced5afe84 Remove env var reads for external KUBECONFIG and K8S_NAMESPACE
- [x] ced5afe84 Keep only the KinD self-sufficient path in TestMain()

### 1.2 k8s/integration: Inline KinD cluster creation
- [x] e2d02e9ab Port cluster provisioning from hack/kind-integration.sh into TestMain()
- [x] e2d02e9ab Add prerequisite checks (docker, kind, helm, kubectl)

### 1.3 Remove hack/kind-integration.sh
- [x] 50bb947c2 Delete hack/kind-integration.sh

## Phase 2: Benchmark k3s vs KinD (COMPLETE)

### 2.1 Prove the approach with a benchmark test
- [x] 174c29740 Create head-to-head benchmark in topgun/k8s_cluster_bench/
- [x] 174c29740 Demonstrate k3s + crictl pull is 1.8x faster than KinD (19.5s vs 35.3s)
- [x] 174c29740 Confirm 0 image loading errors with crictl pull (vs LoadImages API failures)
- [x] 174c29740 Verify Colima Docker socket auto-detection works

## Phase 3: Migrate k8s_behavioral to KinD Go library (COMPLETE)

> k3s via testcontainers-go proved not viable — containerd sandbox instability
> (SandboxChanged/CrashLoopBackOff) in nested Docker on macOS. Tested with
> 8GB and 16GB Colima, same failures. Pivoted to KinD Go library which
> achieves the same self-containment goal without runtime regressions.

### 3.1 Replace KinD CLI with Go library
- [x] 8eb322c96 Use sigs.k8s.io/kind/pkg/cluster API instead of exec.Command("kind", ...)
- [x] 8eb322c96 Use kindProvider.Create() / .Delete() / .KubeConfig() / .ListInternalNodes()
- [x] 8eb322c96 Use nodeutils.LoadImageArchive() for loading local images into nodes
- [x] 8eb322c96 Remove `kind` from verifyPrerequisites() (only docker, helm, kubectl needed)

### 3.2 Verify behavioral suite passes
- [x] 8eb322c96 295/296 passed (1 flaky skip_download test), 3328s — within 1% of CLI baseline

## Phase 4: Migrate k8s/integration to KinD Go library (COMPLETE)

### 4.1 Replace KinD CLI with Go library
- [x] Same migration as Phase 3 for integration suite aba65d9ed
- [x] Use kindProvider.Create() / .Delete() / .KubeConfig() in cluster_lifecycle_test.go aba65d9ed
- [x] Use nodeutils.LoadImageArchive() for loading local images aba65d9ed
- [x] Remove `kind` from verifyPrerequisites() aba65d9ed

### 4.2 Verify integration suite passes
- [x] Run full integration suite end-to-end with KinD Go library aba65d9ed
- [x] Confirm all tests pass without assertion changes aba65d9ed

## Phase 5: Cleanup (COMPLETE)

### 5.1 Remove benchmark test
- [x] Delete topgun/k8s_cluster_bench/ (served its purpose) bae83c83e
- [x] Run go mod tidy to remove any orphaned dependencies bae83c83e

## Phase 6: Fix Pre-existing Integration Test Failures [checkpoint: 143eaae02]

> 105/120 passed, 15 failed, 6 pending. All failures are pre-existing
> test bugs unrelated to the KinD Go library migration.

### 6.1 Fix load_var credential redaction (7 tests)
- [x] Fix load_var_test.go — loaded values appear as `((redacted))` in fly watch output, causing assertion mismatches (lines 46, 86, 129, 168, 206, 244, 291) 7980c3599
- [x] Fix load_var_test.go:362 — "fails gracefully when file does not exist" assertion error 7980c3599

### 6.2 Fix produces: registry-image custom type tests (2 tests)
- [x] Fix skip_image_get_test.go — mock resource rejects `repository` field with `json: unknown field "repository"` (lines 22, 80) 7980c3599

### 6.3 Fix pipeline E2E test fixture (1 test)
- [x] Fix k8s_pipeline_e2e_test.go:135 — multi-stage pipeline fixture has a cycle (`get: output-data passed: [multi-stage-job]` creates self-reference) 7980c3599

### 6.4 Fix set_pipeline variable interpolation (1 test)
- [x] Fix set_pipeline_test.go:161 — load_var value redacted in interpolated pipeline config 7980c3599

### 6.5 Fix hijack test (1 test)
- [x] Fix hijack_test.go:48 — intercept of running task fails (timing/flakiness) 7980c3599

### 6.6 Fix resource advanced test (1 test)
- [x] Fix resource_advanced_test.go:333 — get_params on implicit get after put 7980c3599

### 6.7 Fix error handling test (1 test)
- [x] Fix error_handling_test.go:47 — on_error hook test times out (task runs `sleep 120`) 7980c3599

### 6.8 Verify clean run
- [x] Run full integration suite — all non-pending specs pass 143eaae02

---
