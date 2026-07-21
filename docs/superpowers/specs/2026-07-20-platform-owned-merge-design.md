# Platform-Owned Merge — Design Delta

- **Date:** 2026-07-20
- **Status:** Self-approved for implementation (owner waived the review gate in-session, 2026-07-20)
- **Descends from:** `12-delivery-outcomes.md`, `00-shared-contracts.md` §1.11/§1.11.1
- **Coordinates with:** `codex/postgres-delivered-diffs` (concurrent; see §8)

## 1. Problem

Delivery outcome tracking is built as **passive inference**. The platform pushes
`agent/ticket-<id>`, a human merges it somewhere else, and `agent/outcomewatcher`
tries to reconstruct what happened from a bare git mirror. Verified against the
landed code, that inference fails on the common cases:

- **Rebase-merge is undetectable by construction.** `PatchIDMatch`
  (`agent/gitcheck/mirror.go:227-250`) computes **one** patch-id for the whole
  `base..branchTip` range and compares it against **individual** target commits.
  A rebase replaying N>1 commits can never match — there is no combination it
  tries.
- **Squash only matches when clean and single-commit.** Patch-id hashes context
  lines, so a squash onto a moved target misses even when semantically identical.
  The scan window is the last 200 target commits.
- **Human rebase breaks detection silently.** The recorded `pushed_sha` stops
  being an ancestor; once git GCs it, `--is-ancestor` errors and the watcher hits
  `continue // per-row git fault: skip`. The row stays `open` forever with no log.
- **The delta metric reads zero for the most invasive human action.**
  `HumanDelta` filters on `%an` — *author* (`agent/gitcheck/mirror.go:167`).
  Rebase and squash both **preserve author**, so a human squashing the agent's
  commits leaves them authored by `concourse-agent[bot]`, yielding
  `human_commit_count = 0` and a verdict of plain `merged`.
- **The mirror is unbounded on disk.** `clone --mirror` with no `--depth`, no
  `--filter`, and no GC or retention anywhere in `mirror.go`. `fetch --prune`
  prunes refs, not objects. Same failure shape as the July 2026 registry-PVC
  disk outage.

The root cause is not any single heuristic. It is that **the platform is a
passive observer of an action performed somewhere else.**

## 2. Decision

**The platform performs the merge.** A human reviews on the ticket page and
clicks merge; the platform executes it with a plain `git push`. Outcome data
stops being *detected* and becomes *recorded*, because the platform generated it.

This requires no forge API and no per-forge adapter — it is git plumbing, so it
preserves the project's SCM-agnosticism.

Passive detection is **retained as a backstop**, because people will still merge
on the forge directly. It degrades from the mechanism to the fallback, which is
the correct weight for something built on patch-id.

### 2.1 Invariant reversal — stated loudly, not smuggled

`00-shared-contracts.md`, the end-state spec, and plans 12/14 state **"auto-merge
(never)"** in five places. This design **reverses that for a fenced subset**, on
explicit owner decision (2026-07-20, in-session). It is recorded here rather than
quietly contradicted downstream.

What is preserved: **no agent-authored change reaches the default branch without
either a human decision or a deterministic, owner-configured fence.** The
original intent — no unreviewed agent code on main — holds. What changes is that
"reviewed" may be satisfied by a policy the owner wrote, not only by a click.

## 3. Merge-policy ladder

A `merge_policy` field on the workflow definition, sitting beside the existing
`gate_policy`:

| Tier | Who decides | Fail-safe direction |
|------|-------------|---------------------|
| `manual` (default) | human clicks | — |
| `judge` | judge may **escalate only** | any judge fault → `manual` |
| `auto` | fence evaluates the **real diff** | any fence miss → `manual` |

