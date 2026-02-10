# Spec: Implementation Agent — `ci-agent-implement`

**Track ID:** `agent_can_iterate_on_a_story_given_a_spec_20260209`
**Type:** feature

## Overview

The implementation agent (`ci-agent-implement`) is the execution step in the CI agent pipeline. It receives a `spec.md` and `plan.md` produced by the planning agent (`ci-agent-plan`) and drives an AI agent through the plan's phases and tasks using strict TDD. For each task, the agent writes a failing test (red), implements the code to make it pass (green), and verifies the full test suite remains green. The agent iterates until all objectives are met or it determines it cannot proceed.

This is a **long-running, loop-based agent** — unlike the review and planning agents which make a single pass, the implementation agent maintains state across multiple agent invocations, tracks progress per-task, and makes decisions about whether to continue, retry, or abort.

## Requirements

1. **Accepts plan + spec as input** — reads `spec.md` and `plan.md` from an input directory (produced by `ci-agent-plan`), plus a `repo/` directory containing the code to modify.
2. **Parses plan into executable tasks** — extracts phases and tasks from `plan.md` Markdown, builds an ordered task list with dependencies.
3. **Strict TDD execution per task** — for each task:
   - Invokes the agent to produce a failing test file.
   - Runs the test to confirm it fails (red). If it passes, the task is skipped as already-satisfied.
   - Invokes the agent to write the minimum implementation.
   - Runs the test to confirm it passes (green). If it fails, retries up to `MAX_RETRIES`.
   - Runs the full test suite to check for regressions. If regressions, reverts and retries.
4. **Progress tracking** — maintains a task ledger recording status (pending, red, green, committed, skipped, failed) per task. Persists to disk so execution can be inspected mid-run.
5. **Commit per task** — each successfully completed task (green + no regressions) is committed with a conventional commit message. Commits are atomic and individually revertable.
6. **Termination conditions** — the agent completes when:
   - All plan tasks are done with tests passing → status `pass`.
   - Some tasks succeeded but others were skipped/failed → status `pass` (partial) with reduced confidence, or `fail` if critical tasks failed.
   - The agent cannot make progress (consecutive failures exceed threshold) → status `fail` with explanation.
   - Input is insufficient to proceed → status `abstain`.
7. **Output conforms to agent schema** — produces `results.json` (status, confidence, summary, artifacts) and `events.ndjson` following the conventions from `atc/agent/schema/`.
8. **Summary artifact** — produces a human-readable `summary.md` listing each task, what was done, test results, and any issues encountered.
9. **Lives in `ci-agent/` standalone module** — zero Concourse imports, reuses shared schema types from `ci-agent/schema/`.

## Acceptance Criteria

- [ ] Parses a `plan.md` produced by `ci-agent-plan` into an ordered task list.
- [ ] For each task, writes a failing test before writing implementation (TDD red-green).
- [ ] Confirms each test fails before implementing (rejects tests that pass immediately).
- [ ] Confirms each test passes after implementation.
- [ ] Runs the full test suite after each task to catch regressions.
- [ ] Reverts and retries when a task introduces regressions.
- [ ] Commits each completed task atomically with a conventional commit message.
- [ ] Tracks and persists task progress to `progress.json` in the output directory.
- [ ] Produces valid `results.json` with status, confidence, summary, and artifact references.
- [ ] Produces valid `events.ndjson` with chronological event log.
- [ ] Produces `summary.md` with per-task details.
- [ ] Terminates with `pass` when all tasks complete and all tests pass.
- [ ] Terminates with `fail` when it cannot make further progress.
- [ ] Terminates with `abstain` when input is insufficient.
- [ ] Handles agent adapter timeouts and errors gracefully.
- [ ] CLI reads Concourse-convention environment variables.
- [ ] Concourse task YAML definitions are valid.

## Out of Scope

- Multi-language support — this track targets Go repos only (test runner uses `go test`).
- Interactive human-in-the-loop during execution — the agent runs autonomously.
- Parallel task execution — tasks are executed sequentially in plan order.
- Agent model selection or routing — uses a single configured agent CLI.
- Database storage of implementation history — output is file-based only.
- PR creation — handled by the downstream `ci-agent-fix` / `create-pr` tasks.
