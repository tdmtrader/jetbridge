# Plan: Legacy Cleanup

## Phase 1: Delete dead directories and files
- `[x]` Delete `worker/` top-level directory (README.md, counting_wait_group.go, signals_unix.go, signals_windows.go, suite_test.go)
- `[x]` Delete `cmd/init/init.c`
- `[x]` Delete `hack/overrides/guardian.yml`
- `[x]` Delete `topgun/tasks/garden-deny-network.yml`

## Phase 2: Delete stale topgun BOSH test infrastructure
- `[x]` Delete `topgun/both/` directory
- `[x]` Delete `topgun/runtime/` directory
- `[x]` Delete `topgun/core/` directory
- `[x]` Delete `topgun/pcf/` directory
- `[x]` Delete `topgun/common/` directory
- `[x]` Delete `topgun/deployments/` directory
- `[x]` Delete `topgun/operations/` directory
- `[x]` Delete `topgun/pipelines/` directory
- `[x]` Delete `topgun/vault/` directory
- `[x]` Delete `topgun/certs/` directory
- `[x]` Delete `topgun/exec.go`, `topgun/fly.go`, `topgun/README.md`
- `[x]` Verify `topgun/k8s/` is retained and unmodified

## Phase 3: Delete stale testflight tests
- `[x]` Delete `testflight/container_hermetic_test.go`
- `[x]` Delete `testflight/container_limits_test.go`

## Phase 4: Update docker-compose files
- `[x]` Update `docker-compose.yml` — remove `worker` service, remove stale TSA env vars from `web` service
- `[x]` Update `integration/docker-compose.yml` — remove `CONCOURSE_RUNTIME: containerd` and other stale references

## Phase 5: Update Dockerfiles
- `[x]` Update `Dockerfile` — remove `init.c` build step (lines 17-19)
- `[x]` Update `Dockerfile.build` — remove port 2222 from `EXPOSE`

## Phase 6: Clean pipeline definitions
- `[x]` Update `deploy/concourse-pipeline.yml` — remove gardenruntime and baggageclaim grep filters
- `[x]` Update `deploy/borg-pipeline.yml` — remove gardenruntime and baggageclaim grep filters
- `[x]` Update `deploy/test-pipeline.yml` — remove gardenruntime and baggageclaim grep filters

## Phase 7: Update stale comments and docs
- `[x]` Update `atc/step_validator.go:94` — change "containerd runtime" to "Kubernetes runtime"
- `[x]` Update `atc/runtime/types.go:59` — remove Garden reference from Container comment
- `[x]` Update `atc/worker/jetbridge/container_test.go` — fix gardenruntime comment
- `[x]` Update `conductor/tech-stack.md` — remove containerd references, fix `worker/kubernetes/` paths

## Phase 8: Local sanity check
- `[x]` Run `go build ./cmd/concourse` — confirm build compiles
- `[x]` Grep for stale imports of `"github.com/concourse/concourse/worker"` — confirm zero matches
- `[x]` Verify `topgun/k8s/` is intact

## Phase 9: Commit and push to jetbridge
- `[x]` Commit all changes to `jetbridge` branch — `223c3feb1`
- `[x]` Push to `jetbridge` remote

## Phase 10: Add promote-to-main job to CI pipeline
- `[x]` Add `promote-to-main` job to `deploy/borg-pipeline.yml` — runs after `deploy` and `ci-agent-review` pass, pushes jetbridge HEAD to main
- `[~]` Commit and push to jetbridge
- `[ ]` CI pipeline on concourse.home runs: full test suite, self-deploy, agent reviews
- `[ ]` On green: CI promote-to-main job pushes `jetbridge` -> `main`
