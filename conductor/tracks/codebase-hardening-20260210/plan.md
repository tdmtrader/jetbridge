# Plan: Codebase Hardening

## Phase 1: Commit Outstanding Test Work

> Preserve existing test enhancements before making further changes.

- [x] f165e90 **Task 1: Stage and commit uncommitted test files and untracked integration tests**
  - Stage modified: `ci-agent/fix/orchestrator_test.go`, `ci-agent/implement/orchestrator/orchestrator_test.go`, `ci-agent/orchestrator/orchestrator_test.go`, `ci-agent/orchestrator/qa_orchestrator_test.go`
  - Stage untracked: `atc/api/agentfeedback/handler_integration_test.go`, `atc/worker/jetbridge/live_sidecar_test.go`, `ci-agent/integration/`, `deploy/build-job.yaml`
  - Commit with `chore(tests): commit outstanding test enhancements and integration tests`
  - Verify all tests pass after commit

## Phase 2: Wire Agent Feedback API into ATC

> Critical gap — feedback endpoints exist but are unreachable in production.

- [x] **Task 2: Write tests for feedback API route registration**
  - Add test cases verifying that feedback routes are registered and return non-404 responses
  - Test: POST `/api/v1/agent/feedback` returns 200/400 (not 404)
  - Test: GET `/api/v1/agent/feedback` returns 200 (not 404)
  - Test: GET `/api/v1/agent/feedback/summary` returns 200 (not 404)
  - Test: POST `/api/v1/agent/feedback/classify` returns 200/400 (not 404)

- [x] 3b15907 **Task 3: Implement feedback API route registration**
  - Add route constants to `atc/routes.go`: SubmitFeedback, GetFeedback, GetFeedbackSummary, ClassifyVerdict
  - Add rata.Route entries for the 4 endpoints
  - Wire feedback handler in `atc/api/handler.go` NewHandler(): create MemoryFeedbackStore, create handler, add 4 handler entries
  - Verify tests from Task 2 pass

## Phase 3: Fix QA Orchestrator Wiring

> Browser plan generation receives nil agent, limiting its capability.

- [x] f7fa709 **Task 4: Write test for browser plan agent parameter**
  - Add test case in `ci-agent/orchestrator/qa_orchestrator_test.go` verifying that GenerateBrowserPlan receives the agent runner (not nil) when one is configured

- [x] f7fa709 **Task 5: Implement browser plan agent wiring**
  - Update `ci-agent/orchestrator/qa_orchestrator.go` to pass `opts.Agent` to `browserplan.GenerateBrowserPlan()` instead of `nil`
  - Verify test from Task 4 passes

## Phase 4: Unify Verdict Classifiers

> Two independent classifiers produce inconsistent results.

- [x] 7508897 **Task 6: Write tests for unified classifier**
  - Add test cases validating the consolidated classifier handles all verdict types from both the ci-agent schema and the ATC handler
  - Test keyword matching, pattern matching, and edge cases in a single test file

- [x] 7508897 **Task 7: Implement unified classifier**
  - Updated `atc/api/agentfeedback/handler.go` default classifier to match ci-agent keyword set exactly
  - Added ClassifyFunc dependency injection via WithClassifier option
  - Replaced inline classifyText with defaultClassifyText (same keywords, negation, best-confidence-wins)
  - Verify tests from Task 6 pass and existing handler tests still pass

## Phase 5: Add Missing Unit Tests — JetBridge

> Core K8s execution and error handling code lacks unit tests.

- [x] 9ca6df9 **Task 8: Write tests for `atc/worker/jetbridge/errors.go`**
  - Test `TransientError` wraps and unwraps correctly
  - Test `IsRetryable()` returns true
  - Test `wrapIfTransient()` wraps K8s server errors (429, 500, 503, 504)
  - Test `wrapIfTransient()` wraps network errors (url.Error, net.Error)
  - Test `wrapIfTransient()` passes through non-transient errors unchanged
  - Test `wrapIfTransient()` returns nil for nil input

- [x] 9ca6df9 **Task 9: Write tests for `atc/worker/jetbridge/executor.go`**
  - Test executor creation and configuration
  - Test ExecExitError message formatting
  - Note: ExecInPod integration tested via live_test.go (requires real K8s cluster)

## Phase 6: Add Missing Unit Tests — CI Agent

> Output writing, agent mapping, and gap generation lack coverage.

- [x] a9d6202 **Task 10: Write tests for `ci-agent/plan/orchestrator/writer.go`**
  - Test `WriteSpec()` creates spec.md with correct content and returns proper artifact
  - Test `WritePlan()` creates plan.md with correct content and returns proper artifact
  - Test `WriteResults()` marshals and writes results.json correctly
  - Test error cases (invalid directory, permission errors)

- [x] a9d6202 **Task 11: Write tests for `ci-agent/mapper/agent_mapper.go`**
  - Test `RefineMapping()` with nil agent, mock agent, error, invalid JSON, unknown IDs, invalid status
  - Exercises `buildRefinementPrompt()` and `parseRefinedMappings()` through public API

- [x] a9d6202 **Task 12: Write tests for `ci-agent/gapgen/generator.go` and `executor.go`**
  - Test error handling: invalid JSON, agent errors, multi-gap independent processing
  - ClassifyGapResults already well-tested; ExecuteGapTests requires real Go toolchain

## Phase 7: Add Missing Integration Tests

> Cross-system integration scenarios have no test coverage.

- [ ] **Task 13: Write QA mode end-to-end integration test**
  - Create `ci-agent/integration/qa_test.go`
  - Test full pipeline: spec parse -> requirement mapping -> gap generation -> browser plan -> QA scoring
  - Use in-memory fakes for agent adapter

- [ ] **Task 14: Write Review -> Fix -> QA three-stage integration test**
  - Create `ci-agent/integration/review_fix_qa_test.go`
  - Test: review produces findings -> fix resolves issues -> QA validates fixes
  - Verify output artifacts chain correctly between stages

- [ ] **Task 15: Write ci-agent output -> Feedback API integration test**
  - Create `ci-agent/integration/feedback_api_test.go`
  - Test: orchestrator writes results.json -> parse results -> submit to feedback handler -> verify storage
  - Validate the feedback loop data flow

## Phase 8: Remove Stale and Deprecated Code

> Clean up dead references and no-op flags.

- [x] 471784e **Task 16: Remove stale baggageclaim test references**
  - Delete `topgun/k8s/baggageclaim_drivers_test.go`
  - Remove baggageclaim comments from `topgun/runtime/worker_failing_test.go`, `atc/exec/in_parallel_test.go`

- [x] 471784e **Task 17: Remove ancient TODOs**
  - Remove Go 1.8 TODO and dead else block in `fly/rc/target.go`
  - Clean up stale TODO in `atc/configvalidate/validate.go`

- [x] 471784e **Task 18: Remove deprecated no-op CLI flags**
  - Remove `EnableRedactSecrets`, `EnableAcrossStep`, `EnablePipelineInstances` from `atc/atccmd/command.go`
  - Remove `EnableAcrossStep` from `fly/commands/validate_pipeline.go`

## Phase 9: Final Verification

- [x] **Task 19: Run full test suite and verify all binaries compile**
  - `go build ./cmd/concourse` — passes
  - `go build ./cmd/ci-agent-{review,fix,plan,qa,implement}` — all pass
  - `go test ./...` in ci-agent — 20/20 packages pass
  - `go test` in atc/api/agentfeedback, atc/worker/jetbridge, fly/rc, atc/configvalidate — all pass
  - `go vet` — clean (1 pre-existing unkeyed field warning in atc/exec test)
