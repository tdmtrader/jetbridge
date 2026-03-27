# Pipeline Migration Guide: Concourse → JetBridge Edition

This guide helps you migrate existing Concourse pipelines to take advantage of
JetBridge Edition features. Your existing pipelines are **fully backward
compatible** and will run unchanged on JetBridge — this guide covers optional
enhancements organized by complexity.

## How to Use This Guide

The guide is organized into four tiers:

| Tier | Effort | What You Do |
|------|--------|-------------|
| **Tier 1: Zero-Change Benefits** | Nothing | Deploy JetBridge; benefits are automatic |
| **Tier 2: Simple Additions** | Add a few YAML fields | Ephemeral storage, scratch paths, cache backend |
| **Tier 3: Restructuring** | Refactor step definitions | Sidecars, native image resolution, direct image refs |
| **Tier 4: Architecture Rethink** | Redesign pipeline patterns | Replace Garden-era patterns with K8s-native equivalents |

Start at Tier 1 and work down. Each tier builds on the previous one.

---

## Tier 1: Zero-Change Benefits

These improvements apply automatically when you deploy JetBridge. No pipeline
changes are needed.

### Kubernetes-Native Pod Execution

Every pipeline step (task, get, put, check) runs as a Kubernetes pod instead of
a Garden container. You get:

- **Native scheduling**: Kubernetes handles pod placement, bin-packing, and
  resource allocation across your cluster.
- **Readable pod names**: Pods are named `<pipeline>-<job>-b<build>-<type>-<8hex>`,
  making `kubectl` debugging straightforward.
- **K8s labels**: Every pod is labelled with `concourse.ci/pipeline`,
  `concourse.ci/job`, `concourse.ci/build`, and `concourse.ci/step` for easy
  filtering.

```bash
# Filter pods by pipeline
kubectl -n concourse get pods -l concourse.ci/pipeline=my-pipeline

# Filter by job
kubectl -n concourse get pods -l concourse.ci/job=unit-tests
```

### OpenTelemetry Tracing

Distributed tracing is built into the runtime. Every build step produces
OpenTelemetry spans automatically — no pipeline YAML changes needed.

Configure the backend at the operator level:

```yaml
# Helm values
tracing:
  otlpAddress: "tempo.monitoring.svc:4317"
  serviceName: "concourse-web"
```

Traces cover the full build lifecycle: scheduling, pod creation, image pull,
command execution, artifact upload/download, and garbage collection.

### Notification-Driven Scheduling

JetBridge uses PostgreSQL `NOTIFY`/`LISTEN` for event-driven scheduling instead
of polling loops. Builds start faster after resource version changes.

### No Worker Management

There are no worker VMs to provision, register, or maintain. The web node
talks directly to the Kubernetes API server. The synthetic worker
(`k8s-<namespace>`) registers itself automatically.

### Prometheus Metrics

Two K8s-specific metrics are emitted automatically:

| Metric | Type | Description |
|--------|------|-------------|
| `concourse_k8s_pod_startup_duration_ms` | Gauge | Time from pod creation to Running |
| `concourse_k8s_image_pull_failures_total` | Counter | ImagePullBackOff/ErrImagePull count |

Standard Concourse metrics (`concourse_containers_created_total`, etc.)
continue to work unchanged.

---

## Tier 2: Simple Additions

These features require adding a few YAML fields to existing task configs.

### Ephemeral Storage Quotas

Set Kubernetes ephemeral storage limits and requests on task containers.
This controls how much local disk a container can use before being evicted.

**When to use:**
- Tasks that write large temp files (builds, test artifacts, log processing)
- You want Kubernetes to evict pods that exceed disk usage instead of filling nodes
- You want Burstable QoS (set requests lower than limits)

**Before (standard Concourse):**
```yaml
- task: build
  config:
    platform: linux
    image_resource:
      type: registry-image
      source: { repository: golang }
    container_limits:
      cpu: 2048
      memory: 4GB
    run:
      path: go
      args: ["build", "./..."]
```

