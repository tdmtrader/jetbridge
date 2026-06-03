# Spec: Production Readiness Hardening

**Track ID:** `production_readiness_20260313`
**Type:** feature

## Overview

The JetBridge K8s-native runtime is functionally complete and running on a local cluster. Before a team of ~10 developers relies on it in production, the deployment surface needs hardening: Helm chart security, observability completeness, scaling correctness, and legacy cleanup.

Agentic workflows are explicitly out of scope — they will be tested, validated, and deployed separately.

## Requirements

### Phase 1: Helm Chart Hardening
1. Add pod security contexts to web and PostgreSQL deployments (`runAsNonRoot`, capability drops, `readOnlyRootFilesystem` where applicable, `allowPrivilegeEscalation: false`).
2. Add a NetworkPolicy template restricting ingress to the web pod and egress from task pods.
3. Add a PodDisruptionBudget template for the web deployment (when replicas > 1).
4. Scope RBAC role to labeled task/check pods instead of wildcard pod access.
5. Add `terminationGracePeriodSeconds` (120s) to web deployment for in-flight build draining.
6. Add a startup probe to web deployment (longer timeout than liveness for initial DB migration).
7. Add support for pre-generated signing keys via existing Secret (disable init-container key generation when `secrets.create: false` and a secret name is provided).
8. Add Helm values for connection pool sizing (`web.apiMaxConns`, `web.backendMaxConns`) with production-safe defaults and document the relationship to PostgreSQL `max_connections`.
9. Default `artifactStorePvc.accessModes` comment/documentation to warn about RWO limitations with multiple replicas; add example for RWX / GCS Fuse.
10. Add Prometheus ServiceMonitor template (optional, gated by `serviceMonitor.enabled`).

### Phase 2: Observability Gaps
11. Add a `/api/v1/health` endpoint that checks: DB connectivity, at least one registered worker, and component coordinator liveness.
12. Wire the health endpoint into readiness probe (keep liveness on `/api/v1/info`).
13. Add missing Prometheus/OTel metrics: pod failure/eviction counter, volume operation duration histogram, resource check latency histogram, worker heartbeat gauge.
14. Add tracing spans for: build-to-pod correlation (build_id attribute on pod lifecycle spans), image resolution (registry lookups), and resource check trigger-to-completion.
15. Add Prometheus alerting rules template (PrometheusRule CRD, optional) for: high image pull failure rate, pod startup > 60s, worker heartbeat stale > 60s, DB connection pool exhaustion.

### Phase 3: Cleanup
16. Verify custom image resource replacement: document that native K8s `imagePullSecrets` / `ServiceAccount` replaces custom registry-image resource for GCR auth.
17. Document artifact volume strategy: RWO limitations, RWX recommendation, GCS Fuse setup for GKE.
18. Clean up `artifact_input_step.go` TODO (#3607) — extract volume lookup behind runtime abstraction.
19. Clean up `gc/destroyer.go` — convert from interface to concrete struct, move `FindDestroyingVolumesForGc` to volume repository.
20. Remove or update misleading deprecation comments (`resource.go:FindVersion` XXX comment, `team.go:CreateOneOffBuild` XXX comment).

## Acceptance Criteria

- [ ] `helm template` produces valid manifests with security contexts, network policies, PDB, and scoped RBAC
- [ ] `/api/v1/health` returns 200 when DB + worker are healthy, 503 otherwise
- [ ] Readiness probe uses `/api/v1/health`; liveness stays on `/api/v1/info`
- [ ] New metrics visible in Prometheus/OTel when tracing is enabled
- [ ] Build spans include `build_id` and `pod_name` attributes
- [ ] Connection pool flags are exposed in Helm chart and documented
- [ ] ServiceMonitor template renders correctly when enabled
- [ ] All unit tests pass (`make test-unit`)
- [ ] Integration tests pass (`make test-integration`)
- [ ] `artifact_input_step.go` no longer imports `atc/db` directly for volume lookup
- [ ] `gc/destroyer.go` is a concrete struct with no unused interface

## Out of Scope

- Agentic workflows (agent step type, MCP integration, agent observability)
- Multi-cluster worker support
- Helm chart for external PostgreSQL HA (users should use managed DB; chart supports `postgresql.enabled: false`)
- Grafana dashboard ConfigMaps (can be added later; ServiceMonitor is the foundation)
- Three-level custom resource type chain fix (known K8s limitation, rare in practice)
