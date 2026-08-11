# JetBridge: Kubernetes-Native Runtime for Concourse

JetBridge replaces Concourse's Garden/containerd worker architecture with
direct Kubernetes pod execution. If you're familiar with standard Concourse,
this document explains what changed, what didn't, and how to operate it.

## Origin and Goal of this project
I've always been interested in devops, and I use concourse and kubernetes extensively at my day job. I've long wished that concourse supported a kubernetes-native backend for processing, as tasks seemed to map cleanly into jobs. As we've entereed this agentic era of coding, I wanted to see how far I could push some new tools I was testing, and rewriting the backend of concourse to be kubernetes native seemed like a good domain- one I knew enough about to make major architectural and product decisions, but not enough about to have a preconceived idea of the best way to solve the problem.

To be clear, all of the modifications to the base concourse are 100% vibe coded. This should be taken as an adventure into agent-first codign (where I focused on specifying the outcomes, not the implementation), and a proof of concept for how a k8s runtime for concourse might work- I was surprised at just how much I was able to do away with in favor of having the web node talk directly to the k8s api server, although it did require some compromises on the purity of the concourse architecture.

Adjacent to the k8s runtime work, this also includes agent-first workflows for concourse. I think concourse's DAG model has tremendous potential as a platform for agentic CI operations. At its core, I've always viewed concourse tasks as functions that take an immput and produce an output. The composability of these functions is what gives concourse its power and flexibility, and also why I think it has great potential as an agent platform. The ability to tightly, consistently, and repeatably define exactly what gets fed to an agent and exactly what it produces allow the volatility of the AI Agents themselves to be more tightly bounded for repeatable workflows. The _results_ themselves may differ (unlike a traditional concourse task where ideally a set of inputs produces exactly the same outputs), but the _process_ of producing those results becomes versioned, repeatable, composable, _and_ transparent.


## What Changed

### No Workers

Standard Concourse runs a `concourse worker` process on dedicated VMs.
Workers register with the web node via TSA (SSH tunnels), run Garden
containers, and manage local volumes for caching and artifact passing.

JetBridge removes all of that. The web node talks directly to the
Kubernetes API server. Every pipeline step (task, get, put, check) becomes
a Kubernetes pod. There is no `concourse worker` binary, no TSA, no
Garden, no BaggageClaim.

The web node registers itself as a synthetic worker named `k8s-<namespace>`
by writing directly to the Concourse database. It heartbeats every 15
seconds with a 30-second TTL.

### Pod-per-step execution

Each build step creates one pod:

```
fly set-pipeline → web schedules build → web creates pod → pod runs → pod cleaned up
```

Pods use human-readable names derived from pipeline metadata:
`<pipeline>-<job>-b<build>-<type>-<8hex>`, truncated to 63 characters.
Check pods use `chk-<resource>-<8hex>`.
Resource type operations use `rt-<step-name>-<type>-<8hex>`.

Pods are labelled with `concourse.ci/pipeline`, `concourse.ci/job`,
`concourse.ci/build`, `concourse.ci/step`, and `concourse.ci/handle` for
easy filtering with `kubectl`.

### Artifact passing via the artifact daemon

Standard Concourse streams artifacts between workers over SPDY connections
managed by the ATC. JetBridge replaces this with a per-node DaemonSet over
node-local storage:

1. Step A runs, producing output into a node-local hostPath directory.
2. The node's artifact daemon registers the output and asynchronously mirrors it to peer daemons.
3. Step B's init container asks its local daemon to resolve each input; the daemon fetches from the source node or a mirror when the artifact is not already local.
4. Step B starts with the resolved hostPath mounted at the declared input path.

No shared `ReadWriteMany` volume is involved. The daemon is mandatory: the web
node refuses to start on the K8s runtime without
`--kubernetes-artifact-daemon-host-path`, because a downstream read must never
exec into the producing pod (which is reaped as soon as its step finishes).

### Resource caching

Standard Concourse caches resource versions on worker-local volumes managed
by BaggageClaim. JetBridge uses node-local cache directories under the
daemon's host path, with subPath mounts per cache entry. Data survives pod
restarts on the same node.

