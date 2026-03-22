# Implementation Plan: Burstable QoS for Task Containers

## Phase 1: Core Types and Parsing

- [x] Task: Add `Requests *ContainerLimits` to `TaskConfig` in `atc/task.go` ed99fa775
- [x] Task: Add `CPURequest`/`MemoryRequest` fields to `runtime.ContainerLimits` in `atc/runtime/types.go` 0fccd86bd
- [x] Task: Add `Requests *ContainerLimits` to `TaskPlan`/`RunPlan` in `atc/plan.go` and `TaskStep` in `atc/steps.go` 9466fd355
- [x] Task: Write unit tests for new type parsing (`atc/task_test.go`, `atc/steps_test.go`) 3bd1cbfc6
- [~] Task: Phase 1 checkpoint — types compile, parsing tests green

---

## Phase 2: Execution Pipeline

- [ ] Task: Update `OverrideContainerLimitsSource` in `atc/exec/task_config_source.go` to merge requests
- [ ] Task: Update `TaskStep.run()` in `atc/exec/task_step.go` to propagate requests to containerSpec and apply defaults
- [ ] Task: Wire requests through `atc/builds/planner.go` visitor
- [ ] Task: Write unit tests for merge logic and request propagation (`atc/exec/task_step_test.go`, `atc/exec/task_config_source_test.go`)
- [ ] Task: Phase 2 checkpoint — execution pipeline tests green

---

## Phase 3: K8s Pod Builder

- [ ] Task: Update `buildResourceRequirements()` in `atc/worker/jetbridge/container.go` to handle independent requests
- [ ] Task: Write tests for all QoS combinations: Guaranteed (limits only), Burstable (both), Burstable-no-cap (requests only), BestEffort (neither)
- [ ] Task: Phase 3 checkpoint — pod builder tests green

---

## Phase 4: CLI Defaults and Wiring

- [ ] Task: Add `--default-task-cpu-request` and `--default-task-memory-request` flags in `atc/atccmd/command.go`
- [ ] Task: Pass default requests through `atc/engine/step_factory.go`
- [ ] Task: Write tests for CLI default application
- [ ] Task: Phase 4 checkpoint — full unit test suite green (`make test-unit`)

---
