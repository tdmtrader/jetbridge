# Dispatcher runtime control + UX visibility — DELIVERED (branch `feat/dispatcher-runtime-control`)

> **SUPERSEDED — historical implementation record (not executable).** The approved [Agentic Workflows as Functions over Snapshots design](../../specs/2026-07-21-agentic-workflows-as-functions-design.md) and [Agentic Functions program plan](../2026-07-21-agentic-functions-program.md) are authoritative. This dispatcher runtime-control design was delivered against the ticket-centric dispatcher; the runtime-toggle concept survived (see `docs/agentic/README.md`) but the ticket-claim mechanics below are historical.

**Date:** 2026-07-19 · **Base:** jetbridge · **Status:** implemented, tested, adversarially reviewed, committed on `feat/dispatcher-runtime-control`. **NOT deployed** (deploy runs a prod migration + self-upgrade — owner's call). Descends from the UX audit №4 control-plane finding + the "dispatcher should be configurable, not a flag" ask.

## What it does

Turns the boot-only `--agent-dispatcher-enabled` all-or-nothing flag into a **runtime-configurable three-state mode**, visible and controllable from the UI:

- **active** — auto-dispatch queued tickets AND run the completion reconciler.
- **paused** — do NOT auto-dispatch, but DO keep the reconciler alive (safety net); manual `fly agent tickets dispatch` still works. This is the "enabled but not dispatching" state.
- **off** — neither (fully dormant; equivalent to the historical `--agent-dispatcher-enabled=false`).

Flipping modes takes effect on the loop's next tick (~10s) with **no restart** — the mode is read hot from the DB each tick, mirroring the pipeline-pause precedent.

## Design

- **Storage:** new singleton table `agent_settings` (migration `1773106091`, `id=1` check, `dispatcher_mode` CHECK-constrained, `updated_at`/`updated_by`). No seed row.
- **Resolution (hot each tick):** if the row exists → its mode; else fall back to the boot flag (`--agent-dispatcher-enabled` true→active, false→off). This preserves current live behavior (flag off → effective `off` → dormant) until an admin sets a mode. **Fail-safe:** on a DB read fault the resolver returns `paused` (never auto-dispatches against an admin's pause/off, keeps the reconciler) — `dispatch.EffectiveModeFromRead`.
- **Wiring change:** the dispatcher component is now **always wired** (when the K8s-agent block is active); behavior is gated by the effective mode inside `Dispatcher.Run` (gates only `dispatchQueued`; `reconcileCompletedRuns` runs in active+paused). The boot flag is kept as the default seed, not removed.
- **API:** `GET /api/v1/agent/dispatcher` (any authenticated user — read status) and `PUT /api/v1/agent/dispatcher` (**admin only**, `CheckAdminHandler`). JSON: `{mode, source, updated_at, updated_by, boot_default}`.
- **fly:** `fly agent dispatcher` (status), `fly agent dispatcher pause|resume|off`, `--json`.
- **UI (ticket queue page):** an "Auto-dispatch: active/paused/off" status pill, an admin pause/resume/off control, and a banner above the Queued section when mode ≠ active explaining queued tickets won't auto-run (and that manual dispatch still works) — closing the audit's control-plane visibility gap (a queued ticket now says *why* it's waiting).

## Files (31, ~1,850 LoC) + verification

Backend: `atc/db/migration/migrations/1773106091_create_agent_settings.{up,down}.sql`, `atc/db/agent_settings.go`(+test,+dbfake), `agent/dispatch/mode.go`(+test), `agent/dispatch/dispatcher.go`, `agent/api/dispatcher/*`, `atc/api/handler.go`, `atc/atccmd/command.go`, `atc/routes.go`, `atc/wrappa/api_auth_wrappa.go`(+test), `atc/wrappa/reject_archived_wrappa.go`, `atc/auditor/auditor.go`, `fly/commands/agent_dispatcher.go`(+integration test). Elm: `Concourse/AgentDispatcher.elm`(new), `AgentTickets/AgentTickets.elm`, `Api/Endpoints.elm`, `Message/{Effects,Callback,Message}.elm`, tests, `web/public/elm.min.js` (rebuilt).

**Verified:** `go build ./...` + `go vet` clean; new Go tests pass (dispatch mode-gating + fail-safe, dispatcher handler, wrappa admin-tier incl. "REJECTS non-admin PUT", `atc/db` agent_settings with Postgres); `elm make --optimize` clean; `elm-test` 3159 passed; bundle regenerated. Adversarial review: 2 lenses clean (Go↔Elm contract, migration/DB safety), 1 major fixed (fail-safe on read error), 1 minor fixed (honest `updated_by`).

## Deploy (GATED — owner's call; runs a prod migration + self-upgrade)

1. Review/merge `feat/dispatcher-runtime-control` → `jetbridge` (it's code, so it triggers the release chain). Squash-merge recommended (branch has 2 merge commits + a fix commit).
2. The self-upgrade deploys the new web, which runs migration `1773106091` against the prod DB (additive empty table — low risk, reversible via `.down.sql`).
3. Post-deploy: `fly agent dispatcher` shows status. Since the boot flag is currently off and no row exists, effective mode starts `off` (unchanged from today). `fly agent dispatcher resume` (or the UI) turns auto-dispatch on; `pause` gives you "running but not dispatching".

## Follow-ups (not in this branch)

- Ticket #42's pipeline-archiver is wired in the old dispatcher-flag block; when merged it must wire **independently** of the dispatch mode (housekeeping keeps running when paused/off) — a code comment in `command.go` flags this.
- Optional: extend `agent_settings` with the S-7 runtime daily-cap (same table/mechanism).
- Queued-reason banner currently distinguishes paused/off; a future pass could also surface "over daily budget" as a queued reason.