Set `--kubernetes-cache-store=emptydir` for ephemeral, per-pod caches.

### Exec mode for stdin/stdout

Pods run a pause command (`sleep 86400`) and the web node execs the real
command via the Kubernetes exec API (SPDY). This gives full stdin/stdout/
stderr separation, which standard Concourse gets from Garden's process API.

This is what makes `fly intercept` work — the pause pod stays running after
the command exits, and `fly intercept` execs a new shell into it.

### Garbage collection

A reaper component runs on the default 10-second component-polling interval
(it has no dedicated flag of its own) and:

1. Lists pods with the `concourse.ci/worker` label.
2. Reports active containers to the DB (marks missing ones for GC).
3. Deletes pods the DB has marked for destruction.
4. Deletes each destroyed container's daemon artifact entry with an HTTP
   request to the producing node's daemon.

The artifact daemon owns TTL cleanup and mirror lifecycle; the reaper deletes
artifacts via an HTTP request to the owning node's daemon rather than execing
`rm` inside a live pod's cache volume or relying on an artifact-helper
sidecar. Task caches themselves are `hostpath` or `emptyDir` volumes
(`--kubernetes-cache-store`) — there is no PVC-backed cache in this binary.

## What Didn't Change

- **fly CLI**: Works identically. `fly set-pipeline`, `fly builds`,
  `fly intercept`, `fly execute` all work.
- **Pipeline YAML**: Core syntax unchanged. New optional fields added (see below).
- **Resource types**: Base resource types (git, time, registry-image, s3,
  etc.) are mapped to their standard Docker images.
- **Web UI**: Identical.
- **PostgreSQL schema**: Same database, same migrations.
- **Auth**: Same OIDC/OAuth/local user configuration.
- **API**: Same REST API (plus new health endpoint, see below).

## New Pipeline Features

### skip_download on get steps

Resolves a resource version without downloading artifacts. The version metadata
is registered in the artifact repository for downstream steps, but no data is
fetched. Useful for triggering on new versions without checking out code.

```yaml
- get: my-repo
  skip_download: true
```

**Validation**: `skip_download` is only valid for resources of type `registry-image`
or custom resource types that have an `image:` field.

Source: `atc/steps.go` (GetStep.SkipDownload), `atc/step_validator.go`

### Task step sidecars

Service containers (databases, caches, mock servers) that run alongside a task
in a shared pod network. They start before the main task container and share
`localhost`.

**Inline config:**

```yaml
- task: integration-tests
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
        ports:
          - containerPort: 5432
        resources:
          requests:
            cpu: "100m"
            memory: "256Mi"
```

**File reference** (path to a YAML list in a build artifact):

```yaml
- task: my-task
  config:
    sidecars:
      - "my-artifact/sidecars.yml"
```

**Image artifact reference** (use an image built in a prior step):

```yaml
- task: build-image
  config:
    outputs:
      - name: image
- task: use-built-image
  config:
    sidecars:
      - name: app
        image_artifact: image
```

#### Sidecar configuration fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Container name. Cannot be `main` or `artifact-helper`. |
| `image` | string | yes* | Docker image reference. |
| `image_artifact` | string | yes* | Build artifact name from prior step. Mutually exclusive with `image`. |
| `command` | []string | no | Entrypoint override. |
| `args` | []string | no | Arguments to the entrypoint. |
| `env` | []EnvVar | no | Environment variables (`name`/`value` pairs). |
| `ports` | []Port | no | Exposed ports (`containerPort`, optional `protocol`: TCP/UDP/SCTP). |
| `resources` | object | no | K8s resource requests/limits (`cpu`, `memory`). |
| `workingDir` | string | no | Working directory inside the container. |

Source: `atc/sidecar.go`, `atc/worker/jetbridge/container.go`

### Configurable base resource types

Override or extend the default base resource type image mappings:

```
--kubernetes-base-resource-type git=my-registry/git-resource:v2
--kubernetes-base-resource-type my-custom-type=my-registry/custom:latest
```

Format: `name=image`. Can be specified multiple times. Merges with built-in defaults.

Default mappings:

