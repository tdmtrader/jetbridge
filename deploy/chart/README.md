# Concourse JetBridge Helm Chart

Deploys Concourse CI with the JetBridge Kubernetes-native runtime. Instead of
running tasks in Garden containers on dedicated worker VMs, JetBridge creates
Kubernetes pods directly for every pipeline step.

**Key differences from the official Concourse chart:**

- No worker StatefulSet. Task pods are created on-demand by the web node.
- Artifact passing uses a shared PVC with init containers and a sidecar (no SPDY streaming between workers).
- The web node needs RBAC permissions to create pods, PVCs, and exec into containers in its namespace.

## Quickstart (k3s)

### 1. Build the image

```bash
./build.sh concourse-local:latest
```

On k3s, the image is available to the cluster automatically when using the
default containerd runtime. For kind, load it with `kind load docker-image`.

### 2. Install

Choose a small image with BusyBox-compatible `sh`, `wget`, `base64`, `cat`,
`rm`, `find`, `chgrp`, and `chmod`, resolve it to an OCI digest, and set the
full reference before installing. The chart intentionally has no mutable
helper-image default.

```bash
export ARTIFACT_HELPER_IMAGE='registry.example/jetbridge/artifact-helper@sha256:<64-lowercase-hex>'
helm install concourse ./deploy/chart \
  --namespace concourse --create-namespace \
  --set image.repository=concourse-local \
  --set image.tag=latest \
  --set image.pullPolicy=Never \
  --set-string kubernetes.artifactHelperImage="${ARTIFACT_HELPER_IMAGE}" \
  --set service.type=ClusterIP
```

### 3. Access the UI

```bash
kubectl -n concourse port-forward svc/concourse-jetbridge-web 8080:8080
```

Open http://localhost:8080 and log in with `test` / `test`.

### 4. Set a pipeline

```bash
fly -t local login -c http://localhost:8080 -u test -p test
fly -t local set-pipeline -p hello -c examples/hello.yml
fly -t local unpause-pipeline -p hello
```

## Quickstart (ArgoCD)

Create an ArgoCD `Application` pointing at the chart directory:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: concourse
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/your-org/concourse.git
    targetRevision: jetbridge
    path: deploy/chart
    helm:
      valueFiles:
        - values.yaml
      parameters:
        - name: image.repository
          value: ghcr.io/your-org/concourse
        - name: image.tag
          value: latest
        - name: web.externalUrl
          value: https://concourse.example.com
        # Replace this placeholder with a compatible helper resolved to its
        # exact OCI digest before syncing.
        - name: kubernetes.artifactHelperImage
          value: registry.example/jetbridge/artifact-helper@sha256:<64-lowercase-hex>
        - name: ingress.enabled
          value: "true"
        - name: ingress.host
          value: concourse.example.com
  destination:
    server: https://kubernetes.default.svc
    namespace: concourse
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
```

## Configuration Reference

All parameters are documented in [`values.yaml`](values.yaml). Complete reference below.

### Image

| Parameter | Default | Description |
|-----------|---------|-------------|
| `image.repository` | `concourse-local` | Docker image repository. |
| `image.tag` | `""` (appVersion) | Image tag. |
| `image.pullPolicy` | `IfNotPresent` | Pull policy. Use `Never` for local images on k3s/kind. |
| `image.pullSecrets` | `[]` | Image pull secrets for the web pod. |

### Web

| Parameter | Default | Description |
|-----------|---------|-------------|
| `web.replicas` | `1` | Number of web node replicas. |
| `web.externalUrl` | `http://localhost:8080` | URL users use to reach the UI. |
| `web.clusterName` | `jetbridge` | Cluster name displayed in the UI. |
| `web.logLevel` | `info` | Log level: `debug`, `info`, `error`. |
| `web.localUsers` | `test:test` | Local user credentials (`user:password`). |
| `web.mainTeamLocalUser` | `test` | User granted admin on the main team. |
| `web.apiMaxConns` | `10` | API connection pool max (per replica). |
| `web.backendMaxConns` | `50` | Backend connection pool max (per replica). |
| `web.terminationGracePeriodSeconds` | `120` | Graceful shutdown timeout. |
| `web.resources` | 100m/256Mi req, 2/2Gi limit | CPU/memory resources. |
| `web.env` | `[]` | Extra env vars (supports `value` and `valueFrom`). |
| `web.extraArgs` | `[]` | Additional CLI args for the web command. |
| `web.extraVolumeMounts` | `[]` | Additional volume mounts (e.g. CA bundles). |
| `web.extraVolumes` | `[]` | Additional volumes for the web pod. |
| `web.nodeSelector` | `{}` | Node selector for the web pod. |
| `web.tolerations` | `[]` | Tolerations for the web pod. |
| `web.affinity` | `{}` | Affinity rules for the web pod. |

