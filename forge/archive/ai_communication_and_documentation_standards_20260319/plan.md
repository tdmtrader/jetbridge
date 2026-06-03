# Implementation Plan: CI Agent Binary Consolidation

## Phase 1: Shared Infrastructure

Extract duplicated code into common packages. No behavioral changes — existing binaries continue to work.

- [x] Task: Create `llm` package with `Client` interface and Claude CLI implementation
- [x] Task: Create `envconfig` package with shared env var helpers
- [x] Task: Create `phaseconfig` package for YAML phase config loading
- [x] Task: Create `provenance` package for config hashing and result metadata
- [x] Task: Phase 1 Manual Verification — 35 tests pass, no regressions

---

## Phase 2: Linear Orchestrator

Build the single orchestration engine that all phases will use.

- [x] Task: Create `phaserunner` package with linear execution engine
- [x] Task: Create prompt template renderer with env var and step output substitution
- [x] Task: Create `ci-agent` binary entry point with `--phase` flag
- [x] Task: Phase 2 Manual Verification — 14 tests pass, binary builds clean

---

## Phase 3: Migrate Existing Phases

Create phase YAML + prompt templates for each existing binary.

- [x] Task: Create `phases/plan.yaml` + prompt templates (`prompts/plan/spec.md`, `prompts/plan/plan.md`)
- [x] Task: Create `phases/implement.yaml` + prompt template (`prompts/implement/tasks.md`)
- [x] Task: Create `phases/review.yaml` + prompt template (`prompts/review/findings.md`)
- [x] Task: Create `phases/fix.yaml` + prompt template (`prompts/fix/apply.md`)
- [x] Task: Create `phases/qa.yaml` + prompt template (`prompts/qa/coverage.md`)
- [x] Task: Integration tests validating all 5 phase configs load and run with fake LLM
- [x] Task: Phase 3 Manual Verification — all phase configs parse, run, and produce expected artifacts

---

## Phase 4: Backward Compatibility & Cleanup

- [x] Task: Create backward-compatible wrapper scripts (`scripts/ci-agent-{plan,implement,review,fix,qa}`)
- [ ] Task: Update existing integration tests to use new binary (deferred — old tests still pass against old code)
- [ ] Task: Remove old orchestrator code (deferred — keeping old code during transition period)
- [x] Task: Phase 4 Manual Verification — 32/32 packages pass, zero regressions, binary builds clean

---

## Summary

New packages: `llm`, `envconfig`, `phaseconfig`, `provenance`, `phaserunner`
New binary: `cmd/ci-agent/main.go` (single binary with `--phase` flag)
New configs: `phases/{plan,implement,review,fix,qa}.yaml`
New prompts: `phases/prompts/{plan,implement,review,fix,qa}/*.md`
Wrapper scripts: `scripts/ci-agent-{plan,implement,review,fix,qa}`
Total new tests: 49 specs across 5 new packages
Regressions: 0 (all 32 existing packages still pass)

### Deferred (intentional)

- **Old code removal**: The 5 old `cmd/ci-agent-*` binaries and their orchestrators are still present. Removing them is a destructive operation best done after the new binary has been validated in a real pipeline run.
- **Integration test migration**: Existing integration tests (`integration/plan_implement_test.go`, etc.) still test the old binaries. Migrating them to the new binary should happen alongside old code removal.
