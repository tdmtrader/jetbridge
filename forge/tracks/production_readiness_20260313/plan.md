# Implementation Plan: Production Readiness Hardening

> **Plan reconciliation (2026-05-31):** This plan was written 2026-03-13 and left
> at 77%. A review against current `main` found Phase 2.3 (Tracing) largely
> superseded by `telemetry_simplification_20260331` (completed; commit
> `0a387b942c` removed low-value spans to cut trace volume ~85%):
> - "Add check span in `scanner.go`" → **DROPPED** (conflicts — telemetry_simplification
>   deliberately removed scanner spans; re-adding regresses that work).
> - "Standardize `defer tracing.End(span, err)`" → **already done** (all 6 jetbridge
>   span sites use it).
> - `build_id`/`pod_name` attrs → already done (`container.go:115-116`).
> - "Image-resolution span in `resolver.go`" → **decision needed** (`[~]`): net-new,
>   but a check-path span against the simplification ethos.
> Phase 2.2 metrics remain valid (metrics weren't touched), with one label caveat.
> Phase 1 (chart) + Phase 2.4 (alerts) remain fully valid.

## Phase 1: Helm Chart Hardening

### 1.1 Pod Security Contexts
- [x] Write tests: helm template test asserting securityContext on web and PG containers —
      added `deploy/chart/tests/securitycontext_test.go` (renders chart, asserts web
      runAsNonRoot + readOnlyRootFilesystem + drop ALL, postgres runAsUser/fsGroup 999 +
      drop ALL). Skips gracefully if `helm` is not on PATH. Both tests pass.
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
- [x] Verify RBAC is sufficient by running integration tests against a real cluster.
      VERIFIED 2026-05-31 against `theborg` (k3s, runs concourse.home):
      • CHART RBAC IS SUFFICIENT — `deploy/chart/templates/rbac.yaml` grants the full
        pod lifecycle (pods, pods/exec, pods/log) + deployments + endpointslices in the
        namespaced Role, and `nodes: get` + `secrets: get` via a bound ClusterRole. Live
        `kubectl auth can-i` confirms pods/exec/log = yes; live cluster actively runs
        pipeline pods. Acceptance criterion met by the chart.
      • LIVE-CLUSTER DRIFT (operational, not a chart bug): theborg was deployed ~113d ago
        and its RBAC is stale — the `concourse-web` ClusterRole is ORPHANED (no
        ClusterRoleBinding) and predates `nodes: get`. Effect: `registerDaemonAlias` logs
        a recurring `nodes "theborg" is forbidden` warning (harmless on single-node, but
        would break cross-node artifact routing on a multi-node cluster); cluster-scoped
        `secrets get` is also denied. REMEDIATION: `helm upgrade` theborg to apply the
        current chart RBAC. No chart change required.

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
- [x] Add detailed comments in values.yaml `artifactStorePvc` section about RWO vs RWX tradeoffs pending — NOTE (2026-05-31): these comments are now STALE (the backend was removed from the engine; see below).
- DROPPED (obsolete, 2026-05-31): ~~Add example values block for GCS Fuse configuration.~~
      The engine REMOVED the PVC/GCS-Fuse artifact store — `--kubernetes-artifact-store-claim`
      no longer exists in the ATC (removed in `be1a204f49`), and `validateK8sRuntime`
      enforces the DaemonSet artifact daemon as the required, sole backend. The
      `artifactStorePvc.gcsFuse` values block + `pvc.yaml` GCS branch are dead config.
      Documenting it would encourage a removed feature.
- DROPPED (obsolete, 2026-05-31): ~~Add example values block for NFS-based RWX.~~
      Same reason — RWX shared storage was replaced by DaemonSet host-path replication;
      not supported by the engine.
- ⚠️ RELATED BUG (out of scope here; belongs to `deprecate_pvc_and_spdy_artifact_backends_20260327`):
      chart default `artifactStorePvc.enabled: true` makes a fresh `helm install` emit the
      removed `--kubernetes-artifact-store-claim` flag → web fails to start (parser rejects
      unknown flags). Works today only because deployments set `artifactStorePvc.enabled=false`.

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
- [x] Write tests for new metric emissions (unit test metric recording calls) — added
      to `atc/metric/otel_metrics_test.go` for `volume_operation_duration` (records with
      `op` attr; distinguishes `stream_in` vs `initialize`). Metric suite 54/54 green.
- [x] Add `concourse.k8s.pod_failures` counter pending (labels: reason=OOMKilled|Evicted|Error) in `atc/worker/jetbridge/process.go`
- [x] Add `concourse.k8s.volume_operation_duration` histogram in `atc/worker/jetbridge/volume.go`.
      DONE (2026-05-31) with CORRECTED labels: `op=stream_in|initialize` only — `stream_out`
      dropped (deprecated; cross-node reads are daemon-mediated via DaemonSetVolume, out of
      this type's scope, noted in the RecordVolumeOperationDuration doc). Instrumented
      `StreamIn` (stream_in) + the three `Initialize*` methods (initialize).
- [x] Add `concourse.resource.check_duration` histogram pending in `atc/lidar/scanner.go`
- [x] Add `concourse.worker.heartbeat_age` gauge pending (seconds since last successful registration) in `atc/worker/jetbridge/registrar.go`

### 2.3 Tracing Enhancements
- [x] Add `build_id` and `pod_name` attributes to pod lifecycle spans pending in `atc/worker/jetbridge/container.go` and `process.go` — confirmed present (`container.go:115-116`).
- DROPPED (decision 2026-05-31, by request): ~~Add tracing span around image
      resolution in `atc/imageresolver/resolver.go`.~~ `resolver.go` has no spans today;
      adding one is a check-path span against the `telemetry_simplification_20260331`
      ethos. Decision: do NOT add it — stay consistent with the simplification. Image
      resolution latency, if needed, is better captured as a metric than a span.
- DROPPED (obsolete, 2026-05-31): ~~Add tracing span for resource check
      trigger-to-completion in `atc/lidar/scanner.go`.~~ Directly CONFLICTS with
      `telemetry_simplification_20260331` (commit `0a387b942c`), which removed the
      `scanner.Run` and check-bookkeeping spans from `scanner.go` as <1ms noise.
      Re-adding would regress the ~85% trace-volume reduction. `scanner.go` has 0
      spans today, by design. Check timing is covered by the `concourse.resource.check_duration`
      metric instead (already added).
- [x] Standardize span.End() pattern: use `defer tracing.End(span, err)` consistently
      across jetbridge package — ALREADY DONE. All 6 span sites (container.go:119,236;
      executor.go:70; process.go:72,741; volume.go:170) use `defer func() { tracing.End(span, spanErr) }()`.

### 2.4 Alerting Rules Template
- [x] Create `deploy/chart/templates/prometheusrule.yaml` pending gated by `alertingRules.enabled`
- [x] Rules: ImagePullFailureRateHigh (>5 in 5m), PodStartupSlow (p95 >60s), WorkerHeartbeatStale (>60s), DBConnectionPoolExhausted (active >= max).
      DONE (2026-05-31): added `ConcourseWorkerHeartbeatStale` (`expr: concourse_worker_heartbeat_age > 60`,
      for 2m, severity warning) — completes all 4 rules. `helm template` renders all four.
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