### Web Security Context

| Parameter | Default | Description |
|-----------|---------|-------------|
| `web.podSecurityContext.runAsNonRoot` | `true` | Run pod as non-root. |
| `web.podSecurityContext.runAsUser` | `65534` | UID for web process. |
| `web.podSecurityContext.runAsGroup` | `65534` | GID for web process. |
| `web.podSecurityContext.fsGroup` | `65534` | fsGroup for volume mounts. |
| `web.containerSecurityContext.allowPrivilegeEscalation` | `false` | Prevent privilege escalation. |
| `web.containerSecurityContext.readOnlyRootFilesystem` | `true` | Read-only root filesystem. |
| `web.containerSecurityContext.capabilities.drop` | `["ALL"]` | Drop all Linux capabilities. |

### Web Probes

| Parameter | Default | Description |
|-----------|---------|-------------|
| `web.startupProbe.httpGet.path` | `/api/v1/info` | Startup probe path. |
| `web.startupProbe.initialDelaySeconds` | `5` | Initial delay. |
| `web.startupProbe.periodSeconds` | `5` | Check interval. |
| `web.startupProbe.failureThreshold` | `30` | Failures before restart (allows ~2.5min for DB migration). |
| `web.livenessProbe.httpGet.path` | `/api/v1/info` | Liveness probe path. |
| `web.livenessProbe.initialDelaySeconds` | `15` | Initial delay. |
| `web.livenessProbe.periodSeconds` | `15` | Check interval. |
| `web.livenessProbe.timeoutSeconds` | `3` | Timeout per check. |
| `web.livenessProbe.failureThreshold` | `5` | Failures before restart. |
| `web.readinessProbe.httpGet.path` | `/api/v1/health` | Readiness probe path. Checks DB + workers. |
| `web.readinessProbe.initialDelaySeconds` | `10` | Initial delay. |
| `web.readinessProbe.periodSeconds` | `10` | Check interval. |

### TLS

| Parameter | Default | Description |
|-----------|---------|-------------|
| `web.tls.enabled` | `false` | Enable native HTTPS on the web container. |
| `web.tls.bindPort` | `443` | HTTPS listen port. |
| `web.tls.secretName` | `concourse-web-tls` | K8s Secret containing TLS cert and key. |
| `web.tls.mountPath` | `/concourse-tls` | Mount path for TLS secret. |
| `web.tls.certFilename` | `tls.crt` | Key in Secret for the certificate. |
| `web.tls.keyFilename` | `tls.key` | Key in Secret for the private key. |

### Kubernetes Runtime

| Parameter | Default | Description |
|-----------|---------|-------------|
| `kubernetes.namespace` | release namespace | Namespace where task/check pods are created. Must equal the Helm release namespace. |
| `kubernetes.serviceAccount` | `""` (web SA) | ServiceAccount for task pods. |
| `kubernetes.podStartupTimeout` | `5m` | Max time to wait for pod Running. |
| `kubernetes.imagePullSecrets` | `[]` | Pull secrets for task pod images. |
| `kubernetes.artifactHelperImage` | required | BusyBox-compatible runtime helper image with the commands listed in Quickstart. Must be an exact reference ending in `@sha256:<64 lowercase hex>`. |
| `kubernetes.imageRegistryPrefix` | `""` | Registry prefix for custom resource type images. |
| `kubernetes.imageRegistrySecret` | `""` | Pull secret name for resource type images. |

The helper runs in task/check pods to materialize and capture artifacts. Helm
fails before rendering unless it is configured by digest; tags such as
`latest` are rejected. Jetbridge passes the accepted reference to the web
process exactly as configured, and `web.extraArgs` cannot replace it.

The Kubernetes runtime is currently single-namespace. Helm rejects a
`kubernetes.namespace` value that differs from `.Release.Namespace`; the
cross-namespace RBAC, artifact-daemon discovery, and TLS identities required
to support that topology are not yet provisioned by this chart.

