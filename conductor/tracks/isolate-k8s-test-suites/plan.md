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

## Phase 4: Migrate k8s/integration to KinD Go library

### 4.1 Replace KinD CLI with Go library
- [x] Same migration as Phase 3 for integration suite aba65d9ed
- [x] Use kindProvider.Create() / .Delete() / .KubeConfig() in cluster_lifecycle_test.go aba65d9ed
- [x] Use nodeutils.LoadImageArchive() for loading local images aba65d9ed
- [x] Remove `kind` from verifyPrerequisites() aba65d9ed

### 4.2 Verify integration suite passes
- [~] Run full integration suite end-to-end with KinD Go library
- [~] Confirm all tests pass without assertion changes

## Phase 5: Cleanup

### 5.1 Remove benchmark test
- [ ] Delete topgun/k8s_cluster_bench/ (served its purpose)
- [ ] Run go mod tidy to remove any orphaned dependencies

---
