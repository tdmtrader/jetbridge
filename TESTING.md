# Testing Guide

## Quick Start

```bash
make test-quick    # Unit tests (~3 min, needs PostgreSQL)
make test-all      # Everything including K8s tests (hours)
```

> **Docker runs on theborg, not on this machine.** This Mac has no Docker
> daemon (Colima was removed 2026-08-02). Tiers 1–3 below need no Docker and run
> locally. Anything that does need it — image builds and the two K8s tiers —
> goes through a `docker:dind` pod on theborg's k3s cluster:
>
> ```bash
> ./hack/borg-docker.sh up && eval "$(./hack/borg-docker.sh env)"
> ```
>
> Read **[docs/docker-on-theborg.md](docs/docker-on-theborg.md)** first. It
> covers the one limitation that matters here: ports published by containers
> are not reachable from this Mac, so the K8s tiers cannot be driven from a
> local `go test` and remain CI-only.

## Test Tiers

### 1. Unit Tests (`make test-unit`)

Runs all Ginkgo test suites excluding integration/e2e. Uses parallel execution across packages.

- **Time:** ~3 minutes
- **Prerequisites:** PostgreSQL running on localhost (port 5432 or via `initdb`)
- **What it covers:** 79 test suites across atc/, fly/, skymarshal/, go-concourse/, tracing/

```bash
# Run a specific package
ginkgo ./atc/db/
ginkgo ./atc/exec/
ginkgo ./fly/commands/
```

### 2. Fly Integration Tests (`make test-fly-integration`)

Tests the `fly` CLI binary against a mock ATC server.

- **Time:** ~30 seconds (after initial fly binary build)
- **Prerequisites:** None (builds fly binary, uses mock HTTP)
- **What it covers:** 576 specs covering all fly commands

```bash
ginkgo -r ./fly/integration/
```

### 3. ATC Integration Tests (`make test-integration`)

Starts a real ATC process and tests API behavior.

- **Time:** ~12 seconds
- **Prerequisites:** PostgreSQL running locally
- **What it covers:** 21 specs covering full API request/response flows (1 pending: team migration)

```bash
ginkgo -r -p ./atc/integration/
```

### 4. K8s Integration Tests (`make test-k8s-integration`)

Creates an ephemeral K3s cluster via **testcontainers** (`rancher/k3s:v1.31.6-k3s1`)
and deploys Concourse via Helm. This suite does **not** use KinD — no `kind`
binary is invoked anywhere in `topgun/`.

- **Time:** ~23 minutes (including cluster creation/teardown)
- **Prerequisites:** Docker, Helm, kubectl
- **Where it runs:** **CI only** (`k8s-e2e` on concourse.home). It cannot be
  driven from this Mac: the suite reaches the K3s API server through a
  testcontainers-mapped port, and mapped ports on the theborg dind pod are not
  reachable from here. See [docs/docker-on-theborg.md](docs/docker-on-theborg.md)
  for the two candidate ways to close that gap.
- **What it covers:** 117 specs (7 pending) — pipeline execution, volume passing, pod lifecycle. ~2 pod cleanup specs are flaky due to GC timing.

```bash
# Uses CONCOURSE_IMAGE env var (default: concourse-local:latest)
go test ./topgun/k8s/integration/ -count=1 -v -timeout 30m

# Image rebuild for iteration — needs DOCKER_HOST pointed at theborg, which is
# linux/amd64, so Dockerfile.build no longer needs qemu emulation.
eval "$(./hack/borg-docker.sh env)"
docker build -f Dockerfile.build -t concourse-local:latest .
```

### 5. K8s Behavioral Tests (`make test-k8s-behavioral`)

Full behavioral test suite with one testcontainers K3s cluster per Ginkgo process.

- **Time:** 2-3 hours (with 2 procs)
- **Prerequisites:** Docker, Helm, kubectl (needs significant CPU/memory)
- **Where it runs:** **CI only**, for the same mapped-port reason as tier 4.
- **What it covers:** 302 specs — resource checking, pipeline behavior, volumes, hijacking
- **Note:** Default 2 parallel procs. 4 procs may time out during cluster setup on resource-constrained machines. Override with `K8S_PROCS=4 make test-k8s-behavioral`.

```bash
# Default (2 parallel K3s containers)
make test-k8s-behavioral

# More parallelism if your machine can handle it
K8S_PROCS=4 make test-k8s-behavioral

# Manual single-proc for debugging
ginkgo --procs=1 -v --timeout=3h --output-interceptor-mode=none ./topgun/k8s_behavioral/
```

### 6. jetbridge Live Tests — real cluster, no Docker

`atc/worker/jetbridge/live_*_test.go` talk to a real Kubernetes API with
client-go, not Docker, so they run from this Mac directly against theborg. They
are the cheapest real-cluster coverage available locally. Use a **throwaway**
namespace — never `cicd` or `concourse`, which host live workloads.

```bash
kubectl --context theborg create namespace jb-live-test
KUBECONFIG=~/.kube/config K8S_TEST_NAMESPACE=jb-live-test \
  go test -tags live -run '^TestLive' -v -count=1 -timeout 15m ./atc/worker/jetbridge/
kubectl --context theborg delete namespace jb-live-test
```

## Prerequisites

| Tool | Required For | Install |
|------|-------------|---------|
| Go 1.25+ | All tests | [go.dev](https://go.dev/dl/) |
| Ginkgo v2 | All Ginkgo suites | `go install github.com/onsi/ginkgo/v2/ginkgo@latest` |
| PostgreSQL 14+ | Unit, integration tests | `brew install postgresql@14` |
| Docker **daemon** | K8s tests, image builds | Not installed locally — use `./hack/borg-docker.sh up` ([docs](docs/docker-on-theborg.md)) |
| Docker **CLI** | talks to the theborg daemon | `brew install docker` (client only) |
| Helm | K8s tests | `brew install helm` |
| kubectl | K8s tests, live tests | `brew install kubectl` |

KinD is **not** a prerequisite. The K8s suites moved to testcontainers K3s;
older docs and Makefile checks that demanded a `kind` binary were stale.

## Troubleshooting

### Tests hang or timeout

- **PostgreSQL not running:** Unit and integration tests need Postgres. Check with `pg_isready`.
- **Port conflicts:** ATC integration tests bind to ports `9090+N`. Kill any conflicting processes.
- **K8s tests slow:** K3s container creation takes 2-5 minutes. First run is always slower.

### `Cannot connect to the Docker daemon`

Expected on this Mac — there is no local daemon. Bring up the theborg one and
export `DOCKER_HOST`:

```bash
./hack/borg-docker.sh up && eval "$(./hack/borg-docker.sh env)"
```

If it was already up, the supervised port-forward may have died with the
shell that started it; `./hack/borg-docker.sh status` shows the pod, the
forward, and the daemon, and `up` is idempotent.

### A remote `docker build` appears to hang before step 1

It is uploading the build context. `Dockerfile.build` does `COPY . .`, and the
whole context crosses the port-forward first. Check `.dockerignore` covers any
new local scratch directory.

### Flaky K8s behavioral tests

~3 out of 117 specs may fail due to GC timing. Built-in retries handle most pod race conditions (container-not-found, pod-terminated-before-exec).

### Running a single test

```bash
# By name
ginkgo --focus="creates the volume" ./atc/api/

# By file:line (Ginkgo v2)
ginkgo --focus-file=artifacts_test.go ./atc/api/
```