**After (JetBridge — add ephemeral storage + requests):**
```yaml
- task: build
  config:
    platform: linux
    image_resource:
      type: registry-image
      source: { repository: golang }
    container_limits:
      cpu: 2048
      memory: 4GB
      ephemeral_storage: 10GB
    container_requests:
      cpu: 512
      memory: 1GB
      ephemeral_storage: 2GB
    run:
      path: go
      args: ["build", "./..."]
```

**Key details:**
- `container_requests` is a new top-level field (same schema as `container_limits`)
- Setting requests < limits gives Burstable QoS in Kubernetes
- `ephemeral_storage` accepts human-readable units: `KB`, `MB`, `GB` (binary: 1GB = 1073741824 bytes)
- Setting only `container_limits` without `container_requests` gives Guaranteed QoS

### Scratch Paths

Ephemeral emptyDir volumes mounted into the task container. Unlike caches,
scratch paths are **never preserved** between builds — they exist only for
the lifetime of the task pod.

**When to use:**
- Tasks that need writable temp directories separate from inputs/outputs
- Replacing hacks where you used `caches:` for temp storage that shouldn't persist
- Build steps that need fast local scratch space without polluting the workspace

**Before (abusing caches for temp storage):**
```yaml
- task: compile
  config:
    platform: linux
    image_resource:
      type: registry-image
      source: { repository: golang }
    caches:
      - path: tmp-build    # Hack: using cache for temp storage
    run:
      path: sh
      args:
        - -c
        - |
          export TMPDIR=tmp-build
          go build -o output/binary ./cmd/...
```

**After (JetBridge — use scratch_paths):**
```yaml
- task: compile
  config:
    platform: linux
    image_resource:
      type: registry-image
      source: { repository: golang }
    scratch_paths:
      - path: tmp-build
    run:
      path: sh
      args:
        - -c
        - |
          export TMPDIR=tmp-build
          go build -o output/binary ./cmd/...
```

**Key details:**
- Scratch paths are backed by Kubernetes emptyDir volumes
- They are never cached — guaranteed fresh on every build
- Use `caches:` when you want data to persist between builds; use `scratch_paths:` when you don't
- Multiple scratch paths can be specified

### Cache Backend Selection

JetBridge supports multiple backends for task caches (`caches:` in pipeline YAML).
This is an **operator-level** configuration, not a pipeline YAML change, but
understanding the backends helps you design pipeline caching strategies.

| Backend | Flag Value | Persistence | Performance | Best For |
|---------|-----------|-------------|-------------|----------|
| **emptydir** | `--kubernetes-cache-store=emptydir` | Lost on pod termination | Fast (RAM/local disk) | Stateless CI, no cache needed |
| **pvc** | `--kubernetes-cache-store=pvc` | Survives pod restarts | Moderate (network PVC) | Shared caches across builds |
| **hostpath** | `--kubernetes-cache-store=hostpath` | Node-local, survives restarts | Fast (local disk) | Single-node clusters, node-affinity builds |
| **artifact** | `--kubernetes-cache-store=artifact` | Tar on artifact PVC | Slower (tar/untar) | Multi-node, no hostPath available |

**When to use each:**
- **emptydir**: Development clusters, pipelines where caching isn't important
- **pvc**: Production default — persistent and shared across all pods
- **hostpath**: When builds are pinned to a node (via affinity) and need fast local I/O
- **artifact**: When you need persistence on multi-node clusters without ReadWriteMany PVCs

**Pipeline impact**: Your `caches:` declarations work unchanged regardless of backend.
The backend only affects where the cached data is stored between builds.

```yaml
# This works the same with any cache backend:
- task: build
  config:
    platform: linux
    image_resource:
      type: registry-image
      source: { repository: node }
    caches:
      - path: node_modules
    run:
      path: npm
      args: ["install"]
```

---

## Tier 3: Restructuring

These features may require restructuring step definitions or pipeline topology.

### Inline Sidecars

Run service containers (databases, caches, mock APIs) alongside your task in
a shared pod. Sidecars share `localhost` networking with the main task container.

**When to use:**
- Integration tests that need a database, cache, or service
- Replacing docker-compose-based test setups
- Replacing docker-in-docker for building images (use a BuildKit sidecar instead)
- Any task that needs a helper service running alongside it

