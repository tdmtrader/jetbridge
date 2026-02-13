# Spec: Inline Sidecar Definition in Pipeline Config

## Overview

Currently, task step sidecars are defined exclusively via file references
(e.g., `sidecars: ["my-repo/ci/sidecars/postgres.yml"]`). This requires
the sidecar definition to live in a fetched artifact, which is inconvenient
for simple, self-contained sidecars.

This feature adds the ability to define sidecars inline in the pipeline YAML,
alongside the existing file-reference approach.

## Requirements

1. The `sidecars` field on a task step accepts a mixed list of:
   - **String** — file path in `SOURCE/FILE` format (existing behavior)
   - **Object** — inline `SidecarConfig` (name, image, env, ports, etc.)

2. Inline sidecar objects use the same schema as sidecar YAML files
   (`name`, `image`, `command`, `args`, `env`, `ports`, `resources`,
   `workingDir`).

3. Inline and file-referenced sidecars can be mixed in the same list.

4. Validation rules apply uniformly:
   - `name` and `image` are required on every sidecar (inline or from file)
   - Reserved names (`main`, `artifact-helper`) are rejected
   - Duplicate names across inline + file sidecars are rejected

5. Inline sidecars receive the same volume mounts and security context as
   file-referenced sidecars (no behavioral difference at runtime).

## Example Pipeline YAML

```yaml
jobs:
- name: integration-tests
  plan:
  - get: code-repo
  - task: run-tests
    file: code-repo/ci/test.yml
    sidecars:
    - name: postgres
      image: postgres:15
      env:
      - name: POSTGRES_PASSWORD
        value: test
      ports:
      - containerPort: 5432
    - name: redis
      image: redis:7-alpine
    - code-repo/ci/sidecars/custom-service.yml   # file ref still works
```

## Acceptance Criteria

- [ ] `fly set-pipeline` accepts inline sidecar objects in the `sidecars` list
- [ ] `fly set-pipeline` continues to accept string file references
- [ ] Mixed inline + file-reference lists work correctly
- [ ] Inline sidecars appear as containers in the K8s pod spec
- [ ] Config validation rejects inline sidecars missing `name` or `image`
- [ ] Config validation rejects reserved sidecar names in inline definitions
- [ ] Duplicate names across inline and file sidecars are detected
- [ ] `fly get-pipeline` round-trips inline sidecar definitions correctly

## Out of Scope

- Readiness/liveness probes on sidecars (future enhancement)
- Sidecar-specific volume mounts (sidecars share main container mounts)
- Sidecar definitions at the resource type level
- Sidecar lifecycle management (start ordering, graceful shutdown)
