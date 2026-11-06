# Concourse JetBridge Helm Chart

The artifact daemon, daemon mTLS, signed resolution, and authenticated MCP are
always configured. Supply certificate ownership, a resolve-key Secret and MCP
client registrations before rendering. Hangar remains separately configured.
See [migration instructions](MIGRATING.md) for existing clusters and 0.3.1 upgrades.


Deploys Concourse CI with the JetBridge Kubernetes-native runtime. Instead of
running tasks in Garden containers on dedicated worker VMs, JetBridge creates
Kubernetes pods directly for every pipeline step.

**Key differences from the official Concourse chart:**

- No worker StatefulSet. Task pods are created on-demand by the web node.
- Artifact passing uses a per-node DaemonSet over HTTP (no shared PVC, no SPDY streaming between workers).
- The web node needs RBAC permissions to create pods, exec into containers, and read logs in its namespace.

## Quickstart (k3s)

### 1. Build the image

```bash
./build.sh concourse-local:latest
```

On k3s, the image is available to the cluster automatically when using the
default containerd runtime. For kind, load it with `kind load docker-image`.

### 2. Install

Create `deployment-values.yaml` with the image, URL and users, your explicitly
provisioned references and actual MCP client registrations (the names below
are examples). The chart has no defaults for any of them; a render that leaves
one out fails naming it:

```yaml
image:
  repository: concourse-local
web:
  externalUrl: http://localhost:8080
  localUsers: "test:test"
  mainTeamLocalUser: test
secrets:
  signingKeySecret: jetbridge-session-signing-key
  encryptionKeySecret: jetbridge-encryption-key
  artifactDaemonKeySecret: jetbridge-artifact-daemon-key
artifactDaemon:
  tls:
    source: existingSecret
    existingSecret: jetbridge-daemon-tls
mcp:
  clients:
    - client_id: workstation
      client_name: Workstation
      redirect_uris: ["http://127.0.0.1:8964/callback"]
```

The TLS Secret needs `ca.crt`, `tls.crt`, `tls.key`, `client.crt`, and
`client.key`. The artifact daemon key Secret needs exactly 32 random bytes under
`artifact-daemon.key`: the web signs every resolve capability and Hangar
warrant with it, and every artifact daemon verifies them.
For a live Helm installation, `tls.source: generated` with an empty
`tls.existingSecret` explicitly selects chart-managed certificates instead.

The signing-key Secret holds the session signing key, an RSA private key in
PEM under `session_signing_key`. Every web pod mounts it, so sessions survive
restarts and work across replicas. Create it once:

```bash
kubectl create namespace concourse
concourse generate-key -t rsa -f session_signing_key  # or: openssl genrsa -out session_signing_key 4096
kubectl -n concourse create secret generic jetbridge-session-signing-key \
  --from-file=session_signing_key
```

The encryption-key Secret holds the database encryption key under
`encryption-key`; the web encrypts sensitive columns with it and does not start
without it, and `migrate-db` runs under the same key. Rotate it with
`concourse migrate --encryption-key NEW --old-encryption-key OLD`
([MIGRATING.md](MIGRATING.md)); a lost key is a lost database, so back the Secret up with the database. Create it
once (`openssl rand -hex 16` gives 32 hex characters, a 32-byte key):

```bash
kubectl -n concourse create secret generic jetbridge-encryption-key \
  --from-literal=encryption-key="$(openssl rand -hex 16)"
head -c 32 /dev/urandom > artifact-daemon.key
kubectl -n concourse create secret generic jetbridge-artifact-daemon-key \
  --from-file=artifact-daemon.key
```

Then install:

