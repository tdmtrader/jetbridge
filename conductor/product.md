# Product: Concourse CI — JetBridge Edition

## Overview

Concourse is an open-source, general-purpose CI/CD automation system ("the continuous thing-doer"). The JetBridge fork extends Concourse with a **Kubernetes-native runtime** that replaces the legacy worker architecture (Garden/containerd, Baggageclaim volumes, TSA registration) with direct Kubernetes pod execution, native volume management, and modern observability.

## Target Users

1. **Platform Engineers** — Teams running CI/CD infrastructure on Kubernetes who want Concourse's declarative pipeline model without managing bespoke worker VMs or containers.
2. **DevOps Teams** — Organizations already invested in Concourse who want to modernize their runtime layer to leverage Kubernetes for scheduling, scaling, and resource management.
3. **SREs / Operators** — Engineers responsible for reliability who need production-grade observability (structured logging, distributed tracing, metrics) and resilient task execution.

## Goals

### Primary: Kubernetes-Native Runtime
- Execute Concourse tasks and resources as Kubernetes pods instead of Garden containers.
- Manage volumes through Kubernetes PVCs and ephemeral storage rather than Baggageclaim.
- Register workers dynamically via Kubernetes API rather than TSA SSH tunnels.
- Support pod-to-pod volume passing for pipeline step continuity.

### Secondary: Production Hardening
- Structured logging throughout the runtime layer.
- Distributed tracing via OpenTelemetry for end-to-end pipeline visibility.
- Prometheus-compatible metrics for pod lifecycle, volume operations, and worker health.
- Pod resilience — graceful handling of evictions, OOM kills, and node failures.
- Security hardening — pod security contexts, RBAC, network policies.

### Tertiary: Developer Experience
- Readable, deterministic pod names for easy debugging (`<pipeline>-<job>-<step>-<hash>`).
- `fly hijack` support for Kubernetes pods (exec into running containers).
- Pod watch API for real-time status streaming.
- E2E and integration test coverage for the Kubernetes runtime.

## Features (Implemented / In Progress)

| Feature | Status |
|---------|--------|
| K8s Worker, Container, Volume, Registrar | Done |
| Pod execution (exec mode) | Done |
| Step-to-step volume passing | Done |
| Pod resilience & security contexts | Done |
| Hijack support (fly exec into pods) | Done |
| Volume caching via PVCs | Done |
| OpenTelemetry tracing | Done |
| Prometheus metrics | Done |
| Pod watch API | Done |
| Integration tests | Done |
| E2E tests | Done |
| Readable pod names | Done |
| Legacy Garden/Baggageclaim/TSA removal | Done |
| Production deployment guides | Planned |
| Helm chart for JetBridge runtime | Planned |
| Multi-cluster worker support | Planned |

## Success Metrics

- All existing Concourse pipeline behaviors work correctly on the K8s runtime.
- E2E and integration tests pass reliably.
- Observability stack (logs, traces, metrics) provides full pipeline visibility.
- No regression in pipeline execution time compared to containerd runtime.
