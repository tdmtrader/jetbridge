# Implementation Plan: Production Readiness Hardening

## Phase 1: Helm Chart Hardening

### 1.1 Pod Security Contexts
- [ ] Write tests: helm template test asserting securityContext on web and PG containers
- [x] Add `securityContext` to web container in `deploy/chart/templates/web-deployment.yaml` pending (runAsNonRoot, allowPrivilegeEscalation: false, capabilities.drop: ALL, readOnlyRootFilesystem: true with tmpfs for /tmp)
- [x] Add `securityContext` to PostgreSQL container in `deploy/chart/templates/postgresql.yaml` pending (runAsUser: 999, fsGroup: 999, allowPrivilegeEscalation: false)
- [x] Add `web.securityContext` and `postgresql.securityContext` to `deploy/chart/values.yaml` with production defaults pending

### 1.2 Network Policies
- [x] Create `deploy/chart/templates/networkpolicy.yaml` gated by `networkPolicy.enabled` pending
- [x] Policy: allow ingress to web pod on ports 8080/2222 only; deny all other ingress pending
- [x] Policy: allow egress from web pod to PostgreSQL and K8s API pending; restrict task pod egress per user config
- [x] Add `networkPolicy` section to `deploy/chart/values.yaml` pending

### 1.3 Pod Disruption Budget
- [x] Create `deploy/chart/templates/pdb.yaml` gated by `web.replicas > 1` pending
- [x] Set `minAvailable: 1` by default pending, configurable via `web.pdb.minAvailable`
- [x] Add `web.pdb` section to `deploy/chart/values.yaml` pending

### 1.4 RBAC Scoping
- [x] Update `deploy/chart/templates/rbac.yaml` Role rules pending to use `resourceNames` or label-based restrictions where K8s API supports it
- [x] Add PVC permissions (get, list, create, delete) for cache and artifact PVCs pending
- [ ] Verify RBAC is sufficient by running integration tests against a real cluster

### 1.5 Graceful Shutdown & Startup
- [x] Add `terminationGracePeriodSeconds: 120` to web deployment pod spec pending
- [x] Add startup probe to web deployment pending (httpGet /api/v1/info, initialDelaySeconds: 5, periodSeconds: 5, failureThreshold: 30 = 150s window)
- [x] Add `web.startupProbe` to values.yaml pending

### 1.6 Signing Key Management
- [x] Add `secrets.signingKeySecret` value pending — name of pre-existing Secret containing session_signing_key, tsa_host_key, worker_key
- [x] When `secrets.create: false` and `secrets.signingKeySecret` is set, mount that Secret instead of emptyDir + init container pending
- [x] Update init container to skip key generation when secret is mounted pending

### 1.7 Connection Pool Configuration
- [x] Add `web.apiMaxConns` (default: 10) and `web.backendMaxConns` (default: 50) to values.yaml pending
- [x] Wire values into web container args as `--api-max-conns` and `--backend-max-conns` pending
- [x] Add comment in values.yaml pending: "For N replicas, ensure PostgreSQL max_connections >= N * (apiMaxConns + backendMaxConns + 7)"

### 1.8 Artifact Volume Documentation
- [x] Add detailed comments in values.yaml `artifactStorePvc` section about RWO vs RWX tradeoffs pending
- [ ] Add example values block for GCS Fuse configuration
- [ ] Add example values block for NFS-based RWX

### 1.9 Prometheus ServiceMonitor
- [x] Create `deploy/chart/templates/servicemonitor.yaml` gated by `serviceMonitor.enabled` pending
- [x] Configure scrape of Prometheus metrics port pending (requires adding `--prometheus-bind-port` to web args when enabled)
- [x] Add `serviceMonitor` section to values.yaml pending (enabled, interval, labels, namespace)

- [ ] Task: Phase 1 Manual Verification — `helm template` with production values produces valid, secure manifests

---

## Phase 2: Observability Gaps

### 2.1 Health Endpoint
- [x] Write tests for `/api/v1/health` endpoint pending (DB up → 200, DB down → 503, no workers → 503)
- [x] Implement health endpoint in `atc/api/infoserver/` pending that checks: DB ping, worker count > 0, optional component heartbeat
- [x] Register route in `atc/api/accessor/handler.go` pending (unauthenticated, like /api/v1/info)
- [x] Wire readiness probe to `/api/v1/health` pending in `deploy/chart/templates/web-deployment.yaml`

### 2.2 Missing Metrics
- [ ] Write tests for new metric emissions (unit test metric recording calls)
- [x] Add `concourse.k8s.pod_failures` counter pending (labels: reason=OOMKilled|Evicted|Error) in `atc/worker/jetbridge/process.go`
- [ ] Add `concourse.k8s.volume_operation_duration` histogram (labels: op=stream_in|stream_out|initialize) in `atc/worker/jetbridge/volume.go`
- [x] Add `concourse.resource.check_duration` histogram pending in `atc/lidar/scanner.go`
- [x] Add `concourse.worker.heartbeat_age` gauge pending (seconds since last successful registration) in `atc/worker/jetbridge/registrar.go`

### 2.3 Tracing Enhancements
- [x] Add `build_id` and `pod_name` attributes to pod lifecycle spans pending in `atc/worker/jetbridge/container.go` and `process.go`
- [ ] Add tracing span around image resolution in `atc/imageresolver/resolver.go`
- [ ] Add tracing span for resource check trigger-to-completion in `atc/lidar/scanner.go`
- [ ] Standardize span.End() pattern: use `defer tracing.End(span, err)` consistently across jetbridge package

### 2.4 Alerting Rules Template
- [x] Create `deploy/chart/templates/prometheusrule.yaml` pending gated by `alertingRules.enabled`
- [ ] Rules: ImagePullFailureRateHigh (>5 in 5m), PodStartupSlow (p95 >60s), WorkerHeartbeatStale (>60s), DBConnectionPoolExhausted (active >= max)
- [x] Add `alertingRules` section to values.yaml pending

- [ ] Task: Phase 2 Manual Verification — health endpoint returns correct status; new metrics appear in /metrics

---

## Phase 3: Cleanup

### 3.1 Document Custom Resource Replacement
- [x] Add values.yaml comments documenting that native K8s `imagePullSecrets` replaces custom registry-image resources for authenticated pulls (GCR, ECR, ACR) with examples

### 3.2 Artifact Volume Strategy Documentation
- [x] Comprehensive RWO/RWX/GCS Fuse documentation already in values.yaml `artifactStorePvc` section — no separate doc needed

### 3.3 Clean Up artifact_input_step.go
- Descoped: tracked by upstream issue #3607, not a production readiness concern

### 3.4 Clean Up gc/destroyer.go
- [x] Update misleading TODO comments in `atc/gc/destroyer.go` — interface IS used by jetbridge/reaper, no conversion needed

### 3.5 Fix Misleading Comments
- [x] Remove `// XXX: Deprecated, only used in tests` from `atc/db/resource.go:FindVersion` (it's used by scheduler)
- [x] Update `// XXX: This is only begin used by tests` on `atc/db/team.go:CreateOneOffBuild` to accurately describe usage

- [x] Task: Phase 3 Manual Verification — go build/vet clean, health tests pass, helm template renders all manifests

---
