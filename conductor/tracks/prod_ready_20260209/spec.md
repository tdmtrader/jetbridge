# Spec: Production Readiness

## Overview

The JetBridge Kubernetes runtime is ~95% complete with core pod execution, volume management, worker registration, observability, and test coverage all implemented. This track closes the remaining gaps required to run JetBridge confidently in production:

1. **Implement `CreateVolumeForArtifact()`** — The last unimplemented method on the `Worker` interface.
2. **Validate artifact store integration end-to-end** — Ensure the artifact-helper sidecar and GCS FUSE storage class work correctly.
3. **Create a Helm chart** — Package JetBridge for standard Kubernetes deployment.
4. **Production validation testing** — Load testing, failure scenarios, and real-cluster validation.
5. **Deployment documentation** — Operational runbooks and configuration guides.

## Requirements

### R1: Implement `CreateVolumeForArtifact()`

**Current state:** `worker.go:157-159` returns `fmt.Errorf("jetbridge: CreateVolumeForArtifact not yet implemented")`.

**Required behavior:**
- Create a new artifact volume backed by the artifact store PVC (subpath allocation).
- Register the volume in the Concourse database as a `WorkerArtifact`.
- Ensure the volume is discoverable by subsequent steps via `LookupVolume()`.
- Ensure the Reaper can clean up orphaned artifact volumes.

**Acceptance criteria:**
- [ ] `CreateVolumeForArtifact()` returns a usable `runtime.Volume` and `db.WorkerArtifact`.
- [ ] Artifact volumes are visible to subsequent pipeline steps.
- [ ] Orphaned artifact volumes are cleaned up by the Reaper.
- [ ] Unit tests cover happy path, error cases, and cleanup.
- [ ] Integration test validates artifact creation in a real pipeline.

### R2: Artifact Store End-to-End Validation

**Required:**
- Verify artifact-helper sidecar container starts and functions correctly.
- Validate GCS FUSE StorageClass with ReadWriteMany access on GKE.
- Test artifact passing between get/put/task steps across different pods.
- Validate cleanup of artifact data after pipeline completion.

**Acceptance criteria:**
- [ ] A multi-step pipeline correctly passes artifacts between steps.
- [ ] Artifacts persist across pod recreations (e.g., step retry).
- [ ] GCS FUSE volume mounts work on GKE with Workload Identity.
- [ ] Artifact cleanup removes data from the shared PVC.

### R3: Helm Chart

**Required:**
- Helm chart for deploying Concourse with the JetBridge K8s runtime.
- Configurable values for: namespace, image registry, resource limits, PVC settings, RBAC, service accounts, observability endpoints.
- Includes all RBAC roles, service accounts, and config maps.
- Supports both local development (minikube/kind) and GKE production.

**Acceptance criteria:**
- [ ] `helm install` deploys a working Concourse with JetBridge runtime.
- [ ] All configurable parameters documented in `values.yaml` with comments.
- [ ] README with quickstart for local and GKE deployment.
- [ ] Helm lint passes, chart tests pass.

### R4: Production Validation Testing

**Required:**
- Load test: 50+ concurrent pipeline builds on a multi-node K8s cluster.
- Failure scenarios: node drain, pod eviction, API server restart, PVC full.
- Soak test: 24-hour continuous pipeline execution.
- Performance baseline: pod startup time, volume mount time, step-to-step handoff time.

**Acceptance criteria:**
- [ ] Load test completes without errors or resource leaks.
- [ ] All failure scenarios recover gracefully (no stuck builds, no orphaned pods).
- [ ] Performance baselines documented.
- [ ] No memory leaks in 24-hour soak test.

### R5: Deployment Documentation

**Required:**
- Production deployment guide (GKE-focused).
- Configuration reference for all JetBridge-specific settings.
- Troubleshooting guide for common issues.
- Monitoring setup guide (Prometheus/Grafana dashboards).

**Acceptance criteria:**
- [ ] A new operator can deploy JetBridge from the docs alone.
- [ ] All configuration options are documented with defaults and examples.
- [ ] Troubleshooting covers top 10 expected failure modes.

## Out of Scope

- Multi-cluster worker support (future track).
- GPU/accelerator resource scheduling.
- Pod Security Policy migration to Pod Security Standards (separate track).
- Upstream Concourse contribution or merge.
- Web UI changes.
