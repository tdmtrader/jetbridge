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

```bash
helm install concourse ./deploy/chart \
  --namespace concourse --create-namespace \
  --set image.repository=concourse-local \
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

## Configuration

All parameters are documented in [`values.yaml`](values.yaml). Key sections:

### Image

| Parameter | Default | Description |
|-----------|---------|-------------|
| `image.repository` | `concourse-local` | Docker image repository |
| `image.tag` | `""` (appVersion) | Image tag |
| `image.pullPolicy` | `IfNotPresent` | Pull policy (`Never` for local images) |

### Web

| Parameter | Default | Description |
|-----------|---------|-------------|
| `web.replicas` | `1` | Number of web replicas |
| `web.externalUrl` | `http://localhost:8080` | URL users use to reach the UI |
| `web.localUsers` | `test:test` | Local user credentials |
| `web.resources` | 100m/256Mi req, 2/2Gi limit | CPU/memory resources |

### Service

| Parameter | Default | Description |
|-----------|---------|-------------|
| `service.type` | `LoadBalancer` | Service type: `ClusterIP`, `LoadBalancer`, `NodePort` |
| `service.httpPort` | `8080` | HTTP service port |
| `service.tsaPort` | `2222` | TSA port (unused in JetBridge, kept for compatibility) |
| `service.annotations` | `{}` | Annotations for the service (e.g. AWS LB cert ARNs) |
| `service.labels` | `{}` | Extra labels for the service |
| `service.loadBalancerIP` | `""` | Static IP for LoadBalancer-type services |
| `service.loadBalancerSourceRanges` | `[]` | Restrict LoadBalancer traffic to these CIDRs |

### TLS

| Parameter | Default | Description |
|-----------|---------|-------------|
| `web.tls.enabled` | `false` | Enable native HTTPS on the web container |
| `web.tls.bindPort` | `443` | HTTPS listen port |
| `web.tls.secretName` | `concourse-web-tls` | K8s Secret containing TLS cert and key |
| `web.tls.mountPath` | `/concourse-tls` | Mount path for the TLS secret |
| `web.tls.certFilename` | `tls.crt` | Key in the Secret for the certificate |
| `web.tls.keyFilename` | `tls.key` | Key in the Secret for the private key |

When TLS is enabled, the web binary is started with `--tls-bind-port`, `--tls-cert`,
and `--tls-key`. An HTTPS port is added to both the container and the service. The
TLS Secret is mounted read-only into the web pod.

### Extra Volumes

| Parameter | Default | Description |
|-----------|---------|-------------|
| `web.extraVolumeMounts` | `[]` | Additional volume mounts for the web container |
| `web.extraVolumes` | `[]` | Additional volumes for the web pod |

Use these for custom CA bundles, credential files, or any other operator-specific mounts.

### Kubernetes Runtime

| Parameter | Default | Description |
|-----------|---------|-------------|
| `kubernetes.namespace` | release namespace | Namespace for task pods |
| `kubernetes.podStartupTimeout` | `5m` | Max time to wait for pod Running |
| `kubernetes.artifactHelperImage` | `alpine:latest` | Image for init containers and sidecar |
| `kubernetes.imagePullSecrets` | `[]` | Pull secrets for task pod images |

### Storage

| Parameter | Default | Description |
|-----------|---------|-------------|
| `cachePvc.enabled` | `true` | Enable cache PVC for resource/task caches |
| `cachePvc.size` | `5Gi` | Cache PVC size |
| `artifactStorePvc.enabled` | `true` | Enable artifact store PVC for cross-pod volume passing |
| `artifactStorePvc.size` | `10Gi` | Artifact store PVC size |
| `artifactStorePvc.accessModes` | `[ReadWriteOnce]` | Use `ReadWriteMany` for multi-node clusters |

### PostgreSQL

| Parameter | Default | Description |
|-----------|---------|-------------|
| `postgresql.enabled` | `true` | Deploy bundled PostgreSQL (set `false` for external DB) |
| `postgresql.image` | `postgres:16` | PostgreSQL image (bundled mode only) |
| `postgresql.host` | `""` | External database host (required when `enabled=false`) |
| `postgresql.port` | `5432` | External database port |
| `postgresql.database` | `concourse` | Database name |
| `postgresql.user` | `concourse` | Database user |
| `postgresql.password` | `concourse` | Database password (plaintext) |
| `postgresql.existingSecret` | `""` | K8s Secret name for password (overrides `password`) |
| `postgresql.passwordSecretKey` | `postgresql-password` | Key in the Secret containing the password |
| `postgresql.sslmode` | `disable` | SSL mode: `disable`, `require`, `verify-ca`, `verify-full` |
| `postgresql.caCert` | `""` | Path to CA cert file (mount via `web.extraVolumes`) |
| `postgresql.clientCert` | `""` | Path to client cert file (for mTLS) |
| `postgresql.clientKey` | `""` | Path to client key file (for mTLS) |
| `postgresql.connectTimeout` | `""` | Connection timeout (e.g. `5m`); empty = binary default |
| `postgresql.socket` | `""` | UNIX domain socket path (alternative to host/port) |
| `postgresql.persistence.size` | `8Gi` | Database storage size (bundled mode only) |

#### External PostgreSQL Example

```yaml
postgresql:
  enabled: false
  host: mydb.rds.amazonaws.com
  database: concourse
  user: concourse
  existingSecret: concourse-db-credentials
  passwordSecretKey: password
  sslmode: verify-full
  caCert: /etc/ssl/rds/rds-ca.pem

web:
  extraVolumes:
    - name: rds-ca
      secret:
        secretName: rds-ca-cert
  extraVolumeMounts:
    - name: rds-ca
      mountPath: /etc/ssl/rds
      readOnly: true
```

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
