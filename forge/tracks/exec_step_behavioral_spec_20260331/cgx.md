# CGX — Exec Step Behavioral Specification

## Track Context
- **Track:** exec_step_behavioral_spec_20260331
- **Type:** refactor
- **Created:** 2026-03-31

## Session Log

### Session 1 — 2026-03-31
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
- **Next:** Begin Phase 1 — Get step audit
