# Spec: Task Step Sidecars

**Track ID:** `kubernetes_spec_and_sidecar_20260209`
**Type:** feature

## Overview

Add sidecar container support to Concourse task steps. Users can define service containers (databases, backend servers, etc.) that run alongside the main task container in the same Kubernetes pod. Sidecar definitions are referenced as files from task step inputs, using a format that mirrors a subset of a Kubernetes container spec.

This eliminates the need for Docker-in-Docker or similar runtime dependencies when tasks require auxiliary services.

## Motivation

Common CI/CD patterns require running services alongside test suites:
- A database (Postgres, MySQL) for backend integration tests
- A backend server for frontend E2E tests
- A cache (Redis, Memcached) for integration testing
- Multiple services simultaneously (DB + backend + frontend)

Today, users must either use Docker-in-Docker (heavy, privileged, fragile) or run services externally. Since JetBridge already creates Kubernetes pods for tasks, adding sidecar containers to those pods is the natural solution. Containers in the same pod share a network namespace, so services are reachable at `localhost:<port>`.

## User-Facing Design

### Pipeline YAML (step level)

A new `sidecars` field on the task step references files from task inputs:

```yaml
jobs:
- name: integration-tests
  plan:
  - get: my-repo
  - task: test-with-db
    file: my-repo/ci/tasks/test.yml
    sidecars:
    - my-repo/ci/sidecars/postgres.yml
    - my-repo/ci/sidecars/redis.yml
```

`sidecars` is a list of file paths (relative to build inputs, same as `file:`). Each file defines one or more sidecar containers.

### Sidecar Definition File

Each sidecar file is a YAML list of containers. A single file can define multiple sidecars, mirroring a subset of the Kubernetes container spec:

```yaml
# ci/sidecars/services.yml — multiple sidecars in one file
- name: postgres
  image: postgres:15
  env:
  - name: POSTGRES_PASSWORD
    value: test
  - name: POSTGRES_DB
    value: myapp_test
  ports:
  - containerPort: 5432
  resources:
    requests:
      cpu: 100m
      memory: 256Mi
    limits:
      cpu: 500m
      memory: 512Mi
- name: redis
  image: redis:7
  ports:
  - containerPort: 6379
```

A file with a single sidecar is simply a list of one:

```yaml
# ci/sidecars/postgres.yml — single sidecar
- name: postgres
  image: postgres:15
  env:
  - name: POSTGRES_PASSWORD
    value: test
  ports:
  - containerPort: 5432
```

Supported fields (subset of `corev1.Container`):
- `name` (required) — unique container name
- `image` (required) — Docker image reference
- `command` — entrypoint override (string array)
- `args` — arguments (string array)
- `env` — environment variables (`{name, value}` pairs, K8s-style)
- `ports` — container ports (`{containerPort, protocol}`)
- `resources` — resource requests/limits (`{cpu, memory}` as K8s quantity strings)
- `workingDir` — working directory

### Backwards Compatibility

- `sidecars` is optional. Omitting it produces identical behavior to today.
- No existing fields are modified or removed.
- Pipelines without `sidecars` parse and execute identically.
- The sidecar file format is new and has no conflict with existing config.

## Requirements

1. Task steps accept an optional `sidecars` field — a list of file paths pointing to sidecar definition files in task inputs.
2. Sidecar files use a YAML format mirroring a subset of a K8s container spec (name, image, command, args, env, ports, resources, workingDir).
3. Each sidecar file defines a YAML list of one or more sidecar containers; multiple files can be referenced.
4. Sidecar containers are injected into the task's Kubernetes pod alongside the main container.
5. Sidecar containers share the pod's network namespace (services accessible at `localhost:<port>`).
6. Sidecar containers receive the same volume mounts as the main container (access to task inputs, outputs, and caches).
7. Sidecar containers are terminated when the main task completes.
8. The `sidecars` field flows through the full pipeline: TaskStep -> TaskPlan -> ContainerSpec -> Pod.
9. Invalid sidecar files (missing name/image, bad YAML) produce clear validation errors.
10. All existing pipelines without sidecars continue to work unchanged.

## Acceptance Criteria

- [ ] `sidecars` field on TaskStep parses from pipeline YAML without error
- [ ] Sidecar definition files parse with validation (name and image required)
- [ ] Sidecars are threaded through TaskStep -> TaskPlan -> planner -> ContainerSpec
- [ ] Sidecar files are loaded from build artifacts at task execution time
- [ ] A single sidecar file can define multiple sidecars (YAML list)
- [ ] `buildPod()` injects sidecar containers into the K8s pod spec
- [ ] Sidecar containers share network namespace with the main container (K8s default)
- [ ] Sidecar containers receive the same volume mounts as the main container
- [ ] Sidecars are terminated when the main task container exits
- [ ] Pipelines without `sidecars` field work identically to before
- [ ] Unit tests cover parsing, validation, planner threading, pod construction
- [ ] Integration test demonstrates a task with a working sidecar service

## Out of Scope

- Inline sidecar definitions in pipeline YAML (file references only for v1)
- Readiness probes (defer to v2; users can use retry/wait scripts for now)
- Sidecars on non-task steps (get, put)
- Multi-cluster sidecar support
- Sidecar image caching / pull policies beyond K8s defaults
