# Docker on theborg

**This Mac has no Docker.** Colima was removed on 2026-08-02. Any step in this
repository that needs a Docker daemon — image builds, the K8s test suites,
anything invoking the `docker` CLI — must run against **theborg**, not locally.

Everything in this document was verified on 2026-08-02.

## The short version

```bash
./hack/borg-docker.sh up               # ~1 min: dind pod on theborg + local forward
eval "$(./hack/borg-docker.sh env)"    # exports DOCKER_HOST
docker info                            # linux/amd64, 12 CPU, 31 GiB
# ... do the Docker work ...
./hack/borg-docker.sh down             # deletes the namespace, stops the forward
```

## What theborg actually is

theborg is **not a Docker host**. It runs k3s with containerd:

| | |
|---|---|
| OS | Ubuntu 20.04.3 LTS, 12 CPU, 31 GiB RAM, 181 GiB free on `/` |
| Kubernetes | k3s v1.34.3+k3s1, single control-plane node, kube-context `theborg` |
| Container runtime | `containerd://2.1.5-k3s1` (socket `/run/k3s/containerd/containerd.sock`) |
| Present | `ctr`, `crictl`, `k3s` |
| Absent | `docker`, `podman`, `nerdctl`, `buildah` — **there is no `/run/docker.sock`** |
| Access | SSH as `tdmtrader` (`ssh theborg`); `sudo` **requires a password** |
| Cluster access | kubectl `theborg` context has cluster-admin |

So "connect Docker to theborg" cannot mean "point `DOCKER_HOST` at the host" —
there is nothing listening. Two things could have made one exist, and only one
of them is a good idea:

