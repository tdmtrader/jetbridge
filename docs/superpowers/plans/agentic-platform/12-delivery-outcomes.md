# Delivery Outcomes Implementation Plan

> **SUPERSEDED — historical implementation record (not executable).** The approved [Agentic Workflows as Functions over Snapshots design](../../specs/2026-07-21-agentic-workflows-as-functions-design.md) and [Agentic Functions program plan](../2026-07-21-agentic-functions-program.md) are authoritative. Existing outcome and diff code remains a compatibility/projection source; generic workflow outcomes are keyed by durable workflow-run and snapshot identity, and publication is an explicit DAG node.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the ticket page into a lightweight PR view (paginated diff, review evidence, judge score, plan progress, cost, six-verdict feedback), add ticket-level dispositions (sent-back/abandoned/concluded with a reason taxonomy), and ship the native outcome watcher that records merged / merged-with-fixes / human-touch delta by polling git repos directly — no webhooks.

**Architecture:** A new `agent_outcomes` table (migration `1773106090`) + `agent/api/outcomes` domain package + `atc/db.NewAgentOutcomesFactory` hold merge facts and dispositions keyed by ticket. A new `agent/gitcheck` package maintains bare `git clone --mirror` caches on the web node and answers ancestor / merge-point / human-delta / patch-id / diff questions by shelling out to real git (the `agent/harvest/workspace.go` recipe). `agent/outcomewatcher` is the RunnableComponent (polling only — never notify-only, per the fork lesson) that creates outcome rows from harvest run-metrics, detects merges (reachability first, patch-id squash fallback), and walks the ticket lifecycle **exclusively through ticket-core's `tickets.Store.Transition`**. Four HTTP routes (`GetAgentTicketOutcome`, `SetAgentTicketDisposition`, `GetAgentTicketDiff`, `GetAgentTicketReviews`) feed the Elm PR view, which reuses `Build/AgentReview.elm` and `Concourse/AgentReview.elm` verbatim for the evidence panel and six-verdict feedback.

**Tech Stack:** Go (plain `testing` for `agent/api/outcomes` matching `agent/api/reviews`; Ginkgo/Gomega for `agent/gitcheck`, `agent/outcomewatcher`, `atc/db`, `atc/wrappa`, `atc/api/auth` matching those suites), PostgreSQL migration + factory recipe (`agent_reviews_factory.go`), real-git fixture tests (the `agent/harvest/workspace_test.go` recipe), counterfeiter fakes, jessevdk/go-flags (fly), Elm 0.19 + elm-test.

---

## Context

**Charter (workstreams.json id `delivery-outcomes`, size L, wave 4, depends_on: ticket-core, harvest-step, credentials-and-budgets).**

Scope-in → task mapping (every item maps):

| scope_in item | Tasks |
|---|---|
| Elm ticket PR view: paginated diff, review-evidence panel reusing `Build/AgentReview.elm`, judge score, plan-task progress, cost from ledger rollups, six-verdict finding feedback | 8 (diff API), 9 (reviews-by-ticket API), 13 (Elm data layer), 14 (Elm PR view), 15 (six-verdict wiring) |
| Ticket-level disposition UI: sent-back / abandoned + reason taxonomy + free text (+ `concluded`, 2026-07-09 flow-decoupling amendment — FLOWS.md §4) | 3 (taxonomy), 5 (disposition + outcome API), 13, 14 (UI) |
| `agent_outcomes` migration + factory — nullable-join schema agreed with scorecards at wave start | 1 (wave-start agreement addendum), 2 (migration), 3 (types), 4 (factory) |
| Outcome watcher RunnableComponent (open item 8): polite configurable polling, branch-head-reachable-from-default for merged, pre-merge human-commit detection for merged-with-fixes, human-touch delta with patch-id fallback for squash merges + documented v1 limits, native repo checking, no webhooks | 1 (heuristics frozen in writing), 6 (git machinery), 7 (git detection logic), 10 (watcher), 11 (component wiring) |
| Outcome states flow into the ticket lifecycle exclusively via the transition function | 5, 10 (both call `tickets.Store.Transition`; tests assert no other state writes), 12 (fly disposition still goes through the API → transition) |

