# Plan: Production Readiness

## Phase 1: Complete Runtime Gaps

### Task 1.1: Implement `CreateVolumeForArtifact()`
- [x] Write tests for CreateVolumeForArtifact `63cb0370f`
  - Unit test: happy path — creates volume and returns artifact
  - Unit test: error case — artifact store PVC not configured
  - Unit test: error case — database registration fails
  - Unit test: cleanup — verify orphaned artifacts are reapable
- [x] Implement CreateVolumeForArtifact `63cb0370f`
  - Allocate a unique subpath on the artifact store PVC
  - Create the volume directory via exec into a running pod
  - Register the artifact in the Concourse database
  - Return a volume that supports StreamIn/StreamOut

### Task 1.2: Validate Artifact Store End-to-End
- [x] Write integration tests for artifact passing `c7f00c2ed`
  - Test: multi-step pipeline with get -> task -> put passing artifacts
  - Test: artifact persistence across pod restarts
  - Test: artifact cleanup after pipeline completion
- [x] Validate artifact store end-to-end on K8s `c7d95677f`
  - Live tests for artifact passing via PVC (any storage class)
  - Task→task, get→task→put pipeline chains through artifact store
  - Data integrity verification through tar/extract cycle
  - Artifact cleanup lifecycle validation

## Phase 2: Docker Build & Helm Chart

### Task 2.0: Multi-stage Dockerfile with frontend assets
- [x] Create `Dockerfile.build` — production multi-stage Dockerfile
  - Stage 1 (node): Install yarn 4, build Elm + LESS + webpack assets into web/public/
  - Stage 2 (go): Copy built assets, `go build` embeds them via `//go:embed public`
  - Stage 3 (runtime): Minimal ubuntu:22.04 with ca-certificates + dumb-init
  - Target: single `concourse` binary with embedded frontend, no `CONCOURSE_WEB_PUBLIC_DIR` needed
- [x] Add `build.sh` helper script for local image builds
  - Build image tagged `concourse-local:latest`
  - Optionally push to a registry

### Task 2.1: Create Helm Chart Structure
- [x] Implement Helm chart in `deploy/chart/`
  - Chart.yaml with metadata (based on official concourse-chart pattern)
  - values.yaml: image, postgres, web config, K8s backend flags, RBAC, secrets, PVCs
  - templates/: web-deployment, service, serviceaccount, role, rolebinding,
    secrets (key placeholder), PVCs (cache + artifact-store), ingress (optional)
  - No worker StatefulSet (JetBridge runs tasks as pods directly)
  - PostgreSQL as bundled deployment (toggleable for external DB)
  - Init container for key generation (session signing, TSA host key)
- [x] Validate: `helm lint` passes, `helm template` renders 351 lines (9 resources)

### Task 2.2: Helm Chart Documentation
- [x] Write values.yaml inline documentation (every parameter commented)
- [x] Create chart README with quickstart for k3s/ArgoCD deployment `1469ae0bd`

## Phase 3: Production Validation (Skipped — gap tests added to unit suite)

### Gap-Filling Tests (added to container_test.go)
- [x] StreamOut failure during streamInputs (non-artifact-store mode)
  - failingArtifact type that returns errors on StreamOut
  - Build fails with "streaming inputs" / "stream out artifact" error
  - Main command exec never runs when input streaming fails
- [x] StreamIn failure during streamInputs (tar extract fails)
  - fakeExecutor returns error during tar extract
  - Build fails with "streaming inputs" / "stream in to" error
- [x] Multiple inputs — first broken input fails the build
  - Good input followed by broken input
  - Error identifies the broken artifact by handle
- [x] Concurrent SetProperty/Properties without races
  - 20 goroutines writing + 20 goroutines reading simultaneously
  - Race detector passes
- [x] Concurrent container creation on shared clientset
  - 5 goroutines creating containers simultaneously
  - All succeed independently
- [x] Concurrent Run and pod creation on shared clientset
  - 5 goroutines running containers in parallel
  - All pods created successfully
- [x] Fixed pre-existing data race in Volume.StreamOut
  - spanErr shared between goroutines without synchronization
  - Moved tracing.End into goroutine that owns the span

### Skipped operational tasks (require live cluster)
- Task 3.1: Load Testing (50+ concurrent builds)
- Task 3.2: Failure Scenario Testing (node drain, eviction, PVC full)
- Task 3.3: Soak Testing (24-hour continuous run)

## Phase 4: Documentation

### Task 4.1: Production Deployment Guide
- [ ] Write GKE deployment guide
  - Prerequisites (GKE cluster, service accounts, storage classes)
  - Step-by-step Helm deployment
  - Post-deployment validation

### Task 4.2: Configuration Reference
- [ ] Document all JetBridge-specific configuration options
  - Environment variables, Helm values, CLI flags
  - Defaults, constraints, and examples

### Task 4.3: Troubleshooting Guide
- [ ] Write troubleshooting guide
  - Pod startup failures
  - Volume mount errors
  - Worker registration issues
  - Artifact passing failures
  - Performance degradation

### Task 4.4: Monitoring Setup Guide
- [ ] Write Prometheus/Grafana monitoring guide
  - Recommended metrics to track
  - Example dashboard JSON
  - Alerting rules for critical conditions
