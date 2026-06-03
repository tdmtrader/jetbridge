# Implementation Plan: Deprecate Old Agent Paths and Update Tests

## Phase 1: Remove Old Entry Points and Wrappers

- [x] Task: Delete old binary entry points (`cmd/ci-agent-review/`, `cmd/ci-agent-fix/`, `cmd/ci-agent-plan/`, `cmd/ci-agent-implement/`, `cmd/ci-agent-qa/`) fca6d2ef2
- [x] Task: Delete wrapper scripts (`ci-agent/scripts/ci-agent-review`, `ci-agent-fix`, `ci-agent-plan`, `ci-agent-implement`, `ci-agent-qa`) fca6d2ef2
- [x] Task: Delete any checked-in compiled binaries (`ci-agent/ci-agent-review`, `ci-agent-fix`, `ci-agent-plan`, `ci-agent-qa`, `ci-agent-implement`) fca6d2ef2
- [x] Task: Phase 1 Manual Verification — confirm `go build ./cmd/ci-agent/` still succeeds fca6d2ef2

---

## Phase 2: Remove Old Orchestrator and Adapter Packages

- [x] Task: Delete `ci-agent/orchestrator/` package (old review/QA orchestrators, claude_agent, tests) 245cd8cc1
- [x] Task: Delete `ci-agent/plan/` package (orchestrator, adapters, input_parser, renderers, confidence, tests) 245cd8cc1
- [x] Task: Delete `ci-agent/fix/` package (engine, orchestrator, adapters, git, patch, tests) 245cd8cc1
- [x] Task: Delete `ci-agent/implement/` package (orchestrator, adapters, confidence, git, parser, tests) 245cd8cc1
- [x] Task: Audit remaining code for dangling imports to deleted packages; fix any compilation errors 245cd8cc1
- [x] Task: Verify `go vet ./...` and `go build ./...` pass in `ci-agent/` 245cd8cc1
- [x] Task: Phase 2 Manual Verification — confirm `go test ./...` passes in `ci-agent/` (excluding integration) 245cd8cc1

---

## Phase 3: Update Pipeline, Tasks, and Dockerfile

- [x] Task: Rewrite `ci/tasks/ci-agent-review.yml` to build and invoke `ci-agent --phase phases/review.yaml` f39fe7e9b
- [x] Task: Rewrite `ci/tasks/ci-agent-fix.yml` to use `ci-agent --phase phases/fix.yaml` f39fe7e9b
- [x] Task: Rewrite `ci/tasks/ci-agent-plan.yml` to use `ci-agent --phase phases/plan.yaml` f39fe7e9b
- [x] Task: Rewrite `ci/tasks/ci-agent-implement.yml` to use `ci-agent --phase phases/implement.yaml` f39fe7e9b
- [x] Task: Rewrite `ci/tasks/ci-agent-qa.yml` to use `ci-agent --phase phases/qa.yaml` f39fe7e9b
- [x] Task: Update `deploy/agent-pipeline.yml` — `build-agents` job compiles only unified `ci-agent`; test jobs invoke via `--phase` f39fe7e9b
- [x] Task: Rewrite `deploy/Dockerfile.ci-agent` to build only `ci-agent` binary and copy phase configs into the image f39fe7e9b
- [x] Task: Phase 3 Manual Verification — review all YAML/Dockerfile changes for correctness f39fe7e9b

---

## Phase 4: Update Integration Tests

- [x] Task: Rewrite `ci-agent/integration/review_fix_test.go` to use phase runner (review.yaml → fix.yaml) 14625383e
- [x] Task: Rewrite `ci-agent/integration/review_fix_qa_test.go` to use phase runner (review.yaml → fix.yaml → qa.yaml) 14625383e
- [x] Task: Rewrite `ci-agent/integration/plan_implement_test.go` to use phase runner (plan.yaml → implement.yaml) 14625383e
- [x] Task: Rewrite `ci-agent/integration/qa_test.go` to use phase runner (qa.yaml) 14625383e
- [x] Task: Verify all integration tests pass: `go test ./ci-agent/integration/ -v -count=1` 14625383e
- [x] Task: Phase 4 Manual Verification — confirm no test references old binary names 14625383e

---

## Phase 5: Update Documentation

- [x] Task: Update `forge/product.md` — replace references to 5 separate agent binaries with unified `ci-agent --phase` description 789fe09dd
- [x] Task: Grep entire repo (excluding `forge/archive/` and `.claude/worktrees/`) for stale references to old binary names; update or remove any found 789fe09dd
- [x] Task: Phase 5 Manual Verification — final review of all changes, confirm no stale references remain 789fe09dd

---
