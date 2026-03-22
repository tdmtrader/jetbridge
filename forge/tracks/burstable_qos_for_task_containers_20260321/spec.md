# Spec: Burstable QoS for Task Containers

**Track ID:** `burstable_qos_for_task_containers_20260321`
**Type:** feature

## Overview

Task containers currently only support Guaranteed QoS or BestEffort QoS. When `container_limits` are set, `buildResourceRequirements()` copies limits into requests (`reqs.Requests = res`), making them identical. There is no way to set requests independently from limits, which prevents Burstable QoS — where requests < limits allows the scheduler to bin-pack more pods while still capping resource usage.

Sidecars already support independent requests/limits via the `SidecarResources` model. This track extends the same capability to task containers.

## Requirements

1. Users can set `container_requests` independently from `container_limits` in task config YAML.
2. When only `container_limits` is set (no requests), behavior is unchanged — requests equal limits (Guaranteed QoS).
3. When only `container_requests` is set (no limits), only requests are set on the K8s container (Burstable QoS with no cap).
4. When both are set, each maps independently to K8s requests and limits (Burstable QoS).
5. CLI default flags `--default-task-cpu-request` and `--default-task-memory-request` are added alongside existing limit defaults.
6. Plan-level `container_requests` can override task-config-level requests (same merge semantics as limits).

## Technical Approach

### YAML Surface

```yaml
# Task config
container_limits:
  cpu: 2048
  memory: 4GB
container_requests:
  cpu: 512
  memory: 1GB
```

### Files to Modify

| File | Change |
|------|--------|
| `atc/container_limits.go` | Add `ContainerRequests` struct (reuse `CPULimit`/`MemoryLimit` types) |
| `atc/task.go` | Add `Requests *ContainerLimits` field to `TaskConfig` |
| `atc/plan.go` | Add `Requests *ContainerLimits` to `TaskPlan` and `RunPlan` |
| `atc/steps.go` | Add `Requests` to `TaskStep` pipeline config |
| `atc/runtime/types.go` | Add `CPURequest`/`MemoryRequest` to `ContainerLimits` |
| `atc/exec/task_step.go` | Propagate requests through to `containerSpec`, apply defaults |
| `atc/exec/task_config_source.go` | Merge requests in `OverrideContainerLimitsSource` |
| `atc/worker/jetbridge/container.go` | Update `buildResourceRequirements()` to handle independent requests |
| `atc/atccmd/command.go` | Add `--default-task-cpu-request` and `--default-task-memory-request` flags |
| `atc/engine/step_factory.go` | Pass default requests through factory |
| `atc/builds/planner.go` | Wire `Requests` from step to plan |

## Acceptance Criteria

- [ ] Task containers can be configured with independent requests and limits
- [ ] Omitting requests preserves current behavior (requests = limits, Guaranteed QoS)
- [ ] Omitting limits with requests-only sets Burstable QoS with no cap
- [ ] CLI default request flags work and are overridden by task-level config
- [ ] Plan-level requests override task-config requests (same as limits merge)
- [ ] Unit tests cover all QoS combinations
- [ ] Existing container_limits tests continue to pass unchanged

## Out of Scope

- Changing the sidecar resources model (already supports this)
- Validation that requests <= limits (K8s API server handles this)
- Resource quotas or LimitRange integration
- `fly` CLI display changes (can be a follow-up)