**Before (docker-in-docker for integration tests):**
```yaml
- task: integration-tests
  privileged: true
  config:
    platform: linux
    image_resource:
      type: registry-image
      source: { repository: my-app-test }
    run:
      path: sh
      args:
        - -c
        - |
          # Start services manually
          dockerd &
          sleep 5
          docker run -d --name pg -e POSTGRES_PASSWORD=test -p 5432:5432 postgres:16
          docker run -d --name redis -p 6379:6379 redis:7
          sleep 10
          ./run-integration-tests.sh
```

**After (JetBridge — inline sidecars):**
```yaml
- task: integration-tests
  config:
    platform: linux
    image_resource:
      type: registry-image
      source: { repository: my-app-test }
    run:
      path: ./run-integration-tests.sh
  sidecars:
    - name: postgres
      image: postgres:16
      env:
        - name: POSTGRES_PASSWORD
          value: test
      ports:
        - containerPort: 5432
      resources:
        requests:
          cpu: "100m"
          memory: "256Mi"
        limits:
          cpu: "500m"
          memory: "512Mi"
    - name: redis
      image: redis:7
      ports:
        - containerPort: 6379
```

**File-referenced sidecars** (load sidecar config from a build artifact):
```yaml
- task: my-task
  config:
    platform: linux
    image_resource:
      type: registry-image
      source: { repository: my-app }
    run:
      path: ./test.sh
  sidecars:
    - "my-repo/ci/sidecars/postgres.yml"
```

Where `my-repo/ci/sidecars/postgres.yml` contains:
```yaml
- name: postgres
  image: postgres:16
  env:
    - name: POSTGRES_PASSWORD
      value: test
  ports:
    - containerPort: 5432
```

**Image artifact sidecars** (use an image from a prior step):
```yaml
- get: custom-sidecar-image
  type: registry-image
  source: { repository: my-registry/custom-svc }

- task: test-with-custom-svc
  config:
    platform: linux
    image_resource:
      type: registry-image
      source: { repository: my-app }
    run:
      path: ./test.sh
  sidecars:
    - name: custom-svc
      image_artifact: custom-sidecar-image
```

**Sidecar field reference:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Container name. Cannot be `main` or `artifact-helper`. |
| `image` | string | yes* | Docker image reference. |
| `image_artifact` | string | yes* | Artifact name from a prior step. Mutually exclusive with `image`. |
| `command` | []string | no | Entrypoint override. |
| `args` | []string | no | Arguments to the entrypoint. |
| `env` | []object | no | Environment variables (`name`/`value` pairs). |
| `ports` | []object | no | Exposed ports (`containerPort`, optional `protocol`: TCP/UDP/SCTP). |
| `resources` | object | no | K8s resource requests/limits (`cpu`, `memory`). |
| `workingDir` | string | no | Working directory inside the container. |

### Native Registry-Image Resolution

On JetBridge, `registry-image` resources can be resolved without spinning up
a separate check/get pod. The runtime resolves the image reference directly
via the Kubernetes container runtime.

**When to use:**
- Pipelines with many `registry-image` resources (reduces pod count)
- When you want faster image resolution

**How it works:**
When a task specifies `image_resource` with `type: registry-image`, JetBridge
can short-circuit the check→get chain by resolving the image tag to a digest
directly. The image is pulled as part of the task pod itself rather than
requiring a separate get pod.

**Pipeline impact**: This is largely automatic and transparent. To maximize
the benefit:

```yaml
# Prefer inline image_resource (resolved at pod creation time):
- task: build
  config:
    platform: linux
    image_resource:
      type: registry-image
      source:
        repository: golang
        tag: "1.25"
    run:
      path: go
      args: ["build", "./..."]
```

### skip_download on Get Steps

Resolve a resource version without downloading artifacts. The version metadata
is registered for downstream steps, but no data is fetched and no container
is created.

**When to use:**
- Triggering on new versions without fetching large repos/artifacts
- Image resources used as task images (the image is pulled at task pod creation,
  not by a separate get pod)
- Reducing pod count when you only need version metadata

**Before:**
```yaml
- get: big-repo
  trigger: true
- task: notify
  config:
    platform: linux
    image_resource:
      type: registry-image
      source: { repository: alpine }
    run:
      path: echo
      args: ["new version detected"]
```

