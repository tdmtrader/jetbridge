# Implementation Plan: CI Agent QA Task

## Phase 1: Spec Parser

### Task 1: Requirement extraction from Markdown

- [x] 76e099f42 Write tests for spec parser (9 tests)
- [x] 76e099f42 Implement spec parser

- [x] Phase 1 Checkpoint [checkpoint: 76e099f42] — spec parser tested, extracts requirements + AC from Markdown

---

## Phase 2: QA Schema & Config

### Task 2: Define QA output schema types

- [x] 76e099f42 Write tests for QA schema (12 tests)
- [x] 76e099f42 Implement QA schema types

### Task 3: Define QA config and profiles

- [x] 76e099f42 Write tests for QA config (9 tests)
- [x] 76e099f42 Implement QA config types

- [x] Phase 2 Checkpoint [checkpoint: 76e099f42] — QA schema + config tested, validation works

---

## Phase 3: Requirement Mapper

### Task 4: Test discovery and requirement mapping

- [x] 2e8917446 Write tests for requirement mapper (9 tests)
- [x] 2e8917446 Implement requirement mapper

### Task 5: Agent-assisted mapping refinement

- [x] 2e8917446 Implement agent mapper adapter (graceful fallback to automated mapping)

- [x] Phase 3 Checkpoint [checkpoint: 2e8917446] — mapper finds existing tests, keyword matching with confidence

---

## Phase 4: Gap Test Generator

### Task 6: Generate tests for uncovered requirements

- [x] 2e8917446 Write tests for gap test generator (4 tests)
- [x] 2e8917446 Implement gap test generator

### Task 7: Execute generated tests and classify results

- [x] 2e8917446 Write tests for gap test classification (4 tests)
- [x] 2e8917446 Implement gap test executor and classifier

- [x] Phase 4 Checkpoint [checkpoint: 2e8917446] — gap tests generated, results classified correctly

---

## Phase 5: Coverage Scorer

### Task 8: Compute QA coverage score

- [x] 2e8917446 Write tests for QA scoring (7 tests)
- [x] 2e8917446 Implement QA scorer

- [x] Phase 5 Checkpoint [checkpoint: 2e8917446] — scoring is deterministic, gaps extracted correctly

---

## Phase 6: Browser QA Plan Generator

### Task 9: Generate browser QA plan

- [x] 636abf673 Write tests for browser plan generator (5 tests)
- [x] 636abf673 Implement browser plan generator

- [x] Phase 6 Checkpoint [checkpoint: 636abf673] — browser plan generated with flows and steps

---

## Phase 7: Orchestrator & CLI

### Task 10: QA orchestrator

- [x] 636abf673 Write tests for orchestrator (5 tests)
- [x] 636abf673 Implement orchestrator

### Task 11: CLI binary

- [x] 636abf673 Implement CLI entrypoint with env var parsing
- [x] 636abf673 Binary builds successfully

- [x] Phase 7 Checkpoint [checkpoint: 636abf673] — CLI runs end-to-end, produces valid qa.json

---

## Phase 8: Database Storage & Concourse Integration

### Task 12: PostgreSQL QA history

- [ ] Deferred: PostgreSQL storage not implemented (in-memory store used)

### Task 13: Task YAML definitions

- [x] ad6f41b8c Write ci-agent-qa.yml task definition
- [x] ad6f41b8c Write qa-gate.yml companion task
- [x] ad6f41b8c Validate with fly validate-pipeline

### Task 14: Container image update

- [x] ad6f41b8c Update Dockerfile.ci-agent to include ci-agent-qa binary

- [x] Phase 8 Checkpoint [checkpoint: ad6f41b8c] — task definitions valid, fly validate-pipeline passes

---

## Phase 9: Self-QA & Pipeline Integration

### Task 15: Self-QA validation

- [x] ad6f41b8c CLI smoke test: 40 requirements parsed, 39 mapped, qa.json valid, browser plan generated

### Task 16: Pipeline integration

- [x] ad6f41b8c Added agent-qa job to deploy/borg-pipeline.yml

- [x] Phase 9 Checkpoint — self-QA produces valid output, pipeline validated

---

## Key Files

| File | Change |
|------|--------|
| `ci-agent/specparser/specparser.go` | NEW |
| `ci-agent/specparser/specparser_test.go` | NEW |
| `ci-agent/schema/qa.go` | NEW |
| `ci-agent/schema/qa_test.go` | NEW |
| `ci-agent/config/qa_config.go` | NEW |
| `ci-agent/config/qa_config_test.go` | NEW |
| `ci-agent/mapper/mapper.go` | NEW |
| `ci-agent/mapper/agent_mapper.go` | NEW |
| `ci-agent/mapper/mapper_test.go` | NEW |
| `ci-agent/gapgen/generator.go` | NEW |
| `ci-agent/gapgen/executor.go` | NEW |
| `ci-agent/gapgen/gapgen_test.go` | NEW |
| `ci-agent/scoring/qa_scoring.go` | NEW |
| `ci-agent/scoring/qa_scoring_test.go` | NEW |
| `ci-agent/browserplan/browserplan.go` | NEW |
| `ci-agent/browserplan/browserplan_test.go` | NEW |
| `ci-agent/orchestrator/qa_orchestrator.go` | NEW |
| `ci-agent/orchestrator/qa_orchestrator_test.go` | NEW |
| `cmd/ci-agent-qa/main.go` | NEW |
| `ci/tasks/ci-agent-qa.yml` | NEW |
| `ci/tasks/qa-gate.yml` | NEW |
| `deploy/Dockerfile.ci-agent` | MODIFY |
| `deploy/borg-pipeline.yml` | MODIFY |
