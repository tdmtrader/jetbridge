# Agent Instructions

Before doing any work in this repository, read `CLAUDE.md`.

## Docker runs on theborg, never locally

This Mac has **no Docker daemon** (Colima was removed 2026-08-02). Do not
suggest `colima start`, do not install a local Docker provider, and do not
report a step as blocked simply because `docker` cannot connect.

Any step needing Docker — image builds, `docker` CLI calls, the K8s suites —
runs against a Docker daemon hosted on **theborg**:

```bash
./hack/borg-docker.sh up
eval "$(./hack/borg-docker.sh env)"   # DOCKER_HOST=tcp://127.0.0.1:12375
```

theborg has no Docker on the host either — it is k3s/containerd — so this runs
`docker:dind` as a privileged pod on the cluster. **Never install Docker on the
theborg host**: it rewrites the host `iptables` FORWARD policy and can break
k3s/flannel networking on a live deployment.

Read `docs/docker-on-theborg.md` before doing Docker work. It records what this
setup can and cannot do — in particular, container ports published inside the
pod are **not** reachable from this Mac, so the testcontainers-based K8s suites
(`make test-k8s-integration`, `make test-k8s-behavioral`) still cannot be driven
from here and remain CI-only.

Tear the pod down (`./hack/borg-docker.sh down`) when the work is finished;
theborg hosts the live Concourse and has hit `DiskPressure` before.
