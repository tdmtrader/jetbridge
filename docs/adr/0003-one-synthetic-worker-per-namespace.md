---
status: accepted
date: 2026-02-08
---

# One synthetic worker per namespace

Concourse schedules on workers, and upstream registers one worker per
Garden host. JetBridge runs every step as a Kubernetes pod and has no
per-node agent to register, so the web node registers and heartbeats a
single synthetic worker per namespace, named after the namespace, standing
in for every node of the cluster. Registering each Kubernetes node as a
worker was rejected: nodes come and go under autoscaling, the scheduler
would fight the Kubernetes scheduler over placement, and the artifact
daemon already gives the web node-level affinity through the artifact
locator without making nodes a scheduling unit.

## Consequences

- `fly workers` shows one worker per namespace. Worker-level operations
  (land, retire, prune) mean little and are mostly no-ops.
- Placement is Kubernetes's job. The web only expresses affinity for the
  node that holds a step's inputs.
- Container and volume records exist for compatibility; the reaper and the
  artifact daemon, not worker GC, own their lifecycle.
