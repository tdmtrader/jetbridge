# Spec: Codebase Hardening

## Overview

This track addresses gaps, missing tests, unused code, and wiring issues identified during a comprehensive codebase review. The work spans three areas: (1) critical wiring fixes that prevent delivered features from functioning in production, (2) test coverage gaps for source files and integration scenarios with no tests, and (3) removal of deprecated/stale code that adds maintenance burden.

## Requirements

1. **Wire agent feedback API into ATC router** — Register SubmitFeedback, GetFeedback, GetSummary, and ClassifyVerdict routes in `atc/routes.go` and handler entries in `atc/api/handler.go` so the feedback endpoints are reachable in production.
2. **Commit uncommitted test enhancements** — Stage and commit the 3 modified test files (`ci-agent/fix/orchestrator_test.go`, `ci-agent/implement/orchestrator/orchestrator_test.go`, `ci-agent/orchestrator/qa_orchestrator_test.go`) plus untracked integration tests and the live sidecar test.
3. **Add unit tests for `atc/worker/jetbridge/executor.go`** — Cover core K8s execution logic with isolated unit tests.
4. **Add unit tests for `atc/worker/jetbridge/errors.go`** — Cover TransientError wrapping and `isTransientK8sError()` classification.
5. **Add unit tests for `ci-agent/plan/orchestrator/writer.go`** — Cover WriteSpec, WritePlan, WriteResults output functions.
6. **Add unit tests for `ci-agent/mapper/agent_mapper.go`** — Cover RefineMapping, buildRefinementPrompt, parseRefinedMappings.
7. **Add unit tests for `ci-agent/gapgen/generator.go` and `executor.go`** — Cover gap test generation and execution.
8. **Add QA mode integration test** — End-to-end test covering spec parse -> mapping -> gap generation -> browser plan.
9. **Add Review -> Fix -> QA three-stage integration test** — Validate the full multi-agent pipeline flow.
10. **Add ci-agent output -> Feedback API integration test** — Validate that actual results.json/events.ndjson output flows into the feedback handler.
11. **Unify duplicate verdict classifiers** — Consolidate `ci-agent/feedback/classifier.go` and `atc/api/agentfeedback/handler.go` classifyText into a single shared implementation.
12. **Fix browser plan nil agent** — Pass the QA agent runner (not nil) to `browserplan.GenerateBrowserPlan()` in `qa_orchestrator.go`.
13. **Remove stale baggageclaim test references** — Delete `topgun/k8s/baggageclaim_drivers_test.go` and clean up baggageclaim comments in other test files.
14. **Remove ancient TODOs** — Clean up the Go 1.8 TODO in `fly/rc/target.go` and other stale TODOs.
15. **Remove deprecated no-op CLI flags** — Remove `EnableRedactSecrets`, `EnableAcrossStep`, `EnablePipelineInstances` from `atc/atccmd/command.go` and `fly/commands/validate_pipeline.go`.

## Acceptance Criteria

- All 5 ci-agent binaries continue to compile cleanly.
- ATC compiles and agent feedback routes are reachable via `curl` against a running instance.
- All new and existing tests pass (`go test ./...` in both root and ci-agent modules).
- No source files listed in requirements 3-7 remain without corresponding `_test.go` files.
- Integration tests in requirements 8-10 exist and pass.
- Only one verdict classifier implementation exists.
- `browserplan.GenerateBrowserPlan` receives a non-nil agent.
- No references to baggageclaim remain in test files (except historical comments in non-test code).
- No deprecated no-op CLI flags remain.

## Out of Scope

- PostgreSQL-backed feedback store (separate track).
- PostgreSQL-backed QA history store (separate track).
- Removal of `atc/event/deprecated_events.go` (backward compatibility for old build logs — requires migration strategy).
- Removal of `DependentGetPlan`, legacy auth server, or deprecated Connection interface (backward compat).
- Sidecar error propagation hardening (separate track).