**After (skip_download):**
```yaml
- get: big-repo
  trigger: true
  skip_download: true    # Version registered, no data fetched
- task: notify
  config:
    platform: linux
    image_resource:
      type: registry-image
      source: { repository: alpine }
    run:
      path: echo
      args: ["new version detected"]
```

**Note:** `skip_download` is currently only valid for `registry-image` resources
or custom resource types with an `image:` field.

### Direct Image References for Resource Types

Define custom resource types by pointing directly to a container image instead
of going through check/get resolution.

**When to use:**
- Custom resource types where you control the image and want to pin it directly
- Reducing pod count (no check/get pods for the resource type image)
- Faster pipeline startup (no type resolution chain)

**Before (standard — type resolves via check→get chain):**
```yaml
resource_types:
  - name: slack-notification
    type: docker-image
    source:
      repository: cfcommunity/slack-notification-resource
      tag: latest
```

**After (JetBridge — direct image reference):**
```yaml
resource_types:
  - name: slack-notification
    type: docker-image
    image: cfcommunity/slack-notification-resource:latest
```

When `image` is set on a resource type, no check or get plans are generated
for the resource type itself. The image is pulled directly by the Kubernetes
runtime.

### DaemonSet Artifact Backend

For high-throughput pipelines, JetBridge supports a DaemonSet-based artifact
backend where each node runs a local artifact server. Artifacts are stored
on node-local storage and served via HTTP, eliminating PVC contention.

**When to use:**
- High fan-out pipelines (many parallel jobs producing/consuming artifacts)
- Large artifact sizes where PVC I/O is a bottleneck
- Multi-node clusters where ReadWriteMany PVC performance is inadequate

**Configuration** (operator-level, not pipeline YAML):
```yaml
# Helm values
kubernetes:
  artifactBackend: daemonset
  artifactDaemonPort: 7788
  artifactDaemonHostPath: /var/concourse/artifacts
```

**Pipeline impact**: No pipeline YAML changes needed. The artifact passing
mechanism is transparent — your `inputs:` and `outputs:` work the same way.
The difference is operational: artifacts are stored on the node running the
pod instead of on a shared PVC.

---

## Tier 4: Architecture Rethink

These patterns require rethinking Garden-era pipeline designs that don't
translate directly to Kubernetes.

### Privileged Containers

**Garden pattern:**
```yaml
- task: build-image
  privileged: true
  config:
    platform: linux
    image_resource:
      type: registry-image
      source: { repository: concourse/oci-build-task }
    run:
      path: build
```

**K8s status:** Supported — maps to `securityContext.privileged: true` on the
container. However, many K8s clusters restrict privileged pods via Pod Security
Standards or admission controllers.

**K8s-native replacement — use a BuildKit sidecar:**
```yaml
- task: build-image
  config:
    platform: linux
    image_resource:
      type: registry-image
      source: { repository: my-app }
    outputs:
      - name: image
    run:
      path: sh
      args:
        - -c
        - |
          buildctl --addr tcp://localhost:1234 build \
            --frontend dockerfile.v0 \
            --local context=. \
            --local dockerfile=. \
            --output type=oci,dest=image/image.tar
  sidecars:
    - name: buildkit
      image: moby/buildkit:rootless
      command: ["buildkitd"]
      args: ["--addr", "tcp://0.0.0.0:1234"]
      ports:
        - containerPort: 1234
```

### Docker-in-Docker

**Garden pattern:**
```yaml
- task: test-with-docker
  privileged: true
  config:
    platform: linux
    image_resource:
      type: registry-image
      source: { repository: docker }
    run:
      path: sh
      args:
        - -c
        - |
          dockerd &
          sleep 5
          docker-compose up -d
          ./run-tests.sh
          docker-compose down
```

**K8s-native replacement — use sidecars:**
```yaml
- task: test-with-services
  config:
    platform: linux
    image_resource:
      type: registry-image
      source: { repository: my-test-runner }
    run:
      path: ./run-tests.sh
  sidecars:
    - name: postgres
      image: postgres:16
      env:
        - name: POSTGRES_PASSWORD
          value: test
      ports:
        - containerPort: 5432
    - name: redis
      image: redis:7
      ports:
        - containerPort: 6379
```