```bash
helm install concourse ./deploy/chart \
  -f deployment-values.yaml \
  --namespace concourse \
  --set image.tag=latest \
  --set image.pullPolicy=Never \
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
    targetRevision: core
    path: deploy/chart
    helm:
      # Merge the required certificate, resolve Secret, and mcp.clients settings
      # from deployment-values.yaml above into this application's Helm values,
      # and create the signing-key and encryption-key Secrets as in the k3s
      # quickstart.
      # Use source: existingSecret for Argo; never generated certificates.
      valueFiles:
        - values.yaml
      parameters:
        - name: image.repository
          value: ghcr.io/your-org/concourse
        - name: image.tag
          value: latest
        - name: web.externalUrl
          value: https://concourse.example.com
        - name: web.localUsers
          value: admin:change-me
        - name: web.mainTeamLocalUser
          value: admin
        - name: secrets.signingKeySecret
          value: jetbridge-session-signing-key
        - name: secrets.encryptionKeySecret
          value: jetbridge-encryption-key
        - name: secrets.artifactDaemonKeySecret
          value: jetbridge-artifact-daemon-key
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
| `image.repository` | `""` | **Required.** Image repository, e.g. `concourse-local` for a locally loaded image. |
| `image.tag` | `""` (appVersion) | Image tag. |
| `image.pullPolicy` | `IfNotPresent` | Pull policy. Use `Never` for local images on k3s/kind. |
| `image.pullSecrets` | `[]` | Image pull secrets for the web pod. |

### Web

| Parameter | Default | Description |
|-----------|---------|-------------|
| `web.replicas` | `1` | Number of web node replicas. |
| `web.externalUrl` | `""` | **Required.** URL users use to reach the UI. |
| `web.clusterName` | `jetbridge` | Cluster name displayed in the UI. |
| `web.logLevel` | `info` | Log level: `debug`, `info`, `error`. |
| `web.localUsers` | `""` | Local user credentials (`user:password`, comma-separated). The web reads them from the `<fullname>-web-credentials` Secret, not its args. Empty for SSO only. |
| `web.mainTeamLocalUser` | `""` | User granted admin on the main team. The main team needs an owner: set this, or give it one through `web.env` (`CONCOURSE_MAIN_TEAM_OIDC_GROUP`, `CONCOURSE_MAIN_TEAM_GITHUB_ORG`, ...) for an SSO-only deployment. |
| `web.apiMaxConns` | `10` | API connection pool max (per replica). |
| `web.backendMaxConns` | `50` | Backend connection pool max (per replica). |
| `web.terminationGracePeriodSeconds` | `120` | Graceful shutdown timeout. |
| `web.resources` | 100m/256Mi req, 2/2Gi limit | CPU/memory resources. |
| `web.authDuration` | `""` | Access token lifetime (Go duration; empty keeps the binary's 24h). Rendered as `CONCOURSE_AUTH_DURATION`. |
| `web.caCertFile` | `""` | Path of a PEM CA bundle the web trusts for outbound HTTPS, under a `web.extraVolumeMounts` mountPath (the render fails otherwise). Rendered as `SSL_CERT_FILE`. |
| `web.checks.interval` | `""` | Interval between resource checks (default 1m). `CONCOURSE_RESOURCE_CHECKING_INTERVAL`. |
| `web.checks.resourceTypeInterval` | `""` | Interval between resource type checks (default 1m). `CONCOURSE_RESOURCE_TYPE_CHECKING_INTERVAL`. |
| `web.checks.webhookInterval` | `""` | Interval between checks of a resource with a webhook (default 1m). `CONCOURSE_RESOURCE_WITH_WEBHOOK_CHECKING_INTERVAL`. |
| `web.checks.timeout` | `""` | Time limit on one check (default 1h). `CONCOURSE_GLOBAL_RESOURCE_CHECK_TIMEOUT`. |
| `web.gc.oneOffGracePeriod` | `""` | How long a finished one-off build's step pods are kept (default 5m). `CONCOURSE_GC_ONE_OFF_GRACE_PERIOD`. |
| `web.gc.missingGracePeriod` | `""` | How long a container or volume may be missing before it is reaped (default 5m). `CONCOURSE_GC_MISSING_GRACE_PERIOD`. |
| `web.secretRetry.attempts` | `0` | Attempts at a credential lookup that fails retryably (0 keeps the binary's 5). `CONCOURSE_SECRET_RETRY_ATTEMPTS`. |
| `web.secretRetry.interval` | `""` | Wait between those attempts (default 1s). `CONCOURSE_SECRET_RETRY_INTERVAL`. |
| `web.env` | `[]` | Extra environment for the web container (`value` or `valueFrom`). Configures what the chart does not type: auth connectors (`CONCOURSE_OIDC_*`, `CONCOURSE_GITHUB_*`, ...), credential managers (`CONCOURSE_VAULT_*`, ...), Google Cloud Trace. An entry naming a variable a set typed value renders fails the render ([MIGRATING.md](MIGRATING.md#typed-values-and-webenv-2026-10-31-webenv-kept-in-041)). |
| `web.extraVolumeMounts` | `[]` | Additional volume mounts (e.g. CA bundles). |
| `web.extraVolumes` | `[]` | Additional volumes for the web pod. |
| `web.nodeSelector` | `{}` | Node selector for the web pod. |
| `web.tolerations` | `[]` | Tolerations for the web pod. |
| `web.affinity` | `{}` | Affinity rules for the web pod. |

#### Single sign-on only

Leave `web.localUsers` and `web.mainTeamLocalUser` empty and configure the
connector and the main team's owners through `web.env`. The render fails if
the main team ends up with no owner.

```yaml
web:
  env:
    - name: CONCOURSE_OIDC_DISPLAY_NAME
      value: Company SSO
    - name: CONCOURSE_OIDC_ISSUER
      value: https://login.example.com
    - name: CONCOURSE_OIDC_CLIENT_ID
      value: concourse
    - name: CONCOURSE_OIDC_CLIENT_SECRET
      valueFrom:
        secretKeyRef:
          name: concourse-oidc
          key: client-secret
    - name: CONCOURSE_MAIN_TEAM_OIDC_GROUP
      value: platform-admins
