# Implementation Plan: Task Step Sidecars

## Phase 1: Core Types and Config Parsing

### Task 1: Define SidecarConfig type and file parsing

- [x] b465c3f Write tests for SidecarConfig
  - Parse valid sidecar YAML list with one sidecar (name, image, env, ports, resources, command, args, workingDir)
  - Parse YAML list with multiple sidecars in one file
  - Validate: missing `name` returns error
  - Validate: missing `image` returns error
  - Validate: empty name returns error
  - Validate: duplicate sidecar names returns error
  - Round-trip JSON marshal/unmarshal
  - Unknown fields in sidecar YAML are rejected (strict parsing)
- [x] b465c3f Implement SidecarConfig
  - New file: `atc/sidecar.go`
  - Types: `SidecarConfig`, `SidecarEnvVar`, `SidecarPort`, `SidecarResources`, `SidecarResourceList`
  - `ParseSidecarConfigs(yamlBytes []byte) ([]SidecarConfig, error)` — parse YAML list + validate each
  - `SidecarConfig.Validate() error`

### Task 2: Add sidecars field to TaskStep and TaskPlan

- [x] 81f0313 Write tests for TaskStep sidecars parsing
  - TaskStep with `sidecars: [path1.yml, path2.yml]` unmarshals correctly
  - TaskStep without `sidecars` field still parses (backwards compat)
  - TaskStep marshals back to JSON including sidecars
- [x] 81f0313 Add sidecars to TaskStep and TaskPlan
  - `atc/steps.go` — `TaskStep.Sidecars []string` (`json:"sidecars,omitempty"`)
  - `atc/plan.go` — `TaskPlan.Sidecars []string` (`json:"sidecars,omitempty"`)

### Task 3: Thread sidecars through the planner

- [x] 5216faf Write tests for planner sidecar threading
  - VisitTask with sidecars produces TaskPlan with matching sidecars
  - VisitTask without sidecars produces TaskPlan with nil/empty sidecars
- [x] 5216faf Update planner
  - `atc/builds/planner.go` — `VisitTask` copies `step.Sidecars` to `TaskPlan.Sidecars`

- [x] Task: Phase 1 Manual Verification (autonomous mode)

---

## Phase 2: Sidecar Loading and Runtime

### Task 4: Add sidecars to ContainerSpec

- [x] f4048c6 Write tests for ContainerSpec with sidecars
  - ContainerSpec with sidecars field round-trips correctly
  - ContainerSpec without sidecars is unchanged (backwards compat)
- [x] f4048c6 Add sidecars to ContainerSpec
  - `atc/runtime/types.go` — `ContainerSpec.Sidecars []SidecarConfig`
  - Import `atc.SidecarConfig` (or define equivalent in runtime package)

### Task 5: Load sidecar files from artifacts and thread to ContainerSpec

- [x] 05f0c78 Write tests for sidecar file loading
  - Loading a valid sidecar file with one sidecar returns parsed []SidecarConfig
  - Loading a valid sidecar file with multiple sidecars returns all of them
  - Loading multiple files aggregates all sidecars
  - Loading a non-existent file returns error
  - Loading an invalid YAML file returns error
  - Duplicate sidecar names across files returns error
- [x] 05f0c78 Implement sidecar loading in task_step.go
  - `atc/exec/task_step.go` — new method `loadSidecars(ctx, streamer, paths) ([]SidecarConfig, error)`
  - Uses the same `Streamer` interface as `FileConfigSource` to read files from artifacts
  - Called in `run()` after task config is resolved, before building ContainerSpec
  - Loaded sidecars set on `ContainerSpec.Sidecars`

- [x] Task: Phase 2 Manual Verification — all atc/exec tests pass

---

## Phase 3: Pod Construction

### Task 6: Inject sidecar containers in buildPod()

- [x] 2114f39 Write tests for buildPod with sidecars
  - Pod with zero sidecars has only main container (+ artifact helper)
  - Pod with one sidecar has main + sidecar containers
  - Pod with multiple sidecars has all containers
  - Sidecar env vars, ports, resources, command, args map to K8s container fields
  - Sidecar containers get non-privileged security context
  - Sidecar containers receive the same volume mounts as the main container
  - Sidecars from a multi-sidecar file all appear in the pod
- [x] 2114f39 Implement sidecar injection in buildPod
  - `atc/worker/jetbridge/container.go` — `buildSidecarContainers(sidecars []SidecarConfig, mainMounts []corev1.VolumeMount) []corev1.Container`
  - Converts `SidecarConfig` fields to `corev1.Container` fields
  - Passes the main container's `volumeMounts` to each sidecar (shared inputs/outputs/caches)
  - Appends sidecar containers to pod spec's Containers list
  - Sidecars get `AllowPrivilegeEscalation: false` and `ImagePullPolicy: IfNotPresent`

### Task 7: Handle sidecar lifecycle

- [x] c94f41b Write tests for sidecar termination
  - After main process exits, sidecars receive termination signal
  - Pod completes after sidecars terminate
- [x] c94f41b Implement sidecar lifecycle
  - In direct mode: podExitCode detects main container termination in Running phase
  - In direct mode: Process.Wait deletes pod after main exits when sidecars present
  - In exec mode: GC reaper handles pod cleanup (existing behavior, no change needed)

- [x] Task: Phase 3 Manual Verification — all jetbridge tests pass (251/251)

---

## Phase 4: Integration Testing

### Task 8: Integration test with sidecar

- [x] 954fe2f Write integration test
  - Test: task step with a sidecar verifies:
    - Sidecar starts alongside main container with correct image, env, ports
    - Sidecar shares volume mounts with main container
    - Task completes successfully via exec-mode
    - Multiple sidecars work correctly
  - Located in `atc/worker/jetbridge/integration_test.go`

- [x] Task: Phase 4 Manual Verification — all packages pass: atc, exec, builds, jetbridge

---

## Key Files

| File | Change |
|------|--------|
| `atc/sidecar.go` | NEW — SidecarConfig type, parsing, validation |
| `atc/sidecar_test.go` | NEW — Unit tests for sidecar types |
| `atc/steps.go` | MODIFY — Add `Sidecars []string` to TaskStep |
| `atc/plan.go` | MODIFY — Add `Sidecars []string` to TaskPlan |
| `atc/builds/planner.go` | MODIFY — Thread sidecars in VisitTask |
| `atc/builds/planner_test.go` | MODIFY — Test planner sidecar threading |
| `atc/runtime/types.go` | MODIFY — Add `Sidecars` to ContainerSpec |
| `atc/exec/task_step.go` | MODIFY — Load sidecar files, set on ContainerSpec |
| `atc/exec/task_step_test.go` | MODIFY — Test sidecar loading |
| `atc/worker/jetbridge/container.go` | MODIFY — Build sidecar containers in buildPod() |
| `atc/worker/jetbridge/container_test.go` | MODIFY — Test pod construction with sidecars |
