# Spec: Legacy Cleanup

## Overview

Remove dead code, stale configuration, and outdated references left over from the upstream Concourse Garden/containerd/BaggageClaim/TSA architecture that are no longer used in the JetBridge K8s-native fork.

## Requirements

1. **Delete the `worker/` top-level directory** — Contains `README.md` (references Garden/BaggageClaim), `counting_wait_group.go`, `signals_unix.go`, `signals_windows.go`, `suite_test.go`. Nothing imports this package.

2. **Delete `cmd/init/init.c`** — A containerd PID-1 zombie reaper used only by the old local dev Dockerfile. No Go code references it.

3. **Delete `hack/overrides/guardian.yml`** — Docker-compose override for the removed Guardian runtime.

4. **Delete `topgun/tasks/garden-deny-network.yml`** — Garden-specific test task file.

5. **Delete stale BOSH-based topgun tests** — `topgun/both/` (worker landing/stalling/retiring via BOSH SSH + monit), `topgun/runtime/` (references TSA, `garden address`, placement strategies), `topgun/core/` (BOSH-based encryption/vault/credhub tests), `topgun/pcf/`, and supporting infrastructure (`topgun/common/`, `topgun/deployments/`, `topgun/operations/`, `topgun/pipelines/`, `topgun/vault/`, `topgun/certs/`, `topgun/exec.go`, `topgun/fly.go`, `topgun/README.md`). Retain only `topgun/k8s/` (which contains K8s-specific tests).

6. **Delete stale testflight tests** — `testflight/container_hermetic_test.go` (branches on `containerd` vs `guardian` runtime) and `testflight/container_limits_test.go` (references Guardian cgroup limits).

7. **Update `docker-compose.yml`** — Remove the entire `worker` service definition (references `command: worker` which doesn't exist, plus TSA/BaggageClaim/containerd env vars). Remove stale TSA env vars from the `web` service (`CONCOURSE_TSA_AUTHORIZED_KEYS`, `CONCOURSE_TSA_HOST_KEY`).

8. **Update `integration/docker-compose.yml`** — Remove `CONCOURSE_RUNTIME: containerd` and other stale worker/runtime references.

9. **Clean pipeline definitions** — Remove `grep -v /atc/worker/gardenruntime` and `grep -v /worker/baggageclaim/volume/driver` filters from `deploy/concourse-pipeline.yml`, `deploy/borg-pipeline.yml`, and `deploy/test-pipeline.yml` (these directories don't exist).

10. **Update `Dockerfile` (local dev)** — Remove the `init.c` build step (`build the init executable for containerd`).

11. **Update `Dockerfile.build`** — Remove `EXPOSE 2222` (TSA SSH port, not used in JetBridge).

12. **Update stale comments** — Fix `atc/step_validator.go:94` ("containerd runtime" -> K8s), `atc/runtime/types.go:59` (remove Garden reference from Container comment), `atc/worker/jetbridge/container_test.go` (gardenruntime comment).

13. **Update `conductor/tech-stack.md`** — Remove stale references to containerd as legacy runtime, fix `worker/kubernetes/` path references to `atc/worker/jetbridge/`.

## Acceptance Criteria

- No Go file in the repo imports `"github.com/concourse/concourse/worker"`.
- No references to Garden, Guardian, BaggageClaim, or TSA in non-comment production code (comments explaining "replaced by" are acceptable).
- `docker-compose.yml` no longer defines a `worker` service.
- Pipeline grep filters reference only directories that exist.
- `topgun/k8s/` is retained; all other topgun subdirectories are removed.
- The build compiles (`go build ./cmd/concourse`).
- CI pipeline on concourse.home passes: full test suite, self-deploy, agent reviews.
- On green, CI promotes `jetbridge` -> `main`.

## Out of Scope

- Cleaning up K8s-specific topgun tests (`topgun/k8s/`) that reference `baggageclaim.driver` Helm values -- those are Helm chart concerns, not dead code.
- Removing `go.mod` dependencies (already clean -- no Garden/containerd deps remain).
- Rewriting integration or testflight test suites for K8s.
- Updating `CONTRIBUTING.md` references to old workflows.
