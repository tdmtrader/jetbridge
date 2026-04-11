# Implementation Plan: Exec Step Behavioral Specification

## Phase 1: Get Step Behavioral Spec & Audit ✅

- [x] Finalize get step requirements (GS-01 through GS-10)
- [x] Audit `atc/exec/get_step_test.go`
- [x] Gaps identified: GS-03 (WaitingForWorker), GS-04 (fromCache=true)

---

## Phase 2: Put Step Behavioral Spec & Audit ✅

- [x] Finalize put step requirements (PS-01 through PS-07)
- [x] Audit `atc/exec/put_step_test.go`
- [x] Gap identified: PS-06 (named input not found — no test)

---

## Phase 3: Task Step Behavioral Spec & Audit ✅

- [x] Finalize task step requirements (TS-01 through TS-12)
- [x] Audit `atc/exec/task_step_test.go`
- [x] Gap identified: TS-10 (sidecar failure path)

---

## Phase 4: Set-Pipeline and Load-Var Spec & Audit ✅

- [x] Finalize set-pipeline requirements (SP-01 through SP-04)
- [x] Audit `atc/exec/set_pipeline_step_test.go` — full coverage, no gaps
- [x] Finalize load-var requirements (LV-01 through LV-05)
- [x] Audit `atc/exec/load_var_step_test.go` — full coverage, no gaps

---

## Phase 5: Composite Steps — Sequential and Parallel Spec & Audit ✅

- [x] Finalize do step requirements (DO-01, DO-02) — corrected: do is planner-level, not exec
- [x] Finalize in_parallel requirements (IP-01 through IP-04) — full coverage
- [x] Finalize across requirements (AC-01 through AC-03) — full coverage

---

## Phase 6: Hook and Control Flow Steps Spec & Audit ✅

- [x] Finalize retry requirements (RT-01 through RT-03) — RT-02 clarified for DeadlineExceeded
- [x] Finalize timeout requirements (TO-01, TO-02) — full coverage
- [x] Finalize hook requirements (HS-01, HF-01, HE-01/HE-02, HA-01, EN-01, TR-01)
- [x] HE-02 added: retriable errors skip on_error hook

---

## Phase 7: Artifact Repository & RunState Contract Spec & Audit ✅

- [x] Finalize artifact repository requirements (AR-01 through AR-06)
- [x] Audit `atc/exec/build/repository_test.go` — AR-02 partial (no concurrency test)
- [x] Finalize RunState result requirements (RS-01 through RS-04)
- [x] Audit `atc/exec/run_state_test.go` — full coverage

---

## Phase 8: Coverage Matrix & Report ✅

- [x] Produced `coverage_matrix.md` with all 73 requirements mapped
- [x] Overall coverage: 62/73 covered (85%), 9 partial, 1 missing, 1 spec correction
- [x] Spec updated with: HE-02, RT-02 clarification, DO-01/DO-02 planner correction
- [x] Priority recommendations documented in coverage matrix

---

## Phase 9: Write Missing Tests (Implementation Track) ✅

Based on coverage matrix, add these tests (ordered by priority):

- [x] **GS-04**: Add assertion that cache hit registers artifact with `fromCache=true`
  - File: `atc/exec/get_step_test.go` — "when cache is present on selected worker" context
- [x] **PS-06**: Add test: specified input artifact not found returns error
  - File: `atc/exec/put_step_test.go` — new context under "inputs"
- [x] **SE-03**: Add delegate callback ordering test (ordered recorder)
  - File: `atc/exec/get_step_test.go` — verified actual order: Initializing → Starting → BeforeSelectWorker → SelectedWorker → Finished
- [x] **GS-03**: Verify lock retry mechanism via AcquireCallCount >= 3
  - File: `atc/exec/get_step_test.go` — "when lock isn't initially acquired" context (WaitingForWorker not called in impl; lock retry verified instead)
- [x] **AR-02**: Add concurrent access test for artifact repository
  - File: `atc/exec/build/repository_test.go` — two concurrent `It` blocks with sync.WaitGroup
- [x] **SE-04**: Add test that verifies Finished XOR Errored (never both) across key error paths
  - File: `atc/exec/get_step_test.go` — success path (Finished=1, Errored=0) and timeout path (Errored=1, Finished=0)
- [x] Phase 9 verification: all new tests pass — 538/538 exec specs + 18/18 build specs green

---