- **Installing Docker Engine on the host** is possible (`download.docker.com`
  focal packages resolve; Ubuntu's own `docker.io` 26.1.3 is available) but
  needs an interactive `sudo` password, and it is **not recommended**: Docker
  takes over `iptables` on install and sets the `FORWARD` chain policy to
  `DROP`, which is a well-known way to break flannel/k3s pod networking on a
  live cluster. theborg runs real workloads. Don't do this without a
  deliberate, supervised maintenance window.
- **Running Docker as a pod on the k3s cluster** (`docker:dind`, privileged)
  keeps Docker's networking entirely inside the pod's own netns and touches
  nothing on the host. This is what `hack/borg-docker.sh` does, and it is the
  supported path.

## How the pod path works

`hack/borg-docker.sh up` creates namespace `borg-docker` with a single
privileged `docker:28-dind` pod listening on `tcp://0.0.0.0:2375` (TLS off —
it is only ever reached through the forward), then supervises a
`kubectl port-forward` from `127.0.0.1:12375` on this Mac.

Knobs (env vars): `BORG_DOCKER_NAMESPACE`, `BORG_DOCKER_CONTEXT`,
`BORG_DOCKER_PORT`, `BORG_DOCKER_IMAGE`, `BORG_DOCKER_CPU`,
`BORG_DOCKER_MEMORY`, `BORG_DOCKER_DISK`.

Two deliberate choices in the manifest:

- **No `hostNetwork`.** With host networking, dind's iptables rules land in
  theborg's host netns and can break k3s — the exact failure mode that makes
  installing Docker on the host unattractive.
- **`emptyDir` for `/var/lib/docker`.** The image cache is per-pod and dies
  with the namespace. `down` then `up` starts from a cold cache. If you want a
  warm cache across sessions, leave the pod up rather than adding a PVC.

### Verified working

| Capability | Evidence |
|---|---|
| Remote daemon | `docker version` → server 28.5.2, `linux/amd64` |
| Host resources | `docker info` → 12 CPU, 33.6 GB, `overlay2` |
| Running containers | `docker run --rm alpine:3.20 uname -m` → `x86_64` |
| Nested privileged (so k3s/KinD-in-Docker can boot) | `docker run --privileged alpine sh -c 'mount -t tmpfs none /mnt'` → OK |
| Image builds | `docker build` returns an image digest; a full-repo `COPY . /src` build completes once `.dockerignore` is tight (see below) |

`linux/amd64` is a real upgrade over the old Colima setup, not just a
substitute: `Dockerfile.build` targets amd64 and used to need qemu emulation on
Apple Silicon, which `docs/local-dev.md` records as "impractically slow". On
theborg it is native.

### Verified limitation — published ports do not reach this Mac

This is the one that decides which workflows are possible:

```
docker run -d -P nginx:alpine    →   mapped to 0.0.0.0:32768
curl http://127.0.0.1:32768/     from this Mac   →  connection refused (exit 7)
wget http://127.0.0.1:32768/     inside the pod  →  200, serves the page
```

`-p`/`-P` publishes onto the **pod's** network namespace, not this machine's.
The pod IP (`10.42.0.0/24`) is not routable from the Mac either. The forward
carries the Docker API on 2375 and nothing else.

So the split is:

- **Works from this Mac:** anything that only talks to the daemon API —
  `docker build`, `docker pull`, `docker push`, `docker save/load`,
  `docker run` without needing to reach a published port, `docker image
  inspect`.
- **Does not work from this Mac:** anything that connects *into* a container
  over a mapped port. That includes **testcontainers**, which is how both K8s
  suites reach their K3s API server.

## Consequences for the test tiers

`topgun/k8s/integration/` and `topgun/k8s_behavioral/` both use
**testcontainers-go's K3s module** (`rancher/k3s:v1.31.6-k3s1`). They do *not*
use KinD, despite what older docs and the `Makefile` prerequisite checks said —
no `kind` binary is invoked anywhere in `topgun/`. They shell out to `docker`,
`helm`, `kubectl`, `go`, and `git`.

Because the suite reaches the K3s API server through a testcontainers-mapped
port, **`make test-k8s-integration` and `make test-k8s-behavioral` cannot be
driven from this Mac against the theborg dind pod as-is.** CI (the `k8s-e2e`
pipeline on concourse.home) remains authoritative for those two tiers.

Two ways to close that gap, neither yet proven here:

1. **Make pod IPs routable, then override the testcontainers host.**
   testcontainers-go v0.41.0 honors `TESTCONTAINERS_HOST_OVERRIDE`
   (`docker.go:1526`). With `10.42.0.0/24` routed via theborg — `sshuttle -r
   theborg 10.42.0.0/24`, **sshuttle is not currently installed on this Mac** —
   and `TESTCONTAINERS_HOST_OVERRIDE=<dind pod IP>`, mapped ports would resolve.
2. **Co-locate the test process with the daemon.** Add a second container to
   the dind pod; containers in a pod share a netns, so the test binary reaches
   mapped ports on `localhost` exactly as CI does. This needs a Go toolchain
   and the repo inside the pod (theborg itself has neither — `go` is not
   installed there).

### Tier-by-tier

| Tier | Needs Docker? | Where it runs today |
|---|---|---|
| `make test-unit` / `test-quick` | No | This Mac (needs local PostgreSQL) |
| `make test-dev-mcp` | No | This Mac |
| `make test-fly-integration` | No | This Mac |
| `make test-integration` | No | This Mac (needs local PostgreSQL) |
| Image builds (`Dockerfile.build`, `Dockerfile.local`) | **Yes** | theborg dind — native amd64 |
| `make test-k8s-integration` | **Yes** | CI only (mapped-port limit above) |
| `make test-k8s-behavioral` | **Yes** | CI only (mapped-port limit above) |
| `atc/worker/jetbridge/live_*_test.go` (`-tags live`) | No | Directly against theborg's k3s in a throwaway namespace — these use kubectl/client-go, not Docker |

The `live` tests are the cheap way to get real-cluster coverage from this Mac.
See `CLAUDE.md` for the throwaway-namespace rules (never target `cicd` or
`concourse`).

## Build-context cost — the thing that will actually bite you

`Dockerfile.build` does `COPY . .`, so with a remote daemon the entire build
context crosses the port-forward before the build starts. This was measured,
not estimated:

| | Context reaching the daemon | Result |
|---|---|---|
| Before the `.dockerignore` fix | >6 GB | **aborted after 9 minutes still transferring** |
| After | ~745 MB | transfer + `COPY . /src` in seconds |

The culprit was **5.4 GB of compiled Go/Ginkgo test binaries across 162
`*.test` files**, plus 831 MB of agent worktree scratch under `.claude/`.

One subtlety worth remembering when editing `.dockerignore`: a bare `*.test`
matches the **repo root only**. Nested paths need `**/*.test`. The first version
of this fix used the bare form and saved nothing.

Currently excluded as local scratch that can never be a build input:
`.worktrees/`, `.local-cluster/`, `.claude/worktrees/`, `**/*.test`. None of
them are tracked in git, so CI is unaffected.

The ~745 MB that remains is mostly built binaries at the repo root
(`concourse-linux-*`, `fly-assets/`) — leave those alone, `Dockerfile.local`
`COPY`s them.

**If a build seems to hang before step 1, it is sending context, not stuck.**

## Housekeeping

The dind pod is limited to 6 CPU / 12 GiB and a 60 GiB `emptyDir` by default,
but it still runs on the machine that hosts the live Concourse. Run
`./hack/borg-docker.sh down` when finished — theborg has previously hit
`DiskPressure` from unbounded registry growth (see the disk/DNS outage notes),
and a forgotten image cache is the same class of problem.

```bash
./hack/borg-docker.sh status    # pod, forward, and daemon state
./hack/borg-docker.sh down      # delete namespace, stop forward
```