The web pod sets `automountServiceAccountToken: false`. Only
`concourse-web` receives an explicit projected Kubernetes API credential at
the standard service-account path: a token with a one-hour maximum lifetime,
the namespace CA, and the current namespace file. The key-generation,
snapshot-scratch preparation, and database-migration init containers do not
receive that credential. The `web-kubernetes-api-access` and
`snapshot-scratch` volume names are reserved; Helm rejects attempts to
re-mount either through `web.extraVolumeMounts`.

### Agentic Snapshots and Publication

| Parameter | Default | Description |
|-----------|---------|-------------|
| `agentSnapshots.enabled` | `false` | Enable durable typed snapshots. Requires the artifact daemon and its mTLS mode. |
| `agentSnapshots.replicationFactor` | `2` | Desired durable snapshot replicas. |
| `agentSnapshots.maxBytes` | `10737418240` | Maximum uncompressed bytes per snapshot. |
| `agentSnapshots.maxFiles` | `100000` | Maximum files per snapshot. |
| `agentSnapshots.scratch.existingClaim` | `""` | Existing PVC for web snapshot scratch. |
| `agentSnapshots.scratch.sizeLimit` | `80Gi` | Disk-backed `emptyDir` capacity when no PVC is supplied. |
| `agentExperiments.runnerEnabled` | `false` | Enable the experiment runner; requires snapshots. |
| `agentPublisherGateway.enabled` | `false` | Enable the deployment-owned HTTPS publication gateway. |

Snapshot scratch is never memory-backed. A non-root init container creates a
private `0700` child below the disk/PVC mount, and both
`--agent-snapshot-temp-dir` and `TMPDIR` point to that child. One concurrent
seal can temporarily retain an extracted tree, a spool, a canonical archive,
and an upload copy. Size the volume for at least
`4 * agentSnapshots.maxBytes * peak concurrent seals`, plus filesystem, retry,
and multiple-output headroom. The default `80Gi` supports approximately two
simultaneous maximum-size seals before headroom. When `existingClaim` is set,
the PVC's provisioned capacity is authoritative. Helm rejects `web.extraArgs`
or `web.env` entries that would override the managed snapshot path or
`TMPDIR`.

Publisher `volumes` and `volumeMounts` render only when the gateway is enabled.
Helm requires a one-to-one name match, rejects duplicate/unmounted entries,
requires every mount to be read-only, and requires the policy, token, and
optional CA file to be an absolute clean path strictly beneath one of those
mounts. Dedicated publisher mounts remain exclusive to `concourse-web`.

The artifact daemon is the deliberate host-filesystem trust edge. Its pod and
container explicitly run as UID 0 because task outputs on the hostPath can
have arbitrary ownership; the container still drops every capability and adds
back only `DAC_OVERRIDE`, with privilege escalation disabled.

### Storage — Cache PVC

| Parameter | Default | Description |
|-----------|---------|-------------|
| `cachePvc.enabled` | `true` | Enable cache PVC for resource/task caches. |
| `cachePvc.name` | `concourse-cache` | PVC name. |
| `cachePvc.size` | `5Gi` | Storage size. |
| `cachePvc.storageClass` | `""` (cluster default) | Storage class. |
| `cachePvc.accessModes` | `[ReadWriteOnce]` | Access modes. |

### Storage — Artifact Store PVC

| Parameter | Default | Description |
|-----------|---------|-------------|
| `artifactStorePvc.enabled` | `true` | Enable artifact store PVC for cross-pod volume passing. |
| `artifactStorePvc.name` | `concourse-artifacts` | PVC name. |
| `artifactStorePvc.size` | `10Gi` | Storage size (nominal for GCS Fuse). |
| `artifactStorePvc.storageClass` | `""` (cluster default) | Storage class. |
| `artifactStorePvc.accessModes` | `[ReadWriteOnce]` | Access modes. **Use `ReadWriteMany` for multi-replica or concurrent builds.** |

### Storage — GCS Fuse (GKE Only)