**Scope OUT (do not implement):** auto-merge (never); Jira status sync (phase 2, rides ticket-core's `origin`/`external_ref` seam); finding analytics/aggregation (process-intel-experiments); scorecard rollups (scorecards reads `agent_outcomes` via the nullable joins this plan lands, in its own workstream).

**Prior waves (assumed LANDED exactly as 00-shared-contracts.md + the earlier plans' §11 addenda define):**

- **agent-identity** (wave 1): `atc/api/auth.CheckAgentAuthorizationHandler(handler http.Handler, rejector Rejector) http.Handler` — the wrappa case group giving team-less `/api/v1/agent/*` `authorized` routes real main-team viewer/member authorization (contracts decision 21); the five pre-existing agent feedback routes plus every wave-2/3 team-less `authorized` route already sit in that case group. `accessor.GetAccessor(r).Claims().UserName` (a struct field) resolves the acting human; handlers avoid importing `atc/api/accessor` (cycle through `atc/db`) by taking an injected `UserNameFunc` wired in `atc/api/handler.go`.
- **ticket-core** (wave 2): package `agent/api/tickets` — `Ticket` (fields incl. `Repo`, `Branch`, `TargetBranch`, `State`, `UserName`, `WorkflowName`, `BudgetUSD *float64`), `State` constants (`StateNeedsReview`, `StateMerged`, `StateMergedWithFixes`, `StateSentBack`, `StateAbandoned`, `StateConcluded`, `StateRunning`, … — `StateConcluded` is the 2026-07-09 flow-decoupling addition (FLOWS.md §4): the lifecycle enum is frozen up front, so the neutral spike/research terminal "run finished, human reviewed, no merge intended" lands in ticket-core NOW rather than via a later migration; it is a positive sibling of `StateAbandoned`, reachable only from `needs_review` via explicit human disposition), `TicketDetail{Ticket, Spec *Spec, Tasks []Task}`, `Task{Ordering int; Title, Detail string; Status TaskStatus}`, `ListFilter{State State; Repo, Origin string; Limit int}`, `TransitionMeta{PipelineRunID *int; Branch string; ErrorDetail string}` (**NO `By` field** — frozen by ticket-core addendum §2.1.1), errors `ErrInvalidTransition`/`ErrStaleTransition`/`ErrTicketNotFound`, func `ValidTransition(from, to State) bool`, `Store` interface (with `Transition` as the ONLY state writer), `MemoryStore`, counterfeiter fake `ticketsfakes.FakeStore`; `atc/db.NewAgentTicketsFactory(dbConn)` implementing `tickets.Store` (dbfakes `FakeAgentTicketsFactory`); `UserNameFunc func(r *http.Request) string`; the Elm ticket page `web/elm/src/AgentTickets/AgentTicket.elm` (patterned on `AgentReviews/AgentReviews.elm`), data layer `web/elm/src/Concourse/AgentTicket.elm`, endpoint `Api.Endpoints.AgentTicket`, effect `Effects.FetchAgentTicket Int`, callback `Callback.AgentTicketFetched`, route `/agent-tickets/:id`, a 5s-polling task list. State machine (§1.7): `needs_review → merged | merged_with_fixes | sent_back | abandoned | concluded`. Transition side effects (addendum §2.1.1): → any terminal outcome state stamps `completed_at=now()` (including `concluded`).
- **harvest-step** (wave 3): `agent_reviews`/`agent_feedback` ticket linkage columns (migration `1773106080`) with `reviews.StoredReview.TicketID *int`/`PipelineRunID *int` and `reviews.Store.ListByTicket(ticketID int) ([]StoredReview, error)`; `agent/api/reviews.BuildReviewResponse` (embeds `StoredReview` + `ProvenIssues`/`Observations []Finding` + `Feedback map[string]FindingFeedback` + `FindingCount int`); the harvest step transitions tickets `running → needs_review` with `TransitionMeta{Branch: "agent/ticket-<id>"}` on gates-ok, and upserts an `agent_run_metrics` row whose `results.json` `metadata` carries `{"pushed_branch","head_sha","base_sha","gates","judge",...}` (addendum §2.8.1); judge findings land in `agent_reviews.review` as `observations` with `category:"judge"`, gate failures as `proven_issues` with `category:"gate"`, feedback on judge findings submitted with `finding_type:"judge"` (§6.4.1 — the mapping this plan's UI wires); git identity `concourse-agent[bot]` (§8.3, decision 18); `agent/harvest/workspace.go` git helpers (`HeadSHA`, `BaseSHA`, `Diff(dir, base, maxBytes)`, `DiffTruncatedMarker`, `ChangedPaths`, `BuildManifest`) as the real-git-fixture-test recipe.
- **agent-step** (wave 2): `agent/schema` nested module (`schema.RunMetrics` with `Results json.RawMessage`, `StepName`, `TicketID *int`; `schema.Results` with `Metadata map[string]interface{}`); `agent/api/metrics.Store.ListByTicket(ticketID int) ([]schema.RunMetrics, error)`; `atc/db.NewAgentRunMetricsFactory(dbConn)`.
- **credentials-and-budgets** (wave 1): `GetAgentCostRollup` route `GET /api/v1/agent/costs?group_by=ticket` (authorized viewer) returning cost rollup rows — the cost display on the PR view calls this existing route via the existing `fly agent costs` / Elm cost endpoint; **no new backend cost code in this plan**.

**Wave-mates (parallel, NOT landed):**
- **scorecards** — consumes `agent_outcomes` via nullable LEFT JOINs on `ticket_id`. Task 1 writes the wave-start schema agreement (delta unit, additive columns) it builds against; neither workstream blocks the other because the join is nullable-by-design.
- **dispatch** — no shared files except additive merges in `atc/routes.go` / `atc/wrappa/api_auth_wrappa.go` / `atc/api/accessor/roles.go` / `atc/api/handler.go` / `atc/atccmd/command.go` and the `jetbridgeHeadMigration` const (higher migration number wins on merge — mine is `1773106090`, dispatch owns no migration).

**This plan PRODUCES (contract surface `agent-outcomes-schema`, §9 index rows §1.11 + §2.5):**
- 00-shared-contracts.md **§1.11 `agent_outcomes`** — DDL as written plus the additive deltas declared in Task 1's addendum §1.11.1 (`base_sha` column, `agent_outcomes_open` partial index).
- 00-shared-contracts.md **§2.5 Outcome** — `agent/api/outcomes` types + `Store` interface (additive fields `BaseSha`, `CreatedAt`, `UpdatedAt`), `atc/db.NewAgentOutcomesFactory`.
- 00-shared-contracts.md **§4.2** rows `SetAgentTicketDisposition`, `GetAgentTicketOutcome` (already in the table, owned by delivery-outcomes) plus two additive rows declared in Task 1: `GetAgentTicketDiff`, `GetAgentTicketReviews`.
- The outcome-watcher heuristics document (§1.11.1) scorecards and process-intel-experiments read before trusting `merge_state` / delta columns.

**This plan CONSUMES:**
- **§1.7 / §2.1 Ticket tables / Ticket** — `tickets.Store.Transition` (single-writer discipline), state machine, `Ticket.TargetBranch`/`Branch`/`Repo`/`UserName`.
- **§1.10 / §2.8.1 / §6.4.1** (harvest) — `reviews.Store.ListByTicket`, `reviews.BuildReviewResponse`, results-metadata sha conventions, judge/gate finding categories, `finding_type:"judge"` feedback convention.
- **§1.8 / §2.4** (agent-step) — `agent/api/metrics.Store.ListByTicket` + `schema.RunMetrics.Results`.
- **§4.2 `GetAgentCostRollup`** (credentials-and-budgets) — ticket cost rollup for the PR view (reused, not re-implemented).
- **§4.1/§4.2 + decision 21** (agent-identity) — `CheckAgentAuthorizationHandler` tier for all four routes; `UserNameFunc` injection.
- **§8.3 / decision 18** — bot git identity and the human-touch-delta definition.

**Anchor caveat:** `Modify:` line anchors were verified on branch `jetbridge` at head `fb1c54fac2` (pre-wave-1). Three prior waves will have shifted every anchor in `atc/routes.go`, `atc/wrappa/api_auth_wrappa.go`, `atc/api/accessor/roles.go`, `atc/api/handler.go`, `atc/api/api_suite_test.go`, `atc/atccmd/command.go`, and the Elm `SubPage`/`Routes`/`Endpoints`/`Effects` files. Treat anchors as "the location of the quoted code"; place additions adjacent to the ticket-core / harvest-step / agent-step additions named in each step (search for the named symbol).

---

### Task 1: Wave-start contract addendum — agent_outcomes schema agreement with scorecards, watcher heuristics, route additions

The charter requires the nullable-join schema "agreed with scorecards at wave start" and the delta definition "fixed before scorecards consume it". Freeze all of it in writing in the contracts doc, where scorecards' planner reads it, before any code.

**Files:**
- Modify: `docs/superpowers/plans/agentic-platform/00-shared-contracts.md` (insert `### 1.11.1` immediately after §1.11's closing paragraph, before `### 1.12`; append two rows to the §4.2 route table after the `GetAgentTicketOutcome` row; append to the §11 Amendment log at the end of the file)

**Steps:**

- [ ] Insert the following subsection immediately after §1.11's closing paragraph ("Explicit dispositions (`sent_back`/`abandoned`/`concluded`) live here …", before `### 1.12 Experiment substrate`):

````markdown
### 1.11.1 agent_outcomes wave-4 addendum — owner: **delivery-outcomes** (2026-07-08; sign-off consumers: scorecards, process-intel-experiments)

**Schema agreement (the "agreed at wave start" join contract):** scorecards join `agent_outcomes` on `ticket_id` (unique) with LEFT JOINs — every column readable as written in §1.11, no view layer. Additive deltas from the §1.11 DDL, allowed under the "field lists may grow" rule (§2 preamble):

- `base_sha TEXT NOT NULL DEFAULT ''` — the gate-diff base recorded by harvest (`results.json` `metadata.base_sha`, §2.8.1). Needed because after a merge, `merge-base(pushed_sha, default)` degenerates toward `pushed_sha`, so the PR-view diff must pin the pre-merge base.
- `CREATE INDEX agent_outcomes_open ON agent_outcomes (merge_state) WHERE merge_state = 'open';` — the watcher's work-list scan.
- `Outcome` (§2.5) gains `BaseSha string \`json:"base_sha,omitempty"\``, `CreatedAt int64`, `UpdatedAt int64` (epoch seconds, matching every other agent API type).
- **[2026-07-09 flow-decoupling]** `merge_state` CHECK gains `'concluded'`; `disposition` CHECK gains `'concluded'`; `disposition_reason` CHECK gains `'research_complete'` (taxonomy meaning: spike/research complete, no merge intended); `MergeState` (§2.5) gains `MergeConcluded = "concluded"`. Landed now, alongside ticket-core's `concluded` lifecycle state (FLOWS.md §4: the enums are frozen up front — deciding this later means a migration). *(2026-07-09 verifier follow-up: all four deltas are also amended in place in contracts §1.11/§2.5 with dated comments, so contracts is self-consistent before wave start; this bullet remains the authoritative record of the change.)*

**Human-touch delta definition [CONFIRMED — lines, not hunks; supersedes §1.11's prose only in unit precision]:** `human_lines_added`/`human_lines_deleted` are summed `git show --numstat` line counts of commits in `pushed_sha..<tip-at-merge>` (first-parent walk, merge commits excluded) whose author name is not `concourse-agent[bot]` (§8.3). `human_commit_count` counts those commits. `merged_with_fixes ⇔ human_commit_count > 0`. Binary files numstat as `-`/`-` and count 0 lines. Scorecards MUST label the columns "lines" in any UI.

**Outcome-row creation:** rows are created by the outcome watcher (NOT harvest). Each tick it scans tickets in `needs_review` with a non-empty `branch` and no outcome row, resolving `pushed_sha`/`base_sha` from the newest `agent_run_metrics` row for that ticket whose `results.metadata.pushed_branch` equals the ticket branch. Fallback when no metrics row carries the shas (e.g. metrics reaped): `pushed_sha` = remote branch head at first sync — weaker, because pre-existing human commits then dilute the delta baseline; `base_sha` stays `''` and the diff API returns 404 until it is known. While `merge_state = 'open'`, a re-push refreshes `branch`/`pushed_sha`/`base_sha` in place. A send-back disposition drives the row to `closed_unmerged`; when the ticket later cycles `sent_back → queued → running → needs_review` and is re-dispatched, the watcher's `Ensure` **re-arms** that row — a row with `merge_state = 'closed_unmerged' AND disposition = 'sent_back'` is reset to `open` with fresh `branch`/`pushed_sha`/`base_sha` and its disposition fields cleared, so the re-worked branch's eventual human merge is still detected (F6). Truly terminal rows — `merged`/`merged_with_fixes`, `closed_unmerged` via `abandoned`, or `concluded` — are never refreshed. A `concluded` disposition ("run finished, human reviewed, no merge intended" — spike/research flows, FLOWS.md §3) closes the row as `merge_state = 'concluded'`: the watcher **skips merge-detection entirely** for concluded tickets — the row leaves the open work-list permanently, is never re-armed, and never waits on (or reacts to) a branch merge, even if a human later merges the spike branch anyway.

**Merge-detection heuristics v1 [DECIDED HERE]:**
1. **Primary (true merges + fast-forward):** merged ⇔ `git merge-base --is-ancestor <pushed_sha> refs/heads/<target_branch>` in a `--mirror` clone. `merged_sha` = the oldest merge commit on `git rev-list --ancestry-path --merges <pushed_sha>..<target>` (the commit that brought the branch in); tip-at-merge = that merge commit's second parent when it descends from `pushed_sha`, else `pushed_sha`. Fast-forward (no merge commit on the ancestry path): `merged_sha` = tip-at-merge = the agent branch's remote head if the branch still exists, else `pushed_sha`.
2. **Squash fallback:** when not an ancestor, compute `git patch-id --stable` of the single combined patch `base_sha..<branch tip>` and compare against the patch-ids of the newest N first-parent commits on the target branch (N = `--agent-outcome-squash-scan-limit`, default 200). A match ⇒ merged with `merged_sha` = the matching squash commit; human delta still computed from the agent branch (`pushed_sha..branch head`).
3. **Documented v1 limits:** rebase-merges and squash-merges whose content was edited during merge produce no patch-id match — the outcome stays `open` until a human sets a disposition (the honest answer; never guess). Branch deletion without merge is NOT auto-closed. `closed_unmerged` is set **only** by an explicit disposition (`sent_back`/`abandoned`) in v1; a `concluded` disposition closes the row as `concluded` instead, and merge-detection is skipped for it from that point on. `--is-ancestor` cannot distinguish "merged then reverted"; a revert is human-touch on the *default* branch, out of delta scope by definition (delta measures the agent *branch*).

**Concluded is not a failure [DECIDED HERE, 2026-07-09]:** `merge_state = 'concluded'` (disposition `concluded`, reason `research_complete` unless a better taxonomy fit applies) marks a *successful* delivery whose deliverable is findings, not a merge — spike/research flows (FLOWS.md §3 spike-research). Scorecards MUST exclude `concluded` rows from merge-rate denominators and MUST NOT bucket them with `closed_unmerged` failures; count them as a separate positive outcome class (a finished spike rotting in `needs_review` and dragging down merge rate is exactly the anti-pattern this state removes).

**Watcher & diff configuration [DECIDED HERE]:** web-node flags, all under one group: `--agent-outcome-git-dir` (bare-mirror cache directory; **empty disables both the watcher and the diff API** — the master switch), `--agent-outcome-git-url-template` (default `https://github.com/{repo}.git`; `{repo}` is the canonical slug), `--agent-outcome-git-username` / `--agent-outcome-git-token` (optional fetch credentials, https-only, injected via a temp credential-store file exactly like harvest push §8.3 — never argv), `--agent-outcome-check-interval` (default `5m`; the component's polling interval — "polite" = one `git fetch --prune` per repo per tick regardless of ticket count), `--agent-outcome-squash-scan-limit` (default 200). Component name: `agent_outcome_watcher`. Ticket state changes ride the transition function, so the §8.4 webhook fires for merge outcomes with no extra code here.

**Route additions (extend the §4.2 table):**

| Route name | Method | Path | Auth tier | Owner |
|---|---|---|---|---|
| `GetAgentTicketDiff` | GET | `/api/v1/agent/tickets/:ticket_id/diff` (`?offset=&limit=`) | authorized viewer | delivery-outcomes |
| `GetAgentTicketReviews` | GET | `/api/v1/agent/tickets/:ticket_id/reviews` | authorized viewer | delivery-outcomes |

`GetAgentTicketDiff` serves file-windowed unified diffs (`base_sha..pushed_sha`) from the watcher's mirror cache (default window 50 files, per-file patch cap 64 KiB, `has_more` paging) — the ATC never renders unbounded diffs. `GetAgentTicketReviews` returns exactly the `GetBuildAgentReviews` response shape (`[]reviews.BuildReviewResponse`) for rows matched by `agent_reviews.ticket_id`, so the existing Elm evidence decoder (`Concourse.AgentReview.decodeBuildReview`) is reused unchanged.
````

- [ ] Append two rows to the §4.2 route table, immediately after the `GetAgentTicketOutcome` row:

```markdown
| `GetAgentTicketDiff` | GET | `/api/v1/agent/tickets/:ticket_id/diff` | authorized viewer | delivery-outcomes (addendum §1.11.1) |
| `GetAgentTicketReviews` | GET | `/api/v1/agent/tickets/:ticket_id/reviews` | authorized viewer | delivery-outcomes (addendum §1.11.1) |
```

- [ ] Append to the §11 Amendment log (at the end of the file, after the harvest-step planning entry):

```markdown
- 2026-07-08 (delivery-outcomes planning): added §1.11.1 — wave-start agent_outcomes agreement with scorecards (LEFT-JOIN-on-ticket_id contract; delta unit = LINES via numstat of non-bot first-parent commits; additive base_sha column + agent_outcomes_open partial index; Outcome gains BaseSha/CreatedAt/UpdatedAt), outcome-row creation from harvest run-metrics with branch-head fallback, merge-detection heuristics v1 (ancestor primary, patch-id squash fallback, documented limits: edited squashes/rebase-merges stay open; closed_unmerged only via disposition), watcher/diff web flags (--agent-outcome-git-dir master switch, url template, https-only creds, 5m interval, squash scan limit), and two additive §4.2 routes GetAgentTicketDiff / GetAgentTicketReviews (windowed diff; reviews-by-ticket in the GetBuildAgentReviews response shape). Affects: scorecards, process-intel-experiments.
- 2026-07-09 (delivery-outcomes planning, F6 fix): clarified the §1.11.1 outcome-row lifecycle for the send-back → re-dispatch → merge loop. A send-back disposition sets `closed_unmerged`, but the state machine allows `sent_back → queued → running → needs_review`; on re-dispatch the watcher's `Store.Ensure` now RE-ARMS a row where `merge_state = 'closed_unmerged' AND disposition = 'sent_back'` back to `open` with fresh branch/pushed_sha/base_sha (disposition fields cleared), so the re-worked branch's eventual human merge is recorded (previously the merge — and the human-touch delta, spec §9 — was silently lost on the rework loop). `abandoned` and `merged`/`merged_with_fixes` rows remain terminal and are never re-armed. No schema change (Ensure ON CONFLICT WHERE broadened only). Affects: scorecards, process-intel-experiments.
- 2026-07-09 (delivery-outcomes planning, flow-decoupling: CONCLUDED): added the neutral terminal outcome `concluded` per the owner-approved FLOWS.md §4 decision ("run finished, human reviewed, no merge intended" — spike/research flows; a positive sibling of `abandoned`, reachable only via explicit human disposition from `needs_review`). §1.11.1 changes: `merge_state`/`disposition` CHECKs gain `'concluded'`, `disposition_reason` gains `'research_complete'`; a `concluded` disposition closes the row as `merge_state='concluded'`; the watcher skips merge-detection for concluded tickets (never re-armed, never waits on a branch merge); scorecard rule: `concluded` is NOT a failure — exclude from merge-rate denominators, never bucket with `closed_unmerged`. Rides ticket-core's same-day `StateConcluded` enum addition (`needs_review → concluded`, stamps `completed_at`) — landed now because the lifecycle enum is frozen up front (later = migration). Affects: scorecards, process-intel-experiments, ticket-core.
```

- [ ] Commit: `git add docs/superpowers/plans/agentic-platform/00-shared-contracts.md && git commit -m "docs(agentic): delivery-outcomes contract addendum - outcomes schema agreement, watcher heuristics, diff/reviews routes"`

---

### Task 2: Migration 1773106090 — `agent_outcomes`

**Files:**
- Create: `atc/db/migration/migrations/1773106090_create_agent_outcomes.up.sql`
- Create: `atc/db/migration/migrations/1773106090_create_agent_outcomes.down.sql`
- Modify: `atc/db/migration/legacy_upgrade_test.go:37` (`jetbridgeHeadMigration` const — pre-wave value `1773105504`; by wave 4 it reads `1773106080` from harvest-step)

**Steps:**

- [ ] Write `atc/db/migration/migrations/1773106090_create_agent_outcomes.up.sql` — §1.11 DDL verbatim plus the §1.11.1 additive deltas (`base_sha`, partial index, and the 2026-07-09 `concluded`/`research_complete` enum additions). Migration files are picked up automatically via `go:embed migrations` (`atc/db/migration/migration.go`) — no registration code:

```sql
CREATE TABLE agent_outcomes (
    id                  SERIAL PRIMARY KEY,
    ticket_id           INTEGER NOT NULL,
    repo                TEXT NOT NULL,
    branch              TEXT NOT NULL,
    pushed_sha          TEXT NOT NULL DEFAULT '',
    base_sha            TEXT NOT NULL DEFAULT '',
    merge_state         TEXT NOT NULL DEFAULT 'open'
                        CHECK (merge_state IN ('open','merged','merged_with_fixes','closed_unmerged',
                                               'concluded')),
    merged_sha          TEXT NOT NULL DEFAULT '',
    merged_at           TIMESTAMPTZ,
    human_commit_count  INTEGER NOT NULL DEFAULT 0,
    human_lines_added   INTEGER NOT NULL DEFAULT 0,
    human_lines_deleted INTEGER NOT NULL DEFAULT 0,
    disposition         TEXT NOT NULL DEFAULT ''
                        CHECK (disposition IN ('','sent_back','abandoned','concluded')),
    disposition_reason  TEXT NOT NULL DEFAULT ''
                        CHECK (disposition_reason IN ('','wrong_approach','incomplete','defective',
                                                      'superseded','not_needed','style',
                                                      'research_complete','other')),
    disposition_notes   TEXT NOT NULL DEFAULT '',
    disposed_by         TEXT NOT NULL DEFAULT '',
    last_checked_at     TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (ticket_id)
);

CREATE INDEX agent_outcomes_open ON agent_outcomes (merge_state) WHERE merge_state = 'open';
```

- [ ] Write `atc/db/migration/migrations/1773106090_create_agent_outcomes.down.sql`:

```sql
DROP TABLE agent_outcomes;
```

- [ ] In `atc/db/migration/legacy_upgrade_test.go:37`, set the head const to `1773106090` **only if the current value is lower** (never lower it — dispatch owns no migration, so `1773106090` is the wave-4 head):

```go
const jetbridgeHeadMigration = 1773106090
```

- [ ] Run to verify: `pg_isready && ginkgo --focus='Legacy Database Upgrade' ./atc/db/migration/` — expect green (the suite migrates empty + fixture DBs to HEAD; a SQL syntax error, a missing down file, or a stale head const fails here).
- [ ] Commit: `git add atc/db/migration && git commit -m "feat(delivery-outcomes): migration 1773106090 - agent_outcomes table"`

---

### Task 3: `agent/api/outcomes` — domain types, taxonomy validation, MemoryStore

Package path is binding per contracts §2.5 (`agent/api/outcomes/types.go`). Plain `testing` tests, matching `agent/api/reviews`.

**Files:**
- Create: `agent/api/outcomes/types.go`
- Create: `agent/api/outcomes/memory_store.go`
- Test: `agent/api/outcomes/types_test.go`

**Steps:**

- [ ] Write the failing test `agent/api/outcomes/types_test.go`:

```go
package outcomes_test

import (
	"testing"

	"github.com/concourse/concourse/agent/api/outcomes"
)

func TestDispositionValidation(t *testing.T) {
	if !outcomes.ValidDisposition("sent_back") || !outcomes.ValidDisposition("abandoned") {
		t.Error("sent_back and abandoned must be valid dispositions")
	}
	if !outcomes.ValidDisposition("concluded") {
		t.Error("concluded must be a valid disposition (flow-decoupling 2026-07-09)")
	}
	if outcomes.ValidDisposition("") || outcomes.ValidDisposition("merged") {
		t.Error("empty and merged must be invalid dispositions")
	}
	for _, r := range []string{"wrong_approach", "incomplete", "defective", "superseded", "not_needed", "style", "research_complete", "other"} {
		if !outcomes.ValidDispositionReason(r) {
			t.Errorf("reason %q must be valid", r)
		}
	}
	if outcomes.ValidDispositionReason("") || outcomes.ValidDispositionReason("meh") {
		t.Error("empty and unknown reasons must be invalid")
	}
}

func TestMemoryStoreEnsureRefreshesOnlyOpenRows(t *testing.T) {
	s := outcomes.NewMemoryStore()

	if err := s.Ensure(&outcomes.Outcome{TicketID: 7, Repo: "tdmtrader/concourse", Branch: "agent/ticket-7", PushedSha: "aaa", BaseSha: "bbb"}); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.Get(7)
	if err != nil || !found {
		t.Fatalf("get after ensure: %v found=%v", err, found)
	}
	if got.MergeState != outcomes.MergeOpen || got.PushedSha != "aaa" {
		t.Fatalf("fresh row: %+v", got)
	}

	// open rows refresh branch/pushed/base on re-push
	if err := s.Ensure(&outcomes.Outcome{TicketID: 7, Repo: "tdmtrader/concourse", Branch: "agent/ticket-7", PushedSha: "ccc", BaseSha: "ddd"}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(7)
	if got.PushedSha != "ccc" || got.BaseSha != "ddd" {
		t.Fatalf("open row must refresh shas: %+v", got)
	}

	// merged rows do NOT refresh
	if err := s.RecordMerge(7, outcomes.MergeResult{State: outcomes.Merged, MergedSha: "eee"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Ensure(&outcomes.Outcome{TicketID: 7, Repo: "tdmtrader/concourse", Branch: "agent/ticket-7", PushedSha: "zzz"}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(7)
	if got.PushedSha != "ccc" {
		t.Fatalf("merged row must not refresh: %+v", got)
	}

	// F6: a send-back row (closed_unmerged + disposition='sent_back') is
	// RE-ARMED by Ensure — reset to open with fresh shas and cleared
	// disposition — so the re-dispatch loop's merge is detected.
	if err := s.Ensure(&outcomes.Outcome{TicketID: 8, Repo: "r", Branch: "agent/ticket-8", PushedSha: "p1", BaseSha: "b1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDisposition(8, outcomes.DispositionInput{Disposition: outcomes.DispositionSentBack, Reason: "incomplete", By: "u"}); err != nil {
		t.Fatal(err)
	}
	if g, _, _ := s.Get(8); g.MergeState != outcomes.ClosedUnmerged {
		t.Fatalf("send-back must close the row: %+v", g)
	}
	if err := s.Ensure(&outcomes.Outcome{TicketID: 8, Repo: "r", Branch: "agent/ticket-8", PushedSha: "p2", BaseSha: "b2"}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(8)
	if got.MergeState != outcomes.MergeOpen || got.PushedSha != "p2" || got.BaseSha != "b2" || got.Disposition != "" {
		t.Fatalf("sent_back row must re-arm to open with fresh shas + cleared disposition: %+v", got)
	}

	// An abandoned closed row is terminal — Ensure must NOT re-arm it.
	if err := s.Ensure(&outcomes.Outcome{TicketID: 9, Repo: "r", Branch: "agent/ticket-9", PushedSha: "q1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDisposition(9, outcomes.DispositionInput{Disposition: outcomes.DispositionAbandoned, Reason: "wont_do", By: "u"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Ensure(&outcomes.Outcome{TicketID: 9, Repo: "r", Branch: "agent/ticket-9", PushedSha: "q2"}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(9)
	if got.MergeState != outcomes.ClosedUnmerged || got.PushedSha != "q1" {
		t.Fatalf("abandoned row must stay closed and not refresh: %+v", got)
	}
}

func TestMemoryStoreConcludedDisposition(t *testing.T) {
	s := outcomes.NewMemoryStore()
	_ = s.Ensure(&outcomes.Outcome{TicketID: 3, Repo: "r", Branch: "agent/ticket-3", PushedSha: "p1", BaseSha: "b1"})

	// a concluded disposition closes the open row as 'concluded', NOT
	// closed_unmerged — it is a positive terminal, not a failure.
	if err := s.SetDisposition(3, outcomes.DispositionInput{
		Disposition: outcomes.DispositionConcluded, Reason: "research_complete",
		Notes: "findings in ticket body", By: "tdmtrader",
	}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Get(3)
	if got.MergeState != outcomes.MergeConcluded || got.Disposition != outcomes.DispositionConcluded {
		t.Fatalf("concluded must close the row as concluded: %+v", got)
	}

	// concluded is terminal: Ensure must NOT re-arm it (unlike sent_back).
	if err := s.Ensure(&outcomes.Outcome{TicketID: 3, Repo: "r", Branch: "agent/ticket-3", PushedSha: "p2", BaseSha: "b2"}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(3)
	if got.MergeState != outcomes.MergeConcluded || got.PushedSha != "p1" || got.Disposition != outcomes.DispositionConcluded {
		t.Fatalf("concluded row must stay terminal, keep its shas, and keep its disposition: %+v", got)
	}

	// and it leaves the watcher's open work-list permanently.
	open, err := s.ListOpen()
	if err != nil || len(open) != 0 {
		t.Fatalf("concluded row must leave ListOpen: %v %v", open, err)
	}
}

func TestMemoryStoreMergeAndDisposition(t *testing.T) {
	s := outcomes.NewMemoryStore()
	if err := s.RecordMerge(1, outcomes.MergeResult{State: outcomes.Merged}); err != outcomes.ErrOutcomeNotFound {
		t.Fatalf("merge on missing row: got %v", err)
	}
	_ = s.Ensure(&outcomes.Outcome{TicketID: 1, Repo: "r", Branch: "b", PushedSha: "p"})

	if err := s.RecordMerge(1, outcomes.MergeResult{State: outcomes.MergeOpen}); err == nil {
		t.Fatal("RecordMerge must reject a non-merged target state")
	}
	if err := s.RecordMerge(1, outcomes.MergeResult{
		State: outcomes.MergedWithFixes, MergedSha: "m",
		HumanCommitCount: 2, HumanLinesAdded: 10, HumanLinesDeleted: 3,
	}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Get(1)
	if got.MergeState != outcomes.MergedWithFixes || got.HumanCommitCount != 2 || got.MergedAt == 0 {
		t.Fatalf("merge fields not recorded: %+v", got)
	}
	if err := s.RecordMerge(1, outcomes.MergeResult{State: outcomes.Merged}); err != outcomes.ErrNotOpen {
		t.Fatalf("second merge: got %v, want ErrNotOpen", err)
	}

	open, err := s.ListOpen()
	if err != nil || len(open) != 0 {
		t.Fatalf("merged row must leave ListOpen: %v %v", open, err)
	}

	// disposition on an open row closes it
	_ = s.Ensure(&outcomes.Outcome{TicketID: 2, Repo: "r", Branch: "b2"})
	if err := s.SetDisposition(2, outcomes.DispositionInput{
		Disposition: "sent_back", Reason: "incomplete", Notes: "missing tests", By: "tdmtrader",
	}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(2)
	if got.MergeState != outcomes.ClosedUnmerged || got.Disposition != "sent_back" || got.DispositionReason != "incomplete" || got.DisposedBy != "tdmtrader" {
		t.Fatalf("disposition not recorded: %+v", got)
	}

	// disposition on a merged row keeps merge_state
	if err := s.SetDisposition(1, outcomes.DispositionInput{Disposition: "abandoned", Reason: "other", By: "x"}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(1)
	if got.MergeState != outcomes.MergedWithFixes {
		t.Fatalf("disposition must not overwrite a terminal merge_state: %+v", got)
	}

	if err := s.Touch(2); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(2)
	if got.LastCheckedAt == 0 {
		t.Fatal("Touch must set last_checked_at")
	}
}
```

- [ ] Run `go test ./agent/api/outcomes/` — expect compile failure (package does not exist).
- [ ] Write `agent/api/outcomes/types.go`:

```go
// Package outcomes holds the delivery-outcome domain types (shared-contracts
// §1.11/§1.11.1/§2.5): merge facts recorded by the outcome watcher and
// ticket-level dispositions recorded by humans.
package outcomes

import "errors"

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

type MergeState string

const (
	MergeOpen       MergeState = "open"
	Merged          MergeState = "merged"
	MergedWithFixes MergeState = "merged_with_fixes"
	ClosedUnmerged  MergeState = "closed_unmerged"
	// MergeConcluded is the neutral terminal (flow-decoupling 2026-07-09,
	// FLOWS.md §4): run finished, human reviewed, no merge intended —
	// spike/research flows. Set only by a 'concluded' disposition; never
	// re-armed, never merge-scanned, and NOT a failure (scorecards exclude
	// it from merge-rate denominators, §1.11.1).
	MergeConcluded MergeState = "concluded"
)

// BotAuthor is the platform git identity (§8.3). Commits with this author
// are excluded from the human-touch delta (decision 18).
const BotAuthor = "concourse-agent[bot]"

const (
	DispositionSentBack  = "sent_back"
	DispositionAbandoned = "abandoned"
	// DispositionConcluded is the positive sibling of abandoned: the run is
	// done and reviewed, and no merge was ever intended (spike/research).
	// It maps to the needs_review → concluded lifecycle edge.
	DispositionConcluded = "concluded"
)

// DispositionReasons is the §1.11 reason taxonomy, in display order.
// research_complete: spike/research complete, no merge intended (pairs
// with the 'concluded' disposition).
var DispositionReasons = []string{
	"wrong_approach", "incomplete", "defective",
	"superseded", "not_needed", "style", "research_complete", "other",
}

func ValidDisposition(d string) bool {
	return d == DispositionSentBack || d == DispositionAbandoned || d == DispositionConcluded
}

func ValidDispositionReason(r string) bool {
	for _, v := range DispositionReasons {
		if r == v {
			return true
		}
	}
	return false
}

var (
	ErrOutcomeNotFound = errors.New("agent outcome not found")
	ErrNotOpen         = errors.New("agent outcome is not open")
)

// Outcome mirrors agent_outcomes (§2.5 plus §1.11.1 additive fields).
// Timestamps are epoch seconds.
type Outcome struct {
	TicketID          int        `json:"ticket_id"`
	Repo              string     `json:"repo"`
	Branch            string     `json:"branch"`
	PushedSha         string     `json:"pushed_sha"`
	BaseSha           string     `json:"base_sha,omitempty"`
	MergeState        MergeState `json:"merge_state"`
	MergedSha         string     `json:"merged_sha,omitempty"`
	MergedAt          int64      `json:"merged_at,omitempty"`
	HumanCommitCount  int        `json:"human_commit_count"`
	HumanLinesAdded   int        `json:"human_lines_added"`
	HumanLinesDeleted int        `json:"human_lines_deleted"`
	Disposition       string     `json:"disposition,omitempty"`
	DispositionReason string     `json:"disposition_reason,omitempty"`
	DispositionNotes  string     `json:"disposition_notes,omitempty"`
	DisposedBy        string     `json:"disposed_by,omitempty"`
	LastCheckedAt     int64      `json:"last_checked_at,omitempty"`
	CreatedAt         int64      `json:"created_at,omitempty"`
	UpdatedAt         int64      `json:"updated_at,omitempty"`
}

// MergeResult is what the watcher records when it detects a merge.
type MergeResult struct {
	State             MergeState // Merged or MergedWithFixes only
	MergedSha         string
	HumanCommitCount  int
	HumanLinesAdded   int
	HumanLinesDeleted int
}

// DispositionInput is a human's explicit ticket-level verdict.
type DispositionInput struct {
	Disposition string // sent_back | abandoned | concluded
	Reason      string // §1.11 taxonomy
	Notes       string // free text
	By          string // username
}

// Store is the persistence contract, implemented by
// atc/db.NewAgentOutcomesFactory and MemoryStore.
//
//counterfeiter:generate . Store
type Store interface {
	// Ensure inserts the row if absent (unique ticket_id). When the row
	// exists and merge_state = 'open', it refreshes branch/pushed_sha/
	// base_sha (re-push during the same review). It also RE-ARMS a row
	// that a send-back disposition drove to closed_unmerged: when
	// merge_state = 'closed_unmerged' AND disposition = 'sent_back', the
	// row is reset to 'open' with fresh branch/pushed_sha/base_sha so the
	// re-dispatch loop's eventual human merge is detected (F6). Other
	// terminal rows (merged/merged_with_fixes, closed_unmerged via
	// 'abandoned', or concluded) are untouched.
	Ensure(o *Outcome) error
	Get(ticketID int) (*Outcome, bool, error)
	// ListOpen returns rows with merge_state = 'open', oldest-first.
	ListOpen() ([]Outcome, error)
	// RecordMerge moves an open row to merged/merged_with_fixes and
	// stamps merged_at. ErrNotOpen if the row is terminal,
	// ErrOutcomeNotFound if absent.
	RecordMerge(ticketID int, res MergeResult) error
	// SetDisposition records the human verdict; an open row's
	// merge_state becomes closed_unmerged ('concluded' when the
	// disposition is 'concluded'), terminal states are kept.
	SetDisposition(ticketID int, d DispositionInput) error
	// Touch stamps last_checked_at = now.
	Touch(ticketID int) error
}
```

- [ ] Write `agent/api/outcomes/memory_store.go`:

```go
package outcomes

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemoryStore is the in-memory Store used by handler/watcher tests.
type MemoryStore struct {
	mu   sync.Mutex
	rows map[int]*Outcome
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{rows: map[int]*Outcome{}}
}

func (s *MemoryStore) Ensure(o *Outcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.rows[o.TicketID]
	if !ok {
		cp := *o
		cp.MergeState = MergeOpen
		cp.CreatedAt = time.Now().Unix()
		cp.UpdatedAt = cp.CreatedAt
		s.rows[o.TicketID] = &cp
		return nil
	}
	// Re-arm a send-back row so the re-dispatch loop's merge is detected (F6):
	// a sent_back disposition drove this open row to closed_unmerged, but the
	// ticket has cycled sent_back → queued → running → needs_review again.
	reArm := existing.MergeState == ClosedUnmerged && existing.Disposition == DispositionSentBack
	if existing.MergeState == MergeOpen || reArm {
		if reArm {
			existing.MergeState = MergeOpen
			existing.Disposition = ""
			existing.DispositionReason = ""
			existing.DispositionNotes = ""
			existing.DisposedBy = ""
		}
		existing.Branch = o.Branch
		existing.PushedSha = o.PushedSha
		existing.BaseSha = o.BaseSha
		existing.UpdatedAt = time.Now().Unix()
	}
	return nil
}

func (s *MemoryStore) Get(ticketID int) (*Outcome, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.rows[ticketID]
	if !ok {
		return nil, false, nil
	}
	cp := *o
	return &cp, true, nil
}

func (s *MemoryStore) ListOpen() ([]Outcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Outcome
	for _, o := range s.rows {
		if o.MergeState == MergeOpen {
			out = append(out, *o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TicketID < out[j].TicketID })
	return out, nil
}

func (s *MemoryStore) RecordMerge(ticketID int, res MergeResult) error {
	if res.State != Merged && res.State != MergedWithFixes {
		return fmt.Errorf("invalid merge target state %q", res.State)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.rows[ticketID]
	if !ok {
		return ErrOutcomeNotFound
	}
	if o.MergeState != MergeOpen {
		return ErrNotOpen
	}
	o.MergeState = res.State
	o.MergedSha = res.MergedSha
	o.MergedAt = time.Now().Unix()
	o.HumanCommitCount = res.HumanCommitCount
	o.HumanLinesAdded = res.HumanLinesAdded
	o.HumanLinesDeleted = res.HumanLinesDeleted
	o.UpdatedAt = time.Now().Unix()
	return nil
}

func (s *MemoryStore) SetDisposition(ticketID int, d DispositionInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.rows[ticketID]
	if !ok {
		return ErrOutcomeNotFound
	}
	o.Disposition = d.Disposition
	o.DispositionReason = d.Reason
	o.DispositionNotes = d.Notes
	o.DisposedBy = d.By
	if o.MergeState == MergeOpen {
		if d.Disposition == DispositionConcluded {
			// positive terminal: no merge was ever intended (§1.11.1) —
			// the watcher skips merge-detection from here on.
			o.MergeState = MergeConcluded
		} else {
			o.MergeState = ClosedUnmerged
		}
	}
	o.UpdatedAt = time.Now().Unix()
	return nil
}

func (s *MemoryStore) Touch(ticketID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.rows[ticketID]
	if !ok {
		return ErrOutcomeNotFound
	}
	o.LastCheckedAt = time.Now().Unix()
	return nil
}
```

- [ ] Run `go test ./agent/api/outcomes/` — expect pass.
- [ ] Generate the counterfeiter fake consumers (the handler + watcher tests) use: `cd agent/api/outcomes && go run github.com/maxbrunsfeld/counterfeiter/v6 -o outcomesfakes/fake_store.go . Store && cd ../../..` — then verify `go build ./agent/...`.
- [ ] Commit: `git add agent/api/outcomes && git commit -m "feat(delivery-outcomes): agent/api/outcomes domain types, taxonomy, MemoryStore, fake"`

---

### Task 4: `atc/db` AgentOutcomesFactory implementing `outcomes.Store`

Follows the `agent_reviews_factory.go` recipe (squirrel `psql`, `ON CONFLICT` upsert, epoch scan). This is the persistence backing for the watcher and the API.

**Files:**
- Create: `atc/db/agent_outcomes_factory.go`
- Create: `atc/db/dbfakes/fake_agent_outcomes_factory.go` (generated)
- Test: `atc/db/agent_outcomes_factory_test.go`

**Steps:**

- [ ] Write the failing spec `atc/db/agent_outcomes_factory_test.go` (Ginkgo, matching `atc/db/agent_reviews_factory_test.go`; `dbConn` is the suite's shared connection):

```go
package db_test

import (
	"github.com/concourse/concourse/agent/api/outcomes"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentOutcomesFactory", func() {
	var factory db.AgentOutcomesFactory

	BeforeEach(func() {
		factory = db.NewAgentOutcomesFactory(dbConn)
		_, err := dbConn.Exec("DELETE FROM agent_outcomes")
		Expect(err).NotTo(HaveOccurred())
	})

	It("ensures, gets, and refreshes only open rows", func() {
		Expect(factory.Ensure(&outcomes.Outcome{
			TicketID: 501, Repo: "tdmtrader/concourse", Branch: "agent/ticket-501",
			PushedSha: "aaa", BaseSha: "bbb",
		})).To(Succeed())

		got, found, err := factory.Get(501)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got.MergeState).To(Equal(outcomes.MergeOpen))
		Expect(got.PushedSha).To(Equal("aaa"))
		Expect(got.CreatedAt).To(BeNumerically(">", 0))

		// re-push refreshes an open row
		Expect(factory.Ensure(&outcomes.Outcome{
			TicketID: 501, Repo: "tdmtrader/concourse", Branch: "agent/ticket-501",
			PushedSha: "ccc", BaseSha: "ddd",
		})).To(Succeed())
		got, _, _ = factory.Get(501)
		Expect(got.PushedSha).To(Equal("ccc"))
		Expect(got.BaseSha).To(Equal("ddd"))
	})

	It("records a merge, leaves ListOpen, and rejects a second merge", func() {
		Expect(factory.Ensure(&outcomes.Outcome{TicketID: 502, Repo: "r", Branch: "b", PushedSha: "p", BaseSha: "z"})).To(Succeed())

		open, err := factory.ListOpen()
		Expect(err).NotTo(HaveOccurred())
		Expect(open).To(HaveLen(1))

		Expect(factory.RecordMerge(502, outcomes.MergeResult{
			State: outcomes.MergedWithFixes, MergedSha: "m",
			HumanCommitCount: 2, HumanLinesAdded: 9, HumanLinesDeleted: 1,
		})).To(Succeed())

		got, _, _ := factory.Get(502)
		Expect(got.MergeState).To(Equal(outcomes.MergedWithFixes))
		Expect(got.HumanCommitCount).To(Equal(2))
		Expect(got.MergedAt).To(BeNumerically(">", 0))

		open, _ = factory.ListOpen()
		Expect(open).To(BeEmpty())

		Expect(factory.RecordMerge(502, outcomes.MergeResult{State: outcomes.Merged})).To(Equal(outcomes.ErrNotOpen))
		Expect(factory.RecordMerge(9999, outcomes.MergeResult{State: outcomes.Merged})).To(Equal(outcomes.ErrOutcomeNotFound))
	})

	It("closes an open row on disposition but keeps a terminal merge_state", func() {
		Expect(factory.Ensure(&outcomes.Outcome{TicketID: 503, Repo: "r", Branch: "b"})).To(Succeed())
		Expect(factory.SetDisposition(503, outcomes.DispositionInput{
			Disposition: "abandoned", Reason: "superseded", Notes: "dupe", By: "tdmtrader",
		})).To(Succeed())
		got, _, _ := factory.Get(503)
		Expect(got.MergeState).To(Equal(outcomes.ClosedUnmerged))
		Expect(got.Disposition).To(Equal("abandoned"))
		Expect(got.DisposedBy).To(Equal("tdmtrader"))

		Expect(factory.Ensure(&outcomes.Outcome{TicketID: 504, Repo: "r", Branch: "b", BaseSha: "z"})).To(Succeed())
		Expect(factory.RecordMerge(504, outcomes.MergeResult{State: outcomes.Merged, MergedSha: "x"})).To(Succeed())
		Expect(factory.SetDisposition(504, outcomes.DispositionInput{Disposition: "sent_back", Reason: "other", By: "x"})).To(Succeed())
		got, _, _ = factory.Get(504)
		Expect(got.MergeState).To(Equal(outcomes.Merged)) // terminal state preserved
		Expect(got.Disposition).To(Equal("sent_back"))
	})

	It("re-arms a sent_back row on Ensure but leaves an abandoned row terminal (F6)", func() {
		// sent_back → Ensure reopens with fresh shas + cleared disposition
		Expect(factory.Ensure(&outcomes.Outcome{TicketID: 506, Repo: "r", Branch: "agent/ticket-506", PushedSha: "p1", BaseSha: "b1"})).To(Succeed())
		Expect(factory.SetDisposition(506, outcomes.DispositionInput{Disposition: "sent_back", Reason: "incomplete", By: "u"})).To(Succeed())
		got, _, _ := factory.Get(506)
		Expect(got.MergeState).To(Equal(outcomes.ClosedUnmerged))

		Expect(factory.Ensure(&outcomes.Outcome{TicketID: 506, Repo: "r", Branch: "agent/ticket-506", PushedSha: "p2", BaseSha: "b2"})).To(Succeed())
		got, _, _ = factory.Get(506)
		Expect(got.MergeState).To(Equal(outcomes.MergeOpen))
		Expect(got.PushedSha).To(Equal("p2"))
		Expect(got.BaseSha).To(Equal("b2"))
		Expect(got.Disposition).To(BeEmpty())
		open, _ := factory.ListOpen()
		Expect(open).To(HaveLen(1)) // the re-armed row is back on the work-list

		// abandoned → Ensure must NOT re-arm; row stays closed and shas frozen
		Expect(factory.Ensure(&outcomes.Outcome{TicketID: 507, Repo: "r", Branch: "agent/ticket-507", PushedSha: "q1"})).To(Succeed())
		Expect(factory.SetDisposition(507, outcomes.DispositionInput{Disposition: "abandoned", Reason: "wont_do", By: "u"})).To(Succeed())
		Expect(factory.Ensure(&outcomes.Outcome{TicketID: 507, Repo: "r", Branch: "agent/ticket-507", PushedSha: "q2"})).To(Succeed())
		got, _, _ = factory.Get(507)
		Expect(got.MergeState).To(Equal(outcomes.ClosedUnmerged))
		Expect(got.PushedSha).To(Equal("q1"))
	})

	It("closes an open row as concluded and never re-arms it (flow-decoupling 2026-07-09)", func() {
		Expect(factory.Ensure(&outcomes.Outcome{TicketID: 508, Repo: "r", Branch: "agent/ticket-508", PushedSha: "s1", BaseSha: "t1"})).To(Succeed())
		Expect(factory.SetDisposition(508, outcomes.DispositionInput{
			Disposition: "concluded", Reason: "research_complete", Notes: "spike findings in ticket", By: "tdmtrader",
		})).To(Succeed())
		got, _, _ := factory.Get(508)
		Expect(got.MergeState).To(Equal(outcomes.MergeConcluded), "concluded closes as 'concluded', not closed_unmerged")
		Expect(got.Disposition).To(Equal("concluded"))
		Expect(got.DispositionReason).To(Equal("research_complete"))

		// concluded is terminal: the sent_back re-arm WHERE must never match it
		Expect(factory.Ensure(&outcomes.Outcome{TicketID: 508, Repo: "r", Branch: "agent/ticket-508", PushedSha: "s2", BaseSha: "t2"})).To(Succeed())
		got, _, _ = factory.Get(508)
		Expect(got.MergeState).To(Equal(outcomes.MergeConcluded))
		Expect(got.PushedSha).To(Equal("s1"), "shas stay frozen — the watcher never re-arms a concluded row")
		Expect(got.Disposition).To(Equal("concluded"))
		open, _ := factory.ListOpen()
		Expect(open).To(BeEmpty(), "concluded rows leave the watcher work-list permanently")
	})

	It("stamps last_checked_at on Touch", func() {
		Expect(factory.Ensure(&outcomes.Outcome{TicketID: 505, Repo: "r", Branch: "b"})).To(Succeed())
		Expect(factory.Touch(505)).To(Succeed())
		got, _, _ := factory.Get(505)
		Expect(got.LastCheckedAt).To(BeNumerically(">", 0))
	})
})
```

- [ ] Run to verify it fails: `ginkgo --focus="AgentOutcomesFactory" ./atc/db/` — expected failure: compile error `undefined: db.NewAgentOutcomesFactory`.
- [ ] Write `atc/db/agent_outcomes_factory.go`:

```go
package db

import (
	"database/sql"

	"github.com/concourse/concourse/agent/api/outcomes"
)

//counterfeiter:generate . AgentOutcomesFactory
type AgentOutcomesFactory interface {
	outcomes.Store
}

func NewAgentOutcomesFactory(conn DbConn) AgentOutcomesFactory {
	return &agentOutcomesFactory{conn: conn}
}

type agentOutcomesFactory struct {
	conn DbConn
}

const outcomeColumns = `ticket_id, repo, branch, pushed_sha, base_sha, merge_state,
	merged_sha,
	EXTRACT(EPOCH FROM merged_at)::bigint,
	human_commit_count, human_lines_added, human_lines_deleted,
	disposition, disposition_reason, disposition_notes, disposed_by,
	EXTRACT(EPOCH FROM last_checked_at)::bigint,
	EXTRACT(EPOCH FROM created_at)::bigint,
	EXTRACT(EPOCH FROM updated_at)::bigint`

// Ensure inserts a fresh open row, or refreshes branch/pushed_sha/base_sha
// on an existing OPEN row (re-push after send-back). The WHERE-guarded
// UPDATE-in-DO leaves terminal rows untouched.
func (f *agentOutcomesFactory) Ensure(o *outcomes.Outcome) error {
	// The ON CONFLICT WHERE also re-arms a send-back row (closed_unmerged with
	// disposition='sent_back') back to 'open' and clears the disposition, so the
	// re-dispatch loop's eventual human merge is detected (F6). Truly terminal
	// rows (merged/merged_with_fixes, abandoned, or concluded) never match the
	// WHERE — 'concluded' is its own merge_state, so neither arm can touch it.
	_, err := f.conn.Exec(
		`INSERT INTO agent_outcomes (ticket_id, repo, branch, pushed_sha, base_sha, merge_state)
		 VALUES ($1, $2, $3, $4, $5, 'open')
		 ON CONFLICT (ticket_id) DO UPDATE SET
		   branch      = EXCLUDED.branch,
		   pushed_sha  = EXCLUDED.pushed_sha,
		   base_sha    = EXCLUDED.base_sha,
		   merge_state = 'open',
		   disposition = CASE WHEN agent_outcomes.merge_state = 'closed_unmerged'
		                      THEN '' ELSE agent_outcomes.disposition END,
		   disposition_reason = CASE WHEN agent_outcomes.merge_state = 'closed_unmerged'
		                             THEN '' ELSE agent_outcomes.disposition_reason END,
		   disposition_notes  = CASE WHEN agent_outcomes.merge_state = 'closed_unmerged'
		                             THEN '' ELSE agent_outcomes.disposition_notes END,
		   disposed_by = CASE WHEN agent_outcomes.merge_state = 'closed_unmerged'
		                      THEN '' ELSE agent_outcomes.disposed_by END,
		   updated_at = now()
		 WHERE agent_outcomes.merge_state = 'open'
		    OR (agent_outcomes.merge_state = 'closed_unmerged'
		        AND agent_outcomes.disposition = 'sent_back')`,
		o.TicketID, o.Repo, o.Branch, o.PushedSha, o.BaseSha,
	)
	return err
}

func (f *agentOutcomesFactory) Get(ticketID int) (*outcomes.Outcome, bool, error) {
	row := f.conn.QueryRow(
		`SELECT `+outcomeColumns+` FROM agent_outcomes WHERE ticket_id = $1`, ticketID,
	)
	o, err := scanOutcome(row)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return o, true, nil
}

func (f *agentOutcomesFactory) ListOpen() ([]outcomes.Outcome, error) {
	rows, err := f.conn.Query(
		`SELECT ` + outcomeColumns + ` FROM agent_outcomes
		 WHERE merge_state = 'open' ORDER BY ticket_id ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := []outcomes.Outcome{}
	for rows.Next() {
		o, err := scanOutcome(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, *o)
	}
	return results, rows.Err()
}

func (f *agentOutcomesFactory) RecordMerge(ticketID int, res outcomes.MergeResult) error {
	if res.State != outcomes.Merged && res.State != outcomes.MergedWithFixes {
		return outcomes.ErrNotOpen
	}
	result, err := f.conn.Exec(
		`UPDATE agent_outcomes SET
		   merge_state = $2, merged_sha = $3, merged_at = now(),
		   human_commit_count = $4, human_lines_added = $5, human_lines_deleted = $6,
		   updated_at = now()
		 WHERE ticket_id = $1 AND merge_state = 'open'`,
		ticketID, string(res.State), res.MergedSha,
		res.HumanCommitCount, res.HumanLinesAdded, res.HumanLinesDeleted,
	)
	if err != nil {
		return err
	}
	return classifyOutcomeUpdate(f, ticketID, result)
}

func (f *agentOutcomesFactory) SetDisposition(ticketID int, d outcomes.DispositionInput) error {
	// Open rows become closed_unmerged — or 'concluded' for a concluded
	// disposition (positive terminal, §1.11.1); terminal rows keep merge_state.
	result, err := f.conn.Exec(
		`UPDATE agent_outcomes SET
		   disposition = $2, disposition_reason = $3, disposition_notes = $4, disposed_by = $5,
		   merge_state = CASE WHEN merge_state = 'open'
		                      THEN CASE WHEN $2 = 'concluded' THEN 'concluded' ELSE 'closed_unmerged' END
		                      ELSE merge_state END,
		   updated_at = now()
		 WHERE ticket_id = $1`,
		ticketID, d.Disposition, d.Reason, d.Notes, d.By,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return outcomes.ErrOutcomeNotFound
	}
	return nil
}

func (f *agentOutcomesFactory) Touch(ticketID int) error {
	result, err := f.conn.Exec(
		`UPDATE agent_outcomes SET last_checked_at = now(), updated_at = now() WHERE ticket_id = $1`,
		ticketID,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return outcomes.ErrOutcomeNotFound
	}
	return nil
}

// classifyOutcomeUpdate turns a zero-row RecordMerge into ErrOutcomeNotFound
// (row absent) or ErrNotOpen (row terminal), matching the MemoryStore.
func classifyOutcomeUpdate(f *agentOutcomesFactory, ticketID int, result sql.Result) error {
	if n, _ := result.RowsAffected(); n > 0 {
		return nil
	}
	if _, found, err := f.Get(ticketID); err != nil {
		return err
	} else if !found {
		return outcomes.ErrOutcomeNotFound
	}
	return outcomes.ErrNotOpen
}

type scannable interface {
	Scan(dest ...any) error
}

func scanOutcome(row scannable) (*outcomes.Outcome, error) {
	var o outcomes.Outcome
	var mergedAt, lastChecked, createdAt, updatedAt sql.NullInt64
	if err := row.Scan(
		&o.TicketID, &o.Repo, &o.Branch, &o.PushedSha, &o.BaseSha, &o.MergeState,
		&o.MergedSha, &mergedAt,
		&o.HumanCommitCount, &o.HumanLinesAdded, &o.HumanLinesDeleted,
		&o.Disposition, &o.DispositionReason, &o.DispositionNotes, &o.DisposedBy,
		&lastChecked, &createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}
	o.MergedAt = mergedAt.Int64
	o.LastCheckedAt = lastChecked.Int64
	o.CreatedAt = createdAt.Int64
	o.UpdatedAt = updatedAt.Int64
	return &o, nil
}
```

> If `db.scannable` already exists in this package (another factory may define it), drop the local declaration and reuse the existing one — the compiler flags the duplicate; do not rename the package-level identifier.

- [ ] Generate the fake: `cd atc/db && go run github.com/maxbrunsfeld/counterfeiter/v6 -o dbfakes/fake_agent_outcomes_factory.go . AgentOutcomesFactory && cd ../..`
- [ ] Run to verify pass: `ginkgo --focus="AgentOutcomesFactory" ./atc/db/ && go build ./atc/db/...` — expect green.
- [ ] Commit: `git add atc/db && git commit -m "feat(delivery-outcomes): AgentOutcomesFactory + fake"`

---

### Task 5: `agent/api/outcomes` HTTP handler — GetAgentTicketOutcome + SetAgentTicketDisposition (through the transition function)

The disposition endpoint is a state-changing write: it records the disposition in `agent_outcomes` AND walks the ticket lifecycle to `sent_back`/`abandoned`/`concluded` **exclusively through `tickets.Store.Transition`** (single-writer discipline; `concluded` rides the `needs_review → concluded` edge added by the 2026-07-09 flow-decoupling amendment). Same handler serves the read-only outcome fetch. Follows the `agent/api/tickets/handler.go` idiom — the handler must NOT import `atc/api/accessor` (cycle); it takes an injected `UserNameFunc`.

**Files:**
- Create: `agent/api/outcomes/handler.go`
- Test: `agent/api/outcomes/handler_test.go`

**Steps:**

- [ ] Write the failing test `agent/api/outcomes/handler_test.go`:

```go
package outcomes_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/concourse/concourse/agent/api/outcomes"
	"github.com/concourse/concourse/agent/api/tickets"
)

func newHandler(t *testing.T) (*outcomes.Handler, *outcomes.MemoryStore, *tickets.MemoryStore) {
	t.Helper()
	os := outcomes.NewMemoryStore()
	ts := tickets.NewMemoryStore()
	h := outcomes.NewHandler(os, ts, func(r *http.Request) string { return "tdmtrader" })
	return h, os, ts
}

// seedNeedsReview creates a ticket already at needs_review with an outcome row.
func seedNeedsReview(t *testing.T, os *outcomes.MemoryStore, ts *tickets.MemoryStore, id int) {
	t.Helper()
	tk := &tickets.Ticket{Title: "x", Repo: "r", TargetBranch: "main"}
	created, err := ts.Create(tk)
	if err != nil {
		t.Fatal(err)
	}
	// drive draft -> queued -> running -> needs_review
	_ = ts.Transition(created, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{})
	_ = ts.Transition(created, tickets.StateQueued, tickets.StateRunning, tickets.TransitionMeta{})
	if err := ts.Transition(created, tickets.StateRunning, tickets.StateNeedsReview, tickets.TransitionMeta{Branch: "agent/ticket-1"}); err != nil {
		t.Fatal(err)
	}
	_ = os.Ensure(&outcomes.Outcome{TicketID: created, Repo: "r", Branch: "agent/ticket-1"})
}

func TestGetOutcome(t *testing.T) {
	h, os, _ := newHandler(t)
	_ = os.Ensure(&outcomes.Outcome{TicketID: 1, Repo: "r", Branch: "b", PushedSha: "p"})

	req := httptest.NewRequest("GET", "/api/v1/agent/tickets/1/outcome", nil)
	req.Form = map[string][]string{":ticket_id": {"1"}}
	rec := httptest.NewRecorder()
	h.GetOutcome(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body)
	}
	var got outcomes.Outcome
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.TicketID != 1 || got.PushedSha != "p" {
		t.Fatalf("got %+v", got)
	}

	// no outcome row yet -> 404
	req2 := httptest.NewRequest("GET", "/api/v1/agent/tickets/2/outcome", nil)
	req2.Form = map[string][]string{":ticket_id": {"2"}}
	rec2 := httptest.NewRecorder()
	h.GetOutcome(rec2, req2)
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("missing outcome: code = %d", rec2.Code)
	}
}

func TestSetDispositionTransitionsTicket(t *testing.T) {
	h, os, ts := newHandler(t)
	seedNeedsReview(t, os, ts, 1)

	body, _ := json.Marshal(map[string]string{
		"disposition": "sent_back", "reason": "incomplete", "notes": "needs more tests",
	})
	req := httptest.NewRequest("PUT", "/api/v1/agent/tickets/1/disposition", bytes.NewReader(body))
	req.Form = map[string][]string{":ticket_id": {"1"}}
	rec := httptest.NewRecorder()
	h.SetDisposition(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body)
	}

	// outcome recorded
	got, _, _ := os.Get(1)
	if got.Disposition != "sent_back" || got.DisposedBy != "tdmtrader" || got.MergeState != outcomes.ClosedUnmerged {
		t.Fatalf("outcome not recorded: %+v", got)
	}
	// ticket transitioned via the store
	tk, _, _ := ts.Get(1)
	if tk.State != tickets.StateSentBack {
		t.Fatalf("ticket state = %s, want sent_back", tk.State)
	}
}

func TestSetDispositionConcludedTransitionsTicket(t *testing.T) {
	h, os, ts := newHandler(t)
	seedNeedsReview(t, os, ts, 1)

	// concluded: spike/research done, human reviewed, no merge intended.
	body, _ := json.Marshal(map[string]string{
		"disposition": "concluded", "reason": "research_complete", "notes": "findings recorded in ticket body",
	})
	req := httptest.NewRequest("PUT", "/api/v1/agent/tickets/1/disposition", bytes.NewReader(body))
	req.Form = map[string][]string{":ticket_id": {"1"}}
	rec := httptest.NewRecorder()
	h.SetDisposition(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body)
	}

	// outcome row closes as 'concluded' — never closed_unmerged, never open
	got, _, _ := os.Get(1)
	if got.Disposition != outcomes.DispositionConcluded || got.MergeState != outcomes.MergeConcluded || got.DisposedBy != "tdmtrader" {
		t.Fatalf("concluded outcome not recorded: %+v", got)
	}
	// ticket took the needs_review → concluded edge via the single writer
	tk, _, _ := ts.Get(1)
	if tk.State != tickets.StateConcluded {
		t.Fatalf("ticket state = %s, want concluded", tk.State)
	}
}

func TestSetDispositionRejectsBadTaxonomy(t *testing.T) {
	h, os, ts := newHandler(t)
	seedNeedsReview(t, os, ts, 1)
	for _, bad := range []string{
		`{"disposition":"merged","reason":"other"}`,
		`{"disposition":"sent_back","reason":"nonsense"}`,
		`{"disposition":"","reason":"other"}`,
	} {
		req := httptest.NewRequest("PUT", "/x", bytes.NewReader([]byte(bad)))
		req.Form = map[string][]string{":ticket_id": {"1"}}
		rec := httptest.NewRecorder()
		h.SetDisposition(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: code = %d, want 400", bad, rec.Code)
		}
	}
}

func TestSetDispositionInvalidTransitionIs409(t *testing.T) {
	h, os, ts := newHandler(t)
	// ticket already merged: needs_review -> abandoned is not a legal edge from merged
	tk := &tickets.Ticket{Title: "x", Repo: "r", TargetBranch: "main"}
	id, _ := ts.Create(tk)
	_ = ts.Transition(id, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{})
	_ = ts.Transition(id, tickets.StateQueued, tickets.StateRunning, tickets.TransitionMeta{})
	_ = ts.Transition(id, tickets.StateRunning, tickets.StateNeedsReview, tickets.TransitionMeta{})
	_ = ts.Transition(id, tickets.StateNeedsReview, tickets.StateMerged, tickets.TransitionMeta{})
	_ = os.Ensure(&outcomes.Outcome{TicketID: id, Repo: "r", Branch: "b"})
	_ = os.RecordMerge(id, outcomes.MergeResult{State: outcomes.Merged})

	body, _ := json.Marshal(map[string]string{"disposition": "abandoned", "reason": "other"})
	req := httptest.NewRequest("PUT", "/x", bytes.NewReader(body))
	req.Form = map[string][]string{":ticket_id": {"1"}}
	// use the merged ticket's id
	req.Form.Set(":ticket_id", itoa(id))
	rec := httptest.NewRecorder()
	h.SetDisposition(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409", rec.Code)
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }
```

- [ ] Run `go test ./agent/api/outcomes/` — expect compile failure (`outcomes.NewHandler` undefined).
- [ ] Write `agent/api/outcomes/handler.go`:

```go
package outcomes

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/concourse/concourse/agent/api/tickets"
)

// UserNameFunc resolves the authenticated human username ("" when
// anonymous). Injected because this package must not import
// atc/api/accessor (which imports atc/db, which imports this package via
// AgentOutcomesFactory — a cycle). atc/api/handler.go wires
// accessor.GetAccessor(r).Claims().UserName.
type UserNameFunc func(r *http.Request) string

// Handler serves GetAgentTicketOutcome + SetAgentTicketDisposition. Auth
// is enforced by the wrappa tier (authorized member/viewer, §4.2); this
// handler only reads WHO the verified writer is.
type Handler struct {
	store    Store
	tickets  tickets.Store
	userName UserNameFunc
}

func NewHandler(store Store, ticketStore tickets.Store, userName UserNameFunc) *Handler {
	return &Handler{store: store, tickets: ticketStore, userName: userName}
}

// DispositionRequest is the PUT body for SetAgentTicketDisposition.
type DispositionRequest struct {
	Disposition string `json:"disposition"` // sent_back | abandoned | concluded
	Reason      string `json:"reason"`      // §1.11 taxonomy (required)
	Notes       string `json:"notes,omitempty"`
}

// dispositionToState maps a disposition to its ticket lifecycle state.
// All three are needs_review edges; concluded is the neutral terminal
// ("run finished, human reviewed, no merge intended" — FLOWS.md §4).
func dispositionToState(d string) tickets.State {
	switch d {
	case DispositionAbandoned:
		return tickets.StateAbandoned
	case DispositionConcluded:
		return tickets.StateConcluded
	}
	return tickets.StateSentBack
}

// GetOutcome handles GET /api/v1/agent/tickets/:ticket_id/outcome.
func (h *Handler) GetOutcome(w http.ResponseWriter, r *http.Request) {
	id, ok := ticketID(w, r)
	if !ok {
		return
	}
	o, found, err := h.store.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "no outcome for ticket", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

// SetDisposition handles PUT /api/v1/agent/tickets/:ticket_id/disposition.
func (h *Handler) SetDisposition(w http.ResponseWriter, r *http.Request) {
	id, ok := ticketID(w, r)
	if !ok {
		return
	}
	var req DispositionRequest
	if !readJSON(w, r, &req) {
		return
	}
	if !ValidDisposition(req.Disposition) {
		http.Error(w, "disposition must be sent_back, abandoned, or concluded", http.StatusBadRequest)
		return
	}
	if !ValidDispositionReason(req.Reason) {
		http.Error(w, "reason must be one of the disposition taxonomy values", http.StatusBadRequest)
		return
	}

	tk, found, err := h.tickets.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "ticket not found", http.StatusNotFound)
		return
	}

	// Walk the ticket lifecycle FIRST, through the single writer. If the
	// current state does not allow the edge (e.g. already merged), do not
	// touch agent_outcomes — the disposition and the ticket state stay
	// consistent.
	target := dispositionToState(req.Disposition)
	if err := h.tickets.Transition(id, tk.State, target, tickets.TransitionMeta{}); err != nil {
		switch {
		case errors.Is(err, tickets.ErrInvalidTransition), errors.Is(err, tickets.ErrStaleTransition):
			http.Error(w, err.Error(), http.StatusConflict)
		case errors.Is(err, tickets.ErrTicketNotFound):
			http.Error(w, err.Error(), http.StatusNotFound)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	if err := h.store.SetDisposition(id, DispositionInput{
		Disposition: req.Disposition, Reason: req.Reason, Notes: req.Notes,
		By: h.userName(r),
	}); err != nil {
		if errors.Is(err, ErrOutcomeNotFound) {
			// No outcome row yet (never pushed / dispositioned pre-harvest):
			// create one so the disposition is durable.
			_ = h.store.Ensure(&Outcome{TicketID: id, Repo: tk.Repo, Branch: tk.Branch})
			if err2 := h.store.SetDisposition(id, DispositionInput{
				Disposition: req.Disposition, Reason: req.Reason, Notes: req.Notes, By: h.userName(r),
			}); err2 != nil {
				http.Error(w, err2.Error(), http.StatusInternalServerError)
				return
			}
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	o, _, _ := h.store.Get(id)
	writeJSON(w, http.StatusOK, o)
}

func ticketID(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.FormValue(":ticket_id"))
	if err != nil || id <= 0 {
		http.Error(w, "invalid ticket_id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func readJSON(w http.ResponseWriter, r *http.Request, into any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return false
	}
	if err := json.Unmarshal(body, into); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}
```

- [ ] Run `go test ./agent/api/outcomes/` — expect pass.
- [ ] Commit: `git add agent/api/outcomes && git commit -m "feat(delivery-outcomes): outcome + disposition handler (transition-function single writer)"`

---

### Task 6: `agent/gitcheck` — bare-mirror cache + low-level git operations

The native-repo-checking machinery (no webhooks). A `Mirror` keeps a bare `git clone --mirror` per repo on the web node, refreshed with one `git fetch --prune` per tick, and exposes the git primitives the detection logic composes: ancestor test, merge-point resolution, first-parent commit walk with author, numstat, patch-id, and a file-windowed diff. Real-git fixture tests (the `agent/harvest/workspace_test.go` recipe: build bare origins + clones with `os/exec` git in a `TempDir`).

**Files:**
- Create: `agent/gitcheck/mirror.go`
- Test: `agent/gitcheck/mirror_test.go`

**Steps:**

- [ ] Write the failing spec `agent/gitcheck/mirror_test.go` (Ginkgo, mirroring `agent/harvest/workspace_test.go`'s fixture helpers):

```go
package gitcheck_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/agent/gitcheck"
)

func TestGitcheck(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Gitcheck Suite")
}

// git runs a git command in dir with a fixed non-bot committer, failing on error.
func git(dir string, env []string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

var botEnv = []string{
	"GIT_AUTHOR_NAME=concourse-agent[bot]", "GIT_AUTHOR_EMAIL=agent@concourse.local",
	"GIT_COMMITTER_NAME=concourse-agent[bot]", "GIT_COMMITTER_EMAIL=agent@concourse.local",
}
var humanEnv = []string{
	"GIT_AUTHOR_NAME=Alice", "GIT_AUTHOR_EMAIL=alice@example.com",
	"GIT_COMMITTER_NAME=Alice", "GIT_COMMITTER_EMAIL=alice@example.com",
}

// setupOrigin builds a bare origin with main at one base commit and returns
// (bareDir, baseSha).
func setupOrigin(tmp string) (string, string) {
	bare := filepath.Join(tmp, "origin.git")
	Expect(os.MkdirAll(bare, 0o755)).To(Succeed())
	git(bare, nil, "init", "--bare", "--initial-branch=main")
	seed := filepath.Join(tmp, "seed")
	git(tmp, botEnv, "clone", bare, seed)
	Expect(os.WriteFile(filepath.Join(seed, "base.txt"), []byte("base\n"), 0o644)).To(Succeed())
	git(seed, botEnv, "add", ".")
	git(seed, botEnv, "commit", "-m", "base")
	git(seed, botEnv, "push", "origin", "HEAD:main")
	base := git(seed, botEnv, "rev-parse", "HEAD")
	return bare, base
}

var _ = Describe("gitcheck.Mirror", func() {
	var tmp, bare, base string

	BeforeEach(func() {
		tmp = GinkgoT().TempDir()
		bare, base = setupOrigin(tmp)
	})

	// clone builds a working clone of the origin.
	clone := func() string {
		ws := filepath.Join(tmp, "ws"+RandString())
		git(tmp, botEnv, "clone", bare, ws)
		return ws
	}

	It("clones a mirror and detects a fast-forward merge (branch head is ancestor of main)", func() {
		ws := clone()
		Expect(os.WriteFile(filepath.Join(ws, "f.go"), []byte("package f\n"), 0o644)).To(Succeed())
		git(ws, botEnv, "add", ".")
		git(ws, botEnv, "commit", "-m", "agent work")
		pushed := git(ws, botEnv, "rev-parse", "HEAD")
		git(ws, botEnv, "push", "origin", "HEAD:refs/heads/agent/ticket-1")
		// fast-forward main to the agent branch
		git(ws, botEnv, "push", "origin", "HEAD:main")

		m, err := gitcheck.OpenMirror(filepath.Join(tmp, "cache"), "tdmtrader/concourse", bare, gitcheck.Auth{})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Fetch()).To(Succeed())

		anc, err := m.IsAncestor(pushed, "main")
		Expect(err).NotTo(HaveOccurred())
		Expect(anc).To(BeTrue())

		mp, err := m.MergePoint(pushed, "main")
		Expect(err).NotTo(HaveOccurred())
		Expect(mp.Merged).To(BeTrue())
		Expect(mp.TipAtMerge).To(Equal(pushed)) // fast-forward: tip == pushed
	})

	It("computes the human-touch delta excluding bot commits, first-parent", func() {
		ws := clone()
		Expect(os.WriteFile(filepath.Join(ws, "f.go"), []byte("package f\n"), 0o644)).To(Succeed())
		git(ws, botEnv, "add", ".")
		git(ws, botEnv, "commit", "-m", "agent work")
		pushed := git(ws, botEnv, "rev-parse", "HEAD")
		// a human amends the branch: +2 lines / -0
		Expect(os.WriteFile(filepath.Join(ws, "f.go"), []byte("package f\n\n// fix\nvar X = 1\n"), 0o644)).To(Succeed())
		git(ws, humanEnv, "add", ".")
		git(ws, humanEnv, "commit", "-m", "human fix")
		tip := git(ws, humanEnv, "rev-parse", "HEAD")
		git(ws, humanEnv, "push", "origin", "HEAD:refs/heads/agent/ticket-1")

		m, _ := gitcheck.OpenMirror(filepath.Join(tmp, "cache"), "tdmtrader/concourse", bare, gitcheck.Auth{})
		Expect(m.Fetch()).To(Succeed())

		delta, err := m.HumanDelta(pushed, tip)
		Expect(err).NotTo(HaveOccurred())
		Expect(delta.CommitCount).To(Equal(1))
		Expect(delta.LinesAdded).To(Equal(3)) // blank + comment + var line
		Expect(delta.LinesDeleted).To(Equal(0))
	})

	It("matches a squashed branch via patch-id", func() {
		ws := clone()
		Expect(os.WriteFile(filepath.Join(ws, "f.go"), []byte("package f\nvar A = 1\n"), 0o644)).To(Succeed())
		git(ws, botEnv, "add", ".")
		git(ws, botEnv, "commit", "-m", "c1")
		Expect(os.WriteFile(filepath.Join(ws, "g.go"), []byte("package g\n"), 0o644)).To(Succeed())
		git(ws, botEnv, "add", ".")
		git(ws, botEnv, "commit", "-m", "c2")
		branchTip := git(ws, botEnv, "rev-parse", "HEAD")
		git(ws, botEnv, "push", "origin", "HEAD:refs/heads/agent/ticket-1")

		// simulate a squash-merge onto main: one commit carrying the same net diff
		sq := clone()
		Expect(os.WriteFile(filepath.Join(sq, "f.go"), []byte("package f\nvar A = 1\n"), 0o644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(sq, "g.go"), []byte("package g\n"), 0o644)).To(Succeed())
		git(sq, humanEnv, "add", ".")
		git(sq, humanEnv, "commit", "-m", "squash ticket-1")
		squashSha := git(sq, humanEnv, "rev-parse", "HEAD")
		git(sq, humanEnv, "push", "origin", "HEAD:main")

		m, _ := gitcheck.OpenMirror(filepath.Join(tmp, "cache"), "tdmtrader/concourse", bare, gitcheck.Auth{})
		Expect(m.Fetch()).To(Succeed())

		anc, _ := m.IsAncestor(branchTip, "main")
		Expect(anc).To(BeFalse()) // squash: branch tip is NOT an ancestor

		match, err := m.PatchIDMatch(base, branchTip, "main", 200)
		Expect(err).NotTo(HaveOccurred())
		Expect(match.Found).To(BeTrue())
		Expect(match.Sha).To(Equal(squashSha))
	})
})
```

> The suite uses a `RandString()` helper to isolate clone dirs across specs; add a tiny package-private helper to the test file (`func RandString() string { return strconv.Itoa(GinkgoRandomSeed()) + strconv.Itoa(rand.Int()) }` with `math/rand` + `strconv` imports) or reuse `GinkgoT().TempDir()` per clone if simpler.

- [ ] Run to verify it fails: `ginkgo ./agent/gitcheck/` — expected failure: package does not exist.
- [ ] Write `agent/gitcheck/mirror.go`:

```go
// Package gitcheck maintains bare --mirror clones on the web node and
// answers the merge/ancestor/human-delta/patch-id/diff questions the
// outcome watcher needs — native repo checking, no SCM webhooks
// (shared-contracts §1.11.1, spec §9).
package gitcheck

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Auth carries optional https fetch credentials, injected via a temp
// credential-store file (never argv), matching harvest push (§8.3).
type Auth struct {
	Username string
	Token    string
}

// Mirror is a bare --mirror clone of one repo's origin.
type Mirror struct {
	dir  string // the bare git dir
	repo string // canonical slug
	url  string
	auth Auth
}

// OpenMirror ensures a --mirror clone of url exists under cacheDir/<slug>.git,
// cloning it on first use. slug is used only for the on-disk directory name.
func OpenMirror(cacheDir, repo, url string, auth Auth) (*Mirror, error) {
	safe := strings.NewReplacer("/", "_", ":", "_", "@", "_").Replace(repo)
	dir := filepath.Join(cacheDir, safe+".git")
	m := &Mirror{dir: dir, repo: repo, url: url, auth: auth}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(cacheDir, 0o700); err != nil {
			return nil, err
		}
		if _, err := m.run(cacheDir, "clone", "--mirror", url, dir); err != nil {
			return nil, fmt.Errorf("clone --mirror %s: %w", repo, err)
		}
	} else if err != nil {
		return nil, err
	}
	return m, nil
}

// Fetch refreshes all refs, pruning deleted ones ("polite": one call per tick).
func (m *Mirror) Fetch() error {
	_, err := m.run(m.dir, "fetch", "--prune", "origin", "+refs/*:refs/*")
	return err
}

// IsAncestor reports whether sha is reachable from refs/heads/<branch>.
func (m *Mirror) IsAncestor(sha, branch string) (bool, error) {
	cmd := m.command(m.dir, "merge-base", "--is-ancestor", sha, "refs/heads/"+branch)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		return false, nil // definitively not an ancestor
	}
	return false, fmt.Errorf("is-ancestor: %w", err)
}

// MergePoint describes how pushedSha landed on the target branch.
type MergePoint struct {
	Merged        bool
	MergedSha     string // the merge commit (or fast-forward tip / pushedSha)
	TipAtMerge    string // the branch tip at the moment it merged (for the delta window)
	FastForwarded bool   // true when no merge commit was found (ff / rebase-merge onto target)
}

// MergePoint resolves the merge commit that brought pushedSha into the TARGET
// branch (the `branch` arg is the target, e.g. "main"). Precondition:
// IsAncestor(pushedSha, branch) is true. Note: MergePoint does NOT know the
// agent branch's name, so for a fast-forward it can only fall back to
// pushedSha for TipAtMerge; the §1.11.1 "agent branch remote head" refinement
// is applied by Detect (which owns the agent branch name) — see FastForwarded.
func (m *Mirror) MergePoint(pushedSha, branch string) (MergePoint, error) {
	// oldest merge commit on the ancestry path from pushedSha to the branch head
	out, err := m.run(m.dir, "rev-list", "--ancestry-path", "--merges", "--reverse",
		pushedSha+"..refs/heads/"+branch)
	if err != nil {
		return MergePoint{}, err
	}
	lines := nonEmptyLines(out)
	if len(lines) == 0 {
		// Fast-forward: no merge commit on the ancestry path. With only the
		// target branch in hand, the documented fallback is pushedSha itself
		// (§1.11.1: "the agent branch's remote head if the branch still
		// exists, else pushed_sha"). Detect refines TipAtMerge to the agent
		// branch head when FastForwarded is set.
		return MergePoint{Merged: true, MergedSha: pushedSha, TipAtMerge: pushedSha, FastForwarded: true}, nil
	}
	mergeCommit := lines[0]
	tip := pushedSha
	// second parent of the merge commit, when it descends from pushedSha, is
	// the branch tip that was merged.
	if parents, err := m.run(m.dir, "rev-list", "--parents", "-n", "1", mergeCommit); err == nil {
		fields := strings.Fields(parents)
		if len(fields) >= 3 {
			second := fields[2]
			if anc, _ := m.IsAncestor(pushedSha, "") ; false {
				_ = anc
			}
			if descends, _ := m.commitContains(second, pushedSha); descends {
				tip = second
			}
		}
	}
	return MergePoint{Merged: true, MergedSha: mergeCommit, TipAtMerge: tip}, nil
}

// commitContains reports whether ancestor is reachable from commit.
func (m *Mirror) commitContains(commit, ancestor string) (bool, error) {
	cmd := m.command(m.dir, "merge-base", "--is-ancestor", ancestor, commit)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// Delta is the human-touch delta over a commit window.
type Delta struct {
	CommitCount  int
	LinesAdded   int
	LinesDeleted int
}

// HumanDelta sums numstat lines of non-bot commits in pushedSha..tip
// (first-parent walk, merge commits excluded). Decision 18 / §1.11.1.
func (m *Mirror) HumanDelta(pushedSha, tip string) (Delta, error) {
	if pushedSha == "" || tip == "" || pushedSha == tip {
		return Delta{}, nil
	}
	// list non-merge commits on the first-parent path, with author name.
	out, err := m.run(m.dir, "log", "--first-parent", "--no-merges",
		"--format=%H%x1f%an", pushedSha+".."+tip)
	if err != nil {
		return Delta{}, err
	}
	var d Delta
	for _, line := range nonEmptyLines(out) {
		parts := strings.SplitN(line, "\x1f", 2)
		if len(parts) != 2 {
			continue
		}
		sha, author := parts[0], parts[1]
		if author == BotAuthor {
			continue
		}
		d.CommitCount++
		added, deleted, err := m.numstat(sha)
		if err != nil {
			return Delta{}, err
		}
		d.LinesAdded += added
		d.LinesDeleted += deleted
	}
	return d, nil
}

// BotAuthor mirrors outcomes.BotAuthor without importing that package
// (gitcheck is a leaf). Kept identical to §8.3.
const BotAuthor = "concourse-agent[bot]"

func (m *Mirror) numstat(sha string) (added, deleted int, err error) {
	out, err := m.run(m.dir, "show", "--numstat", "--format=", sha)
	if err != nil {
		return 0, 0, err
	}
	for _, line := range nonEmptyLines(out) {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		a, aerr := strconv.Atoi(fields[0]) // "-" for binary => Atoi fails => 0
		d, derr := strconv.Atoi(fields[1])
		if aerr == nil {
			added += a
		}
		if derr == nil {
			deleted += d
		}
	}
	return added, deleted, nil
}

// PatchMatch is a squash-merge patch-id hit.
type PatchMatch struct {
	Found bool
	Sha   string
}

// PatchIDMatch compares the combined patch base..branchTip against the
// patch-ids of the newest scanLimit first-parent commits on branch.
func (m *Mirror) PatchIDMatch(base, branchTip, branch string, scanLimit int) (PatchMatch, error) {
	if base == "" || branchTip == "" {
		return PatchMatch{}, nil
	}
	wantID, err := m.combinedPatchID(base, branchTip)
	if err != nil || wantID == "" {
		return PatchMatch{}, err
	}
	shas, err := m.run(m.dir, "rev-list", "--first-parent", "-n", strconv.Itoa(scanLimit),
		"refs/heads/"+branch)
	if err != nil {
		return PatchMatch{}, err
	}
	for _, sha := range nonEmptyLines(shas) {
		id, err := m.commitPatchID(sha)
		if err != nil || id == "" {
			continue
		}
		if id == wantID {
			return PatchMatch{Found: true, Sha: sha}, nil
		}
	}
	return PatchMatch{}, nil
}

func (m *Mirror) combinedPatchID(base, tip string) (string, error) {
	diff, err := m.run(m.dir, "diff", base+".."+tip)
	if err != nil {
		return "", err
	}
	return m.patchIDOf(diff)
}

func (m *Mirror) commitPatchID(sha string) (string, error) {
	diff, err := m.run(m.dir, "show", sha)
	if err != nil {
		return "", err
	}
	return m.patchIDOf(diff)
}

func (m *Mirror) patchIDOf(diff string) (string, error) {
	if strings.TrimSpace(diff) == "" {
		return "", nil
	}
	cmd := m.command(m.dir, "patch-id", "--stable")
	cmd.Stdin = strings.NewReader(diff)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

// run executes git in dir and returns trimmed combined stdout.
func (m *Mirror) run(dir string, args ...string) (string, error) {
	cmd := m.command(dir, args...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %v: %w: %s", args, err, ee.Stderr)
		}
		return "", fmt.Errorf("git %v: %w", args, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// command builds a git exec.Cmd with credentials injected via env-based
// credential store (never argv), matching harvest §8.3.
func (m *Mirror) command(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=credential.helper",
		"GIT_CONFIG_VALUE_0="+m.credentialHelper(),
	)
	return cmd
}

// credentialHelper returns an inline shell credential helper that echoes the
// configured token when set, else empty (anonymous https / ssh untouched).
func (m *Mirror) credentialHelper() string {
	if m.auth.Token == "" {
		return ""
	}
	u := m.auth.Username
	if u == "" {
		u = "x-access-token"
	}
	// `!f() { echo "username=..."; echo "password=..."; }; f` — a one-shot
	// helper; the token never appears in argv of the fetch/log commands.
	return fmt.Sprintf(`!f() { echo username=%s; echo password=%s; }; f`, u, m.auth.Token)
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimSpace(l))
		}
	}
	return out
}
```

> The `MergePoint` dead `if anc, _ := ...; false` guard above is a copy-paste artifact — delete it when writing the file; the real second-parent test is `commitContains(second, pushedSha)`.

- [ ] Run `ginkgo ./agent/gitcheck/` — expect green (fast-forward, human-delta, and squash-patch-id specs pass against real git).
- [ ] Commit: `git add agent/gitcheck && git commit -m "feat(delivery-outcomes): agent/gitcheck bare-mirror + merge/delta/patch-id git ops"`

---

### Task 7: `agent/gitcheck` — merge detector + file-windowed diff

Composes the Task-6 primitives into the two answers callers actually want: `Detect(base, pushed, branch, target, scanLimit)` returning a nil-or-`MergeResult` (nil = still open), and `FileDiff(base, pushed, offset, limit)` returning the windowed unified diff the API serves. Kept in `gitcheck` so both the watcher and the diff handler use one code path.

**Files:**
- Create: `agent/gitcheck/detect.go`
- Test: `agent/gitcheck/detect_test.go`

**Steps:**

- [ ] Write the failing spec `agent/gitcheck/detect_test.go` (reuses the Task-6 fixture helpers `git`, `botEnv`, `humanEnv`, `setupOrigin`, all in the same `gitcheck_test` package):

```go
package gitcheck_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/agent/gitcheck"
)