```

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
| `kubernetes.namespace` | release namespace | Namespace where step pods are created. The chart renders the artifact daemon's read-only pods Role there, so the namespace must exist at install time. |
| `kubernetes.serviceAccount` | `""` (web SA) | ServiceAccount for task pods. |
| `kubernetes.podStartupTimeout` | `5m` | Max time to wait for pod Running. |
| `kubernetes.podSchedulingTimeout` | `""` (15m) | Max time a step pod may wait Pending for cluster resources. Resolve capability expiry is derived from effective scheduling + startup + 36m. |
| `kubernetes.defaultTaskCPURequest` | `0` | CPU request (millicores) for a task step that names none; 0 leaves such pods BestEffort. |
| `kubernetes.baseResourceTypes` | `{}` | Base resource type images, `name: image`. Overrides a built-in type (`git`, `registry-image`, ...) or adds one, e.g. `git-with-ado`. Renders `--kubernetes-base-resource-type=name=image` per entry. |
| `kubernetes.preferredStepNode` | `""` | Node label `key=value` every step pod prefers but does not require. Step pods also tolerate the taint `key=value:NoSchedule`, so tainting the node reserves it for builds. |
| `kubernetes.imagePullSecrets` | `[]` | Pull secrets for task pod images. |
| `kubernetes.artifactHelperImage` | `alpine:3.24.4@sha256:93b41ef40b1677fce8fcc64ff4af09daddfacf5040afde618bc46c1e2d1cc7d4` | Image for init containers and sidecar. Must have `tar`. Pinned by digest; mirror it for a cluster that cannot reach Docker Hub. |
| `kubernetes.stepRuntime` | `exec` | How a step's command reaches its pod. `exec`: a pause pod the web execs into (needs `pods/exec`). `job`: one suspended `batch/v1` Job per step with the command in the pod spec, run under the jb-launcher from the jetbridge image, so admission control sees every command; adds `batch/jobs` to the Role and `tokenreviews` to the ClusterRole. A Run-owned exact execution still uses exec in this release, so `pods/exec` stays granted in both. |
| `kubernetes.imageRegistrySecret` | `""` | Pull secret name for resource type images. |

### Storage

The chart creates no artifact PVC — the only PersistentVolumeClaim it renders
belongs to the bundled PostgreSQL. Artifacts live under `artifactDaemon.hostPath`
on each node and move between nodes over HTTP; task caches are node-local
directories or ephemeral emptyDirs.

| Parameter | Default | Description |
|-----------|---------|-------------|
| `cacheStore` | `""` | Task cache backend: `hostpath` or `emptydir`. Empty auto-detects from `cacheHostPath`. |
| `cacheHostPath` | `""` | Node directory for `hostpath` task caches. Empty falls back to emptyDir. |
| `artifactDaemon.tls.source` | `existingSecret` | Certificate ownership: `existingSecret` or live-Helm `generated`. mTLS is mandatory. |
| `artifactDaemon.tls.existingSecret` | `""` | Required certificate Secret in existing-secret mode. |
| `mcp.clients` | `[]` | Required public OAuth client registrations with exact redirects. |
| `mcp.disabledOperations` | `[]` | Operation IDs refused regardless of user consent. |
| `artifactDaemon.hostPath` | `/var/concourse/artifacts` | Node directory the daemon stores artifacts in. |
| `artifactDaemon.port` | `7780` | Port the daemon serves on. |
| `artifactDaemon.ttl` | `2h` | How long an artifact is retained before the daemon sweeps it. |
| `artifactDaemon.hangar.capabilityTTL` | `900s` | Lifetime of every warrant the web signs, in positive whole seconds, at most 900s. |
| `artifactDaemon.outputScratch.path` | `/var/concourse/hangar-output-scratch` | The one canonicalization scratch: a daemon-only `emptyDir`, disjoint from `hostPath`. |
| `artifactDaemon.outputScratch.sizeLimit` | `""` | Required with `hangarOutput.executionControl.enabled`; at least `concurrency` times `maxContentBytes`. |
| `artifactDaemon.outputScratch.maxContentBytes` | `10737418240` | Maximum regular-file content in one tree (capture, input publication or managed read); rendered as `--output-max-content-bytes`. |
| `artifactDaemon.outputScratch.maxEntries` | `100000` | Maximum filesystem entries in one tree; rendered as `--output-max-entries`. |
| `artifactDaemon.outputScratch.concurrency` | `1` | How many trees may be spooled to scratch at once. |

`artifactDaemon` also carries `mirror`, `tls` and `networkPolicy`
blocks; see [`values.yaml`](values.yaml) for those.

Hangar has one output namespace and one tree path: a Run input
is uploaded as an input publication into the output namespace
(`hangarOutput.bucket`) and reaches the task as a managed read of it. There
is no separate input store. Native GCS authenticates with Workload Identity;
S3-compatible and filesystem stores are unsupported.

The artifact daemon key (`secrets.artifactDaemonKeySecret`, exactly 32 raw
bytes under `artifact-daemon.key`) also signs every warrant the web issues --
read, execution control and output capture -- and every artifact daemon
verifies against it. With `hangarOutput.executionControl.enabled` the web is
also handed it as `--kubernetes-hangar-key` and the daemon as `--hangar-key`,
the same file as their resolve key. The key is mounted only into web and
artifact-daemon Pods, never task Pods. The Run input signing key
(`web.runInputSigningKeySecret`) is web-only and must be a different Secret.
See
[`docs/hangar.md`](../../docs/hangar.md) for rollout order, sizing, security,
and fail-closed behavior.

The opt-in artifact-daemon NetworkPolicy includes both current GKE Workload
Identity metadata profiles: standard/Calico (GKE 1.21+) at
`169.254.169.252/32` TCP 987/988 and Dataplane V2 at
`169.254.169.254/32` TCP 80/8080. Enforcement and routing depend on the
cluster CNI; validate this allowlist for the target environment. The obsolete
pre-1.21 loopback profile is not included.

### PostgreSQL

| Parameter | Default | Description |
|-----------|---------|-------------|
| `postgresql.enabled` | `true` | Deploy bundled PostgreSQL. Set `false` for external DB. |
| `postgresql.image` | `postgres:16` | PostgreSQL image (bundled mode only). |
| `postgresql.database` | `concourse` | Database name. |
| `postgresql.user` | `concourse` | Database user. |
| `postgresql.password` | `concourse` | Database password (plaintext in values; use `existingSecret` for production). The web and migrate-db read it from the `<fullname>-web-credentials` Secret, not their args. |
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

### Tracing (OpenTelemetry)

| Parameter | Default | Description |
|-----------|---------|-------------|
| `tracing.otlpAddress` | `""` | OTLP gRPC endpoint for traces (e.g. `tempo.monitoring.svc:4317`). |
| `tracing.otlpHeaders` | `{}` | Additional OTLP headers. |
| `tracing.otlpUseTLS` | `false` | Use TLS for OTLP connection. |
| `tracing.serviceName` | `""` | Service name in traces (default: `concourse-web`). |

Google Cloud Trace (Stackdriver) is the other trace exporter: set
`CONCOURSE_TRACING_STACKDRIVER_PROJECTID` through `web.env`. With both set,
OTLP wins.

### Metrics (OpenTelemetry)

| Parameter | Default | Description |
|-----------|---------|-------------|
| `otelMetrics.otlpAddress` | `""` | OTLP gRPC endpoint for metrics. |
| `otelMetrics.otlpHeaders` | `{}` | Additional OTLP headers for metrics. |
| `otelMetrics.otlpUseTLS` | `false` | Use TLS for metrics OTLP connection. |

### Secrets

| Parameter | Default | Description |
|-----------|---------|-------------|
| `secrets.signingKeySecret` | `""` | **Required.** Pre-existing Secret holding `session_signing_key`, an RSA private key in PEM. Every web pod mounts it at `/keys`. |
| `secrets.artifactDaemonKeySecret` | `""` | **Required.** Pre-existing Secret holding `artifact-daemon.key`, exactly 32 random bytes. The web signs every resolve capability and Hangar warrant with it; every artifact daemon verifies them. The chart never generates it. |
| `secrets.encryptionKeySecret` | `""` | **Required.** Pre-existing Secret holding `encryption-key`, the 16- or 32-byte database encryption key. Rendered as `CONCOURSE_ENCRYPTION_KEY` on the web container and on `migrate-db`. A deployment that already sets `CONCOURSE_ENCRYPTION_KEY` through `web.env` may keep that until it moves; the chart refuses both at once. |

The chart never generates either key. Every web replica mounts the same
signing-key Secret, so sessions survive restarts and work across replicas, and
reads the same encryption key. There is no key rotation: a cluster that needs a
different encryption key restores the database from a backup taken under it.

### Network Policy

| Parameter | Default | Description |
|-----------|---------|-------------|
| `networkPolicy.enabled` | `false` | Enable NetworkPolicy resources. |
| `networkPolicy.ingressFrom` | `[]` | Allow ingress from these pod selectors. Empty = allow all. |
| `networkPolicy.taskEgressTo` | `[]` | Egress rules for task pods. Empty = allow all outbound. The policy renders in the release namespace, so it confines task pods only when `kubernetes.namespace` is unset or names that namespace. |

### Pod Disruption Budget

| Parameter | Default | Description |
|-----------|---------|-------------|
| `pdb.enabled` | `false` | Enable PDB (only useful when `web.replicas > 1`). |
| `pdb.minAvailable` | `1` | Minimum available pods during disruption. |

### Prometheus Monitoring

The ATC serves Prometheus metrics on a dedicated listener, never on the 8080
web port -- `/metrics` there returns the UI's HTML. `metrics.enabled` is what
opens that listener, so `serviceMonitor.enabled` requires it and the render
fails otherwise rather than producing a target that scrapes a web page.

The artifact daemon's own port is mTLS, which a Prometheus scraper cannot
complete. `artifactDaemon.metrics.port` opens a second, plain-HTTP listener on
each daemon that serves `/metrics` and nothing else; with
`serviceMonitor.enabled` it gets its own ServiceMonitor (one target per node,
labelled `node`), and with `alertingRules.enabled` its own rules. It is empty
by default because it renders `--metrics-port`, which a daemon image older than
the flag exits on: set it once the image carrying the flag is running.

| Parameter | Default | Description |
|-----------|---------|-------------|
| `metrics.enabled` | `true` | Expose Prometheus metrics on a dedicated port. |
| `metrics.port` | `9391` | Port for the metrics listener. |
| `artifactDaemon.metrics.port` | `""` | Plain-HTTP metrics-only listener on the artifact daemon, e.g. `9392`. Empty: none, and nothing scrapes the daemon. |
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
            +------ artifact-daemon (DaemonSet, hostPath) ------+
                    (init containers fetch inputs over HTTP;
                     outputs are stored on the node and mirrored)
```