| Type | Image |
|------|-------|
| `time` | `concourse/time-resource` |
| `git` | `concourse/git-resource` |
| `registry-image` | `concourse/registry-image-resource` |
| `s3` | `concourse/s3-resource` |
| `docker-image` | `concourse/docker-image-resource` |
| `pool` | `concourse/pool-resource` |
| `semver` | `concourse/semver-resource` |
| `mock` | `concourse/mock-resource` |

Custom resource types use `--kubernetes-image-registry-prefix` to resolve images.
For example, with prefix `gcr.io/my-project/concourse`, a custom type `my-type`
resolves to `gcr.io/my-project/concourse/my-type`.

Source: `atc/worker/jetbridge/config.go` (DefaultResourceTypeImages, MergeResourceTypeImages)

### Direct image references for resource types

Resource types can specify a direct container image reference via `image_ref`,
bypassing check/get plans entirely. Supports `repo:tag` or `repo@sha256:digest`.

This is used internally when resolving resource type images — the K8s runtime
resolves images to direct references rather than going through the
check-resource-type → get-resource-type chain that Garden used.

Source: `atc/plan.go` (TypeImage.ImageRef)

## Health Endpoint

`GET /api/v1/health` — Returns 200 when healthy, 503 when unhealthy.

Response schema:

```json
{
  "healthy": true,
  "db": "ok",
  "workers": "ok",
  "db_error": null
}
```

| Field | Values | Description |
|-------|--------|-------------|
| `healthy` | `true`/`false` | Overall health status. |
| `db` | `ok`, `unhealthy`, `not-configured` | PostgreSQL connectivity. |
| `workers` | `ok`, `none`, `error`, `not-configured` | Worker availability. |
| `db_error` | string or null | Error details when DB is unhealthy. |

Used by Helm chart for Kubernetes readiness probes (`web.readinessProbe`).
Startup and liveness probes use `/api/v1/info`.

Source: `atc/api/infoserver/health.go`

## Known Limitations

- **TTY**: Not supported for Kubernetes pods in this implementation.
  `SetTTY` is a no-op.
- **Single namespace**: One worker per namespace. The worker name is
  deterministic (`k8s-<namespace>`).

## Configuration Reference

### Kubernetes Runtime Flags

JetBridge is enabled by setting `--kubernetes-namespace`. The artifact daemon
host path, resolve-capability key, and a digest-pinned helper image are also
required; the remaining Kubernetes flags are optional.

| Flag | Default | Description |
|------|---------|-------------|
| `--kubernetes-namespace` | (required) | Namespace for task pods. Enables K8s backend. |
| `--kubernetes-kubeconfig` | in-cluster | Path to kubeconfig. Empty uses the pod's service account. |
| `--kubernetes-pod-startup-timeout` | `5m` | Max time for a pod to reach Running before failing the task. |
| `--kubernetes-pod-scheduling-timeout` | `15m` | Max time to wait for an Unschedulable pod to be scheduled before failing the task. Non-positive values use the `15m` runtime default. |
| `--kubernetes-cache-store` | (auto) | Task cache backend: `hostpath` (node-local dirs) or `emptydir` (ephemeral). |
| `--kubernetes-cache-host-path` | (none) | Base directory on the host node for persistent task caches. Defaults to a directory under the artifact daemon's host path. |
| `--kubernetes-artifact-daemon-host-path` | (required) | Host path for node-local artifact storage. Required whenever `--kubernetes-namespace` is set. |
| `--kubernetes-artifact-daemon-port` | `7780` | HTTP(S) port of the per-node artifact daemon. |
| `--kubernetes-artifact-daemon-service` | `artifact-daemon` | Headless Service name for DaemonSet per-pod DNS. |
| `--kubernetes-artifact-daemon-resolve-capability-key` | (required) | Path to the raw 32-byte key that authorizes artifact resolve operations. |
| `--kubernetes-artifact-daemon-resolve-capability-ttl` | `2h` | Lifetime of operation-bound resolve capabilities; it must cover pod admission and init retry bounds. |
| `--kubernetes-artifact-daemon-tls-cert` | (none) | Client certificate path for mTLS with the artifact daemon. |
| `--kubernetes-artifact-daemon-tls-key` | (none) | Client private-key path for mTLS with the artifact daemon. |
| `--kubernetes-artifact-daemon-tls-ca-cert` | (none) | CA certificate path for verifying the artifact daemon. |
| `--kubernetes-artifact-helper-image` | (required) | Digest-pinned helper image for artifact fetch and cleanup init containers. It must use an exact `@sha256` reference. |
| `--kubernetes-image-pull-secret` | (none) | K8s Secret for imagePullSecrets on task pods. Repeatable. |
| `--kubernetes-service-account` | namespace default | ServiceAccount for task pods. |
| `--kubernetes-image-registry-prefix` | (none) | Registry prefix for custom resource type images (e.g. `gcr.io/my-project/concourse`). |
| `--kubernetes-image-registry-secret` | (none) | K8s Secret (type `kubernetes.io/dockerconfigjson`) for registry auth. Auto-added to every pod. |
| `--kubernetes-base-resource-type` | (none) | Override base resource type images. Format: `name=image`. Repeatable. Merges with defaults. |

