# Concourse JetBridge Helm Chart

Deploys Concourse CI with the JetBridge Kubernetes-native runtime. Instead of
running tasks in Garden containers on dedicated worker VMs, JetBridge creates
Kubernetes pods directly for every pipeline step.

**Key differences from the official Concourse chart:**

- No worker StatefulSet. Task pods are created on-demand by the web node.
- Artifact passing goes through a per-node artifact DaemonSet over node-local storage (no shared RWX volume, no SPDY streaming between workers).
- The web node needs RBAC permissions to create pods and exec into containers in its namespace.

## Managed agent execution broker

`agentBroker.enabled` optionally enables synchronous `request_review` and
`consult_agent` MCP calls for schema-v3 agent nodes. ATC owns a static exact
profile catalog and injects a broker as a managed pod companion; the chart
does not create a broker Deployment or Service. The feature requires durable
agent snapshots, an exact digest-pinned broker image, an existing capability
key Secret, and provider credential Secret coordinates.

Codex 0.146.0 and Claude Code 2.1.212 are the supported harnesses. Cursor is
present in the image but profile admission rejects it because no verified CLI
control disables all repository instructions and MCP configuration. The
companion uses non-root/read-only-root/drop-all security settings and a
fail-closed Landlock filesystem boundary. Managed nodes require Linux 6.2+
with Landlock ABI 3+ and a compatible RuntimeDefault seccomp profile. Reviews
are static and always record that tests were not run.

`agentBroker.networkPolicy.egress` selects the complete managed task pod,
including the parent agent; Kubernetes cannot apply NetworkPolicy to one
container. Loopback remains available, and egress allow rules are the union of
all policies selecting the pod. Operators must audit that complete union and
include ATC, DNS when needed, provider APIs, and parent-agent destinations.

See [Agent execution broker](../../docs/agent-execution-broker.md) for image
pins, profile examples, credential handling, inspection, and rollout gates.

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