| Parameter | Default | Description |
|-----------|---------|-------------|
| `artifactStorePvc.gcsFuse.enabled` | `false` | Enable GCS Fuse-backed artifact store. |
| `artifactStorePvc.gcsFuse.bucketName` | `""` | GCS bucket name. |
| `artifactStorePvc.gcsFuse.onlyDir` | `""` | Restrict mount to subdirectory prefix. |
| `artifactStorePvc.gcsFuse.mountOptions` | `[implicit-dirs]` | Mount options for GCS Fuse driver. |

When enabled, the chart creates a PV + PVC backed by a GCS bucket using
the `gcsfuse.csi.storage.gke.io` CSI driver. The `implicit-dirs` mount
option is recommended for Concourse's tar-based artifact layout.

### PostgreSQL

| Parameter | Default | Description |
|-----------|---------|-------------|
| `postgresql.enabled` | `true` | Deploy bundled PostgreSQL. Set `false` for external DB. |
| `postgresql.image` | `postgres:16` | PostgreSQL image (bundled mode only). |
| `postgresql.database` | `concourse` | Database name. |
| `postgresql.user` | `concourse` | Database user. |
| `postgresql.password` | `concourse` | Database password (plaintext; use `existingSecret` for production). |
| `postgresql.existingSecret` | `""` | K8s Secret name for password. Overrides `password`. |
| `postgresql.passwordSecretKey` | `postgresql-password` | Key in Secret containing the password. |
| `postgresql.host` | `""` | External database host (required when `enabled=false`). |
| `postgresql.port` | `5432` | External database port. |
| `postgresql.socket` | `""` | UNIX domain socket path (alternative to host/port). |
| `postgresql.sslmode` | `disable` | SSL mode: `disable`, `require`, `verify-ca`, `verify-full`. |
| `postgresql.caCert` | `""` | CA cert file path (mount via `web.extraVolumes`). |
| `postgresql.clientCert` | `""` | Client cert file path (for mTLS). |
| `postgresql.clientKey` | `""` | Client key file path (for mTLS). |
| `postgresql.connectTimeout` | `""` | Connection timeout (e.g. `5m`). Empty = binary default. |
| `postgresql.persistence.enabled` | `true` | Enable persistent storage (bundled mode). |
| `postgresql.persistence.size` | `8Gi` | Database storage size. |
| `postgresql.persistence.storageClass` | `""` | Storage class. |

#### PostgreSQL Security Context

| Parameter | Default | Description |
|-----------|---------|-------------|
| `postgresql.podSecurityContext.runAsUser` | `999` | UID for postgres process. |
| `postgresql.podSecurityContext.runAsGroup` | `999` | GID for postgres process. |
| `postgresql.podSecurityContext.fsGroup` | `999` | fsGroup for volume mounts. |
| `postgresql.containerSecurityContext.allowPrivilegeEscalation` | `false` | Prevent privilege escalation. |
| `postgresql.containerSecurityContext.capabilities.drop` | `["ALL"]` | Drop all Linux capabilities. |
| `postgresql.resources` | 250m/256Mi req, 500m/512Mi limit | CPU/memory resources. |

#### External PostgreSQL Example (Cloud SQL)

```yaml
postgresql:
  enabled: false
  host: 10.0.0.3            # Cloud SQL private IP or proxy address
  database: concourse
  user: concourse
  existingSecret: concourse-db-credentials
  passwordSecretKey: password
  sslmode: verify-ca
  caCert: /etc/ssl/cloudsql/server-ca.pem

web:
  extraVolumes:
    - name: cloudsql-certs
      secret:
        secretName: cloudsql-instance-credentials
  extraVolumeMounts:
    - name: cloudsql-certs
      mountPath: /etc/ssl/cloudsql
      readOnly: true
```

### Service

| Parameter | Default | Description |
|-----------|---------|-------------|
| `service.type` | `LoadBalancer` | Service type: `ClusterIP`, `LoadBalancer`, `NodePort`. |
| `service.httpPort` | `8080` | HTTP port. |
| `service.tsaPort` | `2222` | TSA port (unused in JetBridge, kept for compatibility). |
| `service.annotations` | `{}` | Service annotations (e.g. cloud provider LB annotations). |
| `service.labels` | `{}` | Extra labels. |
| `service.loadBalancerIP` | `""` | Static IP for LoadBalancer-type services. |
| `service.loadBalancerSourceRanges` | `[]` | Restrict LoadBalancer to these CIDRs. |

### Ingress