Instead of running docker-compose inside a privileged container, declare
each service as a sidecar. They share `localhost` networking, start before
the main task, and are cleaned up automatically.

### Worker Tags and Platform Filtering

**Garden pattern:**
```yaml
- task: gpu-training
  tags: [gpu]
  config:
    platform: linux
    ...
```

**K8s status:** Worker tags and platform filtering are **removed**. JetBridge
has one worker per namespace that supports all resource types.

**K8s-native replacement:** Use Kubernetes node selectors and tolerations
(configured at the operator/Helm level) for node placement, or use separate
namespaces for different workload classes.

### Host Volume Mounts

**Garden pattern:** Garden workers could mount host directories into containers
for shared state or performance.

**K8s-native replacement:** Use `hostPath` volumes via the cache backend
(`--kubernetes-cache-store=hostpath`) for cache-like workloads. For other
host access needs, consider `scratch_paths` (emptyDir) or dedicated PVCs.

### BaggageClaim / btrfs Cache Snapshots

**Garden pattern:** BaggageClaim used btrfs copy-on-write snapshots for
instant volume cloning and cache restoration.

**K8s status:** Removed. No snapshot-based caching.

**K8s-native replacement:** Use the `pvc` or `artifact` cache backends.
PVC caching uses subpath mounts — data survives pod restarts but there's
no instant cloning. For workloads that relied heavily on btrfs snapshot
performance, consider:
- `hostpath` backend on nodes with fast local SSDs
- `scratch_paths` for truly ephemeral scratch data
- Restructuring pipelines to minimize cache dependency

### TSA / External Workers

**Garden pattern:** External workers registered via TSA SSH tunnels, allowing
workers on separate networks or VMs.

**K8s status:** Removed. Only in-cluster workers are supported.

**K8s-native replacement:** All workers are in the same Kubernetes namespace.
For multi-environment pipelines, deploy separate JetBridge instances per
cluster/environment.

### TTY / Interactive Terminal Resize

**Garden pattern:** Garden supported TTY allocation for interactive processes.

**K8s status:** `SetTTY` is a no-op. Terminal resize during `fly intercept`
sessions does not work.

**Workaround:** `fly intercept` still works (exec into the pod), but terminal
resize is not supported. Use a fixed terminal size.

### Hermetic Mode

**Garden pattern:** `hermetic: true` dropped all external network traffic.

**K8s status:** The flag is accepted but enforcement depends on cluster-level
NetworkPolicy configuration. Ensure your cluster has a NetworkPolicy controller
(Calico, Cilium, etc.) for hermetic mode to take effect.

---

## Quick-Reference Migration Cheatsheet

| Feature | YAML | Where | When to Use |
|---------|------|-------|-------------|
| Ephemeral storage limit | `container_limits.ephemeral_storage: 10GB` | Task config | Prevent disk-hungry tasks from filling nodes |
| Ephemeral storage request | `container_requests.ephemeral_storage: 2GB` | Task config | Burstable QoS, guaranteed minimum disk |
| Resource requests | `container_requests: {cpu: 512, memory: 1GB}` | Task config | Burstable QoS (request < limit) |
| Scratch paths | `scratch_paths: [{path: /tmp}]` | Task config | Ephemeral writable dirs, never cached |
| Inline sidecar | `sidecars: [{name: pg, image: postgres:16}]` | Task step | Service containers alongside tasks |
| File sidecar | `sidecars: ["path/to/sidecars.yml"]` | Task step | Reusable sidecar definitions |
| Artifact sidecar image | `sidecars: [{name: x, image_artifact: img}]` | Task step | Use image from prior pipeline step |
| Skip download | `skip_download: true` | Get step | Version metadata only, no data fetch |
| Direct image ref | `image: repo/image:tag` | Resource type | Skip check/get for resource type image |

---

## Agent Migration Prompt Template

Use the following prompt with an AI agent to analyze a pipeline and suggest
migration changes. Provide your pipeline YAML as input.

