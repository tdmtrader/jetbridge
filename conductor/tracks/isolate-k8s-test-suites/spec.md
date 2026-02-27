# Spec: Isolate K8s Test Suites

## Overview

Remove external cluster connectivity from both K8s test suites (`topgun/k8s_behavioral/` and `topgun/k8s/integration/`) and make them fully self-contained. Tests currently support connecting to an external Concourse cluster via `ATC_URL`/`KUBECONFIG` env vars, which caused 13,000 pending pods on the user's primary cluster. Phase 1 hardens the existing KinD-based approach. Phase 2 migrates to `testcontainers-go` for Go-native lifecycle management.

## Requirements

1. Remove all env-var-driven external cluster connectivity (`ATC_URL`, external `KUBECONFIG`, `K8S_NAMESPACE` for connecting to pre-existing deployments).
2. Make `topgun/k8s_behavioral/` exclusively use the existing `TestMain()` KinD path — no bypass.
3. Add self-sufficient KinD cluster creation to `topgun/k8s/integration/` (currently has no embedded cluster creation — only the external `hack/kind-integration.sh` script).
4. Migrate both suites from shell-orchestrated KinD to `testcontainers-go` with k3s module for Go-native lifecycle management.
5. Provide cluster cleanup procedure for the 13,000 pending pods on the primary cluster.

## Acceptance Criteria

- Running `go test ./topgun/k8s_behavioral/ -count=1 -v -timeout 30m` creates an ephemeral cluster, runs all tests, and tears down automatically.
- Running `go test ./topgun/k8s/integration/ -count=1 -v -timeout 30m` creates an ephemeral cluster, runs all tests, and tears down automatically.
- No environment variable can redirect tests to an external/production cluster.
- `testcontainers-go` manages k3s lifecycle via Go APIs (Phase 2).
- All existing tests continue to pass without modification to their assertions.
- `hack/kind-integration.sh` is removed (functionality absorbed into test suite).

## Out of Scope

- Adding new test cases or modifying test assertions.
- Changes to the `testflight/` suite.
- CI pipeline reconfiguration (pipelines adapt naturally once tests are self-sufficient).
- Changes to the Concourse runtime or Helm chart themselves.
