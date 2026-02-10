# Implementation Plan: Implementation Agent — `ci-agent-implement`

## Design Constraint

This agent lives within the `ci-agent/` standalone Go module (same module as the review, fix, and plan tracks). Zero Concourse imports. All new code lives under `ci-agent/implement/` with its own packages. Reuses shared schema types from `ci-agent/schema/` (Results, Event, EventWriter, Artifact, Status). Reuses `ci-agent/runner/` for Go test execution where applicable.

The implementation agent reads `spec.md` + `plan.md` (produced by `ci-agent-plan`) and a `repo/` directory, then drives an AI agent through TDD cycles for each plan task.

## Dependency: Prior Tracks

- `agent_step_output_schema` — defines `results.json` and `events.ndjson` envelope types in `atc/agent/schema/`
- `agent_can_produce_a_clear_documented_plan_from_a_promptjira_story` — defines the planning agent that produces the `spec.md` + `plan.md` this agent consumes
- Shared `ci-agent/schema/` types (Results, Event, EventWriter) should exist before this track starts

---

## Phase 1: Plan Parser & Task Tracker

### Task 1: Plan Markdown parser

- [x] Write tests for plan parser (10 tests)
- [x] Implement plan parser
  - New file: `ci-agent/implement/parser.go`
  - `ParsePlan(r io.Reader) ([]Phase, error)`
  - `ParsePlanFile(path string) ([]Phase, error)`
  - Types: `Phase{Name string, Tasks []PlanTask}`, `PlanTask{ID string, Description string, Phase string, Files []string}`

### Task 2: Spec reader

- [x] Write tests for spec reader (4 tests)
- [x] Implement spec reader
  - New file: `ci-agent/implement/spec_reader.go`
  - `ReadSpec(path string) (*SpecContext, error)`
  - `SpecContext{Raw string, AcceptanceCriteria []string}`

### Task 3: Task progress tracker

- [x] Write tests for task tracker (17 tests)
- [x] Implement task tracker
  - New file: `ci-agent/implement/tracker.go`
  - Types: `TaskTracker`, `TaskStatus` (enum: pending, red, green, committed, skipped, failed), `TaskProgress{TaskID, Status, Reason, CommitSHA, TestFile, Duration}`
  - Methods: `Advance`, `Skip`, `Fail`, `NextPending`, `IsComplete`, `CanContinue`, `Save`, `Summary`

- [x] Phase 1 Checkpoint — plan parser and tracker tested (31 tests passing)

---

## Phase 2: TDD Executor — Red Phase

### Task 4: Agent adapter interface for code generation

- [x] Write tests for adapter types (4 tests)
- [x] Define adapter interface

### Task 5: Test prompt builder

- [x] Write tests for test prompt (5 tests)
- [x] Implement test prompt builder

### Task 6: Test writer and Red-phase verifier

- [x] Write tests for test writer (3 tests)
- [x] Write tests for red-phase verifier (3 tests)
- [x] Implement test writer and red verifier

- [x] Phase 2 Checkpoint — adapter interface defined, red phase writes and verifies failing tests (15 tests passing)

---

## Phase 3: TDD Executor — Green Phase

### Task 7: Implementation prompt builder

- [x] Write tests for impl prompt (5 tests)
- [x] Implement implementation prompt builder

### Task 8: Patch applier

- [x] Write tests for patch applier (5 tests)
- [x] Implement patch applier

### Task 9: Green-phase verifier

- [x] Write tests for green-phase verifier (3 tests)
- [x] Implement green verifier

- [x] Phase 3 Checkpoint — green phase applies patches and verifies tests pass (28 tests passing)

---

## Phase 4: Regression Guard & Git Operations

### Task 10: Full test suite runner

- [x] Write tests for suite runner (3 tests)
- [x] Implement suite runner

### Task 11: Git operations

- [x] Write tests for git operations (5 tests)
- [x] Implement git operations

### Task 12: Regression rollback logic

- [x] Write tests for regression rollback (3 tests)
- [x] Implement regression rollback

- [x] Phase 4 Checkpoint — full suite runs, git ops work, regressions detected and rolled back (70 tests passing)

---

## Phase 5: TDD Loop Orchestration

### Task 13: Single-task TDD loop

- [x] Write tests for single-task TDD loop (4 tests)
- [x] Implement single-task TDD loop

### Task 14: Multi-task sequencer

- [x] Write tests for task sequencer (3 tests)
- [x] Implement task sequencer

- [x] Phase 5 Checkpoint — TDD loop executes tasks end-to-end with fake adapter (77 tests passing)

---

## Phase 6: Claude Code Adapter

### Task 15: Claude Code adapter for test generation

