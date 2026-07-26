# Work item — address the agentic-platform design review

**Repo:** `tdmtrader/concourse` (jetbridge fork)
**Filed:** 2026-07-08, right after the review landed
**Type:** feedback / review-response

## Context

The agentic-platform program is fully planned but not yet executing. Its design
lives in three places, all in-tree:

- `docs/superpowers/specs/2026-07-07-agentic-platform-end-state-design.md` — the
  end-state spec (the vision the program is built against).
- `docs/superpowers/plans/agentic-platform/00-shared-contracts.md` — **normative**
  for every cross-workstream interface (SQL DDL, Go domain types, MCP tool
  schemas, API routes, event schema, workflow-definition grammar, credential
  contracts, decision list, amendment log). If a plan and `00` disagree, `00`
  wins, or `00` gets an amendment first.
- `docs/superpowers/plans/agentic-platform/01-…14-….md` — one plan per
  workstream, each a task-by-task TDD script (failing test → implementation →
  commit) for code that has **not been written yet**. `ROADMAP.md` sequences
  them into five waves.

A design-quality review of the whole set has just been committed to
`docs/superpowers/plans/agentic-platform/REVIEW.md` (also attached to this work
item as `review.md` — identical bytes). Eighteen reviewers scored the spec and
the 14 plans on design, sensibility, vision-fit and scoping; the raw findings
were then put through an adversarial verification pass before being written up.
Wave 1 is blocked on this review being answered.

## What is being asked

**Address the review.** Work through it and make the design set correct.

Notes on how this repo expects that to be done:

- **The review is an input, not a work order.** This repo expects review
  feedback to be received with technical rigor rather than transcribed: where
  you disagree with the review on the merits, say so and act on your own
  analysis. A wrong correction is worse than none.
- **Nothing here is compiled.** The subject files are plan and spec documents;
  the Go, SQL and YAML inside them are the scripts the workstreams will execute
  later. A correction lands as an edit to those documents — including the test
  the plan tells the implementer to write first, when the finding is about a
  contract that a test should pin.
- **Cross-workstream corrections need both owners.** Several plans describe two
  ends of the same seam. If a fix changes a seam, every plan on that seam and
  the normative `00-shared-contracts.md` must end up saying the same thing —
  the amendment log in `00` §11 is where cross-workstream changes are recorded.
- **Leave an auditable record.** Each plan already carries a dated
  addendum/amendment section; follow that convention so the next reader can see
  what changed and why.
- Keep the change scoped to the design set. No code outside
  `docs/superpowers/` needs to move for this work item.

## Deliverable

One commit against `main` containing the corrected design set, plus a
per-finding record of what was done and why.
