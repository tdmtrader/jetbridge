# Spec: Isolate K8s Test Suites

## Overview

Migrate both K8s test suites (`topgun/k8s_behavioral/` and `topgun/k8s/integration/`) from KinD to testcontainers-go/k3s for Go-native cluster lifecycle management.

Phase 1 (external cluster removal, KinD self-containment) was completed previously. This track now covers Phase 2: replacing KinD with testcontainers-go/k3s using a **hybrid approach** — testcontainers-go for cluster lifecycle, `crictl pull` for public images.

## Background

- Phase 1 (completed): Removed external cluster env vars, made both suites self-contained with KinD.
- Previous Phase 2 attempt (reverted in `49d32db90`): Used `k3s.LoadImages()` API which had manifest compatibility issues with certain Docker images (e.g., `concourse/mock-resource`).
- Benchmark proof (`174c29740`): k3s + crictl pull is 1.8x faster than KinD (19.5s vs 35.3s), 0 image load errors, no `kind` CLI dependency.

## Approach: Hybrid k3s + crictl pull

1. Use `testcontainers-go/modules/k3s` for cluster creation/teardown (Go-native, no `kind` CLI).
2. Use `k3sContainer.LoadImages()` **only** for locally-built images (Concourse) that aren't on any registry.
3. Use `crictl pull` via `docker exec <containerID>` for public images (busybox, alpine, mock-resource) — same reliable approach used by KinD today.
4. Use `k3sContainer.GetKubeConfig()` for kubeconfig extraction (Go API, no file management).
5. Colima compatibility: set `DOCKER_HOST` and `TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE` correctly.

## Requirements

1. Replace KinD cluster creation in `topgun/k8s_behavioral/` with testcontainers-go k3s.
2. Replace KinD cluster creation in `topgun/k8s/integration/` with testcontainers-go k3s.
3. Remove `kind` from prerequisite checks.
4. Use `crictl pull` for public images (not `LoadImages` API).
5. Use `LoadImages` only for locally-built Concourse image.
6. Handle Colima Docker socket detection automatically.

## Acceptance Criteria

- `go test ./topgun/k8s_behavioral/ -count=1 -v -timeout 30m` creates an ephemeral k3s cluster, runs all tests, and tears down.
- `go test ./topgun/k8s/integration/ -count=1 -v -timeout 30m` creates an ephemeral k3s cluster, runs all tests, and tears down.
- No `kind` binary required — only docker, helm, kubectl.
- All image loading succeeds with 0 errors.
- All existing tests pass without assertion changes.
- Cluster startup is faster than KinD (~10s vs ~25s).

## Out of Scope

- Adding new test cases or modifying test assertions.
- Changes to `testflight/` or the legacy `topgun/k8s/` suite.
- CI pipeline changes.
- Changes to the Concourse runtime or Helm chart.