- [x] Write tests for Claude Code adapter (6 tests)
- [x] Implement Claude Code test gen adapter

### Task 16: Claude Code adapter for implementation generation

- [x] Implement Claude Code impl gen adapter (combined with Task 15)

- [x] Phase 6 Checkpoint — adapter dispatches to Claude Code CLI, parses responses

---

## Phase 7: Confidence & Results

### Task 17: Implementation confidence scorer

- [x] Write tests for confidence scoring (6 tests)
- [x] Implement confidence scorer

### Task 18: Results builder

- [x] Write tests for results builder (3 tests)
- [x] Implement results builder

### Task 19: Summary renderer

- [x] Write tests for summary renderer (3 tests)
- [x] Implement summary renderer

- [x] Phase 7 Checkpoint — confidence scoring, results, and summary work

---

## Phase 8: Orchestrator & CLI

### Task 20: Orchestrator (wires all components)

- [x] Write tests for orchestrator (2 tests)
- [x] Implement orchestrator

### Task 21: Output writer

- [x] Output writing integrated into orchestrator (no separate writer needed)

### Task 22: CLI binary

- [x] Implement CLI entrypoint with env var parsing
- [x] Binary builds successfully

- [x] Phase 8 Checkpoint — CLI runs end-to-end, produces valid output files

---

## Phase 9: Concourse Integration

### Task 23: Task YAML definitions

- [x] Write `ci/tasks/ci-agent-implement.yml` task definition
- [x] Write `ci/tasks/implement-gate.yml` companion task
- [x] Validate task definitions with `fly validate-pipeline`

### Task 24: Container image

- [x] Add `ci-agent-implement` binary to `deploy/Dockerfile.ci-agent`
- [x] Pipeline validated with fly

- [x] Phase 9 Checkpoint — task definitions valid, pipeline validated

---

## Phase 10: Self-Test & Pipeline Integration

### Task 25: Self-test validation

- [x] CLI smoke test: 2 tasks parsed, results.json valid, events.ndjson chronological, summary.md generated
- [x] Verify `results.json` validates against schema
- [x] Verify `progress.json` shows task status
- [x] Verify `summary.md` has expected sections
- [x] Verify `events.ndjson` has valid chronological events

### Task 26: Pipeline integration

- [x] Added agent-implement job to `deploy/borg-pipeline.yml`
- [x] Pipeline validated with fly validate-pipeline

- [x] Phase 10 Checkpoint — self-test produces valid output, pipeline integrates cleanly

---

## Key Files

| File | Change |
|------|--------|
| `ci-agent/implement/parser.go` | NEW — Plan Markdown parser |
| `ci-agent/implement/parser_test.go` | NEW — Parser tests |
| `ci-agent/implement/spec_reader.go` | NEW — Spec file reader |
| `ci-agent/implement/spec_reader_test.go` | NEW — Spec reader tests |
| `ci-agent/implement/tracker.go` | NEW — Task progress tracker |
| `ci-agent/implement/tracker_test.go` | NEW — Tracker tests |
| `ci-agent/implement/adapter/adapter.go` | NEW — Adapter interface, request/response types |
| `ci-agent/implement/adapter/fakes/fake_adapter.go` | NEW — Counterfeiter-generated fake |
| `ci-agent/implement/adapter/prompt_test_gen.go` | NEW — Test generation prompt builder |
| `ci-agent/implement/adapter/prompt_impl_gen.go` | NEW — Implementation prompt builder |
| `ci-agent/implement/adapter/prompt_test.go` | NEW — Prompt tests |
| `ci-agent/implement/adapter/claude/claude.go` | NEW — Claude Code adapter |
| `ci-agent/implement/adapter/claude/claude_test.go` | NEW — Claude Code adapter tests |
| `ci-agent/implement/tdd/red.go` | NEW — Test writer + red-phase verifier |
| `ci-agent/implement/tdd/red_test.go` | NEW — Red phase tests |
| `ci-agent/implement/tdd/green.go` | NEW — Green-phase verifier |
| `ci-agent/implement/tdd/green_test.go` | NEW — Green phase tests |
| `ci-agent/implement/tdd/patch.go` | NEW — File patch applier |
| `ci-agent/implement/tdd/patch_test.go` | NEW — Patch tests |
| `ci-agent/implement/tdd/suite.go` | NEW — Full test suite runner |
| `ci-agent/implement/tdd/suite_test.go` | NEW — Suite runner tests |
| `ci-agent/implement/tdd/regression.go` | NEW — Regression check + rollback |
| `ci-agent/implement/tdd/regression_test.go` | NEW — Regression tests |
| `ci-agent/implement/tdd/loop.go` | NEW — Single-task TDD loop |
| `ci-agent/implement/tdd/loop_test.go` | NEW — TDD loop tests |
| `ci-agent/implement/git.go` | NEW — Git operations (stage, commit, revert, branch) |
| `ci-agent/implement/git_test.go` | NEW — Git operations tests |
| `ci-agent/implement/sequencer.go` | NEW — Multi-task sequencer |
| `ci-agent/implement/sequencer_test.go` | NEW — Sequencer tests |
| `ci-agent/implement/confidence.go` | NEW — Implementation confidence scorer |
| `ci-agent/implement/confidence_test.go` | NEW — Confidence tests |
| `ci-agent/implement/results.go` | NEW — Results builder |
| `ci-agent/implement/results_test.go` | NEW — Results builder tests |
| `ci-agent/implement/summary.go` | NEW — Summary Markdown renderer |
| `ci-agent/implement/summary_test.go` | NEW — Summary tests |
| `ci-agent/implement/orchestrator/orchestrator.go` | NEW — End-to-end orchestration |
| `ci-agent/implement/orchestrator/orchestrator_test.go` | NEW — Orchestrator tests |
| `ci-agent/implement/orchestrator/writer.go` | NEW — Output file writer |
| `ci-agent/implement/orchestrator/writer_test.go` | NEW — Writer tests |
| `cmd/ci-agent-implement/main.go` | NEW — CLI entrypoint |
| `cmd/ci-agent-implement/main_test.go` | NEW — CLI tests |
| `ci/tasks/ci-agent-implement.yml` | NEW — Concourse task definition |
| `ci/tasks/implement-gate.yml` | NEW — Gate companion task |
| `deploy/Dockerfile.ci-agent` | MODIFY — Add implement binary |
| `deploy/borg-pipeline.yml` | MODIFY — Add implement job |

