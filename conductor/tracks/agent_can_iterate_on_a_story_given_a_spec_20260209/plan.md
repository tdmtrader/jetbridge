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

- [ ] Write tests for Claude Code test gen adapter
  - Constructs correct CLI invocation: `claude -p "<prompt>" --output-format json`
  - Parses structured JSON output into `TestGenResponse`
  - Handles timeout (context deadline), non-zero exit, malformed JSON
  - Captures stderr for error reporting
- [ ] Implement Claude Code test gen adapter
  - New file: `ci-agent/implement/adapter/claude/claude.go`
  - Implements `Adapter` interface
  - `GenerateTest` method: builds prompt, invokes CLI, parses response

### Task 16: Claude Code adapter for implementation generation

- [ ] Write tests for Claude Code impl gen adapter
  - Constructs correct CLI invocation with implementation prompt
  - Parses output into `ImplGenResponse` with `[]FilePatch`
  - Handles same error cases as test gen
  - Rejects patches that modify the test file (enforced in prompt and post-parse)
- [ ] Implement Claude Code impl gen adapter
  - Extends `ci-agent/implement/adapter/claude/claude.go`
  - `GenerateImpl` method: builds prompt, invokes CLI, parses response, validates patches

- [ ] Phase 6 Checkpoint — adapter dispatches to Claude Code CLI, parses responses. Run: `go test ./implement/adapter/...`

---

## Phase 7: Confidence & Results

### Task 17: Implementation confidence scorer

- [ ] Write tests for confidence scoring
  - All tasks committed: confidence = 1.0
  - Half committed, half skipped (pre-satisfied): confidence = 0.9 (pre-satisfied is fine)
  - Half committed, half failed: confidence = 0.5
  - All failed: confidence = 0.0
  - One committed, rest failed: confidence proportional to committed/total, with penalty
  - Full test suite passing at end: +0.1 bonus (capped at 1.0)
  - Full test suite failing at end: override confidence to 0.0, status = fail
- [ ] Implement confidence scorer
  - New file: `ci-agent/implement/confidence.go`
  - `ScoreConfidence(tracker *TaskTracker, suitePass bool) *ConfidenceResult`
  - `ConfidenceResult{Score float64, Status schema.Status, Breakdown map[string]float64}`

### Task 18: Results builder

- [ ] Write tests for results builder
  - Builds `schema.Results` from tracker summary, confidence, and artifact list
  - Artifacts include: `summary.md`, `progress.json`, plus any generated test files
  - Status maps from confidence: >= threshold → pass, < threshold → fail, abstain when input insufficient
  - Summary text includes: tasks completed/total, test pass rate, list of failures
  - Metadata includes: repo path, branch, agent CLI used, duration
- [ ] Implement results builder
  - New file: `ci-agent/implement/results.go`
  - `BuildResults(tracker, confidence, opts) *schema.Results`

### Task 19: Summary renderer

- [ ] Write tests for summary renderer
  - Renders Markdown with: H1 title, overview stats table, per-phase sections with task results
  - Each task shows: status icon, description, commit SHA (if committed), test file path, failure reason (if failed)
  - Footer shows: total duration, final test suite status, confidence score
- [ ] Implement summary renderer
  - New file: `ci-agent/implement/summary.go`
  - `RenderSummary(tracker *TaskTracker, confidence *ConfidenceResult, duration time.Duration) (string, error)`

- [ ] Phase 7 Checkpoint — confidence scoring, results, and summary work. Run: `go test ./implement/... -run "Confidence|Results|Summary"`

---

## Phase 8: Orchestrator & CLI

### Task 20: Orchestrator (wires all components)

- [ ] Write tests for orchestrator
  - Full pipeline: read spec → parse plan → init tracker → create branch → run sequencer → final suite check → score confidence → build results → write outputs
  - Writes `results.json`, `events.ndjson`, `summary.md`, `progress.json` to output directory
  - Events log includes: `agent.start`, `implement.plan_parsed`, `implement.task_start`, `implement.red`, `implement.green`, `implement.committed`, `implement.task_end` (per task), `implement.suite_check`, `implement.confidence_scored`, `artifact.written`, `agent.end`
  - Exit code 0 when status is pass; 1 when fail or error
  - Abstain mode: plan has no parseable tasks → status `abstain`, skip execution
  - Handles adapter errors gracefully: partial progress saved, status = error
  - Resumes from existing `progress.json` if present (idempotent restart)
