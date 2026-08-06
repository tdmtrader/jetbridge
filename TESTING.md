# Testing Guide

## Quick Start

```bash
make test-postgres-up
eval "$(./hack/test-postgres.sh env)"
make test-quick
make test-all      # Everything including K8s tests (hours)
```

The named `concourse-test-postgres` container stays up so independent test
commands can run concurrently. Run `make test-postgres-down` only as explicit
teardown after tests have finished; it is unsafe to run while tests are active.

## Test Tiers

### 1. Unit Tests (`make test-unit`)

Runs all Ginkgo test suites excluding integration/e2e. Uses parallel execution across packages.

- **Time:** ~3 minutes
- **Prerequisites:** the shared test PostgreSQL service (`make test-postgres-up`)
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
- **Prerequisites:** the shared test PostgreSQL service (`make test-postgres-up`)
- **What it covers:** 21 specs covering full API request/response flows (1 pending: team migration)

```bash
ginkgo -r -p ./atc/integration/
```

### 4. K8s Integration Tests (`make test-k8s-integration`)

Creates a KinD (Kubernetes-in-Docker) cluster and deploys Concourse via Helm.

- **Time:** ~23 minutes (including KinD cluster creation/teardown)
- **Prerequisites:** Docker, KinD, Helm, kubectl
- **What it covers:** 117 specs (7 pending) — pipeline execution, volume passing, pod lifecycle. ~2 pod cleanup specs are flaky due to GC timing.

```bash
# Uses CONCOURSE_IMAGE env var (default: concourse-local:latest)
go test ./topgun/k8s/integration/ -count=1 -v -timeout 30m

# Fast image rebuild for iteration
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o concourse-linux-arm64 ./cmd/concourse
docker build -f Dockerfile.local -t concourse-local:latest .
```

### 5. K8s Behavioral Tests (`make test-k8s-behavioral`)

Full behavioral test suite with parallel KinD clusters (one per process).

- **Time:** 2-3 hours (with 2 procs)
- **Prerequisites:** Docker, KinD, Helm, kubectl (needs significant CPU/memory)
- **What it covers:** 302 specs — resource checking, pipeline behavior, volumes, hijacking
- **Note:** Default 2 parallel procs. 4 procs may time out during cluster setup on resource-constrained machines. Override with `K8S_PROCS=4 make test-k8s-behavioral`.

```bash
# Default (2 parallel KinD clusters)
make test-k8s-behavioral

# More parallelism if your machine can handle it
K8S_PROCS=4 make test-k8s-behavioral

# Manual single-proc for debugging
ginkgo --procs=1 -v --timeout=3h --output-interceptor-mode=none ./topgun/k8s_behavioral/
```

## Prerequisites

| Tool | Required For | Install |
|------|-------------|---------|
| Go 1.25+ | All tests | [go.dev](https://go.dev/dl/) |
| Ginkgo v2 | All Ginkgo suites | `go install github.com/onsi/ginkgo/v2/ginkgo@latest` |
| PostgreSQL 14+ | Unit, integration tests | `make test-postgres-up` (existing Colima runtime) |
| Docker | K8s tests | [docker.com](https://www.docker.com/products/docker-desktop/) |
| KinD | K8s tests | `brew install kind` |
| Helm | K8s tests | `brew install helm` |
| kubectl | K8s tests | `brew install kubectl` |

## Troubleshooting

### Tests hang or timeout

- **PostgreSQL not running:** Unit and integration tests need the shared service. Run `make test-postgres-up`, then check with `pg_isready -h 127.0.0.1 -p 15432 -U postgres`.
- **Port conflicts:** ATC integration tests bind to ports `9090+N`. Kill any conflicting processes.
- **K8s tests slow:** KinD cluster creation takes 2-5 minutes. First run is always slower.

### Flaky K8s behavioral tests

~3 out of 117 specs may fail due to GC timing. Built-in retries handle most pod race conditions (container-not-found, pod-terminated-before-exec).

### Running a single test

```bash
# By name
ginkgo --focus="creates the volume" ./atc/api/

# By file:line (Ginkgo v2)
ginkgo --focus-file=artifacts_test.go ./atc/api/
```
