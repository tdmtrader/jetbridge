# Concourse CI — JetBridge Edition

Kubernetes-native fork of [Concourse CI](https://github.com/concourse/concourse) replacing Garden/containerd with direct K8s pod execution, plus an AI-powered CI agent system.

**What's different from upstream Concourse:**

- **JetBridge Runtime** — every pipeline step runs as a Kubernetes pod; no Garden, no containerd, no TSA, no BaggageClaim
- **Jetbridge Agentic Workflows** — versioned workflow functions over immutable, typed snapshots for AI-assisted pipeline automation; see [`docs/agentic/README.md`](docs/agentic/README.md)
- **Task Sidecars** — service containers (databases, caches, etc.) that run alongside task steps in a shared pod network
- **`skip_download`** — get steps can resolve version metadata without downloading artifacts
- **Configurable base resource types** — override default resource type images via `--kubernetes-base-resource-type name=image`
- **Artifact daemon** — per-node DaemonSet that stores, mirrors, and resolves step artifacts over node-local storage
- **Health endpoint** — `GET /api/v1/health` with DB + worker checks (used for K8s readiness probes)
- **OpenTelemetry** — distributed tracing (OTLP, Jaeger, Honeycomb, Stackdriver) and metrics export

Pipeline YAML, `fly` CLI, web UI, resource types, auth, and the REST API are all unchanged from upstream.

---

## Architecture

```
fly CLI → ATC (web) → Kubernetes API → Pods (one per step)
```

| Component | Location |
|-----------|----------|
| JetBridge Runtime | [`atc/worker/jetbridge/`](atc/worker/jetbridge/) |
| Jetbridge Agentic Workflows | [`docs/agentic/README.md`](docs/agentic/README.md) |
| Task Sidecars | [`atc/sidecar.go`](atc/sidecar.go) |
| Helm Chart | [`deploy/chart/`](deploy/chart/) |

**Removed from upstream:** Garden runtime, containerd integration, BaggageClaim volume manager, TSA (SSH worker registration), deprecated CLI flags.

---

## Quick Start

### Prerequisites

- Kubernetes cluster (GKE, EKS, AKS, k3s, kind)
- Helm 3
- `kubectl` configured with cluster access

### Build & Deploy

```bash
# Build the Concourse image
./build.sh ghcr.io/your-org/concourse:latest

# Resolve a project-owned helper image containing sh/wget/base64/coreutils to
# an immutable digest. The chart rejects mutable helper tags.
export ARTIFACT_HELPER_IMAGE='ghcr.io/your-org/jetbridge-artifact-helper@sha256:<64-lowercase-hex>'

# Install with Helm
helm install concourse ./deploy/chart \
  --namespace concourse --create-namespace \
  --set image.repository=ghcr.io/your-org/concourse \
  --set image.tag=latest \
  --set-string kubernetes.artifactHelperImage="${ARTIFACT_HELPER_IMAGE}" \
  --set web.externalUrl=https://concourse.example.com

# Log in with fly
fly -t ci login -c https://concourse.example.com

# Set a pipeline (standard Concourse YAML — no changes)
fly -t ci set-pipeline -p my-pipeline -c pipeline.yml
```

See [`deploy/chart/values.yaml`](deploy/chart/values.yaml) for all Helm parameters and [`JETBRIDGE.md`](JETBRIDGE.md) for the full deployment guide.

---

## JetBridge Runtime

JetBridge replaces Concourse's worker architecture with direct Kubernetes pod execution. The web node talks to the K8s API server — every task, get, put, and check step becomes a pod.

See [`JETBRIDGE.md`](JETBRIDGE.md) for the complete runtime reference, configuration flags, deployment guide, troubleshooting, and monitoring.

### Pod-per-step execution

Each build step creates one Kubernetes pod with a deterministic, human-readable name derived from pipeline metadata (`<pipeline>-<job>-b<build>-<type>-<8hex>`).

Key files: [`container.go`](atc/worker/jetbridge/container.go), [`executor.go`](atc/worker/jetbridge/executor.go), [`podname.go`](atc/worker/jetbridge/podname.go)

### Worker registration

The web node registers itself as a synthetic worker (`k8s-<namespace>`) by writing directly to the database — no TSA, no SSH tunnels.

Key file: [`registrar.go`](atc/worker/jetbridge/registrar.go)

### Artifact passing via the artifact daemon

A per-node DaemonSet replaces SPDY streaming for artifact transfer between steps. Step outputs stay on the producing node's host path; the daemon registers them, mirrors them to peers, and resolves each downstream input — fetching a remote copy when the artifact was produced on another node.

Key files: [`volume_daemonset.go`](atc/worker/jetbridge/volume_daemonset.go), [`daemon_client.go`](atc/worker/jetbridge/daemon_client.go), [`config.go`](atc/worker/jetbridge/config.go)

### Resource caching

Node-local cache directories under the daemon's host path, with subPath mounts per cache entry. Configured via `--kubernetes-cache-store` and `--kubernetes-cache-host-path`.

Key file: [`config.go`](atc/worker/jetbridge/config.go)

### Transient error handling

Automatic retry with classification of Kubernetes-specific transient errors (image pull backoff, pod eviction, API server timeouts).

Key file: [`errors.go`](atc/worker/jetbridge/errors.go)

### Garbage collection

A reaper runs every 30 seconds to reconcile pods with the database, delete completed/orphaned pods, and ask the artifact daemon to release the node-local artifacts they produced.

Key file: [`reaper.go`](atc/worker/jetbridge/reaper.go)

### Key source files

| File | Purpose |
|------|---------|
| `container.go` | Pod creation, lifecycle management, sidecar injection |
| `executor.go` | Command execution via K8s exec API (SPDY) |
| `podname.go` | Deterministic pod name generation |
| `registrar.go` | Synthetic worker registration (direct DB) |
| `volume_daemonset.go` | Artifact-daemon backed volumes |
| `daemon_client.go` | HTTP client for the per-node artifact daemon |
| `config.go` | K8s flags, cache/artifact config, image mappings |
| `errors.go` | Transient error classification and retry |
| `reaper.go` | Pod and volume garbage collection |
| `process.go` | Process abstraction over K8s exec |
| `volume.go` | Volume interface implementation |
| `watch.go` | Pod status watching |
| `worker.go` | Worker interface implementation |

### Test coverage

| Test file | Scope |
|-----------|-------|
| `*_test.go` (unit) | `container_test.go`, `executor_test.go`, `podname_test.go`, `registrar_test.go`, `storage_daemonset_test.go`, `volume_daemonset_test.go`, `daemon_client_test.go`, `config_test.go`, `errors_test.go`, `reaper_test.go`, `process_test.go`, `volume_test.go`, `watch_test.go`, `worker_test.go` |
| `live_e2e_test.go` | End-to-end against a real K8s cluster |
| `live_sidecar_test.go` | Sidecar injection in a real cluster |
| `live_streaming_test.go` | Log streaming against a real cluster |
| `podname_integration_test.go` | Pod name generation integration |
| `artifact_integration_test.go` | Artifact store integration |

---

## Task Step Sidecars

Sidecars are service containers (databases, caches, mock servers) that run alongside a task in a shared pod network. They start before the main task container and share `localhost`.

### Configuration fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Container name (cannot be `main` or `artifact-helper`) |
| `image` | string | yes* | Docker image. Mutually exclusive with `image_artifact`. |
| `image_artifact` | string | yes* | Build artifact name from prior step. Mutually exclusive with `image`. |
| `command` | []string | no | Entrypoint override |
| `args` | []string | no | Arguments to the entrypoint |
| `env` | []EnvVar | no | Environment variables (`name`/`value` pairs) |
| `ports` | []Port | no | Exposed ports (`containerPort`, optional `protocol`: TCP/UDP/SCTP) |
| `resources` | object | no | K8s resource requests/limits (`cpu`, `memory`) |
| `workingDir` | string | no | Working directory inside the container |

Sidecars can also be specified as a file reference (string path to a YAML list in a build artifact).

### Example: Postgres sidecar

```yaml
task: integration-tests
config:
  platform: linux
  image_resource:
    type: registry-image
    source: { repository: my-app }
  run:
    path: ./run-tests.sh
  sidecars:
    - name: postgres
      image: postgres:16
      env:
        - name: POSTGRES_PASSWORD
          value: test
        - name: POSTGRES_DB
          value: myapp_test
      ports:
        - containerPort: 5432
      resources:
        requests:
          cpu: "100m"
          memory: "256Mi"
```

### References

- Sidecar types and parsing: [`atc/sidecar.go`](atc/sidecar.go)
- Pod injection: [`atc/worker/jetbridge/container.go`](atc/worker/jetbridge/container.go) (`buildSidecarContainers()`)
- Live test: [`atc/worker/jetbridge/live_sidecar_test.go`](atc/worker/jetbridge/live_sidecar_test.go)

---

## Jetbridge Agentic Workflows

AI-assisted pipeline automation — versioned workflow functions running as
ordinary visible Concourse DAGs over immutable, typed snapshots. This
replaced an earlier standalone `ci-agent` module and ticket-centric review
pipeline; both are gone except a small retained `ci-agent/devmcp` server
(see `ci-agent/RETAINED.md`) kept as the in-repo build/test MCP
implementation.

See [`docs/agentic/README.md`](docs/agentic/README.md) for the authoritative
description: runtime prerequisites, snapshot storage, workflow-def format,
and the agent API surface.

---

## Deviations from Upstream Concourse

### Removed

- Garden container runtime
- containerd integration
- BaggageClaim volume manager
- TSA (SSH-based worker registration)
- Deprecated CLI flags and stale references

### Unchanged

- `fly` CLI (all commands work identically)
- Pipeline YAML core syntax
- Resource types (git, time, registry-image, s3, etc.)
- Web UI
- PostgreSQL schema and migrations
- Auth (OIDC, OAuth, local users)
- REST API

### Added

- JetBridge Kubernetes runtime (pod-per-step)
- Task step sidecars (inline, file reference, or `image_artifact`)
- `skip_download` on get steps (resolve version without downloading)
- Configurable base resource types (`--kubernetes-base-resource-type name=image`)
- Direct image references for resource types (`image_ref` field)
- Health endpoint (`GET /api/v1/health`) for K8s probes
- OpenTelemetry tracing (OTLP, Jaeger, Honeycomb, Stackdriver) and metrics
- CI agent system (review, fix, plan, QA, implement)
- Agent feedback API
- Deterministic pod naming
- Transient error retry with K8s error classification
- Artifact-daemon based artifact passing and node-local resource caching

### Known limitations

- **No TTY** — `SetTTY` is a no-op for Kubernetes pods
- **Single namespace per worker** — worker name is deterministic (`k8s-<namespace>`)

---

## Build & Test

### Build

```bash
# Concourse binary
go build -o concourse ./cmd/concourse

# Docker image -- needs a Docker daemon, which on this fork's dev machine lives
# on theborg, not locally. See docs/docker-on-theborg.md
eval "$(./hack/borg-docker.sh env)"
docker build -f Dockerfile.build -t concourse:latest .
```

### Test

```bash
# JetBridge unit tests
go test ./atc/worker/jetbridge/...

# JetBridge live tests (requires K8s cluster)
go test ./atc/worker/jetbridge/... -run Live -tags live
```

### Key test files

| Area | Files |
|------|-------|
| JetBridge unit | `atc/worker/jetbridge/*_test.go` |
| JetBridge live | `atc/worker/jetbridge/live_*.go` |
| Sidecar types | `atc/sidecar_test.go` |

---

## Deployment & Operations

See [`JETBRIDGE.md`](JETBRIDGE.md) for the full deployment guide, including:

- Complete configuration reference (all `--kubernetes-*`, `--tracing-*`, `--otel-metrics-*`, `--gc-*` flags)
- New pipeline features (`skip_download`, sidecars, configurable base resource types)
- Health endpoint (`GET /api/v1/health`) response schema
- Production checklist (external DB, auth, secrets, multi-node artifact mirroring, connection pool sizing)
- RBAC requirements
- Troubleshooting (pod startup, image pulls, artifact passing, GC)
- Monitoring: Prometheus metrics, OpenTelemetry tracing/metrics, ServiceMonitor
- Useful `kubectl` commands

### RBAC summary

The web pod needs these permissions in its namespace:

```yaml
apiGroups: [""]
resources: ["pods", "pods/exec", "pods/log"]
verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
```

The Helm chart creates these automatically when `rbac.create=true`.

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for the development process, testing strategy, and code style guidelines.

---

## License

Apache 2.0 — see [LICENSE.md](LICENSE.md).
