# Code review: dispatcher runtime control (off | paused | active)

**Type:** code review — pre-merge adversarial pass
**Repo:** jetbridge (fork of concourse/concourse)
**Requested:** 2026-07-19
**Base ref:** `644184e3f011369f3da77dc82caee200bd8fd196`
**Head ref:** `335faaf363cb5085471a0c797c537f09d636b9d2`
**Change under review:** `change.diff` in this directory
(equivalently `git diff <base_ref>..<head_ref>`), 30 files, +1849/−14.

## What the change does

The autonomous agent-ticket dispatcher has been boot-only until now: the
`--agent-dispatcher-enabled` flag decided at web start-up whether the loop ran
at all, and changing your mind meant a redeploy. This change makes it
runtime-controllable, in three slices:

1. **Persistence.** A new migration adds a singleton `agent_settings` row with a
   `dispatcher_mode` column, plus `db.AgentSettingsFactory` (`GetDispatcherMode`
   hot read, `GetDispatcherSetting` for provenance, UPSERT `SetDispatcherMode`).
2. **Loop gating.** `agent/dispatch/mode.go` introduces the three modes and
   `ResolveEffectiveMode`; `Dispatcher.Run` reads the mode fresh each tick and
   gates on it — `active` dispatches queued tickets and reconciles, `paused`
   skips dispatch but keeps the completion reconciler alive, `off` is fully
   dormant. `atc/atccmd/command.go` now wires the dispatcher component
   unconditionally and hands it a hot mode resolver instead of gating the
   component on the boot flag.
3. **Control surface.** `GET /api/v1/agent/dispatcher` (any authenticated user)
   and `PUT` (admin only) via `agent/api/dispatcher`, wired through the wrappa
   auth tiers and the auditor; a `fly agent dispatcher` command; and an Elm
   surface on the ticket queue page (status pill, a banner when the dispatcher
   is not active, admin-only two-step pause/resume/off controls).

The `--agent-dispatcher-enabled` boot flag is retained as the **fallback seed**:
when no `agent_settings` row exists yet, the flag decides the effective mode
(`true` → active, `false` → off), which reproduces today's live behavior on
first boot after the migration.

## Why this is worth a careful pass

The dispatcher is not a read-only component. When it is active it claims queued
tickets and creates pipeline runs that mint credentials, spend real budget, and
push branches to GitHub. The runtime control is therefore an operator kill
switch on an autonomous, money-spending, repo-mutating loop, and the PUT side is
admin-gated for that reason. This change goes straight onto the mainline that
the live deployment self-upgrades from, so it will be exercised against a real
Postgres, a real cluster, and real operators the same day.

It is also the first thing in the fork to let an operator-set value control a
component loop at runtime, so there is no in-tree precedent for this shape to
copy from.

## What to produce

Real defects, not style. For each concern:

1. Prove it. Write a failing Go test that demonstrates the defect against the
   code at the head ref, and run it to confirm it actually fails before you
   report it. A concern you cannot prove with a test is an **observation**, not a
   proven issue — say so rather than inflating it.
2. Classify severity as `critical | high | medium | low` and category as
   `security | correctness | performance | maintainability | testing`, by what
   the test demonstrates rather than by how bad it sounds.
3. Anchor it: file path and line, with enough of the causal chain that a reader
   can follow how the code gets into the bad state.

Report proven issues and observations separately, plus a one-paragraph summary.

## Scope and constraints

- Review the diff, not the whole repository. Pre-existing behavior outside these
  files is out of scope unless the change makes it newly reachable or newly
  wrong.
- Do not redesign the feature. The three-mode model, the admin-only PUT tier and
  the singleton-row storage are settled design; findings should be about defects
  within that design, not proposals for a different one.
- The generated Elm bundle (`web/public/elm.min.js`) is excluded from the diff.
  Review the Elm sources.
- Migrations are append-only and already numbered against the current head; do
  not propose renumbering.
- Be honest about a clean angle. If an area of the change holds up, say it holds
  up. Padding the report with speculative highs is a worse outcome than a short
  report.