Source: `atc/atccmd/command.go` (RunCommand.Kubernetes struct)

### Garbage Collection Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--gc-interval` | `30s` | Interval between GC runs. |
| `--gc-one-off-grace-period` | `5m` | Grace period before GC of one-off build containers. |
| `--gc-missing-grace-period` | `5m` | Grace period for containers that went missing from the worker. |
| `--gc-hijack-grace-period` | `5m` | Grace period before GC of hijacked containers. |
| `--gc-failed-grace-period` | `120h` | Grace period before GC of failed containers. |
| `--gc-check-recycle-period` | `1m` | Interval for reaping completed checks. |
| `--gc-var-source-recycle-period` | `5m` | Interval for reaping unused credential/var sources. |

Source: `atc/atccmd/command.go` (RunCommand.GC struct)

### OpenTelemetry Tracing Flags

Tracing supports four backends. Only one can be active at a time (first configured wins).

**Common flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--tracing-service-name` | `concourse-web` | Service name in traces. |
| `--tracing-attribute` | (none) | Key:value attributes on all traces. Repeatable. |

**OTLP (recommended):**

| Flag | Default | Description |
|------|---------|-------------|
| `--tracing-otlp-address` | (none) | OTLP gRPC endpoint (e.g. `tempo.monitoring.svc:4317`). |
| `--tracing-otlp-header` | (none) | Headers on OTLP requests. Repeatable. |
| `--tracing-otlp-use-tls` | `false` | Use TLS for OTLP connection. |

**Jaeger:**

| Flag | Default | Description |
|------|---------|-------------|
| `--tracing-jaeger-endpoint` | (none) | Jaeger HTTP thrift collector URL. |
| `--tracing-jaeger-tags` | (none) | Key:value tags on Jaeger spans. Repeatable. |
| `--tracing-jaeger-service` | `web` | Jaeger service name. |

**Honeycomb:**

| Flag | Default | Description |
|------|---------|-------------|
| `--tracing-honeycomb-api-key` | (none) | Honeycomb API key. |
| `--tracing-honeycomb-dataset` | (none) | Honeycomb dataset name. |

**Stackdriver:**

| Flag | Default | Description |
|------|---------|-------------|
| `--tracing-stackdriver-projectid` | (none) | GCP project ID for Cloud Trace. |

**Sampling:**

| Flag | Default | Description |
|------|---------|-------------|
| `--tracing-sampling-strategy` | `always` | Strategy: `always`, `never`, `probability`. |
| `--tracing-sampling-rate` | `1.0` | Sampling rate for `probability` strategy (0.0 to 1.0). |

Source: `tracing/tracer.go`, `tracing/otlp.go`, `tracing/jaeger.go`, `tracing/honeycomb.go`, `tracing/sampling.go`

### OpenTelemetry Metrics Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--otel-metrics-otlp-address` | (none) | OTLP gRPC endpoint for metrics export. |
| `--otel-metrics-otlp-header` | (none) | Headers on OTLP metrics requests. Repeatable. |
| `--otel-metrics-otlp-use-tls` | `false` | Use TLS for metrics OTLP connection. |
| `--otel-metrics-gcp-project-id` | (none) | GCP project ID for Cloud Monitoring (uses `monitoring.googleapis.com:443`). |

