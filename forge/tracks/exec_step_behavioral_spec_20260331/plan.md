# Implementation Plan: Exec Step Behavioral Specification

## Phase 1: Get Step Behavioral Spec & Audit

Write behavioral requirements for the get step, then audit existing tests against them.

- [ ] Finalize get step requirements (GS-01 through GS-10) — review against `atc/exec/get_step.go`
- [ ] Audit `atc/exec/get_step_test.go` — map each existing test to a requirement ID
- [ ] Identify gaps: requirements with zero or partial test coverage
- [ ] Identify redundant tests: tests that only assert on fake call counts with no behavioral value
- [ ] Phase 1 verification: get step coverage matrix complete

---

## Phase 2: Put Step Behavioral Spec & Audit

Write behavioral requirements for the put step, then audit existing tests.

- [ ] Finalize put step requirements (PS-01 through PS-07) — review against `atc/exec/put_step.go`
- [ ] Audit `atc/exec/put_step_test.go` — map each existing test to a requirement ID
- [ ] Identify gaps and redundant tests
- [ ] Phase 2 verification: put step coverage matrix complete

---

## Phase 3: Task Step Behavioral Spec & Audit

Write behavioral requirements for the task step, then audit existing tests.

- [ ] Finalize task step requirements (TS-01 through TS-12) — review against `atc/exec/task_step.go`
- [ ] Audit `atc/exec/task_step_test.go` — map each existing test to a requirement ID
- [ ] Identify gaps and redundant tests
- [ ] Phase 3 verification: task step coverage matrix complete

---

## Phase 4: Set-Pipeline and Load-Var Spec & Audit

Write behavioral requirements for set_pipeline and load_var, then audit existing tests.

- [ ] Finalize set-pipeline requirements (SP-01 through SP-04) — review against `atc/exec/set_pipeline_step.go`
- [ ] Audit `atc/exec/set_pipeline_step_test.go` — map tests to requirements
- [ ] Finalize load-var requirements (LV-01 through LV-05) — review against `atc/exec/load_var_step.go`
- [ ] Audit `atc/exec/load_var_step_test.go` — map tests to requirements
- [ ] Phase 4 verification: set-pipeline and load-var coverage matrices complete

---

## Phase 5: Composite Steps — Sequential and Parallel Spec & Audit

Write behavioral requirements for do, in_parallel, and across steps.

- [ ] Finalize do step requirements (DO-01, DO-02) — review against `atc/exec/do_step.go` or equivalent
- [ ] Finalize in_parallel requirements (IP-01 through IP-04) — review against `atc/exec/in_parallel.go`
- [ ] Finalize across requirements (AC-01 through AC-03) — review against `atc/exec/across_step.go`
- [ ] Audit existing tests for do, in_parallel, across — map to requirements
- [ ] Identify gaps and redundant tests
- [ ] Phase 5 verification: composite step coverage matrices complete

---

## Phase 6: Hook and Control Flow Steps Spec & Audit

Write behavioral requirements for retry, timeout, and all hook steps.

- [ ] Finalize retry requirements (RT-01 through RT-03) — review against `atc/exec/retry_step.go`
- [ ] Finalize timeout requirements (TO-01, TO-02) — review against `atc/exec/timeout_step.go`
- [ ] Finalize hook requirements (HS-01, HF-01, HE-01, HA-01, EN-01, TR-01) — review against hook files
- [ ] Audit existing tests for all hook/control steps — map to requirements
- [ ] Identify gaps and redundant tests
- [ ] Phase 6 verification: hook and control flow coverage matrices complete

---

## Phase 7: Artifact Repository & RunState Contract Spec & Audit

Write behavioral requirements for the shared state contracts.

- [ ] Finalize artifact repository requirements (AR-01 through AR-06) — review against `atc/exec/build/repository.go`
- [ ] Audit `atc/exec/build/repository_test.go` — map tests to requirements
- [ ] Finalize RunState result requirements (RS-01 through RS-04) — review against `atc/exec/run_state.go`
- [ ] Audit `atc/exec/run_state_test.go` — map tests to requirements
- [ ] Phase 7 verification: artifact and RunState coverage matrices complete

---

## Phase 8: Cross-Cutting Concerns & Final Coverage Report

Audit step execution contract and context propagation, produce final report.

- [ ] Audit step execution contract (SE-01 through SE-04) across all step types
- [ ] Verify context cancellation behavior (SE-02) is tested for key steps
- [ ] Verify delegate lifecycle ordering (SE-03, SE-04) is tested for each leaf step
- [ ] Produce consolidated coverage matrix: requirement ID x test status (covered/partial/missing/redundant)
- [ ] Prioritize missing coverage by risk: get/put/task > hooks > parallel > set-pipeline/load-var
- [ ] Document recommendations: which tests to rewrite as behavioral vs. keep vs. remove
- [ ] Phase 8 verification: final coverage report complete, priorities documented

---
