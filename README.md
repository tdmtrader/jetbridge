# Concourse CI — JetBridge Edition

Kubernetes-native fork of [Concourse CI](https://github.com/concourse/concourse) replacing Garden/containerd with direct K8s pod execution, plus an AI-powered CI agent system.

**What's different from upstream Concourse:**

- **JetBridge Runtime** — every pipeline step runs as a Kubernetes pod; no Garden, no containerd, no TSA, no BaggageClaim
- **CI Agent System** — five standalone AI agent binaries for automated code review, planning, QA, fixing, and implementation
- **Agent Feedback API** — HTTP endpoints for collecting and summarizing human verdicts on agent findings
- **Task Sidecars** — service containers (databases, caches, etc.) that run alongside task steps in a shared pod network

Pipeline YAML, `fly` CLI, web UI, resource types, auth, and the REST API are all unchanged from upstream.

---

## Architecture

```
fly CLI → ATC (web) → Kubernetes API → Pods (one per step)
```

| Component | Location |
|-----------|----------|
| JetBridge Runtime | [`atc/worker/jetbridge/`](atc/worker/jetbridge/) |
| CI Agent System | [`ci-agent/`](ci-agent/) |
| Agent Feedback API | [`atc/api/agentfeedback/`](atc/api/agentfeedback/) |
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

# Install with Helm
helm install concourse ./deploy/chart \
  --namespace concourse --create-namespace \
  --set image.repository=ghcr.io/your-org/concourse \
  --set image.tag=latest \
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

### Artifact passing via PVC

A shared PVC replaces SPDY streaming for artifact transfer between steps. An artifact-helper sidecar tars outputs to the PVC; init containers in downstream pods extract them.

Key files: [`volume_artifactstore.go`](atc/worker/jetbridge/volume_artifactstore.go), [`config.go`](atc/worker/jetbridge/config.go)

### Resource caching

Shared PVC mounted at `/concourse/cache` with subPath mounts per cache entry. Configured via `--kubernetes-cache-pvc` and `--kubernetes-artifact-store-claim`.

Key file: [`config.go`](atc/worker/jetbridge/config.go)

### Transient error handling

Automatic retry with classification of Kubernetes-specific transient errors (image pull backoff, pod eviction, API server timeouts).

Key file: [`errors.go`](atc/worker/jetbridge/errors.go)

### Garbage collection

A reaper runs every 30 seconds to reconcile pods with the database, delete completed/orphaned pods, and clean up cache and artifact PVC contents.

Key file: [`reaper.go`](atc/worker/jetbridge/reaper.go)

### Key source files

| File | Purpose |
|------|---------|
| `container.go` | Pod creation, lifecycle management, sidecar injection |
| `executor.go` | Command execution via K8s exec API (SPDY) |
| `podname.go` | Deterministic pod name generation |
| `registrar.go` | Synthetic worker registration (direct DB) |
| `volume_artifactstore.go` | PVC-based artifact store volumes |
| `config.go` | K8s flags, PVC config, image mappings |
| `errors.go` | Transient error classification and retry |
| `reaper.go` | Pod and volume garbage collection |
| `process.go` | Process abstraction over K8s exec |
| `volume.go` | Volume interface implementation |
| `watch.go` | Pod status watching |
| `worker.go` | Worker interface implementation |

### Test coverage

| Test file | Scope |
|-----------|-------|
| `*_test.go` (unit) | `container_test.go`, `executor_test.go`, `podname_test.go`, `registrar_test.go`, `volume_artifactstore_test.go`, `config_test.go`, `errors_test.go`, `reaper_test.go`, `process_test.go`, `volume_test.go`, `watch_test.go`, `worker_test.go` |
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
| `image` | string | yes | Docker image |
| `command` | []string | no | Entrypoint override |
| `args` | []string | no | Arguments to the entrypoint |
| `env` | []EnvVar | no | Environment variables (`name`/`value` pairs) |
| `ports` | []Port | no | Exposed ports (`containerPort`, optional `protocol`) |
| `resources` | object | no | K8s resource requests/limits (`cpu`, `memory`) |
| `workingDir` | string | no | Working directory inside the container |

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

## CI Agent System

Five standalone binaries for AI-powered CI automation. The agent system is an independent Go module (`ci-agent/go.mod`) with zero imports from the main Concourse codebase.

### Agents

| Binary | Entry point | Purpose | Key env vars |
|--------|------------|---------|-------------|
| `ci-agent-review` | [`ci-agent/cmd/ci-agent-review/main.go`](ci-agent/cmd/ci-agent-review/main.go) | Automated code review | `ANTHROPIC_API_KEY`, `GITHUB_TOKEN` |
| `ci-agent-fix` | [`ci-agent/cmd/ci-agent-fix/main.go`](ci-agent/cmd/ci-agent-fix/main.go) | Auto-fix review findings | `ANTHROPIC_API_KEY`, `GITHUB_TOKEN` |
| `ci-agent-plan` | [`ci-agent/cmd/ci-agent-plan/main.go`](ci-agent/cmd/ci-agent-plan/main.go) | Implementation planning | `ANTHROPIC_API_KEY` |
| `ci-agent-qa` | [`ci-agent/cmd/ci-agent-qa/main.go`](ci-agent/cmd/ci-agent-qa/main.go) | QA validation of fixes | `ANTHROPIC_API_KEY` |
| `ci-agent-implement` | [`ci-agent/cmd/ci-agent-implement/main.go`](ci-agent/cmd/ci-agent-implement/main.go) | Code implementation from plans | `ANTHROPIC_API_KEY`, `GITHUB_TOKEN` |

### Output schemas

Structured JSON output is defined in [`ci-agent/schema/`](ci-agent/schema/):

| File | Defines |
|------|---------|
| `results.go` | Common result envelope |
| `review.go` | Review findings, categories, severities |
| `qa.go` | QA validation results |
| `fix_report.go` | Fix attempt outcomes |
| `event.go` | Streaming event types |
| `feedback.go` | Feedback records and verdicts |

### Building

```bash
# Build all agent binaries
cd ci-agent && go build ./cmd/...

# Build the agent Docker image
docker build -f deploy/Dockerfile.ci-agent -t ci-agent:latest .
```

Docker image: [`deploy/Dockerfile.ci-agent`](deploy/Dockerfile.ci-agent)

### Integration tests

Tests in [`ci-agent/integration/`](ci-agent/integration/):

| File | Covers |
|------|--------|
| `review_fix_test.go` | Review → fix pipeline |
| `review_fix_qa_test.go` | Review → fix → QA pipeline |
| `qa_test.go` | Standalone QA validation |
| `plan_implement_test.go` | Plan → implement pipeline |
| `integration_suite_test.go` | Test suite setup |

---

## Agent Feedback API

HTTP endpoints for collecting human feedback on agent findings, built into the ATC web server.

### Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/agent/feedback` | Submit a feedback record |
| `GET` | `/api/v1/agent/feedback` | Get feedback by `?repo=…&commit=…` |
| `GET` | `/api/v1/agent/feedback/summary` | Aggregated stats by `?repo=…` |
| `POST` | `/api/v1/agent/feedback/classify` | Classify natural-language text into a verdict |

### Verdicts

`accurate`, `false_positive`, `noisy`, `overly_strict`, `partially_correct`, `missed_context`

### Example

```bash
curl -X POST https://concourse.example.com/api/v1/agent/feedback \
  -H "Content-Type: application/json" \
  -d '{
    "review_ref": {"repo": "org/repo", "commit": "abc123"},
    "finding_id": "finding-1",
    "finding_type": "bug",
    "finding_snapshot": {"message": "null pointer dereference"},
    "verdict": "accurate",
    "confidence": 0.9,
    "reviewer": "alice"
  }'
```

### References

- Route definitions: [`atc/routes.go`](atc/routes.go)
- Handler implementation: [`atc/api/agentfeedback/handler.go`](atc/api/agentfeedback/handler.go)
- Tests: `handler_test.go`, `handler_integration_test.go`, `route_registration_test.go`, `feedback_pipeline_test.go`

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
- Pipeline YAML syntax
- Resource types (git, time, registry-image, s3, etc.)
- Web UI
- PostgreSQL schema and migrations
- Auth (OIDC, OAuth, local users)
- REST API

### Added

- JetBridge Kubernetes runtime (pod-per-step)
- Task step sidecars
- CI agent system (review, fix, plan, QA, implement)
- Agent feedback API
- Deterministic pod naming
- Transient error retry with K8s error classification
- PVC-based artifact passing and resource caching

### Known limitations

- **No TTY** — `SetTTY` is a no-op for Kubernetes pods
- **Single namespace per worker** — worker name is deterministic (`k8s-<namespace>`)
- **`fly execute -i` with artifact store** — when `ArtifactStoreClaim` is configured, `fly execute --input` needs additional work for the upload path

---

## Build & Test

### Build

```bash
# Concourse binary
go build -o concourse ./cmd/concourse

# Agent binaries
cd ci-agent && go build ./cmd/...

# Docker images
docker build -f Dockerfile.build -t concourse:latest .
docker build -f deploy/Dockerfile.ci-agent -t ci-agent:latest .
```

### Test

```bash
# JetBridge unit tests
go test ./atc/worker/jetbridge/...

# JetBridge live tests (requires K8s cluster)
go test ./atc/worker/jetbridge/... -run Live -tags live

# Agent unit + integration tests
cd ci-agent && go test ./...

# Feedback API tests
go test ./atc/api/agentfeedback/...
```

### Key test files

| Area | Files |
|------|-------|
| JetBridge unit | `atc/worker/jetbridge/*_test.go` |
| JetBridge live | `atc/worker/jetbridge/live_*.go` |
| Agent integration | `ci-agent/integration/*_test.go` |
| Feedback API | `atc/api/agentfeedback/*_test.go` |
| Sidecar types | `atc/sidecar_test.go` |

---

## Deployment & Operations

See [`JETBRIDGE.md`](JETBRIDGE.md) for the full deployment guide, including:

- Production checklist (external DB, auth, secrets, multi-node PVC)
- RBAC requirements
- Troubleshooting (pod startup, image pulls, artifact passing, GC)
- Monitoring and Prometheus metrics
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

Note: this repository has two Go modules — the main module at the repo root, and the CI agent module at `ci-agent/`. They are intentionally independent; the agent binaries have zero imports from the main Concourse codebase.

---

## License

Apache 2.0 — see [LICENSE.md](LICENSE.md).
