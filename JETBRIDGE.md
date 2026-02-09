# JetBridge: Kubernetes-Native Runtime for Concourse

JetBridge replaces Concourse's Garden/containerd worker architecture with
direct Kubernetes pod execution. If you're familiar with standard Concourse,
this document explains what changed, what didn't, and how to operate it.

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

Pods are labelled with `concourse.ci/pipeline`, `concourse.ci/job`,
`concourse.ci/build`, `concourse.ci/step`, and `concourse.ci/handle` for
easy filtering with `kubectl`.

### Artifact passing via PVC

Standard Concourse streams artifacts between workers over SPDY connections
managed by the ATC. JetBridge replaces this with a shared PVC:

1. Step A runs, produces output in an emptyDir volume.
2. An artifact-helper sidecar tars the emptyDir to the artifact store PVC.
3. Step B's init container extracts the tar from the PVC into its own emptyDir.
4. Step B runs with the data available.

The artifact store PVC can be any storage class: hostPath (single-node),
NFS, GCS FUSE, EBS, etc. Multi-node clusters need `ReadWriteMany` access.

Without the artifact store PVC configured, JetBridge falls back to SPDY
streaming (same as standard Concourse but between pods instead of workers).

### Resource caching via PVC

Standard Concourse caches resource versions on worker-local volumes managed
by BaggageClaim. JetBridge uses a shared PVC mounted at `/concourse/cache`
with subPath mounts per cache entry. Data survives pod restarts.

Without the cache PVC configured, caches use emptyDir (ephemeral, per-pod).

### Exec mode for stdin/stdout

Pods run a pause command (`sleep 86400`) and the web node execs the real
command via the Kubernetes exec API (SPDY). This gives full stdin/stdout/
stderr separation, which standard Concourse gets from Garden's process API.

This is what makes `fly intercept` work — the pause pod stays running after
the command exits, and `fly intercept` execs a new shell into it.

### Garbage collection

A reaper component runs every 30 seconds and:

1. Lists pods with the `concourse.ci/worker` label.
2. Reports active containers to the DB (marks missing ones for GC).
3. Deletes pods the DB has marked for destruction.
4. Cleans up cache PVC subdirectories by exec-ing `rm -rf` in an active pod.
5. Cleans up artifact store tar files by exec-ing `rm -f` in an active pod's
   artifact-helper sidecar.

Cache and artifact cleanup require at least one active pod in the namespace.

## What Didn't Change

- **fly CLI**: Works identically. `fly set-pipeline`, `fly builds`,
  `fly intercept`, `fly execute` all work.
- **Pipeline YAML**: No changes to pipeline definitions.
- **Resource types**: Base resource types (git, time, registry-image, s3,
  etc.) are mapped to their standard Docker images.
- **Web UI**: Identical.
- **PostgreSQL schema**: Same database, same migrations.
- **Auth**: Same OIDC/OAuth/local user configuration.
- **API**: Same REST API.

## Known Limitations

- **TTY**: Not supported for Kubernetes pods in this implementation.
  `SetTTY` is a no-op.
- **Single namespace**: One worker per namespace. The worker name is
  deterministic (`k8s-<namespace>`).
- **`fly execute -i` with artifact store**: When ArtifactStoreClaim is
  configured, `fly execute --input` needs additional work to handle the
  upload path (the volume's StreamIn returns an error directing callers
  to use the artifact-helper instead).

## Configuration Reference

JetBridge is enabled by setting `--kubernetes-namespace`. All other
Kubernetes flags are optional.

| Flag | Default | Description |
|------|---------|-------------|
| `--kubernetes-namespace` | (required) | Namespace for task pods. Enables K8s backend. |
| `--kubernetes-kubeconfig` | in-cluster | Path to kubeconfig. Empty uses the pod's service account. |
| `--kubernetes-pod-startup-timeout` | `5m` | Max time for a pod to reach Running before failing the task. |
| `--kubernetes-cache-pvc` | (none) | PVC name for shared cache volume. |
| `--kubernetes-artifact-store-claim` | (none) | PVC name for artifact passing. |
| `--kubernetes-artifact-helper-image` | `alpine:latest` | Image for init containers and sidecar. Must have `tar`. |
| `--kubernetes-image-pull-secret` | (none) | Secret names for imagePullSecrets (repeatable). |
| `--kubernetes-service-account` | (default) | ServiceAccount for task pods. |
| `--kubernetes-image-registry-prefix` | (none) | Registry prefix for custom resource type images. |
| `--kubernetes-image-registry-secret` | (none) | Pull secret for the image registry. |