## Environment Variables (Concourse Convention)

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `SPEC_DIR` | yes | — | Directory containing `spec.md` and `plan.md` (from planning agent) |
| `REPO_DIR` | yes | — | Repository directory to modify |
| `OUTPUT_DIR` | yes | — | Directory for output artifacts |
| `AGENT_CLI` | no | `claude` | Agent CLI binary name or path |
| `BRANCH_NAME` | no | `implement/agent-<ts>` | Branch name for implementation commits |
| `TEST_CMD` | no | `go test ./...` | Command to run full test suite |
| `MAX_RETRIES` | no | `2` | Max retry attempts per task phase (red, green, regression) |
| `MAX_CONSECUTIVE_FAILURES` | no | `3` | Consecutive task failures before aborting |
| `CONFIDENCE_THRESHOLD` | no | `0.7` | Minimum confidence to report `pass` |
| `TIMEOUT` | no | `30m` | Maximum total execution time |

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

- name: agent-implement
  plan:
  - get: repo
    passed: [agent-plan]
  - get: plan-output
    passed: [agent-plan]
  - task: implement
    file: ci/tasks/ci-agent-implement.yml
    input_mapping: { repo: repo, plan-output: plan-output }
    output_mapping: { implemented-repo: implemented-repo, implement-output: implement-output }
    params:
      AGENT_CLI: claude
      TEST_CMD: "go test ./..."
      MAX_RETRIES: "2"
      CONFIDENCE_THRESHOLD: "0.7"
  - task: implement-gate
    file: ci/tasks/implement-gate.yml
    input_mapping: { implement-output: implement-output }
    params:
      CONFIDENCE_THRESHOLD: "0.7"
  - put: repo
    inputs: [implemented-repo]
    params:
      repository: implemented-repo

# Downstream: review the implementation
- name: agent-review
  plan:
  - get: repo
    passed: [agent-implement]
  - task: ai-review
    file: ci/tasks/ci-agent-review.yml
    # ...
```

## Task Definition (`ci/tasks/ci-agent-implement.yml`)

```yaml
platform: linux
image_resource:
  type: registry-image
  source:
    repository: ghcr.io/jetbridge/ci-agent
    tag: latest

inputs:
- name: repo
- name: plan-output

outputs:
- name: implemented-repo
- name: implement-output

params:
  AGENT_CLI: claude
  BRANCH_NAME: ""
  TEST_CMD: "go test ./..."
  MAX_RETRIES: "2"
  MAX_CONSECUTIVE_FAILURES: "3"
  CONFIDENCE_THRESHOLD: "0.7"
  TIMEOUT: "30m"
  SPEC_DIR: plan-output
  REPO_DIR: repo
  OUTPUT_DIR: implement-output

run:
  path: /usr/local/bin/ci-agent-implement
```