ArgoCD renders Helm charts without a live-cluster `lookup`, so every Secret the
chart generates is re-minted on each reconciliation. Point
`secrets.signingKeySecret`, `artifactDaemon.tls.existingSecret`, and
`artifactDaemon.resolveCapability.existingSecret` at Secrets you manage outside
the chart; see the [GitOps caveat](#gitops-caveat) for what each one breaks when
it churns. Leaving them unset is correct only for a throwaway cluster.

Create the shared Secrets once before syncing the Application:

```bash
kubectl create namespace concourse --dry-run=client -o yaml | kubectl apply -f -
kubectl -n concourse create secret generic concourse-web-signing-key \
  --from-file=session_signing_key=<(openssl genrsa 4096)
kubectl -n concourse create secret generic concourse-artifact-daemon-resolve-capability \
  --from-literal=resolve.key="$(openssl rand -base64 24)"
```

The artifact-daemon TLS CA is a keypair rather than a single value, so it has no
one-liner here; generate it however you manage internal CAs and hand the Secret
name to `artifactDaemon.tls.existingSecret`.

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
        - name: secrets.signingKeySecret
          value: concourse-web-signing-key
        - name: artifactDaemon.resolveCapability.existingSecret
          value: concourse-artifact-daemon-resolve-capability
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

If daemon TLS is enabled, also provision its five documented certificate keys
outside the chart and set `artifactDaemon.tls.existingSecret`; otherwise a
clusterless render generates a different CA and certificates on every refresh.

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
| `kubernetes.serviceAccount` | `""` (task namespace default) | ServiceAccount for task pods. Set it explicitly only when tasks intentionally require Kubernetes API access. |
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

An empty `kubernetes.serviceAccount` leaves the task pod ServiceAccount
unset, so Kubernetes selects the task namespace's default ServiceAccount. Set
an explicit account only for tasks that intentionally call the Kubernetes API,
and bind only the RBAC those tasks require. This does not select or grant the
web ServiceAccount.

The web pod sets `automountServiceAccountToken: false`. Within that pod, only
`concourse-web` receives the chart-managed projected Kubernetes API credential at
the standard service-account path: a token with a one-hour maximum lifetime,
the namespace CA, and the current namespace file. The key-generation,
snapshot-scratch preparation, and database-migration init containers do not
receive that credential. The `web-kubernetes-api-access` and
`snapshot-scratch` volume names are reserved; Helm rejects attempts to
re-mount either through `web.extraVolumeMounts`.

Task and check pods are separate from the web pod. Ordinary non-hermetic pods
follow Kubernetes token-automount behavior for their selected (or default)
ServiceAccount; hermetic pods disable token automount.

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
| `agentPublisher.enabled` | `false` | Enable exact-policy outbound publication inside ATC; requires durable snapshots. |
| `agentPublisher.policySecret.name` | `""` | Dedicated Secret containing the human-reviewed publisher policy. |
| `agentPublisher.policySecret.key` | `policy.json` | Policy key mounted at the chart-owned policy path. |
| `agentPublisher.credentialSecret.name` | `""` | Dedicated Secret containing only the destination-scoped credentials mapped below. |
| `agentPublisher.credentials` | `[]` | Exact `reference`, Secret `key`, and clean relative `path` mappings beneath the credential root. |
| `agentPublisher.directGit.enabled` | `false` | Enable direct branch and trunk publication. |
| `agentPublisher.pullRequests.enabled` | `false` | Enable provider-native PR publication and the server-owned polling monitor; PR-only configuration is supported. |
| `agentPublisher.pullRequests.resourceImage` | `""` | Exact lowercase digest-pinned `forge-pr` resource image; required when pull requests are enabled. |
| `agentPublisher.pullRequests.pollInterval` | `5m` | Positive provider observation polling cadence. |
| `agentPublisher.pullRequests.freshnessInterval` | `6h` | Positive pending-review refresh cadence; must be no shorter than the polling cadence. |
| `agentPublisher.requestTimeout` | `30s` | Positive Go duration no greater than one hour. |
| `agentPublisher.leaseDuration` | `5m` | Go duration greater than `requestTimeout` and no greater than 24 hours. |

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

When enabled, the chart mounts the policy and credentials read-only only into
`concourse-web`, at chart-owned paths; neither Secret reaches migration,
workers, agents, or sidecars. Policy and credential Secrets must be distinct,
the credential mappings must be unique and non-overlapping, and each mapping
must obey Kubernetes AtomicWriter path bounds. The publisher Secrets cannot be
aliased through extra volumes, environment references, image-pull settings,
Ingress TLS, PostgreSQL, artifact-daemon, web TLS, or signing configuration.

Direct Git can publish branches or already-rebased changes to trunk.
Provider-native pull requests can be enabled independently, so a PR-only
publisher does not require direct Git. Provider, destination, and credential
reference authority stays solely in the mounted publisher policy; chart values
only enable the adapter, pin its resource image, and set polling/freshness
cadences. With the Kubernetes credential manager enabled, the
credential-manager namespace prefix must be nonempty and the release namespace
must be outside the `<namespacePrefix><team>` pipeline-variable namespace set.

The artifact daemon is the deliberate host-filesystem trust edge. Its pod and
container explicitly run as UID 0 because task outputs on the hostPath can
have arbitrary ownership; the container still drops every capability and adds
back only `DAC_OVERRIDE`, with privilege escalation disabled.

### Storage — DaemonSet and Node-Local Caches

| Parameter | Default | Description |
|-----------|---------|-------------|
| `cacheStore` | `""` (auto) | Task cache backend: `hostpath` or `emptydir`. Auto uses the artifact DaemonSet's node-local storage. |
| `cacheHostPath` | `""` | Optional separate node-local directory for persistent task caches. |
| `artifactDaemon.enabled` | `true` | Deploy the required per-node artifact daemon. Must remain enabled for the Kubernetes runtime. |
| `artifactDaemon.port` | `7780` | Daemon HTTP/host port. |
| `artifactDaemon.hostPath` | `/var/concourse/artifacts` | Node-local artifact and default cache root. |
| `artifactDaemon.ttl` | `2h` | Retention period for daemon-managed artifacts. |
| `artifactDaemon.mirror.replicas` | `2` | Total desired copies, including the local copy; `all` mirrors to every peer. |
| `artifactDaemon.mirror.concurrency` | `4` | Maximum concurrent mirror jobs per daemon. |
| `artifactDaemon.mirror.timeout` | `5m` | Per-peer mirror timeout. |
| `artifactDaemon.tls.enabled` | `false` | Enable HTTPS and mutual TLS between web and daemon peers. |
| `artifactDaemon.networkPolicy.enabled` | `false` | Restrict daemon ingress to Concourse components. |

The current runtime no longer supports the retired cache/artifact PVC flags.
Artifacts live on the producing node's host path and are served and replicated
by the DaemonSet. Downstream pods resolve inputs through the local daemon,
which fetches a peer copy when the artifact originated on another node.

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

Within the web pod, the web ServiceAccount credential is not ambient: the
chart projects it only into `concourse-web`, the sole container in that pod
that talks to the Kubernetes API. Task and check pod behavior is governed by
`kubernetes.serviceAccount` and the runtime rules described above.

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
| `secrets.create` | `true` | Create and manage the `<fullname>-keys` Secret holding the session signing key. Ignored when `signingKeySecret` is set. |
| `secrets.signingKeySecret` | `""` | Pre-existing Secret with signing keys. Takes precedence over `create`. Leaving it empty with `create=false` selects the single-replica `emptyDir` fallback described below. |

All web replicas MUST share the same signing keys — sessions fail when
requests hit a replica with different keys.

With `secrets.create=true` the chart mints one RSA key, stores it in
`<fullname>-keys` (annotated `helm.sh/resource-policy: keep`), and mounts that
Secret into every web pod. Subsequent renders reuse the stored key instead of
reminting it, so `web.replicas > 1` works and sessions survive pod restarts and
upgrades. Only when `create=false` **and** `signingKeySecret` is empty does the
chart fall back to a per-pod `emptyDir` key generated by an init container;
that mode is single-replica and drops all sessions on every restart.

<a id="gitops-caveat"></a>
**GitOps caveat — applies to three Secrets, not one.** Reuse of every
chart-generated secret depends on Helm's `lookup`, which returns nothing when
the chart is rendered without a cluster connection. Tools that only run
`helm template` (Argo CD without the Helm hook/plugin path,
`helm template | kubectl apply`) generate *new* material on every render and
churn the Secret. Two consecutive cluster-less renders of this chart really do
differ. Set the corresponding `existingSecret` value in those pipelines and
manage the material outside the chart:

| Value | Secret | What churn breaks |
|-------|--------|-------------------|
| `secrets.signingKeySecret` | session signing key | Every session is invalidated; multi-replica web nodes disagree. |
| `artifactDaemon.tls.existingSecret` | daemon CA + server/client certs | Rendered whenever `artifactDaemon.tls.enabled` (implied by `agentSnapshots.enabled`). Neither the ATC nor the daemon reloads TLS material, so a re-applied Secret does nothing until a pod restarts — then that pod trusts a CA the others do not and every ATC↔daemon mTLS call fails, until both the web Deployment and the whole DaemonSet have rolled. |
| `artifactDaemon.resolveCapability.existingSecret` | resolve capability signing key | Rendered whenever `artifactDaemon.enabled`, which the Kubernetes runtime requires — so this one churns on *every* GitOps deployment, TLS or not. Both sides load the key once at start; after the first asymmetric restart, capabilities minted by the other side are rejected and `fetch-inputs` init containers fail closed. |

Argo CD caches generated manifests per source revision, so the churn is per new
commit / cache expiry / repo-server restart rather than per reconcile — with
`selfHeal: true` each regeneration is applied automatically. `helm install`
and `helm upgrade` print a warning listing whichever of these Secrets is being
chart-generated.

The chart-generated daemon Secrets carry `helm.sh/resource-policy: keep` so a
`helm uninstall`/reinstall cycle finds the same material. That annotation only
prevents deletion: it does **not** stop `helm upgrade` or an Argo CD sync from
applying a freshly minted Secret over the old one. Only `existingSecret` does.

**Do not commit rendered manifests.** The same cluster-less render puts a live
4096-bit RSA private key into the Secret's `data` as ordinary base64, so
`helm template > manifests.yaml` in a rendered-manifests-in-git workflow
publishes a usable signing key to version control (and every `helm diff` shows
the Secret changing). `secrets.signingKeySecret` avoids this too: with it set,
the chart renders no Secret at all.

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
traffic can be matched before or after DNAT. Runtime ingress
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
| `web.metrics.enabled` | `false` | Start the ATC's Prometheus exposition listener on its own container port. Required by the web ServiceMonitor. |
| `web.metrics.bindIP` | `0.0.0.0` | Listen address for that listener. |
| `web.metrics.port` | `9391` | Metrics port. Must not collide with 8080, 2222, or `web.tls.bindPort`. |
| `serviceMonitor.enabled` | `false` | Create ServiceMonitor CRD (requires prometheus-operator). |
| `serviceMonitor.interval` | `30s` | Scrape interval. |
| `serviceMonitor.labels` | `{}` | Labels for Prometheus discovery. |
| `serviceMonitor.namespace` | `""` | Namespace for ServiceMonitor. |
| `serviceMonitor.web.enabled` | `true` | Scrape the web node. Requires `web.metrics.enabled`; rendering fails otherwise. |
| `serviceMonitor.artifactDaemon.enabled` | `true` | Also scrape the artifact daemon's `/metrics`. Requires `serviceMonitor.enabled` and `artifactDaemon.enabled`. |
| `serviceMonitor.artifactDaemon.interval` | `""` (inherits) | Scrape interval override for the daemon. |
| `serviceMonitor.artifactDaemon.tlsConfig` | `{}` | Endpoint `tlsConfig`, used only when `artifactDaemon.tls.enabled=true`. |
| `serviceMonitor.scrapeFrom` | `[]` | NetworkPolicy peers allowed to scrape the daemon and web metrics ports. Required when a NetworkPolicy and the corresponding ServiceMonitor are both enabled. |
| `alertingRules.enabled` | `false` | Create PrometheusRule CRD. |
| `alertingRules.labels` | `{}` | Labels for alert rule discovery. |

Two ServiceMonitors are rendered: one selecting the web Service and one
selecting the artifact-daemon headless Service, whose per-node pods export
resolve latency, peer-fetch counts, and snapshot operation counts and bytes.

**The web node serves no `/metrics` on its API port.** Concourse exposes the
Prometheus text format only from a dedicated listener that the ATC starts when
it is given *both* `--prometheus-bind-ip` and `--prometheus-bind-port`; there is
no `/metrics` route on 8080. Worse, an unmatched path on 8080 falls through to
the web UI handler, which answers HTTP 200 with `text/html` — so a scrape aimed
there is not a clean 404 but a target that fails to parse, and `curl`ing it
looks healthy. Set `web.metrics.enabled=true` to render those flags, a `metrics`
container port, and a matching `metrics` Service port; the web ServiceMonitor
selects that port by name and refuses to render without it. Set
`serviceMonitor.web.enabled=false` to scrape only the daemon.

The endpoint is unauthenticated, which is why it is opt-in and on its own port.
When `networkPolicy.enabled` is set, the metrics port is opened to the same
peers as the API; if `networkPolicy.ingressFrom` narrows those peers, the chart
requires `serviceMonitor.scrapeFrom` to name the Prometheus peers as well,
rather than letting every scrape be dropped silently.

The `alertingRules` PrometheusRule draws on three different pipelines, and an
alert pointed at the wrong one evaluates over an empty vector forever with no
error anywhere. `concourse_k8s_*`, `concourse_db_*` and `concourse_workers_*`
come from the web listener above and need `web.metrics.enabled`.
`concourse_agent_*` are OTel instruments the ATC only pushes over OTLP, so they
need `otelMetrics.otlpAddress` (or a collector wired outside the chart); their
names carry the unit suffix the OTLP-to-Prometheus translation appends.
`artifact_daemon_*` come from the daemon scrape. `helm install`/`upgrade` prints
a warning when `alertingRules.enabled` is set without the corresponding source.

Two alerts have no directly equivalent instrument.
`ConcourseDBConnectionPoolExhausted` compares `concourse_db_connections` against
the values that size the pools (`web.apiMaxConns`, `web.backendMaxConns`, or the
binary's own defaults when those are unset), because nothing publishes a pool
maximum series. `ConcourseWorkerStalled` replaces an alert on
`concourse_worker_heartbeat_age`, a declared OTel gauge with no production call
site: it fires on `concourse_workers_registered{state="stalled"}`, which the
worker collector really does emit when a worker stops re-registering.

Under `artifactDaemon.tls.enabled=true` the daemon serves that same port over
HTTPS, so the daemon endpoint switches to `scheme: https`. `/metrics` is one of
the routes the daemon leaves outside client-certificate enforcement
(`VerifyClientCertIfGiven`), so a scraper needs no client cert — but it does
have to accept the server cert, and the chart-generated CA is self-signed with
SANs covering the headless Service rather than the pod IPs Prometheus targets.
The chart therefore defaults that endpoint to `insecureSkipVerify: true`. To
verify properly, set `serviceMonitor.artifactDaemon.tlsConfig` to a full
prometheus-operator `tlsConfig` (CA/cert/key secret refs plus `serverName`);
note that prometheus-operator resolves those Secret references in the
Prometheus instance's namespace, not the Concourse release namespace.

If a chart NetworkPolicy is enabled (`networkPolicy.enabled` or
`artifactDaemon.networkPolicy.enabled`), the daemon policy restricts ingress on
the daemon port, which would silently drop every scrape. Because that port also
serves the artifact API, the chart refuses to open it to all peers: rendering
fails until `serviceMonitor.scrapeFrom` names the Prometheus peers (e.g. a
`namespaceSelector` for the monitoring namespace) — or the daemon
ServiceMonitor is disabled. The web metrics port is a dedicated port serving
only the exposition format, so it simply follows `networkPolicy.ingressFrom`;
the same `serviceMonitor.scrapeFrom` peers are added when that list is
non-empty (and required, for the same reason).

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
            +---------------------+---------------------+
                                  |
                         +--------+---------+
                         | node hostPath    |
                         +--------+---------+
                                  |
                         +--------+---------+     +--------------+
                         | artifact daemon  |<--->| peer daemons |
                         +------------------+     +--------------+
```

**Artifact passing flow:**

1. Step A writes output into a node-local hostPath managed by JetBridge.
2. The node's artifact daemon registers the output and asynchronously mirrors it to peers.
3. Step B's init container asks its local daemon to resolve each input; the daemon fetches from the source node or a mirror when needed.
4. Step B starts with the resolved hostPath mounted at the declared input path.

No RWX PersistentVolumeClaim is required. For multi-node resilience, schedule
the DaemonSet on every eligible build node and keep
`artifactDaemon.mirror.replicas` greater than one.

## Production Notes

- **Secrets:** Replace `web.localUsers` with OIDC/OAuth via `web.extraArgs`. `secrets.create=true` is safe for multi-replica; supply `secrets.signingKeySecret` instead when the key is managed externally or the deployment is rendered without a cluster connection.
- **Database:** Use an external managed database (Cloud SQL, RDS) with `postgresql.enabled=false`.
- **Multi-node:** Run the artifact DaemonSet on every eligible build node and set `artifactDaemon.mirror.replicas` for the desired failure tolerance.
- **TLS:** For native HTTPS, set `web.tls.enabled=true` and create a K8s Secret with your cert/key. Alternatively, terminate TLS at the ingress layer with `ingress.enabled=true`.
- **Ingress:** Enable `ingress.enabled=true` with your ingress controller and TLS.
- **Resources:** Tune `web.resources` based on pipeline count. The web node is the control plane and doesn't run builds.
- **Connection pools:** For N replicas, ensure PostgreSQL `max_connections >= N * (apiMaxConns + backendMaxConns + 7)`.
