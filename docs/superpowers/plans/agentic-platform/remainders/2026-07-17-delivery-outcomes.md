# Delivery Outcomes — Remainder Plan (re-scope of plan 12)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. This plan is a DELTA over `docs/superpowers/plans/agentic-platform/12-delivery-outcomes.md` — where a task says "execute plan 12 Task N as written", open that file, execute its full text, and apply ONLY the delta notes listed here. Where a task here carries full text, that text supersedes the plan-12 original.

**Date:** 2026-07-17
**Status:** draft for review
**Depends-on (all LANDED on `jetbridge`, verified at HEAD `187cad4926`):** ticket-core (migrations 1773106062-64, commit 8f830e779d), manual-dispatch + fly UX round, harvest v0 (0689163540..1aef877c49) + harvest gates v0.5 (b8d064906c, ticket #14), agentic-ui wave A-D (66c3eb45ba, merged 4a7be09b7c) **plus waves E+F (0866d89fc9 — see Landed state; they pre-satisfy part of the Elm scope)**, workflow-source-format slice-a (550a8dbd7a, migration **1773106066** — it moved the dual constants since the original scout pass).
**Does NOT depend on:** plan-09 harvest-step remainder (judge, `agent_reviews` ticket linkage / migration ~1773106080) — explicitly deferred, see Scope Out.

**Goal:** Land the plan-12 remainder — the `agent_outcomes` table + disposition write path (taxonomy, notes, attribution), the native outcome watcher that records merged / merged-with-fixes / human-touch delta by polling git mirrors, and the Elm PR-view remainder (outcome badge, paginated in-app diff, disposition form) — on top of the already-shipped ticket page.

**Architecture:** `agent_outcomes` (migration in the 1773106090 block) + `agent/api/outcomes` + `atc/db.NewAgentOutcomesFactory` hold merge facts and dispositions keyed uniquely by ticket. **Sha provenance changes from the original plan:** harvest-runner now emits `base_sha` in its results metadata and `exec.HarvestStep` tees the runner's stdout to capture `results.json`, seeding the outcome row with authoritative `pushed_sha`/`base_sha` at push time (the original plan's `agent_run_metrics` lookup is dead — harvest never wrote metrics rows); the watcher keeps only the branch-head fallback as backstop. `agent/gitcheck` maintains bare `--mirror` caches on the web node; `agent/outcomewatcher` is a polling-only RunnableComponent that detects merges (is-ancestor primary, patch-id squash fallback) and walks ticket state exclusively through `tickets.Store.Transition`. Three routes land now (`SetAgentTicketDisposition`, `GetAgentTicketOutcome`, `GetAgentTicketDiff`); `GetAgentTicketReviews` stays reserved until plan-09's ticket-linkage migration exists.

**Tech stack:** Go (plain `testing` for `agent/api/outcomes` and `agent/harvest`; Ginkgo/Gomega for `agent/gitcheck`, `agent/outcomewatcher`, `atc/db`, `atc/exec`, `atc/wrappa`, `atc/auditor`), PostgreSQL migration + factory (recipe: `atc/db/agent_reviews_factory.go`), real-git fixture tests (recipe: `agent/harvest/runner_test.go` — NOT `workspace.go`, which does not exist), counterfeiter fakes, jessevdk/go-flags (fly), Elm 0.19 + elm-test + embedded-bundle regeneration.

---

## 1. Landed state — the executor must NOT rebuild any of this

Verified on `jetbridge` @ `187cad4926`, 2026-07-17 (post waves E+F):

| What | Where | Notes |
|---|---|---|
| Ticket state machine incl. terminal `concluded`; `needs_review → merged \| merged_with_fixes \| sent_back \| abandoned \| concluded \| queued` | `agent/api/tickets/types.go:41-49`, `:24` | Single-writer `Transition` with from-guard; `TransitionMeta{PipelineRunID, Branch, ErrorDetail}` — **NO `By` field** |
| `atc/db` tickets factory | commit 8f830e779d | |
| `fly agent tickets close` — walks running→needs_review→<disposition> via **bare** `TransitionAgentTicket`; default `concluded`; NO taxonomy/notes/disposed_by | `fly/commands/agent_tickets.go:350-405` | Task B3 re-points its terminal hop |
| Elm ticket detail page `/agent-tickets/:id`: spec/plan tabs, live task list, lifecycle buttons mirroring `validTransitions` (server-authoritative 409→refetch), per-run cost summed from run-metrics `costUsd`, review-evidence panel reusing `Build.AgentReview`, six-verdict feedback | `web/elm/src/AgentTickets/AgentTicket.elm` (930 lines; `transitionTargets` :517-544, cost sums :551/:805, `SubmitAgentReviewVerdict` :288) | Commits 66c3eb45ba + 9ac2bf4b9a + 0866d89fc9. **Do not rebuild the tabs/tasks/evidence/cost/feedback** — Tasks D1-D3 are additive deltas only |
| **Waves E+F (0866d89fc9):** provenance line on ticket detail — repo web link + `branch <b> — review diff vs <target>` GitHub **compare link** (`Concourse/AgentTicket.elm` `repoWebUrl` :164 / `compareUrl` :202, rendered :393-443); queue rows show the branch; RUNS rows link to their build; unattributed spend on queue footer + console; `FetchAgentPlatformCredentials` effect | `web/elm/src/AgentTickets/AgentTicket.elm:393-443`, `web/elm/src/Concourse/AgentTicket.elm:164-213` | **Pre-satisfies plan-12 Task 14's branch display/link** — D2's scope shrinks accordingly. Bundle-regen recipe is now this commit (elm.js + elm.min.js, elm-test 3090 green) |
| Elm data spine: `Concourse/AgentTicket.elm` (incl. `branch` field in the decoder), endpoints `AgentTicketsList/AgentTicket/AgentTicketState/AgentTicketDispatch/AgentTicketTask/AgentTicketMetrics`, effects `TransitionAgentTicket`/`FetchAgentTicketMetrics`/`FetchBuildAgentReviews` | `web/elm/src/Api/Endpoints.elm`, `web/elm/src/Message/Effects.elm` (search the symbol names — E+F shifted line anchors) | commit d6da5718d7 |
| Review-evidence stopgap: page fetches `ListAgentRunMetrics` (`atc/routes.go:310`), takes max buildId, then `FetchBuildAgentReviews` | `AgentTicket.elm:143-151` | KEPT in this plan (Task 9 deferred) |
| Harvest records branch on ticket at push: `exec.HarvestStep` → `Transition(running→needs_review, Branch)` on exit 0+push; exit 1 → needs_review empty branch; exit 2/process death → errored; guarded by `RunBelongsToPipeline` + `TicketBelongsToRun` | `atc/exec/harvest_step.go:279-338` | Branch name `agent/ticket-<id>` from `agent/dispatch/render.go` |
| harvest-runner v0.5: emits one-line `results.json {status, metadata:{pushed_branch, head_sha, detail, gates[]}}` to stdout; pushes head-sha `--force-with-lease`; GIT_ASKPASS creds from mounted `agent-harvest-git-<slug>` secret; full-scope build/test/lint gate engine | `agent/harvest/runner.go`, `gates.go`, `policy.go` | The landed typed `ResultsMetadata` (runner.go:20-25) has **NO base_sha**. It is REPLACED (not extended) by judge-evidence Task 9's `agent/schema.Results` (metadata is a map, carrying `base_sha`); B4 consumes that map — see B4's cross-plan header |
| Git-cred machinery: `GitCredSecretName` slugging, token+optional-username keys, GIT_ASKPASS temp helper (token never on argv), `SecretMounts` main-container-only | `agent/harvest/policy.go:64-81`, `runner.go:159-181`, commit 96e96a8461 | Contracts §8.3: bot identity `concourse-agent[bot] <agent@concourse.local>`; PAT push-scoped to `agent/*` |
| Contract groundwork: §1.11 `agent_outcomes` DDL at 1773106090 with `concluded` amendments applied in place; §2.5 `Outcome` type; §4.2 rows `SetAgentTicketDisposition` (PUT, authorized member) + `GetAgentTicketOutcome` (GET, authorized viewer) | `00-shared-contracts.md:407-443, :707-742, :1332-1333` | §1.11.1 was never inserted — Task A1 |
| Auth seams: `CheckAgentAuthorizationHandler`, `AgentPrincipalOrMainTeamHandler`, `UserNameFunc` injection | `atc/api/auth/check_agent_authorization_handler.go:17`, `agent/api/tickets/handler.go:18` | |
| Component anchors: `ComponentAgentPlatformCredentialSyncer` / `ComponentAgentRunSecretReaper` consts; interval-polled component wiring | `atc/component.go:25-26`, `atc/atccmd/command.go:1355-1376` | |
| C1 touchpoint anchors (current tree): ONE plain team-less agent `CheckAgentAuthorizationHandler` case block at `:205-224` holds BOTH viewer routes (`atc.ListAgentRunMetrics`) AND member routes (`atc.UpdateAgentTicket`, `atc.DispatchAgentTicket`) with no principal path; a SEPARATE `AgentPrincipalOrMainTeamHandler` combined-tier block at `:228-235` holds `atc.TransitionAgentTicket`. **The wrappa case block does NOT encode viewer-vs-member** — that split is resolved downstream by `roles.go` `DefaultRoles` + `accessor.hasRequiredRole`. | `atc/wrappa/api_auth_wrappa.go:205-224,228-235`; `atc/wrappa/reject_archived_wrappa.go:137,143`; `atc/auditor/auditor.go:175,181`; `atc/api/accessor/roles.go:113,134` | |
| Scorecards' reciprocal sign-off on the §1.11.1 join contract (LEFT JOIN on unique ticket_id; delta unit = LINES; concluded excluded from merge-rate denominators; dark-until-filled) | `13-scorecards.md:34,37,77` | Deviating from the frozen delta requires scorecards re-sign-off |
| Dual migration constants both = **1773106066** (moved from 1773106064 by workflow-source slice-a, commit 550a8dbd7a) | `atc/db/migration/legacy_upgrade_test.go:37`, `docs/migration/migrate-preflight.sh:38` | Migration `1773106066_add_agent_workflow_source_manifest` is landed on disk |