**The judge can veto but never authorize.** Its outputs are "auto-merge still
permitted" or "escalate to human" — it can only move a ticket *toward* review,
never past it. The existing judge is explicitly advisory ("triage signal for the
human merger"); this is the smallest promotion that keeps a judge failure mode
from becoming a merge.

**The `auto` fence checks the diff, not the label.** "Version bump" is a category
of *intent*; the agent's diff is what lands. The fence is deterministic:

- a **path allowlist** (globs) every changed file must match, and
- a **changed-line ceiling**,

both evaluated at merge time against the real `base_sha..pushed_sha` diff. A
dep-bump workflow that touches `go.mod` plus eleven source files falls out of the
auto tier automatically, with no human having to notice.

`merged_by` (`human | judge | auto`) is recorded on the outcome row, making merge
rate decomposable by tier — a scorecard signal that does not exist today.

## 4. Merge-time freshness

Scheduled rebasing of agent branches is **rejected**. It would:

- break the delta metric by the exact mechanism diagnosed in §1 (rewriting the
  agent's commits so `pushed_sha..tip` is meaningless) — automating the failure,
  on a timer;
- fight harvest's `--force-with-lease` re-push, racing the fix-loop;
- change what was reviewed — an approval at sha X no longer refers to the X′ that
  merges, conflict resolutions and all.

Instead, three measures, cheapest first:

1. **Report staleness, don't fix it.** `git rev-list --count pushed_sha..<target>`
   plus a clean-merge check, surfaced on the ticket page. Most of the value, none
   of the risk. Ships independently.
2. **Freshen at merge time.** The merge action rebases-or-merges *then*, runs
   gates on the result, and lands only if green. The branch stays immutable while
   under review; freshness is guaranteed at the only moment it matters.
3. **Evaluate in a scratch ref.** Compute the prospective merge into a temporary
   ref, gate it, discard. Zuul's speculative model — learn "would this land
   cleanly and pass" without mutating what the reviewer read.

A genuinely stale, conflicted branch is a **re-dispatch through the fix-loop**,
not a mechanical rebase: the agent resolves conflicts with context, and the
lifecycle already models this as `sent_back → queued`.

## 5. Commit trailer (SCM-agnostic backstop)

Harvest stamps `Agent-Ticket: <id>` onto the tip commit before pushing.

Amending a commit **message** leaves the tree hash identical, so it cannot
invalidate the gates that already ran on that tree — the safety argument that
makes this cheap.

Detection then becomes `git log --grep` on the target branch, which survives
rebase (messages preserved), squash (forges concatenate messages by default), and
cherry-pick. This is what Gerrit's `Change-Id` exists for, and it is strictly
more robust than patch-id. Worth keeping even after §2, for merges performed
outside the platform.

## 6. What this retires from plan 12

| Plan-12 machinery | Disposition |
|---|---|
| Ancestor + patch-id detection | demoted to backstop |
| `human_commit_count/lines` via mirror | superseded for platform merges (exact diff known) |
| Bare-mirror cache on the web node | **retired** once §2 lands and codex's diff work lands |
| `--agent-outcome-git-dir` master switch | scoped down to the backstop watcher only |
| `closed_unmerged` requiring a human form | still human, but now one click in the same UI |

## 7. Non-goals

- Replacing the forge. No git hosting, no issues, no PR objects.
- Auto-merge outside the `auto` fence.
- Backfilling historical tickets.
- Changing the judge's per-finding verdict taxonomy.
- Removing the outcome watcher (it remains the backstop).

## 8. Concurrency contract with `codex/postgres-delivered-diffs`

That branch moves the **diff surface** off the mirror into Postgres
(`agent_delivery_diffs`, migration `1773106095`) and explicitly defers "merge,
squash-merge, branch-deletion, or closed-unmerged detection" and "human-touch
delta computation" — precisely this design's scope. The split is clean and
complementary: codex retires the mirror's *diff* job, this retires its
*detection* job, and the subsystem disappears entirely.

**Quarantined until codex lands** (actively edited there, uncommitted in places):

- `atc/exec/harvest_step.go`, `agent/harvest/runner.go`, `agent/harvest/flight.go`
- `atc/atccmd/command.go`, `atc/engine/step_factory.go`
- `agent/api/outcomes/diff_handler.go`, `agent/outcomewatcher/mirror_cache.go`
- `00-shared-contracts.md`, `12-delivery-outcomes.md`,
  `remainders/2026-07-17-delivery-outcomes.md` (uncommitted edits present)

**Migration order is load-bearing.** Head on this branch is `1773106094`; codex
takes `95`. This design takes **`1773106096`** and **must land after** codex —
the version-pointer migrator skips migrations that appear below an already
applied head.

Consequence for sequencing: the trailer (§5) and the freshness step (§4.2) both
live in quarantined files and are **deferred to phase 2**. Phase 1 is the
greenfield merge domain, which collides with nothing.