Source: `tracing/meter.go`

## Deployment

### Prerequisites

- Kubernetes cluster (GKE, EKS, AKS, k3s, kind)
- `kubectl` configured with cluster access
- Helm 3
- A container image built from `Dockerfile.build`
- A BusyBox-compatible artifact-helper image (`sh`, `wget`, `base64`, `cat`,
  `rm`, `find`, `chgrp`, `chmod`) resolved to an exact OCI digest

### Build the image

```bash
./build.sh ghcr.io/your-org/concourse:latest
```

This runs a multi-stage Docker build: Node (frontend assets) → Go (binary
with embedded assets) → runtime (minimal Ubuntu with ca-certificates).

On this fork's dev machine there is no local Docker daemon — point `DOCKER_HOST`
at the theborg dind pod first (`./hack/borg-docker.sh up`, see
[docs/docker-on-theborg.md](docs/docker-on-theborg.md)). It is native
`linux/amd64`, so the default platform needs no emulation there.

Options: `PLATFORM=linux/arm64` for ARM builds, `CONCOURSE_VERSION=x.y.z` for
version injection, `--push` flag to push to registry.

### Install with Helm

```bash
export ARTIFACT_HELPER_IMAGE='registry.example/jetbridge/artifact-helper@sha256:<64-lowercase-hex>'
helm install concourse ./deploy/chart \
  --namespace concourse --create-namespace \
  --set image.repository=ghcr.io/your-org/concourse \
  --set image.tag=latest \
  --set-string kubernetes.artifactHelperImage="${ARTIFACT_HELPER_IMAGE}" \
  --set web.externalUrl=https://concourse.example.com
```

The helper image has no chart default. Replace the placeholder with a real
digest reference; a mutable tag causes Helm rendering to fail.

See `deploy/chart/values.yaml` for all configurable parameters and
`deploy/chart/README.md` for the complete Helm values reference.

### Production checklist

- **Database**: Use an external managed database (Cloud SQL, RDS).
  Set `postgresql.enabled=false` and provide `postgresql.host`/`postgresql.port`.
- **Auth**: Replace `web.localUsers` with OIDC/OAuth via `web.extraArgs`.
- **Secrets**: All web replicas MUST share the same signing key. The default
  `secrets.create=true` already does that; supply `secrets.signingKeySecret`
  instead when the key is managed externally or the chart is rendered without
  a cluster connection. The same applies to `artifactDaemon.tls.existingSecret`
  and `artifactDaemon.resolveCapability.existingSecret`: GitOps tools that only
  run `helm template` (e.g. Argo CD without the Helm plugin) get an empty
  `lookup` and re-mint these Secrets on every render. The resolve-capability
  key churns on *every* such deployment (it's required whenever the daemon is,
  which the K8s runtime mandates) and silently fails artifact fetch after the
  next asymmetric restart; set the `existingSecret` values under GitOps.
- **Multi-node**: Run the artifact DaemonSet on every eligible build node and
  set `artifactDaemon.mirror.replicas` for the desired failure tolerance.
- **Ingress**: Enable `ingress.enabled=true` with TLS.
- **Image registry**: Set `kubernetes.imageRegistryPrefix` and
  `kubernetes.imageRegistrySecret` if using custom resource types from a
  private registry.
- **Artifact helper**: Pin `kubernetes.artifactHelperImage` by digest. Treat
  changing it as a runtime rollout, because it executes in every task/check
  pod that materializes or captures artifacts.
- **Agent snapshot scratch**: When snapshots are enabled, size the disk-backed
  emptyDir or PVC for the extracted tree, spool, canonical archive, and upload
  copy across every concurrent output. A conservative floor is
  `4 * max snapshot bytes * peak concurrent seals`, plus retry headroom.
- **Connection pool sizing**: For N web replicas, ensure PostgreSQL
  `max_connections >= N * (apiMaxConns + backendMaxConns + 7)`.
  Default: `N * (10 + 50 + 7) = 67 per replica`.

