# Fully Local Development (no theborg / concourse.home)

> ## ⚠️ Superseded for every Docker-backed tier
>
> **Colima was removed from this Mac on 2026-08-02.** There is no local Docker
> daemon, so **Tier 2 onward does not work as written**, and reinstalling
> Colima is not the plan: Docker now runs as a pod on **theborg**.
>
> ```bash
> ./hack/borg-docker.sh up && eval "$(./hack/borg-docker.sh env)"
> ```
>
> Read **[docker-on-theborg.md](docker-on-theborg.md)** for what that setup can
> and cannot do. The short version: daemon-side work (`build`, `pull`, `push`,
> `save`/`load`, `run`) works and is now native `linux/amd64`; ports published
> by containers are **not** reachable from this Mac, which is what keeps KinD
> and the testcontainers K8s suites out of reach locally.
>
> What still works unchanged:
> - **Tier 1** (unit/integration tests) — needs only Go and local PostgreSQL.
> - **The jetbridge `live` tests** — they use client-go against a real cluster,
>   not Docker. Point them at a throwaway namespace on theborg instead of the
>   KinD cluster in Tier 3 (never `cicd` or `concourse`).
>
> Tiers 2–5 below are kept as the **historical 2026-07-11 record** of the
> Colima/KinD setup — accurate for that date, not reproducible today.

Verified 2026-07-11 on this Mac (Apple Silicon, 10 CPU / 24 GiB, Darwin 24.6,
go 1.25.6, Colima, kind v1.35.0 node image, helm, kubectl). Everything below
was actually run; anything not verified is explicitly marked.

TL;DR (historical): `./hack/local-dev-up.sh` stood up the whole thing (Colima +
KinD + locally built image + Helm chart with artifact daemon). All 22 jetbridge
live tests and a real two-task pipeline (with cross-step volume passing) passed
on the local cluster. The script depends on Colima and will not run today.

## Tier 1 — Unit tests (no Docker needed)

Requires local PostgreSQL (`pg_isready`).

```bash
make test-quick        # alias for test-unit: Ginkgo unit suites (~5m)
make test-dev-mcp      # retained ci-agent/devmcp server tests, no Postgres needed
make test-fly-integration
make test-integration  # real ATC + Postgres, ~12s
```