### Resource type image mapping

Base resource types are mapped to Docker images:

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

Custom resource types use the `--kubernetes-image-registry-prefix` to
resolve images. For example, with prefix `gcr.io/my-project/concourse`,
a custom type `my-type` resolves to `gcr.io/my-project/concourse/my-type`.

## Deployment

### Prerequisites

- Kubernetes cluster (GKE, EKS, AKS, k3s, kind)
- `kubectl` configured with cluster access
- Helm 3
- A container image built from `Dockerfile.build`

### Build the image

```bash
./build.sh ghcr.io/your-org/concourse:latest
```

This runs a multi-stage Docker build: Node (frontend assets) → Go (binary
with embedded assets) → runtime (minimal Ubuntu with ca-certificates).

### Install with Helm

```bash
helm install concourse ./deploy/chart \
  --namespace concourse --create-namespace \
  --set image.repository=ghcr.io/your-org/concourse \
  --set image.tag=latest \
  --set web.externalUrl=https://concourse.example.com
```

See `deploy/chart/values.yaml` for all configurable parameters.

### Production checklist

- **Database**: Use an external managed database (Cloud SQL, RDS).
  Set `postgresql.enabled=false` and provide `postgresql.host`/`postgresql.port`.
- **Auth**: Replace `web.localUsers` with OIDC/OAuth via `web.extraArgs`.
- **Secrets**: Generate signing keys externally and set `secrets.create=false`.
- **Multi-node**: Set `artifactStorePvc.accessModes: [ReadWriteMany]` and
  use a storage class that supports it (NFS, GCS FUSE, EFS).
- **Ingress**: Enable `ingress.enabled=true` with TLS.
- **Image registry**: Set `kubernetes.imageRegistryPrefix` and
  `kubernetes.imageRegistrySecret` if using custom resource types from a
  private registry.

### RBAC

The web pod needs these permissions in its namespace:

```yaml
apiGroups: [""]
resources: ["pods", "pods/exec", "pods/log"]
verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
```

The Helm chart creates these automatically when `rbac.create=true`.

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
- PVC not bound (storage class misconfigured)

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
For private registries, verify `kubernetes.imagePullSecrets` or
`kubernetes.imageRegistrySecret` is set correctly.

### Artifact passing failures

**Symptoms**: Downstream step runs with empty inputs, or build fails with
"uploading artifacts".

**Check**:
```bash
# Verify the artifact store PVC is bound and writable
kubectl -n concourse get pvc concourse-artifacts

# Check the artifact-helper sidecar logs
kubectl -n concourse logs <pod-name> -c artifact-helper
```

**Common causes**:
- PVC full (the sidecar's `tar cf` fails with "No space left on device")
- PVC access mode wrong (`ReadWriteOnce` on a multi-node cluster)
- Init container failed (the tar file doesn't exist on the PVC yet —
  check that the upstream step completed successfully)

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
period or intercept while the build is still running.

### Pods not being cleaned up

**Symptoms**: Old pods accumulate in the namespace.

The reaper runs every 30 seconds. Check that the web pod is healthy and
can reach the Kubernetes API server. Verify RBAC permissions include
`delete` on pods.

```bash
kubectl -n concourse get pods -l concourse.ci/worker --sort-by=.metadata.creationTimestamp
```

## Monitoring

### Prometheus metrics

JetBridge emits two K8s-specific metrics in addition to standard Concourse
metrics:

| Metric | Type | Description |
|--------|------|-------------|
| `concourse_k8s_pod_startup_duration_ms` | Gauge | Time from pod creation to Running phase (milliseconds). Reports max value per scrape interval. |
| `concourse_k8s_image_pull_failures_total` | Counter | Number of ImagePullBackOff / ErrImagePull failures. |

Standard Concourse metrics (`concourse_containers_created_total`,
`concourse_failed_containers_total`, etc.) continue to work.

### Key things to watch

- **Pod startup latency**: If `pod_startup_duration_ms` climbs, check node
  capacity, image pull times, or PVC binding delays.
- **Image pull failures**: Sustained failures indicate a registry issue or
  misconfigured pull secrets.
- **PVC usage**: Monitor disk usage on the cache and artifact store PVCs.
  Full PVCs cause tar failures.
- **Pod count**: Track pods in the namespace. A growing count of
  non-Running pods indicates the reaper isn't keeping up or pods are stuck.

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

# PVC usage (requires metrics-server or df in a pod)
kubectl -n concourse exec deploy/concourse-jetbridge-web -- df -h /concourse/cache
```