var _ = Describe("gitcheck.Detect + FileDiff", func() {
	var tmp, bare, base string

	BeforeEach(func() {
		tmp = GinkgoT().TempDir()
		bare, base = setupOrigin(tmp)
	})

	openMirror := func() *gitcheck.Mirror {
		m, err := gitcheck.OpenMirror(filepath.Join(tmp, "cache"), "tdmtrader/concourse", bare, gitcheck.Auth{})
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Fetch()).To(Succeed())
		return m
	}

	It("returns nil for an open (unmerged) branch", func() {
		ws := filepath.Join(tmp, "ws")
		git(tmp, botEnv, "clone", bare, ws)
		Expect(os.WriteFile(filepath.Join(ws, "f.go"), []byte("package f\n"), 0o644)).To(Succeed())
		git(ws, botEnv, "add", ".")
		git(ws, botEnv, "commit", "-m", "work")
		pushed := git(ws, botEnv, "rev-parse", "HEAD")
		git(ws, botEnv, "push", "origin", "HEAD:refs/heads/agent/ticket-1")

		m := openMirror()
		res, err := m.Detect(base, pushed, "agent/ticket-1", "main", 200)
		Expect(err).NotTo(HaveOccurred())
		Expect(res).To(BeNil())
	})

	It("returns merged (no human commits) for a fast-forward with only bot commits", func() {
		ws := filepath.Join(tmp, "ws")
		git(tmp, botEnv, "clone", bare, ws)
		Expect(os.WriteFile(filepath.Join(ws, "f.go"), []byte("package f\n"), 0o644)).To(Succeed())
		git(ws, botEnv, "add", ".")
		git(ws, botEnv, "commit", "-m", "work")
		pushed := git(ws, botEnv, "rev-parse", "HEAD")
		git(ws, botEnv, "push", "origin", "HEAD:refs/heads/agent/ticket-1")
		git(ws, botEnv, "push", "origin", "HEAD:main")

		m := openMirror()
		res, err := m.Detect(base, pushed, "agent/ticket-1", "main", 200)
		Expect(err).NotTo(HaveOccurred())
		Expect(res).NotTo(BeNil())
		Expect(res.State).To(Equal(gitcheck.StateMerged))
		Expect(res.HumanCommitCount).To(Equal(0))
	})

	It("returns merged_with_fixes when a human commit precedes the merge", func() {
		ws := filepath.Join(tmp, "ws")
		git(tmp, botEnv, "clone", bare, ws)
		Expect(os.WriteFile(filepath.Join(ws, "f.go"), []byte("package f\n"), 0o644)).To(Succeed())
		git(ws, botEnv, "add", ".")
		git(ws, botEnv, "commit", "-m", "work")
		pushed := git(ws, botEnv, "rev-parse", "HEAD")
		Expect(os.WriteFile(filepath.Join(ws, "f.go"), []byte("package f\nvar Fix = 1\n"), 0o644)).To(Succeed())
		git(ws, humanEnv, "add", ".")
		git(ws, humanEnv, "commit", "-m", "human fix")
		git(ws, humanEnv, "push", "origin", "HEAD:refs/heads/agent/ticket-1")
		git(ws, humanEnv, "push", "origin", "HEAD:main")

		m := openMirror()
		res, err := m.Detect(base, pushed, "agent/ticket-1", "main", 200)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.State).To(Equal(gitcheck.StateMergedWithFixes))
		Expect(res.HumanCommitCount).To(Equal(1))
		Expect(res.HumanLinesAdded).To(Equal(1))
	})

	It("windows the diff by file with a has_more flag", func() {
		ws := filepath.Join(tmp, "ws")
		git(tmp, botEnv, "clone", bare, ws)
		for _, f := range []string{"a.go", "b.go", "c.go"} {
			Expect(os.WriteFile(filepath.Join(ws, f), []byte("package x\n"), 0o644)).To(Succeed())
		}
		git(ws, botEnv, "add", ".")
		git(ws, botEnv, "commit", "-m", "three files")
		pushed := git(ws, botEnv, "rev-parse", "HEAD")
		git(ws, botEnv, "push", "origin", "HEAD:refs/heads/agent/ticket-1")

		m := openMirror()
		page, err := m.FileDiff(base, pushed, 0, 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(page.Files).To(HaveLen(2))
		Expect(page.HasMore).To(BeTrue())
		Expect(page.TotalFiles).To(Equal(3))

		page2, err := m.FileDiff(base, pushed, 2, 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(page2.Files).To(HaveLen(1))
		Expect(page2.HasMore).To(BeFalse())
	})
})
```

- [ ] Run to verify it fails: `ginkgo ./agent/gitcheck/` — expected failure: `m.Detect` / `m.FileDiff` undefined.
- [ ] Write `agent/gitcheck/detect.go`:

```go
package gitcheck

