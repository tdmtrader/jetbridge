# Implementation Plan: Planning Agent — `ci-agent-plan`

## Design Constraint

This agent lives within the `ci-agent/` standalone Go module (same module as the review and fix tracks). Zero Concourse imports. All new code lives under `ci-agent/` with its own packages. Types structurally conform to the `atc/agent/schema/` conventions (same JSON field names: `schema_version`, `status`, `confidence`, `artifacts`, `metadata`) but do not import that package.

The planning agent takes a structured JSON story/prompt as input and produces four output files: `spec.md`, `plan.md`, `results.json`, and `events.ndjson`.

## Dependency: `agent_step_output_schema` Track

The planning agent's `results.json` and `events.ndjson` outputs must structurally conform to the schemas defined in the `agent_step_output_schema_20260209` track (`atc/agent/schema/`). Since this is a standalone module, we mirror the envelope types with identical JSON field names rather than importing them. Shared envelope types (Results, Artifact, Status, Event, EventType, EventWriter, EventReader) go in `ci-agent/schema/` so the review, fix, and plan agents can all reuse them.

---

## Phase 1: Schema & Envelope Types

### Task 1: Bootstrap planning agent packages within `ci-agent/`

- [x] 8a4a1c44b Verify `ci-agent/go.mod` exists or initialize it as an independent module (`module github.com/concourse/ci-agent`)
- [x] 8a4a1c44b Create package structure: `ci-agent/schema/`, `ci-agent/plan/`, `ci-agent/plan/adapter/`, `ci-agent/plan/confidence/`, `ci-agent/plan/orchestrator/`
- [x] 8a4a1c44b Verify module builds and tests run independently of parent repo

### Task 2: Define results envelope types (structurally conformant to `atc/agent/schema/`)

- [x] 8a4a1c44b Write tests for results schema
- [x] 8a4a1c44b Implement results schema types

### Task 3: Define event schema types (structurally conformant to `atc/agent/schema/`)

- [x] 8a4a1c44b Write tests for event types
- [x] 8a4a1c44b Implement event types

### Task 4: NDJSON EventWriter and EventReader

- [x] 8a4a1c44b Write tests for EventWriter
- [x] 8a4a1c44b Implement EventWriter
- [x] 8a4a1c44b Write tests for EventReader
- [x] 8a4a1c44b Implement EventReader

### Task 5: Define planning input schema types

- [x] 8a4a1c44b Write tests for planning input schema
- [x] 8a4a1c44b Implement planning input schema types

- [x] Phase 1 Checkpoint [checkpoint: 8a4a1c44b] — module builds independently, all schema types tested (78 tests pass), JSON field names match conventions

---

## Phase 2: Input Parsing & Completeness Scoring

### Task 6: Input file reader

- [x] 4ef736e9a Write tests for input parsing (8 tests)
- [x] 4ef736e9a Implement input parser

### Task 7: Input completeness scorer

- [x] 4ef736e9a Write tests for completeness scoring (13 tests)
- [x] 4ef736e9a Implement completeness scorer

- [x] Phase 2 Checkpoint [checkpoint: 4ef736e9a] — input parsing (8 tests) and completeness scoring (13 tests) all pass

---

## Phase 3: Spec Generation

### Task 8: Adapter interface and spec/plan output types

- [x] a3f9165f2 Write tests for adapter output types (5 tests)
- [x] a3f9165f2 Define adapter interface and types

### Task 9: Spec prompt template

- [x] a3f9165f2 Write tests for spec prompt builder (5 tests)
- [x] a3f9165f2 Implement spec prompt template

### Task 10: Claude Code adapter for spec generation

- [x] a3f9165f2 Write tests for Claude Code adapter (4 tests)
- [x] a3f9165f2 Implement Claude Code adapter

### Task 11: Spec markdown renderer

- [x] a3f9165f2 Write tests for spec renderer (9 tests)
- [x] a3f9165f2 Implement spec renderer

- [x] Phase 3 Checkpoint [checkpoint: a3f9165f2] — adapter, prompts, Claude adapter, spec renderer all tested

---

## Phase 4: Plan Generation

### Task 12: Plan prompt template

- [x] e551a9c2f Write tests for plan prompt builder (5 tests)
- [x] e551a9c2f Implement plan prompt template

### Task 13: Claude Code adapter for plan generation