```
You are a Concourse pipeline migration advisor for JetBridge Edition.

Analyze the following pipeline YAML and suggest tier-by-tier migration
improvements. For each suggestion:
1. Identify the current pattern
2. Explain which JetBridge feature applies
3. Show the exact YAML diff (before/after)
4. Explain the benefit

## JetBridge Features Available

### Tier 2 (simple additions):
- `container_requests` — Set K8s resource requests for Burstable QoS
- `container_limits.ephemeral_storage` / `container_requests.ephemeral_storage` — Ephemeral storage quotas
- `scratch_paths: [{path: <dir>}]` — Ephemeral writable directories (emptyDir, never cached)

### Tier 3 (restructuring):
- `sidecars` on task steps — Inline service containers (databases, caches, etc.) sharing localhost
  - Inline: `sidecars: [{name: pg, image: postgres:16, env: [...], ports: [...]}]`
  - File ref: `sidecars: ["artifact/path/to/sidecars.yml"]`
  - Artifact image: `sidecars: [{name: x, image_artifact: prior-step-image}]`
- `skip_download: true` on get steps — Resolve version without downloading (registry-image only)
- `image: repo:tag` on resource_types — Direct image reference, skip check/get chain

### Tier 4 (architecture rethink):
- Replace `privileged: true` + docker-in-docker with sidecars
- Replace docker-compose in tasks with sidecar service containers
- Worker tags are removed — use K8s node selectors instead
- BaggageClaim/btrfs caching replaced by PVC/hostpath/artifact/emptydir backends

## Patterns to Flag
- `privileged: true` — Suggest sidecar alternative if used for dind
- `caches:` used for temp storage — Suggest `scratch_paths` instead
- docker/dockerd commands in task scripts — Suggest sidecars
- Large `get` steps that only need version info — Suggest `skip_download`
- `resource_types` with `type: docker-image` — Suggest `image:` field for direct reference
- Missing `container_requests` — Suggest for Burstable QoS
- Missing `ephemeral_storage` on disk-heavy tasks — Suggest limits

## Pipeline YAML to Analyze

<paste pipeline YAML here>

Respond with a structured migration plan organized by tier. Only suggest
changes that would meaningfully improve the pipeline.
```

---

## Real-World Migration Examples

### Example 1: Go Build Pipeline

**Before:**
```yaml
jobs:
- name: build-and-test
  plan:
  - get: source-code
    trigger: true
  - task: test
    privileged: true
    config:
      platform: linux
      image_resource:
        type: registry-image
        source: { repository: golang, tag: "1.25" }
      inputs:
        - name: source-code
      caches:
        - path: go-mod-cache
        - path: tmp-build        # temp storage, not a real cache
      container_limits:
        cpu: 2048
        memory: 4GB
      run:
        path: sh
        args:
          - -c
          - |
            export GOMODCACHE=$PWD/go-mod-cache
            export TMPDIR=$PWD/tmp-build
            cd source-code
            go test ./...
            go build -o ../output/binary ./cmd/...
      outputs:
        - name: output
```

**After (JetBridge):**
```yaml
jobs:
- name: build-and-test
  plan:
  - get: source-code
    trigger: true
  - task: test
    config:
      platform: linux
      image_resource:
        type: registry-image
        source: { repository: golang, tag: "1.25" }
      inputs:
        - name: source-code
      caches:
        - path: go-mod-cache       # Real cache — persist module downloads
      scratch_paths:
        - path: tmp-build           # Temp storage — use scratch_paths
      container_limits:
        cpu: 2048
        memory: 4GB
        ephemeral_storage: 10GB     # Prevent node disk pressure
      container_requests:
        cpu: 512                    # Burstable QoS
        memory: 1GB
        ephemeral_storage: 2GB
      run:
        path: sh
        args:
          - -c
          - |
            export GOMODCACHE=$PWD/go-mod-cache
            export TMPDIR=$PWD/tmp-build
            cd source-code
            go test ./...
            go build -o ../output/binary ./cmd/...
      outputs:
        - name: output
```