| Parameter | Default | Description |
|-----------|---------|-------------|
| `ingress.enabled` | `false` | Enable ingress resource. |
| `ingress.className` | `""` | Ingress class name. |
| `ingress.annotations` | `{}` | Ingress annotations. |
| `ingress.host` | `""` | Ingress hostname. |
| `ingress.tls` | `[]` | TLS configuration (list of `secretName`/`hosts` entries). |

### RBAC & ServiceAccount

| Parameter | Default | Description |
|-----------|---------|-------------|
| `rbac.create` | `true` | Create Role + RoleBinding for the web pod (pod/exec/log CRUD). |
| `serviceAccount.create` | `true` | Create ServiceAccount for the web pod. |
| `serviceAccount.annotations` | `{}` | ServiceAccount annotations (e.g. GKE Workload Identity). |

The pod-level service account is not ambient: the chart projects a bounded
credential only into `concourse-web`, which is the sole container that talks
to the Kubernetes API.

### Tracing (OpenTelemetry)

| Parameter | Default | Description |
|-----------|---------|-------------|
| `tracing.otlpAddress` | `""` | OTLP gRPC endpoint for traces (e.g. `tempo.monitoring.svc:4317`). |
| `tracing.otlpHeaders` | `{}` | Additional OTLP headers. |
| `tracing.otlpUseTLS` | `false` | Use TLS for OTLP connection. |
| `tracing.serviceName` | `""` | Service name in traces (default: `concourse-web`). |

### Metrics (OpenTelemetry)

| Parameter | Default | Description |
|-----------|---------|-------------|
| `otelMetrics.otlpAddress` | `""` | OTLP gRPC endpoint for metrics. |
| `otelMetrics.otlpHeaders` | `{}` | Additional OTLP headers for metrics. |
| `otelMetrics.otlpUseTLS` | `false` | Use TLS for metrics OTLP connection. |

### Secrets

| Parameter | Default | Description |
|-----------|---------|-------------|
| `secrets.create` | `true` | Auto-generate signing keys. Set `false` for multi-replica. |
| `secrets.signingKeySecret` | `""` | Pre-existing Secret with signing keys (required when `create=false`). |

All web replicas MUST share the same signing keys — sessions fail when
requests hit a replica with different keys.

### Network Policy

| Parameter | Default | Description |
|-----------|---------|-------------|
| `networkPolicy.enabled` | `false` | Enable general web/database/artifact/non-hermetic task policies. |
| `networkPolicy.ingressFrom` | `[]` | Allow ingress from these pod selectors. Empty = allow all. |
| `networkPolicy.taskEgressTo` | `[]` | Egress rules for task pods. Empty = allow all outbound. |
| `networkPolicy.hermeticEgressTo` | `[]` | Complete NetworkPolicy egress rules for `hermetic: true` pods. Empty = fail closed. |
| `artifactDaemon.networkPolicy.enabled` | `false` | Restrict daemon egress to peers and explicitly configured destinations. |
| `artifactDaemon.networkPolicy.dnsEgressTo` | `[]` | DNS destination peers. Empty = no DNS egress. |
| `artifactDaemon.networkPolicy.kubernetesAPIEgressTo` | `[]` | Kubernetes API destination peers. Empty = no API egress. |

The hermetic ingress-and-egress policy is emitted even when
`networkPolicy.enabled` is `false`; that switch controls the chart's general
web, database, artifact, and non-hermetic task policies. Jetbridge labels
hermetic pods from the server-side runtime specification and disables
service-account token automount. Ingress is denied, while containers in the
same pod can still communicate over localhost because NetworkPolicy does not
govern intra-pod loopback traffic. No DNS, cloud metadata, arbitrary HTTPS, or
private-network rule is added implicitly. Use
`networkPolicy.hermeticEgressTo` only for explicit destinations such as a
model egress proxy, and constrain both its peer selector/CIDR and ports.
The cluster CNI must implement Kubernetes NetworkPolicy; otherwise creating
the policy object does not enforce the boundary.

Kubernetes NetworkPolicy has an unavoidable own-node exception: a pod can
reach services on the node where it is running. Jetbridge uses
`status.hostIP:<artifactDaemon.port>` for node-local artifact bootstrap, so
inputs still materialize under the default-empty policy. The same exception
means the main container and sidecars can reach other listening node services;
that can include node-local DNS caches, metadata proxies, or kubelet endpoints.
Standard NetworkPolicy cannot narrow that residual to only the artifact daemon.
Clusters requiring a stronger boundary must add CNI-specific host/firewall
enforcement. NetworkPolicy rules from any other policy selecting the same pod
are also additive and can widen the effective allowlist.

