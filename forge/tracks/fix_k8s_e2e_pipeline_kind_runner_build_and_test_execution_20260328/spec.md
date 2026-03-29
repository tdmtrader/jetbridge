# Spec: Fix k8s-e2e Pipeline Kind-Runner Build and Test Execution

**Track ID:** `fix_k8s_e2e_pipeline_kind_runner_build_and_test_execution_20260328`
**Type:** bugfix

## Overview

The `k8s-e2e` pipeline on concourse.home has never successfully run the k8s integration or behavioral tests. All 5 historical builds of `k8s-behavioral-tests` and all recent `k8s-integration-tests` builds have failed.

### Bug 1: Go module resolution in kind-runner container

The `k8s-integration-tests` job fails with:
```
topgun/k8s/integration/integration_suite_test.go:14:2: no required module provides package
github.com/concourse/concourse/topgun; to add it: go get github.com/concourse/concourse/topgun
```

The `topgun` package exists in the repo and compiles locally, but fails inside the kind-runner Docker image. The source is copied via `tar | kubectl exec` into a DinD builder pod's `/build` directory. The Go module cache is populated before the source copy (`go mod download` then `COPY . .`), so the module graph should be correct. Likely cause: the `topgun/` helper package (non-test `.go` files imported by test code) isn't being included in the Docker build context.

### Bug 2: Ginkgo "Found no test suites" for behavioral tests

The `k8s-behavioral-tests` job fails with:
```
ginkgo run failed
  Found no test suites
```

The `topgun/k8s_behavioral/` directory has a valid `behavioral_suite_test.go` and 20+ test files. The ginkgo binary in the kind-runner image may be incompatible, or the source files weren't copied correctly into the image.

### Root Cause

Both bugs stem from the `build-kind-runner` job's Docker image build process:
1. Source is cloned with `git clone --depth 1 --branch jetbridge` into `/tmp/fresh-src`
2. Source is tarred (excluding `.git`) and piped into the DinD builder pod via `kubectl exec`
3. The Dockerfile is written via heredoc with leading whitespace from YAML indentation
4. The `COPY . .` context is `/build` which contains the Dockerfile, helm binary, and source files

## Requirements

1. `k8s-integration-tests` must compile and run `go test ./topgun/k8s/integration/` inside the kind-runner container
2. `k8s-behavioral-tests` must find and run the ginkgo suite at `./topgun/k8s_behavioral/`
3. The kind-runner image must contain the full repo source at `/src` with working Go module context
4. Both test jobs must build the concourse binary and create a KinD-compatible Docker image

## Acceptance Criteria

- [ ] `k8s-e2e/k8s-integration-tests` passes (creates KinD cluster, deploys Concourse, runs topgun integration suite)
- [ ] `k8s-e2e/k8s-behavioral-tests` starts running tests (ginkgo finds the suite and begins execution)
- [ ] Behavioral tests complete with ≤3 flaky failures (known GC timing issues per CLAUDE.md)

## Out of Scope

- Fixing the ~3 flaky behavioral test specs (GC timing dependent, documented in CLAUDE.md)
- Cross-node artifact passing in behavioral tests (single-node KinD cluster)