Things the original plan cites that DO NOT exist (do not go looking for them):
- `agent/harvest/workspace.go` (`HeadSHA`/`BaseSHA`/`Diff`/`ChangedPaths`/`BuildManifest`) — did not exist at HEAD; harvest landed as `runner.go`/`gates.go`/`policy.go` with an inline git closure. The real-git-fixture recipe is `agent/harvest/runner_test.go` + `gates_test.go`. **Update:** the sibling judge-evidence remainder (Task 5) CREATES `agent/harvest/workspace.go` with exactly these helpers; when it lands first (recommended), B4 reuses `harvest.BaseSHA` rather than resolving base_sha itself (see B4's cross-plan header, ONE resolver total).
- `agent_run_metrics` rows from harvest — `harvest_step.go` has zero metrics references; runner results go only to step stdout.
- `agent_reviews.ticket_id` / `reviews.Store.ListByTicket` / migration 1773106080 — plan-09 Tasks 2-4/8/9, not landed (`agent/api/reviews/types.go` has no TicketID; no ListByTicket anywhere but metrics).
- `atc/exec/harvest_step_test.go` — the exec-level harvest step has no test file yet (runner tests live in `agent/harvest/`).
- `GetAgentCostRollup`-driven cost display on the ticket page — the shipped page sums per-run metrics instead. Drop that wiring from Task 13; keep the metrics-based sum.

---

## 2. Scope

**IN (this plan):**
1. Contract addendum §1.11.1 (amended for the sha-provenance and writer-reconciliation decisions below) — Task A1.
2. Migration `agent_outcomes` + dual-constant bump — Task A2.
3. `agent/api/outcomes` (types/taxonomy/MemoryStore), `atc/db` factory, HTTP handlers for outcome + disposition — Tasks A3, A4, B1.
4. Route wiring for `SetAgentTicketDisposition`, `GetAgentTicketOutcome` (Task B2) and `GetAgentTicketDiff` (Task C5) — full C1 six-touchpoint discipline each time.
5. go-concourse client + `fly agent tickets dispose` + **re-pointing `fly agent tickets close`'s terminal hop** through the disposition route — Task B3.
6. **NEW:** harvest sha seeding — runner emits `base_sha`, `exec.HarvestStep` tees stdout for `results.json` and seeds the outcome row via `outcomes.Store.Ensure` — Task B4.
7. `agent/gitcheck` (mirror cache, merge detector, windowed diff) — Tasks C1, C2.
8. `agent/outcomewatcher` RunnableComponent (amended: no metrics dependency; seeding backstop that never clobbers harvest-seeded shas; terminal sweep for bypassed dispositions) + component/flag wiring — Tasks C3, C5.
9. `GetAgentTicketDiff` handler — Task C4.
10. Elm remainder: outcome/diff data layer, outcome badge + paginated in-app diff on the ticket page, disposition form replacing the bare terminal buttons for sent_back/abandoned/concluded, bundle regen — Tasks D1-D4. (Branch display/link is ALREADY SHIPPED by wave E — see Landed state.)
11. Live verification: mirror-fetch probe + theborg watcher smoke — Tasks E1, E2.

**OUT (deferred, with a home):**
- **`GetAgentTicketReviews` route + handler (plan-12 Task 9).** Blocked on plan-09's `agent_reviews`/`agent_feedback` ticket-linkage migration + `reviews.Store.ListByTicket` (none landed). The shipped build-id stopgap on the ticket page works. Defers to the plan-09/judge-evidence remainder item. The §4.2 row stays reserved; Task A1 marks it deferred.
- **Judge-score display on the ticket page** (part of plan-12 Task 14). The judge does not exist; harvest v0.5 refuses judge configs. Defers with the judge-evidence item. Do not plan or stub that UI here.
- **`GetAgentCostRollup` wiring in Elm** (part of plan-12 Task 13). Superseded by the shipped metrics-based cost sum.
- Auto-merge (never), Jira sync (phase 2), finding analytics (process-intel-experiments), scorecard rollups (plan 13).

**Decisions this plan takes (owner MUST confirm before Task A1 merges — see §7):**
- **D-1 (delta definition):** proceed with the frozen LINES definition (numstat of non-`concourse-agent[bot]` first-parent commits in `pushed_sha..tip-at-merge`; `merged_with_fixes ⇔ human_commit_count > 0`; patch-id squash fallback N=200; edited-squash/rebase stays `open`; `closed_unmerged` only via disposition). Already reciprocally signed off by plan 13 — confirming, not re-deciding.
- **D-2 (sha provenance):** harvest seeds the outcome row at push time with authoritative shas (Task B4); watcher keeps the branch-head fallback as backstop only. Rejected alternatives: (a) fallback-only — permanently weaker delta baseline and a dark diff API; (b) pulling plan-09's metrics upsert + migration 1773106080 forward — drags a second migration and half of plan-09 into this item.
- **D-3 (writer reconciliation):** `fly agent tickets close` (terminal hop) and the Elm terminal buttons for `sent_back`/`abandoned`/`concluded` migrate to `SetAgentTicketDisposition`. Raw `TransitionAgentTicket` keeps permitting `needs_review → merged | merged_with_fixes` (manual override; the watcher independently detects the real git merge and fills the row) and — for API compatibility (C3, ADD-never-REPLACE) — keeps accepting the other terminal edges too; the watcher's terminal sweep (Task C3) closes any outcome row orphaned by a bypassing writer.

---

## 3. Slices and verification stories

Each slice is independently shippable in order A → B → C → D → E. Per-slice verification is split three ways:
- **gate-verifiable** — pure-Go `go build ./... && go test ./... && go vet ./...` the harvest gates run in-pod (no postgres, no cluster, no Elm).
- **local-verify** — postgres-backed; a human runs it locally BEFORE merge.
- **live-verify** — theborg cluster smoke.

### Slice A — contracts + schema + domain (Tasks A1-A4)
Ships: the frozen contract, the table, the domain package, the DB factory. No user-visible behavior.
- gate-verifiable: `go test ./agent/api/outcomes/` (plain testing, MemoryStore + taxonomy).
- local-verify: `pg_isready` then `ginkgo ./atc/db/migration/` (migration walk + `jetbridgeHeadMigration` consistency) and `ginkgo --focus="AgentOutcomes" ./atc/db/` (factory suite). Confirm `docs/migration/migrate-preflight.sh` constant matches.
- live-verify: none.

### Slice B — disposition write path + harvest sha seeding (Tasks B1-B4)
Ships: outcome/disposition API, fly UX reconciliation, harvest-side row seeding. Usable without the watcher (dispositions work; rows carry authoritative shas).
- gate-verifiable: `go test ./agent/api/outcomes/ ./agent/harvest/ ./atc/exec/ ./atc/wrappa/... ./atc/auditor/... ./atc/api/... ./go-concourse/... ./fly/...` — handler tests (plain testing, MemoryStores), runner base_sha tests (real git in tempdir — needs the `git` binary in the agent image, see §7 O-2), stdout-observer unit tests, wrappa/auditor panic-switch coverage.
- local-verify: `make test-unit` (full atc suite against postgres); `make test-fly-integration` if the fly mock ATC gains the new routes.
- live-verify: rides Slice E (a real harvest push seeds a row with both shas — check via `fly curl` or SQL).

### Slice C — git machinery + watcher + diff (Tasks C1-C5)
Ships: mirror cache, merge detection, human-touch delta, diff API, the component (off-by-default behind `--agent-outcome-git-dir`).
- gate-verifiable: `go test ./agent/gitcheck/ ./agent/outcomewatcher/` (Ginkgo suites shelling out to real git in hermetic tempdirs — no network; same git-binary caveat) + the Task C5 route/wiring suites as in Slice B.
- local-verify: `make test-unit`; a manual `concourse web` boot with `--agent-outcome-git-dir=/tmp/mirrors` against a local repo to watch one seed+detect tick (optional but cheap).
- live-verify: Task E2 (theborg deploy with the flag set; real merge observed within one 5m tick; §8.4 webhook fires via the transition function).

### Slice D — Elm PR-view remainder (Tasks D1-D4)
Ships: outcome badge, paginated in-app diff, disposition form, bundle.
- gate-verifiable: none (no Elm toolchain in gates — do NOT dispatch this slice to the loop).
- local-verify: `elm-test` in `web/elm`; `elm make` clean; bundle regeneration (`web/public/elm.js` + `elm.min.js`, cf. commit 0866d89fc9) and a local web boot clicking through the ticket page (spine-serialization rule: no parallel Elm sessions).
- live-verify: rides Slice E on concourse.home.

### Slice E — live verification (Tasks E1-E2)
- gate-verifiable: none.
- local-verify: E1 runs from this machine (outbound git-over-https only).
- live-verify: E2 is the end-to-end proof on theborg.

---

## 4. Tasks

### Task A1 (Slice A): contract addendum §1.11.1 — amended freeze

**Files:**
- Modify: `docs/superpowers/plans/agentic-platform/00-shared-contracts.md`

**Execute plan 12 Task 1 as written (lines 59-118 of `12-delivery-outcomes.md`), with these amendments:**

Delta notes (what changed since Task 1 was written):
- The 2026-07-09 `concluded` amendments the addendum cross-references are ALREADY applied in place in §1.7 (line 267), §1.11 (:407-443), §2.5 (:707-742) — the anchors are current; insert §1.11.1 immediately after §1.11's closing paragraph (line 443), before `### 1.12` (line 445), exactly as Task 1 says.
- The §11 amendment log has grown 8+ entries since Task 1 was written. **Append the three Task-1 log entries plus the new entry below at the TRUE END of the log — after the `2026-07-18 (harvest v0.5 …)` entry** (the file's last entry; its date is a pod UTC clock artifact, it is the newest), NOT "after the harvest-step planning entry". **Cross-plan: all five remainder plans append to this same §11 tail — re-read it at commit time and append after whatever entry is now LAST (a sibling may have appended since), never after a pinned line; treat §11 as single-writer per merge window and land the docs commits serially.**
- Date the addendum heading `2026-07-17` (re-scope date), not 2026-07-08.

Amendment 1 — **REPLACE** the addendum's "Outcome-row creation" paragraph (plan 12 line 82) with:

````markdown
**Outcome-row creation [AMENDED 2026-07-17 re-scope — sha provenance]:** rows are created by **harvest, at push time** (primary): on the exit-0 + push path, `exec.HarvestStep` tees the runner's stdout, parses the single-line `results.json`, and calls `outcomes.Store.Ensure` with the runner's authoritative `pushed_sha` (= `metadata.head_sha`) and `base_sha` (= `metadata.base_sha`, a NEW additive §2.8.1 metadata field: the merge-base of the committed workspace HEAD and `origin/<target_branch>`, best-effort, `''` when unresolvable). Ordering is load-bearing: transition first (`running → needs_review` through the single writer), outcome bookkeeping second; a seeding failure is logged, never fatal. The watcher's `seedRows` is the **backstop only**: each tick it scans tickets in `needs_review` with a non-empty `branch`; when NO outcome row exists (pre-seeding tickets, seeding failure) it creates one with `pushed_sha` = remote branch head at first sync — weaker, because pre-existing human commits then dilute the delta baseline — and `base_sha = ''` (the diff API returns 404 until a later harvest push seeds it). When a row EXISTS and is open, `seedRows` does NOT call `Ensure` — the backstop must never clobber harvest-seeded shas with fallback values. `Ensure`'s own semantics are unchanged: an open row refreshes `branch`/`pushed_sha`/`base_sha` in place (harvest re-push), and a row with `merge_state = 'closed_unmerged' AND disposition = 'sent_back'` is **re-armed** to `open` with fresh shas and cleared disposition fields (F6) — the re-arm now normally fires from harvest's push-time `Ensure` (authoritative shas), with `seedRows` re-arming as fallback when it finds a re-armable row for a `needs_review` ticket. Truly terminal rows — `merged`/`merged_with_fixes`, `closed_unmerged` via `abandoned`, or `concluded` — are never refreshed or re-armed. A `concluded` disposition closes the row as `merge_state = 'concluded'`: the watcher skips merge-detection entirely for concluded tickets, permanently.
````

Amendment 2 — **ADD** two paragraphs to the addendum, after "Merge-detection heuristics v1" (plan 12 line 87), before "Concluded is not a failure":

````markdown
**Writer reconciliation [DECIDED 2026-07-17 re-scope]:** `SetAgentTicketDisposition` is the canonical writer for the terminal dispositions `sent_back`/`abandoned`/`concluded` (taxonomy + notes + `disposed_by`, ticket transition FIRST via §2.1, outcome row second). The two shipped bare-transition writers migrate to it: `fly agent tickets close`'s terminal hop and the Elm ticket-page terminal buttons. Raw `TransitionAgentTicket` continues to accept every §1.7 edge (ADD-never-REPLACE; scripts and `fly curl` depend on it), including `needs_review → merged | merged_with_fixes` as a manual override — the watcher independently detects the real git merge and fills the row's `merged_sha`/delta, so a manual "Merge" click loses nothing. **Terminal sweep:** to keep bypassing writers from stranding rows, each watcher tick closes any OPEN outcome row whose ticket is already terminal: ticket `abandoned` → row `closed_unmerged` (disposition fields stay empty — the sweep never invents taxonomy), ticket `concluded` → row `concluded`; tickets in `merged`/`merged_with_fixes` are left to organic merge detection (the row is closed with a full delta when the mirror shows the merge).

**Reviews route deferral [2026-07-17 re-scope]:** `GetAgentTicketReviews` (§4.2 additive row) is RESERVED but does not land in this effort — it requires plan-09's `agent_reviews`/`agent_feedback` ticket-linkage migration and `reviews.Store.ListByTicket`, none of which exist. The ticket page's shipped build-id stopgap (newest run's build via `ListAgentRunMetrics` → `GetBuildAgentReviews`) remains the evidence path until the plan-09 remainder lands.
````

Amendment 3 — in the addendum's route-additions table and the §4.2 rows appended after `GetAgentTicketOutcome`, annotate the `GetAgentTicketReviews` row: `reserved — lands with the plan-09 remainder (ticket-linkage migration)`. Wire only three routes in this effort.

Amendment 4 — **APPEND** one more §11 log entry after the three from plan-12 Task 1:

````markdown
- 2026-07-17 (delivery-outcomes re-scope, post-harvest-v0.5): §1.11.1 amended before first code lands. (a) Sha provenance: harvest seeds outcome rows at push time — §2.8.1 results metadata gains additive `base_sha` (merge-base of workspace HEAD and origin/<target_branch>, best-effort); `exec.HarvestStep` tees runner stdout for results.json and calls `outcomes.Store.Ensure` (transition-first ordering; seeding failures non-fatal); the watcher's metrics-row lookup from the original plan is DEAD (harvest v0/v0.5 never wrote agent_run_metrics) and `seedRows` becomes a create-if-absent + re-arm backstop that never clobbers harvest-seeded shas. (b) Writer reconciliation: fly close's terminal hop and the Elm terminal buttons migrate to SetAgentTicketDisposition for sent_back/abandoned/concluded; raw TransitionAgentTicket keeps all §1.7 edges; a watcher terminal sweep closes rows orphaned by bypassing writers (abandoned → closed_unmerged with empty disposition fields; concluded → concluded; merged left to organic detection). (c) GetAgentTicketReviews stays reserved pending plan-09's ticket-linkage migration; the build-id evidence stopgap on the ticket page remains. Affects: scorecards, process-intel-experiments, harvest-step (plan-09 remainder).
````

- [ ] Apply plan 12 Task 1 with amendments 1-4.
- [ ] Verify: `grep -n "### 1.11.1" docs/superpowers/plans/agentic-platform/00-shared-contracts.md` prints exactly one hit between §1.11 and §1.12; `grep -c "GetAgentTicketDiff" …/00-shared-contracts.md` ≥ 2 (table + addendum); the §11 log's last entries are the four appended ones.
- [ ] STOP for owner confirmation of D-1/D-2/D-3 (§7) before committing. Commit: `docs(agentic): delivery-outcomes contract addendum §1.11.1 — re-scoped sha provenance, writer reconciliation, reviews deferral`

### Task A2 (Slice A): migration `agent_outcomes` + dual-constant bump

**Execute plan 12 Task 2 as written (lines 122-179), with these deltas:**
- Plan 12 says "by wave 4 [the head constant] reads 1773106080 from harvest-step" — **FALSE.** Harvest v0/v0.5 landed with NO migration. Both constants read **1773106066** today (`atc/db/migration/legacy_upgrade_test.go:37`, `docs/migration/migrate-preflight.sh:38` — workflow-source slice-a moved them past the scout-era 1773106064).
- Migration number: **re-verify the head at land time** and follow §5 (Migration allocations) below — the expected number is `1773106090`, but the landing-order rule there is normative. Bump BOTH constants to the landed number in the same commit (C2).
- The DDL must include the §1.11.1 additive deltas exactly as Task 1/A1 froze them: `base_sha TEXT NOT NULL DEFAULT ''` and the `agent_outcomes_open` partial index, plus the `concluded` CHECK values already amended in place in §1.11.
- local-verify (not gate-verifiable): `ginkgo ./atc/db/migration/` with postgres up.

### Task A3 (Slice A): `agent/api/outcomes` — types, taxonomy, MemoryStore

**Execute plan 12 Task 3 as written (lines 183-666), with these deltas:**
- The package is confirmed greenfield (`agent/api/outcomes` does not exist; `agent/api/` currently holds costs, feedback, metrics, principals, reviews, tickets, workflows).
- Add one sentence to `Store.Ensure`'s doc comment: "Called by BOTH exec.HarvestStep (push-time seeding, authoritative shas) and the outcome watcher's seedRows backstop (fallback shas, create-if-absent + re-arm only — see §1.11.1)." Semantics are already exactly what Task 3 specifies (open-row refresh, F6 sent_back re-arm, terminal rows untouched) — no behavior change.
- **ADD one method** to the `Store` interface, `MemoryStore`, and the counterfeiter fake — needed by Task C3's terminal sweep:

```go
// Close closes an OPEN row to the given terminal merge_state without
// touching disposition fields — the watcher's terminal sweep for tickets
// closed by a bypassing raw-transition writer (§1.11.1 writer
// reconciliation). No-op (nil) when the row is absent or not open.
// state must be ClosedUnmerged or MergeConcluded.
Close(ticketID int, state MergeState) error
```

MemoryStore implementation:

```go
func (s *MemoryStore) Close(ticketID int, state MergeState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state != ClosedUnmerged && state != MergeConcluded {
		return fmt.Errorf("close: invalid terminal state %q", state)
	}
	o, ok := s.rows[ticketID]
	if !ok || o.MergeState != MergeOpen {
		return nil
	}
	o.MergeState = state
	s.touch(o)
	return nil
}
```

(Adapt field/mutex/helper names to Task 3's MemoryStore as written — e.g. if Task 3 stores values not pointers, reassign into the map; if it has no `touch` helper, set `UpdatedAt` inline.)

Additional test (append to `types_test.go`):

```go
func TestMemoryStoreCloseSweepsOnlyOpenRows(t *testing.T) {
	s := outcomes.NewMemoryStore()
	_ = s.Ensure(&outcomes.Outcome{TicketID: 4, Repo: "r", Branch: "agent/ticket-4", PushedSha: "p", BaseSha: "b"})

	if err := s.Close(4, outcomes.ClosedUnmerged); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Get(4)
	if got.MergeState != outcomes.ClosedUnmerged || got.Disposition != "" {
		t.Fatalf("close must set the state and never invent a disposition: %+v", got)
	}

	// closed rows are terminal for Close too
	if err := s.Close(4, outcomes.MergeConcluded); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.Get(4); got.MergeState != outcomes.ClosedUnmerged {
		t.Fatalf("close must not rewrite a closed row: %+v", got)
	}

	// absent row: benign no-op
	if err := s.Close(999, outcomes.ClosedUnmerged); err != nil {
		t.Fatal(err)
	}

	// invalid target state: error
	if err := s.Close(4, outcomes.Merged); err == nil {
		t.Fatal("Close(Merged) must be rejected — merges go through RecordMerge")
	}
}
```

- [ ] Run `go test ./agent/api/outcomes/` — expect the Task 3 suite plus the Close spec green.

### Task A4 (Slice A): `atc/db` AgentOutcomesFactory

**Execute plan 12 Task 4 as written (lines 670-1026), with these deltas:**
- Greenfield confirmed (`atc/db/agent_outcomes_factory.go` and its dbfakes do not exist). Recipe: `atc/db/agent_reviews_factory.go`.
- Implement the new `Close` method (Task A3) in SQL:

```sql
UPDATE agent_outcomes
SET merge_state = $2, updated_at = now()
WHERE ticket_id = $1 AND merge_state = 'open'
```

with the Go wrapper validating `state ∈ {closed_unmerged, concluded}` before executing, returning nil on zero rows affected. Add a spec: "Close sweeps only open rows and never touches disposition columns" (assert `disposition`, `disposition_reason`, `disposition_notes`, `disposed_by` all still `''` after Close; assert a `merged` row is untouched).
- C3 (ADD-never-REPLACE) applies with full force to `Ensure`'s `ON CONFLICT … DO UPDATE SET` clause — the upsert spec must assert EVERY SET column with differing values (Task 4's spec already does; keep it intact when adding anything).
- Postgres-backed — **NOT gate-verifiable.** local-verify: `ginkgo --focus="AgentOutcomes" ./atc/db/` then full `make test-unit` before merge.
- Regenerate the counterfeiter fake (`atc/db/dbfakes`) in the same commit.

### Task B1 (Slice B): outcomes HTTP handler — GetAgentTicketOutcome + SetAgentTicketDisposition

**Execute plan 12 Task 5 as written (lines 1027-1384), with these deltas:**
- All consumed seams are landed exactly as the task assumes: `tickets.Store.Transition` signature (`agent/api/tickets/types.go`), `TransitionMeta` with NO `By` field, `UserNameFunc` (recipe: `agent/api/tickets/handler.go:18`), `dispositionToState` mapping `concluded → tickets.StateConcluded`.
- The load-bearing ordering is unchanged and MUST survive: the disposition handler transitions the ticket FIRST (through the single writer, from-guard `needs_review`), writes the outcome row second; a 409 from the transition aborts before any outcome write.
- Plain `testing` + MemoryStores → fully gate-verifiable: `go test ./agent/api/outcomes/`.

### Task B2 (Slice B): route wiring — SetAgentTicketDisposition + GetAgentTicketOutcome (C1 × 2)

This is the first half of plan-12 Task 11's route work, split out so Slice B ships alone. ALL SIX C1 touchpoints in ONE commit. The two panicking switches (`reject_archived_wrappa`, `auditor.ValidateAction`) are the enforcement — the auditor one is NOT covered by the api/wrappa suites; test it explicitly.

**Files:**
- Modify: `atc/routes.go` (constants near :145-153; route table after `DispatchAgentTicket` at :320)
- Modify: `atc/wrappa/api_auth_wrappa.go` (the plain team-less agent `CheckAgentAuthorizationHandler` case block at :205-224 — it already holds BOTH `atc.ListAgentRunMetrics` (viewer) AND `atc.UpdateAgentTicket`/`atc.DispatchAgentTicket` (member); do NOT confuse it with the SEPARATE `AgentPrincipalOrMainTeamHandler` combined-tier block at :228-235 containing `atc.TransitionAgentTicket`)
- Modify: `atc/wrappa/reject_archived_wrappa.go` (agent bucket at :137-143)
- Modify: `atc/auditor/auditor.go` (agent arm at :175-181)
- Modify: `atc/api/accessor/roles.go` (agent rows at :113/:134)
- Modify: `atc/api/handler.go` + its two `NewHandler` call sites: `atc/api/api_suite_test.go`, `atc/atccmd/command.go` (arg threading near :2390, after `cmd.AgentReviewPublishToken`)
- Modify: `docs/superpowers/plans/agentic-platform/agent-route-scopes.md` (one row per route — mandatory per that file's header)
- Test: `atc/wrappa/api_auth_wrappa_test.go` (incl. an explicit spec pinning the tier: `SetAgentTicketDisposition` must REJECT a bare `tickets:write` agent-principal token — 401/403 — since it carries no principal path; contrast with `TransitionAgentTicket`, which accepts one), `atc/auditor/auditor_test.go`, `atc/api/…` route-presence specs (follow the ticket-core route specs)

**Steps:**

- [ ] `atc/routes.go` — add alongside the ticket constants and table entries:

```go
	SetAgentTicketDisposition = "SetAgentTicketDisposition"
	GetAgentTicketOutcome     = "GetAgentTicketOutcome"
```

```go
	{Path: "/api/v1/agent/tickets/:ticket_id/disposition", Method: "PUT", Name: SetAgentTicketDisposition},
	{Path: "/api/v1/agent/tickets/:ticket_id/outcome", Method: "GET", Name: GetAgentTicketOutcome},
```

- [ ] `atc/wrappa/api_auth_wrappa.go` — add BOTH `atc.GetAgentTicketOutcome` AND `atc.SetAgentTicketDisposition` to the SAME plain team-less case block at :205-224 (the one wrapping `atc.ListAgentRunMetrics` and `atc.UpdateAgentTicket` in `auth.CheckAgentAuthorizationHandler(handler, rejector)`). The wrappa case block does NOT encode viewer-vs-member — that split is resolved downstream by `roles.go` `DefaultRoles` (`GetAgentTicketOutcome` → viewer, `SetAgentTicketDisposition` → member), so both new routes sit in this one plain block exactly like `GetAgentTicketOutcome`'s sibling viewer routes and `UpdateAgentTicket`'s sibling member routes already do. **Do NOT** add `atc.SetAgentTicketDisposition` to the `AgentPrincipalOrMainTeamHandler` combined-tier block at :228-235 (the one containing `atc.TransitionAgentTicket`): the frozen §4.2 contract row (`00-shared-contracts.md:1332`) grants the disposition route plain `authorized member` with NO principal path — unlike `TransitionAgentTicket`'s `also principal(tickets:write)` at :1323 — and `agent-route-scopes.md:65` lists its scope column as `—`. Wiring it through the principal handler would let any `tickets:write` agent principal `PUT .../disposition` and dispose its own ticket (`sent_back`/`abandoned`/`concluded`) with no human in the loop, bypassing the review gate dispositions exist to enforce (D-3). The panic-switch suites would NOT catch this — they only assert a route resolves to SOME handler, not the semantically-correct one — so the tier-pinning wrappa spec named in the Test list is the guard. Run the wrappa suite FIRST to see the new routes fail, then add.
- [ ] `atc/wrappa/reject_archived_wrappa.go` — add both to the same bucket as `atc.TransitionAgentTicket` (agent routes carry no pipeline context; archived pipelines cannot affect them).
- [ ] `atc/auditor/auditor.go` — add both names to the `ValidateAction` switch arm containing `atc.TransitionAgentTicket`. Add an explicit auditor spec asserting both route names validate (and that an unknown name still panics — pin the enforcement).
- [ ] `atc/api/accessor/roles.go` — `DefaultRoles`: `"SetAgentTicketDisposition": "member"`, `"GetAgentTicketOutcome": "viewer"`.
- [ ] `atc/api/handler.go` — construct the Task B1 handler (`outcomes.NewHandler(outcomesStore, ticketsStore, userName)` per plan-12 Task 5's constructor) and map both route names; thread the `outcomes.Store` argument through `NewHandler`'s signature and BOTH call sites (`api_suite_test.go` with the fake, `command.go` with `db.NewAgentOutcomesFactory(dbConn)`).
- [ ] `agent-route-scopes.md` — two rows, tier "authorized viewer/member (main)" per §4.2.
- [ ] Run: `go test ./atc/wrappa/... ./atc/auditor/... && ginkgo ./atc/api/` — the panic switches are the proof; expect green.
- [ ] Commit: `feat(agentic): wire SetAgentTicketDisposition + GetAgentTicketOutcome (C1 six-touchpoint)`

### Task B3 (Slice B): go-concourse client + fly disposition UX + close re-point

Materially amended from plan-12 Task 12 (lines 3235-3421): `fly agent tickets close` ALREADY EXISTS with bare transitions. Shipping `dispose` alongside an unchanged `close` would leave two divergent close paths, one bypassing outcome rows — re-point `close` in the same task.

**Files:**
- Modify: `go-concourse/concourse/agent_tickets.go` (+ `client.go` interface; + counterfeiter fake regen)
- Modify: `fly/commands/agent_tickets.go` (`AgentTicketsCloseCommand` at :350-405; command registration)
- Test: `go-concourse/concourse/agent_tickets_test.go`, `fly/integration` (mock ATC gains the disposition route)

**Steps:**

- [ ] Client method (plain wrapper, follows `TransitionAgentTicket` at `agent_tickets.go:58`):

```go
func (client *client) SetAgentTicketDisposition(id int, req outcomes.DispositionRequest) (outcomes.Outcome, error) {
	var outcome outcomes.Outcome
	buf := bytes.Buffer{}
	if err := json.NewEncoder(&buf).Encode(req); err != nil {
		return outcome, err
	}
	err := client.connection.Send(internal.Request{
		RequestName: atc.SetAgentTicketDisposition,
		Params:      rata.Params{"ticket_id": strconv.Itoa(id)},
		Header:      http.Header{"Content-Type": []string{"application/json"}},
		Body:        &buf,
	}, &internal.Response{Result: &outcome})
	return outcome, err
}
```

(`outcomes.DispositionRequest` is the request body type plan-12 Task 5 defines; adapt the name if Task 5's landed shape differs. Add to the `Client` interface + regen the fake.)

- [ ] Failing client test first (recipe: the existing `TransitionAgentTicket` test in `agent_tickets_test.go`): ghttp server expecting `PUT /api/v1/agent/tickets/12/disposition` with the JSON body, returning an Outcome; assert round-trip.
- [ ] New `fly agent tickets dispose` command:

```go
type AgentTicketsDisposeCommand struct {
	ID          int    `long:"id" required:"true" description:"Ticket id"`
	Disposition string `long:"disposition" required:"true" choice:"sent_back" choice:"abandoned" choice:"concluded" description:"Terminal disposition"`
	Reason      string `long:"reason" choice:"wrong_approach" choice:"incomplete" choice:"defective" choice:"superseded" choice:"not_needed" choice:"style" choice:"research_complete" choice:"other" description:"Reason taxonomy (§1.11)"`
	Notes       string `long:"notes" description:"Free-text notes"`
}

func (command *AgentTicketsDisposeCommand) Execute([]string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}
	outcome, err := target.Client().SetAgentTicketDisposition(command.ID, outcomes.DispositionRequest{
		Disposition: command.Disposition,
		Reason:      command.Reason,
		Notes:       command.Notes,
	})
	if err != nil {
		return err
	}
	fmt.Printf("ticket #%d disposed: %s (%s)\n", command.ID, outcome.Disposition, outcome.MergeState)
	return nil
}
```

Register it beside the existing tickets subcommands (follow the `close` registration).

- [ ] Re-point `AgentTicketsCloseCommand.Execute`'s terminal hop (`agent_tickets.go:397-403`): when `command.Disposition` is `sent_back`/`abandoned`/`concluded`, replace the bare `TransitionAgentTicket(needs_review → X)` call with `SetAgentTicketDisposition` (reason defaults: empty is legal only if the B1 handler allows it — pass `Reason: "other"` default? NO: keep the handler's validation authoritative; add `--reason`/`--notes` flags to close with the same choice lists, defaulting `research_complete` when disposition is the default `concluded`, `other` otherwise). When `command.Disposition` is `merged`/`merged_with_fixes`, keep the raw transition (D-3: manual override stays legal). The `running → needs_review` first hop stays a raw transition (it is not a disposition).
- [ ] fly integration: mock ATC (`fly/integration`) gains the disposition route; specs: `dispose` happy path per disposition, taxonomy rejection surfaces the server 400, `close --disposition sent_back --reason incomplete` hits the disposition route (assert by mock route match), `close --disposition merged` still hits the transition route.
- [ ] Run: `go test ./go-concourse/... && make test-fly-integration` — expect green (fly integration is NOT postgres-backed; it is gate-compatible in toolchain terms but the harvest gates only run `go test ./...`, which includes these packages — fine).
- [ ] Commit: `feat(fly): agent tickets dispose + close re-pointed through the disposition route`

### Task B4 (Slice B) — NEW: harvest sha seeding (runner base_sha + exec stdout tee + outcome Ensure)

The sha-provenance fix (D-2). Three parts, one commit each or one combined commit.

> **⚠ CROSS-PLAN COLLISION — the harvest results shape and the exec harvest step are CO-OWNED with the judge-evidence remainder (`remainders/2026-07-17-judge-evidence.md`).** Both plans edit `agent/harvest/runner.go`'s results representation, both CREATE `atc/exec/harvest_step_test.go` (absent today), both modify `atc/exec/harvest_step.go`, both extract a shared verifier helper out of `transitionTicket`, and both append to `atc/engine/step_factory.go` `harvestOpts`. They are INCOMPATIBLE as originally drafted (this plan used a TYPED `harvest.Results`/`ResultsMetadata` with struct-field access; judge-evidence Task 9 DELETES those types and converges on `agent/schema.Results` whose `Metadata` is a `map[string]interface{}`). **Reconciliation (authoritative):**
> 1. **Canonical shape = `agent/schema.Results`, `head_sha`/`base_sha` in the metadata MAP.** judge-evidence Task 9 owns the runner's `Run` rewrite and lands FIRST. After it lands, runner stdout AND flight `results.json` are `schema.Results`; there is NO typed `ResultsMetadata` and NO `ResultsMetadata.BaseSHA` field. This plan reads `res.Metadata["head_sha"]` / `res.Metadata["base_sha"]` as map keys (see the reframed code below), never struct fields.
> 2. **Runner base_sha is judge-evidence's, not this plan's.** judge-evidence Task 9 already resolves base via `harvest.BaseSHA(...)` and writes `base_sha` into the `schema.Results` metadata map. So this plan's original "runner emits base_sha" step is SUPERSEDED when judge-evidence lands first (the recommended order) — SKIP the `ResultsMetadata`/`resolveBaseSHA` runner edits and the two runner tests below; the shas are already in the metadata map. Land the minimal base_sha-in-metadata-map here ONLY if this plan somehow lands before judge-evidence Task 9 (still into the `schema.Results` map form, never a typed field). ONE base_sha resolver total (`harvest.BaseSHA`), not two.
> 3. **Merge into existing exec files; reuse the single verifier helper.** This plan lands second on `atc/exec/harvest_step.go` and `atc/exec/harvest_step_test.go` — ADD to the file judge-evidence created, do not recreate it. Reuse judge-evidence Task 12's `verifiedIDs(logger)` helper for the seeding path's re-verification instead of adding a second `verifiedTicketRun` helper (if this plan lands first, name the helper `verifiedIDs` so judge-evidence consumes it). `harvestOpts` in `step_factory.go` gains this plan's `WithHarvestOutcomesStore` alongside judge-evidence's options (C3).
> 4. **Sequencing:** never run this Task B4's exec edits concurrently with judge-evidence Task 9/10/12. Land judge-evidence's harvest core first; this plan's B4 then reduces to the exec-side observer (map access) + `seedOutcome` + engine threading.

**Files (co-owned with judge-evidence — see B4 header):**
- Modify: `agent/harvest/runner.go` — **FALLBACK ONLY** (skip when judge-evidence Task 9 landed first; it owns the `Run` rewrite + `schema.Results` shape + base_sha in the metadata map)
- Test: `agent/harvest/runner_test.go` — fallback only, per above
- Create: `atc/exec/harvest_results_observer.go` (this plan's own file; parses `schema.Results`)
- Test: `atc/exec/harvest_results_observer_test.go` (Ginkgo, matching the exec package's suite convention)
- Modify: `atc/exec/harvest_step.go` (option, tee, seeding) — **shared with judge-evidence; ADD alongside its SecretEnv/flight/ingestion edits, reuse its `verifiedIDs` helper**
- Modify (NOT create): `atc/exec/harvest_step_test.go` — judge-evidence Task 10 CREATES this file; this plan MERGES its seeding specs in (recreating it would clobber judge-evidence's specs). If this plan somehow lands first, create it and note judge-evidence extends it.
- Modify: `atc/engine/step_factory.go` (follow the `agentTicketsStore` threading — field :41, option :60-63, harvestOpts append :291; ADD `WithHarvestOutcomesStore` alongside judge-evidence's harvestOpts, C3) + the factory's construction site in `atc/atccmd/command.go`

**Steps:**

- [ ] **Runner (RECONCILED — see B4 header point 2): SKIP this whole runner step when judge-evidence Task 9 has landed first (the recommended order).** Its `Run` rewrite already resolves base via `harvest.BaseSHA` and writes `head_sha`/`base_sha` into the `schema.Results` metadata MAP — there is nothing to add here. The two tests and the `ResultsMetadata`/`resolveBaseSHA` code below are the FALLBACK for the (discouraged) case where this plan lands before judge-evidence; even then, emit into the `schema.Results` metadata map, never a typed field, and read the fallback assertions as map keys (`res.Metadata["base_sha"].(string)`, the `runHarvest` helper decoding `schema.Results`). Fallback failing test, appended to `agent/harvest/runner_test.go` (uses the existing `workspaceWithRemote` fixture — the workspace is a clone with `origin/main` present):

```go
func TestRunEmitsBaseSha(t *testing.T) {
	workspace, remote := workspaceWithRemote(t)
	wantBase := git(t, workspace, "merge-base", "HEAD", "origin/main")

	code, res, out := runHarvest(t, harvest.Config{
		StepName: "harvest", Workspace: "ws", Repo: "o/r",
		TargetBranch: "main", TicketID: 42, Branch: "agent/ticket-42", Push: true,
	}, workspace)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	// res is a schema.Results; shas live in the metadata MAP.
	if res.Metadata["base_sha"] != wantBase {
		t.Fatalf("base_sha = %v, want %q", res.Metadata["base_sha"], wantBase)
	}
	if res.Metadata["head_sha"] == res.Metadata["base_sha"] {
		t.Fatal("head and base must differ — the workspace has agent work on top of the base")
	}
	_ = remote
}

func TestRunBaseShaBestEffortWhenTargetRefMissing(t *testing.T) {
	workspace, _ := workspaceWithRemote(t)
	// A target branch the clone has no remote ref for: base_sha must fall
	// back (origin/HEAD, else empty) — never fail the harvest.
	code, res, out := runHarvest(t, harvest.Config{
		StepName: "harvest", Workspace: "ws", Repo: "o/r",
		TargetBranch: "release/9.9", TicketID: 42, Branch: "agent/ticket-42", Push: true,
	}, workspace)
	if code != 0 {
		t.Fatalf("missing target ref must not fail the harvest: exit %d: %s", code, out)
	}
	// origin/HEAD exists in a fresh clone, so the fallback resolves; the
	// assertion is "did not error and did not lie": empty or a real sha.
	if base, _ := res.Metadata["base_sha"].(string); base != "" {
		if base != git(t, workspace, "merge-base", "HEAD", "origin/HEAD") {
			t.Fatalf("fallback base_sha %q is not merge-base(HEAD, origin/HEAD)", base)
		}
	}
}
```

(Adapt the `runHarvest`/`git`/`workspaceWithRemote` helper names to the fixture helpers actually present in `runner_test.go`; if the suite has no combined runner helper, follow its existing invocation pattern for `harvest.Run` and decode the results line from the output buffer.)

- [ ] Runner implementation — **FALLBACK ONLY (skip when judge-evidence Task 9 has landed; base_sha is already in the `schema.Results` metadata map via `harvest.BaseSHA`).** If this plan somehow lands first, do NOT reintroduce a typed `ResultsMetadata.BaseSHA` field (judge-evidence deletes the whole `ResultsMetadata` type). Instead put `base_sha` into the `schema.Results` metadata map under key `"base_sha"`, resolving it with the SAME `harvest.BaseSHA(workspaceDir, cfg.TargetBranch)` helper judge-evidence's Task 5 lands (ONE resolver, not a second inline `resolveBaseSHA`) — best-effort, `""` when unresolvable (§1.11.1: the diff API 404s until a later push supplies it), never fails the harvest.
- [ ] Run `go test ./agent/harvest/` — (fallback path only) expect the base_sha map key populated, existing suite untouched.
- [ ] **Observer:** failing spec `atc/exec/harvest_results_observer_test.go` (Ginkgo, in `exec_test` package like the rest of the suite):

```go
var _ = Describe("HarvestResultsObserver", func() {
	It("passes bytes through and captures the results.json line", func() {
		var sink bytes.Buffer
		obs := exec.NewHarvestResultsObserver(&sink)

		fmt.Fprintln(obs, "gate output noise")
		fmt.Fprintln(obs, `{"status":"pass","metadata":{"pushed_branch":"agent/ticket-7","head_sha":"aaa","base_sha":"bbb"}}`)

		Expect(sink.String()).To(ContainSubstring("gate output noise"))
		res := obs.Results()
		Expect(res).NotTo(BeNil())
		// schema.Results.Metadata is a map[string]interface{} (judge-evidence
		// Task 9) — read the shas as map keys, not struct fields.
		Expect(res.Metadata["head_sha"]).To(Equal("aaa"))
		Expect(res.Metadata["base_sha"]).To(Equal("bbb"))
	})

	It("keeps the LAST parseable results line", func() {
		obs := exec.NewHarvestResultsObserver(io.Discard)
		fmt.Fprintln(obs, `{"status":"fail","metadata":{"head_sha":"old"}}`)
		fmt.Fprintln(obs, `{"status":"pass","metadata":{"head_sha":"new"}}`)
		Expect(obs.Results().Metadata["head_sha"]).To(Equal("new"))
	})

	It("survives torn writes across Write calls", func() {
		obs := exec.NewHarvestResultsObserver(io.Discard)
		line := `{"status":"pass","metadata":{"head_sha":"torn"}}` + "\n"
		obs.Write([]byte(line[:10]))
		obs.Write([]byte(line[10:]))
		Expect(obs.Results().Metadata["head_sha"]).To(Equal("torn"))
	})

	It("returns nil when no results line ever arrived", func() {
		obs := exec.NewHarvestResultsObserver(io.Discard)
		fmt.Fprintln(obs, "just logs")
		Expect(obs.Results()).To(BeNil())
	})
})
```

- [ ] Observer implementation `atc/exec/harvest_results_observer.go`:

```go
package exec

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"

	schema "github.com/concourse/concourse/agent/schema"
)

// observerBufCap bounds the retained stdout; the runner's single-line
// results.json is the LAST thing it prints, so keep-the-tail is correct.
const observerBufCap = 1 << 20

// HarvestResultsObserver tees the harvest-runner's stdout (which also
// streams to the build log untouched) and captures the §2.8.1
// results.json line so the web side gets the runner's authoritative shas
// without a flight-recorder artifact. Never blocks, never breaks
// passthrough; a torn or absent line just yields Results() == nil
// (callers fall back per §1.11.1).
type HarvestResultsObserver struct {
	dst io.Writer
	mu  sync.Mutex
	buf bytes.Buffer
}

func NewHarvestResultsObserver(dst io.Writer) *HarvestResultsObserver {
	return &HarvestResultsObserver{dst: dst}
}

func (o *HarvestResultsObserver) Write(p []byte) (int, error) {
	n, err := o.dst.Write(p)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.buf.Write(p[:n])
	if o.buf.Len() > observerBufCap {
		// keep the tail (the results line is last); drop whole leading bytes
		tail := o.buf.Bytes()[o.buf.Len()-observerBufCap/2:]
		kept := make([]byte, len(tail))
		copy(kept, tail)
		o.buf.Reset()
		o.buf.Write(kept)
	}
	return n, err
}

// Results scans the retained stdout for the last parseable results line.
// The runner (judge-evidence Task 9) emits agent/schema.Results with the
// shas in the metadata MAP, so callers read res.Metadata["head_sha"] /
// res.Metadata["base_sha"] (see metaString below), NOT struct fields.
func (o *HarvestResultsObserver) Results() *schema.Results {
	o.mu.Lock()
	defer o.mu.Unlock()
	var last *schema.Results
	for _, line := range bytes.Split(o.buf.Bytes(), []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var r schema.Results
		if json.Unmarshal(line, &r) == nil && r.Status != "" {
			cp := r
			last = &cp
		}
	}
	return last
}

// metaString pulls a string value out of schema.Results' interface-typed
// metadata map ("" when absent or not a string).
func metaString(m map[string]interface{}, key string) string {
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}
```

- [ ] **Step seeding:** in `atc/exec/harvest_step.go` — add the option + field:

```go
// WithHarvestOutcomesStore wires delivery-outcomes' store so a
// successful push seeds the agent_outcomes row with the runner's
// authoritative shas (§1.11.1: harvest is the primary outcome-row
// creator; the watcher's branch-head fallback covers rows harvest could
// not seed). nil disables seeding — never fatal.
func WithHarvestOutcomesStore(s outcomes.Store) HarvestStepOption {
	return func(h *HarvestStep) { h.outcomesStore = s }
}
```

Refactor `transitionTicket`'s guard prelude (ticketID>0, run verifier, `RunBelongsToPipeline`, `TicketBelongsToRun` — `harvest_step.go:302-325`) into a shared server-side verifier used by both `transitionTicket` and the new `seedOutcome` (plan env is attacker-writable — the seeding path re-verifies). **Reuse judge-evidence Task 12's `verifiedIDs(logger lager.Logger) (ticketID, runID int)` helper (`(0,0)` when unverified) — do NOT add a second helper (see B4 header point 3).** If this plan lands first, name the helper `verifiedIDs` so judge-evidence consumes it. Wrap the process stdout:

```go
	resultsObserver := NewHarvestResultsObserver(delegate.Stdout())
	process, err := attachOrRun(ctx, container, processSpec, runtime.ProcessIO{
		Stdout: resultsObserver,
		Stderr: delegate.Stderr(),
	})
```

Extend the exit-taxonomy switch's case 0 (transition-first ordering is load-bearing):

```go
	case 0:
		branch := ""
		if step.plan.Push {
			branch = step.plan.Branch
		}
		step.transitionTicket(ctx, logger, "needs_review", branch, "")
		if branch != "" {
			step.seedOutcome(ctx, logger, resultsObserver.Results(), branch)
		}
```

```go
// seedOutcome creates/refreshes the agent_outcomes row with the runner's
// authoritative shas (§1.11.1 primary path). Failures are logged, never
// fatal — the watcher's fallback recovers.
func (step *HarvestStep) seedOutcome(ctx context.Context, logger lager.Logger, res *schema.Results, branch string) {
	if step.outcomesStore == nil {
		return
	}
	ticketID, _ := step.verifiedIDs(logger) // shared helper (judge-evidence Task 12); 0 when unverified
	if ticketID == 0 {
		return
	}
	var pushed, base string
	if res != nil {
		// schema.Results.Metadata is a map (judge-evidence Task 9) — read shas as map keys.
		pushed, base = metaString(res.Metadata, "head_sha"), metaString(res.Metadata, "base_sha")
	}
	err := step.outcomesStore.Ensure(&outcomes.Outcome{
		TicketID:  ticketID,
		Repo:      step.plan.Repo,
		Branch:    branch,
		PushedSha: pushed,
		BaseSha:   base,
	})
	if err != nil {
		logger.Error("failed-to-seed-outcome", err, lager.Data{"ticket-id": ticketID})
		return
	}
	logger.Info("outcome-seeded", lager.Data{"ticket-id": ticketID, "pushed-sha": pushed, "base-sha": base})
}
```

- [ ] **Step tests** — MERGE the seeding specs into `atc/exec/harvest_step_test.go` (Ginkgo, `exec_test` package). judge-evidence Task 10 CREATES this file with the exec harness; ADD these specs to it rather than recreating it (recreating clobbers judge-evidence's specs). If landing before judge-evidence, create the file and note judge-evidence extends it. Harness: copy the fake wiring from `agent_step_test.go`'s BeforeEach (fake Pool/worker/container via `execfakes` + runtime fakes as that suite does; fake `TaskDelegate` whose `Stdout()` returns a `gbytes.Buffer`); the fake process's `Wait` returns the scripted exit status AFTER the scripted runner stdout (the results line) has been written through the step's ProcessIO. Stores: `tickets.NewMemoryStore()` seeded with a ticket in `running`, `outcomes.NewMemoryStore()`, a fake `AgentRunVerifier` returning true/true. Specs (write all five):
  1. exit 0 + push=true + results line with head+base → ticket `needs_review` with branch AND outcome row `{Branch, PushedSha: head, BaseSha: base, MergeState: open}`.
  2. exit 0 + push=true + NO parseable results line → ticket transitions; outcome row exists with empty shas (Ensure still called — the row anchors `created_at`).
  3. exit 0 + push=false → NO outcome row.
  4. exit 1 → needs_review with empty branch, NO outcome row.
  5. nil outcomes store (option not set) → transition unaffected, no panic.
  Plus one ordering spec: a failing Ensure (fake store erroring) does not fail the step and the ticket is already transitioned.
- [ ] **Wiring:** `atc/engine/step_factory.go` — follow the `agentTicketsStore` threading (field :41, `WithAgentTicketsStore` :60-63, harvestOpts append :291): add an `agentOutcomesStore outcomes.Store` factory field + option, `harvestOpts = append(harvestOpts, exec.WithHarvestOutcomesStore(factory.agentOutcomesStore))` when non-nil, and thread `db.NewAgentOutcomesFactory(dbConn)` from the engine construction site in `atc/atccmd/command.go` (grep `agentTicketsStore` for the exact constructor call chain — same files).
- [ ] Run: `go test ./agent/harvest/ ./atc/exec/ ./atc/engine/` — expect green.
- [ ] Commit: `feat(harvest): seed agent_outcomes with authoritative shas at push time (runner base_sha + stdout results tee)`

### Task C1 (Slice C): `agent/gitcheck` — bare-mirror cache + low-level git ops

**Execute plan 12 Task 6 as written (lines 1385-1876), with these deltas:**
- The task's recipe reference `agent/harvest/workspace.go` / `workspace_test.go` DOES NOT EXIST. The real-git-fixture recipe is `agent/harvest/runner_test.go` (its git-in-tempdir helpers) and `gates_test.go`. Hermetic tempdirs, git on PATH, no network.
- REVIEW.md honesty note applies: this is a **third** git-credential/repo-cache system (after the harvest push path and the dispatch git resource). Say so in the package doc comment, name the other two, and state why it is separate (web-node-resident read-only mirrors vs in-pod push vs resource checkout).
- Credential handling mirrors the harvest GIT_ASKPASS pattern (`runner.go:159-181`): https-only, temp credential helper, token never on argv. Reuse by pattern, not by import, unless a clean shared helper falls out — if you extract one, put it in `agent/gitcheck` and have callers converge later (ADD-never-REPLACE: do not touch the harvest runner's copy in this task).

### Task C2 (Slice C): `agent/gitcheck` — merge detector + windowed diff

**Execute plan 12 Task 7 as written (lines 1877-2157), with the same recipe-reference delta as Task C1.** The heuristics are exactly the frozen §1.11.1 v1 set (is-ancestor primary; ancestry-path merge-point; patch-id squash fallback N=200; edited-squash/rebase stays open; numstat LINES delta over non-bot first-parent commits; binary files count 0).

### Task C3 (Slice C): `agent/outcomewatcher` — amended RunnableComponent

**Execute plan 12 Task 10 as written (lines 2518-3051) EXCEPT the following amendments, which supersede the original text wherever they conflict.** Everything else — MirrorCache doubling as the diff handler's `MirrorProvider`, detectMerges re-checking ticket state == `needs_review` before transitioning, transitions exclusively via `tickets.Store.Transition` with `ErrStaleTransition` benign, the F6 re-arm spec with its two load-bearing orderings (F38), the concluded skip-detection spec, `Touch` on every scanned row, polling-only (NEVER notify-only — the notifications bus silently drops) — stands as written.

**Amendment C3-1 — drop the metrics dependency entirely (by design, not by coincidence).** The watcher deliberately does NOT read `agent_run_metrics` for shas — the authoritative sha source is harvest's push-time outcome seeding (Task B4), full stop. Note the premise is design, not absence: harvest v0/v0.5 wrote no metrics rows, and the sibling judge-evidence remainder's Task 12 ADDS an `agent_run_metrics` write to `exec.HarvestStep` — so "metrics don't exist" is NOT the reason and MUST NOT be relied on. The watcher stays metrics-independent whether or not harvest writes metrics. The original `resolveShas` metrics lookup is dropped; the watcher takes THREE collaborators, not four:

```go
type Watcher struct {
	tickets  tickets.Store
	outcomes outcomes.Store
	cache    *MirrorCache
}

func New(t tickets.Store, o outcomes.Store, cache *MirrorCache) *Watcher {
	return &Watcher{tickets: t, outcomes: o, cache: cache}
}
```

Remove every `metrics.Store` / `agent/api/metrics` / `agent/schema` reference from the watcher and its spec (imports, constructor, fixture `metricStore`, the metrics-row insertions at the original plan's spec lines that set `"metadata": map[string]any{"pushed_branch": ..., "head_sha": ..., "base_sha": ...}`). Do NOT re-add a metrics lookup later EVEN THOUGH judge-evidence Task 12 makes harvest write `agent_run_metrics` — harvest's push-time seeding (Task B4) is the authoritative sha source by design; the watcher is metrics-independent on purpose.

**Amendment C3-2 — `seedRows` becomes a create-if-absent + re-arm backstop that never clobbers seeded shas:**

```go
// seedRows is the §1.11.1 BACKSTOP: harvest seeds rows with authoritative
// shas at push time (exec.HarvestStep.seedOutcome). Here we only (a)
// create a fallback row when none exists — pushed_sha = remote branch
// head at first sync (weaker baseline), base_sha = '' (diff 404s until a
// re-push seeds it) — and (b) re-arm a sent_back row (F6) when harvest's
// own Ensure did not. An existing OPEN row is left alone: the backstop
// must never overwrite harvest-seeded shas with fallback values.
func (w *Watcher) seedRows() error {
	tks, err := w.tickets.List(tickets.ListFilter{State: tickets.StateNeedsReview})
	if err != nil {
		return err
	}
	for _, tk := range tks {
		if tk.Branch == "" || tk.Repo == "" {
			continue
		}
		row, found, err := w.outcomes.Get(tk.ID)
		if err != nil {
			return err
		}
		rearmable := found &&
			row.MergeState == outcomes.ClosedUnmerged &&
			row.Disposition == string(outcomes.DispositionSentBack)
		if found && !rearmable {
			continue // open rows keep their (possibly authoritative) shas; terminal rows are terminal
		}
		pushed, base := w.fallbackShas(tk)
		if err := w.outcomes.Ensure(&outcomes.Outcome{
			TicketID: tk.ID, Repo: tk.Repo, Branch: tk.Branch,
			PushedSha: pushed, BaseSha: base,
		}); err != nil {
			return err
		}
	}
	return nil
}

// fallbackShas: remote branch head at first sync; base unknown.
func (w *Watcher) fallbackShas(tk tickets.Ticket) (pushed, base string) {
	m, err := w.cache.Mirror(tk.Repo)
	if err != nil {
		return "", ""
	}
	head, err := m.BranchHead(tk.Branch)
	if err != nil {
		return "", ""
	}
	return head, ""
}
```

(Adapt `Mirror`/`BranchHead` names to Task 6's landed API.)

Spec changes this forces in `watcher_test.go`:
- Wherever the original spec inserted a metrics row to supply shas, instead call `outcomeStore.Ensure(&outcomes.Outcome{TicketID: ..., Repo: ..., Branch: ..., PushedSha: pushed, BaseSha: base})` directly — simulating harvest's push-time seeding, which is exactly what production does.
- The F6 tick-2 re-arm assertions hold: with no harvest re-seed simulated, `seedRows` re-arms the `closed_unmerged`+`sent_back` row with FALLBACK shas (pushed = reworked branch head from the mirror, base = ''). Amend the F6 spec's sha assertions accordingly — recommended: simulate harvest's re-seed before tick 2 so the spec pins the primary path, and add a SEPARATE smaller spec "re-arms with fallback shas when harvest did not re-seed".
- NEW spec — "does not clobber harvest-seeded shas": Ensure a row with `PushedSha: "aaa", BaseSha: "bbb"` (harvest), run a tick (branch head in the mirror differs from "aaa"), assert the row still reads `aaa`/`bbb`.

**Amendment C3-3 — terminal sweep (writer-reconciliation cleanup, D-3).** Add a third phase to `Run` (after detectMerges, so an actual merge wins the race):

```go
// sweepTerminal closes OPEN rows whose ticket a bypassing writer already
// drove terminal (§1.11.1 writer reconciliation): abandoned →
// closed_unmerged, concluded → concluded — never inventing disposition
// taxonomy. merged/merged_with_fixes tickets are NOT swept: organic
// detection closes those rows with a full delta when the mirror shows
// the merge.
func (w *Watcher) sweepTerminal() error {
	open, err := w.outcomes.ListOpen()
	if err != nil {
		return err
	}
	for _, row := range open {
		tk, found, err := w.tickets.Get(row.TicketID)
		if err != nil || !found {
			continue
		}
		switch tk.State {
		case tickets.StateAbandoned:
			if err := w.outcomes.Close(row.TicketID, outcomes.ClosedUnmerged); err != nil {
				return err
			}
		case tickets.StateConcluded:
			if err := w.outcomes.Close(row.TicketID, outcomes.MergeConcluded); err != nil {
				return err
			}
		}
	}
	return nil
}
```

Specs: (1) open row + ticket raw-transitioned to `abandoned` → after one tick the row is `closed_unmerged` with ALL disposition fields still empty; (2) same for `concluded` → `concluded`; (3) open row + ticket raw-transitioned to `merged` → row stays open (and a later mirror merge closes it as `merged` with delta — reuse the detection fixture); (4) rows already closed are untouched (Close's own no-op, but pin it at watcher level).

**Delta notes (unchanged behavior to re-verify, not re-design):** one `git fetch --prune` per repo per tick regardless of ticket count; `Run` returns per-repo errors without aborting the whole tick; concluded skip is structural (`ListOpen` excludes `concluded`; sweep + SetDisposition are the only concluded writers).

- [ ] Run `ginkgo ./agent/outcomewatcher/` — expect green including the amended F6 spec, the no-clobber spec, and the four sweep specs.
- [ ] Commit: `feat(agentic): outcome watcher — harvest-seeded shas, fallback backstop, terminal sweep`

### Task C4 (Slice C): `GetAgentTicketDiff` handler

**Execute plan 12 Task 8 as written (lines 2158-2344), with these deltas:**
- The "404 while base_sha unknown" behavior is now the EXCEPTION, not the norm: Task B4 seeds `base_sha` at push time, so the diff lights up for every post-B4 harvest. Keep the 404 branch (fallback-seeded rows) and its spec.
- Window numbers are frozen in §1.11.1: 50-file window, 64 KiB per-file cap, `has_more` paging.
- Rationale vs wave E's GitHub compare link (see Landed state): the external compare link degrades after merge (branch vs target compares empty) and needs GitHub; the in-app diff pins `base_sha..pushed_sha` permanently, is windowed, and works for private/non-GitHub remotes. Both stay.

### Task C5 (Slice C): diff route wiring (C1 × 1) + component + flags

Second half of plan-12 Task 11 (lines 3052-3234). Anchor drift only — the design is as written.

**Files:** the six C1 touchpoints (as Task B2) + `atc/component.go` + `atc/atccmd/command.go` (flag group, a NEW `agentOutcomeMirrorProvider` field on `RunCommand`, the diff-handler threading, and the component block) + `agent-route-scopes.md`.

**Steps:**

- [ ] `GetAgentTicketDiff` through all SIX C1 touchpoints exactly as in Task B2 (constant + `{Path: "/api/v1/agent/tickets/:ticket_id/diff", Method: "GET", Name: GetAgentTicketDiff}`; the plain `CheckAgentAuthorizationHandler` case block at :205-224 — viewer via `DefaultRoles`, NOT the `AgentPrincipalOrMainTeamHandler` block at :228-235; reject_archived bucket; auditor switch + explicit spec; `DefaultRoles: "viewer"`; handler map + both `NewHandler` call sites; `agent-route-scopes.md` row). The diff route is served by a SECOND handler — construct `outcomesapi.NewDiffHandler(outcomesStore, outcomeDiffProvider)` beside the Task B1 `NewHandler` and map `atc.GetAgentTicketDiff: http.HandlerFunc(outcomeDiffServer.GetDiff)` (plan-12 Task 11 lines 3168-3178). It needs the `MirrorProvider` seam — thread the shared `MirrorCache` (or a true-nil interface when the flag is off → handler returns 404/503 per Task 8's disabled behavior); see the field + threading step below.
- [ ] `atc/atccmd/command.go` — declare the bridging field on `RunCommand`, next to `k8sArtifactLocator` (:126), so the component block (which builds the cache) can hand it to the diff-handler arg-threading (a DIFFERENT method — same reason `k8sArtifactLocator` bridges `backendComponents` and `constructPool`):

```go
	// agentOutcomeMirrorProvider is the shared outcome-diff mirror cache,
	// built in the component block when --agent-outcome-git-dir is set and
	// consumed by GetAgentTicketDiff's handler threading. nil (a true nil
	// interface) when the master switch is off → the diff API 404/503s.
	agentOutcomeMirrorProvider outcomes.MirrorProvider
```

  Thread it into the diff handler at the `NewHandler` arg site (near :2390, the same site Task B2 threads the outcomes store): pass `cmd.agentOutcomeMirrorProvider` as the `outcomeDiffProvider outcomes.MirrorProvider` param plan-12 Task 11 adds to `atc/api/handler.go` (:3160-3162). Because the field is an interface and is assigned ONLY inside the `!= ""` guard (to a real non-nil `*MirrorCache`), it stays a true nil interface when disabled — do NOT assign a typed-nil `*MirrorCache` to it, which would defeat the handler's `provider == nil` check (plan-12 Task 11 line 3208).
- [ ] `atc/component.go` — add `ComponentAgentOutcomeWatcher = "agent_outcome_watcher"` alongside `ComponentAgentRunSecretReaper` (:26).
- [ ] `atc/atccmd/command.go` — flag group next to `AgentRepoBaseURL` (:234):

```go
	AgentOutcomeGitDir          string        `long:"agent-outcome-git-dir" description:"Directory for the outcome watcher's bare git mirrors. Empty disables both the watcher and the ticket diff API (the master switch)."`
	AgentOutcomeGitURLTemplate  string        `long:"agent-outcome-git-url-template" default:"https://github.com/{repo}.git" description:"Template for mirror clone URLs; {repo} is the ticket's repo slug."`
	AgentOutcomeGitUsername     string        `long:"agent-outcome-git-username" description:"Optional username for mirror fetches (https only)."`
	AgentOutcomeGitToken        string        `long:"agent-outcome-git-token" description:"Optional token for mirror fetches (https only; delivered via a temp credential helper, never argv)."`
	AgentOutcomeCheckInterval   time.Duration `long:"agent-outcome-check-interval" default:"5m" description:"Outcome watcher polling interval (one fetch --prune per repo per tick)."`
	AgentOutcomeSquashScanLimit int           `long:"agent-outcome-squash-scan-limit" default:"200" description:"How many recent target-branch commits the patch-id squash fallback scans."`
```

- [ ] Component wiring in the interval-polled block (`command.go:1355-1376`, after `ComponentAgentRunSecretReaper`) — but NOTE: unlike the two agent components there, the watcher must NOT be inside any k8s-conditional guard; the watcher needs no cluster. Place it adjacent but gated ONLY on `cmd.AgentOutcomeGitDir != ""`:

```go
	if cmd.AgentOutcomeGitDir != "" {
		// NewMirrorCache takes POSITIONAL args (dir, urlTemplate, Auth, scanLimit).
		// There is NO MirrorConfig struct — plan-12 Task 10 defines
		// `func NewMirrorCache(dir, urlTemplate string, auth Auth, scanLimit int) *MirrorCache`
		// with a nested `Auth{Username, Token}` (and `type Auth = gitcheck.Auth`).
		outcomeCache := outcomewatcher.NewMirrorCache(
			cmd.AgentOutcomeGitDir,
			cmd.AgentOutcomeGitURLTemplate,
			outcomewatcher.Auth{Username: cmd.AgentOutcomeGitUsername, Token: cmd.AgentOutcomeGitToken},
			cmd.AgentOutcomeSquashScanLimit,
		)
		cmd.agentOutcomeMirrorProvider = outcomeCache // bridges to the diff-handler threading (declared step above)
		components = append(components, RunnableComponent{
			// atc.Component has ONLY a Name field (atc/component.go:32-34) —
			// the interval lives on the outer RunnableComponent, exactly like
			// ComponentAgentRunSecretReaper at command.go:1371-1383.
			Component: atc.Component{
				Name: atc.ComponentAgentOutcomeWatcher,
			},
			Runnable: outcomewatcher.New(
				db.NewAgentTicketsFactory(dbConn),
				db.NewAgentOutcomesFactory(dbConn),
				outcomeCache,
			),
			Interval: cmd.AgentOutcomeCheckInterval,
		})
	}
```

(Adapt constructor names to the landed Task 6/C3 API. `outcomewatcher.New` takes THREE collaborators here — tickets factory, outcomes factory, cache — matching the C3-1 amendment that dropped the metrics dependency (plan-12's four-arg original is dead). `atc.Component{}` carries only `Name`; the interval is on the outer `RunnableComponent` per the `ComponentAgentRunSecretReaper` precedent. Polling-only: the component gets NO notification channel.)
- [ ] Run: `go test ./atc/wrappa/... ./atc/auditor/... ./atc/atccmd/... && ginkgo ./atc/api/` — expect green.
- [ ] Commit: `feat(agentic): GetAgentTicketDiff route + agent_outcome_watcher component + --agent-outcome-* flags`

### Task D1 (Slice D): Elm data layer — outcome + diff

**Execute plan 12 Task 13 as written (lines 3422-3694), with these deltas:**
- **DROP** from the task: the `GetAgentTicketReviews` endpoint/effect/callback (deferred with Task 9 — the shipped build-id stopgap at `AgentTicket.elm:143-151` stays), and ALL `GetAgentCostRollup` wiring (the shipped page sums per-run metrics `costUsd` at :551/:805 — keep that).
- **KEEP**: `web/elm/src/Concourse/AgentOutcome.elm` (decoder incl. `base_sha`, `merge_state`, delta fields, disposition fields), diff types + decoder, endpoints `AgentTicketOutcome`/`AgentTicketDiff` (append to the existing `AgentTickets`-prefixed endpoints in `Api/Endpoints.elm` — search the symbols; waves E+F shifted line anchors), effects `FetchAgentTicketOutcome`/`FetchAgentTicketDiff Int Int Int` (offset/limit)/`SetAgentTicketDisposition`, callbacks with the established `AgentTicket*` naming.
- The `Concourse/AgentTicket.elm` decoder already decodes `branch` (and E+F added `repoWebUrl`/`compareUrl` helpers :164-213) — no decoder change needed for branch anywhere.
- elm-test for every decoder (follow the shipped `Concourse.AgentReview` decoder-test recipe).
- Spine-serialization rule: this task and D2/D3 touch `Effects.elm`/`Callback.elm`/`Endpoints.elm` and `AgentTickets/AgentTicket.elm` — NO parallel Elm sessions. **Cross-plan co-editors of these SAME files:** platform-mcp-hitl Tasks 27-28 (the `div#ticket-hitl-slot` question banner in `AgentTicket.elm` + spine effects/callbacks/endpoints), and judge-evidence Task 13 (different Elm modules — the build page's `Concourse.elm`/`StepTree.elm` — but it regenerates the SAME embedded bundle `web/public/elm.js`+`elm.min.js`). Serialize ALL Elm work across the three plans: no two Elm slices run concurrently; each rebases onto whatever Elm landed first and regenerates the bundle as its own final commit.

### Task D2 (Slice D): Elm PR-view additions — outcome badge + paginated diff

**Execute plan 12 Task 14 as written (lines 3695-3973), with these deltas:**
- **Already shipped, do not re-implement:** spec/plan tabs, live task list, evidence panel (reuses `Build.AgentReview` verbatim), cost display, six-verdict feedback, **and — new since the scout pass — the branch display + repo/compare links** (wave E N1: provenance line at `AgentTicket.elm:393-443` rendering `branch <b> — review diff vs <target>` via `Concourse.AgentTicket.compareUrl`). Plan-12 Task 14's branch-display scope is fully pre-satisfied; do NOT add a second branch row.
- Task 14's remaining scope is ONLY: (a) the outcome badge via `mergeStateLabel` — including `"concluded (no merge intended)"`; (b) the paginated in-app diff view (offset/limit against `FetchAgentTicketDiff`, `has_more` → "load more"). Place the badge in the existing provenance line beside the state pill; place the diff view as a new collapsible section patterned on the shipped evidence panel.
- **DROP:** the judge-score panel — and note NO dedicated judge-specific ticket-page UI is built in any plan. Once the judge-evidence remainder lands, judge findings (category `judge`) surface WITHOUT new UI here: the build page renders the harvest step + judged evidence (judge-evidence Task 13), and the already-shipped generic six-verdict feedback (`SubmitAgentReviewVerdict`, :288) is category-agnostic and already works on `judge` findings. Do not plan or stub a judge-score/judge-feedback panel here.
- Fetch the outcome on page load only when `ticket.branch /= ""`; render the badge `open / merged / merged with fixes (+N -M by K commits) / closed unmerged / concluded (no merge intended)`; a 404 (row not yet seeded) renders nothing — dark-until-filled, matching plan 13's expectation.
- elm-test: `mergeStateLabel` cases incl. concluded; diff paging message flow; badge hidden on outcome-404.

### Task D3 (Slice D): Elm disposition form replacing the bare terminal buttons

**Execute plan 12 Task 15 as written (lines 3974-4142), with these deltas:**
- The shipped `transitionTargets` (`AgentTicket.elm:517-544`) offers five bare terminal buttons from `needs_review`: `merged`, `merged_with_fixes`, `sent_back`, `concluded`, `abandoned`. Per D-3: **keep** `( "merged", "Merge" )` and `( "merged_with_fixes", "Merge with fixes" )` as raw transitions; **remove** `sent_back`/`concluded`/`abandoned` from the `needs_review` list and replace them with the Task 15 disposition form (disposition dropdown incl. `concluded`, reason taxonomy incl. `research_complete`, free-text notes, submit → `SetAgentTicketDisposition` effect, server 400/409 → inline error + refetch exactly like the shipped transition-button error path, `actionError`/`lifecycleBar` at :460-502).
- The `draft`/`queued` states' `Abandon` buttons stay raw transitions: the B1 handler's from-guard is `needs_review`, and draft/queued abandonment predates any push, so there is no outcome row to bookkeep (the terminal sweep is moot there). Note this in a comment.
- Six-verdict finding feedback is ALREADY SHIPPED (`SubmitAgentReviewVerdict`, :288) — Task 15's feedback wiring section is fully pre-satisfied; skip it.
- elm-test: form validation (reason required per the handler's rule), concluded/research_complete submission, 409 refetch.

### Task D4 (Slice D): bundle regeneration + local click-through

- [ ] `cd web/elm && elm-test && elm make` clean (baseline: 3090 green after waves E+F).
- [ ] Regenerate the embedded bundle (`web/public/elm.js` + `elm.min.js`) exactly as commit 0866d89fc9 did; commit the bundle with the Elm source in one commit (spine-serialization rule).
- [ ] Local web boot; on a ticket with a branch: badge renders, diff pages, disposition form round-trips, 409 path refetches, the shipped compare link still renders beside the new badge.
- [ ] Commit: `feat(web): ticket PR view — outcome badge, paginated diff, disposition form (+bundle)`

### Task E1 (Slice E): live mirror-fetch probe

**Execute plan 12 Task 16 as written (lines 4143-4190):** `//go:build live` plain-Go test `atc/worker/jetbridge/live_outcome_watcher_test.go`, env-gated `AGENT_OUTCOME_LIVE_REPO`, needs only outbound git-over-https from this machine (no cluster):

```
AGENT_OUTCOME_LIVE_REPO=tdmtrader/concourse go test -tags live -run '^TestLiveOutcomeMirrorFetch$' -v -count=1 -timeout 5m ./atc/worker/jetbridge/
```

### Task E2 (Slice E): theborg end-to-end watcher smoke (human-supervised)

Not in the original plan as a task; execution-notes material promoted to a checklist. Requires theborg + the release pipeline.

- [ ] Deploy the release with `--agent-outcome-git-dir` set (helm values: web extraArgs), url template default, and the fetch credential decision from §7 O-1 resolved (separate read-only token recommended; contracts pin the harvest PAT as push-scoped).
- [ ] Dispatch a ticket end-to-end (per the manual-dispatch runbook: create → queue → dispatch; agent-run-<id> secret attach is mandatory for ticketed runs); let harvest push `agent/ticket-<N>`.
- [ ] Verify the outcome row was harvest-seeded with BOTH shas (`fly curl /api/v1/agent/tickets/N/outcome`, or SQL).
- [ ] Open the ticket page: badge `open`, compare link live, in-app diff renders (base_sha known).
- [ ] Merge the branch on github with one human fixup commit first; within one 5m tick: ticket flips to `merged_with_fixes` via the transition function, row records `merged_sha` + a LINES delta of the fixup, §8.4 webhook fires (LISTEN scanner per the theborg runbook).
- [ ] Dispose a second (spike) ticket `concluded` via the Elm form; verify the row closes `concluded` and the watcher's next tick does not touch it.

---

## 5. Migration allocations (C2 — read before Task A2 lands)

- This plan owns exactly ONE migration: `agent_outcomes`, contract-labelled **1773106090** (the delivery-outcomes block 1773106090-99). Down = `DROP TABLE agent_outcomes;` (one table + one partial index; clean rollback).
- Deployed head TODAY = **1773106066** (both dual constants verified at HEAD `187cad4926`: `atc/db/migration/legacy_upgrade_test.go:37`, `docs/migration/migrate-preflight.sh:38`). Workflow-source slice-a landed `1773106066_add_agent_workflow_source_manifest` — the scout-era head 1773106064 is stale. Bump BOTH constants to the landed number in the SAME commit as the migration (C2).
- **PARK-V2 note (informational, not this plan's problem):** the reserved `1773106065` (`agent_run_step_state`) is now BELOW the deployed head — it was foreclosed the moment 1773106066 landed, and must be renumbered above the then-current head at ITS land time regardless of anything this plan does (the standing SS11 rule already says so).
- **Landing-order hazard (normative):** the migrator is version-pointer based and silently skips lower numbers merged late. The only pending LOWER reservation between head and 1773106090 is plan-09's `1773106080-89` block (agent_reviews ticket linkage, not landed). **Landing 1773106090 forecloses that block for late landing** — after this lands, plan-09's migration must renumber above the then-current head at its landing (the ticket-core precedent: reserved 1773106050-52 landed as 1773106062-64).
- Therefore, at land time: (1) re-verify the actual head; (2) check whether the plan-09 remainder is mid-flight and about to land 1773106080 — if it is days away, prefer letting it land first (owner call, see §7 O-3); (3) land this migration at `1773106090` (or the next free number above the verified head if something ≥1773106090 landed meanwhile — nothing is allocated there today except this plan; scorecards owns 1773106110); (4) record the foreclosure in the Task A1 SS11 entry if it was not already worded there.
- If, against this plan's recommendation, plan-09's `1773106080` linkage is pulled into the same effort after all: it MUST merge BEFORE this migration (lowest-version-first).

---

## 6. Not re-verified here (trust the plan-12 text)

Plan-12 tasks referenced as-written (Tasks 2-8, 10 core, 13-16) contain their own failing-test-first steps, full code, and expected outputs — verified against F6/F38 fixes and the concluded amendments per the SS11 log. This remainder plan adds only the deltas listed per task. If a referenced task's text contradicts a delta note here, the delta note wins; if it contradicts landed code (anchor drift), the landed symbol wins (plan-12's own anchor caveat, line 55). Line anchors in THIS document were verified at HEAD `187cad4926`; treat them as "the location of the quoted code" and search by symbol on drift.

---

## 7. Risks & open decisions

**Owner decisions required before Task A1 merges:**
- **O-1 (credentials, operator check — cannot verify from the repo):** the watcher's fetch credential. Contracts pin the harvest PAT as push-scoped; §1.11.1 flags a separate `--agent-outcome-git-token`. Recommended: mint a read-only fine-grained PAT for the watcher; do NOT reuse the push PAT. Needed only for private repos — theborg's current repos are public-read, so E2 can start credential-less.
- **O-2 (agent image git binary):** `agent/gitcheck` + `agent/outcomewatcher` suites shell out to real git; harvest-runner already requires git in the same image family, so in-pod gates almost certainly pass — but CONFIRM (`kubectl exec` a runner pod: `git --version`, or check the image Dockerfile) BEFORE dispatching Slice C to the loop. If absent: add git to the agent-runner image first (its own small ticket) or run Slice C natively.
- **O-3 (migration landing order):** confirm the plan-09 remainder's status at land time per §5 (workflow slice-a already landed; PARK-V2 is already foreclosed independently of this plan). If plan-09's 1773106080 lands within days, sequence this migration after it.
- **D-1/D-2/D-3 confirmations** (§2): delta definition (already plan-13-signed), sha provenance via harvest seeding, writer reconciliation incl. the terminal sweep.

**Risks:**
- **Sha provenance is only as good as the tee.** A runner OOM after push but before the results line leaves an empty-sha row (spec 2 of Task B4 pins the behavior); the watcher fallback then supplies branch-head. Documented v1 limit; the delta baseline for such rows is weaker.
- **Base-sha resolution depends on the git-resource clone's remote refs** (`harvest.BaseSHA`, judge-evidence Task 5; or this plan's fallback resolver if it somehow lands first). If dispatch ever moves to shallow/treeless clones, `merge-base` may fail → base `''` → diff dark for that ticket. Non-fatal by design; note in §1.11.1 if clone shape changes.
- **Two divergent close paths during the B3 rollout window:** until D3 lands, the Elm terminal buttons still bypass the disposition route. The terminal sweep (C3-3) bounds the damage (rows close, taxonomy empty). Land D3 in the same release train as B3 if possible.
- **Time-to-merge honesty (REVIEW.md):** `agent_outcomes.created_at` is now the harvest-push timestamp for seeded rows (a real anchor!) but first-sync time for fallback rows. Scorecards must label durations "time in review" unless the row was harvest-seeded — carry this note into plan 13's consumption when it executes.
- **Third git-system sprawl:** accepted and documented (Task C1 delta). Consolidation is future work, not this plan.
- **Anchor churn is live:** three commits landed between the scout pass and this plan (1773106066, waves E+F); more may land before execution. Every executor step that names a line number must re-grep the symbol first (this plan's anchors were re-verified at `187cad4926`).
- **Loop budget:** Slices B and C as loop tickets are each ≈ ticket-#14-sized or slightly above; do not fan them out in parallel (shared rate-limit window; dispatch → settle → dispatch).

---

## 8. Complexity, risk, and recommended execution level

Honest assessment: this is a LARGE remainder (one migration, one new table+factory, three new Go packages, four C1 route rounds compressed to three, an exec-step change on the harvest hot path, an Elm round, and a live smoke). The saving grace: plan 12's original text is unusually complete (full code + F6/F38-hardened specs), so most tasks are faithful execution, not design.

**Recommendation: SPLIT by slice.**

| Slice | Level | Rationale |
|---|---|---|
| A1 (contracts) | **native-fable** (short session) | Carries the flagged human decisions (D-1/D-2/D-3, O-3); docs-only but every later task builds on the frozen text. Owner in the loop by definition. |
| A2-A4 (migration + domain + factory) | **native-opus** | Migration + postgres-backed factory suite are NOT gate-verifiable; the dual-constant bump and §5 ordering check want the owner's machine. Task A3 alone is loop-sonnet-able but not worth a separate dispatch. |
| B1-B3 (handler, routes, fly) | **loop-opus** (one ticket) | Pure-Go, fully specced, all tests in-gate (plain testing + wrappa/auditor + fly mock — no postgres). C1 six-touchpoint text is spelled out per the loop-ability rule. Sized ≈ ticket #14. Human pre-merge local-verify: `make test-unit` + `make test-fly-integration`. |
| B4 (harvest seeding) | **loop-fable** (one ticket) — or native-opus | Novel design on the harvest hot path (stdout tee, verifier-guarded seeding, engine threading) = the architectural end of the loop spectrum; the plan text here is complete enough for a faithful executor, but pick the top model tier. Fully gate-verifiable (`agent/harvest`, `atc/exec`, `atc/engine` — no postgres). |
| C1-C5 (gitcheck, watcher, diff, wiring) | **loop-opus** (two tickets: C1+C2, then C3+C4+C5) — GATED on O-2 (git in the agent image); fall back to native-opus | Real-git hermetic suites run under plain `go test ./...` in-pod IF git exists. The C3 amendments are fully written out above. Two sequential tickets, no fan-out. Human pre-merge: `make test-unit`. |
| D1-D4 (Elm) | **native-opus** | No Elm toolchain in gates — never loop. Spine-serialization forbids parallelism. Moderate residual ambiguity (placement/styling on the shipped page); opus suffices given the audit-wave + waves-E/F patterns, escalate to fable only if the diff view needs design judgment. |
| E1-E2 (live) | **native-fable** | theborg deploy, real merge, webhook LISTEN verification, 5m-tick observation — inherently owner-supervised, full-access session. |

Sequencing: A (native) → B1-B3 (loop) → B4 (loop) → C (loop ×2) → D (native) → E (native). Each loop dispatch waits for the previous merge (dispatch-timing rule: push → settle → dispatch; no double-spend). The ticketed runs' `gate_policy` must be scoped like tickets #13/#14 (full-scope go gates; postgres-backed specs explicitly excluded from the gate and covered by the human local-verify step named per slice).
