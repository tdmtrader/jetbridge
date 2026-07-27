# platform-mcp + HITL — wave-3 remainder plan (plan 08, re-scoped against the 2026-07-17 tree)

> **SUPERSEDED — historical implementation record (not executable).** The approved [Agentic Workflows as Functions over Snapshots design](../../../specs/2026-07-21-agentic-workflows-as-functions-design.md) and [Agentic Functions program plan](../../2026-07-21-agentic-functions-program.md) are authoritative. This remainder plan re-scopes plan 08 against the 2026-07-17 tree; the platform-mcp sidecar it wraps remains live, but the ticket-centric identity model below is historical.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. This plan is a **delta wrapper**: most task bodies live in `docs/superpowers/plans/agentic-platform/08-platform-mcp-hitl.md` ("plan 08") and are executed *as written there* plus the delta notes below. Read a referenced task's full plan-08 text before executing it; the deltas amend, they do not replace.

**Date:** 2026-07-17
**Status:** draft for review
**Depends-on:** `08-platform-mcp-hitl.md` (task-body source of record), `00-shared-contracts.md` (§1.9, §3 preamble, §3.1, §3.2, §4.1/§4.2, §5, §8.1, §8.4, §8.5, §11 amendment log — now ending at line 1931; the 2026-07-18 date on the final entry is a pod UTC clock artifact, do not "fix" it), `CONVENTIONS.md` (C1/C2/C3), `REVIEW.md` (F13, F14, F15, F28, F31, F34), landed slices listed in §1 below.

**Goal:** Land plan 08's entire remainder — the `agent_run_questions` HITL data plane, the SSE-upgraded `atc/api/mcpserver`, the `agent/platformmcp` sidecar with its seven tools plus `ask_human` short-park and PARK-V2 long-park exit signals, checkpoint-gate execution, the polling notifier, the Elm question banner, image packaging, the renderer/mint integration seams, and the two live proofs — without rebuilding anything the 2026-07-17 slices already shipped.

**Architecture:** Unchanged from plan 08's header (questions table + 4 ATC routes carry HITL state; the sidecar is an SSE streamable-HTTP MCP server translating seven tools into principal-authed ATC calls, parking by blocking the call over a resilient long-poll; PARK-V2 long-park exits via the `flight/park.json` sentinel / checkpoint `202`). What this remainder adds on top of plan 08's assumptions: the runtime seams (SidecarEnv/SidecarSecretEnv, supervised agent containers, looping pause pods) and the exec-side §8.1 env producers are **already landed**, so the sidecar consumes a live contract rather than a promised one; and two small integration tasks (renderer refusal-lift subset, per-run principal mint delta) that plan 08 assumed neighbors would provide are pulled into this item because nothing else ships them before the sidecar needs them.

**Tech stack:** Go (main module), squirrel/psql + counterfeiter in `atc/db`, `atc/api/mcpserver` for MCP (SSE-upgraded in place, mirrored from `ci-agent/devmcp`), plain-Go httptest tests in `agent/*`, Ginkgo in `atc/db`/`atc/api`/`atc/wrappa`, Elm 0.19 (`web/elm`), plain-Go `//go:build live` tests (local claude CLI + theborg), alpine-based sidecar image via `deploy/Dockerfile.platform-mcp`.

---

## 1. Landed state (verified 2026-07-17 against HEAD `187cad4926` — the executor must NOT rebuild any of this)

**Tree delta vs. the shared ground state this item was scouted under:** since the scout snapshot (HEAD `b88d124540`), workflow-source slice-a and Elm waves E+F landed (`ac9347c9aa`..`187cad4926`). **Deployed head migration is now `1773106066`** (`atc/db/migration/legacy_upgrade_test.go:37` and `docs/migration/migrate-preflight.sh:38` both pin it — NOT 1773106064). See §5 for consequences. This is the second time the head moved while a plan for this wave was being written; treat every migration number below as verify-at-land-time.

