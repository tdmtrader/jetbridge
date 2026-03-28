# Implementation Plan: Ephemeral Storage Requests and Limits

## Phase 1: Core Type Changes

- [ ] Write tests for EphemeralStorageLimit parsing in `atc/container_limits.go`
- [ ] Add `EphemeralStorage` field to `atc.ContainerLimits` and parsing logic
- [ ] Write tests for ephemeral_storage in task config YAML parsing (`atc/task_test.go`)
- [ ] Verify `TaskConfig` serialization/deserialization with ephemeral_storage
- [ ] Phase 1 Manual Verification

## Phase 2: Runtime Wiring

- [ ] Add `EphemeralStorage` and `EphemeralStorageRequest` fields to `atc/runtime/types.go` `ContainerLimits`
- [ ] Wire fields from `atc.ContainerLimits` to `runtime.ContainerLimits` in `atc/exec/task_step.go`
- [ ] Write tests for `buildResourceRequirements()` with ephemeral-storage (`atc/worker/jetbridge/container_test.go`)
- [ ] Add `corev1.ResourceEphemeralStorage` handling to `buildResourceRequirements()` in `atc/worker/jetbridge/container.go`
- [ ] Phase 2 Manual Verification
