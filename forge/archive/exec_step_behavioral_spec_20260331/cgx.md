# CGX — Exec Step Behavioral Specification

## Track Context
- **Track:** exec_step_behavioral_spec_20260331
- **Type:** refactor
- **Created:** 2026-03-31

## Session Log

### Session 1 — 2026-03-31 (completed)
- **Goal:** Create track with behavioral spec and audit plan
- **Approach:** Gap analysis across all subsystems, then deep-dive into exec steps
- **Findings:**
  - Exec steps have 526 tests across 26 files, all heavily implementation-coupled
  - DB layer (1,126 specs) and Fly CLI (647 specs) have the strongest behavioral coverage
  - Scheduler (117 specs), build tracker (0 Ginkgo specs), and worker lifecycle (1 spec) are also weak
  - Exec steps chosen as first priority due to: core pipeline execution path + highest refactoring risk
- **Decisions:**
  - Spec-first approach: write behavioral requirements, then audit existing tests against them
  - Skip event types for now (follow-up track)
  - ~65 tagged requirements across 10 sections
- **Outcome:** Track created, spec written, phases 1-8 completed in same session

### Session 1 continued — Phases 1-8

- **Goal:** Audit all 526 exec step tests against 73 behavioral requirements
- **Approach:** Read every test file in parallel batches; map tests to requirements
- **Coverage result:** 62/73 (85%) covered, 9 partial, 1 missing, 1 spec correction needed
- **Key findings:**
  - Hooks (on_success/failure/error/abort, ensure, try) are **well-covered** — behavioral, minimal mocking
  - Composite steps (in_parallel, across, retry, timeout) are **well-covered**
  - `do` step is NOT an exec-layer construct — it's a planner-level DoPlan transform (spec corrected)
  - `on_error` has an undocumented but tested behavior: retriable errors skip the hook (HE-02 added to spec)
  - Get step is mostly covered but `fromCache=true` assertion missing on cache hit (GS-04)
  - Put step missing one error case: named input not found (PS-06)
  - Delegate callback ordering (SE-03) is never verified as a sequence
- **Spec changes made:** HE-02 added, RT-02 clarified (DeadlineExceeded retried), DO-01/DO-02 rewritten to target planner layer
- **Next:** Phase 9 — write the 6 missing/partial tests

### Session 2 — 2026-03-31 (completed)
- **Goal:** Phase 9 — write all 6 missing/partial tests
- **Approach:** Edit test files directly; verify with `ginkgo ./atc/exec/ ./atc/exec/build/`
- **Tests written:**
  - `atc/exec/get_step_test.go`: GS-04 (fromCache=true on cache hit), GS-03 (lock retry AcquireCallCount), SE-03 (callback ordering), SE-04 ×2 (Finished XOR Errored on success and timeout)
  - `atc/exec/put_step_test.go`: PS-06 (PutInputNotFoundError for missing named input, worker not selected)
  - `atc/exec/build/repository_test.go`: AR-02 ×2 (concurrent RegisterArtifact/ArtifactFor/AsMap; concurrent parent+child scope reads)
- **Implementation discoveries:**
  - Get step actual callback order: `Initializing → Starting → BeforeSelectWorker → SelectedWorker → Finished` (Starting fires before worker selection, not after — spec SE-03 corrected)
  - `WaitingForWorker` is NOT called during lock contention (only stderr message written) — GS-03 tests `AcquireCallCount >= 3` instead
  - AR-02 concurrent test required `"sync"` import in repository_test.go
- **Final count:** 538 exec specs + 18 build specs, all green
- **Outcome:** Track complete — 73 behavioral requirements documented, 538 tests cover them, 12 new behavioral tests added