- [x] a3f9165f2 Implement Claude Code adapter (plan path included in adapter)

### Task 14: Plan markdown renderer

- [x] e551a9c2f Write tests for plan renderer (8 tests)
- [x] e551a9c2f Implement plan renderer

- [x] Phase 4 Checkpoint [checkpoint: e551a9c2f] — plan prompt and renderer tested

---

## Phase 5: Confidence Scoring Engine

### Task 15: Spec coverage scorer

- [x] ef930f247 Write tests for spec coverage scoring (6 tests)
- [x] ef930f247 Implement spec coverage scorer

### Task 16: Plan actionability scorer

- [x] ef930f247 Write tests for plan actionability scoring (7 tests)
- [x] ef930f247 Implement plan actionability scorer

### Task 17: Composite confidence computation

- [x] ef930f247 Write tests for composite confidence (8 tests)
- [x] ef930f247 Implement composite confidence

- [x] Phase 5 Checkpoint [checkpoint: ef930f247] — 35 confidence tests pass, scoring is deterministic

---

## Phase 6: Orchestrator & Output Packaging

### Task 18: Orchestrator (wires all components)

- [x] f962516a5 Write tests for orchestrator (5 tests + 1 skipped)
- [x] f962516a5 Implement orchestrator

### Task 19: Output file writer

- [x] f962516a5 Write tests for output writer (4 tests)
- [x] f962516a5 Implement output writer

- [x] Phase 6 Checkpoint [checkpoint: f962516a5] — orchestrator runs end-to-end with fake adapter, all output files valid

---

## Phase 7: CLI & Configuration

### Task 20: CLI binary

- [x] a5a109747 Implement CLI entrypoint with env var parsing
- [x] a5a109747 Binary builds successfully

- [x] Phase 7 Checkpoint [checkpoint: a5a109747] — CLI builds and runs, handles missing adapter gracefully

---

## Phase 8: Concourse Integration

### Task 21: Task YAML definitions

- [x] fca566a6b Write ci-agent-plan.yml task definition
- [x] fca566a6b Write plan-gate.yml companion task
- [x] fca566a6b Validate with fly validate-pipeline

### Task 22: Container image

- [x] fca566a6b Add ci-agent-plan binary to Dockerfile.ci-agent

- [x] Phase 8 Checkpoint [checkpoint: fca566a6b] — task definitions valid, fly validate-pipeline passes

---

## Phase 9: Self-Test & Pipeline Integration

### Task 23: Self-test validation

- [x] fca566a6b CLI smoke test with synthetic input: results.json validates, events.ndjson has chronological events, error handling works

### Task 24: Pipeline integration

- [x] fca566a6b Added agent-plan job to deploy/borg-pipeline.yml

- [x] Phase 9 Checkpoint — self-test produces valid output, pipeline validated

---

## Key Files