import (
	"strconv"
	"strings"
)

// State values mirror the outcome merge-state vocabulary (§1.11) without
// importing agent/api/outcomes (gitcheck is a leaf package).
const (
	StateMerged          = "merged"
	StateMergedWithFixes = "merged_with_fixes"
)

// Result is a detected merge fact (nil from Detect means "still open").
type Result struct {
	State             string // StateMerged | StateMergedWithFixes
	MergedSha         string
	HumanCommitCount  int
	HumanLinesAdded   int
	HumanLinesDeleted int
}

// Detect runs the §1.11.1 heuristics against a freshly-fetched mirror:
// ancestor-primary, then patch-id squash fallback. Returns nil when the
// branch is neither reachable nor patch-id-matched (stays open).
func (m *Mirror) Detect(base, pushed, branch, target string, scanLimit int) (*Result, error) {
	// Primary: reachability.
	anc, err := m.IsAncestor(pushed, target)
	if err != nil {
		return nil, err
	}
	if anc {
		mp, err := m.MergePoint(pushed, target)
		if err != nil {
			return nil, err
		}
		// §1.11.1 fast-forward refinement: MergePoint only knows the target
		// branch, so it falls back to pushed for a fast-forward. Detect owns
		// the agent branch name, so it resolves the branch's remote head as
		// tip-at-merge (the delta window covers human commits that fast-
		// forwarded onto the branch before it merged); if the branch was
		// deleted, the documented fallback is pushed. True merges (a merge
		// commit exists) already carry the correct second-parent tip.
		tip := mp.TipAtMerge
		mergedSha := mp.MergedSha
		if mp.FastForwarded {
			if head, err := m.run(m.dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil && head != "" {
				tip = head
				mergedSha = head // §1.11.1: merged_sha = tip-at-merge for a pure ff
			}
		}
		delta, err := m.HumanDelta(pushed, tip)
		if err != nil {
			return nil, err
		}
		return resultFrom(mergedSha, delta), nil
	}

	// Squash fallback (needs a known base).
	if base != "" {
		branchTip := branch
		if tip, err := m.run(m.dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil && tip != "" {
			branchTip = tip
		}
		match, err := m.PatchIDMatch(base, branchTip, target, scanLimit)
		if err != nil {
			return nil, err
		}
		if match.Found {
			// Human delta still measured on the agent branch (pushed..tip).
			delta, err := m.HumanDelta(pushed, branchTip)
			if err != nil {
				return nil, err
			}
			return resultFrom(match.Sha, delta), nil
		}
	}

	return nil, nil // still open — the honest v1 answer
}

func resultFrom(mergedSha string, d Delta) *Result {
	r := &Result{
		State:             StateMerged,
		MergedSha:         mergedSha,
		HumanCommitCount:  d.CommitCount,
		HumanLinesAdded:   d.LinesAdded,
		HumanLinesDeleted: d.LinesDeleted,
	}
	if d.CommitCount > 0 {
		r.State = StateMergedWithFixes
	}
	return r
}

// DiffFile is one file's unified-diff patch in a windowed diff.
type DiffFile struct {
	Path      string `json:"path"`
	Patch     string `json:"patch"`
	Truncated bool   `json:"truncated,omitempty"`
}

// DiffPage is a file-windowed diff (§1.11.1 diff API contract).
type DiffPage struct {
	Files      []DiffFile `json:"files"`
	Offset     int        `json:"offset"`
	Limit      int        `json:"limit"`
	TotalFiles int        `json:"total_files"`
	HasMore    bool       `json:"has_more"`
}

// perFilePatchCap bounds any single file's patch (§1.11.1: 64 KiB).
const perFilePatchCap = 64 << 10

// FileDiff returns the base..pushed unified diff windowed to [offset, offset+limit)
// files, each capped at perFilePatchCap bytes.
func (m *Mirror) FileDiff(base, pushed string, offset, limit int) (DiffPage, error) {
	if limit <= 0 {
		limit = 50
	}
	names, err := m.run(m.dir, "diff", "--name-only", base+".."+pushed)
	if err != nil {
		return DiffPage{}, err
	}
	all := nonEmptyLines(names)
	page := DiffPage{Offset: offset, Limit: limit, TotalFiles: len(all)}
	if offset >= len(all) {
		page.Files = []DiffFile{}
		return page, nil
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	page.HasMore = end < len(all)
	for _, path := range all[offset:end] {
		patch, err := m.run(m.dir, "diff", base+".."+pushed, "--", path)
		if err != nil {
			return DiffPage{}, err
		}
		df := DiffFile{Path: path, Patch: patch}
		if len(df.Patch) > perFilePatchCap {
			df.Patch = df.Patch[:perFilePatchCap] + "\n... [diff truncated]\n"
			df.Truncated = true
		}
		page.Files = append(page.Files, df)
	}
	return page, nil
}
```

- [ ] Run `ginkgo ./agent/gitcheck/` — expect green (open, merged, merged_with_fixes, and windowed-diff specs pass).
- [ ] Commit: `git add agent/gitcheck && git commit -m "feat(delivery-outcomes): gitcheck merge detector + file-windowed diff"`

---

### Task 8: `GetAgentTicketDiff` handler — windowed diff from the mirror cache

Serves `GET /api/v1/agent/tickets/:ticket_id/diff?offset=&limit=`. Reads the outcome row's `base_sha`/`pushed_sha`, opens the repo's mirror from the shared cache, and returns a `gitcheck.DiffPage`. Returns 404 when the diff API is disabled (no git-dir) or `base_sha` is unknown — the ATC never renders unbounded diffs. This handler lives in `agent/api/outcomes` alongside the outcome handler; it takes a `MirrorProvider` seam so tests inject a fixture mirror.

**Files:**
- Create: `agent/api/outcomes/diff_handler.go`
- Test: `agent/api/outcomes/diff_handler_test.go`

**Steps:**

- [ ] Write the failing test `agent/api/outcomes/diff_handler_test.go`:

```go
package outcomes_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/concourse/concourse/agent/api/outcomes"
	"github.com/concourse/concourse/agent/gitcheck"
)

// stubProvider returns a canned DiffPage, recording the shas it was asked for.
type stubProvider struct {
	page      gitcheck.DiffPage
	err       error
	gotRepo   string
	gotBase   string
	gotPushed string
	gotOffset int
	gotLimit  int
}

func (s *stubProvider) Diff(repo, base, pushed string, offset, limit int) (gitcheck.DiffPage, error) {
	s.gotRepo, s.gotBase, s.gotPushed, s.gotOffset, s.gotLimit = repo, base, pushed, offset, limit
	return s.page, s.err
}

func TestDiffHandlerServesWindow(t *testing.T) {
	os := outcomes.NewMemoryStore()
	_ = os.Ensure(&outcomes.Outcome{TicketID: 1, Repo: "tdmtrader/concourse", Branch: "agent/ticket-1", PushedSha: "head", BaseSha: "base"})
	prov := &stubProvider{page: gitcheck.DiffPage{Files: []gitcheck.DiffFile{{Path: "a.go", Patch: "diff"}}, TotalFiles: 1}}
	h := outcomes.NewDiffHandler(os, prov)

	req := httptest.NewRequest("GET", "/x?offset=0&limit=50", nil)
	req.Form = map[string][]string{":ticket_id": {"1"}}
	rec := httptest.NewRecorder()
	h.GetDiff(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body)
	}
	if prov.gotRepo != "tdmtrader/concourse" || prov.gotBase != "base" || prov.gotPushed != "head" {
		t.Fatalf("provider args: %+v", prov)
	}
	var page gitcheck.DiffPage
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if len(page.Files) != 1 {
		t.Fatalf("page: %+v", page)
	}
}

func TestDiffHandler404WhenBaseUnknown(t *testing.T) {
	os := outcomes.NewMemoryStore()
	_ = os.Ensure(&outcomes.Outcome{TicketID: 1, Repo: "r", Branch: "b", PushedSha: "head"}) // base_sha == ""
	h := outcomes.NewDiffHandler(os, &stubProvider{})
	req := httptest.NewRequest("GET", "/x", nil)
	req.Form = map[string][]string{":ticket_id": {"1"}}
	rec := httptest.NewRecorder()
	h.GetDiff(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
}

func TestDiffHandler404WhenDisabled(t *testing.T) {
	os := outcomes.NewMemoryStore()
	_ = os.Ensure(&outcomes.Outcome{TicketID: 1, Repo: "r", Branch: "b", PushedSha: "head", BaseSha: "base"})
	// nil provider == diff API disabled (no --agent-outcome-git-dir)
	h := outcomes.NewDiffHandler(os, nil)
	req := httptest.NewRequest("GET", "/x", nil)
	req.Form = map[string][]string{":ticket_id": {"1"}}
	rec := httptest.NewRecorder()
	h.GetDiff(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled: code = %d, want 404", rec.Code)
	}
}

func TestDiffHandler502OnGitError(t *testing.T) {
	os := outcomes.NewMemoryStore()
	_ = os.Ensure(&outcomes.Outcome{TicketID: 1, Repo: "r", Branch: "b", PushedSha: "head", BaseSha: "base"})
	h := outcomes.NewDiffHandler(os, &stubProvider{err: errors.New("git exploded")})
	req := httptest.NewRequest("GET", "/x", nil)
	req.Form = map[string][]string{":ticket_id": {"1"}}
	rec := httptest.NewRecorder()
	h.GetDiff(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("git error: code = %d, want 502", rec.Code)
	}
}
```

- [ ] Run `go test ./agent/api/outcomes/` — expect compile failure (`outcomes.NewDiffHandler` / `MirrorProvider` undefined).
- [ ] Write `agent/api/outcomes/diff_handler.go`:

```go
package outcomes

import (
	"net/http"
	"strconv"

	"github.com/concourse/concourse/agent/gitcheck"
)

// MirrorProvider opens/fetches the repo mirror and returns a windowed diff.
// Implemented by the outcome watcher's mirror cache (Task 10). A nil
// provider means the diff API is disabled (no --agent-outcome-git-dir).
type MirrorProvider interface {
	Diff(repo, base, pushed string, offset, limit int) (gitcheck.DiffPage, error)
}

// DiffHandler serves GetAgentTicketDiff.
type DiffHandler struct {
	store    Store
	provider MirrorProvider
}

func NewDiffHandler(store Store, provider MirrorProvider) *DiffHandler {
	return &DiffHandler{store: store, provider: provider}
}

// GetDiff handles GET /api/v1/agent/tickets/:ticket_id/diff.
func (h *DiffHandler) GetDiff(w http.ResponseWriter, r *http.Request) {
	id, ok := ticketID(w, r)
	if !ok {
		return
	}
	if h.provider == nil {
		http.Error(w, "diff API is not enabled", http.StatusNotFound)
		return
	}
	o, found, err := h.store.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found || o.BaseSha == "" || o.PushedSha == "" {
		http.Error(w, "no diff available for ticket", http.StatusNotFound)
		return
	}

	offset := atoiDefault(r.URL.Query().Get("offset"), 0)
	limit := atoiDefault(r.URL.Query().Get("limit"), 50)
	if limit > 200 {
		limit = 200
	}

	page, err := h.provider.Diff(o.Repo, o.BaseSha, o.PushedSha, offset, limit)
	if err != nil {
		http.Error(w, "diff unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return def
	}
	return n
}
```

- [ ] Run `go test ./agent/api/outcomes/` — expect pass.
- [ ] Commit: `git add agent/api/outcomes && git commit -m "feat(delivery-outcomes): GetAgentTicketDiff windowed-diff handler"`

---

### Task 9: `GetAgentTicketReviews` handler — evidence panel data in the GetBuildAgentReviews shape

Returns `[]reviews.BuildReviewResponse` for the ticket's linked reviews, so the Elm evidence decoder (`Concourse.AgentReview.decodeBuildReview`) is reused unchanged. Reuses harvest's `reviews.Store.ListByTicket` plus the same finding/feedback unpacking `reviews.Handler.GetByBuild` does. To avoid duplicating that unpacking logic, this handler asks the reviews package to build the response via a small exported helper added here.

**Files:**
- Create: `agent/api/outcomes/reviews_handler.go`
- Modify: `agent/api/reviews/handler.go` (export the per-record response builder used by `GetByBuild`)
- Test: `agent/api/outcomes/reviews_handler_test.go`

**Steps:**

- [ ] Write the failing test `agent/api/outcomes/reviews_handler_test.go`:

```go
package outcomes_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/concourse/concourse/agent/api/feedback"
	"github.com/concourse/concourse/agent/api/outcomes"
	"github.com/concourse/concourse/agent/api/reviews"
)

func TestReviewsByTicketHandler(t *testing.T) {
	rs := reviews.NewMemoryStore()
	fs := feedback.NewMemoryStore()
	tid := 42
	_ = rs.Upsert(&reviews.StoredReview{
		BuildID: 100, Repo: "tdmtrader/concourse", CommitSha: "abc",
		Score: 8, MaxScore: 10, Pass: true, Summary: "looks good",
		TicketID: &tid,
		Review: json.RawMessage(`{"schema_version":"harvest/1","observations":[{"id":"judge-correctness-1","title":"edge case","category":"judge"}],"proven_issues":[]}`),
	})

	h := outcomes.NewReviewsHandler(rs, fs)
	req := httptest.NewRequest("GET", "/x", nil)
	req.Form = map[string][]string{":ticket_id": {"42"}}
	rec := httptest.NewRecorder()
	h.GetReviews(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body)
	}
	var got []reviews.BuildReviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 review, got %d", len(got))
	}
	if len(got[0].Observations) != 1 || got[0].Observations[0].Category != "judge" {
		t.Fatalf("observation not unpacked: %+v", got[0].Observations)
	}
	if got[0].Review != nil {
		t.Fatal("raw payload must be dropped from the detail response")
	}
}

