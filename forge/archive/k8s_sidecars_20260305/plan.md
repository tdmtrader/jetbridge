# Implementation Plan: K8s Sidecars Hardening

## Phase 1: Sidecar Failure Detection ~~Unit Test Coverage for Pod Construction~~

> **Plan update (2026-03-13):** Phase 1 was originally about adding unit tests for
> `buildSidecarContainers()` and `buildSidecarResourceRequirements()`. These tests
> already exist — `container_test.go:3501-3868` has extensive coverage including
> basic fields, env vars, ports, resources, volume sharing, security context, and
> multi-sidecar scenarios. Phase 1 is therefore **complete** and has been replaced
> with the highest-value remaining work: sidecar failure detection.

`isPodFailedFast()` (`process.go:262`) only checks the main container — if a
sidecar hits ImagePullBackOff or CrashLoopBackOff, the task hangs instead of
failing fast. This is the most impactful gap.

- [ ] Write tests for `isPodFailedFast()` detecting sidecar terminal states (ImagePullBackOff, CrashLoopBackOff on non-main containers) — currently line 267 has `if cs.Name != mainContainerName { continue }` which skips all sidecars
- [ ] Remove the `cs.Name != mainContainerName` guard in `isPodFailedFast()` so all container statuses are checked
- [ ] Write test verifying `writePodDiagnostics()` includes sidecar container status — note: the function already iterates all containers (process.go:304-313) so this may just need a dedicated unit test confirming the behavior
- [ ] Phase 1 Manual Verification

## Phase 2: Sidecar Log Streaming

`streamLogs()` (`process.go:212`) is hardcoded to `Container: mainContainerName`
(line 225). Sidecar output is invisible in build logs, making debugging difficult.

- [ ] Write tests for multi-container log streaming — verify sidecar logs are captured with container name prefix
- [ ] Implement sidecar log streaming in `streamLogs()` — launch goroutines per sidecar container, prefix output lines with `[container-name]`
- [ ] Phase 2 Manual Verification

## Phase 3: Protocol Validation

`SidecarPort.Protocol` (`sidecar.go:40`) accepts any string and is cast directly
to `corev1.Protocol` in `buildSidecarContainers()` (container.go:566).
`SidecarConfig.Validate()` (sidecar.go:106) validates name and image but does
not check port protocols.

- [ ] Write tests for `SidecarPort` protocol validation — valid protocols (TCP, UDP, SCTP, empty default), invalid protocol rejected
- [ ] Implement protocol validation in `SidecarConfig.Validate()` — reject protocols not in {TCP, UDP, SCTP, ""}
- [ ] Phase 3 Manual Verification

---