| File | Change |
|------|--------|
| `ci-agent/go.mod` | NEW or MODIFY — standalone Go module |
| `ci-agent/schema/results.go` | NEW — Results, Artifact, Status (shared envelope) |
| `ci-agent/schema/results_test.go` | NEW — Results schema tests |
| `ci-agent/schema/event.go` | NEW — Event, EventType (shared event types) |
| `ci-agent/schema/event_test.go` | NEW — Event schema tests |
| `ci-agent/schema/event_writer.go` | NEW — NDJSON event writer |
| `ci-agent/schema/event_reader.go` | NEW — NDJSON event reader |
| `ci-agent/schema/event_io_test.go` | NEW — EventWriter/EventReader tests |
| `ci-agent/schema/planning_input.go` | NEW — PlanningInput, PlanningContext, Priority, StoryType |
| `ci-agent/schema/planning_input_test.go` | NEW — Input schema tests |
| `ci-agent/plan/input_parser.go` | NEW — Parse input.json from file or reader |
| `ci-agent/plan/input_parser_test.go` | NEW — Input parser tests |
| `ci-agent/plan/confidence/completeness.go` | NEW — Input completeness scoring |
| `ci-agent/plan/confidence/coverage.go` | NEW — Spec coverage scoring |
| `ci-agent/plan/confidence/actionability.go` | NEW — Plan actionability scoring |
| `ci-agent/plan/confidence/confidence.go` | NEW — Composite confidence computation |
| `ci-agent/plan/confidence/confidence_test.go` | NEW — All confidence scoring tests |
| `ci-agent/plan/adapter/adapter.go` | NEW — Adapter interface, SpecOutput, PlanOutput types |
| `ci-agent/plan/adapter/fakes/fake_adapter.go` | NEW — Counterfeiter-generated fake |
| `ci-agent/plan/adapter/prompt_spec.go` | NEW — Spec prompt builder |
| `ci-agent/plan/adapter/prompt_plan.go` | NEW — Plan prompt builder |
| `ci-agent/plan/adapter/prompt_test.go` | NEW — Prompt template tests |
| `ci-agent/plan/adapter/claude/claude.go` | NEW — Claude Code adapter |
| `ci-agent/plan/adapter/claude/claude_test.go` | NEW — Claude Code adapter tests |
| `ci-agent/plan/spec_renderer.go` | NEW — Render SpecOutput to Markdown |
| `ci-agent/plan/spec_renderer_test.go` | NEW — Spec renderer tests |
| `ci-agent/plan/plan_renderer.go` | NEW — Render PlanOutput to Markdown |
| `ci-agent/plan/plan_renderer_test.go` | NEW — Plan renderer tests |
| `ci-agent/plan/orchestrator/orchestrator.go` | NEW — End-to-end orchestration |
| `ci-agent/plan/orchestrator/orchestrator_test.go` | NEW — Orchestrator tests |
| `ci-agent/plan/orchestrator/writer.go` | NEW — Output file writer |
| `ci-agent/plan/orchestrator/writer_test.go` | NEW — Writer tests |
| `cmd/ci-agent-plan/main.go` | NEW — CLI entrypoint |
| `cmd/ci-agent-plan/main_test.go` | NEW — CLI tests |
| `ci/tasks/ci-agent-plan.yml` | NEW — Concourse task definition |
| `ci/tasks/plan-gate.yml` | NEW — Gate companion task |
| `deploy/Dockerfile.ci-agent` | NEW or MODIFY — Add plan binary |
| `deploy/borg-pipeline.yml` | MODIFY — Add planning job |

## Environment Variables (Concourse Convention)

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `INPUT_DIR` | yes | — | Directory containing `input.json` (Concourse input mapping) |
| `OUTPUT_DIR` | yes | — | Directory for output artifacts (Concourse output mapping) |
| `AGENT_CLI` | no | `claude` | Agent CLI binary name or path |
| `AGENT_MODEL` | no | _(adapter default)_ | Model to pass to agent CLI |
| `CONFIDENCE_THRESHOLD` | no | `0.6` | Minimum confidence to pass (0.0–1.0) |
| `CONFIDENCE_WEIGHTS` | no | `{"completeness":0.25,"coverage":0.35,"actionability":0.40}` | JSON weights for sub-scores |
| `TIMEOUT` | no | `5m` | Maximum time for agent invocations |

## Pipeline Integration Example

```yaml
jobs:
- name: agent-plan
  plan:
  - get: story
    trigger: true
  - task: generate-plan
    file: ci/tasks/ci-agent-plan.yml
    input_mapping: { story: story }
    output_mapping: { plan-output: plan-output }
    params:
      AGENT_CLI: claude
      CONFIDENCE_THRESHOLD: "0.6"
  - task: plan-gate
    file: ci/tasks/plan-gate.yml
    input_mapping: { plan-output: plan-output }
    params:
      CONFIDENCE_THRESHOLD: "0.6"

# Downstream: use spec.md + plan.md as context for implementation agent
- name: agent-implement
  plan:
  - get: repo
    passed: [agent-plan]
  - get: plan-output
    passed: [agent-plan]
  # ... implementation agent uses plan artifacts as input context
```

## Task Definition (`ci/tasks/ci-agent-plan.yml`)

```yaml
platform: linux
image_resource:
  type: registry-image
  source:
    repository: ghcr.io/jetbridge/ci-agent
    tag: latest

inputs:
- name: story

outputs:
- name: plan-output

params:
  AGENT_CLI: claude
  AGENT_MODEL: ""
  CONFIDENCE_THRESHOLD: "0.6"
  CONFIDENCE_WEIGHTS: ""
  TIMEOUT: "5m"
  INPUT_DIR: story
  OUTPUT_DIR: plan-output

run:
  path: /usr/local/bin/ci-agent-plan
```
