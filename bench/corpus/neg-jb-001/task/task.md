# Ticket: agent-runner must work in the resolved output workspace

**Repo:** jetbridge · **Workflow:** develop-gated · **Filed:** 2026-07-20 · **Priority:** load-bearing

## Problem

Two consecutive dispatched runs for ticket #43 (a whole plan-doc backend
feature: six route touchpoints + persistence + tests) pushed **empty** branches
— `head_sha == base_sha` — and both read as successful. ~$8 spent, zero work
delivered.

- **Run 42** (`develop-gated` v1, sonnet, `max_turns: 100`). The CLI's result
  event was
  `{"type":"result","subtype":"error_max_turns","num_turns":100,"is_error":false,"total_cost_usd":5.98}`.
  Nothing was committed; the `build` gate ran against the unchanged tree, passed
  trivially, and `agent/ticket-43` was pushed at the base sha.
- **Run 43** (`develop-gated` v2, sonnet, `max_turns: 250` + the new
  commit-incrementally guidance). Terminated `subtype:"success"` at only 48
  turns / $2.15 / ~30k output tokens — and ALSO pushed `base_sha == head_sha`.

So the second failure is not the turn cap and not missing commit guidance: the
agent did ~30k output tokens of work that never reached the `outputs:
[workspace]` dir the harvest pushes. The agent edited the input `repo/` tree
instead of the workspace output. The full write-up is the top entry of
`ci/dogfood/FINDINGS.md` ("CONFIRMED SYSTEMATIC: the ticket loop pushed TWO
empty no-op branches for #43").

## Already shipped this session — containment, not the cause

1. **Harvest no-op guard.** `agent/harvest` now FAILS a run whose workspace HEAD
   equals the base, before gates and before push, and publishes nothing
   (`TestRunNoOpEmptyBranchFails`). This stops the money-burning "successful
   empty run".
2. **Prompt self-verify.** `develop-gated` / `develop-elm` gained a step telling
   the agent to run `git -C "<WS>" show --stat HEAD` and confirm HEAD is its own
   commit.

Neither touches the mechanism that let the work land in the wrong tree.

## The assignment

This is the deeper fix that was scoped as a follow-up, and FINDINGS fix (3):
make the workspace materialization robust instead of trusting the agent to
hand-resolve `$AGENT_OUTPUT_WORKSPACE` and `cp -a repo/.` into it.

Scoped concretely: **make the agent-runner set its working directory to the
resolved output workspace**, so a dispatched agent physically cannot edit or
commit into the input `repo/` copy.

### Expected behavior

- An agent's edits and commits land in the step's `workspace` output by
  construction, not by prompt discipline.
- A run can no longer deliver nothing because the agent worked in the wrong tree.
- Runs that already follow the workspace protocol correctly are unaffected.

### Constraints

- `agent/runner` ships inside the `agent-runner` image. Any change here needs a
  manual image rebuild plus a `CONCOURSE_AGENT_STEP_IMAGE` bump in home-infra,
  so keep it minimal, self-contained and reviewable.
- Do not break the §8.1 agent-step contract: `AGENT_PROMPT_FILE` resolution,
  skills materialization, the flight recorder (`flight/events.ndjson`,
  `flight/results.json`), and the `agent/ticket-<id>` push.
- `go build ./... && go test ./agent/...` must stay green.
- Keep the diff focused; no drive-by refactors.

### Deliverable

Your commit(s) in the workspace, with tests, plus a short entry in
`ci/dogfood/FINDINGS.md` recording what you concluded and what you changed
(that file is the hand-kept dogfood log; every loop-hardening change gets an
entry).

That entry is this ticket's written outcome — it is what gets read to close the
ticket out — so commit it as part of the deliverable and open it with a one-line
`Disposition:` saying what you did with this ticket and why, with the reasoning
under it. Do not leave a reader to reconstruct the disposition from the
analysis.
