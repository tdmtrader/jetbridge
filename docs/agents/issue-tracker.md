# Issue tracker: the anvil hearth

This repo does not track work in GitHub Issues or in `.scratch/`. Work lives in
the anvil **hearth** at `../jetbridge-hearth` (resolved from `.hearth` at the
repo root) and is driven through the `anvil` MCP tools (`checkin`, `begin`,
`complete`, `catalog`, `describe`). The lifecycle rules are the hearth's own
`playbooks/conventions.md`, not this file.

## Vocabulary

| Skill says            | Hearth artifact                                                                 |
| --------------------- | ------------------------------------------------------------------------------- |
| issue / ticket        | a **track** (concrete slice of a **proposal**); never called a ticket in the repo |
| epic / feature        | a **proposal** (vision → active → tracks)                                       |
| idea, not yet scoped  | a **spark**                                                                     |
| backlog item          | a **backlog_item** (governed, parent-less)                                      |
| decision record       | a hearth **decision** (only if hard to reverse, surprising, a real trade-off); repo-level ADRs live in `docs/adr/` |

`track`, `ticket`, `workflow`, `playbook` are hearth words and must not leak
into repo code or glossaries (see `CONTEXT-MAP.md`).

## What the engineering skills may and may not do

The hearth playbooks already thread these primitives into the lifecycle and
call them by name: `grilling`, `domain-modeling`, `codebase-design`, `tdd`,
`diagnosing-bugs`, `code-review` (smell baseline), `retro`, `handoff`. Use them
from inside the phase that calls them.

Never invoke `to-tickets`, `to-spec`, `implement`, `implement-spec`, `triage`
or `wayfinder`: each assumes it owns the issue queue, and the hearth already is
that queue with its own spec, plan, review and reflection phases.

## When a skill says "publish to the issue tracker"

Do not create a GitHub issue or a `.scratch/` file. Either:

- record it as a finding, decision or reflection line in the **current track**
  (`checkin` first; the actor identity rules in conventions §1 apply), or
- if it is out of scope, create a **spark** and stop.

## When a skill says "fetch the relevant ticket"

`describe` the track by id (`catalog` lists active ones), then read its
`spec.md`, `plan.md` and latest `handoff.md` under
`../jetbridge-hearth/tracks/<id>/`.

## PRs as a request surface

Off. External PRs do not enter any queue.