Follow CLAUDE.md conventions (no `--race`; if `database "testdb_template"
already exists`, another test run is active — wait, don't delete).

## Tier 2 — Colima / Docker (HISTORICAL — Colima is gone)

Replaced by `./hack/borg-docker.sh up`; see
[docker-on-theborg.md](docker-on-theborg.md). Retained for context only.

```bash
colima start --cpu 4 --memory 8   # ~40s; profile on this machine is docker+k3s
docker ps
```

Notes:
- `colima start` switches the kubectl current-context to `colima` (the VM's
  built-in k3s). If you care about the default context, switch it back with
  `kubectl config use-context <ctx>`. Nothing below depends on the current
  context — every command uses an explicit `--kubeconfig`.
- Colima's built-in k3s (`colima` context) is a usable second local cluster,
  but the verified workflow below uses KinD.

## Tier 3 — Local K8s cluster + jetbridge live tests (KinD part HISTORICAL)

The **live tests themselves still work today** — they need a Kubernetes API, not
Docker. Run them against a throwaway namespace on theborg (see TESTING.md tier
6). Only the KinD cluster below is unreachable now.

KinD on Colima worked. Use an **isolated kubeconfig** so `~/.kube/config`
(and the theborg context) is never touched:

```bash
kind create cluster --name jetbridge-local --kubeconfig .local-cluster/kubeconfig
export KUBECONFIG=$PWD/.local-cluster/kubeconfig
kubectl create namespace jb-live-test   # throwaway ns, no pod-security label
```

Run the live tests (plain `go test`, build tag `live`):

```bash
KUBECONFIG=$PWD/.local-cluster/kubeconfig \
K8S_TEST_NAMESPACE=jb-live-test \
go test -tags live -run '^TestLive' -v -count=1 -timeout 15m ./atc/worker/jetbridge/
```

Result (verified): **19/22 pass** with no extra setup, including
`TestLiveSidecarLogStreamTimeout` (the one that genuinely needs a live
cluster). The 3 `TestLiveVolume*` tests fail without a DaemonSet artifact
cache — they need the deployed artifact daemon from Tier 5. With it deployed
(see below), add two env vars and **all 22 pass** (verified):

```bash
KUBECONFIG=$PWD/.local-cluster/kubeconfig \
K8S_TEST_NAMESPACE=jb-live-test \
ARTIFACT_DAEMON_HOST_PATH=/var/concourse/artifacts \
ARTIFACT_DAEMON_SERVICE=concourse-concourse-jetbridge-artifact-daemon \
go test -tags live -run '^TestLive' -v -count=1 -timeout 15m ./atc/worker/jetbridge/
```

(The hostPath is node-global, so the daemon installed in the `concourse`
namespace serves test pods in `jb-live-test` on the same single kind node.)

## Tier 4 — K8s integration suites (testcontainers/K3s) — still CI-only

Current status (2026-08-02): these remain CI-only, now for a *different*
reason than below. Against the theborg dind daemon the blocker is that
testcontainers reaches the K3s API server on a published port, and published
ports live in the pod's netns — unreachable from this Mac.
[docker-on-theborg.md](docker-on-theborg.md) records the two candidate fixes
(route `10.42.0.0/24` + `TESTCONTAINERS_HOST_OVERRIDE`, or co-locate the test
process in the dind pod). The Colima-era analysis below is historical.

Historically these failed under Colima ("K3s-in-Docker-in-Colima namespace
errors"). **That failure mode is gone** with current Colima (docker 28.x) —
a raw `docker run --privileged rancher/k3s:v1.31.6-k3s1 server` reaches node
Ready with system pods Running.

The remaining gotcha is that testcontainers-go v0.41 does not honor the
docker CLI context and panics with `rootless Docker not found`. Fix: point it
at Colima's socket explicitly:

```bash
export DOCKER_HOST=unix://$HOME/.colima/default/docker.sock
export TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock

# needs concourse-local:latest in the docker daemon (see Tier 5 image build).
# The suite resolves and pins busybox:1.37.0 for the artifact helper by
# default. To test a project-owned helper, set ARTIFACT_HELPER_IMAGE to an
# exact repository@sha256:<64 lowercase hex> reference.
go test ./topgun/k8s/integration/ -count=1 -v -timeout 30m   # full suite ~30m — not run in full
```

Probe result (single focused spec, 2026-07-11): **still fails — remains
CI-only.** With `DOCKER_HOST` set, TestMain gets much further than before:
the K3s testcontainer boots, node goes Ready, images load, and Helm deploys.
But every pod in the cluster (kube-system included) then flaps with
`Pod sandbox changed, it will be killed and re-created` /
`sandbox container "..." is not running` (containers exit 137, not OOM —
the VM had >5 GiB free), and the suite aborts at
`artifact daemon is required for tests`. Notably a *raw*
`docker run --privileged rancher/k3s:v1.31.6-k3s1 server` on the same Colima
runs pods stably, so the instability is specific to how testcontainers
launches the K3s container — plausibly fixable someday, but out of scope.
Both `topgun/k8s/integration/` and `topgun/k8s_behavioral/` (same mechanism)
therefore stay on the k8s-e2e pipeline on concourse.home.

Note this is NOT a coverage gap for the runtime itself: the same chart +
image deploy and run pipelines fine in KinD (Tier 5), and all 22 live tests
pass there.

## Tier 5 — Local Concourse deployment (Helm chart in KinD) — HISTORICAL

Needs Colima + KinD, so it does not run today. The equivalent now is a Helm
install into a throwaway namespace on theborg, with the image built on the
theborg dind daemon (native amd64 — the qemu caveat at the end of this document
no longer applies).

One command: `./hack/local-dev-up.sh`. Manual steps it automates:

### 1. Build the image (fast path, ~2 min, no emulation)

`build.sh`/`Dockerfile.build` target linux/amd64 and rebuild the Elm frontend
under qemu — very slow on Apple Silicon. Use the documented fast path
(TESTING.md) instead — checked-in `web/public/` assets are embedded via
`//go:embed`:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build \
  -ldflags "-s -w -X github.com/concourse/concourse.Version=0.0.0-local" \
  -o concourse-linux-arm64 ./cmd/concourse
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "-s -w" \
  -o artifact-daemon-linux-arm64 ./cmd/artifact-daemon
mkdir -p fly-assets && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build \
  -o /tmp/fly-linux-arm64 ./fly && \
  tar czf fly-assets/fly-linux-arm64.tgz -C /tmp -s '/fly-linux-arm64/fly/' fly-linux-arm64
docker build -f Dockerfile.local -t concourse-local:latest .
```

### 2. Load into KinD

`kind load docker-image` fails with containerd image stores; use the archive:

```bash
docker save concourse-local:latest -o /tmp/concourse-local.tar
kind load image-archive --name jetbridge-local /tmp/concourse-local.tar

# Runtime helpers execute with elevated filesystem authority, so the chart
# rejects mutable tags. Resolve the helper actually pulled by Docker and load
# those exact bytes into KinD.
docker pull docker.io/library/busybox:1.37.0
export ARTIFACT_HELPER_IMAGE="$(
  docker image inspect --format '{{index .RepoDigests 0}}' \
    docker.io/library/busybox:1.37.0
)"
docker save docker.io/library/busybox:1.37.0 -o /tmp/artifact-helper.tar
kind load image-archive --name jetbridge-local /tmp/artifact-helper.tar
```

### 3. Helm install — required local overrides

```bash
helm upgrade --install concourse ./deploy/chart \
  --kubeconfig $PWD/.local-cluster/kubeconfig \
  -n concourse --create-namespace \
  --set image.tag=latest \
  --set image.pullPolicy=Never \
  --set-string kubernetes.artifactHelperImage="${ARTIFACT_HELPER_IMAGE}" \
  --set postgresql.persistence.enabled=false \
  --set artifactDaemon.enabled=true
```

Why each override (both failure modes were hit and verified):

- `postgresql.persistence.enabled=false` — kind's `standard` storage class is
  a hostPath-style local-path PV, where `fsGroup` is not applied; postgres
  (uid 999) fails `initdb` with `could not change permissions ... Operation
  not permitted`. emptyDir honors fsGroup. (Data is ephemeral — fine locally.)
- `artifactDaemon.enabled=true` is the values.yaml default; kept explicit
  because the web node refuses to start without
  `--kubernetes-artifact-daemon-host-path` on the K8s runtime.

### 4. Use it

```bash
export KUBECONFIG=$PWD/.local-cluster/kubeconfig
kubectl -n concourse port-forward svc/concourse-concourse-jetbridge-web 18080:8080 &
go build -o /tmp/fly ./fly
/tmp/fly -t local login -c http://localhost:18080 -u test -p test
/tmp/fly -t local set-pipeline -p hello -c hello.yml -n
/tmp/fly -t local unpause-pipeline -p hello
/tmp/fly -t local trigger-job -j hello/hello -w
```

Verified end-to-end: a two-task pipeline (task writes an output, second task
reads it as an input) ran to `succeeded` on worker `k8s-concourse` — i.e.
scheduling, pod execution, and cross-step volume passing through the
DaemonSet artifact cache all work fully locally. This is sufficient for
dogfood-style pipeline runs without the home cluster.

### Teardown

```bash
./hack/local-dev-up.sh --destroy       # deletes kind cluster + .local-cluster/
colima stop                            # optional
```

## What cannot run locally

- **theborg-specific work**: pulling live logs from concourse.home, the
  `jetbridge release pipeline`, GHCR publishing, and the k8s-e2e CI pipeline
  itself. These need the home network.
- **Full behavioral suite verification**: mechanically unblocked (see Tier 4)
  but a full 2-3h run on this 24 GiB machine with Colima capped at 8 GiB is
  untested; treat CI as authoritative.
- **amd64 image builds via `build.sh`**: qemu emulation of the Elm/Go build was
  impractically slow on Apple Silicon, so arm64 (`Dockerfile.local`) was the
  local path. **No longer a constraint** — the theborg dind daemon is native
  `linux/amd64`, so `Dockerfile.build` compiles without emulation there. The
  cost moves to uploading the build context over the port-forward.

## Status summary (2026-07-11)

Historical record of a point-in-time run. Note that `make test-quick` is now a
plain alias for `make test-unit` and no longer touches `ci-agent` — the v1
standalone phase runner that module held was deleted with the v1 agentic
surface. A small slice of `ci-agent` (the dev-mcp server, its binary, and
`dev-mcp.yml`) is deliberately retained as the in-repo build/test MCP
implementation — see `ci-agent/RETAINED.md` — and is exercised separately by
`make test-dev-mcp`, not by `test-quick`/`test-unit`.

| Tier | Status |
|------|--------|
| 1. `make test-quick` | PASS (84 suites, 5m19s) |
| 2. Colima + docker | PASS (4 CPU / 8 GiB) |
| 3. Live tests on KinD | 22/22 PASS (3 need the deployed artifact daemon + 2 env vars) |
| 4. topgun/k8s/integration probe | FAIL — pod sandbox churn in testcontainers K3s; CI-only |
| 5. Helm deploy + real pipeline | PASS (hello pipeline succeeded, volume passing OK) |