- [ ] Implement orchestrator
  - New file: `ci-agent/implement/orchestrator/orchestrator.go`
  - `Run(ctx context.Context, opts Options) (*schema.Results, error)`
  - `Options{SpecDir, RepoDir, OutputDir, AgentCLI, BranchName, TestCmd, MaxRetries, MaxConsecutiveFailures, ConfidenceThreshold, Timeout}`

### Task 21: Output writer

- [ ] Write tests for output writer — creates output dir, writes all files, permissions 0644, results.json validates against schema
- [ ] Implement output writer
  - New file: `ci-agent/implement/orchestrator/writer.go`
  - `WriteResults(outputDir, results)`, `WriteSummary(outputDir, summary)`, `WriteProgress(outputDir, tracker)`

### Task 22: CLI binary

- [ ] Write tests for CLI argument parsing
  - Reads env vars: `SPEC_DIR` (contains spec.md + plan.md), `REPO_DIR`, `OUTPUT_DIR`
  - Optional: `AGENT_CLI` (default: `claude`), `BRANCH_NAME` (default: `implement/agent-<timestamp>`), `TEST_CMD` (default: `go test ./...`), `MAX_RETRIES` (default: `2`), `MAX_CONSECUTIVE_FAILURES` (default: `3`), `CONFIDENCE_THRESHOLD` (default: `0.7`), `TIMEOUT` (default: `30m`)
  - Validates: `SPEC_DIR` exists with `spec.md` and `plan.md`, `REPO_DIR` exists and is a git repo
  - Creates output directory if not exists
- [ ] Implement CLI entrypoint
  - New file: `cmd/ci-agent-implement/main.go`
  - Reads env vars per Concourse convention
  - Calls orchestrator, exit code from result status

- [ ] Phase 8 Checkpoint — CLI runs end-to-end with fake adapter, produces valid output files. Run: `go test ./implement/... && go build ./cmd/ci-agent-implement/`

---

## Phase 9: Concourse Integration

### Task 23: Task YAML definitions

- [ ] Write `ci/tasks/ci-agent-implement.yml` task definition
  - Inputs: `repo` (required), `plan-output` (required, contains spec.md + plan.md from planning agent)
  - Outputs: `implemented-repo` (repo with commits), `implement-output` (results.json, events.ndjson, summary.md, progress.json)
  - Params with defaults; runs `/usr/local/bin/ci-agent-implement`
- [ ] Write `ci/tasks/implement-gate.yml` companion task
  - Input: `implement-output` (reads results.json)
  - Params: `CONFIDENCE_THRESHOLD`
  - Script: parse results.json, check status + confidence, exit 0/1
- [ ] Validate task definitions with `fly validate-pipeline`

### Task 24: Container image

- [ ] Add `ci-agent-implement` binary to `deploy/Dockerfile.ci-agent` (extends existing image)
- [ ] Build and test image locally

- [ ] Phase 9 Checkpoint — task definitions valid, image builds. Run: `fly validate-pipeline`

---

## Phase 10: Self-Test & Pipeline Integration

### Task 25: Self-test validation

- [ ] Create a synthetic spec.md + plan.md fixture (small: 2 phases, 3 tasks, targeting a temp Go module with a trivial feature)
- [ ] Run `ci-agent-implement` against the synthetic fixture with a fake adapter that returns predetermined test/impl code
- [ ] Verify `results.json` validates against schema
- [ ] Verify `progress.json` shows all tasks committed
- [ ] Verify `summary.md` has expected sections
- [ ] Verify `events.ndjson` has valid chronological events
- [ ] Verify git log shows one commit per task with conventional messages
- [ ] Verify confidence is high when all tasks succeed

### Task 26: Pipeline integration

- [ ] Add implement job to `deploy/borg-pipeline.yml`
  - Wired after `agent-plan`, receives `plan-output` as input
  - Uses `ci-agent-implement` task
  - Output repo available for downstream review/PR steps
- [ ] Verify pipeline DAG runs correctly end-to-end

- [ ] Phase 10 Checkpoint — self-test produces valid output, pipeline integrates cleanly

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
