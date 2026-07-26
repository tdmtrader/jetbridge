#!/bin/sh
# feedback-jb-003 — ground-truth validation, NOT agent grading.
#
# Fourteen textual anchors, one or two per finding, chosen so that each is
# ABSENT at pre_state efb0a8cdb068c5e6e6074e0ffc1d0a63e7218b4f and PRESENT at
# the terminal artifact 8157023ae91960df4a3ca2208a87c2062d33c7f1. Running this
# against a materialized tree proves the pinned SHAs really carry the claimed
# before/after states and that the reference diff is real.
#
#   at pre_state  : 14 MISS, exit 1
#   at post-state : 14 ok,   exit 0
#
# Do NOT use this to score an agent. The anchors are keyed to the reference
# author's exact identifiers and wording (`UpsertReturningInserted`,
# `applyWindow`, `there is no runtime race`, …); a correct answer that names
# things differently would fail every check. Agent grading is `rubric.md`.
#
# Run from the root of a materialized tree:  sh validate_ground_truth.sh
# Needs nothing but POSIX sh + grep. No Postgres, no network, no build.

P=docs/superpowers/plans/agentic-platform
S=docs/superpowers/specs/2026-07-07-agentic-platform-end-state-design.md
fail=0
chk() { if eval "$2" >/dev/null 2>&1; then echo "  ok   $1"; else echo "  MISS $1"; fail=1; fi; }

chk F1   "grep -q 'func renderCheckpointStep(in RenderInput, s workflow.Step) atc.TaskStep' $P/11-dispatch.md"
chk F2   "grep -q 'there is no runtime race' $P/00-shared-contracts.md"
chk F3   "grep -q 'UpsertReturningInserted' $P/07-agent-step.md"
chk F4   "grep -q 'context.WithoutCancel' $P/07-agent-step.md"
chk F5   "! grep -q 'The approved spec and plan are in spec.md and plan.md' $P/05-workflow-store.md"
chk F6   "! grep -q 'already tracked; re-push refresh handled' $P/12-delivery-outcomes.md"
chk F7a  "grep -q 'requires ask_timeout_seconds > 0' $P/05-workflow-store.md"
chk F7b  "grep -q 'PLATFORM_MCP_ASK_TIMEOUT_SECONDS must be > 0' $P/08-platform-mcp-hitl.md"
chk F8   "grep -q 'FROM agent_workflow_definitions' $P/13-scorecards.md"
chk F9   "grep -q 'func applyWindow' $P/14-process-intel-experiments.md"
chk F10  "grep -q 'SubmitSpec' $P/14-process-intel-experiments.md"
chk F11  "grep -q 'TestSyncerDeletesSecretWhenCredentialUnvaulted' $P/02-credentials-and-budgets.md"
chk F12a "grep -q 'gateway enforces for sub-agent calls only' $P/00-shared-contracts.md"
chk F12b "grep -q 'not.*interrupted mid-call' $S"

exit $fail
