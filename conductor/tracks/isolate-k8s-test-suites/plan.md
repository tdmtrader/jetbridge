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

## Phase 3: Migrate k8s_behavioral to testcontainers-go k3s

### 3.1 Replace KinD cluster lifecycle with k3s
- [ ] Replace createKindCluster() with k3s.Run() in cluster_lifecycle_test.go
- [ ] Replace loadImagesIntoKind() with k3sContainer.LoadImages() for local Concourse image only
- [ ] Replace preloadImages() crictl-via-KinD-node with crictl-via-k3s-container
- [ ] Replace deleteKindCluster() with k3sContainer.Terminate()
- [ ] Add Colima Docker socket auto-detection to TestMain

### 3.2 Update kubeconfig and port management
- [ ] Use k3sContainer.GetKubeConfig() instead of file-based kubeconfig from KinD
- [ ] Write kubeconfig to temp file for helm/kubectl CLI commands
- [ ] Keep port-forward manager (still needed for helm-deployed Concourse)

### 3.3 Remove kind dependency
- [ ] Remove `kind` from verifyPrerequisites() (keep docker, helm, kubectl)
- [ ] Update package comments to reference k3s instead of KinD

### 3.4 Verify behavioral suite passes
- [ ] Run full behavioral suite end-to-end with k3s cluster
- [ ] Confirm all tests pass without assertion changes

## Phase 4: Migrate k8s/integration to testcontainers-go k3s

### 4.1 Replace KinD cluster lifecycle with k3s
- [ ] Same migration as Phase 3 for integration suite
- [ ] Replace createKindCluster() with k3s.Run()
- [ ] Replace loadImagesIntoKind() with k3sContainer.LoadImages() for local image + crictl for public images
- [ ] Add Colima Docker socket auto-detection

### 4.2 Update kubeconfig and cleanup
- [ ] Use k3sContainer.GetKubeConfig() for kubeconfig
- [ ] Replace deleteKindCluster() with k3sContainer.Terminate()
- [ ] Remove `kind` from verifyPrerequisites()

### 4.3 Verify integration suite passes
- [ ] Run full integration suite end-to-end with k3s cluster
- [ ] Confirm all tests pass without assertion changes

## Phase 5: Cleanup

### 5.1 Remove benchmark test
- [ ] Delete topgun/k8s_cluster_bench/ (served its purpose)
- [ ] Run go mod tidy to remove any orphaned dependencies

---