**Changes:**
1. Removed `privileged: true` (not needed for Go builds)
2. Moved `tmp-build` from `caches` to `scratch_paths` (it's temp storage, not a real cache)
3. Added `ephemeral_storage` limits (prevents node disk pressure from large builds)
4. Added `container_requests` for Burstable QoS (can burst to 2 CPU / 4GB but only requests 512m / 1GB)

### Example 2: Integration Test Pipeline with Services

**Before:**
```yaml
resource_types:
  - name: slack-notification
    type: docker-image
    source:
      repository: cfcommunity/slack-notification-resource
      tag: latest

jobs:
- name: integration-tests
  plan:
  - get: source-code
    trigger: true
  - task: run-tests
    privileged: true
    config:
      platform: linux
      image_resource:
        type: registry-image
        source: { repository: my-app-test }
      inputs:
        - name: source-code
      container_limits:
        cpu: 4096
        memory: 8GB
      run:
        path: sh
        args:
          - -c
          - |
            # Start docker daemon
            dockerd &
            sleep 5
            # Start services
            docker run -d --name pg -e POSTGRES_PASSWORD=test \
              -p 5432:5432 postgres:16
            docker run -d --name redis -p 6379:6379 redis:7
            # Wait for services
            sleep 10
            # Run tests
            cd source-code
            DATABASE_URL=postgres://postgres:test@localhost:5432/test \
            REDIS_URL=redis://localhost:6379 \
            ./run-integration-tests.sh
  - put: notify-slack
    resource: slack-notification
    params:
      text: "Integration tests passed"
```

**After (JetBridge):**
```yaml
resource_types:
  - name: slack-notification
    type: docker-image
    image: cfcommunity/slack-notification-resource:latest   # Direct image ref

jobs:
- name: integration-tests
  plan:
  - get: source-code
    trigger: true
  - task: run-tests
    config:
      platform: linux
      image_resource:
        type: registry-image
        source: { repository: my-app-test }
      inputs:
        - name: source-code
      container_limits:
        cpu: 2048                    # Less CPU needed (no dockerd overhead)
        memory: 4GB
        ephemeral_storage: 5GB
      container_requests:
        cpu: 512
        memory: 1GB
      run:
        path: sh
        args:
          - -c
          - |
            cd source-code
            DATABASE_URL=postgres://postgres:test@localhost:5432/test \
            REDIS_URL=redis://localhost:6379 \
            ./run-integration-tests.sh
    sidecars:
      - name: postgres
        image: postgres:16
        env:
          - name: POSTGRES_PASSWORD
            value: test
        ports:
          - containerPort: 5432
        resources:
          requests:
            cpu: "100m"
            memory: "256Mi"
          limits:
            cpu: "500m"
            memory: "512Mi"
      - name: redis
        image: redis:7
        ports:
          - containerPort: 6379
        resources:
          requests:
            memory: "64Mi"
  - put: notify-slack
    resource: slack-notification
    params:
      text: "Integration tests passed"
```

**Changes:**
1. Replaced docker-in-docker with inline sidecars (postgres + redis)
2. Removed `privileged: true` (no longer needed without dockerd)
3. Added `image:` to slack resource type (direct reference, skips check/get)
4. Reduced CPU/memory limits (no dockerd overhead)
5. Added `ephemeral_storage` and `container_requests`
6. Sidecars share `localhost` — same connection URLs work
7. No `sleep` for service startup — sidecars start before the main container

---

## Compatibility Notes

### What Works Unchanged
- All pipeline YAML syntax (resources, jobs, steps, hooks)
- `fly` CLI commands (`set-pipeline`, `builds`, `intercept`, `execute`)
- Resource types (git, time, registry-image, s3, etc.)
- Web UI
- Authentication (OIDC, OAuth, local users)
- API endpoints
- `caches:` declarations (backed by configurable backend)
- `hermetic: true` (requires cluster NetworkPolicy controller)

### What's Different
- Pods instead of Garden containers (visible in `kubectl`, not `fly containers`)
- No worker VMs or `concourse worker` binary
- Single worker per namespace (`k8s-<namespace>`)
- Cache persistence depends on backend selection (operator config)
- `fly intercept` terminal resize doesn't work (TTY is no-op)

### What's Removed
- TSA (SSH tunnel worker registration)
- BaggageClaim (volume management)
- Garden (container runtime)
- Worker tags and platform filtering
- External/remote worker support