### RBAC

The web pod needs these permissions in its namespace:

```yaml
apiGroups: [""]
resources: ["pods", "pods/exec", "pods/log"]
verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
```

The Helm chart creates these automatically when `rbac.create=true`.
It disables pod-level service-account token automount and projects a bounded
one-hour API token, CA, and namespace only into `concourse-web`. Init
containers do not receive the API credential.

## Troubleshooting

### Pod not starting

**Symptoms**: Build hangs, then fails with "timed out waiting for pod to start".

**Check**:
```bash
kubectl -n concourse get pods -l concourse.ci/worker
kubectl -n concourse describe pod <pod-name>
```

**Common causes**:
- Image pull failure (wrong image name, missing pull secret)
- Insufficient resources (no node can schedule the pod)
- Artifact daemon unavailable on the selected node, or its host path is not ready

JetBridge writes pod failure diagnostics to the build log, including the
pod phase, conditions, and container waiting reasons. Look for the
`--- Pod Failure Diagnostics ---` block in build output.

### Image pull errors

**Symptoms**: Build fails with "pod failed: ImagePullBackOff".

**Check**: The pod events will show the image that failed:
```bash
kubectl -n concourse describe pod <pod-name> | grep -A5 Events
```

For base resource types, verify the image mapping matches your environment.
Override with `--kubernetes-base-resource-type name=correct-image` if needed.
For private registries, verify `kubernetes.imagePullSecrets` or
`kubernetes.imageRegistrySecret` is set correctly.

### Artifact passing failures

**Symptoms**: Downstream step runs with empty inputs, or build fails with
"uploading artifacts".

**Check**:
```bash
# Verify an artifact daemon is Running on every build node
kubectl -n concourse get pods -l app.kubernetes.io/component=artifact-daemon -o wide

# Check the daemon on the node that produced the artifact
kubectl -n concourse logs <artifact-daemon-pod>

# Check the downstream fetch init container logs
kubectl -n concourse logs <pod-name> -c fetch-inputs
```

**Common causes**:
- Node disk full under `artifactDaemon.hostPath` (writes fail with "No space left on device")
- The producing node's daemon is gone and no mirror copy exists
  (`artifactDaemon.mirror.replicas: 1` keeps only the local copy)
- The consuming pod landed on a node without the
  `concourse.dev/artifact-cache=ready` label
- The `fetch-inputs` init container failed to resolve the producer or a
  mirrored peer; verify the upstream step completed successfully

### Build output missing

**Symptoms**: Build shows no output or partial output.

The log stream is non-fatal — if the connection drops, the build still
completes based on the pod exit code. Check for `warning: log stream
interrupted` in the build output. This usually means the Kubernetes API
server dropped the log follow connection.

### fly intercept not working

**Symptoms**: `fly intercept` fails with "container not found".

`fly intercept` works by exec-ing into the pause pod. If the reaper has
already cleaned up the pod, intercept will fail. Increase the GC grace
period (`--gc-hijack-grace-period`) or intercept while the build is still running.
A completed step's pod is retained for as long as its owning build keeps
running, so intercepting a finished step in an active build is reliable;
check pods are still reaped immediately.

Before `27d3a56a97`, a looked-up container resolved to the wrong pod name
for any pipeline job/task step, so `fly intercept -j pipeline/job` could
not work at all, and hijacking an already-completed pod deleted it outright
(destroying the exit-status annotation used for restart recovery). Both are
fixed: lookups now resolve to the pod the step actually created and never
fabricate or replace one.

### Pods not being cleaned up

**Symptoms**: Old pods accumulate in the namespace.

The reaper runs every `--gc-interval` (default 30s). Check that the web pod
is healthy and can reach the Kubernetes API server. Verify RBAC permissions
include `delete` on pods.

```bash
kubectl -n concourse get pods -l concourse.ci/worker --sort-by=.metadata.creationTimestamp
```

## Monitoring

### Prometheus metrics

JetBridge emits two K8s-specific metrics in addition to standard Concourse
metrics:

