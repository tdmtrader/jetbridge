# Testing Guide

See [CLAUDE.md’s Running Tests section](CLAUDE.md#running-tests) for test tiers, commands, prerequisites, and timing.

## Disposable K3s and deployed-cluster acceptance

In CI the disposable K3s tier is the `k8s-e2e` pipeline (`deploy/k8s-e2e-pipeline.yml`): `build-kind-runner`
→ `hangar-generated-pod-contract` → `k8s-behavioral-tests`, on a 24 h timer and on every `core` commit.
The other contract jobs (`hangar-disk-store-contract`, `hangar-cluster-contract`,
`run-cancellation-kubelet`) hang off `build-kind-runner` and gate nothing.
The jetbridge pipeline's `k8s-live-tests` job is a different thing: the `-tags live` suites in
`atc/worker/jetbridge`, which run after `self-upgrade` against the cluster's *deployed* artifact
daemon (hostPath, node-IP dialing, mTLS SANs, capability key) as a post-upgrade acceptance check.
`TestMain` reads the deployed web pod and daemon once and stops the run if either is missing or
unreadable, and `TestLiveDeployedFeatureSet` fails unless the deployment has exactly the features
`atc/worker/jetbridge/testdata/live_deployment.json` says it has. That manifest is also the live
coverage map: each feature that is on names the `live` tests covering it, or says why none can.
Changing a feature in home-infra's `apps/concourse.yaml` therefore means changing the manifest too
(another deployment passes its own via `K8S_LIVE_DEPLOYMENT_MANIFEST`).

Two properties of the CI node these tiers depend on, both learned the hard way on 2026-09-04:
every `dockerd` started inside a pod (the DinD builders and the in-task daemon here) must run with
`--mtu=1450` or full-size segments from package mirrors are dropped and apt "times out"; and
`fs.inotify.max_user_instances` must be raised from Ubuntu's 128 (home-infra pins 1024) or the
kubelet fails every follow-logs stream, which is how testcontainers waits for K3s to be ready.

The behavioral suite creates its K3s clusters from inside the suite via `testcontainers-go/modules/k3s`, then deploys Concourse via Helm.

```bash
# Image rebuild for iteration (Dockerfile.build, every binary + frontend).
# The suite loads CONCOURSE_IMAGE (default: concourse-local:latest).
./build.sh
```

### K8s behavioral execution

Full behavioral test suite with parallel K3s containers (one per process).

- **Note:** Default 2 parallel procs. 4 procs may time out during cluster setup on resource-constrained machines. Override with `K8S_PROCS=4 make test-k8s-behavioral`.

```bash
# Default (2 parallel K3s containers)
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
| PostgreSQL 14+ server binaries on `PATH` | Unit tests (each runner starts its own `postgres`) | `brew install postgresql@14` |
| Docker | K8s tests | [docker.com](https://www.docker.com/products/docker-desktop/) |
| Helm | K8s tests | `brew install helm` |
| kubectl | K8s tests | `brew install kubectl` |

## Troubleshooting

### Tests hang or timeout

- **PostgreSQL binaries missing:** the unit tests start their own `postgres` from `PATH`; check with `command -v initdb postgres`. No running server is needed.
- **Port conflicts:** the in-process ATC suite (`atc/integration`) binds to ports `9090+N`. Kill any conflicting processes.
- **K8s tests slow:** K3s container startup takes 2-5 minutes. First run is always slower.

### Flaky K8s behavioral tests

Pod cleanup specs may fail due to GC timing. Built-in retries handle most pod race conditions (container-not-found, pod-terminated-before-exec).

### Running a single test

```bash
# By name
ginkgo --focus="creates the volume" ./atc/api/

# By file:line (Ginkgo v2)
ginkgo --focus-file=artifacts_test.go ./atc/api/
```
