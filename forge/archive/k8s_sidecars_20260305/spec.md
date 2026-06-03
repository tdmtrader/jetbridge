# Spec: K8s Sidecars Hardening

**Track ID:** `k8s_sidecars_20260305`
**Type:** feature

## Overview

The K8s sidecar implementation is functionally complete — users can declare sidecars (inline or file-referenced) on task steps, and they run in the same pod with shared networking and volumes. However, there are gaps in failure detection, observability, and input validation that reduce production confidence. This track hardens the existing implementation.

## Requirements

> **Update (2026-03-13):** Requirements 1–2 (unit tests for pod construction) are
> already satisfied. `container_test.go:3501-3868` has extensive coverage for
> `buildSidecarContainers()` and `buildSidecarResourceRequirements()` including
> basic fields, env vars, ports, resources, volume sharing, security context, and
> multi-sidecar scenarios.

1. ~~**Unit tests for `buildSidecarContainers()`**~~ — **DONE.** Extensive tests exist in `container_test.go:3501-3868`.
2. ~~**Unit tests for `buildSidecarResourceRequirements()`**~~ — **DONE.** Tests exist in `container_test.go:3684-3745`.
3. **Sidecar failure detection** — `isPodFailedFast()` (`process.go:262`) only checks the `main` container (line 267: `if cs.Name != mainContainerName { continue }`). If a sidecar hits `ImagePullBackOff`, `CrashLoopBackOff`, etc., the task hangs instead of failing fast.
4. **Sidecar log streaming** — `streamLogs()` (`process.go:212`) only streams the `main` container (line 225: `Container: mainContainerName`). Sidecar output is invisible in build logs, making debugging difficult.
5. **Protocol validation** — `SidecarPort.Protocol` (`sidecar.go:40`) accepts any string and casts it directly to `corev1.Protocol` (`container.go:566`). Should validate against TCP/UDP/SCTP.

## Acceptance Criteria

- [x] `buildSidecarContainers()` has unit tests covering: basic fields, env vars, ports with default/explicit protocol, resource limits, volume mount sharing, empty sidecars list, security context
- [x] `buildSidecarResourceRequirements()` has unit tests covering: CPU only, memory only, both, requests vs limits, empty
- [ ] `isPodFailedFast()` detects terminal waiting states on sidecar containers (not just main)
- [ ] Sidecar terminal failures surface in build logs via `writePodDiagnostics()` (note: function already iterates all containers, needs dedicated test)
- [ ] `streamLogs()` streams sidecar container logs alongside main container logs (prefixed with container name)
- [ ] `SidecarPort.Protocol` validated to TCP/UDP/SCTP during `Validate()`
- [ ] Existing E2E and unit tests continue to pass

## Out of Scope

- Sidecar readiness probes / startup ordering
- Sidecar exit code propagation as task failure
- Env var inheritance from main container
- Init container support for sidecar setup
- ~~Image registry prefixing for sidecar images~~ (now handled by `image_ref_hardening_for_tasks_etc` track — sidecar digest pinning)