func TestReviewsByTicketEmpty(t *testing.T) {
	h := outcomes.NewReviewsHandler(reviews.NewMemoryStore(), feedback.NewMemoryStore())
	req := httptest.NewRequest("GET", "/x", nil)
	req.Form = map[string][]string{":ticket_id": {"7"}}
	rec := httptest.NewRecorder()
	h.GetReviews(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if body := rec.Body.String(); body != "[]\n" && body != "[]" {
		t.Fatalf("empty must be [], got %q", body)
	}
}
```

- [ ] Run `go test ./agent/api/outcomes/` — expect compile failure (`outcomes.NewReviewsHandler` undefined; `reviews.BuildResponseFor` undefined).
- [ ] In `agent/api/reviews/handler.go`, export the per-record builder so `GetByBuild` and the ticket handler share one path. Replace the body of the `for _, rec := range recs` loop in `GetByBuild` with a call to a new exported function, and define that function (it is the existing loop body verbatim, lifted out):

```go
// BuildResponseFor unpacks one StoredReview into the detail response shape
// (findings + feedback), dropping the raw payload. Shared by GetByBuild and
// the ticket reviews handler so both produce byte-identical JSON.
func BuildResponseFor(rec StoredReview, feedbackStore feedback.Store) BuildReviewResponse {
	resp := BuildReviewResponse{StoredReview: rec, Feedback: map[string]FindingFeedback{}}

	var payload ReviewPayload
	if err := json.Unmarshal(rec.Review, &payload); err == nil {
		resp.ProvenIssues = decodeFindings(payload.ProvenIssues)
		resp.Observations = decodeFindings(payload.Observations)
		resp.FindingCount = len(resp.ProvenIssues) + len(resp.Observations)
	} else {
		resp.FindingCount = rec.ProvenCount + rec.ObservationCount
	}
	if resp.ProvenIssues == nil {
		resp.ProvenIssues = []Finding{}
	}
	if resp.Observations == nil {
		resp.Observations = []Finding{}
	}

	fbs, err := feedbackStore.GetByReview(rec.Repo, rec.CommitSha)
	if err == nil {
		for _, fb := range fbs {
			resp.Feedback[fb.FindingID] = FindingFeedback{
				Verdict: fb.Verdict, Notes: fb.Notes, Reviewer: fb.Reviewer,
			}
		}
	}
	resp.EvaluatedCount = len(resp.Feedback)
	resp.Review = nil
	return resp
}
```

and rewrite `GetByBuild`'s loop to:

```go
	responses := []BuildReviewResponse{}
	for _, rec := range recs {
		responses = append(responses, BuildResponseFor(rec, h.feedbackStore))
	}
```

- [ ] Run `go test ./agent/api/reviews/` — expect green (the extraction is behaviour-preserving; the existing `GetByBuild` handler_test still passes).
- [ ] Write `agent/api/outcomes/reviews_handler.go`:

```go
package outcomes

import (
	"net/http"

	"github.com/concourse/concourse/agent/api/feedback"
	"github.com/concourse/concourse/agent/api/reviews"
)

// ReviewsHandler serves GetAgentTicketReviews: the ticket's linked reviews
// in the exact GetBuildAgentReviews response shape, so the existing Elm
// evidence decoder is reused unchanged (§1.11.1).
type ReviewsHandler struct {
	reviews  reviews.Store
	feedback feedback.Store
}

func NewReviewsHandler(reviewStore reviews.Store, feedbackStore feedback.Store) *ReviewsHandler {
	return &ReviewsHandler{reviews: reviewStore, feedback: feedbackStore}
}

// GetReviews handles GET /api/v1/agent/tickets/:ticket_id/reviews.
func (h *ReviewsHandler) GetReviews(w http.ResponseWriter, r *http.Request) {
	id, ok := ticketID(w, r)
	if !ok {
		return
	}
	recs, err := h.reviews.ListByTicket(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	responses := []reviews.BuildReviewResponse{}
	for _, rec := range recs {
		responses = append(responses, reviews.BuildResponseFor(rec, h.feedback))
	}
	writeJSON(w, http.StatusOK, responses)
}
```

- [ ] Run `go test ./agent/api/outcomes/ ./agent/api/reviews/` — expect pass.
- [ ] Commit: `git add agent/api/outcomes agent/api/reviews && git commit -m "feat(delivery-outcomes): GetAgentTicketReviews (reuses GetBuildAgentReviews response shape)"`

---

### Task 10: `agent/outcomewatcher` — RunnableComponent + mirror cache (MirrorProvider)

The polling component (never notify-only, per the fork lesson). Each `Run`:
1. Ensures an outcome row for every `needs_review` ticket with a non-empty `branch` and no row, resolving `pushed_sha`/`base_sha` from the newest matching `agent_run_metrics.results.metadata` (branch-head fallback when absent) — §1.11.1 outcome-row creation.
2. For each open row: `git fetch --prune` its mirror once, run `gitcheck.Mirror.Detect`, and on a hit call `store.RecordMerge` then transition the ticket to `merged`/`merged_with_fixes` **through `tickets.Store.Transition`** (single writer). `Touch`es every row scanned.

The `MirrorCache` type doubles as the `outcomes.MirrorProvider` the diff handler consumes — one cache, opened once per repo. The watcher takes a `TicketLister` seam (the subset of `tickets.Store` it needs) plus `metrics.Store`, `outcomes.Store`, and `tickets.Store`; tests drive it with MemoryStores + a fixture `MirrorCache` pointed at a bare origin.

**Files:**
- Create: `agent/outcomewatcher/watcher.go`
- Create: `agent/outcomewatcher/mirror_cache.go`
- Test: `agent/outcomewatcher/watcher_test.go`

**Steps:**

- [ ] Write the failing spec `agent/outcomewatcher/watcher_test.go` (Ginkgo; reuses real-git fixtures like `agent/gitcheck` — a bare origin + working clone built with `os/exec` git; the watcher's mirror cache points at that origin):

```go
package outcomewatcher_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/agent/api/metrics"
	"github.com/concourse/concourse/agent/api/outcomes"
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/outcomewatcher"
	schema "github.com/concourse/concourse/agent/schema"
)

func TestOutcomeWatcher(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Outcome Watcher Suite")
}

var botEnv = []string{
	"GIT_AUTHOR_NAME=concourse-agent[bot]", "GIT_AUTHOR_EMAIL=agent@concourse.local",
	"GIT_COMMITTER_NAME=concourse-agent[bot]", "GIT_COMMITTER_EMAIL=agent@concourse.local",
}

func git(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), botEnv...)
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

var _ = Describe("outcomewatcher", func() {
	var (
		tmp, bare, base, pushed string
		ticketStore             *tickets.MemoryStore
		outcomeStore            *outcomes.MemoryStore
		metricStore             *metrics.MemoryStore
		cache                   *outcomewatcher.MirrorCache
		watcher                 *outcomewatcher.Watcher
		ticketID                int
	)

	BeforeEach(func() {
		tmp = GinkgoT().TempDir()
		bare = filepath.Join(tmp, "origin.git")
		Expect(os.MkdirAll(bare, 0o755)).To(Succeed())
		git(bare, "init", "--bare", "--initial-branch=main")
		seed := filepath.Join(tmp, "seed")
		git(tmp, "clone", bare, seed)
		Expect(os.WriteFile(filepath.Join(seed, "base.txt"), []byte("base\n"), 0o644)).To(Succeed())
		git(seed, "add", ".")
		git(seed, "commit", "-m", "base")
		git(seed, "push", "origin", "HEAD:main")
		base = git(seed, "rev-parse", "HEAD")

		// agent work on a branch
		Expect(os.WriteFile(filepath.Join(seed, "f.go"), []byte("package f\n"), 0o644)).To(Succeed())
		git(seed, "add", ".")
		git(seed, "commit", "-m", "agent work")
		pushed = git(seed, "rev-parse", "HEAD")
		git(seed, "push", "origin", "HEAD:refs/heads/agent/ticket-1")

		ticketStore = tickets.NewMemoryStore()
		outcomeStore = outcomes.NewMemoryStore()
		metricStore = metrics.NewMemoryStore()

		// ticket at needs_review with branch set
		id, err := ticketStore.Create(&tickets.Ticket{Title: "x", Repo: "tdmtrader/concourse", TargetBranch: "main"})
		Expect(err).NotTo(HaveOccurred())
		ticketID = id
		_ = ticketStore.Transition(id, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{})
		_ = ticketStore.Transition(id, tickets.StateQueued, tickets.StateRunning, tickets.TransitionMeta{})
		_ = ticketStore.Transition(id, tickets.StateRunning, tickets.StateNeedsReview, tickets.TransitionMeta{Branch: "agent/ticket-1"})

		// harvest run-metrics carrying the shas
		md, _ := json.Marshal(map[string]any{
			"metadata": map[string]any{"pushed_branch": "agent/ticket-1", "head_sha": pushed, "base_sha": base},
		})
		_ = metricStore.Upsert(&schema.RunMetrics{
			BuildID: 1, PlanID: "0.1", StepName: "harvest", TicketID: &id,
			Status: schema.RunStatusOK, Results: json.RawMessage(md),
		})

		// cache pointed at the bare origin via a url template that ignores {repo}
		cache = outcomewatcher.NewMirrorCache(filepath.Join(tmp, "cache"), bare+"#{repo}", outcomewatcher.Auth{}, 200)
		watcher = outcomewatcher.New(ticketStore, outcomeStore, metricStore, cache)
	})

	It("creates an outcome row from harvest metrics on first tick", func() {
		Expect(watcher.Run(nil)).To(Succeed())
		o, found, _ := outcomeStore.Get(ticketID)
		Expect(found).To(BeTrue())
		Expect(o.PushedSha).To(Equal(pushed))
		Expect(o.BaseSha).To(Equal(base))
		Expect(o.MergeState).To(Equal(outcomes.MergeOpen))
		Expect(o.LastCheckedAt).To(BeNumerically(">", 0))
	})

	It("detects a merge and transitions the ticket through the store", func() {
		// merge the branch to main (fast-forward) then run the watcher
		seed := filepath.Join(tmp, "seed")
		git(seed, "push", "origin", "HEAD:main")

		Expect(watcher.Run(nil)).To(Succeed()) // tick 1: create row
		Expect(watcher.Run(nil)).To(Succeed()) // tick 2: detect merge

		o, _, _ := outcomeStore.Get(ticketID)
		Expect(o.MergeState).To(Equal(outcomes.Merged))

		tk, _, _ := ticketStore.Get(ticketID)
		Expect(tk.State).To(Equal(tickets.StateMerged))
	})

	It("leaves an unmerged branch open", func() {
		Expect(watcher.Run(nil)).To(Succeed())
		Expect(watcher.Run(nil)).To(Succeed())
		o, _, _ := outcomeStore.Get(ticketID)
		Expect(o.MergeState).To(Equal(outcomes.MergeOpen))
		tk, _, _ := ticketStore.Get(ticketID)
		Expect(tk.State).To(Equal(tickets.StateNeedsReview))
	})

	It("re-arms a sent-back row and detects the rework merge with the NEW shas (F6)", func() {
		// tick 1: create the open row for the first push.
		Expect(watcher.Run(nil)).To(Succeed())
		o, _, _ := outcomeStore.Get(ticketID)
		Expect(o.PushedSha).To(Equal(pushed))
		Expect(o.MergeState).To(Equal(outcomes.MergeOpen))

		// human sends the ticket back: disposition closes the row unmerged,
		// then the ticket walks sent_back → queued → running.
		Expect(outcomeStore.SetDisposition(ticketID, outcomes.DispositionInput{
			Disposition: outcomes.DispositionSentBack, Reason: "incomplete", By: "tdmtrader",
		})).To(Succeed())
		o, _, _ = outcomeStore.Get(ticketID)
		Expect(o.MergeState).To(Equal(outcomes.ClosedUnmerged))
		_ = ticketStore.Transition(ticketID, tickets.StateNeedsReview, tickets.StateSentBack, tickets.TransitionMeta{})
		_ = ticketStore.Transition(ticketID, tickets.StateSentBack, tickets.StateQueued, tickets.TransitionMeta{})
		_ = ticketStore.Transition(ticketID, tickets.StateQueued, tickets.StateRunning, tickets.TransitionMeta{})

		// the re-worked branch: the re-dispatched agent pushes a NEW head (bot
		// commit) on top of the first push. A fresh harvest metrics row carries
		// the NEW shas. NOTE (F38): this rework commit becomes the row's new
		// pushed_sha, so it is NOT part of the human-touch delta — the delta
		// window is pushed_sha..tip-at-merge (§1.11.1). merged_with_fixes is
		// earned below by a separate human commit landing AFTER this push.
		seed := filepath.Join(tmp, "seed")
		newBase := pushed // the rework is based on the first push
		Expect(os.WriteFile(filepath.Join(seed, "g.go"), []byte("package g\n"), 0o644)).To(Succeed())
		git(seed, "add", ".")
		git(seed, "commit", "-m", "agent rework")
		reworked := git(seed, "rev-parse", "HEAD")
		Expect(reworked).NotTo(Equal(pushed))
		git(seed, "push", "origin", "HEAD:refs/heads/agent/ticket-1")
		md, _ := json.Marshal(map[string]any{
			"metadata": map[string]any{"pushed_branch": "agent/ticket-1", "head_sha": reworked, "base_sha": newBase},
		})
		_ = metricStore.Upsert(&schema.RunMetrics{
			BuildID: 2, PlanID: "0.1", StepName: "harvest", TicketID: &ticketID,
			Status: schema.RunStatusOK, Results: json.RawMessage(md),
		})

		// ticket is back at needs_review. The branch is deliberately NOT merged
		// yet (F38): Run does seedRows then detectMerges in the SAME tick, so a
		// merge pushed before tick 2 would be detected on the re-arm tick and
		// the open-row assertions below would never observe the re-arm.
		_ = ticketStore.Transition(ticketID, tickets.StateRunning, tickets.StateNeedsReview, tickets.TransitionMeta{Branch: "agent/ticket-1"})

		// tick 2: seedRows must RE-ARM the closed_unmerged/sent_back row back to
		// open with the NEW shas — not `continue` past it. detectMerges finds
		// the branch still unmerged, so the row stays open through the tick.
		Expect(watcher.Run(nil)).To(Succeed())
		o, _, _ = outcomeStore.Get(ticketID)
		Expect(o.MergeState).To(Equal(outcomes.MergeOpen), "row must be re-armed to open")
		Expect(o.PushedSha).To(Equal(reworked), "pushed_sha must be the reworked head")
		Expect(o.BaseSha).To(Equal(newBase))
		Expect(o.Disposition).To(BeEmpty(), "the send-back disposition must be cleared on re-arm")

		// NOW the human reviewer lands a fix commit on top of the reworked push
		// and merges to main. Only this commit sits inside pushed_sha..tip, so
		// merged_with_fixes is genuinely earned (human_commit_count > 0 with a
		// non-empty line delta). Merging the reworked head alone would be plain
		// `merged` — an empty pushed_sha..tip delta (§1.11.1). The branch ref
		// stays at `reworked` and no new metrics row is written, so the row's
		// pushed_sha is not refreshed past the human commit.
		Expect(os.WriteFile(filepath.Join(seed, "g.go"), []byte("package g\n\nvar Fix = 1\n"), 0o644)).To(Succeed())
		git(seed, "add", ".")
		humanFix := exec.Command("git", "commit", "-m", "human fix before merge")
		humanFix.Dir = seed
		humanFix.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Reviewer", "GIT_AUTHOR_EMAIL=rev@example.com",
			"GIT_COMMITTER_NAME=Reviewer", "GIT_COMMITTER_EMAIL=rev@example.com")
		out, err := humanFix.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), "human fix commit: %s", out)
		git(seed, "push", "origin", "HEAD:main")

		// tick 3: detect the merge on the re-armed row with a non-empty
		// pushed_sha..tip human-touch delta.
		Expect(watcher.Run(nil)).To(Succeed())
		o, _, _ = outcomeStore.Get(ticketID)
		Expect(o.MergeState).To(Equal(outcomes.MergedWithFixes))
		Expect(o.PushedSha).To(Equal(reworked))
		Expect(o.BaseSha).To(Equal(newBase))
		Expect(o.HumanCommitCount).To(Equal(1), "only the post-push human fix is in the delta window")
		Expect(o.HumanLinesAdded).To(BeNumerically(">", 0), "merged_with_fixes must carry a non-empty delta")
		tk, _, _ := ticketStore.Get(ticketID)
		Expect(tk.State).To(Equal(tickets.StateMergedWithFixes))
	})

	It("skips merge-detection for a concluded ticket — the row closes as 'concluded' and never flips (flow-decoupling 2026-07-09)", func() {
		// tick 1: create the open row for the pushed spike branch.
		Expect(watcher.Run(nil)).To(Succeed())
		o, _, _ := outcomeStore.Get(ticketID)
		Expect(o.MergeState).To(Equal(outcomes.MergeOpen))

		// human concludes the spike: run finished, reviewed, no merge intended.
		// The disposition closes the row as 'concluded' (NOT closed_unmerged)
		// and the ticket takes the needs_review → concluded edge via the
		// single writer — exactly what the API handler does (Task 5).
		Expect(outcomeStore.SetDisposition(ticketID, outcomes.DispositionInput{
			Disposition: outcomes.DispositionConcluded, Reason: "research_complete", By: "tdmtrader",
		})).To(Succeed())
		o, _, _ = outcomeStore.Get(ticketID)
		Expect(o.MergeState).To(Equal(outcomes.MergeConcluded))
		Expect(ticketStore.Transition(ticketID, tickets.StateNeedsReview, tickets.StateConcluded, tickets.TransitionMeta{})).To(Succeed())

		// someone merges the spike branch upstream anyway (it happens: a
		// reviewer cherry-picks the prototype). The outcome must NOT flip —
		// concluded never waits on, or reacts to, a branch merge.
		seed := filepath.Join(tmp, "seed")
		git(seed, "push", "origin", "HEAD:main")

		// tick 2: the concluded row is off the open work-list (ListOpen) and
		// the ticket is no longer needs_review (seedRows scans needs_review
		// only), so nothing is re-armed and nothing is detected.
		Expect(watcher.Run(nil)).To(Succeed())
		o, _, _ = outcomeStore.Get(ticketID)
		Expect(o.MergeState).To(Equal(outcomes.MergeConcluded), "concluded is terminal — merge-detection is skipped")
		Expect(o.Disposition).To(Equal(outcomes.DispositionConcluded), "the concluded disposition is never cleared")
		tk, _, _ := ticketStore.Get(ticketID)
		Expect(tk.State).To(Equal(tickets.StateConcluded), "the ticket never leaves concluded")
	})

	It("serves the windowed diff via the MirrorProvider seam", func() {
		Expect(watcher.Run(nil)).To(Succeed()) // creates the row + fetches the mirror
		page, err := cache.Diff("tdmtrader/concourse", base, pushed, 0, 50)
		Expect(err).NotTo(HaveOccurred())
		Expect(page.TotalFiles).To(Equal(1))
		Expect(page.Files[0].Path).To(Equal("f.go"))
	})
})
```

> The fixture uses a `url template` of the literal form `<bare>#{repo}`; `MirrorCache.urlFor(repo)` does `strings.ReplaceAll(template, "{repo}", repo)`, so the `#{repo}` suffix is a harmless fragment on a local path — the clone still resolves to the bare origin. In production the template is `https://github.com/{repo}.git`.

