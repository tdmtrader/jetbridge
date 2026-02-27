# Plan: Isolate K8s Test Suites

## Phase 1: Remove External Cluster Mode (Quick Win)

### 1.1 k8s_behavioral: Remove external cluster env var path
- [x] ced5afe84 In `behavioral_suite_test.go`, remove the code path that skips `TestMain()` cluster creation when `ATC_URL` is set
- [x] ced5afe84 Remove env var reads for external `KUBECONFIG` and `K8S_NAMESPACE` used to connect to pre-existing deployments
- [x] ced5afe84 Keep only the KinD self-sufficient path in `TestMain()`
- [x] ced5afe84 Ensure `SynchronizedBeforeSuite` always uses the KinD-generated kubeconfig and ATC URL

### 1.2 k8s/integration: Inline KinD cluster creation
- [x] e2d02e9ab Port the cluster provisioning logic from `hack/kind-integration.sh` into the suite's `TestMain()`, matching the pattern in k8s_behavioral
- [x] e2d02e9ab Add KinD cluster creation, Helm deployment, and port-forward setup to `integration_suite_test.go`
- [x] e2d02e9ab Remove reliance on external cluster setup — `ATC_URL` and `KUBECONFIG` are generated internally
- [x] e2d02e9ab Add prerequisite checks (docker, kind, helm, kubectl) matching k8s_behavioral

### 1.3 Remove hack/kind-integration.sh
- [x] 50bb947c2 Delete `hack/kind-integration.sh` — cluster creation is now embedded in the test suite

### 1.4 Verify both suites pass in self-sufficient mode
- [ ] Run `go test ./topgun/k8s_behavioral/ -count=1 -v -timeout 30m` end-to-end (requires Docker)
- [ ] Run `go test ./topgun/k8s/integration/ -count=1 -v -timeout 30m` end-to-end (requires Docker)
- [ ] Confirm both create their own clusters and clean up on completion

## Phase 2: Migrate to testcontainers-go

### 2.1 Add testcontainers-go dependency
- [x] b72bf8d9c Add `github.com/testcontainers/testcontainers-go` v0.40.0 and k3s module to `go.mod`
- [x] b72bf8d9c Run `go mod tidy` to resolve transitive dependencies

### 2.2 k8s_behavioral: Replace KinD with testcontainers k3s
- [x] b72bf8d9c Replace `TestMain()` KinD logic with `testcontainers-go` k3s container (k3s.Run / GetKubeConfig / LoadImages)
- [x] b72bf8d9c Use Go APIs for kubeconfig extraction from k3s container
- [x] b72bf8d9c Deploy Concourse via Helm CLI using kubeconfig from k3s container
- [x] b72bf8d9c Remove `kind` from prerequisite checks

### 2.3 k8s/integration: Replace KinD with testcontainers k3s
- [x] b72bf8d9c Same migration as 2.2 for the legacy integration suite
- [x] b72bf8d9c Shared helpers (fly CLI, pod inspection) work against k3s-managed cluster (unchanged)

### 2.4 Remove KinD CLI dependency
- [x] b72bf8d9c Prerequisite checks now require only docker, helm, kubectl (kind removed)
- [x] b72bf8d9c All documentation updated to reference k3s instead of KinD

### 2.5 Final validation
- [ ] Run both suites end-to-end with testcontainers-managed k3s (requires Docker)
- [x] b72bf8d9c Verify no external cluster connectivity remains (grep confirmed clean)
- [ ] Confirm cleanup: k3s containers are removed after test completion (requires Docker)

## Phase 3: Cluster Cleanup & Documentation

### 3.1 Document cluster cleanup procedure
- [x] Provided kubectl commands to clean up pending pods (inline in session)
- [x] Document namespace-scoped cleanup (delete pods by label selector `concourse.ci/worker`)
- [x] Document verification (check deployment pods still healthy after cleanup)