The artifact-daemon policy has exactly one
`NetworkPolicy/<release namespace>/<chart fullname>-artifact-daemon`
identity.
It is emitted when the daemon is enabled and either
`networkPolicy.enabled` or `artifactDaemon.networkPolicy.enabled` is true.
Either switch supplies ingress isolation; the daemon-specific switch also adds
peer-daemon egress. DNS and Kubernetes API access are fail closed unless
operators configure destination-specific NetworkPolicy peers in
`artifactDaemon.networkPolicy.dnsEgressTo` and
`artifactDaemon.networkPolicy.kubernetesAPIEgressTo`; port-only rules,
all-destination CIDRs (`0.0.0.0/0` and `::/0`), and empty selectors are
rejected. For the API, configure the destination seen by your CNI—service
traffic can be matched before or after DNAT. When the GCP preemption watcher is
enabled, the policy additionally allows only
`169.254.169.254/32` on TCP port 80 for the metadata probe. Runtime ingress
selects the actual `concourse.ci/worker` label, covering pipeline, one-off, and
hermetic pods in the release namespace.

### Pod Disruption Budget

| Parameter | Default | Description |
|-----------|---------|-------------|
| `pdb.enabled` | `false` | Enable PDB (only useful when `web.replicas > 1`). |
| `pdb.minAvailable` | `1` | Minimum available pods during disruption. |

### Prometheus Monitoring

| Parameter | Default | Description |
|-----------|---------|-------------|
| `serviceMonitor.enabled` | `false` | Create ServiceMonitor CRD (requires prometheus-operator). |
| `serviceMonitor.interval` | `30s` | Scrape interval. |
| `serviceMonitor.labels` | `{}` | Labels for Prometheus discovery. |
| `serviceMonitor.namespace` | `""` | Namespace for ServiceMonitor. |
| `alertingRules.enabled` | `false` | Create PrometheusRule CRD. |
| `alertingRules.labels` | `{}` | Labels for alert rule discovery. |

## Architecture

```
                         +------------------+
                         |   concourse-web  |
                         |   (Deployment)   |
                         +--------+---------+
                                  |
                    K8s API: create pods, exec, watch
                                  |
            +---------------------+---------------------+
            |                     |                     |
     +------+------+     +-------+-------+     +-------+-------+
     |  task pod   |     |   get pod     |     |   put pod     |
     | (on-demand) |     |  (on-demand)  |     |  (on-demand)  |
     +------+------+     +-------+-------+     +-------+-------+
            |                     |                     |
            +--------- artifact-store PVC --------------+
                    (init containers extract inputs,
                     sidecar uploads outputs as tars)
```

**Artifact passing flow:**

1. Step A runs and produces output in an emptyDir volume.
2. The artifact-helper sidecar tars the emptyDir to the artifact store PVC.
3. Step B's init container extracts the tar from the PVC into its own emptyDir.
4. Step B runs with the extracted data available as input.

The artifact store PVC can use any storage class: hostPath (single-node),
NFS, GCS FUSE, EBS, etc. Multi-node clusters need `ReadWriteMany` access.

## Production Notes

- **Secrets:** Replace `web.localUsers` with OIDC/OAuth via `web.extraArgs`. Generate signing keys externally and set `secrets.create=false`.
- **Database:** Use an external managed database (Cloud SQL, RDS) with `postgresql.enabled=false`.
- **Multi-node:** Set `artifactStorePvc.accessModes: [ReadWriteMany]` and use a storage class that supports it.
- **TLS:** For native HTTPS, set `web.tls.enabled=true` and create a K8s Secret with your cert/key. Alternatively, terminate TLS at the ingress layer with `ingress.enabled=true`.
- **Ingress:** Enable `ingress.enabled=true` with your ingress controller and TLS.
- **Resources:** Tune `web.resources` based on pipeline count. The web node is the control plane and doesn't run builds.
- **Connection pools:** For N replicas, ensure PostgreSQL `max_connections >= N * (apiMaxConns + backendMaxConns + 7)`.