- [ ] Run to verify it fails: `ginkgo ./agent/outcomewatcher/` — expected failure: package does not exist.
- [ ] Write `agent/outcomewatcher/mirror_cache.go`:

```go
// Package outcomewatcher polls target repos natively (no webhooks) to record
// merge outcomes and the human-touch delta (spec §9, shared-contracts §1.11.1).
package outcomewatcher

import (
	"strings"
	"sync"

	"github.com/concourse/concourse/agent/gitcheck"
)

// Auth aliases gitcheck.Auth so callers configure one type.
type Auth = gitcheck.Auth

// MirrorCache opens one bare --mirror per repo under a shared dir and is the
// outcomes.MirrorProvider the diff handler consumes.
type MirrorCache struct {
	dir         string
	urlTemplate string
	auth        Auth
	scanLimit   int

	mu      sync.Mutex
	mirrors map[string]*gitcheck.Mirror
}

func NewMirrorCache(dir, urlTemplate string, auth Auth, scanLimit int) *MirrorCache {
	if scanLimit <= 0 {
		scanLimit = 200
	}
	return &MirrorCache{
		dir: dir, urlTemplate: urlTemplate, auth: auth, scanLimit: scanLimit,
		mirrors: map[string]*gitcheck.Mirror{},
	}
}

func (c *MirrorCache) urlFor(repo string) string {
	return strings.ReplaceAll(c.urlTemplate, "{repo}", repo)
}

// mirror returns the (lazily-cloned) mirror for repo.
func (c *MirrorCache) mirror(repo string) (*gitcheck.Mirror, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if m, ok := c.mirrors[repo]; ok {
		return m, nil
	}
	m, err := gitcheck.OpenMirror(c.dir, repo, c.urlFor(repo), c.auth)
	if err != nil {
		return nil, err
	}
	c.mirrors[repo] = m
	return m, nil
}

// FetchAndDetect fetches once and runs the merge heuristics.
func (c *MirrorCache) FetchAndDetect(repo, base, pushed, branch, target string) (*gitcheck.Result, error) {
	m, err := c.mirror(repo)
	if err != nil {
		return nil, err
	}
	if err := m.Fetch(); err != nil {
		return nil, err
	}
	return m.Detect(base, pushed, branch, target, c.scanLimit)
}

// BranchHead returns the remote head of branch (fallback pushed_sha source).
func (c *MirrorCache) BranchHead(repo, branch string) (string, error) {
	m, err := c.mirror(repo)
	if err != nil {
		return "", err
	}
	if err := m.Fetch(); err != nil {
		return "", err
	}
	return m.BranchHead(branch)
}

// Diff implements outcomes.MirrorProvider.
func (c *MirrorCache) Diff(repo, base, pushed string, offset, limit int) (gitcheck.DiffPage, error) {
	m, err := c.mirror(repo)
	if err != nil {
		return gitcheck.DiffPage{}, err
	}
	return m.FileDiff(base, pushed, offset, limit)
}
```

> This adds one method to `gitcheck.Mirror`: `BranchHead(branch string) (string, error)` = `m.run(m.dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)` returning `""` (not an error) when the ref is absent. Add it in `agent/gitcheck/mirror.go` in the same commit and a one-line spec in `mirror_test.go` (`Expect(m.BranchHead("agent/ticket-1")).To(HaveLen(40))`).

- [ ] Write `agent/outcomewatcher/watcher.go`:

```go
package outcomewatcher

import (
	"context"
	"encoding/json"

	"github.com/concourse/concourse/agent/api/metrics"
	"github.com/concourse/concourse/agent/api/outcomes"
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/gitcheck"
)

// Watcher is the RunnableComponent (atc/component.Runnable). It never uses
// notify-only: the ATC schedules Run on a polling interval (fork lesson).
type Watcher struct {
	tickets  tickets.Store
	outcomes outcomes.Store
	metrics  metrics.Store
	cache    *MirrorCache
}

func New(t tickets.Store, o outcomes.Store, m metrics.Store, cache *MirrorCache) *Watcher {
	return &Watcher{tickets: t, outcomes: o, metrics: m, cache: cache}
}

// Run performs one tick: seed outcome rows, then detect merges on open rows.
func (w *Watcher) Run(ctx context.Context) error {
	if err := w.seedRows(); err != nil {
		return err
	}
	return w.detectMerges()
}

// seedRows ensures an outcome row for every needs_review ticket with a branch.
func (w *Watcher) seedRows() error {
	pending, err := w.tickets.List(tickets.ListFilter{State: tickets.StateNeedsReview})
	if err != nil {
		return err
	}
	for _, tk := range pending {
		if tk.Branch == "" {
			continue
		}
		// Ensure is idempotent and does the refresh itself: it inserts a new
		// row, refreshes branch/pushed_sha/base_sha on an existing open row
		// (plain needs_review → queued → needs_review re-dispatch, or a re-push
		// mid-review), AND re-arms a send-back row (closed_unmerged +
		// disposition='sent_back') back to open with fresh shas (F6). We must
		// NOT `continue` on a found row — that was the old bug: the re-worked
		// branch's new pushed_sha/base_sha never got re-resolved, so the human
		// merge on the rework loop was silently lost (spec §9 delta). Terminal
		// merged / abandoned / concluded rows are left untouched by Ensure
		// (and concluded tickets never appear in this needs_review scan).
		pushed, base := w.resolveShas(tk)
		if err := w.outcomes.Ensure(&outcomes.Outcome{
			TicketID: tk.ID, Repo: tk.Repo, Branch: tk.Branch,
			PushedSha: pushed, BaseSha: base,
		}); err != nil {
			return err
		}
	}
	return nil
}

// resolveShas reads pushed/base from the newest harvest metrics row whose
// metadata.pushed_branch matches the ticket branch; falls back to the remote
// branch head for pushed (base stays "" — diff API 404s until known).
func (w *Watcher) resolveShas(tk tickets.Ticket) (pushed, base string) {
	rows, err := w.metrics.ListByTicket(tk.ID)
	if err == nil {
		// newest last (ListByTicket is oldest-first), so walk in reverse
		for i := len(rows) - 1; i >= 0; i-- {
			md := metadataOf(rows[i].Results)
			if md["pushed_branch"] == tk.Branch {
				pushed, _ = md["head_sha"].(string)
				base, _ = md["base_sha"].(string)
				if pushed != "" {
					return pushed, base
				}
			}
		}
	}
	if head, err := w.cache.BranchHead(tk.Repo, tk.Branch); err == nil {
		pushed = head
	}
	return pushed, base
}

func metadataOf(results json.RawMessage) map[string]any {
	if len(results) == 0 {
		return map[string]any{}
	}
	var parsed struct {
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal(results, &parsed); err != nil || parsed.Metadata == nil {
		return map[string]any{}
	}
	return parsed.Metadata
}

// detectMerges runs the heuristics on every open row and records hits.
func (w *Watcher) detectMerges() error {
	open, err := w.outcomes.ListOpen()
	if err != nil {
		return err
	}
	for _, o := range open {
		_ = w.outcomes.Touch(o.TicketID)
		tk, found, err := w.tickets.Get(o.TicketID)
		if err != nil {
			return err
		}
		if !found || tk.State != tickets.StateNeedsReview {
			continue // only detect merges for tickets still awaiting review
		}
		res, err := w.cache.FetchAndDetect(o.Repo, o.BaseSha, o.PushedSha, o.Branch, tk.TargetBranch)
		if err != nil {
			// git/network fault on one repo must not abort the whole tick.
			continue
		}
		if res == nil {
			continue // still open — the honest answer
		}
		if err := w.recordAndTransition(tk, res); err != nil {
			return err
		}
	}
	return nil
}

func (w *Watcher) recordAndTransition(tk *tickets.Ticket, res *gitcheck.Result) error {
	state := outcomes.MergeState(res.State) // "merged" | "merged_with_fixes"
	if err := w.outcomes.RecordMerge(tk.ID, outcomes.MergeResult{
		State: state, MergedSha: res.MergedSha,
		HumanCommitCount: res.HumanCommitCount,
		HumanLinesAdded:  res.HumanLinesAdded,
		HumanLinesDeleted: res.HumanLinesDeleted,
	}); err != nil {
		return err
	}
	// Single-writer discipline: ticket state changes ONLY via Transition.
	to := tickets.StateMerged
	if state == outcomes.MergedWithFixes {
		to = tickets.StateMergedWithFixes
	}
	return w.tickets.Transition(tk.ID, tickets.StateNeedsReview, to, tickets.TransitionMeta{})
}
```

- [ ] Run `ginkgo ./agent/outcomewatcher/` — expect green (row-creation, merge detection + transition, open-stays-open, the send-back re-arm + rework-merge loop (F6), the concluded skip-merge-detection spec, and the diff-provider seam all pass against real git). The concluded spec needs no watcher code change — the skip falls out structurally (`ListOpen` excludes `merge_state='concluded'`; `seedRows` scans `needs_review` only; the `Ensure` re-arm WHERE matches only `closed_unmerged`+`sent_back`) — but it pins that structure: if anyone later broadens `Ensure`'s re-arm or the work-list, this spec fails at `Expect(o.MergeState).To(Equal(outcomes.MergeConcluded))`. Before the `seedRows` re-arm fix (unconditional `continue` on a found row) the F6 spec fails at `Expect(o.MergeState).To(Equal(outcomes.MergeOpen), "row must be re-armed to open")` — the row stays `closed_unmerged` and the rework merge is never recorded. Two orderings in that spec are load-bearing (F38): the merge is pushed only AFTER the tick-2 re-arm assertions (`Run` does seedRows then detectMerges in one tick, so an earlier merge push would be detected on the re-arm tick and hide the open-row state), and the `merged_with_fixes` verdict comes from the post-push "human fix before merge" commit — the only commit in the `pushed_sha..tip` window — never from the rework commit, which IS the new pushed_sha and therefore outside the delta.
- [ ] Commit: `git add agent/outcomewatcher agent/gitcheck && git commit -m "feat(delivery-outcomes): outcome watcher RunnableComponent + mirror cache"`

---

### Task 11: Route registration, auth tiers, ATC + component wiring, web flags