**Artifact passing flow:**

1. Step A runs and produces output in an emptyDir volume.
2. The `artifact-daemon` on that node stores the output under its hostPath and
   mirrors it to peer nodes per `artifactDaemon.mirror`.
3. Step B's init container resolves which node holds the artifact and fetches
   it over authenticated HTTPS.
4. Step B runs with the fetched data available as input.

Nothing is shared between nodes, so no `ReadWriteMany` storage class is needed.
The chart always deploys the artifact daemon and wires its host path into web.

## Production Notes

- **Secrets:** Add OIDC/OAuth through `web.env` (`CONCOURSE_OIDC_*` and the like); keep any `web.localUsers` to strong credentials, or leave it empty for SSO only. The signing-key Secret named in `secrets.signingKeySecret` is required; generate the key once, outside the chart.
- **Database:** Use an external managed database (Cloud SQL, RDS) with `postgresql.enabled=false`.
- **TLS:** For native HTTPS, set `web.tls.enabled=true` and create a K8s Secret with your cert/key. Alternatively, terminate TLS at the ingress layer with `ingress.enabled=true`.
- **Ingress:** Enable `ingress.enabled=true` with your ingress controller and TLS.
- **Resources:** Tune `web.resources` based on pipeline count. The web node is the control plane and doesn't run builds.
- **Connection pools:** For N replicas, ensure PostgreSQL `max_connections >= N * (apiMaxConns + backendMaxConns + 7)`.