| Metric | Type | Description |
|--------|------|-------------|
| `concourse_k8s_pod_startup_duration_milliseconds` | Histogram | Time from pod creation to Running phase (milliseconds). Sampled every 10s as the max value observed since the last sample; query the `_bucket`/`_sum`/`_count` series (not a `_ms`-suffixed name). |
| `concourse_k8s_image_pull_failures_total` | Counter | Number of ImagePullBackOff / ErrImagePull failures. |

Standard Concourse metrics (`concourse_containers_created_total`,
`concourse_failed_containers_total`, etc.) continue to work.

Enable Prometheus scraping via Helm `serviceMonitor.enabled=true` **and**
`web.metrics.enabled=true` (requires prometheus-operator CRDs). The ATC only
starts its Prometheus listener, and the chart only renders the `metrics`
container/Service port and the web ServiceMonitor, when
`web.metrics.enabled=true`; `serviceMonitor.enabled=true` alone scrapes only
the artifact daemon.

### OpenTelemetry

Configure tracing and metrics export via the flags documented above. Example
for Grafana Tempo + OTLP:

```yaml
# Helm values
tracing:
  otlpAddress: "tempo.monitoring.svc:4317"
  serviceName: "concourse-web"
otelMetrics:
  otlpAddress: "tempo.monitoring.svc:4317"
```

### Key things to watch

- **Pod startup latency**: If `pod_startup_duration_ms` climbs, check node
  capacity, image pull times, or artifact-daemon readiness.
- **Image pull failures**: Sustained failures indicate a registry issue or
  misconfigured pull secrets.
- **Node disk usage**: Monitor `artifactDaemon.hostPath` on every build node.
  A full node disk fails artifact writes.
- **Pod count**: Track pods in the namespace. A growing count of
  non-Running pods indicates the reaper isn't keeping up or pods are stuck.
- **Health endpoint**: Poll `GET /api/v1/health` — returns 503 when DB or
  workers are unhealthy.

### Useful kubectl commands

```bash
# All Concourse pods, sorted by age
kubectl -n concourse get pods -l concourse.ci/worker --sort-by=.metadata.creationTimestamp

# Pods for a specific pipeline
kubectl -n concourse get pods -l concourse.ci/pipeline=my-pipeline

# Pods for a specific job
kubectl -n concourse get pods -l concourse.ci/job=my-job

# Failed pods
kubectl -n concourse get pods -l concourse.ci/worker --field-selector=status.phase=Failed

# Node-local artifact/cache disk usage, per daemon pod
kubectl -n concourse exec <artifact-daemon-pod> -- df -h /var/concourse/artifacts
```

## Key Source Files

| File | Purpose |
|------|---------|
| `atc/worker/jetbridge/config.go` | K8s flags, node-local cache config, base resource type image mappings |
| `atc/worker/jetbridge/container.go` | Pod creation, lifecycle management, sidecar injection |
| `atc/worker/jetbridge/executor.go` | Command execution via K8s exec API |
| `atc/worker/jetbridge/podname.go` | Deterministic pod name generation |
| `atc/worker/jetbridge/registrar.go` | Synthetic worker registration (direct DB write) |
| `atc/worker/jetbridge/storage_daemonset.go` | Daemon-backed artifact and cache storage decisions |
| `atc/worker/jetbridge/volume_daemonset.go` | Artifact-daemon backed volumes |
| `atc/worker/jetbridge/daemon_client.go` | HTTP client for the per-node artifact daemon |
| `atc/worker/jetbridge/errors.go` | Transient error classification and retry |
| `atc/worker/jetbridge/reaper.go` | Pod and volume garbage collection |
| `atc/worker/jetbridge/process.go` | Process abstraction over K8s exec |
| `atc/worker/jetbridge/volume.go` | Volume interface implementation |
| `atc/worker/jetbridge/watch.go` | Pod status watching |
| `atc/worker/jetbridge/worker.go` | Worker interface implementation |
| `atc/sidecar.go` | Sidecar type definitions, parsing, validation |
| `atc/api/infoserver/health.go` | Health endpoint implementation |
| `atc/atccmd/command.go` | All CLI flags (lines 108-279) |
| `tracing/` | OTel tracing and metrics configuration |