Registers the four routes (contracts §4.2 rows + the two §1.11.1 additive rows), wires them into the exhaustive wrappa switch (all four `authorized`, so they join agent-identity's `CheckAgentAuthorizationHandler` case group), adds `DefaultRoles`, threads the outcome/reviews/diff servers through `atc/api/handler.go`, adds the web-node flag group, constructs the `MirrorCache` + `outcomes` stores in `atc/atccmd/command.go`, and registers the `agent_outcome_watcher` RunnableComponent. The wrappa test (`atc/wrappa/api_auth_wrappa_test.go` "handles each route") iterates every `atc.Routes` entry and panics on a missing case — that panic is this task's failing test.

**Files:**
- Modify: `atc/routes.go` (route-name consts near the `SubmitAgentReview` block :127-129; route entries near :260)
- Modify: `atc/wrappa/api_auth_wrappa.go` (add the four routes to the team-less `authorized` case group agent-identity created via `CheckAgentAuthorizationHandler` — grep `CheckAgentAuthorizationHandler`)
- Modify: `atc/api/accessor/roles.go` (after `atc.GetBuildAgentReviews: ViewerRole` at :114 — four `ViewerRole`/`MemberRole` entries)
- Modify: `atc/api/handler.go` (param list after `agentReviewPublishToken string` :92; server construction after `reviewsServer` :123-139; handlers map after `atc.ListTeamAgentReviews` :277)
- Modify: `atc/api/api_suite_test.go` (NewHandler args — append the outcome-store + userName + provider args in call order)
- Modify: `atc/atccmd/command.go` (flag group in the `RunConfig`/web struct near :216; `NewHandler` args after `cmd.AgentReviewPublishToken` :2299; component registration near the k8s registrar block :1300-1320; `component.Clock`/interval imports as needed)
- Modify: `atc/component.go` (add `ComponentAgentOutcomeWatcher = "agent_outcome_watcher"` to the const block)
- Modify: `docs/superpowers/plans/agentic-platform/agent-route-scopes.md` (add rows for the four routes if that audit doc exists post-wave-1; skip if absent)
- Test: `agent/api/outcomes/route_registration_test.go`

**Steps:**

- [ ] Write the failing route-registration test `agent/api/outcomes/route_registration_test.go` (precedent: `agent/api/feedback/route_registration_test.go`):

```go
package outcomes_test

import (
	"testing"

	"github.com/concourse/concourse/atc"
)

func TestOutcomeRoutesRegistered(t *testing.T) {
	required := []struct{ name, method, path string }{
		{atc.GetAgentTicketOutcome, "GET", "/api/v1/agent/tickets/:ticket_id/outcome"},
		{atc.SetAgentTicketDisposition, "PUT", "/api/v1/agent/tickets/:ticket_id/disposition"},
		{atc.GetAgentTicketDiff, "GET", "/api/v1/agent/tickets/:ticket_id/diff"},
		{atc.GetAgentTicketReviews, "GET", "/api/v1/agent/tickets/:ticket_id/reviews"},
	}
	for _, rr := range required {
		found := false
		for _, route := range atc.Routes {
			if route.Name == rr.name {
				found = true
				if route.Method != rr.method || route.Path != rr.path {
					t.Errorf("route %q: got %s %s, want %s %s", rr.name, route.Method, route.Path, rr.method, rr.path)
				}
			}
		}
		if !found {
			t.Errorf("route %q (%s %s) not registered", rr.name, rr.method, rr.path)
		}
	}
}
```

- [ ] Run to verify it fails: `go test ./agent/api/outcomes/ -run TestOutcomeRoutesRegistered` — expected failure: `undefined: atc.GetAgentTicketOutcome`.
- [ ] Add the route-name constants to `atc/routes.go` after the `UpdateAgentTicketTask` const (added by ticket-core; grep it):

```go
	GetAgentTicketOutcome     = "GetAgentTicketOutcome"
	SetAgentTicketDisposition = "SetAgentTicketDisposition"
	GetAgentTicketDiff        = "GetAgentTicketDiff"
	GetAgentTicketReviews     = "GetAgentTicketReviews"
```

and the route entries to `atc.Routes` after the ticket-core `.../tasks/:ordering` entry:

```go
	{Path: "/api/v1/agent/tickets/:ticket_id/outcome", Method: "GET", Name: GetAgentTicketOutcome},
	{Path: "/api/v1/agent/tickets/:ticket_id/disposition", Method: "PUT", Name: SetAgentTicketDisposition},
	{Path: "/api/v1/agent/tickets/:ticket_id/diff", Method: "GET", Name: GetAgentTicketDiff},
	{Path: "/api/v1/agent/tickets/:ticket_id/reviews", Method: "GET", Name: GetAgentTicketReviews},
```

- [ ] Run `go test ./agent/api/outcomes/ -run TestOutcomeRoutesRegistered` — expect PASS. Then `ginkgo ./atc/wrappa/` — expected failure: panic `you missed a spot: "GetAgentTicketOutcome"`.
- [ ] In `atc/wrappa/api_auth_wrappa.go`, add all four routes to the team-less `authorized` case group agent-identity landed on `CheckAgentAuthorizationHandler` (grep the string; add adjacent to the ticket-core `authorized` ticket routes — `GetAgentTicketOutcome`, `GetAgentTicketDiff`, `GetAgentTicketReviews` are viewer, `SetAgentTicketDisposition` is member; both viewer and member live in the same `CheckAgentAuthorizationHandler` case group — the role is enforced by `DefaultRoles`, not by a separate wrappa case):

```go
			atc.GetAgentTicketOutcome,
			atc.SetAgentTicketDisposition,
			atc.GetAgentTicketDiff,
			atc.GetAgentTicketReviews,
```

- [ ] In `atc/api/accessor/roles.go` after `atc.GetBuildAgentReviews: ViewerRole` (:114; note the file comment that a missing entry silently becomes admin-only):

```go
	atc.GetAgentTicketOutcome:     ViewerRole,
	atc.GetAgentTicketDiff:        ViewerRole,
	atc.GetAgentTicketReviews:     ViewerRole,
	atc.SetAgentTicketDisposition: MemberRole,
```

- [ ] Run `ginkgo ./atc/wrappa/ ./atc/api/accessor/` — expect green (every route now has a case + role).
- [ ] Add `ComponentAgentOutcomeWatcher = "agent_outcome_watcher"` to the const block in `atc/component.go` (alongside `ComponentK8sWorkerReaper`).
- [ ] Add the web-node flag group to the ATC web command struct in `atc/atccmd/command.go` (near the other `group:` blocks around :216; §1.11.1 flag names verbatim):

```go
	AgentOutcome struct {
		GitDir          string        `long:"agent-outcome-git-dir" description:"Directory for bare --mirror git caches used by the outcome watcher and ticket diff API. Empty disables both."`
		GitURLTemplate  string        `long:"agent-outcome-git-url-template" default:"https://github.com/{repo}.git" description:"Clone URL template; {repo} is the canonical owner/name slug."`
		GitUsername     string        `long:"agent-outcome-git-username" description:"Optional https fetch username (injected via a temp credential store, never argv)."`
		GitToken        string        `long:"agent-outcome-git-token" description:"Optional https fetch token."`
		CheckInterval   time.Duration `long:"agent-outcome-check-interval" default:"5m" description:"Polling interval for the outcome watcher (never notify-only)."`
		SquashScanLimit int           `long:"agent-outcome-squash-scan-limit" default:"200" description:"Newest first-parent commits on the target branch scanned for a squash-merge patch-id match."`
	} `group:"Agent Outcome Watcher"`
```

- [ ] In `atc/api/handler.go`: add the constructor params after `agentReviewPublishToken string` (:92):

```go
	outcomesStore outcomes.Store,
	agentUserName outcomes.UserNameFunc,
	outcomeDiffProvider outcomes.MirrorProvider,
```

import `outcomesapi "github.com/concourse/concourse/agent/api/outcomes"` (alias to avoid clashing with any local var), construct the three servers after `reviewsServer` (:139):

```go
	outcomesServer := outcomesapi.NewHandler(outcomesStore, ticketsStore, agentUserName)
	outcomeDiffServer := outcomesapi.NewDiffHandler(outcomesStore, outcomeDiffProvider)
	outcomeReviewsServer := outcomesapi.NewReviewsHandler(reviewsStore, feedbackStore)
```

(`ticketsStore` is the `tickets.Store` param ticket-core added to `NewHandler` in wave 2 — reuse it; if the param is named differently, use that name.) Then the handlers map after `atc.ListTeamAgentReviews` (:277):

```go
		atc.GetAgentTicketOutcome:     http.HandlerFunc(outcomesServer.GetOutcome),
		atc.SetAgentTicketDisposition: http.HandlerFunc(outcomesServer.SetDisposition),
		atc.GetAgentTicketDiff:        http.HandlerFunc(outcomeDiffServer.GetDiff),
		atc.GetAgentTicketReviews:     http.HandlerFunc(outcomeReviewsServer.GetReviews),
```

- [ ] In `atc/atccmd/command.go`, build the mirror cache + stores and pass them to `NewHandler` after `cmd.AgentReviewPublishToken` (:2299). The provider is nil when the master switch is off:

```go
		db.NewAgentOutcomesFactory(dbConn),
		func(r *http.Request) string { return accessor.GetAccessor(r).Claims().UserName },
		outcomeDiffProvider, // *outcomewatcher.MirrorCache or nil
```

where `outcomeDiffProvider` is computed once earlier in this method (next to where other web-scoped helpers are built):

```go
	var outcomeMirrorCache *outcomewatcher.MirrorCache
	if cmd.AgentOutcome.GitDir != "" {
		outcomeMirrorCache = outcomewatcher.NewMirrorCache(
			cmd.AgentOutcome.GitDir,
			cmd.AgentOutcome.GitURLTemplate,
			outcomewatcher.Auth{Username: cmd.AgentOutcome.GitUsername, Token: cmd.AgentOutcome.GitToken},
			cmd.AgentOutcome.SquashScanLimit,
		)
	}
	var outcomeDiffProvider outcomes.MirrorProvider
	if outcomeMirrorCache != nil {
		outcomeDiffProvider = outcomeMirrorCache
	}
```

(assigning through the typed nil-able var keeps `outcomeDiffProvider` a true nil interface when disabled, so the diff handler's `provider == nil` check works — do NOT assign a typed-nil `*MirrorCache` directly.)

- [ ] Register the component where the other RunnableComponents are appended (near the k8s registrar block :1300-1320), gated on the master switch:

```go
	if cmd.AgentOutcome.GitDir != "" && outcomeMirrorCache != nil {
		components = append(components, RunnableComponent{
			Component: atc.Component{
				Name: atc.ComponentAgentOutcomeWatcher,
			},
			Runnable: outcomewatcher.New(
				db.NewAgentTicketsFactory(dbConn),
				db.NewAgentOutcomesFactory(dbConn),
				db.NewAgentRunMetricsFactory(dbConn),
				outcomeMirrorCache,
			),
			Interval: cmd.AgentOutcome.CheckInterval,
		})
	}
```

- [ ] Update `atc/api/api_suite_test.go`: append the new `NewHandler` args in call order — `outcomes.NewMemoryStore()`, a `func(*http.Request) string { return "test" }`, and `nil` (diff disabled in the API suite unless a specific test injects a provider).
- [ ] Run to verify: `go build ./... && ginkgo ./atc/wrappa/ ./atc/api/accessor/ && go test ./agent/api/outcomes/` — expect green.
- [ ] Commit: `git add atc agent docs && git commit -m "feat(delivery-outcomes): register outcome/diff/reviews routes + outcome-watcher component + web flags"`

---

### Task 12: go-concourse client + `fly agent tickets dispose`

A `SetAgentTicketDisposition` client method and a `fly agent tickets dispose` subcommand so a human can send-back/abandon/conclude from the CLI (the disposition still rides the API → transition function). Follows the `agent_tickets.go` client idiom (Task 10 of ticket-core) and the shared `AgentCommand` struct (credentials-and-budgets addendum: wave-mates append subcommand fields).

**Files:**
- Modify: `go-concourse/concourse/client.go` (add `SetAgentTicketDisposition` to the `Client` interface, after the ticket-core methods)
- Create: `go-concourse/concourse/agent_outcomes.go`
- Modify (regenerated): `go-concourse/concourse/concoursefakes/fake_client.go`
- Modify: `fly/commands/agent.go` (append a `Dispose` field to the `AgentTicketsCommand` struct ticket-core created — or to `AgentCommand` if ticket-core nested tickets subcommands directly)
- Create: `fly/commands/agent_ticket_dispose.go`
- Test: `go-concourse/concourse/agent_outcomes_test.go`, `fly/integration/agent_tickets_dispose_test.go`

**Steps:**

- [ ] Write the failing go-concourse spec `go-concourse/concourse/agent_outcomes_test.go` (idiom: `agent_tickets_test.go`; `atcServer` is the suite's ghttp server, `client` the suite client):

```go
package concourse_test

import (
	"net/http"

	"github.com/concourse/concourse/agent/api/outcomes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("SetAgentTicketDisposition", func() {
	It("PUTs the disposition and returns the outcome", func() {
		expected := outcomes.Outcome{TicketID: 7, MergeState: outcomes.ClosedUnmerged, Disposition: "sent_back"}
		atcServer.AppendHandlers(
			ghttp.CombineHandlers(
				ghttp.VerifyRequest("PUT", "/api/v1/agent/tickets/7/disposition"),
				ghttp.VerifyJSONRepresenting(map[string]string{
					"disposition": "sent_back", "reason": "incomplete", "notes": "more tests",
				}),
				ghttp.RespondWithJSONEncoded(http.StatusOK, expected),
			),
		)
		got, err := client.SetAgentTicketDisposition(7, outcomes.DispositionRequestFor("sent_back", "incomplete", "more tests"))
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Disposition).To(Equal("sent_back"))
	})
})
```

> `outcomes.DispositionRequestFor(disposition, reason, notes)` is a tiny constructor returning `DispositionRequest` — add it to `agent/api/outcomes/handler.go` next to the `DispositionRequest` type (three-line helper) so both the client and tests build the body without duplicating field names.

- [ ] Run to verify it fails: `ginkgo ./go-concourse/concourse/` — expected failure: `client.SetAgentTicketDisposition undefined`.
- [ ] Add to the `Client` interface in `go-concourse/concourse/client.go` (after the ticket-core methods; import `agent/api/outcomes`):

```go
	SetAgentTicketDisposition(ticketID int, req outcomes.DispositionRequest) (outcomes.Outcome, error)
```

- [ ] Create `go-concourse/concourse/agent_outcomes.go`:

```go
package concourse

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/concourse/concourse/agent/api/outcomes"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/go-concourse/concourse/internal"
	"github.com/tedsuo/rata"
)

func (client *client) SetAgentTicketDisposition(ticketID int, req outcomes.DispositionRequest) (outcomes.Outcome, error) {
	buffer := &bytes.Buffer{}
	if err := json.NewEncoder(buffer).Encode(req); err != nil {
		return outcomes.Outcome{}, err
	}
	var result outcomes.Outcome
	err := client.connection.Send(internal.Request{
		RequestName: atc.SetAgentTicketDisposition,
		Params:      rata.Params{"ticket_id": strconv.Itoa(ticketID)},
		Body:        buffer,
		Header:      http.Header{"Content-Type": []string{"application/json"}},
	}, &internal.Response{
		Result: &result,
	})
	return result, err
}
```

- [ ] Regenerate the client fake: `cd go-concourse/concourse && go run github.com/maxbrunsfeld/counterfeiter/v6 -o concoursefakes/fake_client.go . Client && cd ../..`
- [ ] Run to verify pass: `ginkgo ./go-concourse/concourse/ && go build ./go-concourse/...` — expect green.
- [ ] Write the failing fly integration spec `fly/integration/agent_tickets_dispose_test.go` (idiom: the ticket-core `fly agent tickets` specs; the suite builds the fly binary against a mock ATC):

```go
package integration_test

import (
	"os/exec"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
	"net/http"
)

var _ = Describe("fly agent tickets dispose", func() {
	It("sends a disposition", func() {
		atcServer.AppendHandlers(
			ghttp.CombineHandlers(
				ghttp.VerifyRequest("PUT", "/api/v1/agent/tickets/7/disposition"),
				ghttp.RespondWithJSONEncoded(http.StatusOK, map[string]any{
					"ticket_id": 7, "merge_state": "closed_unmerged", "disposition": "sent_back",
				}),
			),
		)
		flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "dispose",
			"--ticket", "7", "--disposition", "sent_back", "--reason", "incomplete", "--notes", "more tests")
		sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())
		Eventually(sess).Should(gexec.Exit(0))
		Expect(sess.Out).To(gbytes.Say("sent_back"))
	})
})
```

- [ ] Run to verify it fails: `ginkgo ./fly/integration/ --focus="fly agent tickets dispose"` — expected failure: `Unknown command 'dispose'`.
- [ ] Add the `Dispose` subcommand field to the tickets command struct in `fly/commands/agent.go` (the `AgentTicketsCommand` ticket-core created; match its field style):

```go
	Dispose AgentTicketDisposeCommand `command:"dispose" description:"Send back, abandon, or conclude a ticket with a reason"`
```

- [ ] Create `fly/commands/agent_ticket_dispose.go`:

```go
package commands

import (
	"fmt"

	"github.com/concourse/concourse/agent/api/outcomes"
	"github.com/concourse/concourse/fly/commands/internal/flaghelpers"
	"github.com/concourse/concourse/fly/rc"
)

type AgentTicketDisposeCommand struct {
	Ticket      int    `long:"ticket" required:"true" description:"Ticket id"`
	Disposition string `long:"disposition" required:"true" choice:"sent_back" choice:"abandoned" choice:"concluded" description:"Disposition (concluded = run reviewed, no merge intended — spike/research)"`
	Reason      string `long:"reason" required:"true" description:"Reason taxonomy value (wrong_approach|incomplete|defective|superseded|not_needed|style|research_complete|other)"`
	Notes       string `long:"notes" description:"Free-text notes"`
}

func (command *AgentTicketDisposeCommand) Execute(args []string) error {
	target, err := rc.LoadTarget(Fly.Target, false)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}
	if !outcomes.ValidDispositionReason(command.Reason) {
		return fmt.Errorf("invalid reason %q", command.Reason)
	}
	out, err := target.Client().SetAgentTicketDisposition(
		command.Ticket,
		outcomes.DispositionRequestFor(command.Disposition, command.Reason, command.Notes),
	)
	if err != nil {
		return err
	}
	fmt.Printf("ticket %d: %s (%s)\n", out.TicketID, out.Disposition, out.MergeState)
	return nil
}
```

> `flaghelpers` import is a placeholder in case the command needs shared flag helpers; drop it if `goimports` flags it unused. `Fly.Target` / `rc.LoadTarget` / `target.Client()` follow the existing fly command recipe (grep any `fly/commands/*.go` that calls `target.Client()`).

- [ ] Run to verify pass: `ginkgo ./fly/integration/ --focus="fly agent tickets dispose"` — expect green (the mock ATC version must match `versions.go`, currently `0.1.0`, per CLAUDE.md).
- [ ] Commit: `git add go-concourse fly agent && git commit -m "feat(delivery-outcomes): fly agent tickets dispose + go-concourse client"`

---

### Task 13: Elm data layer — outcome / diff / reviews / cost decoders, endpoints, effects, callbacks

Extends ticket-core's Elm data layer with the API surfaces the PR view consumes. The evidence panel reuses `Concourse.AgentReview.decodeBuildReview` verbatim (the `GetAgentTicketReviews` response IS the `GetBuildAgentReviews` shape). New decoders: `Outcome`, the windowed `DiffPage`, and the cost `RollupResponse` (charter PR-view item "cost from ledger rollups" — served by credentials-and-budgets' existing `GetAgentCostRollup` route `GET /api/v1/agent/costs?group_by=ticket`; **no new backend**, and since credentials-and-budgets shipped no Elm surface the decoder + endpoint are owned here).

**Files:**
- Create: `web/elm/src/Concourse/AgentOutcome.elm`
- Modify: `web/elm/src/Api/Endpoints.elm` (variants after `AgentTicket Int`; path cases after `AgentTicket id ->`)
- Modify: `web/elm/src/Message/Effects.elm` (effect variants after `FetchAgentTicket Int`; interpretations)
- Modify: `web/elm/src/Message/Callback.elm` (callback variants after `AgentTicketFetched`)
- Test: `web/elm/tests/ApiEndpointsTests.elm` (append endpoint tests)

**Steps:**

- [ ] Append the failing endpoint tests to the list in `web/elm/tests/ApiEndpointsTests.elm` (match the file's existing `test`/`expectPath` style):

```elm
        , test "AgentTicketOutcome path" <|
            \_ ->
                Endpoints.AgentTicketOutcome 12
                    |> expectPath "/api/v1/agent/tickets/12/outcome"
        , test "AgentTicketDiff path" <|
            \_ ->
                Endpoints.AgentTicketDiff 12
                    |> expectPath "/api/v1/agent/tickets/12/diff"
        , test "AgentTicketReviews path" <|
            \_ ->
                Endpoints.AgentTicketReviews 12
                    |> expectPath "/api/v1/agent/tickets/12/reviews"
        , test "AgentTicketDisposition path" <|
            \_ ->
                Endpoints.AgentTicketDisposition 12
                    |> expectPath "/api/v1/agent/tickets/12/disposition"
        , test "AgentCostRollup path" <|
            \_ ->
                Endpoints.AgentCostRollup
                    |> expectPath "/api/v1/agent/costs"
```

(if the file's assertion helper is not named `expectPath`, use whatever the surrounding tests use — grep the existing `AgentFeedback`/`AgentTicket` endpoint test. `AgentCostRollup` carries no path param; the `?group_by=ticket` query is added by the effect, not the endpoint path.)

- [ ] Run to verify it fails: `cd web/elm && ../../node_modules/.bin/elm-test tests/ApiEndpointsTests.elm` — expected failure: `Endpoints.AgentTicketOutcome` not found.
- [ ] Create `web/elm/src/Concourse/AgentOutcome.elm`:

```elm
module Concourse.AgentOutcome exposing
    ( CostRollup
    , CostRow
    , DiffFile
    , DiffPage
    , Outcome
    , decodeCostRollup
    , decodeDiffPage
    , decodeOutcome
    , dispositionReasons
    )

import Json.Decode
import Json.Decode.Extra exposing (andMap)


-- dispositionReasons is the §1.11 taxonomy, matching outcomes.DispositionReasons order.
-- research_complete: spike/research complete, no merge intended (pairs with 'concluded').
dispositionReasons : List String
dispositionReasons =
    [ "wrong_approach", "incomplete", "defective", "superseded", "not_needed", "style", "research_complete", "other" ]


type alias Outcome =
    { ticketId : Int
    , repo : String
    , branch : String
    , pushedSha : String
    , baseSha : String
    , mergeState : String
    , mergedSha : String
    , mergedAt : Int
    , humanCommitCount : Int
    , humanLinesAdded : Int
    , humanLinesDeleted : Int
    , disposition : String
    , dispositionReason : String
    , dispositionNotes : String
    , disposedBy : String
    }


type alias DiffFile =
    { path : String
    , patch : String
    , truncated : Bool
    }


type alias DiffPage =
    { files : List DiffFile
    , offset : Int
    , limit : Int
    , totalFiles : Int
    , hasMore : Bool
    }


defaultTo : a -> Json.Decode.Decoder a -> Json.Decode.Decoder a
defaultTo default =
    Json.Decode.maybe >> Json.Decode.map (Maybe.withDefault default)


decodeOutcome : Json.Decode.Decoder Outcome
decodeOutcome =
    Json.Decode.succeed Outcome
        |> andMap (Json.Decode.field "ticket_id" Json.Decode.int)
        |> andMap (defaultTo "" <| Json.Decode.field "repo" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "branch" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "pushed_sha" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "base_sha" Json.Decode.string)
        |> andMap (defaultTo "open" <| Json.Decode.field "merge_state" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "merged_sha" Json.Decode.string)
        |> andMap (defaultTo 0 <| Json.Decode.field "merged_at" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "human_commit_count" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "human_lines_added" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "human_lines_deleted" Json.Decode.int)
        |> andMap (defaultTo "" <| Json.Decode.field "disposition" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "disposition_reason" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "disposition_notes" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "disposed_by" Json.Decode.string)


decodeDiffFile : Json.Decode.Decoder DiffFile
decodeDiffFile =
    Json.Decode.map3 DiffFile
        (Json.Decode.field "path" Json.Decode.string)
        (defaultTo "" <| Json.Decode.field "patch" Json.Decode.string)
        (defaultTo False <| Json.Decode.field "truncated" Json.Decode.bool)


decodeDiffPage : Json.Decode.Decoder DiffPage
decodeDiffPage =
    Json.Decode.succeed DiffPage
        |> andMap (defaultTo [] <| Json.Decode.field "files" (Json.Decode.list decodeDiffFile))
        |> andMap (defaultTo 0 <| Json.Decode.field "offset" Json.Decode.int)
        |> andMap (defaultTo 50 <| Json.Decode.field "limit" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "total_files" Json.Decode.int)
        |> andMap (defaultTo False <| Json.Decode.field "has_more" Json.Decode.bool)


-- CostRow / CostRollup mirror credentials-and-budgets' RollupResponse
-- (§2.7 RollupRow + the {group_by, summary, rows} envelope). The PR view
-- reads the row whose key == the ticket id from a ?group_by=ticket query.
type alias CostRow =
    { key : String
    , entries : Int
    , inputTokens : Int
    , outputTokens : Int
    , turns : Int
    , costUsd : Float
    }


type alias CostRollup =
    { rows : List CostRow }


decodeCostRow : Json.Decode.Decoder CostRow
decodeCostRow =
    Json.Decode.succeed CostRow
        |> andMap (defaultTo "" <| Json.Decode.field "key" Json.Decode.string)
        |> andMap (defaultTo 0 <| Json.Decode.field "entries" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "input_tokens" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "output_tokens" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "turns" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "cost_usd" Json.Decode.float)


decodeCostRollup : Json.Decode.Decoder CostRollup
decodeCostRollup =
    Json.Decode.map CostRollup
        (defaultTo [] <| Json.Decode.field "rows" (Json.Decode.list decodeCostRow))
```

- [ ] In `web/elm/src/Api/Endpoints.elm`: add the variants after `| AgentTicket Int`:

```elm
    | AgentTicketOutcome Int
    | AgentTicketDiff Int
    | AgentTicketReviews Int
    | AgentTicketDisposition Int
    | AgentCostRollup
```

and the path cases after `AgentTicket id ->`:

```elm
        AgentTicketOutcome id ->
            base |> appendPath [ "agent", "tickets", String.fromInt id, "outcome" ]

        AgentTicketDiff id ->
            base |> appendPath [ "agent", "tickets", String.fromInt id, "diff" ]

        AgentTicketReviews id ->
            base |> appendPath [ "agent", "tickets", String.fromInt id, "reviews" ]

        AgentTicketDisposition id ->
            base |> appendPath [ "agent", "tickets", String.fromInt id, "disposition" ]

        AgentCostRollup ->
            base |> appendPath [ "agent", "costs" ]
```

- [ ] Run to verify pass: `cd web/elm && ../../node_modules/.bin/elm-test tests/ApiEndpointsTests.elm` — expect green.
- [ ] In `web/elm/src/Message/Callback.elm`: import `Concourse.AgentOutcome` and `Concourse.AgentReview`, add after `| AgentTicketFetched ...`:

```elm
    | AgentTicketOutcomeFetched (Fetched Concourse.AgentOutcome.Outcome)
    | AgentTicketDiffFetched (Fetched Concourse.AgentOutcome.DiffPage)
    | AgentTicketReviewsFetched (Fetched (List Concourse.AgentReview.BuildReview))
    | AgentTicketDispositionSet (Fetched Concourse.AgentOutcome.Outcome)
    | AgentTicketCostFetched (Fetched Concourse.AgentOutcome.CostRollup)
```

- [ ] In `web/elm/src/Message/Effects.elm`: import `Concourse.AgentOutcome`, add the effect variants after `| FetchAgentTicket Int`:

```elm
    | FetchAgentTicketOutcome Int
    | FetchAgentTicketDiff Int Int Int
    | FetchAgentTicketReviews Int
    | FetchAgentTicketCost Int
    | SetAgentTicketDisposition Int { disposition : String, reason : String, notes : String }
```

and their interpretations (next to the `FetchAgentTicket` interpretation; the `get`/`put` helpers, `Endpoints.*`, and the callback constructors follow the file's existing agent-ticket idiom — `FetchAgentTicketDiff id offset limit` appends `?offset=&limit=` query params via the file's query-building helper):

```elm
        FetchAgentTicketOutcome id ->
            get (Endpoints.AgentTicketOutcome id) []
                (Concourse.AgentOutcome.decodeOutcome
                    |> Json.Decode.map (AgentTicketOutcomeFetched << Ok)
                )

        FetchAgentTicketDiff id offset limit ->
            get (Endpoints.AgentTicketDiff id)
                [ ( "offset", String.fromInt offset ), ( "limit", String.fromInt limit ) ]
                (Concourse.AgentOutcome.decodeDiffPage
                    |> Json.Decode.map (AgentTicketDiffFetched << Ok)
                )

        FetchAgentTicketReviews id ->
            get (Endpoints.AgentTicketReviews id) []
                (Json.Decode.list Concourse.AgentReview.decodeBuildReview
                    |> Json.Decode.map (AgentTicketReviewsFetched << Ok)
                )

        FetchAgentTicketCost id ->
            get (Endpoints.AgentCostRollup)
                [ ( "group_by", "ticket" ) ]
                (Concourse.AgentOutcome.decodeCostRollup
                    |> Json.Decode.map (AgentTicketCostFetched << Ok)
                )

        SetAgentTicketDisposition id body ->
            put (Endpoints.AgentTicketDisposition id)
                (Http.jsonBody (encodeDisposition body))
                (Concourse.AgentOutcome.decodeOutcome
                    |> Json.Decode.map (AgentTicketDispositionSet << Ok)
                )
```

> Match the actual `get`/`put` signatures already used for `FetchAgentTicket` and `SaveAgentTicket` in this file — the sketch above shows the intent (endpoint, query, decoder→callback). Add an `encodeDisposition` JSON encoder (`{ disposition, reason, notes }`) beside the other encoders in this module. If the existing agent-ticket effects decode via `Callback.AgentTicketFetched` using a `Fetched` wrapper with error handling, mirror that error path exactly (map to `<< Err` on failure), don't hand-roll a new one.

- [ ] Run to verify everything still compiles: `cd web/elm && ../../node_modules/.bin/elm-test` — expect the full suite green (no page consumes the new effects/callbacks yet; unused constructors compile).
- [ ] Commit: `git add web/elm && git commit -m "feat(delivery-outcomes): Elm data layer for outcome, diff, reviews, cost, disposition"`

---

### Task 14: Elm PR view — diff, outcome badge, judge score, evidence panel on the ticket page

Extends ticket-core's `AgentTickets/AgentTicket.elm` page with a PR-view section rendered below the existing title/body/task-list: an outcome/merge badge (with human-touch delta when merged-with-fixes), a paginated diff (load-more using the `has_more` flag), the review-evidence panel via `Build.AgentReview.view` (verbatim), and the judge score already carried on each `BuildReview.info.score`. On page load and on each 5s clock tick (the ticket-core subscription), the page also fetches the outcome + reviews + first diff window.

**Files:**
- Modify: `web/elm/src/AgentTickets/AgentTicket.elm` (Model fields, init/load effects, clock-tick effects, `handleCallback`, view section)
- Modify: `web/elm/src/Message/Message.elm` (page messages for load-more diff + disposition UI — some added in Task 15)
- Test: `web/elm/tests/AgentTicketPRViewTests.elm`
- Modify (generated): `web/public/elm.js`, `web/public/elm.min.js` (bundle rebuild)

**Steps:**

- [ ] Write the failing page test `web/elm/tests/AgentTicketPRViewTests.elm` (idiom: `AgentTicketPageTests.elm` from ticket-core):

```elm
module AgentTicketPRViewTests exposing (all)

import Application.Application as Application
import Common
import Message.Callback as Callback
import Message.Effects as Effects
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (class, containing, text)


loadedPage : Application.Model
loadedPage =
    Common.init "/agent-tickets/12"
        |> Application.handleCallback
            (Callback.AgentTicketOutcomeFetched
                (Ok
                    { ticketId = 12
                    , repo = "tdmtrader/concourse"
                    , branch = "agent/ticket-12"
                    , pushedSha = "abc"
                    , baseSha = "base"
                    , mergeState = "merged_with_fixes"
                    , mergedSha = "m"
                    , mergedAt = 1
                    , humanCommitCount = 1
                    , humanLinesAdded = 4
                    , humanLinesDeleted = 2
                    , disposition = ""
                    , dispositionReason = ""
                    , dispositionNotes = ""
                    , disposedBy = ""
                    }
                )
            )
        |> Tuple.first
        |> Application.handleCallback
            (Callback.AgentTicketDiffFetched
                (Ok
                    { files = [ { path = "f.go", patch = "@@ diff @@", truncated = False } ]
                    , offset = 0
                    , limit = 50
                    , totalFiles = 1
                    , hasMore = False
                    }
                )
            )
        |> Tuple.first


all : Test
all =
    describe "agent ticket PR view"
        [ test "fetches outcome, reviews, and diff on load" <|
            \_ ->
                Common.init "/agent-tickets/12"
                    |> Tuple.second
                    |> Expect.all
                        [ Common.contains (Effects.FetchAgentTicketOutcome 12)
                        , Common.contains (Effects.FetchAgentTicketReviews 12)
                        , Common.contains (Effects.FetchAgentTicketDiff 12 0 50)
                        , Common.contains (Effects.FetchAgentTicketCost 12)
                        ]
        , test "renders the merge badge with the human-touch delta" <|
            \_ ->
                loadedPage
                    |> Common.queryView
                    |> Query.find [ class "ticket-outcome-badge" ]
                    |> Query.has [ text "merged with fixes", text "+4", text "-2" ]
        , test "renders the paginated diff" <|
            \_ ->
                loadedPage
                    |> Common.queryView
                    |> Query.find [ class "ticket-diff" ]
                    |> Query.has [ containing [ text "f.go" ], containing [ text "@@ diff @@" ] ]
        ]
```

(add `import Expect` to the test; match `AgentTicketPageTests.elm`'s import set.)

- [ ] Run to verify it fails: `cd web/elm && ../../node_modules/.bin/elm-test tests/AgentTicketPRViewTests.elm` — expected failure: compile error (Model has no outcome/diff fields; the page ignores the new callbacks).
- [ ] In `web/elm/src/AgentTickets/AgentTicket.elm`, extend the page `Model` with the PR-view state:

```elm
    , outcome : Maybe Concourse.AgentOutcome.Outcome
    , reviews : List Concourse.AgentReview.BuildReview
    , diff : Maybe Concourse.AgentOutcome.DiffPage
    , diffOffset : Int
    , cost : Maybe Concourse.AgentOutcome.CostRow
    -- the AgentReview.PanelState fields the evidence panel needs:
    , agentReviews : List Concourse.AgentReview.BuildReview
    , agentReviewLoadError : Bool
    , agentReviewPanelExpanded : Bool
    , expandedFindings : Set String
    , showObservations : Bool
    , agentReviewNotes : Dict String String
    , verdictErrors : Set String
```

initialize them (empty/`Nothing`/`Set.empty`/`Dict.empty`; `cost = Nothing`) in the page `init`, and append the four fetches to the effects `init` returns and to the `OnClockTick FiveSeconds` branch (alongside ticket-core's existing `FetchAgentTicket`):

```elm
    [ Effects.FetchAgentTicket id
    , Effects.FetchAgentTicketOutcome id
    , Effects.FetchAgentTicketReviews id
    , Effects.FetchAgentTicketDiff id 0 diffWindow
    , Effects.FetchAgentTicketCost id
    ]
```

with `diffWindow = 50` a module constant.

- [ ] Handle the new callbacks in the page `handleCallback` (mirroring how ticket-core handles `AgentTicketFetched`):

```elm
        Callback.AgentTicketOutcomeFetched (Ok o) ->
            ( { model | outcome = Just o }, [] )

        Callback.AgentTicketOutcomeFetched (Err _) ->
            ( model, [] )

        Callback.AgentTicketReviewsFetched (Ok rs) ->
            ( { model | reviews = rs, agentReviews = rs, agentReviewLoadError = False }, [] )

        Callback.AgentTicketReviewsFetched (Err _) ->
            ( { model | agentReviewLoadError = True }, [] )

        Callback.AgentTicketDiffFetched (Ok page) ->
            ( { model | diff = Just (mergeDiffPage model.diff page), diffOffset = page.offset + List.length page.files }, [] )

        Callback.AgentTicketDiffFetched (Err _) ->
            ( model, [] )

        Callback.AgentTicketCostFetched (Ok rollup) ->
            -- pick the row whose key == this ticket id (group_by=ticket)
            ( { model | cost = List.head (List.filter (\r -> r.key == String.fromInt model.id) rollup.rows) }, [] )

        Callback.AgentTicketCostFetched (Err _) ->
            ( model, [] )
```

with a `mergeDiffPage` helper (appends files when paging past offset 0, else replaces):

```elm
mergeDiffPage : Maybe Concourse.AgentOutcome.DiffPage -> Concourse.AgentOutcome.DiffPage -> Concourse.AgentOutcome.DiffPage
mergeDiffPage existing page =
    case ( existing, page.offset ) of
        ( Just prev, offset ) ->
            if offset > 0 then
                { page | files = prev.files ++ page.files }

            else
                page

        _ ->
            page
```

- [ ] Add the PR-view render below the task list in the page `view`. The outcome badge, diff block, and evidence panel:

```elm
prView : Model -> Html Message
prView model =
    Html.div [ class "ticket-pr-view" ]
        [ outcomeBadge model.outcome
        , costView model.cost
        , Build.AgentReview.view "" model
        , diffView model.diff
        ]


costView : Maybe Concourse.AgentOutcome.CostRow -> Html Message
costView maybe =
    case maybe of
        Nothing ->
            Html.text ""

        Just c ->
            Html.div [ class "ticket-cost" ]
                [ Html.text ("$" ++ formatUsd c.costUsd)
                , Html.span [ class "ticket-cost-detail" ]
                    [ Html.text (" · " ++ String.fromInt c.turns ++ " turns · " ++ String.fromInt (c.inputTokens + c.outputTokens) ++ " tokens") ]
                ]


-- formatUsd renders a float to 2 decimals without a String.format dependency.
formatUsd : Float -> String
formatUsd v =
    let
        cents =
            round (v * 100)
    in
    String.fromInt (cents // 100) ++ "." ++ String.padLeft 2 '0' (String.fromInt (modBy 100 (abs cents)))


outcomeBadge : Maybe Concourse.AgentOutcome.Outcome -> Html Message
outcomeBadge maybe =
    case maybe of
        Nothing ->
            Html.text ""

        Just o ->
            Html.div [ class "ticket-outcome-badge", class ("merge-" ++ o.mergeState) ]
                [ Html.span [] [ Html.text (mergeStateLabel o.mergeState) ]
                , if o.mergeState == "merged_with_fixes" then
                    Html.span [ class "human-delta" ]
                        [ Html.text (" +" ++ String.fromInt o.humanLinesAdded)
                        , Html.text (" -" ++ String.fromInt o.humanLinesDeleted)
                        , Html.text (" over " ++ String.fromInt o.humanCommitCount ++ " human commit(s)")
                        ]

                  else
                    Html.text ""
                ]


mergeStateLabel : String -> String
mergeStateLabel s =
    case s of
        "merged" -> "merged"
        "merged_with_fixes" -> "merged with fixes"
        "closed_unmerged" -> "closed (unmerged)"
        "concluded" -> "concluded (no merge intended)"
        _ -> "open"


diffView : Maybe Concourse.AgentOutcome.DiffPage -> Html Message
diffView maybe =
    case maybe of
        Nothing ->
            Html.text ""

        Just page ->
            Html.div [ class "ticket-diff" ]
                (List.map diffFileView page.files
                    ++ (if page.hasMore then
                            [ Html.button
                                [ class "diff-load-more", onClick ClickAgentTicketDiffMore ]
                                [ Html.text "Load more files" ]
                            ]

                        else
                            []
                       )
                )


diffFileView : Concourse.AgentOutcome.DiffFile -> Html Message
diffFileView f =
    Html.div [ class "diff-file" ]
        [ Html.div [ class "diff-file-path" ] [ Html.text f.path ]
        , Html.pre [ class "diff-file-patch" ] [ Html.text f.patch ]
        ]
```

wire `prView model` into the existing page body, and add `ClickAgentTicketDiffMore` to `web/elm/src/Message/Message.elm` (page messages block) plus its handler in the page `update` (emits `FetchAgentTicketDiff model.id model.diffOffset diffWindow`).

- [ ] Import `Build.AgentReview`, `Concourse.AgentReview`, `Concourse.AgentOutcome`, `Set`, `Dict`, and `Html.Events.onClick` in the page module.
- [ ] Run to verify pass: `cd web/elm && ../../node_modules/.bin/elm-test tests/AgentTicketPRViewTests.elm && ../../node_modules/.bin/elm-test` — expect green (the full suite; `Build.AgentReview.view ""` renders the reused panel).
- [ ] Rebuild the bundle: from repo root, `cd web && yarn build` (or the repo's documented Elm build — grep `package.json` `scripts` for `build`), producing updated `web/public/elm.js` / `elm.min.js`.
- [ ] Commit: `git add web && git commit -m "feat(delivery-outcomes): ticket PR view - outcome badge, paginated diff, evidence panel"`

---

### Task 15: Elm six-verdict feedback + disposition UI on the ticket page

The evidence panel (`Build.AgentReview.view`) emits the existing review messages — `AgentReviewVerdictClicked`, `AgentReviewNoteChanged`, `ToggleAgentReviewFinding`, `ToggleAgentReviewObservations`, `ToggleAgentReviewPanel` — which the `AgentReviews/AgentReviews.elm` page already handles. Wire the same handling into the ticket page (reusing the `SubmitAgentReviewVerdict` effect that posts to `AgentFeedback`, so judge findings' six-verdict feedback flows into the existing calibration loop). Then add the ticket-level disposition control (sent-back/abandoned/concluded + reason dropdown + notes) that calls `SetAgentTicketDisposition` — `concluded` is the positive "spike/research done, no merge intended" verdict, not a failure.

**Files:**
- Modify: `web/elm/src/AgentTickets/AgentTicket.elm` (message handling in `update`; disposition form in `view`)
- Modify: `web/elm/src/Message/Message.elm` (disposition-form page messages)
- Test: `web/elm/tests/AgentTicketDispositionTests.elm`
- Modify (generated): `web/public/elm.js`, `web/public/elm.min.js`

**Steps:**

- [ ] Write the failing test `web/elm/tests/AgentTicketDispositionTests.elm`:

```elm
module AgentTicketDispositionTests exposing (all)

import Application.Application as Application
import Common
import Expect
import Message.Callback as Callback
import Message.Effects as Effects
import Message.Message as Message
import Test exposing (Test, describe, test)
import Test.Html.Event as Event
import Test.Html.Query as Query
import Test.Html.Selector exposing (class, tag, text)


loadedPage : Application.Model
loadedPage =
    Common.init "/agent-tickets/12"
        |> Application.handleCallback
            (Callback.AgentTicketReviewsFetched
                (Ok
                    [ { info =
                            { buildId = 1, buildName = "1", teamName = "main", pipelineName = "p"
                            , jobName = "j", repo = "tdmtrader/concourse", commitSha = "abc"
                            , branch = "agent/ticket-12", score = 8, maxScore = 10, pass = True
                            , provenCount = 0, observationCount = 1, summary = "ok", createdAt = 0, evaluatedCount = 0
                            }
                      , provenIssues = []
                      , observations = [ { id = "judge-correctness-1", severity = "", title = "edge case", description = "", file = "f.go", line = 1, category = "judge", testName = "", testOutput = "" } ]
                      , feedback = Dict.empty
                      , findingCount = 1
                      }
                    ]
                )
            )
        |> Tuple.first


all : Test
all =
    describe "ticket disposition + six-verdict"
        [ test "clicking a verdict on a judge finding submits feedback" <|
            \_ ->
                loadedPage
                    |> Application.update
                        (Message.Update <| Message.ToggleAgentReviewPanel)
                    |> Tuple.first
                    |> Application.update
                        (Message.Update <|
                            Message.AgentReviewVerdictClicked
                                { repo = "tdmtrader/concourse", commitSha = "abc", findingId = "judge-correctness-1", verdict = "accurate" }
                        )
                    |> Tuple.second
                    |> Common.contains
                        (Effects.SubmitAgentReviewVerdict
                            { repo = "tdmtrader/concourse", commitSha = "abc", findingId = "judge-correctness-1", verdict = "accurate", notes = "", reviewer = "" }
                        )
        , test "submitting a disposition emits SetAgentTicketDisposition" <|
            \_ ->
                loadedPage
                    |> Application.update (Message.Update <| Message.AgentTicketDispositionChanged "sent_back")
                    |> Tuple.first
                    |> Application.update (Message.Update <| Message.AgentTicketDispositionReasonChanged "incomplete")
                    |> Tuple.first
                    |> Application.update (Message.Update <| Message.SubmitAgentTicketDisposition)
                    |> Tuple.second
                    |> Common.contains
                        (Effects.SetAgentTicketDisposition 12
                            { disposition = "sent_back", reason = "incomplete", notes = "" }
                        )
        , test "concluded is offered and submits with research_complete (flow-decoupling)" <|
            \_ ->
                loadedPage
                    |> Application.update (Message.Update <| Message.AgentTicketDispositionChanged "concluded")
                    |> Tuple.first
                    |> Application.update (Message.Update <| Message.AgentTicketDispositionReasonChanged "research_complete")
                    |> Tuple.first
                    |> Application.update (Message.Update <| Message.SubmitAgentTicketDisposition)
                    |> Tuple.second
                    |> Common.contains
                        (Effects.SetAgentTicketDisposition 12
                            { disposition = "concluded", reason = "research_complete", notes = "" }
                        )
        ]
```

(match the reviewer/notes fields the existing `SubmitAgentReviewVerdict` params record uses — grep `AgentReviews/AgentReviews.elm` for the exact record it builds on `AgentReviewVerdictClicked`; the sketch above assumes empty reviewer/notes at click time, filled from the panel's note dict. Add `import Dict`.)

- [ ] Run to verify it fails: `cd web/elm && ../../node_modules/.bin/elm-test tests/AgentTicketDispositionTests.elm` — expected failure: the page does not handle `AgentReviewVerdictClicked` / disposition messages.
- [ ] Add the disposition page messages to `web/elm/src/Message/Message.elm` (near ticket-core's ticket-edit page messages):

```elm
    | AgentTicketDispositionChanged String
    | AgentTicketDispositionReasonChanged String
    | AgentTicketDispositionNotesChanged String
    | SubmitAgentTicketDisposition
```

- [ ] In the ticket page `update`, handle the reused review-panel messages exactly as `AgentReviews/AgentReviews.elm` does (copy that page's branches for `ToggleAgentReviewPanel`, `ToggleAgentReviewFinding`, `ToggleAgentReviewObservations`, `AgentReviewNoteChanged`, `AgentReviewVerdictClicked`, and the `AgentReviewVerdictSubmitted` callback — they mutate the `expandedFindings`/`showObservations`/`agentReviewNotes`/`verdictErrors` fields Task 14 added to the Model, and emit `SubmitAgentReviewVerdict`). Then add the disposition branches:

```elm
        AgentTicketDispositionChanged d ->
            ( { model | dispositionChoice = d }, [] )

        AgentTicketDispositionReasonChanged reason ->
            ( { model | dispositionReason = reason }, [] )

        AgentTicketDispositionNotesChanged notes ->
            ( { model | dispositionNotes = notes }, [] )

        SubmitAgentTicketDisposition ->
            ( model
            , [ Effects.SetAgentTicketDisposition model.id
                    { disposition = model.dispositionChoice
                    , reason = model.dispositionReason
                    , notes = model.dispositionNotes
                    }
              ]
            )
```

and handle `AgentTicketDispositionSet (Ok o)` in `handleCallback` by storing `{ model | outcome = Just o }` and refetching the ticket (so the lifecycle badge updates), `(Err _)` by leaving the model unchanged.

- [ ] Add the Model fields `dispositionChoice : String`, `dispositionReason : String`, `dispositionNotes : String` (init `""`), and render the disposition form in `view` (only when the ticket is in a non-terminal reviewable state — hide it once `outcome.mergeState` is a terminal merge state):

```elm
dispositionForm : Model -> Html Message
dispositionForm model =
    Html.div [ class "ticket-disposition" ]
        [ Html.select [ onInput AgentTicketDispositionChanged ]
            (List.map optionFor [ "", "sent_back", "abandoned", "concluded" ])
        , Html.select [ onInput AgentTicketDispositionReasonChanged ]
            (List.map optionFor ("" :: Concourse.AgentOutcome.dispositionReasons))
        , Html.textarea [ class "disposition-notes", onInput AgentTicketDispositionNotesChanged ] []
        , Html.button
            [ class "disposition-submit"
            , onClick SubmitAgentTicketDisposition
            ]
            [ Html.text "Record disposition" ]
        ]


optionFor : String -> Html Message
optionFor v =
    Html.option [ Html.Attributes.value v ] [ Html.text (if v == "" then "—" else v) ]
```

> `Concourse.AgentOutcome.dispositionReasons` was already added and exposed in Task 13 (the §1.11 taxonomy, matching `outcomes.DispositionReasons` order) — import and use it here, do not redefine. Wire `dispositionForm model` into the page body near the outcome badge.

- [ ] Run to verify pass: `cd web/elm && ../../node_modules/.bin/elm-test` — expect the full suite green.
- [ ] Rebuild the bundle: `cd web && yarn build` (updated `web/public/elm.js` / `elm.min.js`).
- [ ] Commit: `git add web && git commit -m "feat(delivery-outcomes): six-verdict feedback + ticket disposition UI"`

---

### Task 16: Live theborg verification — native merge detection against a real repo

The watcher's git heuristics (fast-forward, true-merge, squash patch-id) are exercised by the real-git fixture suites in Tasks 6, 7, 10. The one thing fixtures cannot prove is that the flag-configured mirror cache clones and fetches a *real* remote repo over https on the live web node. This task verifies that end-to-end on theborg without touching live workloads.

**Files:**
- Create: `atc/worker/jetbridge/live_outcome_watcher_test.go` (Go test gated behind `//go:build live`, matching the `live_*_test.go` pattern in `atc/worker/jetbridge`)

**Steps:**

- [ ] Write `atc/worker/jetbridge/live_outcome_watcher_test.go` — a `//go:build live` Go test (NOT Ginkgo) that: creates a throwaway temp dir as `--agent-outcome-git-dir`; constructs an `outcomewatcher.MirrorCache` with `GitURLTemplate = "https://github.com/{repo}.git"` and the repo `tdmtrader/concourse`; calls `cache.BranchHead(repo, "main")` and asserts a 40-char sha; then picks a known-merged short-lived branch on that repo (or `main` itself against `HEAD~1`) and asserts `Detect` reports merged. Skip with `t.Skip` when `AGENT_OUTCOME_LIVE_REPO` env is unset so the default `-tags live` run does not require network:

```go
//go:build live

package jetbridge_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/agent/outcomewatcher"
)

func TestLiveOutcomeMirrorFetch(t *testing.T) {
	repo := os.Getenv("AGENT_OUTCOME_LIVE_REPO") // e.g. "tdmtrader/concourse"
	if repo == "" {
		t.Skip("set AGENT_OUTCOME_LIVE_REPO to run the live mirror-fetch probe")
	}
	cache := outcomewatcher.NewMirrorCache(
		t.TempDir(),
		"https://github.com/{repo}.git",
		outcomewatcher.Auth{Token: os.Getenv("AGENT_OUTCOME_LIVE_TOKEN")},
		200,
	)
	head, err := cache.BranchHead(repo, "main")
	if err != nil {
		t.Fatalf("BranchHead: %v", err)
	}
	if len(head) != 40 {
		t.Fatalf("main head = %q, want a 40-char sha", head)
	}
	_ = filepath.Separator // keep imports honest if the body is trimmed
}
```

- [ ] Run locally against a public repo (no cluster needed — this is a plain git-over-https probe): `AGENT_OUTCOME_LIVE_REPO=tdmtrader/concourse go test -tags live -run '^TestLiveOutcomeMirrorFetch$' -v -count=1 ./atc/worker/jetbridge/` — expect PASS (clones, fetches, resolves `main`).
- [ ] Commit: `git add atc/worker/jetbridge && git commit -m "test(delivery-outcomes): live mirror-fetch probe for the outcome watcher"`

---

## Execution notes

**Running this workstream's test suite (all green before close-out):**

```bash
pg_isready                                                    # PostgreSQL required for atc/db + migration specs
go test ./agent/api/outcomes/                                 # types, handlers, taxonomy, diff, reviews (plain testing)
ginkgo ./agent/gitcheck/ ./agent/outcomewatcher/              # real-git fixture suites (need `git` on PATH)
ginkgo --focus="AgentOutcomesFactory" ./atc/db/               # DB factory
ginkgo --focus='Legacy Database Upgrade' ./atc/db/migration/  # migration 1773106090 walk
ginkgo ./atc/wrappa/ ./atc/api/accessor/                      # route auth wiring (exhaustive-switch guard)
ginkgo ./go-concourse/concourse/                              # client method
ginkgo ./fly/integration/ --focus="fly agent tickets dispose" # fly command (builds the fly binary; mock ATC version must match versions.go = 0.1.0)
cd web/elm && ../../node_modules/.bin/elm-test                # full Elm suite (data layer, PR view, disposition)
go build ./...                                                # nothing else breaks from the NewHandler/atccmd changes
```

Per CLAUDE.md: unit tests run in parallel with `-p`; **never** pass `--race` (parallel-compilation failures). Do not lower the `atc/db` template-DB timeouts. The `agent/gitcheck` and `agent/outcomewatcher` suites shell out to real `git` in `TempDir`s — they are hermetic (no network) and safe to run under `make test-unit` once added, but confirm `git` is on PATH in CI.

**Live-test requirements (theborg pattern per CLAUDE.md / MEMORY.md):**
- Task 16's probe is a plain git-over-https clone/fetch; it needs no KinD/K3s cluster and can run on this machine (Colima usually down; not needed here). For a public repo, `AGENT_OUTCOME_LIVE_TOKEN` may be omitted.
- To validate the full watcher on the live theborg web node: deploy with `--agent-outcome-git-dir=/var/lib/concourse/outcome-mirrors` set (the master switch) and `--agent-outcome-check-interval=5m`; file a ticket, let harvest push `agent/ticket-<id>`, merge it upstream, and confirm within one interval that `fly curl /api/v1/agent/tickets/<id>/outcome` shows `merge_state: merged` and the ticket page badge flips. Use a throwaway namespace / sandbox pipeline — never the live `cicd` review job. Confirm the §8.4 notification webhook fired for the merge (the transition function emits it; no extra code here).

**Rollback notes for the risky diffs:**
- **Migration 1773106090** is additive (one new table + one partial index); the `.down.sql` `DROP TABLE agent_outcomes` fully reverses it. No existing table is touched, so a rollback cannot lose non-outcome data. If the head-const merge with dispatch conflicts, the **higher** number wins (`1773106090`).
- **The outcome watcher is off by default:** `--agent-outcome-git-dir` empty disables both the component and the diff API. Shipping the code without setting the flag is a no-op — the safe default for the first deploy. Turn it on per-repo once the mirror dir has disk headroom.
- **`agent/api/reviews.GetByBuild` refactor (Task 9)** is behaviour-preserving (the loop body was lifted verbatim into `BuildResponseFor`); its own handler_test is the guard. If the extracted function ever diverges, revert Task 9's `handler.go` edit and inline the loop again — the ticket reviews handler is the only other caller.
- **The disposition endpoint transitions the ticket through `tickets.Store.Transition` FIRST, then writes `agent_outcomes`** — if the transition is rejected (409), no disposition row is written, so ticket state and outcome state never diverge. This ordering is load-bearing; do not reorder it.
- **Single-writer discipline:** the watcher and the disposition handler are the only two writers of outcome-driven ticket states (merged/merged_with_fixes via the watcher; sent_back/abandoned/concluded via the handler), and both go through `tickets.Store.Transition` with a `from` guard. A concurrent transition (e.g. a human abandons while the watcher detects a merge) makes the loser's `Transition` fail `ErrStaleTransition`/`ErrInvalidTransition` and no-op — never a corrupt state. The watcher's `detectMerges` re-checks `tk.State == needs_review` before acting, so a human disposition mid-tick is respected.

**Design-review amendments:**
- **2026-07-09 (F6 — send-back → re-dispatch → merge never recorded):** `seedRows` (Task 10 `agent/outcomewatcher/watcher.go`) previously `continue`d unconditionally on any found outcome row, so a re-dispatched ticket's row was never re-resolved. Combined with a send-back disposition moving the row to terminal `closed_unmerged` and `Ensure`'s refresh firing only `WHERE merge_state='open'`, the re-worked branch's eventual human merge was silently lost (spec §9 human-touch delta). Fix: (1) `Store.Ensure` — both `MemoryStore` (Task 3) and `agentOutcomesFactory` (Task 4 SQL) — now RE-ARM a row where `merge_state='closed_unmerged' AND disposition='sent_back'` back to `open` with fresh branch/pushed_sha/base_sha and cleared disposition fields; `abandoned` and merged rows stay terminal. (2) `seedRows` no longer `continue`s on a found row — it always calls `Ensure`, so the plain `needs_review → queued → needs_review` re-dispatch path also refreshes shas. (3) §1.11.1 prose + the `Store.Ensure` interface doc-comment now document the re-arm; the misleading "handled … elsewhere" comment was removed. Tests added: `TestMemoryStoreEnsureRefreshesOnlyOpenRows` gains send-back-re-arm + abandoned-stays-closed cases (Task 3); the `AgentOutcomesFactory` suite gains "re-arms a sent_back row on Ensure but leaves an abandoned row terminal (F6)" (Task 4); the `outcomewatcher` suite gains "re-arms a sent-back row and detects the rework merge with the NEW shas (F6)" driving `needs_review → sent_back → queued → running → needs_review(new shas)`, asserting the tick-2 re-arm while the branch is still unmerged, then landing a post-push human fix commit and merging so the row ends `merged_with_fixes` (non-empty `pushed_sha..tip` delta) with the new base/pushed shas. Contract-visible surface unchanged (no new `Store` method, no schema change — `Ensure`'s `ON CONFLICT WHERE` was broadened only).
- **2026-07-09 (F38 — F6 watcher spec: merge pushed too early + unearned `merged_with_fixes`):** final-review fix to the Task 10 spec "re-arms a sent-back row and detects the rework merge with the NEW shas (F6)". Two defects: (1) the spec pushed the merge to `main` BEFORE tick 2 — but `Watcher.Run` executes `seedRows` then `detectMerges` in the same tick, so the merge was detected on the re-arm tick itself and the tick-2 open-row assertions (`MergeOpen`, reworked shas, cleared disposition) could never observe the re-armed state; (2) it expected `merged_with_fixes` from the rework commit alone — but the rework commit IS the new `pushed_sha`, so `pushed_sha..tip-at-merge` was empty and by the plan's own frozen definition (`merged_with_fixes ⇔ human_commit_count > 0` over that window, §1.11.1 / Task 1) the correct verdict was plain `merged`. Fix: the rework commit is now a bot commit (the re-dispatched agent's push — the honest fixture; its authorship never mattered to the delta), the merge push moves AFTER the tick-2 re-arm assertions, and a genuine human commit ("human fix before merge", `Reviewer` author) lands on top of the reworked push before the merge — the only commit in the delta window — with new assertions `HumanCommitCount == 1` and `HumanLinesAdded > 0`. Misleading comments corrected in place; Task 10's expect-green note now flags both load-bearing orderings. Watcher/`Ensure` implementation code unchanged — this was a test-recipe bug, not a runtime bug. The F6 amendment entry above was corrected to describe the merge-after-re-arm + post-push-human-fix recipe. Contract-visible surface unchanged.
- **2026-07-09 (flow-decoupling — CONCLUDED terminal state; FLOWS.md §3 spike-research / §4):** owner-approved decision: a new TERMINAL ticket state `concluded` — "run finished, human reviewed, no merge intended" (spike/research flows) — reachable from `needs_review` via explicit human disposition, a positive sibling of `abandoned`, landed NOW because the §1.7 lifecycle enum is frozen up front (deciding later means a migration). Changes in this plan: (1) **Task 1 §1.11.1** — `merge_state`/`disposition` CHECKs gain `'concluded'`, `disposition_reason` gains `'research_complete'` (spike/research complete, no merge intended); outcome-row lifecycle documents that a `concluded` disposition closes the row as `merge_state='concluded'` (terminal, never re-armed, watcher skips merge-detection permanently); new scorecard-facing rule "**Concluded is not a failure**" — exclude from merge-rate denominators, never bucket with `closed_unmerged`; §11 log entry appended. (2) **Task 2** migration CHECKs extended. (3) **Task 3** — `MergeConcluded`/`DispositionConcluded` constants, `research_complete` in `DispositionReasons`, `ValidDisposition` accepts it; `MemoryStore.SetDisposition` closes an open row as `concluded` for a concluded disposition; `TestMemoryStoreConcludedDisposition` pins close-as-concluded + no-re-arm + off-the-work-list. (4) **Task 4** — factory `SetDisposition` SQL CASE targets `'concluded'` when `$2='concluded'`; new spec "closes an open row as concluded and never re-arms it". (5) **Task 5** — `dispositionToState` maps `concluded → tickets.StateConcluded` (the `needs_review → concluded` edge, still exclusively via `tickets.Store.Transition`); new handler test `TestSetDispositionConcludedTransitionsTicket`. (6) **Task 10** — new watcher spec "skips merge-detection for a concluded ticket": after a concluded disposition + transition, a subsequent upstream merge of the spike branch must NOT flip the row or the ticket; no watcher code change (the skip is structural — `ListOpen`/`seedRows`/re-arm WHERE all exclude it) but the spec pins that structure. (7) **Tasks 12/13/14/15** — fly `--disposition` gains `choice:"concluded"` and the reason list `research_complete`; Elm `dispositionReasons` gains `research_complete`; `mergeStateLabel` renders "concluded (no merge intended)"; the disposition dropdown offers `concluded`; new Elm test case submits concluded/research_complete. Depends on ticket-core's same-day amendment adding `StateConcluded` + the `needs_review → concluded` edge (stamps `completed_at`, fires the §8.4 webhook via the transition function — no code here). Affects: scorecards, process-intel-experiments, ticket-core.
- **2026-07-09 (verifier follow-up — Task 1 insertion anchor drifted by the concluded amendment):** Task 1's insertion step quoted §1.11's closing paragraph as "Explicit dispositions (`sent_back`/`abandoned`) live here …", but the same-day concluded amendment to 00-shared-contracts.md rewrote that paragraph to "Explicit dispositions (`sent_back`/`abandoned`/`concluded`) live here …" — a literal-anchor mismatch that risked a mis-placed §1.11.1 insertion at execution time. Fix: the quoted anchor text in the Task 1 step now includes `/`concluded`` so it matches the amended contracts paragraph verbatim. No task content, code, or contract-surface change — anchor text only.
