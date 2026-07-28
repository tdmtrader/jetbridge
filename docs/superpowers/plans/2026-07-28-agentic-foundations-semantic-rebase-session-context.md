# Agentic foundations semantic rebase — session operating context

Read this file at the start of every new root-agent or subagent session working
on this track.

## Why this context exists

This work was initially described as a rebase, but it became a semantic
reconstruction against the substantially changed Jetbridge v3 architecture.
The implementation plan contains 19 tasks and the current branch already spans
more than 160 files. Unbounded implementation/review cycles made the work much
slower and more token-intensive than intended.

The goal from this point forward is bounded, reviewable integration—not
unlimited hardening or adjacent platform design.

## Workspace and branch

- Work only in:
  `/Users/tdmtrader/concourse/concourse/.worktrees/agentic-platform-rebase`
- Branch: `codex/agentic-platform-rebase`
- Preserve the old foundations worktree as reference only.
- Do not push, merge, open a PR, or modify the main checkout unless the user
  explicitly requests it.
- Preserve unrelated/user changes. Never discard a dirty worktree to obtain a
  clean state.

## Model and agent policy

- Prefer `gpt-5.6-terra` for implementation, investigation, and review agents.
- Reserve `gpt-5.6-sol` for a genuinely complex blocker that Terra could not
  resolve, or when the user explicitly asks for Sol.
- Use one implementation agent per shared-worktree task.
- Use concise task briefs and limited context forks. Do not fork the full
  transcript when a small written brief is sufficient.
- Do not create parallel reviewers for the same delta.

## Scope discipline

- Implement only the current task's explicit acceptance criteria.
- Do not turn a rebase fix into a new general platform abstraction unless the
  task cannot be made correct without it.
- When an adjacent improvement is useful but not blocking correctness,
  security, migration safety, or the current acceptance test, record it in
  `2026-07-28-agentic-foundations-semantic-rebase-deferred-items.md` and
  continue.
- Do not expand a task merely to make every related package ideal.
- Known unrelated failures must be documented, not opportunistically repaired
  inside the current task.

## Review budget

- Fix only Critical, High, or otherwise genuinely blocking findings:
  correctness failures, security-boundary violations, data loss/corruption,
  migration hazards, or a required acceptance test that cannot pass.
- Record Medium, Minor, cleanup, optimization, speculative hardening, and
  additional-nice-to-have coverage in the deferred-item catalog.
- A task gets at most **two review rounds total**:
  1. initial focused review;
  2. one focused re-review of blocking fixes.
- If a blocking finding remains or a new blocker appears after the second
  round, mark the task **Human Review Required**, record the exact evidence and
  proposed choices, and proceed to the next safe independent task.
- Review only the relevant delta. Do not repeatedly re-audit already approved
  history without new evidence that it regressed.

### Task 6 exception at this checkpoint

Task 6 already exceeded the review budget before these rules were adopted.
Finish the currently known Critical Secret-substitution/TOCTOU fix and perform
exactly one final blocking-only review. If that review finds another blocker,
mark Task 6 **Human Review Required** and stop iterating on it.

## Test budget

- Use focused unit/integration tests while implementing.
- Run broad package suites once at the task checkpoint, not after every edit.
- Run the repository-wide acceptance suite only at a milestone or before a
  requested integration handoff.
- PostgreSQL-backed suites run serially because they share fixed ports.
- Do not spend repeated cycles diagnosing known sandbox-only infrastructure
  failures. Record them once with the narrower passing evidence.
- Borg may be used for resource-intensive cluster tests when reachable, but do
  not publish source/images or make unrelated external changes solely to make a
  test possible.

## Current checkpoint

Completed and independently approved:

- Task 1 — direct snapshot team ownership
- Task 2 — remove principals
- Task 3 — retained dev-capability core
- Task 4 — compiled validation authority
- Task 5 — validation/v1 revision 3 provenance

Known branch-wide compatibility issue:

- Existing merge-preflight still emits validation data incompatible with
  revision 3. Repair it as a bounded integration prerequisite after Task 6.

Task 6:

- Substantially implemented through commit `b935d1b27e`.
- The last review found one Critical post-Pod Secret-substitution race.
- An interrupted, uncommitted correction currently modifies:
  - `atc/task.go`
  - `atc/worker/jetbridge/container.go`
  - `cmd/function-runner/dev_validate.go`
- The partial correction pre-creates immutable Secrets before Pod creation,
  CAS-binds exact Secret identity to the created Pod, and adds fixed
  profile/config digest arguments checked before launching dev-capability.
- Treat those edits as unverified work: finish targeted tests, inspect failure
  cleanup/reaping, and commit only when focused verification passes.

Tasks 7–19 have not started.

## Near-term sequence

1. Finish the known Task 6 Critical fix.
2. Run focused Task 6 tests and one final blocking-only review.
3. If accepted, mark Task 6 complete. If rejected, mark Human Review Required
   and stop iterating.
4. Repair the known merge-preflight validation-revision compatibility issue as
   a bounded prerequisite.
5. Establish a clean integration checkpoint and run one consolidated
   acceptance suite.
6. Treat the remaining feature groups as separate bounded tracks rather than
   one continuous "rebase."

## Session handoff requirement

Before ending a session:

- Update the local SDD `progress.md` with commits, focused test evidence, review
  round count, and accepted/human-review-required state.
- Update the deferred-item catalog for every intentionally deferred finding.
- Leave a precise dirty-worktree note if uncommitted work remains.
- Do not claim completion without current verification evidence.
