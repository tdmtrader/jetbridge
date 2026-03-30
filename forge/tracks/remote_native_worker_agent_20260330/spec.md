# Spec: Remote Native Worker Agent

**Track ID:** `remote_native_worker_agent_20260330`
**Type:** feature

## Overview

The co-located native worker requires the web process to run on macOS for darwin tasks. This track adds a standalone gRPC agent binary that runs on a Mac and accepts task execution from a web process running anywhere — enabling the standard deployment (web in K8s) alongside darwin build capacity.

## Requirements

1. A standalone `native-agent` binary that listens on a gRPC port and executes commands locally
2. Web-side runtime.Worker implementation that proxies execution to the remote agent
3. Artifact streaming: K8s volumes → web → agent (StreamIn) and agent → web (StreamOut)
4. Authentication (token for dev, mTLS for production)
5. Multi-agent support with health checking and stalled worker detection

## Technical Approach

- gRPC protocol with 5 RPCs: Exec (server-streams stdout/stderr + exit code), Kill, StreamIn (client-streams tar), StreamOut (server-streams tar), Ping
- DB lifecycle stays on the web side; agent is stateless between requests
- Exec stream stays open until process exits — no separate Wait RPC
- Agent duplicates ~20 lines of mergeEnv/path resolution rather than extracting a shared package prematurely
- See `docs/native-worker-remote-design.md` for full protocol definition and architecture

## Acceptance Criteria

- [ ] `fly execute` runs a `platform: darwin` task on a remote Mac with web in K8s
- [ ] Brine pipeline: K8s get → artifact streams to remote agent → darwin cargo build succeeds
- [ ] Darwin task outputs stream back to web for downstream steps
- [ ] Unauthenticated connections are rejected when auth is configured
- [ ] Multiple agents: kill one, new builds route to the other
- [ ] `fly workers` shows remote native agents with correct platform/arch
- [ ] Co-located `--native-worker` continues to work unchanged (verified in Phase 1)

## Out of Scope

- NAT traversal (agent-connects-to-web reverse mode)
- Container isolation, images, cgroups on the agent
- Agent scheduling logic (web decides what runs where)
- Resource type execution on native workers (get/put stay on K8s)
