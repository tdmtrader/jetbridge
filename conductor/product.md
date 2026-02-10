# Product: Concourse CI — JetBridge Edition

## Overview

Concourse is an open-source, general-purpose CI/CD automation system ("the continuous thing-doer"). The JetBridge fork extends Concourse along two primary axes:

1. **Kubernetes-native runtime** — Replaces the legacy worker architecture (Garden/containerd, Baggageclaim volumes, TSA registration) with direct Kubernetes pod execution, native volume management, and modern observability. This work is largely complete.

2. **Agent-first workflows** — Extends Concourse's declarative DAG pipeline model to support autonomous AI agent execution as a first-class concern. Agents operate as long-running, decision-making participants within pipelines — able to observe context, invoke tools, and drive multi-step work — while the DAG still governs ordering, resource flow, and structural guarantees.

## Target Users

1. **Platform Engineers** — Teams running CI/CD infrastructure on Kubernetes who want Concourse's declarative pipeline model without managing bespoke worker VMs or containers.
2. **DevOps Teams** — Organizations already invested in Concourse who want to modernize their runtime layer to leverage Kubernetes for scheduling, scaling, and resource management.
3. **SREs / Operators** — Engineers responsible for reliability who need production-grade observability (structured logging, distributed tracing, metrics) and resilient task execution.

## Goals

### Epic 1: Kubernetes-Native Runtime (largely complete)
- Execute Concourse tasks and resources as Kubernetes pods instead of Garden containers.
- Manage volumes through Kubernetes PVCs and ephemeral storage rather than Baggageclaim.
- Register workers dynamically via Kubernetes API rather than TSA SSH tunnels.
- Support pod-to-pod volume passing for pipeline step continuity.

### Epic 2: Agent-First Workflows
- **Agent step type** — A new step primitive that runs an AI agent as a long-lived pod, with access to tools, context from prior steps, and the ability to produce structured outputs for downstream steps.
- **DAG-honoring autonomy** — Agents operate within the pipeline's dependency graph. The DAG defines when an agent runs, what inputs it receives, and what outputs it must produce. Agents have autonomy _within_ their step; the DAG governs the _between_.
- **Tool and MCP integration** — Agents can invoke tools (shell commands, API calls, MCP servers) as part of their execution. Tool access is declared in the step config and enforced at runtime.
- **Context passing** — Pipeline artifacts, metadata, and prior step outputs flow into agent steps as structured context. Agent outputs flow back into the DAG as artifacts for downstream consumption.
- **Observability** — Agent reasoning traces, tool invocations, and decision points are captured as OpenTelemetry spans, giving full visibility into what an agent did and why.
- **Human-in-the-loop checkpoints** — Agents can pause execution to request human approval before proceeding, integrating with Concourse's existing manual trigger and approval mechanisms.

### Production Hardening
- Structured logging throughout the runtime layer.
- Distributed tracing via OpenTelemetry for end-to-end pipeline visibility.
- Prometheus-compatible metrics for pod lifecycle, volume operations, and worker health.
- Pod resilience — graceful handling of evictions, OOM kills, and node failures.
- Security hardening — pod security contexts, RBAC, network policies.

### Developer Experience
- Readable, deterministic pod names for easy debugging (`<pipeline>-<job>-<step>-<hash>`).
- `fly hijack` support for Kubernetes pods (exec into running containers).
- Pod watch API for real-time status streaming.
- E2E and integration test coverage for the Kubernetes runtime.

## Features (Implemented / In Progress)

| Feature | Status |
|---------|--------|
| **K8s-Native Runtime** | |
| K8s Worker, Container, Volume, Registrar | Done |
| Pod execution (exec mode) | Done |
| Step-to-step volume passing | Done |
| Pod resilience & security contexts | Done |
| Hijack support (fly exec into pods) | Done |
| Volume caching via PVCs | Done |
| Task step sidecar containers | Done |
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
| **Agent-First Workflows** | |
| Agent step type (pipeline primitive) | Planned |
| Agent context passing (artifacts in/out) | Planned |
| Tool / MCP server integration | Planned |
| Agent observability (OTel traces) | Planned |
| Human-in-the-loop checkpoints | Planned |
| Agent step DAG integration | Planned |
| **CI Agent Pipeline** | |
| Agent code review (ci-agent-review) | Planned |
| Agent fix step (ci-agent-fix) | Planned |
| Review → Fix → PUT → PR pipeline | Planned |
| Human feedback on agent reviews (Elm UI + PostgreSQL) | Planned |

## Success Metrics

### K8s-Native Runtime
- All existing Concourse pipeline behaviors work correctly on the K8s runtime.
- E2E and integration tests pass reliably.
- Observability stack (logs, traces, metrics) provides full pipeline visibility.
- No regression in pipeline execution time compared to containerd runtime.

### Agent-First Workflows
- Agent steps execute within the DAG — respecting ordering, inputs, and outputs — without requiring users to abandon declarative pipelines.
- Agent tool invocations and reasoning are fully observable via OpenTelemetry traces.
- Existing non-agent pipelines are unaffected (full backwards compatibility).
- Agent steps can be mixed freely with traditional get/task/put steps in the same pipeline.
