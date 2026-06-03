# Implementation Plan: Burstable QoS for Task Containers

## Phase 1: Core Types and Parsing [checkpoint: 49170c744]

- [x] Task: Add `Requests *ContainerLimits` to `TaskConfig` in `atc/task.go` ed99fa775
- [x] Task: Add `CPURequest`/`MemoryRequest` fields to `runtime.ContainerLimits` in `atc/runtime/types.go` 0fccd86bd
- [x] Task: Add `Requests *ContainerLimits` to `TaskPlan`/`RunPlan` in `atc/plan.go` and `TaskStep` in `atc/steps.go` 9466fd355
- [x] Task: Write unit tests for new type parsing (`atc/task_test.go`, `atc/steps_test.go`) 3bd1cbfc6
- [x] Task: Phase 1 checkpoint — types compile, parsing tests green 49170c744

---

## Phase 2: Execution Pipeline [checkpoint: 814633de3]

- [x] Task: Update `OverrideContainerLimitsSource` in `atc/exec/task_config_source.go` to merge requests 60c2a584f
- [x] Task: Update `TaskStep.run()` in `atc/exec/task_step.go` to propagate requests to containerSpec and apply defaults 272de70fb
- [x] Task: Wire requests through `atc/builds/planner.go` visitor 775f469f8
- [x] Task: Write unit tests for merge logic and request propagation (`atc/exec/task_step_test.go`, `atc/exec/task_config_source_test.go`) c32534efe
- [x] Task: Phase 2 checkpoint — execution pipeline tests green 814633de3

---

## Phase 3: K8s Pod Builder [checkpoint: e2db1a888]

- [x] Task: Update `buildResourceRequirements()` in `atc/worker/jetbridge/container.go` to handle independent requests 7e3e14593
- [x] Task: Write tests for all QoS combinations: Guaranteed (limits only), Burstable (both), Burstable-no-cap (requests only), BestEffort (neither) 637d112b4
- [x] Task: Phase 3 checkpoint — pod builder tests green e2db1a888

---

## Phase 4: CLI Defaults and Wiring [checkpoint: 4871c962b]

- [x] Task: Add `--default-task-cpu-request` and `--default-task-memory-request` flags in `atc/atccmd/command.go` 5da17aa91
- [x] Task: Pass default requests through `atc/engine/step_factory.go` 272de70fb
- [x] Task: Write tests for CLI default application 5da17aa91
- [x] Task: Phase 4 checkpoint — full unit test suite green (`make test-unit`) 4871c962b

---