| Landed seam | Evidence | What it means for this plan |
|---|---|---|
| Runtime seams (plan 07 Task 11B): `runtime.ContainerSpec.SidecarEnv map[string][]string` + `SidecarSecretEnv`, applied by `buildSidecarContainers`; `applySecretRefs` secretKeyRef-only; `supervised()` covers `db.ContainerTypeAgent`; pause pods loop their sleep (parks survive >24h) | commit `b83a7932a3`; `atc/runtime/types.go:189-202`; `atc/worker/jetbridge/container.go`, `process.go:728-741` | Tasks 11/12 consume these rows; no runtime work in this plan |
| Agent-step exec populates §8.1/F15 sidecar env: `mcpSidecarPorts {dev:7780, platform:7781, gateway:7782}`, `PLATFORM_MCP_URL` derivation, common rows, `PLATFORM_MCP_ASK_TIMEOUT_POLICY/_SECONDS` pass-through from planEnv, `AGENT_PRINCIPAL_TOKEN` secretKeyRef from `agent-run-<runID>` (server-verified id only), §8.5 WorkingDir convention | `atc/exec/agent_step.go:434-557`; commit `44fdad0f64` | The sidecar's env contract has a live producer — but nothing *renders* the planEnv values yet (Task 25) |
| Per-run credentials: `DispatchOne` mints principal `run-<id>` (24h expiry, scopes tickets:read/write + metrics:write + costs:write — **no questions:answer**; the `run-<id>` name MISMATCHES the reaper's `RevokeByName(RunSecretName(runID))` = `agent-run-<id>` at `secret_reaper.go:117`, so revoke-by-name is broken today) + attaches `agent-run-<id>` secret (keys `anthropic-token`, `principal-token`); `Attach` is create-or-update (§8.2 resume-refresh satisfied) | `agent/dispatch/dispatch.go:182-219` (`9a8eaf452c`); `agent/credentials/secret_attacher.go:41-70` | Mint exists but lacks the `agent-run-<id>` name fix + `questions:answer` scope (sibling **dispatcher-budget-reconciler Task 8**) and the park-aware expiry (this plan's **Task 26**, layered on Task 8 — see its ownership box) |
| Ticket-core surface complete + hardened: 9 routes incl. embedding `GetAgentTicket` (`TicketDetail{Ticket,Spec,Tasks}`), SubmitSpec/SubmitPlan handlers, atomic `UpdateActiveTask` TOCTOU fix | `atc/routes.go:145-153`; `agent/api/tickets/handler.go:180-205,289-370`; commit `1e586340e5`; contracts §11 2026-07-17 same-day-fix entry | Task 13's read-projection assumption holds; `update_task_status` rides a race-free route |
| Auth tiers: `CheckAgentPrincipalHandlerFactory.HandlerFor(delegate, rejector, scope)` (+`HandlerForWithLegacyBypass`), `auth.AgentPrincipalOrMainTeamHandler(principalTier, mainTeamTier)`, `auth.CheckAgentAuthorizationHandler`, `principals.ScopeQuestionsAnswer` | `atc/api/auth/check_agent_principal_handler.go:27,35,41`; `agent_principal_or_main_team_handler.go:19`; `check_agent_authorization_handler.go:17`; `agent/api/principals/types.go:19,29` | Every survey expectation in plan 08 Tasks 1/6 is CONFIRMED verbatim — no discovery risk left in Task 9 |
| §5 HITL event constants exist: `human.ask`, `human.answer`, `checkpoint.wait`, `checkpoint.release` + typed payloads (`step.park`/`step.resume` do NOT exist — plan 07's) | `agent/schema/event_payloads.go:15-18,100-124` | Task 16 shrinks to the sidecar's NDJSON writer only |
| PARK-V2 metrics leg: `agent_run_metrics` `parked` status + `session_id` (migration `1773106061`); `RunActive()` counts `awaiting_human` | `1773106061_agent_run_metrics_parked.up.sql`; `atc/db/agent_run_checker.go:23-35` | Cross-plan leg satisfied; do not touch |
| Elm surface prepared: ticket PR-view page with reserved empty `div#ticket-hitl-slot` (now **line 365** after waves E+F), dashboard renders `awaiting_human` with shared `AgentBadge` | `web/elm/src/AgentTickets/AgentTicket.elm:365` (`66c3eb45ba`, survives `0866d89fc9`); `web/elm/tests/PipelineTests.elm:890,961` | Tasks 27/28 fill the slot; run-status decode tolerance already exists |
| Render refusals mark exactly the seams this plan lifts: sidecars (`render.go:56-58`), checkpoints (59-61), `spec_delivery` mcp/empty (125-131), hitl (157-159), all naming wave-3/platform-mcp; `workflow.Config` parses+hashes HITL/SpecDelivery/Sidecars at import; contracts pre-authorize the lift (2026-07-17 dogfood entry) | `agent/dispatch/render.go`; `agent/workflow/config.go:14-26,96-101` | Task 25 lifts the enforceable subset; checkpoint refusal STAYS (plan 11's) |
| `atc/api/mcpserver` exists, buffered-JSON only, 2-arg `ToolHandler` — and now has a live in-tree consumer: `RegisterTools` with ~26 read-only agentic tool closures, registered at `atc/api/handler.go:191-192` | `atc/api/mcpserver/{server,tools}.go`; commit `afd5bf5365` | Task 2's in-place SSE upgrade must migrate this consumer (mechanical, see delta) |
| SSE wire spec of record: `ci-agent/devmcp` server (DefaultHeartbeat 15s, `ProgressFunc`-taking `ToolHandler`, -32602 only for malformed input) | `ci-agent/devmcp/server.go:7-62` | Mirror source for Task 2; no cross-module import, drift-guarded by mirrored tests |
| Live-test scaffolding proven: `live_agent_mcp_test.go`, `live_agent_resume_test.go` (`//go:build live`, kubeClient(t), `K8S_TEST_NAMESPACE`, theborg context, throwaway ns) | commit `62f0289b4c` | Tasks 3/29 copy the harness and reuse the existing `//go:build live` tag. Plan 08 Task 18b's `TestLiveCLIParkPin` is a `live` test too (`08-platform-mcp-hitl.md:7500`). Do NOT invent a `live_claude` tag — that is plan 07's SEPARATE runner test (`TestLiveClaudeParkExitResume`, `07-agent-step.md:3408`), which is NOT in this plan's scope |
| Runner PARK-V2 halves NOT landed: `agent/runner` has no `park.json` watch, exit-86, session capture, or stream-json tee | `grep park agent/runner/` | Tasks 15/20 ship inert-but-correct exit signals (see Scope) |
| Harvest v0.5 gate engine (build/test/lint, scope full) is the loop's pre-push verifier | commits `59d3410745`/`b8d064906c`; contracts §11 2026-07-18 entry | Sets the loop-dispatch envelope for this plan's pure-Go slices |
| Image packaging template: `deploy/Dockerfile.mcp-dev-concourse` + `deploy/MCP_IMAGES.md` (registry conventions; `mcp-platform` name already reserved there) | `deploy/` | Task 24 copies it; GHCR classic-PAT gotcha known from the release pipeline |

**Plan 08 proper is NOT started:** no `agent/platformmcp`, no `cmd/platform-mcp`, no `deploy/Dockerfile.platform-mcp`, no `agent_run_questions` anything, no `agent/notify`, no checkpoint code, no SSE in `atc/api/mcpserver`, no `TestLiveCLIParkPin` (the `//go:build live` park pin — Task 3). Everything below is greenfield except where a delta says otherwise.

**Also relevant wiring anchors:** `dispatch.Deps` is constructed at `atc/atccmd/command.go:~2395` (Task 25/26 flag path); `atc/component.go:25-26` holds `ComponentAgentPlatformCredentialSyncer`/`ComponentAgentRunSecretReaper` (Task 23's neighbor constants).

---

## 2. Scope

**In (this plan):** plan 08 Tasks 1–19 complete (including all PARK-V2 sidecar/questions halves 5b, 6b, 9c, 11b, 14c, 17b and the SSE Task 9b), plus two integration tasks plan 08 left to neighbors that nothing else ships in time:
- **Task 25 (new):** the renderer refusal-lift *subset* — platform-role sidecar rendering + auto-injection, `spec_delivery: mcp`, hitl-derived `PLATFORM_MCP_ASK_TIMEOUT_*`, renderer-emitted `PLATFORM_MCP_SHORT_PARK_MAX_SECONDS`, web flags `--agent-platform-mcp-image` / `--agent-short-park-max`. Pre-authorized by the contracts' 2026-07-17 dogfood entry ("harvest-step/platform-mcp-hitl will relax this when their consumers land").
- **Task 26 (new):** the per-run principal mint's park-aware expiry layer — `now + --agent-park-timeout` for park/checkpoint workflows, `now + --agent-run-timeout` otherwise, via the frozen `RunPrincipalTimeout` selector (F31 follow-up 3). Layers on the sibling dispatcher-budget-reconciler Task 8 (which lands the `agent-run-<id>` rename + `questions:answer` scope + `--agent-run-timeout` base); coordinate per Task 26's ownership box. Without the park bound a >24h park outlives its principal and `AwaitAnswer`'s fatal-auth fires mid-park.

**Out (stays deferred, with its owner):**
- **Plan 07 (agent-step):** runner-side sentinel watch/SIGTERM/exit-86, stream-json tee + `flight/session.jsonl` + `session_id` capture, `agent_run_step_state` table (reserved `1773106065` — now BELOW head, must renumber at land time), continuation replay/`--resume`, `TestLiveClaudeParkExitResume` pin, and the exec-side `PLATFORM_MCP_PARK_PATH`/`PLATFORM_MCP_EVENTS_PATH` rows (plan 07 Task 26 — the exec owns them because only it knows the flight mount).
- **Plan 03 (pipeline-runs):** `awaiting_human` status migration (reserved `1773106032` — below head, must renumber at land time), lifecycler entry/exit, the `--agent-park-timeout` wall clock erroring stale parks (this plan adds the FLAG and its mint consumer; the lifecycler consumer is plan 03's).
- **Plan 11 (dispatch):** `reconcileAwaitingRuns` + continuation builds, the Dispatcher loop + run-completion reconciler (deferred-as-a-set — Task 10's notify has no listener until they land; manual `fly agent tickets transition` close-out remains the resume path), `renderCheckpointStep`, `atc.SidecarEnvVar` ValueFrom/secretKeyRef + `--kubernetes-sidecar-secret-prefixes` (the checkpoint-pod credential path). **The checkpoint refusal at `render.go:59-61` is NOT lifted by this plan.**
- **`questionsCheckpointAdapter` — this remainder's DEFERRED responsibility, NOT built in this wave.** The sibling `dispatcher-budget-reconciler` remainder's Task 6 defines a local `dispatch.QuestionLister`/`dispatch.CheckpointRow` seam, wires `LoopConfig.Questions: nil` (its Task 10), and names THIS plan as the owner of the bridge: a `questionsCheckpointAdapter` mapping this plan's `questions.Store.ListByRun` (Task 19, returns `[]questions.Question`) → `[]dispatch.CheckpointRow`, wired into `LoopConfig.Questions` at the K8s dispatcher construction site. This plan OWNS it (it owns the questions store) but does NOT build it here: checkpoints stay render-refused (`render.go:59-61`, above), so the dispatcher's checkpoint-reconciliation branches are **knowingly-dead** until checkpoint activation. The adapter lands together with checkpoint activation (the dispatcher loop + a checkpoint render-lift, both OUTSIDE this five-plan wave); the v0 end state is checkpoint-free by design. Task 19 ships only the store surface the adapter will later consume.
- **Gateway plan 10, dev-mcp, harvest plan 09 remainders:** untouched, except that Task 3 (the park pin) gates gateway 10 Task 7's merge. Push/publish mechanics and `request_review`/`ask_agent` stay plan 08 scope-out.

**Legal intermediate state:** short-park-only. Tasks 1–24 ship the `ask_human`/checkpoint sidecar + questions-plane mechanism buildable and unit-tested in isolation — but NOT yet reachable by a dispatched ticket: `render.go` still refuses any workflow declaring an `hitl` block (`render.go:157-159`) or `spec_delivery: mcp` (`render.go:125-131`) until **Task 25** lifts those refusals, and the timeout-resolution `AnswerAgentQuestion` call 403s until **Task 26** adds the `questions:answer` scope. So Task 25 (render lift) and Task 26 (principal scope) are the two integration seams that make anything end-to-end reachable; only then does an in-pod (SSE-held) park work, with `--agent-short-park-max=0` as the rollback hatch. Long-park exit (Tasks 15/20) writes sentinels nothing watches until plan 07's runner half lands — wiring-forward with zero schema waste, exactly as PARK-V2 §A intends.

---

## 3. Slices

Each slice is independently shippable (in order). Verification story per slice is split three ways:
- **gate-verifiable** — pure-Go tests the loop's in-pod `go build ./... && go test ./... && go vet ./...` gates can run;
- **local-verify** — postgres-backed Ginkgo (and Elm toolchain) the human runs locally pre-merge;
- **live-verify** — theborg cluster (or real-CLI) smoke.

| Slice | Tasks | Gate-verifiable | Local-verify (human, pre-merge) | Live-verify |
|---|---|---|---|---|
| **A — transport + entry gate** | 1, 2, 3 | `ginkgo ./atc/api/mcpserver/` (mirrored SSE specs + migrated tools_test) | `ginkgo ./atc/api/` (handler suite touches the live MCP endpoint consumer) | Task 3: `TestLiveCLIParkPin` — real claude CLI parked >5min against a stub. **WAVE-3 ENTRY GATE**: nothing after Task 3 merges until it passes |
| **B — questions data plane** | 4–10 | `go test ./agent/api/questions/` (memory store + handler httptest) | `ginkgo ./atc/db/ ./atc/db/migration/ ./atc/api/ ./atc/wrappa/ ./atc/auditor/` (migrations, SQL factory, C1 six-touchpoint panics) | none |
| **C — sidecar core** | 11–17 | `go test ./agent/platformmcp/... ./cmd/platform-mcp/` (config, client retry/fatal-auth, 7 tools, short-park, events, binary — all httptest) | none | none (Slice H proves it in-cluster) |
| **D — checkpoint + contract kit** | 18–21 | `go test ./agent/platformmcp/...` (endpoint + client exit codes + dedup fast path, httptest); contract kit | Task 19 (`ListByRun`): `ginkgo ./atc/db/` | none |
| **E — notifier** | 22–23 | `go test ./agent/notify/` (webhook library, httptest) | Task 23: `ginkgo ./atc/db/` + `go build ./atc/...` (component registration) | optional: point `--agent-notify-webhook-url` at a request-bin |
| **F — integration seams** | 24–26 | Tasks 25/26: `go test ./agent/dispatch/ ./atc/exec/` + `go build ./atc/...`; Task 24: none (image) | `ginkgo ./atc/api/` unchanged-pass (flag plumbing) | Task 24: pull + run smoke of `ghcr.io/tdmtrader/mcp-platform` on theborg |
| **G — Elm banner** | 27–28 | none (no Elm toolchain in gates) | `elm-test`, `elm make` in `web/elm`; separate embedded-bundle regen commit (precedent `46db7b9735`) | eyeball on concourse.home after deploy |
| **H — live proofs + sweep** | 29–30 | none | Task 30's local half (`make test-quick`) | Task 29: restart-while-parked in a throwaway theborg namespace (NOT cicd/concourse) |

---

## 4. Tasks

Execution order below. "Execute plan 08 Task N as written" means: open `08-platform-mcp-hitl.md`, execute that task's full checkbox text, applying the delta note. Anchors in plan 08 were pinned at `dab0f2c6e2` and have drifted — always anchor to named neighbors, never raw line numbers.

---

### Task 1 [Slice A] — landed-seam survey + §11 contract addendum (plan 08 Task 1, MATERIALLY AMENDED — survey answers are now ground truth)

Plan 08 Task 1's survey step is **already done** — every `<fill>` marker is resolved by §1 of this document. Skip the survey commands; append the addendum with the fills below and the four new bullets the intervening slices made necessary.

**Files:**
- Modify: `docs/superpowers/plans/agentic-platform/00-shared-contracts.md` (append to §11 — the log now ends at **line 1931**, not plan 08's "1471"; the last entry is dated 2026-07-18 from a pod UTC clock — append after it, date this entry 2026-07-17). **Cross-plan: all five remainder plans append here — re-read the current §11 tail at commit time and append after whatever entry is now LAST (a sibling may have appended since), never after a pinned line; §11 is single-writer per merge window, land docs commits serially.**

- [ ] Append the plan-08 Task 1 addendum text (its bullets: §1.9 `notified_at`, §4.2 `ListAgentTicketQuestions`, §8.4 `atc.ComponentAgentNotifier`, §3.2 read-model frozen delta, §3.2 checkpoint endpoint concretization + supersession of the retracted client→ATC wording, §8.1 `PLATFORM_MCP_EVENTS_PATH`, §3.2 timeout-resolution `answered_by`, §8.5 packaging instantiation, §8.1 `PLATFORM_MCP_PARK_PATH` producer, §1.9/§3.2 idempotency-by-question — all verbatim from plan 08 Task 1) with the survey block replaced by this RESOLVED version:

```markdown
  - Landed-seam survey results (resolved 2026-07-17 from the tree — all plan-06/07/08 expectations CONFIRMED): combined principal-or-main-team wrappa helper = `auth.AgentPrincipalOrMainTeamHandler(principalTier, mainTeamTier http.Handler) http.Handler` (atc/api/auth/agent_principal_or_main_team_handler.go:19); principal-tier factory = `CheckAgentPrincipalHandlerFactory.HandlerFor(delegate, rejector, scope)` plus `HandlerForWithLegacyBypass` (check_agent_principal_handler.go:35,41); main-team tier = `auth.CheckAgentAuthorizationHandler` (check_agent_authorization_handler.go:17); questions:answer scope constant = `principals.ScopeQuestionsAnswer` (agent/api/principals/types.go:19, already in fly help text); GetAgentTicket response embeds spec + tasks = YES (`TicketDetail{Ticket,Spec,Tasks}`, agent/api/tickets/handler.go); ticket page Elm module = `web/elm/src/AgentTickets/AgentTicket.elm` (reserved `div#ticket-hitl-slot` at line 365); dev-mcp packaging template = `deploy/Dockerfile.mcp-dev-concourse` + `deploy/MCP_IMAGES.md` (registry `ghcr.io/tdmtrader/mcp-platform` already reserved there).
```

- [ ] Add these FOUR bullets (new since plan 08 was written) to the same addendum entry:

```markdown
  - Migration re-pin (2026-07-17 tree state): deployed head is now `1773106066` (workflow-source slice-a `agent_workflow_source_manifest`). Plan 08's reservations `1773106070` (agent_run_questions), `1773106071` (notified_at), `1773106072` (question_hash dedup) remain ABOVE head and are kept — §1.9's explicit references to 1773106071/1773106072 stay valid. `1773106067–69` become sacrificial gap numbers once 1773106070 lands: never reuse them. The reserved-but-absent `1773106065` (`agent_run_step_state`, agent-step plan 07) is now BELOW head and MUST be renumbered above the then-current head at land time (third application of the 2026-07-12 hole rule).
  - §8.2/§4.1 mint delta (F31 follow-up 3; the mint block is CO-OWNED with the dispatcher-budget-reconciler remainder — its Task 8 lands the name/scope/RunTimeout base, this plan's Task 26 layers park-aware expiry). Reconciled end-state: the per-run principal is named `agent-run-<run-id>` (= `credentials.RunSecretName`; fixes the reaper's revoke-by-name, `secret_reaper.go:117`, broken today by the landed `run-<id>` mint), carries scope `questions:answer` (the sidecar's timeout-resolution `AnswerAgentQuestion` authenticates as the run principal), and expires at a WORKFLOW-CONDITIONAL bound: `now + --agent-run-timeout` (6h) for an ordinary run, `now + --agent-park-timeout` (72h) only when the frozen workflow declares a park-policy `ask_human` or a checkpoint — selected by the frozen `dispatch.RunPrincipalTimeout(cfg, runTimeout, parkTimeout)` helper (`11-dispatch.md:2465`). NEVER NULL, NO invented margin (the §8.1 contract row is normative). `--agent-park-timeout` is shared: plan 03's lifecycler consumes the same value for the awaiting_human wall clock.
  - §6/§8.1 renderer-lift subset (harvest-v0.5 precedent — relax exactly the enforceable subset; pre-authorized by the 2026-07-17 dogfood entry): once the platform image exists, `dispatch.Render` (a) renders platform-ROLE sidecars and auto-injects the platform sidecar (container name always `platform` per the §8.1 well-known-name URL derivation) into every TICKETED agent step when `--agent-platform-mcp-image` is set — the §3.2 read-model's "MCP tools mounted in BOTH delivery modes"; (b) accepts `spec_delivery: mcp`/empty when the image flag is set (refuses loudly when not — never a silent files fallback); (c) lifts the hitl refusal, emitting `PLATFORM_MCP_ASK_TIMEOUT_POLICY` (default `park`) / `PLATFORM_MCP_ASK_TIMEOUT_SECONDS` into step env (the agent-step exec's existing planEnv pass-through carries them to SidecarEnv["platform"]); (d) emits `PLATFORM_MCP_SHORT_PARK_MAX_SECONDS` from `--agent-short-park-max` (default 30m; 0 = pure PARK-V1). Checkpoint steps, dev/gateway/custom-role sidecars, judge, and affected-scope gates KEEP their existing refusal wording (plans 11/10/09).
  - §8.1 note: `PLATFORM_MCP_SHORT_PARK_MAX_SECONDS` joins `PLATFORM_MCP_ASK_TIMEOUT_POLICY`/`_SECONDS` in the agent-step exec's platform-sidecar planEnv pass-through list (atc/exec/agent_step.go — ADD to the list, C3).
```

- [ ] Commit as plan 08 Task 1 specifies.

---

### Task 2 [Slice A] — SSE progress-heartbeat transport in `atc/api/mcpserver`

**Execute plan 08 Task 9b as written.** Frozen ordering: before Task 13 (tool assembly) and before gateway plan 10 Task 7.

**Delta:**
- `atc/api/mcpserver` now has a live in-tree consumer that barely existed when 9b was drafted: `RegisterTools` in `atc/api/mcpserver/tools.go` (~26 two-arg `func(ctx, args)` closures, commit `afd5bf5365`), constructed via zero-arg `NewServer()` at `atc/api/handler.go:191-192`. Plan 08's design already absorbs this — `NewServer()` delegates to `NewServerWithHeartbeat(0)` so handler.go compiles unchanged, and the task's file list already names `tools.go`/`tools_test.go` for the mechanical 3-arg signature update — but the mechanical scope is now ~26 closures plus every handler literal in `tools_test.go`. Do it as a regex-assisted sweep and verify with `go vet ./atc/api/mcpserver/`.
- The 2-arg→3-arg change is source-breaking for any OTHER `ToolHandler` consumer: run `grep -rn "mcpserver.ToolHandler\|mcpserver.NewServer" atc/ agent/ cmd/` first and list every hit in the commit message. That qualified grep returns only `atc/api/handler.go`, `atc/api/mcpserver/tools_test.go`, `atc/api/mcpserver/server_test.go` today — NOT `tools.go`: its ~26 registrations are bare inline `func(ctx, args) (any, error)` literals in `package mcpserver` that never spell the identifier `ToolHandler` (`grep -c ToolHandler atc/api/mcpserver/tools.go` = 0). Those ~26 closures still need the mechanical 3-arg (`progress`) update regardless — count them with `grep -c "func(ctx context.Context, args json.RawMessage) (any, error)" atc/api/mcpserver/tools.go` and sweep them separately.
- The ATC's own MCP endpoint stays effectively buffered (its clients don't send `_meta.progressToken`) — no behavior change for it; its tools gain an ignored `progress` arg. State this in the commit body so review doesn't chase a phantom.
- Preserve the frozen error-mapping difference from devmcp exactly as plan 08 pins it: handler errors stay `isError=true` tool results, never `-32602`, in both buffered and SSE modes.

Verification: gate-verifiable (`ginkgo ./atc/api/mcpserver/` runs under `go test`); local-verify `ginkgo ./atc/api/` for the endpoint consumer.

---

### Task 3 [Slice A] — MANDATORY real-CLI >5-minute park pin (WAVE-3 ENTRY GATE)

**Execute plan 08 Task 18b as written.**

**Delta:**
- Still greenfield: no `TestLiveCLIParkPin` anywhere (grep verified 2026-07-17). The test carries `//go:build live` (plan 08 Task 18b, `08-platform-mcp-hitl.md:7500` — NOT `live_claude`; that tag belongs to plan 07's runner test `TestLiveClaudeParkExitResume`, a different, out-of-scope test). Run it with `go test -tags live -run '^TestLiveCLIParkPin$' -v -count=1 -timeout 12m ./agent/platformmcp/`.
- The harness conventions plan 08 references are now PROVEN landed patterns — copy `atc/worker/jetbridge/live_agent_mcp_test.go` / `live_agent_resume_test.go` (commit `62f0289b4c`, both `//go:build live`) for structure, but note 18b runs on the OWNER'S MACHINE with the real claude CLI + `--strict-mcp-config` + `ANTHROPIC_BASE_URL` stub, not on theborg.
- Sequencing is absolute: this pin gates every merge after Task 2 in this plan AND gateway plan 10 Task 7. If the pin fails (the CLI abandons the SSE-held call), STOP and re-open the §3.1 transport contract with the owner before building Tasks 11–21.
- Budget note: the pin drives a real CLI session >5 minutes; run it native, never through the loop. Record the pinned CLI version in the test header.

Verification: live-verify only (real CLI).

---

### Task 4 [Slice B] — `agent_run_questions` migrations (1773106070, 1773106071)

**Execute plan 08 Task 2 as written.**

**Delta (C2 — plan 08 predates the convention doc):**
- Plan 08 Task 2 omits the dual-constant bump; without it `ginkgo ./atc/db/migration/` FAILS (`ExpectDatabaseMigrationVersionToEqual`). In the same commit set `atc/db/migration/legacy_upgrade_test.go` `jetbridgeHeadMigration = 1773106071` and `docs/migration/migrate-preflight.sh` `JETBRIDGE_VERSION=1773106071`. Current value of both is **`1773106066`** (NOT 1773106064 — the scout snapshot predates workflow-source slice-a).
- Pre-flight check before creating files: `ls atc/db/migration/migrations/ | sort | tail -3` must show nothing above `1773106066`. If something landed above it meanwhile, see §5 for the renumber procedure.
- **Specific, already-flagged collision candidate — the sibling judge-evidence remainder plan's migration `1773106080`.** Per `remainders/2026-07-17-judge-evidence.md:1849` its Slice-B-first task lands `1773106080_add_ticket_linkage_to_agent_reviews` (its **Decision D1** default = "land at 1773106080 as reserved"), which advances the version pointer PAST this plan's whole 70-72 block, forcing a renumber-above-head here — exactly the ticket-core precedent. That task is a small, quick-to-land single migration and is likely to merge before this Slice B (Task 4 sits behind the mandatory live-CLI park pin, Task 3). So before creating files, check judge-evidence's status explicitly: if `1773106080` (or any number `> 1773106066`) is in-tree, take the next free numbers ABOVE the ACTUAL head — do NOT assume `1773106066` — and follow §5's renumber procedure (including the `00-shared-contracts.md` §1.9 references to `1773106071`/`1773106072`, which move too).
- The gap `1773106067–69` is intentionally skipped (contract-named numbers win); after this task lands those numbers are dead forever.

Verification: local-verify (`ginkgo ./atc/db/migration/`, needs postgres).

---

### Task 5 [Slice B] — `agent/api/questions` domain types, validation, Store interface, memory store

**Execute plan 08 Task 3 as written.** No delta — the package is greenfield and depends on nothing that moved. Gate-verifiable.

---

### Task 6 [Slice B] — `atc/db` questions factory (SQL Store) + counterfeiter fake

**Execute plan 08 Task 4 as written.**

**Delta:** ticket-core's `UpdateActiveTask` (commit `1e586340e5`, contracts §11 2026-07-17 same-day-fix entry) is the landed store-level precedent for the answered-row guarded write — mirror its single-tx FOR-UPDATE discipline in `Answer` exactly as plan 08 specifies, and cite the commit in yours. Local-verify (`ginkgo ./atc/db/`).

---

### Task 7 [Slice B] — questions HTTP handler (ask, list, long-poll get, guarded answer)

**Execute plan 08 Task 5 as written.** No delta. Gate-verifiable (httptest against the memory store); the DB-backed paths ride Task 6's suite.

---

### Task 8 [Slice B] — idempotency-by-question: migration 1773106072, server-side `question_hash`, find-or-create Ask

**Execute plan 08 Task 5b as written.**

**Delta (C2):** bump both dual constants to `1773106072` in this task's commit (plan 08 omits it; same failure mode as Task 4's delta). The §1.9 contract names `1773106072` explicitly, so if §5's renumber procedure ever moves it, update the contract in the same commit. Local-verify.

---

### Task 9 [Slice B] — ATC route registration (4 question routes, C1 six-touchpoint)

**Execute plan 08 Task 6 as written.**

**Delta:**
- Every helper name plan 08's Task 6 hedged on is confirmed landed exactly as expected (§1 table, "Auth tiers" row) — the `AnswerAgentQuestion` combined-tier snippet in plan 08 compiles against today's tree as-is. `AskAgentQuestion`'s tier is frozen by plan 08's route table: `principal(tickets:write)` only, joining the `atc.UpdateAgentTicketTask` case group.
- CONVENTIONS C1 applies in full: all six touchpoints (routes.go, api_auth_wrappa.go, reject_archived_wrappa.go with its panic, auditor.go with its panic, roles.go, handler.go + its two NewHandler call sites) in ONE commit; run `go test ./atc/wrappa/... ./atc/auditor/...` explicitly — the auditor panic is NOT covered by the api suite.
- All plan-08 line anchors here have drifted (ticket-core + dispatch + workflow-source additions); anchor to named neighbors (`ListTeamAgentReviews`, ticket-core's agent case groups, the `agentReviewPublishToken` param).

Verification: local-verify (`go build ./atc/... && ginkgo ./atc/wrappa/ ./atc/api/ ./atc/auditor/`).

---

### Task 10 [Slice B] — `AnswerAgentQuestion` fires the `agent_dispatcher` component notify

**Execute plan 08 Task 6b as written.**

**Delta:** the notify has NO listener yet — the Dispatcher RunnableComponent + run-completion reconciler were "DEFERRED AS A SET" by the manual-dispatch slice (contracts §11 2026-07-17 entry). This task is wiring-forward, not functional, until plan 11's loop lands; polling remains the guaranteed resume path (§8.4 — never notify-only). Say so in the commit body so nobody hunts for the missing consumer. Local-verify.

---

### Task 11 [Slice C] — sidecar env config + resilient ATC client (`agent/platformmcp`)

**Execute plan 08 Task 9 as written.**

**Delta:** the exec-side producers are FURTHER along than plan 08 assumed — `PLATFORM_MCP_ASK_TIMEOUT_POLICY/_SECONDS` pass-through and `AGENT_PRINCIPAL_TOKEN` secretKeyRef already land in `SidecarEnv`/`SidecarSecretEnv` (`atc/exec/agent_step.go:516-527`). But nothing PRODUCES the planEnv values until Task 25, and `PLATFORM_MCP_PARK_PATH`/`PLATFORM_MCP_EVENTS_PATH` have no producer until plan 07 Task 26 — the config loader must treat every one of these as optional-with-documented-default exactly as plan 08 specifies (unset PARK_PATH = never write a sentinel = the legal checkpoint-pod shape). `AwaitAnswer`'s retry contract is frozen: transport/5xx retry-forever, `AuthFailureLimit` 12 consecutive 401/403 at 5s → `ErrPrincipalRejected` surfaced loudly (F31 leg 3); the F34 pattern (healthz-wait before first use) applies. Gate-verifiable.

---

### Task 12 [Slice C] — short-park threshold config (`PLATFORM_MCP_SHORT_PARK_MAX_SECONDS` + `PLATFORM_MCP_PARK_PATH` consumption)

**Execute plan 08 Task 9c as written.** Delta: same producer note as Task 11 — the renderer side arrives in Task 25; `0`/unset = pure PARK-V1 (never exit), the rollback hatch. Gate-verifiable.

---

### Task 13 [Slice C] — MCP server assembly: read_ticket / list_tasks / get_task / submit_spec / submit_plan / update_task_status

**Execute plan 08 Task 10 as written.**

**Delta:**
- The embedding `GetAgentTicket` payload the three read tools project is CONFIRMED landed (`TicketDetail{Ticket,Spec,Tasks}`) — no shape discovery needed.
- `update_task_status` rides the atomic `UpdateActiveTask` route (TOCTOU already fixed server-side); the tool's input/behavior is unchanged per the contracts' 2026-07-17 same-day-fix entry.
- Read-model frozen delta (2026-07-08) is binding: `read_ticket` = envelope + spec only; `list_tasks` = skeleton; `get_task` = one task; unknown ordering = MCP tool error `isError=true`, NOT `-32602`.
- Builds on Task 2's upgraded `mcpserver` (3-arg handlers with `progress`).

Gate-verifiable (httptest fake ATC).

---

### Task 14 [Slice C] — `ask_human`: park, resume, timeout policies

**Execute plan 08 Task 11 as written.** Delta: none beyond Tasks 11/12's producer notes. The SSE heartbeat keeps the call alive per the Task 3 pin; timeout resolution answers as the principal — which needs Task 26's scope, so sequence Task 26 before any live exercise of the timeout path or the `AnswerAgentQuestion` call 403s. Gate-verifiable.

---

### Task 15 [Slice C] — ask_human long-park exit: threshold timer, `flight/park.json` sentinel, answered-row fast path

**Execute plan 08 Task 11b as written.** Delta: the sentinel write is INERT until plan 07's runner-side watch/SIGTERM/exit-86 lands (`agent/runner` has no park.json watch today — verified). Ship it anyway: the atomic temp+rename write is this plan's half of the frozen PARK-V2 seam, and the open question row remains the durable authority for the wait. Gate-verifiable.

---

### Task 16 [Slice C] — flight-recorder events: sidecar event log

**Execute plan 08 Task 12 as written, MINUS the schema-constants half.** Delta (task shrinks): `human.ask`/`human.answer`/`checkpoint.wait`/`checkpoint.release` constants + typed payloads already exist (`agent/schema/event_payloads.go:15-18,100-124`) — do NOT re-add or reshape them (C3); just import. Only the sidecar's NDJSON writer against `PLATFORM_MCP_EVENTS_PATH` (unset = stdout) remains. Gate-verifiable.

---

### Task 17 [Slice C] — `cmd/platform-mcp` binary (serve mode)

**Execute plan 08 Task 13 as written.** No delta. Gate-verifiable.

---

### Task 18 [Slice D] — checkpoint-gate execution: `POST /checkpoint` endpoint + client mode

**Execute plan 08 Task 14 as written.**

**Delta:** the sidecar-POST model is frozen contract (F14 — supersedes the retracted client→ATC wording; Task 1's addendum carries the supersession). Buildable and httptest-able standalone, but a checkpoint cannot RUN end-to-end until plan 11's side exists (`renderCheckpointStep`, SidecarEnvVar ValueFrom, `--kubernetes-sidecar-secret-prefixes` — all absent, all plan 11's; `atc.SidecarEnvVar` is Name/Value-only today, `atc/sidecar.go:32-35`). Zero write/idle timeouts on the endpoint (§3.2 park-transport exemption); responses 200/400/502; client exits 0/1/2. Gate-verifiable.

---

### Task 19 [Slice D] — `ListByRun` store surface + checkpoint answer validation

**Execute plan 08 Task 14b as written.** Delta: newest-first ordering is FROZEN (plan 11's reconciler re-sorts locally — do not "fix" it). Local-verify (`ginkgo ./atc/db/`).

---

### Task 20 [Slice D] — checkpoint long-park exit: `202 {"parked": true}`, client exit 3, DB-backed dedup fast path

**Execute plan 08 Task 14c as written.** Delta: exit code 3 and the 202 body are frozen (PARK-V2 §B4); like Task 15 this is inert-but-correct until plans 03/11 consume the exit. Gate-verifiable.

---

### Task 21 [Slice D] — contract-test kit (`agent/platformmcp/contracttest`)

**Execute plan 08 Task 15 as written.** No delta. Gate-verifiable.

---

### Task 22 [Slice E] — `agent/notify` webhook notifier library

**Execute plan 08 Task 7 as written.** No delta. Gate-verifiable.

---

### Task 23 [Slice E] — `agent_notifier` RunnableComponent + web flag wiring

**Execute plan 08 Task 8 as written.**

**Delta:** `atc.ComponentAgentNotifier` does not exist yet — add it in `atc/component.go` alongside `ComponentAgentPlatformCredentialSyncer`/`ComponentAgentRunSecretReaper` (lines 25-26; C3 add-never-replace). Polling component, NEVER notify-only (§8.4 — the fork's lossy-NOTIFY lesson: `handleNotification` silently drops on a full channel), gated on `--agent-notify-webhook-url`, marks rows via `notified_at`. Local-verify (component wiring + `ginkgo ./atc/db/`).

---

### Task 24 [Slice F] — sidecar image packaging: `deploy/Dockerfile.platform-mcp` + CI job

**Execute plan 08 Task 16 as written.**

**Delta:** template confirmed at `deploy/Dockerfile.mcp-dev-concourse`, conventions in `deploy/MCP_IMAGES.md` (which already reserves `ghcr.io/tdmtrader/mcp-platform`). F28 is binding: base `alpine:3.21`, nonroot 65532, NEVER distroless — this image doubles as the checkpoint TASK MAIN image and needs POSIX `sh` + `tail`/`mv`/`cat`/`sleep`/`mkdir`/`kill` for jetbridge's pause command and sh supervisor; `command -v` smoke in the Dockerfile. GHCR pushes need the classic PAT (fine-grained PATs fail — release-pipeline gotcha).

Live-verify (use plan 08 Task 16's own verified smoke — NOT `platform-mcp --help`): the Dockerfile's `ENTRYPOINT ["/usr/local/bin/platform-mcp"]` has no `CMD`, and the binary has no `--help` mode — `main.go` special-cases only `os.Args[1] == "checkpoint"`; anything else falls through to `platformmcp.ConfigFromEnv()`, which `os.Exit(2)`s when `ATC_EXTERNAL_URL` is unset (plan 08 Task 9's `TestConfigFromEnvDefaultsAndErrors`). So `docker run … platform-mcp --help` runs `platform-mcp platform-mcp --help` and exits 2 with a config error, verifying nothing. Instead serve it with the required env, exactly as plan 08 Task 16 does:

```bash
docker run --rm -d --name mcp-platform-smoke \
  -e ATC_EXTERNAL_URL=http://127.0.0.1:1 \
  -e AGENT_PRINCIPAL_TOKEN=cap1.0.smoke \
  -e AGENT_TICKET_ID=1 \
  -p 7781:7781 ghcr.io/tdmtrader/mcp-platform:<tag> \
  && sleep 2 && curl -fsS http://127.0.0.1:7781/healthz && docker rm -f mcp-platform-smoke
# F28 task-main-image contract (sh supervisor + binary on PATH):
docker run --rm --entrypoint sh ghcr.io/tdmtrader/mcp-platform:<tag> \
  -c 'command -v platform-mcp && for c in tail mv cat sleep mkdir kill; do command -v "$c" >/dev/null || exit 1; done && echo task-main-contract-ok'
```

plus a theborg pod smoke.

---

### Task 25 [Slice F] — NEW: renderer refusal-lift subset (platform sidecar, mcp delivery, hitl env, short-park env)

Lifts exactly the refusals whose consumer this plan lands, harvest-v0.5 style: platform-ROLE sidecars render (+ auto-injection on ticketed steps), `spec_delivery: mcp` renders, hitl renders as sidecar env. Checkpoints, dev/gateway/custom sidecars, judge, affected-scope gates keep their existing refusal wording verbatim.

> **⚠ CROSS-PLAN COLLISION — `agent/dispatch/render.go` (+ `render_test.go`) is edited by THREE pending plans against the same anchors. Re-read the CURRENT file immediately before editing and ADD/remove alongside whatever already landed (C3); never patch from the line numbers cited below if another plan merged first.**
> 1. **workflow-source-format Task 3 (slice-b)** adds `SystemPrompt: systemPrompt, Context: contextText,` to the SAME `return atc.AgentStep{…}` literal (`render.go:103-113`) this task adds `Sidecars: sidecars` to, and inserts a resolver right after the model/maxTurns block (`workflow-source-format.md:379-396`). A block-replace of that literal against a stale snapshot silently drops the other plan's two fields.
> 2. **judge-evidence Task 11** DELETES the judge refusal (`render.go:160-162`) and removes the `"judge"` sub-case from `TestRenderRefusesDeclaredButUnenforcedPolicyBlocks` (`judge-evidence.md:1875`). So the "judge … stays byte-identical" assertions below hold ONLY if judge-evidence has not landed — if it has, the judge refusal is already gone; do not re-add it.
> 3. These three Tasks MUST land sequentially, never in parallel. Whoever lands second/third re-reads the post-merge file and preserves the others' already-applied edits (removed sub-cases, added struct fields, added refusals). The shared surfaces are: the `RenderAgentStep` return literal, the `Render` refusal chain (`spec_delivery`/gate_policy/hitl/judge/source-format), and the `render_test.go` refusal table test.

**Files:**
- Modify: `agent/dispatch/render.go`
- Modify: `agent/dispatch/dispatch.go` (`Deps` + `RenderInput` threading in `DispatchOne`)
- Modify: `atc/atccmd/command.go` (two flags + `dispatch.Deps` wiring at ~line 2395)
- Modify: `atc/exec/agent_step.go` (one pass-through key, C3 ADD)
- Test: `agent/dispatch/render_test.go` (plain Go, existing `renderInput()` helper style)

- [ ] Write the failing tests — append to `agent/dispatch/render_test.go` (the helper `renderInput()` already builds a files-delivery ticketed input; adjust field spelling to its literal):

```go
func TestRenderInjectsPlatformSidecarOnTicketedSteps(t *testing.T) {
	in := renderInput()
	in.PlatformMCPImage = "ghcr.io/tdmtrader/mcp-platform:v0.1.0"

	cfg, err := dispatch.Render(in)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	step := findAgentStep(t, cfg, "implement")
	if len(step.Sidecars) != 1 || step.Sidecars[0].Config == nil {
		t.Fatalf("want exactly one inline sidecar, got %+v", step.Sidecars)
	}
	sc := step.Sidecars[0].Config
	if sc.Name != "platform" || sc.Image != "ghcr.io/tdmtrader/mcp-platform:v0.1.0" {
		t.Fatalf("want platform sidecar with the flag image, got %+v", sc)
	}
	// §8.1 rows ride step env -> exec planEnv pass-through.
	if step.Env["PLATFORM_MCP_ASK_TIMEOUT_POLICY"] != "park" {
		t.Fatalf("want default ask-timeout policy park, got %q", step.Env["PLATFORM_MCP_ASK_TIMEOUT_POLICY"])
	}
}

func TestRenderDeclaredPlatformSidecarOverridesImage(t *testing.T) {
	in := renderInput()
	in.PlatformMCPImage = "ghcr.io/tdmtrader/mcp-platform:v0.1.0"
	in.Workflow.Sidecars = map[string]workflow.Sidecar{
		"plat": {Image: "ghcr.io/tdmtrader/mcp-platform:v0.2.0", Role: "platform"},
	}
	in.Workflow.Steps[0].Sidecars = []string{"plat"}

	cfg, err := dispatch.Render(in)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	sc := findAgentStep(t, cfg, "implement").Sidecars[0].Config
	if sc.Name != "platform" || sc.Image != "ghcr.io/tdmtrader/mcp-platform:v0.2.0" {
		t.Fatalf("declared platform sidecar must win with well-known name, got %+v", sc)
	}
}

func TestRenderStillRefusesNonPlatformSidecars(t *testing.T) {
	in := renderInput()
	in.PlatformMCPImage = "ghcr.io/tdmtrader/mcp-platform:v0.1.0"
	in.Workflow.Sidecars = map[string]workflow.Sidecar{
		"d": {Image: "ghcr.io/tdmtrader/mcp-dev-concourse:v1", Role: "dev"},
	}
	in.Workflow.Steps[0].Sidecars = []string{"d"}

	if _, err := dispatch.Render(in); err == nil || !strings.Contains(err.Error(), "not deployed") {
		t.Fatalf("want dev-role refusal, got %v", err)
	}
}

func TestRenderMCPDeliveryRequiresImageFlag(t *testing.T) {
	in := renderInput()
	in.Workflow.SpecDelivery = "mcp"
	in.PlatformMCPImage = ""

	if _, err := dispatch.Render(in); err == nil || !strings.Contains(err.Error(), "agent-platform-mcp-image") {
		t.Fatalf("want mcp-without-image refusal naming the flag, got %v", err)
	}

	in.PlatformMCPImage = "ghcr.io/tdmtrader/mcp-platform:v0.1.0"
	if _, err := dispatch.Render(in); err != nil {
		t.Fatalf("mcp delivery with image must render: %v", err)
	}
}

func TestRenderHITLAndShortParkEnv(t *testing.T) {
	in := renderInput()
	in.PlatformMCPImage = "ghcr.io/tdmtrader/mcp-platform:v0.1.0"
	in.Workflow.HITL = workflow.HITL{AskTimeout: "default", AskTimeoutSeconds: 900}
	in.ShortParkMaxSeconds = 1800

	cfg, err := dispatch.Render(in)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	env := findAgentStep(t, cfg, "implement").Env
	for k, want := range map[string]string{
		"PLATFORM_MCP_ASK_TIMEOUT_POLICY":     "default",
		"PLATFORM_MCP_ASK_TIMEOUT_SECONDS":    "900",
		"PLATFORM_MCP_SHORT_PARK_MAX_SECONDS": "1800",
	} {
		if env[k] != want {
			t.Errorf("env[%s] = %q, want %q", k, env[k], want)
		}
	}
}

func TestRenderHITLWithoutImageStillRefuses(t *testing.T) {
	in := renderInput()
	in.PlatformMCPImage = ""
	in.Workflow.HITL = workflow.HITL{AskTimeout: "park"}

	if _, err := dispatch.Render(in); err == nil || !strings.Contains(err.Error(), "hitl") {
		t.Fatalf("want hitl-without-sidecar refusal, got %v", err)
	}
}

// findAgentStep digs the named agent step out of the rendered job plan.
func findAgentStep(t *testing.T, cfg atc.Config, name string) *atc.AgentStep {
	t.Helper()
	for _, s := range cfg.Jobs[0].PlanSequence {
		if as, ok := s.Config.(*atc.AgentStep); ok && as.Name == name {
			return as
		}
	}
	t.Fatalf("agent step %q not in rendered plan", name)
	return nil
}
```

- [ ] Run to see them fail: `go test ./agent/dispatch/ -run TestRender` — expect compile errors (`in.PlatformMCPImage` undefined), then refusal-path failures.

- [ ] Add the two fields to `RenderInput` in `agent/dispatch/render.go` (after `RepoBaseURL`):

```go
	// PlatformMCPImage is the platform-mcp sidecar image
	// (--agent-platform-mcp-image). Empty = the platform sidecar cannot
	// be rendered: spec_delivery "mcp" and hitl blocks refuse exactly as
	// v0 did, and files-delivery workflows render sidecar-less (the
	// rollback hatch).
	PlatformMCPImage string
	// ShortParkMaxSeconds is --agent-short-park-max in whole seconds,
	// rendered as PLATFORM_MCP_SHORT_PARK_MAX_SECONDS on platform-sidecar
	// steps (PARK-V2 §A; 0 = never exit-park, pure PARK-V1).
	ShortParkMaxSeconds int
```

- [ ] In `RenderAgentStep`, replace the blanket sidecar refusal (lines 56-58) with a call to the new resolver, keep the checkpoint refusal (59-61) byte-identical, and append the platform env after the existing `env` map build:

```go
	sidecars, err := renderSidecars(in, step)
	if err != nil {
		return atc.AgentStep{}, err
	}
```

```go
	// §8.1 rows for the platform sidecar: the agent-step exec passes
	// these planEnv keys through to SidecarEnv["platform"]. Emitted only
	// when a platform sidecar is actually rendered.
	if len(sidecars) > 0 {
		policy := in.Workflow.HITL.AskTimeout
		if policy == "" {
			policy = "park"
		}
		env["PLATFORM_MCP_ASK_TIMEOUT_POLICY"] = policy
		if in.Workflow.HITL.AskTimeoutSeconds > 0 {
			env["PLATFORM_MCP_ASK_TIMEOUT_SECONDS"] = strconv.Itoa(in.Workflow.HITL.AskTimeoutSeconds)
		}
		if in.ShortParkMaxSeconds > 0 {
			env["PLATFORM_MCP_SHORT_PARK_MAX_SECONDS"] = strconv.Itoa(in.ShortParkMaxSeconds)
		}
	}
```

  and set `Sidecars: sidecars` in the returned `atc.AgentStep` (the field exists: `atc/steps.go:411`). Add the resolver:

```go
// renderSidecars resolves a step's sidecar declarations plus the
// automatic platform sidecar. Platform-role sidecars render (their
// image is deployed — §8.5 instantiation); dev/gateway/custom roles
// keep the v0 refusal until their images exist (wave 3). Per the §3.2
// read-model, every TICKETED agent step carries the platform sidecar in
// BOTH delivery modes: a declared platform-role sidecar wins on image,
// otherwise --agent-platform-mcp-image is injected. Pure-CI (ticketless)
// renders and an empty image flag inject nothing. The rendered container
// name is ALWAYS "platform" — §8.1 derives PLATFORM_MCP_URL strictly by
// well-known name, regardless of the workflow's map key.
func renderSidecars(in RenderInput, step workflow.Step) ([]atc.SidecarSource, error) {
	var out []atc.SidecarSource
	havePlatform := false
	for _, name := range step.Sidecars {
		sc, ok := in.Workflow.Sidecars[name]
		if !ok {
			return nil, fmt.Errorf("agent step %q references undeclared sidecar %q", step.Agent, name)
		}
		if sc.Role != "platform" {
			return nil, fmt.Errorf("agent step %q declares sidecar %q (role %q): only platform-role sidecars render — dev/gateway wave-3 sidecar images are not deployed", step.Agent, name, sc.Role)
		}
		if havePlatform {
			return nil, fmt.Errorf("agent step %q declares more than one platform-role sidecar", step.Agent)
		}
		out = append(out, atc.SidecarSource{Config: &atc.SidecarConfig{Name: "platform", Image: sc.Image}})
		havePlatform = true
	}
	if !havePlatform && in.Ticket.ID > 0 && in.PlatformMCPImage != "" {
		out = append(out, atc.SidecarSource{Config: &atc.SidecarConfig{Name: "platform", Image: in.PlatformMCPImage}})
	}
	return out, nil
}
```

- [ ] In `Render`, replace the `spec_delivery` switch (lines 125-131) and the hitl refusal (157-159) — every other refusal (gate_policy, source-format, checkpoint via `RenderAgentStep`, and the judge refusal UNLESS judge-evidence Task 11 already removed it — see the collision box) stays untouched:

```go
	switch in.Workflow.SpecDelivery {
	case "files":
	case "", "mcp":
		// mcp (the §3.2 default): the agent reads spec/plan through the
		// platform sidecar's read tools; no ticket artifact is forced.
		// Refuse loudly without the sidecar image — never a silent
		// files fallback.
		if in.PlatformMCPImage == "" {
			return atc.Config{}, fmt.Errorf("workflow %q spec_delivery %q requires the platform sidecar: set --agent-platform-mcp-image (or declare spec_delivery: files)", in.WorkflowName, in.Workflow.SpecDelivery)
		}
	default:
		return atc.Config{}, fmt.Errorf("workflow %q has unknown spec_delivery %q", in.WorkflowName, in.Workflow.SpecDelivery)
	}
```

```go
	if (in.Workflow.HITL.AskTimeout != "" || in.Workflow.HITL.AskTimeoutSeconds != 0) && in.PlatformMCPImage == "" {
		return atc.Config{}, fmt.Errorf("workflow %q declares an hitl block but no platform sidecar is configured: set --agent-platform-mcp-image", in.WorkflowName)
	}
	if p := in.Workflow.HITL.AskTimeout; p != "" && p != "park" && p != "default" && p != "fail" {
		return atc.Config{}, fmt.Errorf("workflow %q hitl.ask_timeout %q: want park|default|fail", in.WorkflowName, p)
	}
```

  (`needsTicket`/`writeTicketTask` behavior is untouched: a step that explicitly consumes `ticket` still gets the artifact in either mode — additive, C3.)

- [ ] Thread through `dispatch.Deps` (add `PlatformMCPImage string` and `ShortParkMaxSeconds int` fields with the same comments) and set them on the `RenderInput` literal inside `DispatchOne`.

- [ ] Wire the flags in `atc/atccmd/command.go` — add next to `AgentRepoBaseURL` (~line 234):

```go
	AgentPlatformMCPImage string        `long:"agent-platform-mcp-image" default:"" description:"Platform-mcp sidecar image dispatch injects into ticketed agent steps (e.g. ghcr.io/tdmtrader/mcp-platform:v0.1.0). Empty disables the platform sidecar: spec_delivery mcp and hitl workflows are refused at render time."`
	AgentShortParkMax     time.Duration `long:"agent-short-park-max" default:"30m" description:"Longest in-pod ask_human/checkpoint park before the platform sidecar signals exit-and-respawn (PARK-V2). 0 parks in-pod indefinitely (pure PARK-V1)."`
```

  and in the `dispatch.Deps{...}` literal (~line 2395): `PlatformMCPImage: cmd.AgentPlatformMCPImage,` `ShortParkMaxSeconds: int(cmd.AgentShortParkMax.Seconds()),`.

- [ ] One-hunk exec change in `atc/exec/agent_step.go` (~line 519), C3 ADD to the existing list — the platform sidecar pass-through gains the third key (**shared file with workflow-source-format Tasks 3a/4, which append `AGENT_SYSTEM_PROMPT`/`AGENT_CONTEXT`/`AGENT_SKILL_DIRS` at a DIFFERENT region ~`:370-372`; both additive — re-read the file before editing rather than patching the cited line**):

```go
			for _, k := range []string{"PLATFORM_MCP_ASK_TIMEOUT_POLICY", "PLATFORM_MCP_ASK_TIMEOUT_SECONDS", "PLATFORM_MCP_SHORT_PARK_MAX_SECONDS"} {
```

  Diff-check the hunk: both pre-existing keys must survive (C3 corollary).

- [ ] Run to pass: `go test ./agent/dispatch/ && go build ./atc/... && go test ./atc/exec/ -run TestExec -count=1`. Expected: all green; the existing v0 refusal tests for checkpoints/gate_policy/judge/source-format still pass unchanged. If any existing test asserted the OLD sidecar/hitl/mcp refusals, update those assertions in this commit and call each one out in the commit body.
  - **Do NOT use `-run Agent`** for the exec leg: `atc/exec` is a Ginkgo suite whose ONLY top-level Go test function is `TestExec` (`atc/exec/exec_suite_test.go:21`); the AgentStep specs live in `Describe("AgentStep", …)` (`agent_step_test.go:47`), reached through `RunSpecs` under `TestExec`. `go test -run Agent` matches only against top-level `func Test…` names, and "Agent" is not a substring of "TestExec", so it runs ZERO specs and prints a green PASS that verifies nothing — the exact env-row change this task makes would be untested. `-run TestExec` (or `ginkgo --focus="AgentStep" ./atc/exec/`) actually exercises the `for _, k := range []string{…}` pass-through list at `agent_step.go:519`.

- [ ] Commit: `feat(dispatch): render platform sidecar + hitl env; lift spec_delivery mcp (wave-3 subset)` with the refusals-kept list in the body.

---

### Task 26 [Slice F] — NEW: per-run principal mint gains park-aware expiry (layered on the sibling's `agent-run-<id>` rename)

> **⚠ CROSS-PLAN OWNERSHIP — the `attachRunSecret` mint block (name/scope/expiry) is edited by BOTH this Task 26 and the sibling `remainders/2026-07-17-dispatcher-budget-reconciler.md` Task 8; they diff from the SAME landed baseline (`dispatch.go:197-208`: `Name: fmt.Sprintf("run-%d", runID)`, hardcoded `24*time.Hour`, 4 scopes, no `questions:answer`). This box mirrors that plan's ownership box (its lines 1548-1554), which explicitly instructs it be carried here.**
> - **Sequencing: land dispatcher-budget-reconciler Task 8 FIRST.** It is the independent rename + reaper fix: it renames the principal to `credentials.RunSecretName(runID)` = `agent-run-<id>` (so `secret_reaper.go:117`'s `RevokeByName(RunSecretName(runID))` can find it — revoke-by-name is BROKEN today because mint writes `run-<id>` while the reaper expects `agent-run-<id>`), adds `principals.ScopeQuestionsAnswer` (5 scopes), and introduces `Deps.RunTimeout` (from `--agent-run-timeout`, 6h default; zero-value falls back to the landed 24h).
> - **After Task 8, THIS task layers ONLY park-aware expiry.** It adds `Deps.ParkTimeout` + `--agent-park-timeout`, and changes the ONE expiry line from `now + RunTimeout` to a workflow-CONDITIONAL selection via `RunPrincipalTimeout`. It must **keep the `agent-run-<id>` name and the `questions:answer` scope Task 8 already landed** — do NOT restate `Name: fmt.Sprintf("run-%d", runID)` or re-diff from the pre-rename baseline (C3: whoever lands second AMENDS the landed call; a wholesale block replace silently reverts the reaper fix).
> - **If this task lands FIRST instead** (Task 8 not yet merged): additionally apply Task 8's rename + `questions:answer` scope + `Deps.RunTimeout` here, and flag it so Task 8 reduces to a no-op on those fields.
> - **Expiry is workflow-conditional, NOT unconditional + a margin.** The FROZEN contract row (`00-shared-contracts.md`, §8.1 `AGENT_PRINCIPAL_TOKEN`: "`expires_at` = now + `--agent-run-timeout` (6h default) — EXCEPT when the rendered workflow contains a park-policy `ask_human` or any checkpoint, in which case … now + `--agent-park-timeout`") is normative. An ORDINARY (non-park) run must keep the 6h bound — it must NOT get a 72h/84h token. The selector is the frozen `dispatch.RunPrincipalTimeout(cfg, runTimeout, parkTimeout)` helper (defined in `11-dispatch.md:2465-2476`); there is NO "+12h margin" anywhere in the contract or plan 11 — drop it. (The sibling plan's line 1553 sketch of `now + max(RunTimeout, ParkTimeout+12h)` is unconditional-84h and contradicts both the contract and its own stated "non-park run keeps 6h" goal; `RunPrincipalTimeout` is the contract-faithful reconciliation both plans should converge on — flag this to the dispatcher-budget-reconciler owner.)

**Files:**
- Modify: `agent/dispatch/dispatch.go` (`Deps.ParkTimeout`; `attachRunSecret` signature gains the workflow config + park-aware expiry; add `RunPrincipalTimeout` if plan 11 hasn't landed it)
- Modify: `atc/atccmd/command.go` (flag + Deps wiring)
- Test: `agent/dispatch/dispatch_test.go`

- [ ] Write the failing tests — append to `agent/dispatch/dispatch_test.go`, using the REAL fixtures (there is no `testDeps`/`dispatchTicketForTest`): `dispatchDeps(t)` returns a 4-tuple `(dispatch.Deps, *tickets.MemoryStore, *fakeSaver, *dbfakes.FakePipelineRunFactory)` (`:87`) whose `run.IDReturns(555)`, and you queue+dispatch via `queuedTicket(t, store, "smoke")` (`:115`) then `dispatch.DispatchOne(context.Background(), deps, id, "admin")` (`:135`). Override `deps.Principals` with a store you hold a reference to (the one inside `dispatchDeps` is not returned). `smoke` is a plain files workflow (no hitl/checkpoint), so it exercises the ORDINARY expiry path. (Add the `time` import — the file does not import it yet.)

```go
func TestDispatchMintsQuestionsAnswerPrincipalWithRunTimeoutExpiry(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	pstore := principals.NewMemoryStore()
	deps.Principals = pstore
	deps.RunTimeout = 6 * time.Hour   // landed by dispatcher-budget-reconciler Task 8
	deps.ParkTimeout = 72 * time.Hour // added by THIS task
	id := queuedTicket(t, store, "smoke")

	if _, err := dispatch.DispatchOne(context.Background(), deps, id, "admin"); err != nil {
		t.Fatalf("DispatchOne: %v", err)
	}

	ps, err := pstore.List()
	if err != nil || len(ps) != 1 {
		t.Fatalf("want 1 minted principal, got %d (err %v)", len(ps), err)
	}
	p := ps[0]
	// §2.8.2: name is agent-run-<run-id> so the reaper's
	// RevokeByName(RunSecretName(runID)) finds it (secret_reaper.go:117).
	if p.Name != credentials.RunSecretName(555) {
		t.Fatalf("principal name = %q, want %q (reaper revoke-by-name)", p.Name, credentials.RunSecretName(555))
	}
	if !p.HasScope(principals.ScopeQuestionsAnswer) {
		t.Fatalf("run principal missing questions:answer (F31 follow-up 3): %v", p.Scopes)
	}
	if p.ExpiresAt == nil {
		t.Fatal("run principal must NEVER be non-expiring (§8.1)")
	}
	// A plain (non-park) workflow expires at now+RunTimeout — the park bound
	// must NOT leak onto an ordinary run.
	lo := time.Now().Add(5 * time.Hour).Unix()
	hi := time.Now().Add(7 * time.Hour).Unix()
	if *p.ExpiresAt < lo || *p.ExpiresAt > hi {
		t.Fatalf("plain-workflow expiry %d not ~now+RunTimeout(6h) [%d,%d] — park bound leaked", *p.ExpiresAt, lo, hi)
	}
}

// If plan 11 already landed dispatch.RunPrincipalTimeout, SKIP this test and
// the helper below — consume the existing one (C3).
func TestRunPrincipalTimeoutSelectsParkForParkWorkflows(t *testing.T) {
	plain := workflow.Config{Steps: []workflow.Step{{Agent: "a"}}}
	park := workflow.Config{HITL: workflow.HITL{AskTimeout: "park"}}
	checkpoint := workflow.Config{Steps: []workflow.Step{{Agent: "gate", Checkpoint: "approve"}}}
	if got := dispatch.RunPrincipalTimeout(plain, 6*time.Hour, 72*time.Hour); got != 6*time.Hour {
		t.Errorf("plain workflow: got %v, want 6h (run timeout)", got)
	}
	if got := dispatch.RunPrincipalTimeout(park, 6*time.Hour, 72*time.Hour); got != 72*time.Hour {
		t.Errorf("ask_timeout park: got %v, want 72h (park timeout)", got)
	}
	if got := dispatch.RunPrincipalTimeout(checkpoint, 6*time.Hour, 72*time.Hour); got != 72*time.Hour {
		t.Errorf("checkpoint step: got %v, want 72h (park timeout)", got)
	}
}
```

- [ ] Run to see them fail: `go test ./agent/dispatch/ -run 'TestDispatchMintsQuestionsAnswerPrincipalWithRunTimeoutExpiry|TestRunPrincipalTimeout'` — expect `deps.ParkTimeout`/`dispatch.RunPrincipalTimeout` undefined, then (once Task 8 is in) the expiry-window assertion failing because the landed mint uses `now + RunTimeout` unconditionally.

- [ ] Add to `Deps` (KEEP the `RunTimeout` field Task 8 landed — do not remove it):

```go
	// ParkTimeout is --agent-park-timeout: the platform-wide bound on an
	// awaiting_human park (72h default; plan 03's lifecycler consumes the
	// same flag for the wall clock). A run whose frozen workflow declares a
	// park-policy ask_human or a checkpoint is minted with this lifetime so
	// a parked question is not killed by the 6h RunTimeout; every other run
	// keeps RunTimeout. Selection is RunPrincipalTimeout — NOT a margin.
	ParkTimeout time.Duration
```

- [ ] Add the frozen selector to `agent/dispatch/dispatch.go` **IF plan 11 has not already landed it** (`grep -n "func RunPrincipalTimeout" agent/dispatch/*.go` — if present, consume it and skip this step):

```go
// RunPrincipalTimeout selects the per-run principal lifetime (F31
// principal-expiry leg; contracts §8.1 AGENT_PRINCIPAL_TOKEN row): a run
// whose frozen workflow contains a park-policy step — hitl.ask_timeout=="park"
// or any checkpoint step (checkpoints always park) — is minted with
// now + parkTimeout so a parked question outlives the 6h run bound; every
// other run uses runTimeout. Expiry stays NOT NULL either way — a park
// outliving it fails LOUDLY via AwaitAnswer's fatal-auth contract, never as
// a silent forever-park.
func RunPrincipalTimeout(cfg workflow.Config, runTimeout, parkTimeout time.Duration) time.Duration {
	if cfg.HITL.AskTimeout == "park" {
		return parkTimeout
	}
	for _, s := range cfg.Steps {
		if s.Checkpoint != "" {
			return parkTimeout
		}
	}
	return runTimeout
}
```

- [ ] Thread the frozen workflow config into `attachRunSecret` so it can select the expiry. Change the signature to `attachRunSecret(ctx context.Context, deps Deps, t *tickets.Ticket, wf workflow.Config, runID int)` and the sole call site (`dispatch.go:167`) to `attachRunSecret(ctx, deps, t, def.Config, runID)` (`def` is in scope there).

- [ ] AMEND the mint call inside `attachRunSecret` — change ONLY the expiry expression, keeping the `credentials.RunSecretName(runID)` name and the 5 scopes Task 8 landed. The converged end-state (both plans applied) is:

```go
	principalToken := ""
	if deps.Principals != nil {
		runTimeout := deps.RunTimeout // dispatcher-budget-reconciler Task 8; zero → its landed 24h fallback
		if runTimeout <= 0 {
			runTimeout = 24 * time.Hour
		}
		parkTimeout := deps.ParkTimeout
		if parkTimeout <= 0 {
			parkTimeout = 72 * time.Hour
		}
		expires := time.Now().Add(RunPrincipalTimeout(wf, runTimeout, parkTimeout)).Unix()
		_, token, err := deps.Principals.Create(principals.CreateSpec{
			Name:        credentials.RunSecretName(runID), // §2.8.2 — reaper revoke-by-name (Task 8)
			Description: fmt.Sprintf("per-run principal for pipeline run %d (ticket %d)", runID, t.ID),
			Scopes: []string{
				principals.ScopeTicketsRead,
				principals.ScopeTicketsWrite,
				principals.ScopeMetricsWrite,
				principals.ScopeCostsWrite,
				principals.ScopeQuestionsAnswer, // Task 8
			},
			CreatedBy: "dispatch",
			ExpiresAt: &expires,
		})
		if err != nil {
			return fmt.Errorf("mint run principal: %w", err)
		}
		principalToken = token
	}
```

  If Task 8 already landed, the ONLY lines this task changes are the two `runTimeout`/`parkTimeout` locals, the `RunPrincipalTimeout(...)` expression on the `expires` line, and the `wf` param — leave `Name`/`Scopes` exactly as Task 8 left them. Update `attachRunSecret`'s doc comment to name the park-aware, workflow-conditional rule (it currently says "name run-<id>, 24h expiry").

- [ ] Wire the flag in `atc/atccmd/command.go` next to Task 25's flags (and beside Task 8's `--agent-run-timeout`):

```go
	AgentParkTimeout time.Duration `long:"agent-park-timeout" default:"72h" description:"Platform-wide bound on an awaiting_human park. A run whose workflow declares a park-policy ask_human or a checkpoint mints its principal to last this long (ordinary runs use --agent-run-timeout); the pipeline-runs lifecycler (plan 03) errors runs whose oldest open park question exceeds it."`
```

  and `ParkTimeout: cmd.AgentParkTimeout,` in the `dispatch.Deps{...}` literal (leave Task 8's `RunTimeout: cmd.AgentRunTimeout,` in place).

- [ ] Run to pass: `go test ./agent/dispatch/ && go build ./atc/...`.

- [ ] Commit: `feat(dispatch): park-aware run-principal expiry (RunPrincipalTimeout) atop the agent-run-<id> rename` citing the Task 1 addendum bullet and the dispatcher-budget-reconciler Task 8 ownership box.

---

### Task 27 [Slice G] — ticket-page question banner + answer UI (Elm)

**Execute plan 08 Task 17 as written.**

**Delta:** the ticket page landed DIFFERENTLY from plan 08's wave-2 assumption — it is the agentic-ui wave's PR-view page `web/elm/src/AgentTickets/AgentTicket.elm` with the reserved empty `div#ticket-hitl-slot` now at **line 365** (waves E+F shifted it from 364) and an established fetch/poll pattern + data-layer spine. Fill the slot; follow the spine-serialization rule (no parallel Elm edits in flight anywhere else); the answer PUT hits Task 9's `AnswerAgentQuestion`; the list GET hits `ListAgentTicketQuestions`. Remember the SEPARATE embedded-bundle regen commit (precedent `46db7b9735`). Local-verify: `elm-test` + `elm make` in `web/elm`; no gate coverage exists for Elm. **Cross-plan co-editors of `AgentTicket.elm` + the spine (`Effects.elm`/`Callback.elm`/`Endpoints.elm`) + the SAME embedded bundle:** delivery-outcomes Slice D (outcome badge / paginated diff / disposition form on `AgentTicket.elm`) and judge-evidence Task 13 (build-page Elm, same bundle). Serialize ALL Elm work across the three plans — no two Elm slices run concurrently; rebase onto whatever Elm landed first before filling the slot, and regenerate the bundle as this task's own final commit.

---

### Task 28 [Slice G] — "Awaiting human" state chip on the question banner

**Execute plan 08 Task 17b as written.** Delta: the dashboard already renders `awaiting_human` runs with the shared `AgentBadge` (`web/elm/tests/PipelineTests.elm:890,961`) — reuse that badge component for the chip rather than minting a new style. Same bundle-regen note as Task 27.

---

### Task 29 [Slice H] — live theborg test: restart-while-parked

**Execute plan 08 Task 18 as written.**

**Delta:** scope precisely what this proves TODAY: a SHORT park (SSE-held `ask_human`) surviving a web restart via the resilient `AwaitAnswer` retry loop + the supervised agent container + looping pause pod (all landed). The full exit-and-respawn resume across restarts needs plans 03/07/11 and is NOT this test's claim — say so in the test header. Throwaway namespace on theborg (NEVER `cicd`/`concourse`), harness per `live_agent_resume_test.go` conventions (`kubeClient(t)`, `K8S_TEST_NAMESPACE`, kube-context `theborg`), `t.Cleanup` deletes pods then the namespace. Fake clientset cannot exercise this — live mandatory. Run Task 26 first or the timeout-resolution leg 403s.

---

### Task 30 [Slice H] — full verification sweep

**Execute plan 08 Task 19 as written.** Delta: add to its checklist — Task 25's refusal-boundary spot-check (a workflow declaring a dev-role sidecar, a checkpoint, or affected-scope gates must STILL refuse with the pre-existing wording), `make test-quick` locally, and the dispatch-timing rule for anything self-deployed (push → settle → dispatch; a self-upgrade restarts web and double-spends agents).

---

## 5. Migration allocations

**Current head (verified in-tree 2026-07-17): `1773106066`** — `atc/db/migration/legacy_upgrade_test.go:37` and `docs/migration/migrate-preflight.sh:38`. This SUPERSEDES the scout snapshot's 1773106064: workflow-source slice-a landed `1773106066_add_agent_workflow_source_manifest` (commit `ac9347c9aa`) while this plan was being written.

| Number | Content | Task | C2 bump in that commit |
|---|---|---|---|
| `1773106070` | `CREATE TABLE agent_run_questions` + open/ticket indexes | Task 4 | — (bumped with 71) |
| `1773106071` | `notified_at` + `agent_run_questions_unnotified` partial index | Task 4 | both constants → `1773106071` |
| `1773106072` | `question_hash` + UNIQUE partial dedup index | Task 8 | both constants → `1773106072` |

Rules (all binding):
- **C2 dual-constant:** `jetbridgeHeadMigration` and `JETBRIDGE_VERSION` move together in the same commit as the migration files. Plan 08 Tasks 2/5b predate the convention doc and omit this — the deltas on Tasks 4/8 add it.
- **Lowest-first / hole rule:** `1773106067–69` are unreserved and become permanently dead once `1773106070` lands (the version-pointer migrator never revisits below-head numbers). Nothing is reserved there — an acceptable sacrifice to keep the contract-named 1773106070–72. The reserved-but-absent numbers now BELOW head that OTHER plans must renumber at land time: `1773106065` (`agent_run_step_state`, plan 07 — overtaken by workflow-source slice-a) and `1773106032` (`awaiting_human`, plan 03). Neither is this plan's to land, but Task 1's addendum records the re-pin so no executor trusts the stale numbers.
- **Land-time re-check (Task 4 pre-flight):** if anything lands above `1773106066` before Task 4 merges, take the next free numbers ABOVE the actual head instead, and update in the same commit: both C2 constants, the §1.9 contract references to 1773106071/1773106072, and this file. Ticket-core's renumber (1773106050-52 → 1773106062-64) is the worked precedent. **The specific candidate to check for is judge-evidence's `1773106080`** (`remainders/2026-07-17-judge-evidence.md:1849`, Decision D1 default = land as reserved) — a single quick migration likely to merge before this plan's Slice B clears the Task 3 park-pin gate. If it (or anything else) is above head, renumber this block above the ACTUAL head; the 70-72 numbers are contract-named but the hole rule wins — the version-pointer migrator silently skips lower numbers merged late.
- **Merge order within this plan:** Task 4 (70-71) merges before Task 8 (72). Never in the same push as any other plan's migration without checking relative numbering.
- The `1773106050–59` block stays vacated forever.

## 6. Risks & open decisions

**Decisions needing the human owner (flag before executing the affected task):**
1. **Auto-injection policy (Task 25):** this plan injects the platform sidecar into EVERY ticketed agent step when `--agent-platform-mcp-image` is set, per the §3.2 read-model ("tools mounted in both modes"). Alternative: inject only when `spec_delivery: mcp` or hitl is declared. Injection-always is the contract-faithful reading and makes `ask_human` universally available, but every agent pod grows a container. **Owner sign-off on the Task 1 addendum bullet decides this** — it is written as injection-always.
2. **Whether plan 07's runner-side PARK-V2 halves ship in the same wave** (carried open question): this plan is written short-park-first — Tasks 15/20 ship inert sentinels/exit signals, `--agent-short-park-max=0` is the rollback hatch, and NOTHING here blocks on plan 07. If the owner wants long-park end-to-end in one wave, schedule plan 07's runner tasks (watch/exit-86/session capture + `agent_run_step_state` renumbered above head) between Slices D and H.
3. **Dispatcher-loop timing (plan 11):** Task 10's notify and PARK-V2 resume both target the deferred-as-a-set Dispatcher/reconciler. Until they land, `fly agent tickets transition`/`close` is the manual resume path for awaiting runs. No action here; recorded so the wave review doesn't mistake it for a gap.
4. **`AskAgentQuestion` auth tier** is frozen by plan 08 Task 6 as `principal(tickets:write)` only — resolved, no new scope. Re-confirm at review only if gateway's `ask_agent` (plan 10) wants to reuse the route.

**Execution risks:**
- **Task 3 pin failure** invalidates the transport design for Tasks 11–21 — that is why it is sequenced third, before any sidecar code exists. Do not reorder.
- **Task 2 blast radius:** the mechanical 3-arg sweep touches the live ATC MCP endpoint's ~26 tools. Pure-Go tests cover it, but a missed closure is a compile error at best and behavior drift at worst — review the tools.go diff hunk-by-hunk (C3 discipline).
- **Migration races with parallel sessions:** the head moved 1773106064→1773106066 *while this item was being scouted*. The Task 4 pre-flight check is mandatory, not ceremonial.
- **Elm collisions:** waves E+F just landed; the spine-serialization rule means Tasks 27/28 must not run concurrently with any other Elm-touching session, and the bundle regen is its own commit.
- **Principal-expiry interim window:** until Task 26 lands, any live `ask_human` timeout-resolution exercise 403s (mint lacks `questions:answer`). Task 26 precedes Task 29 in the order for this reason.
- **Loop dispatch budget:** every loop ticket spends the shared Claude window; do not fan out Slices C and D in parallel with a native session doing Slice B (dispatch-timing rule: push → settle → dispatch).

## Complexity, risk, and recommended execution level

**Honest sizing:** 30 tasks, but two-thirds execute plan 08's already-complete task bodies. The genuinely new surface is small (Tasks 1, 25, 26 + deltas); the risk concentrates in (a) the SSE upgrade's blast radius on a live endpoint, (b) the C1 six-touchpoint route registration with its two panicking switches, (c) migration ordering in a repo whose head demonstrably moves mid-plan, and (d) the two live proofs that cannot be simulated.

**Recommended level: split**, per slice:

| Slice | Level | Rationale |
|---|---|---|
| A: Task 1 | native-fable | Contract-amendment judgment; cheap; do it in the owner session that reviews this plan |
| A: Task 2 (SSE) | loop-opus | Mirrored implementation with wire spec and tests fully written in plan 08; pure-Go gate-verifiable; opus not sonnet because the ~26-closure sweep + frozen error-mapping nuance needs care; human reviews the tools.go diff |
| A: Task 3 (park pin) | native-fable | Real claude CLI on the owner's machine, >5min wall clock, design-invalidating if it fails — the definition of native work. WAVE-3 ENTRY GATE |
| B (Tasks 4–10) | native-opus | Migrations (C2 + ordering hazard), postgres-backed factory, C1 six-touchpoint with two panicking switches. Plan text spells out all six touchpoints so loop is *possible*, but the mandatory human local-verify before every merge erases the loop's economics; one native session lands the slice in order |
| C (Tasks 11–17) | loop-opus (12, 16, 17 loop-sonnet) | Exactly the proven ticket #13/#14 profile: plain-Go httptest in `agent/*`, no postgres, full task text in plan 08, gate-verifiable end-to-end. 12/16/17 are mechanical enough for sonnet; 11/13/14/15 carry protocol judgment → opus. One ticket per task, sequential (shared rate window) |
| D (Tasks 18–21) | loop-opus, EXCEPT Task 19 native-opus | 18/20/21 are httptest pure-Go (frozen F14 model, pinned exit codes); 19 is a postgres-backed store surface — fold it into the Slice B native session or a short native follow-up |
| E (Tasks 22–23) | 22 loop-sonnet; 23 native-opus | The webhook library is mechanical httptest; the component registration + web flag is `atccmd` wiring with DB specs |
| F (Tasks 24–26) | native-fable | 24 needs docker+ghcr (no loop access); 25/26 are small but carry the two open owner decisions and touch dispatch/exec/atccmd across boundaries — design judgment mid-flight |
| G (Tasks 27–28) | native-opus | Elm: no gate coverage, spine-serialization rule, bundle-regen ritual — never loop |
| H (Tasks 29–30) | native-fable | Live theborg, throwaway-namespace discipline, restart choreography, final-sweep judgment |

**Sequencing spine:** 1 → 2 → **3 (gate)** → B natively while C loops → D → E → F natively → G → H. The loop's output lands as `agent/ticket-N` branches; the human merges in migration-safe order and runs each slice's local-verify column before merging it.
