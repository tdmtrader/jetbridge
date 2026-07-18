# Agentic Platform — Shared Contracts

- **Date:** 2026-07-07
- **Status:** Frozen for planning. Parallel workstream planners treat this document as gospel; changes require a cross-workstream sign-off note appended to §11.
- **Source of truth:** `docs/superpowers/specs/2026-07-07-agentic-platform-end-state-design.md`
- **Grounding:** all conventions below were verified against the codebase on branch `jetbridge` (migrations `1773105502/04`, `atc/db/agent_reviews_factory.go`, `agent/` package, `ci-agent/schema` + `ci-agent/phaseconfig`, `atc/routes.go`, `atc/wrappa/api_auth_wrappa.go`, `atc/api/accessor/roles.go`, `atc/sidecar.go`, `atc/steps.go` RunStep, `pipelines.instance_vars`).

Every section states its **owner** (the workstream that lands and may evolve the contract) and its **consumers** (workstreams that build against it and must sign off on changes).

---

## Conventions that apply to the whole document

These decisions apply everywhere below and are marked once:

- **[DECIDED HERE: cross-aggregate references are plain integer/text columns, not SQL FKs.]** Precedent: `agent_reviews.build_id` is `INTEGER NOT NULL` with no `REFERENCES` (builds are reaped; agent tables outlive them). The same rule solves wave-ordering: `agent_cost_ledger` (wave 1) can carry a `ticket_id` before `agent_tickets` (wave 2) exists. Real FKs are used only *within* a single workstream's own table family (e.g. `agent_ticket_specs.ticket_id → agent_tickets.id`) and to stable core tables that land in the same migration wave. Join-key semantics are documented per column.
- **[DECIDED HERE: `agent/schema` is extracted into its own nested Go module (`github.com/concourse/concourse/agent/schema`, its own `go.mod`) and is the canonical shared schema package.]** Two near-identical copies exist today: `ci-agent/schema` and `agent/schema` (the latter already adds `tool.call`/`tool.result`). Verified reality check: ci-agent has **no** Go dependency on the main module today — it is a fully standalone module (`github.com/concourse/ci-agent`) whose `go.mod` requires only go-yaml, ginkgo/gomega, and otel, with no `require` and no `replace` for `github.com/concourse/concourse`, and there is no `go.work` in the repo — so "switch the import" means introducing a brand-new cross-module dependency, not flipping an existing one. Because `agent/schema`'s non-test code imports only the standard library, the agent-step workstream extracts it into a nested module with zero external requires; both sides consume it via `require` + `replace`: the root `go.mod` adds `replace github.com/concourse/concourse/agent/schema => ./agent/schema`, and `ci-agent/go.mod` adds `replace github.com/concourse/concourse/agent/schema => ../agent/schema`. The alternative — ci-agent requiring the entire main module — is rejected (it would pull the full ATC dependency graph into a deliberately lean CLI module). The agent-step workstream then extends `agent/schema`; ci-agent switches its imports; `ci-agent/schema` is deleted. This resolves spec open item 11.
- **Migration filenames** follow the repo convention `"<unix-ts>_<snake_name>.up.sql"` / `.down.sql` in `atc/db/migration/migrations/`. **[DECIDED HERE: numbers below are pre-allocated in blocks of 10 per workstream, all greater than the current head `1773105504`, so parallel branches never collide and wave order is encoded in the numbers.]** Every `.up.sql` has a matching `.down.sql` that drops exactly what it created.
- **Factories** follow `atc/db/agent_reviews_factory.go`: a `Store` interface defined next to the domain types under `agent/api/<area>/` (or `agent/<area>/`), a `New<X>Factory(conn DbConn)` in `atc/db/`, counterfeiter `//counterfeiter:generate` directive, squirrel (`psql`) query building, upserts via `ON CONFLICT`.
- **Timestamps** are `TIMESTAMPTZ NOT NULL DEFAULT now()` for `created_at`/`updated_at`; API JSON exposes Unix epoch seconds (matching `agent_reviews` scan of `EXTRACT(EPOCH FROM ...)::bigint`).
- **Money** is `NUMERIC(12,6)` USD in the DB and `float64` USD in Go/JSON (matching ci-agent's `CostUSD float64` from the claude CLI envelope). The budget library (§2.7) is the only place comparison/summation logic lives.
- **Status taxonomy** for anything a run/step can produce: `ok | failed | error` ("agent did badly" ≠ "platform broke"). `agent/schema.Results.Status` today is `pass|fail|error|abstain`; the shared module adds the three-way constants and a mapping (`pass→ok`, `fail→failed`, `error→error`, `abstain→failed` with `abstained: true` metadata). **[DECIDED HERE: keep results.json v1.0 wire values for backward compat; the DB and APIs use the three-way values.]**
- **Repo identity**: the `repo` text column everywhere uses the same canonical form `agent_reviews.repo` already uses in production (owner/name slug, e.g. `tdmtrader/concourse`). It is the join key across reviews, feedback, tickets, outcomes, benchmark cases.

---

## 1. Data model — SQL DDL and migration stubs

### 1.1 Wave/number allocation

| Block | Workstream (owner) | Migrations |
|---|---|---|
| 1773106010–19 | agent-identity | `agent_principals` |
| 1773106020–29 | credentials-and-budgets | `agent_user_credentials`, `agent_cost_ledger` |
| 1773106030–39 | pipeline-runs | `pipeline_runs`, pipelines template columns, `awaiting_human` status (`1773106032`, 2026-07-10 PARK-V2) |
| 1773106040–49 | workflow-store | `agent_workflow_definitions` |
| 1773106050–59 | ticket-core (VACATED, 2026-07-17 renumber §11) | original ticket-tables reservation; overtaken by the deploy head before landing — never reused |
| 1773106060–69 | agent-step + ticket-core (interleaved per 2026-07-17 renumber §11) | `agent_run_metrics` (`1773106060`), parked status + `session_id` (`1773106061`, PARK-V2), `agent_tickets` (`1773106062`), `agent_ticket_specs` (`1773106063`), `agent_ticket_tasks` (`1773106064`), `agent_run_step_state` (`1773106065`, PARK-V2, deferred/Task 25) |
| 1773106070–79 | platform-mcp-hitl | `agent_run_questions` (+ `question_hash` dedup, `1773106072`, 2026-07-10 PARK-V2) |
| 1773106080–89 | harvest-step | `agent_reviews`/`agent_feedback` linkage columns |
| 1773106090–99 | delivery-outcomes | `agent_outcomes` |
| 1773106100–109 | process-intel-experiments | `agent_benchmark_cases`, `agent_experiments`, `agent_experiment_runs` |

### 1.2 `agent_principals` — owner: **agent-identity**; consumers: ticket-core, agent-step, platform-mcp-hitl, gateway-mcp, harvest-step, dispatch

`1773106010_create_agent_principals.up.sql`:

```sql
CREATE TABLE agent_principals (
    id           SERIAL PRIMARY KEY,
    name         TEXT NOT NULL,                  -- e.g. 'harvest', 'gateway', 'ci-agent-review', 'agent-step'
    description  TEXT NOT NULL DEFAULT '',
    token_prefix TEXT NOT NULL,                  -- first 12 chars of the token, for display + O(1) lookup
    token_hash   TEXT NOT NULL,                  -- hex(sha256(full token)); raw token never stored
    scopes       TEXT[] NOT NULL DEFAULT '{}',   -- see scope list in §4.1
    team_name    TEXT NOT NULL DEFAULT 'main',   -- join key to teams.name; '' = platform-global
    created_by   TEXT NOT NULL DEFAULT '',       -- concourse username that minted it
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ,                    -- NULL = no expiry
    revoked_at   TIMESTAMPTZ,                    -- NULL = active
    last_used_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX agent_principals_token_hash ON agent_principals (token_hash);
CREATE INDEX agent_principals_name ON agent_principals (name);
```

Token wire format: `cap1.<id>.<43-char base64url secret>` (`cap` = concourse agent principal, `1` = version). Verification: parse id, fetch row, constant-time compare `sha256(token)` with `token_hash`, check `revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())`, check requested scope ∈ `scopes`. **[DECIDED HERE: hash-at-rest with prefix display, GitHub-PAT style — no JWT machinery; principals are few and verification is one indexed SELECT with a 60s in-memory cache.]**

Migration also backfills one principal named `legacy-publish` from nothing (no token) — the static `--agent-review-publish-token` flag remains accepted by the reviews handler until agent-identity's final task removes it.

### 1.3 `agent_user_credentials` — owner: **credentials-and-budgets**; consumers: dispatch, gateway-mcp

`1773106020_create_agent_user_credentials.up.sql`:

```sql
CREATE TABLE agent_user_credentials (
    id               SERIAL PRIMARY KEY,
    user_id          INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    user_name        TEXT NOT NULL,               -- denormalized users.username for display/attribution
    kind             TEXT NOT NULL DEFAULT 'anthropic_oauth'
                     CHECK (kind IN ('anthropic_oauth', 'anthropic_api_key')),
    encrypted_token  TEXT NOT NULL,               -- via existing atc/db/encryption EncryptionStrategy
    nonce            TEXT,                        -- NULL when DB encryption is disabled (matches pipelines.nonce convention)
    expires_at       TIMESTAMPTZ,                 -- claude setup-token: ~1 year; platform nags at horizon
    last_verified_at TIMESTAMPTZ,                 -- last successful probe using this token
    jira_account_id  TEXT NOT NULL DEFAULT '',    -- phase-2 Jira-user mapping seam
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX agent_user_credentials_user_kind ON agent_user_credentials (user_id, kind);
```

`users` is a real, stable core table (`1563997651_users_table.up.sql`: id serial, sub, username, connector) — a true FK is correct here. **[DECIDED HERE: encryption at rest reuses Concourse's existing `atc/db/encryption` AES-GCM `EncryptionStrategy` (the same mechanism that encrypts pipeline configs and team auth), storing the nonce alongside — no new crypto, key rotation rides the existing `concourse web --encryption-key` rotation path.]**

### 1.4 `agent_cost_ledger` — owner: **credentials-and-budgets**; consumers: dispatch, gateway-mcp, agent-step, scorecards, delivery-outcomes, process-intel-experiments

`1773106021_create_agent_cost_ledger.up.sql`:

```sql
CREATE TABLE agent_cost_ledger (
    id                    BIGSERIAL PRIMARY KEY,
    occurred_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    user_id               INTEGER,                -- join key users.id; NULL = platform-funded (§1.13)
    user_name             TEXT NOT NULL DEFAULT '',
    ticket_id             INTEGER,                -- join key agent_tickets.id; NULL for pure-CI agent work
    pipeline_run_id       INTEGER,                -- join key pipeline_runs.id
    build_id              INTEGER NOT NULL DEFAULT 0,  -- concourse build; 0 = not build-scoped
    step_name             TEXT NOT NULL DEFAULT '',
    source                TEXT NOT NULL
                          CHECK (source IN ('agent_step','gateway','harvest_judge','retrospective','ci_agent','probe')),
    provider              TEXT NOT NULL DEFAULT 'anthropic',
    model                 TEXT NOT NULL DEFAULT '',
    input_tokens          BIGINT NOT NULL DEFAULT 0,
    output_tokens         BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens     BIGINT NOT NULL DEFAULT 0,
    cache_creation_tokens BIGINT NOT NULL DEFAULT 0,
    turns                 INTEGER NOT NULL DEFAULT 0,
    cost_usd              NUMERIC(12,6) NOT NULL DEFAULT 0,
    metadata              JSONB
);

CREATE INDEX agent_cost_ledger_user_day ON agent_cost_ledger (user_id, occurred_at DESC);
CREATE INDEX agent_cost_ledger_ticket   ON agent_cost_ledger (ticket_id) WHERE ticket_id IS NOT NULL;
CREATE INDEX agent_cost_ledger_day      ON agent_cost_ledger (occurred_at DESC);
```

Ledger rows are **append-only**; token/cost values come straight from the claude CLI envelope fields ci-agent already parses (`ci-agent/llm/result.go`: `cost_usd`, `usage.input_tokens`, etc.). Rollups are queries, never materialized mutations.

### 1.5 `pipeline_runs` + pipelines template columns — owner: **pipeline-runs**; consumers: dispatch, platform-mcp-hitl, process-intel-experiments

`1773106030_add_template_columns_to_pipelines.up.sql`:

```sql
ALTER TABLE pipelines ADD COLUMN template BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE pipelines ADD COLUMN params_schema JSONB;      -- §7 representation; NULL for non-templates
ALTER TABLE pipelines ADD COLUMN run_retention JSONB;      -- {"keep_last": K, "ttl_days": N}; NULL = keep all
ALTER TABLE pipelines ADD COLUMN last_run_number INTEGER NOT NULL DEFAULT 0;
```

`1773106031_create_pipeline_runs.up.sql`:

```sql
CREATE TABLE pipeline_runs (
    id                   SERIAL PRIMARY KEY,
    template_pipeline_id INTEGER NOT NULL REFERENCES pipelines (id) ON DELETE CASCADE,
    instance_pipeline_id INTEGER REFERENCES pipelines (id) ON DELETE SET NULL,
                         -- the instanced pipeline (instance_vars: {"run": N}) executing this run
    number               INTEGER NOT NULL,        -- monotonic per template, like build names
    params               JSONB NOT NULL DEFAULT '{}',  -- validated param values as given
    status               TEXT NOT NULL DEFAULT 'running'
                         CHECK (status IN ('running','succeeded','failed','errored','aborted')),
    created_by           TEXT NOT NULL DEFAULT '',     -- username or principal name
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at         TIMESTAMPTZ,
    archived             BOOLEAN NOT NULL DEFAULT false
);

CREATE UNIQUE INDEX pipeline_runs_template_number ON pipeline_runs (template_pipeline_id, number);
CREATE INDEX pipeline_runs_status ON pipeline_runs (status) WHERE status = 'running';
```

Run number allocation: `UPDATE pipelines SET last_run_number = last_run_number + 1 WHERE id = $1 RETURNING last_run_number` inside the creation transaction (row-lock serializes; same trick as build naming). FKs to `pipelines` are correct here — both sides are this workstream's core machinery.

**Completion contract** (the part every consumer depends on): the lifecycle component (`atc.ComponentPipelineRunLifecycler`, polling+notify) marks a run complete when the instanced pipeline has **no builds in `pending` or `started`** and at least one entry job has run. Aggregate status is worst-of over the latest build of every job that ran (`errored > aborted > failed > succeeded`). **A parked step (`ask_human` / checkpoint) keeps its build `started`, therefore a parked run counts as `running`** — parking never completes a run. *(2026-07-10 amendment, §11 — PARK-V2)* This sentence now describes the **SHORT-PARK** case only: past `--agent-short-park-max` the parked step **exits** (§3.2 PARK-V2) and the run enters the non-terminal `awaiting_human` state below — a long park still never completes a run, it just stops holding a build `started` to do it. In-flight aborts: when every remaining build is aborted/finished, the run completes with `aborted` if any latest build was aborted and none errored.

*(2026-07-10 amendment, §11 — PARK-V2 seam delta)* **Parked-run contract — non-terminal `awaiting_human` [DECIDED PRE-FREEZE, per the `concluded` enum lesson].** `1773106032_add_awaiting_human_to_pipeline_runs.up.sql` (pipeline-runs block):

```sql
ALTER TABLE pipeline_runs DROP CONSTRAINT pipeline_runs_status_check;
ALTER TABLE pipeline_runs ADD CONSTRAINT pipeline_runs_status_check
    CHECK (status IN ('running','awaiting_human','succeeded','failed','errored','aborted'));

DROP INDEX pipeline_runs_status;
CREATE INDEX pipeline_runs_status ON pipeline_runs (status)
    WHERE status IN ('running','awaiting_human');
```

- **ENTRY (lifecycler-owned; single writer preserved):** when the completion query finds no builds in `pending`/`started` and ≥ 1 entry-job build ran, it FIRST checks for OPEN `agent_run_questions` rows for the run (`answered_at IS NULL AND timeout_policy = 'park'`): if any exist → `status = 'awaiting_human'`, `completed_at` stays NULL, and the run is NOT complete. Only `timeout_policy = 'park'` rows count — `default`/`fail` rows self-resolve (§3.2), and orphans of those on truly-dead runs still flow through the reconciler's existing release branch once the run completes.
- **COMPLETION DETECTION treats `awaiting_human` exactly as pending:** `CompletedRunsWithNewActivity`, retention (`keep_last`/`ttl_days` key off `completed_at`, which is NULL — an `awaiting_human` run is never archived mid-wait), and dispatch's run-completion reconciler (whose candidates require a COMPLETE run, §1.7/§3.2) all ignore it by construction. The reconciler can still never fire mid-wait.
- **EXIT paths:** (1) **resume** — the run gains a pending/started build (the continuation build, §3.2 PARK-V2); the lifecycler's existing F26 reopen machinery (plan 03), whose `NotEq('running')` status filter already admits `awaiting_human` (no query change — pinned by plan 03 Task 24 specs), flips the run back to `running`. The dispatch reconciler never writes run status — single-writer preserved. (2) **`--agent-park-timeout` wall clock** (72h default, same flag as the principal backstop — the lifecycler gains the value via its config, second consumer): a new lifecycler pass ends any `awaiting_human` run whose OLDEST open park question has `asked_at + park_timeout < now()` as `errored` (`completed_at = now()`), releases the open rows (`Answer(id, "", "platform")`), and fires the §8.4 notification (`event: "run.park_expired"`) so the owner is notified; the now-complete errored run then flows into the reconciler's existing unanswered-checkpoint branch, which errors the ticket.
- **`RunActive(runID)`** (the `RunSecretReaper` seam, §8.2): `awaiting_human` COUNTS AS ACTIVE — the `agent-run-<run-id>` secret and per-run principal row must survive the wait for the continuation to re-attach.

### 1.6 `agent_workflow_definitions` — owner: **workflow-store**; consumers: dispatch, harvest-step, platform-mcp-hitl, scorecards, process-intel-experiments

`1773106040_create_agent_workflow_definitions.up.sql`:

```sql
CREATE TABLE agent_workflow_definitions (
    id           SERIAL PRIMARY KEY,
    name         TEXT NOT NULL,                  -- e.g. 'standard-dev', 'review-only'
    version      INTEGER NOT NULL,               -- monotonic per name, assigned by the store on import
    content_hash TEXT NOT NULL,                  -- hex(sha256(definition)) — same fn as ci-agent/phaseconfig.Hash
    definition   TEXT NOT NULL,                  -- raw YAML source (§6); the hash covers exactly these bytes
    live         BOOLEAN NOT NULL DEFAULT false, -- eligible for ticket dispatch
    description  TEXT NOT NULL DEFAULT '',
    created_by   TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    promoted_at  TIMESTAMPTZ,
    promoted_by  TEXT NOT NULL DEFAULT '',       -- who ran set-live (audit; 2026-07 owner amendment)
    UNIQUE (name, version)
);

CREATE UNIQUE INDEX agent_workflow_definitions_live ON agent_workflow_definitions (name) WHERE live;
CREATE UNIQUE INDEX agent_workflow_definitions_hash ON agent_workflow_definitions (name, content_hash);
```

At most one live version per name (partial unique index). Importing byte-identical YAML is an idempotent no-op (hash unique index). Definitions are **immutable once created** — edits create a new version.

### 1.7 Ticket tables — owner: **ticket-core**; consumers: platform-mcp-hitl, dispatch, harvest-step, delivery-outcomes, process-intel-experiments

`1773106062_create_agent_tickets.up.sql` (renumbered from `1773106050`, 2026-07-17 §11; landed with two additive addendum columns `created_by`/`external_ref` after `user_name`):

```sql
CREATE TABLE agent_tickets (
    id                     SERIAL PRIMARY KEY,    -- ticket number; branch is agent/ticket-<id>
    title                  TEXT NOT NULL,
    body                   TEXT NOT NULL DEFAULT '',  -- markdown problem statement
    state                  TEXT NOT NULL DEFAULT 'draft'
                           CHECK (state IN ('draft','queued','running','needs_review',
                                            'merged','merged_with_fixes','sent_back',
                                            'abandoned','concluded','failed','errored')),
    origin                 TEXT NOT NULL DEFAULT 'web'
                           CHECK (origin IN ('web','fly','jira','retrospective')),
    repo                   TEXT NOT NULL,             -- canonical slug, joins agent_reviews.repo
    target_branch          TEXT NOT NULL DEFAULT 'main',
    workflow_name          TEXT NOT NULL DEFAULT '',
    workflow_version       INTEGER,                   -- NULL = live version at dispatch time
    workflow_definition_id INTEGER,                   -- join key; resolved+frozen by dispatch
    budget_usd             NUMERIC(12,6),             -- NULL = workflow definition default
    user_id                INTEGER,                   -- join key users.id (triggering user)
    user_name              TEXT NOT NULL DEFAULT '',
    pipeline_run_id        INTEGER,                   -- join key pipeline_runs.id (latest attempt)
    branch                 TEXT NOT NULL DEFAULT '',  -- set by harvest after push
    attempt_count          INTEGER NOT NULL DEFAULT 0,
    error_detail           TEXT NOT NULL DEFAULT '',  -- populated on state 'errored'
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    queued_at              TIMESTAMPTZ,
    dispatched_at          TIMESTAMPTZ,
    completed_at           TIMESTAMPTZ
);

CREATE INDEX agent_tickets_state ON agent_tickets (state);
CREATE INDEX agent_tickets_repo  ON agent_tickets (repo, created_at DESC);
```

**[DECIDED HERE: ticket number = `id`.]** No separate `number` column; the spec's `agent/ticket-<n>` is `agent/ticket-<id>`. Small team, single global sequence, zero renumbering machinery.

**[DECIDED HERE: spec's `failed` splits into `failed` (agent did badly / gates failed terminally after retries) and `errored` (platform broke), mirroring the ok/failed/error taxonomy at ticket level.]**

**State machine** (the single-writer transition function, §2.1, is the only legal writer of `state`):

```
draft → queued → running → needs_review → merged | merged_with_fixes | sent_back | abandoned | concluded
draft → abandoned
queued → draft (unqueue) | abandoned
running → queued (retryable platform error OR rejected send_back checkpoint re-dispatch; attempt_count++) | failed | errored | needs_review
needs_review → queued (re-dispatch after send-back edits)
sent_back → queued
failed | errored → queued (manual retry)
```

*(2026-07-09 amendment, §11)* `running → queued` has **two legitimate callers**: dispatch's retryable-platform-error path and dispatch's run-completion reconciler when a `send_back` checkpoint is rejected (§3.2); the §2.1 side effect (attempt_count+1, completed_at=NULL, queued_at=now()) covers both. `running → needs_review` has **two writers**: harvest (primary) and the run-completion reconciler (later/backup safety net). `ErrStaleTransition`/`ErrTicketNotFound` are benign to the reconciler — harvest may have raced.

*(2026-07-09 amendment, §11 — FLOWS.md spike-research / `concluded`)* **`concluded` is a TERMINAL state**: "run finished, human reviewed, no merge intended" — the positive sibling of `abandoned`, for spike/research/advisory flows whose deliverable is findings rather than a merged branch. It is entered **only** from `needs_review` via an explicit human disposition (`SetAgentTicketDisposition` §4.2 → the §2.1 transition function; harvest, dispatch, and the run-completion reconciler never write it). No merge is expected: the **outcome watcher MUST NOT poll or wait on `concluded` tickets** — if an outcome row exists (harvest pushed a branch), setting the disposition closes it terminally: as with `abandoned`, the watcher stops polling and no merge is expected, but the row closes as its own `merge_state = 'concluded'` — never `closed_unmerged` (§1.11 / §1.11.1, delivery-outcomes) — and merge-rate metrics exclude `concluded` tickets from their denominators (a finished spike is a success, not a miss). Landed in the frozen enum now because the CHECK constraint cannot be amended cheaply later (now-or-migration, FLOWS.md §4).

`1773106063_create_agent_ticket_specs.up.sql` (renumbered from `1773106051`, 2026-07-17 §11):

```sql
CREATE TABLE agent_ticket_specs (
    id                  SERIAL PRIMARY KEY,
    ticket_id           INTEGER NOT NULL REFERENCES agent_tickets (id) ON DELETE CASCADE,
    version             INTEGER NOT NULL DEFAULT 1,   -- resubmission bumps version, old rows kept
    title               TEXT NOT NULL,
    body                TEXT NOT NULL DEFAULT '',     -- markdown prose (rationale is load-bearing)
    acceptance_criteria JSONB NOT NULL DEFAULT '[]',  -- ["criterion", ...]
    links               JSONB NOT NULL DEFAULT '[]',  -- [{"title": "", "url": ""}, ...]
    submitted_by        TEXT NOT NULL DEFAULT '',     -- principal name (agent) or username (human edit)
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (ticket_id, version)
);
```

`1773106064_create_agent_ticket_tasks.up.sql` (renumbered from `1773106052`, 2026-07-17 §11):

```sql
CREATE TABLE agent_ticket_tasks (
    id           SERIAL PRIMARY KEY,
    ticket_id    INTEGER NOT NULL REFERENCES agent_tickets (id) ON DELETE CASCADE,
    plan_version INTEGER NOT NULL DEFAULT 1,      -- submit_plan replaces the active plan by bumping version
    ordering     INTEGER NOT NULL,
    title        TEXT NOT NULL,
    detail       TEXT NOT NULL DEFAULT '',        -- optional markdown
    status       TEXT NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending','in_progress','done','skipped','blocked')),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (ticket_id, plan_version, ordering)
);

CREATE INDEX agent_ticket_tasks_ticket ON agent_ticket_tasks (ticket_id, plan_version, ordering);
```

The active plan for a ticket is `MAX(plan_version)`; older versions are retained for process intelligence.

*(2026-07-09 amendment, §11 — FLOWS.md E5)* **Normative: a ticket MAY have zero spec rows and zero task rows through its entire lifecycle.** Spec-lessness is the normal entry state — rendering happens at dispatch, before any agent step can call `submit_spec`, so even the seeded spec-first workflow dispatches against a spec-less ticket — and workflows that never submit a spec or plan (direct-fix, spike-research) are first-class. Every consumer (the renderer §6.2, the platform-mcp read tools §3.2, ticket UI, harvest, scorecards) must handle absence as a normal value, never as an error.

### 1.8 `agent_run_metrics` — owner: **agent-step**; consumers: gateway-mcp, scorecards, process-intel-experiments, harvest-step

`1773106060_create_agent_run_metrics.up.sql`:

```sql
CREATE TABLE agent_run_metrics (
    id                    BIGSERIAL PRIMARY KEY,
    ticket_id             INTEGER,                 -- NULL for pure-CI agent steps
    pipeline_run_id       INTEGER,
    build_id              INTEGER NOT NULL,
    plan_id               TEXT NOT NULL DEFAULT '',    -- atc plan ID of the step (unique within build)
    step_name             TEXT NOT NULL,
    workflow_name         TEXT NOT NULL DEFAULT '',
    workflow_version      INTEGER,
    workflow_hash         TEXT NOT NULL DEFAULT '',    -- content_hash frozen at render time
    status                TEXT NOT NULL CHECK (status IN ('ok','failed','error')),
    summary               TEXT NOT NULL DEFAULT '',
    model                 TEXT NOT NULL DEFAULT '',
    input_tokens          BIGINT NOT NULL DEFAULT 0,
    output_tokens         BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens     BIGINT NOT NULL DEFAULT 0,
    cache_creation_tokens BIGINT NOT NULL DEFAULT 0,
    turns                 INTEGER NOT NULL DEFAULT 0,
    wall_time_seconds     INTEGER NOT NULL DEFAULT 0,
    cost_usd              NUMERIC(12,6) NOT NULL DEFAULT 0,
    results               JSONB,                   -- full results.json payload
    events_artifact       TEXT NOT NULL DEFAULT '',-- artifact-fabric handle for events.ndjson
    event_counts          JSONB,                   -- {"tool.call": 87, "subagent.call": 3, ...}
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX agent_run_metrics_build_plan ON agent_run_metrics (build_id, plan_id);
CREATE INDEX agent_run_metrics_ticket   ON agent_run_metrics (ticket_id) WHERE ticket_id IS NOT NULL;
CREATE INDEX agent_run_metrics_workflow ON agent_run_metrics (workflow_name, workflow_version);
```

Full events.ndjson stays in the artifact fabric; the row carries the rollup plus per-type counts so "where did the turns go" is one query, with drill-down fetching the artifact.

*(2026-07-10 amendment, §11 — PARK-V2)* The `status` CHECK gains `'parked'` and the table gains a `session_id TEXT NOT NULL DEFAULT ''` column — the latest claude session id for the execution — (same additive migration in the agent-step block, `1773106061`; wire status `"parked"`; `agent/schema` `ThreeWayStatus` maps `parked → parked`): at park-exit (§3.2 PARK-V2) the AgentStep exec runs its synchronous partial ingestion — a metrics row with `status = 'parked'` and best-effort usage/cost accumulated from the teed stream-json events, plus a ledger append for the partial spend (normal F3 `inserted` gate; the continuation build means a new `(build_id, plan_id)` row, no dedup collision). **Consumer note (scorecards/attribution):** executions of one logical step — a parked execution and its continuation(s), or a replayed step — share `(pipeline_run_id, step_name)`; aggregate cost/turns across all rows with that key, never per single `(build_id, plan_id)` row.

### 1.9 `agent_run_questions` — owner: **platform-mcp-hitl**; consumers: dispatch, ticket-core (UI), process-intel-experiments

`1773106070_create_agent_run_questions.up.sql`:

```sql
CREATE TABLE agent_run_questions (
    id              SERIAL PRIMARY KEY,
    ticket_id       INTEGER NOT NULL,             -- join key agent_tickets.id
    pipeline_run_id INTEGER,
    build_id        INTEGER NOT NULL DEFAULT 0,
    step_name       TEXT NOT NULL DEFAULT '',
    kind            TEXT NOT NULL DEFAULT 'question'
                    CHECK (kind IN ('question','checkpoint')),
    question        TEXT NOT NULL,                -- markdown; for checkpoints: what is being approved
    options         JSONB NOT NULL DEFAULT '[]',  -- ["option a", "option b"]; empty = free text
    timeout_policy  TEXT NOT NULL DEFAULT 'park'
                    CHECK (timeout_policy IN ('park','default','fail')),
    timeout_seconds INTEGER NOT NULL DEFAULT 0,   -- 0 = no timeout (park indefinitely)
    default_answer  TEXT,                         -- from the ask_human call's 'default' field (§3.2); used when timeout_policy = 'default'
    asked_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    answered_at     TIMESTAMPTZ,
    answer          TEXT,
    answered_by     TEXT NOT NULL DEFAULT ''
);

CREATE INDEX agent_run_questions_open   ON agent_run_questions (ticket_id) WHERE answered_at IS NULL;
CREATE INDEX agent_run_questions_ticket ON agent_run_questions (ticket_id, asked_at DESC);
```

Checkpoint gates reuse this table with `kind = 'checkpoint'` and `options = ["approve","reject"]` — one park/resume mechanism, one UI surface, one notification path.

*(2026-07-10 amendment, §11 — PARK-V2, IDEMPOTENT-BY-QUESTION)* `1773106072_add_question_hash_to_agent_run_questions.up.sql` (platform-mcp block):

```sql
ALTER TABLE agent_run_questions ADD COLUMN question_hash TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX agent_run_questions_dedup
    ON agent_run_questions (pipeline_run_id, step_name, kind, question_hash)
    WHERE pipeline_run_id IS NOT NULL;
```

`question_hash` = `hex(sha256(question || '\x00' || options-joined-by-'\x00'))`, computed by the **ATC ask route** (never the sidecar or client). `AskAgentQuestion` becomes **FIND-OR-CREATE** on the dedup key: an existing ANSWERED row is returned as-is — the sidecar sees `answered_at` set and returns the `ask_human` result immediately, which is the resume fast path (§3.2 PARK-V2) — and an existing OPEN row is joined (same id; the sidecar parks on it). This generalizes the checkpoint per-name dedup ("a second POST for the same name joins the open row") from sidecar-local (`ckOpen`) to DB-enforced — necessary anyway, since a continuation build runs a FRESH sidecar with an empty in-memory map. The `ckOpen` map is retained as a same-pod optimization only.

### 1.10 `agent_reviews` / `agent_feedback` linkage — owner: **harvest-step**; consumers: dispatch, delivery-outcomes, scorecards, process-intel-experiments

`1773106080_add_ticket_linkage_to_agent_reviews.up.sql`:

```sql
ALTER TABLE agent_reviews  ADD COLUMN ticket_id INTEGER;        -- NULL = plain CI review (today's rows)
ALTER TABLE agent_reviews  ADD COLUMN pipeline_run_id INTEGER;
ALTER TABLE agent_feedback ADD COLUMN ticket_id INTEGER;

CREATE INDEX agent_reviews_ticket  ON agent_reviews  (ticket_id) WHERE ticket_id IS NOT NULL;
CREATE INDEX agent_feedback_ticket ON agent_feedback (ticket_id) WHERE ticket_id IS NOT NULL;
```

Existing upsert key `(build_id, repo, commit_sha)` is untouched; existing CI review publishing keeps working with NULL linkage.

### 1.11 `agent_outcomes` — owner: **delivery-outcomes**; consumers: scorecards, process-intel-experiments

`1773106090_create_agent_outcomes.up.sql`:

```sql
CREATE TABLE agent_outcomes (
    id                  SERIAL PRIMARY KEY,
    ticket_id           INTEGER NOT NULL,          -- join key agent_tickets.id
    repo                TEXT NOT NULL,
    branch              TEXT NOT NULL,             -- agent/ticket-<id>
    pushed_sha          TEXT NOT NULL DEFAULT '',  -- branch head at harvest push time
    merge_state         TEXT NOT NULL DEFAULT 'open'
                        CHECK (merge_state IN ('open','merged','merged_with_fixes','closed_unmerged',
                                               'concluded')),  -- 'concluded' added 2026-07-09 flow-decoupling (§11)
    merged_sha          TEXT NOT NULL DEFAULT '',  -- default-branch commit from which pushed_sha became reachable
    merged_at           TIMESTAMPTZ,
    human_commit_count  INTEGER NOT NULL DEFAULT 0,   -- commits on the branch after pushed_sha, non-agent author
    human_lines_added   INTEGER NOT NULL DEFAULT 0,   -- human-touch delta: numstat of those commits
    human_lines_deleted INTEGER NOT NULL DEFAULT 0,
    disposition         TEXT NOT NULL DEFAULT ''
                        CHECK (disposition IN ('','sent_back','abandoned','concluded')),
    disposition_reason  TEXT NOT NULL DEFAULT ''
                        CHECK (disposition_reason IN ('','wrong_approach','incomplete','defective',
                                                      'superseded','not_needed','style','other',
                                                      'research_complete')),  -- 'research_complete' added 2026-07-09 flow-decoupling (§11)
    disposition_notes   TEXT NOT NULL DEFAULT '',
    disposed_by         TEXT NOT NULL DEFAULT '',
    last_checked_at     TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (ticket_id)
);
```

**Human-touch delta definition [DECIDED HERE]:** the sum of `git diff --numstat <pushed_sha>..<branch-head-at-merge>` restricted to commits whose author is not the platform's git identity (`concourse-agent[bot]` — see §8.3). Merge commits themselves are excluded (first-parent walk). Computed once by the outcome watcher when `merge_state` transitions to merged; `merged_with_fixes` ⇔ `human_commit_count > 0`.

Explicit dispositions (`sent_back`/`abandoned`/`concluded`) live here (with the reason taxonomy); the ticket `state` mirrors them via the transition function. *(2026-07-09 amendment, §11)* A `concluded` disposition (needs_review → concluded, §1.7) closes the outcome terminally: as with `abandoned`, the outcome watcher stops polling the row — no merge is expected — but the row closes as its own `merge_state = 'concluded'` (never `closed_unmerged`; §1.11.1 / delivery-outcomes, which must never bucket `concluded` with `closed_unmerged` failures), and merge-rate metrics exclude the ticket from their denominators. Spike/research runs that never pushed have no outcome row at all; `concluded` is still valid for them (the disposition then only drives the ticket state).

### 1.12 Experiment substrate — owner: **process-intel-experiments**; consumers: scorecards

`1773106100_create_agent_benchmark_cases.up.sql`:

```sql
CREATE TABLE agent_benchmark_cases (
    id            SERIAL PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE,
    repo          TEXT NOT NULL,
    prompt        TEXT NOT NULL,             -- the ticket-style prompt
    before_ref    TEXT NOT NULL,             -- git ref: state before the original change
    reference_ref TEXT NOT NULL,             -- git ref: the human solution, for comparison
    tags          TEXT[] NOT NULL DEFAULT '{}',
    notes         TEXT NOT NULL DEFAULT '',
    created_by    TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

**[DECIDED HERE: benchmark cases live in the DB, not in-repo dirs]** — cases span repos, refs pin the content, and the experiment runner needs to enumerate them without cloning anything (resolves spec open item 6).

`1773106101_create_agent_experiments.up.sql`:

```sql
CREATE TABLE agent_experiments (
    id           SERIAL PRIMARY KEY,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    matrix       JSONB NOT NULL,   -- {"cases":[names], "workflows":[{"name":..,"version":..}], "repetitions":N}
    status       TEXT NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending','running','complete','error')),
    created_by   TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);

CREATE TABLE agent_experiment_runs (
    id                SERIAL PRIMARY KEY,
    experiment_id     INTEGER NOT NULL REFERENCES agent_experiments (id) ON DELETE CASCADE,
    benchmark_case_id INTEGER NOT NULL REFERENCES agent_benchmark_cases (id) ON DELETE CASCADE,
    workflow_name     TEXT NOT NULL,
    workflow_version  INTEGER NOT NULL,
    repetition        INTEGER NOT NULL DEFAULT 1,
    pipeline_run_id   INTEGER,                 -- join key pipeline_runs.id
    status            TEXT NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending','running','ok','failed','error')),
    UNIQUE (experiment_id, benchmark_case_id, workflow_name, workflow_version, repetition)
);
```

### 1.13 Platform credential policy — owner: **credentials-and-budgets**; consumers: harvest-step, process-intel-experiments, gateway-mcp

**[DECIDED HERE: platform-initiated LLM work (harvest judge, retrospective agent, calibration jobs) is funded by a designated platform credential — an `agent_user_credentials` row owned by a real admin user but flagged by convention `user_name = 'platform'` via a dedicated service user row inserted by the credentials workstream (`users` row with `sub='agent-platform'`, `connector='local'`). Ledger rows for platform work carry that user_id and `source IN ('harvest_judge','retrospective','probe')`; the global daily cap includes platform spend; per-ticket budgets do NOT include harvest-judge spend (the judge must never be starved by an agent that burned the budget — judge spend is capped separately by `judge.budget_usd` in the workflow definition, §6).]** The platform credential reaches pods via the long-lived `agent-platform-credential` K8s secret (§8.2) — never via the per-run `agent-run-<run-id>` secret.

### 1.14 `agent_run_step_state` — owner: **agent-step**; consumers: dispatch, pipeline-runs, platform-mcp-hitl, scorecards *(added 2026-07-10, PARK-V2 seam delta, §11)*

`1773106065_create_agent_run_step_state.up.sql` (agent-step block; renumbered from `1773106062`, 2026-07-17 §11):

```sql
CREATE TABLE agent_run_step_state (
    id              BIGSERIAL PRIMARY KEY,
    pipeline_run_id INTEGER NOT NULL,
    step_name       TEXT NOT NULL,
    state           TEXT NOT NULL CHECK (state IN ('completed','awaiting_human')),
    build_id        INTEGER NOT NULL,            -- build that produced this state
    session_id      TEXT NOT NULL DEFAULT '',    -- latest claude session id for this step
    question_id     INTEGER,                     -- open agent_run_questions row at park-exit
    artifacts       JSONB NOT NULL DEFAULT '{}', -- {"workspace": "<fabric handle>", "flight": "<handle>", ...}
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (pipeline_run_id, step_name)
);
```

Upserted by the AgentStep exec at **EVERY agent-step end**: `state = 'completed'` on a normal end (recording the output artifact handles), `state = 'awaiting_human'` on exit-86 park-exit (recording `session_id` + `question_id` + handles). On exit-86 the exec still registers ALL outputs (workspace + flight, incl. `flight/session.jsonl`) into the artifact fabric exactly as on a normal end (the F23 upload path already guarantees this on non-happy exits) and runs the §1.8 partial ingestion (`status = 'parked'`) plus the partial-spend ledger append. This table is what makes a continuation build (§3.2 PARK-V2) able to replay completed steps and resume the parked one — keyed `(pipeline_run_id, step_name)`, no per-build vars needed.

**Continuation-pin GC rule:** artifact volumes whose handles appear in `agent_run_step_state.artifacts` of a run in (`running`, `awaiting_human`) are **exempt from volume GC**; the pin dissolves when the run reaches a terminal status. Bounded by `--agent-park-timeout` (72h, §1.5) — nothing is pinned forever. Workspace state therefore "stays" across the park gap in the DaemonSet artifact cache, with zero pods.

---

## 2. Go domain types

Sketches follow the `agent/api/reviews.StoredReview` + `Store` idiom: plain structs with epoch-seconds timestamps for API types; factory interfaces implemented in `atc/db`. Package paths are binding; field lists may grow (never shrink/rename) without contract re-sign-off.

### 2.1 Ticket — owner: **ticket-core** (`agent/api/tickets/types.go`)

```go
package tickets

type State string

const (
	StateDraft            State = "draft"
	StateQueued           State = "queued"
	StateRunning          State = "running"
	StateNeedsReview      State = "needs_review"
	StateMerged           State = "merged"
	StateMergedWithFixes  State = "merged_with_fixes"
	StateSentBack         State = "sent_back"
	StateAbandoned        State = "abandoned"
	StateConcluded        State = "concluded" // terminal; needs_review → concluded via human disposition only (§1.7)
	StateFailed           State = "failed"
	StateErrored          State = "errored"
)

type Ticket struct {
	ID                   int     `json:"id"`
	Title                string  `json:"title"`
	Body                 string  `json:"body"`
	State                State   `json:"state"`
	Origin               string  `json:"origin"`
	Repo                 string  `json:"repo"`
	TargetBranch         string  `json:"target_branch"`
	WorkflowName         string  `json:"workflow_name"`
	WorkflowVersion      *int    `json:"workflow_version,omitempty"`
	WorkflowDefinitionID *int    `json:"workflow_definition_id,omitempty"`
	BudgetUSD            *float64 `json:"budget_usd,omitempty"`
	UserID               *int    `json:"user_id,omitempty"`
	UserName             string  `json:"user_name"`
	PipelineRunID        *int    `json:"pipeline_run_id,omitempty"`
	Branch               string  `json:"branch"`
	AttemptCount         int     `json:"attempt_count"`
	ErrorDetail          string  `json:"error_detail,omitempty"`
	CreatedAt            int64   `json:"created_at"`
	UpdatedAt            int64   `json:"updated_at"`
	CompletedAt          int64   `json:"completed_at,omitempty"`
}

// Store is the single-writer contract. Transition is THE ONLY way any code
// path (API handler, dispatcher, harvest, outcome watcher, HITL) changes
// Ticket.State. It enforces the state machine in shared-contracts §1.7,
// records timestamps, and returns ErrInvalidTransition otherwise. It uses
// optimistic concurrency: the UPDATE is guarded by the expected `from` state.
//
//counterfeiter:generate . Store
type Store interface {
	Create(t *Ticket) (int, error)
	Get(id int) (*Ticket, bool, error)
	List(filter ListFilter) ([]Ticket, error)
	Update(id int, upd Update) error // title/body/budget/workflow ref; never state
	Transition(id int, from, to State, meta TransitionMeta) error

	SubmitSpec(ticketID int, spec Spec) (version int, err error)
	SubmitPlan(ticketID int, tasks []Task) (planVersion int, err error)
	UpdateTaskStatus(ticketID int, planVersion, ordering int, status TaskStatus) error
	ActivePlan(ticketID int) ([]Task, error)
	LatestSpec(ticketID int) (*Spec, bool, error)
}
```

`atc/db/agent_tickets_factory.go` implements `tickets.Store` (counterfeiter fake for consumers). Consumers (dispatch, harvest, HITL, outcomes) receive the interface, never raw SQL.

*(2026-07-09 amendment, §11 — FLOWS.md E5)* **Absence semantics (normative):** `LatestSpec` returns `(nil, false, nil)` when the ticket has no spec rows, and `ActivePlan` returns `([]Task{}, nil)` — an empty slice with a nil error — when the ticket has no plan. Neither is an error condition, and implementers must not add error-on-absence: dispatch's renderer (§6.2) and the platform-mcp read tools (§3.2) rely on exactly these zero values for spec-less/plan-less tickets (§1.7).

### 2.2 WorkflowDefinition — owner: **workflow-store** (`agent/workflow/definition.go`)

```go
package workflow

// Definition is the parsed, validated form of the YAML in
// agent_workflow_definitions.definition (§6). ContentHash is
// hex(sha256(raw YAML bytes)) — identical fn to ci-agent/phaseconfig.Hash.
type Definition struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Version     int    `json:"version"`
	ContentHash string `json:"content_hash"`
	Live        bool   `json:"live"`
	Description string `json:"description"`
	CreatedBy   string `json:"created_by"`
	CreatedAt   int64  `json:"created_at"`

	Config Config `json:"config"` // parsed YAML, §6 grammar

	// RawYAML is the exact stored definition bytes (the hashed provenance
	// unit). Populated by Get and Live; empty in List/Versions.
	RawYAML string `json:"raw_yaml,omitempty"`
}

//counterfeiter:generate . Store
type Store interface {
	Import(name string, rawYAML []byte, createdBy string) (*Definition, error) // idempotent on hash
	Get(name string, version int) (*Definition, bool, error)
	Live(name string) (*Definition, bool, error)
	List() ([]Definition, error)          // latest version per name + live marker
	Versions(name string) ([]Definition, error)
	Promote(name string, version int, promotedBy string) error // atomically swaps the live flag
}
```

### 2.3 PipelineRun — owner: **pipeline-runs** (`atc/db/pipeline_run.go`, core table so it lives with the other atc/db first-class objects)

```go
package db

type PipelineRunStatus string

const (
	PipelineRunRunning   PipelineRunStatus = "running"
	// PipelineRunAwaitingHuman added 2026-07-10 PARK-V2 (§1.5): NON-TERMINAL —
	// long park exited, zero pods, run waiting for a human answer. Treated as
	// pending by completion detection, retention, and the dispatch reconciler.
	PipelineRunAwaitingHuman PipelineRunStatus = "awaiting_human"
	PipelineRunSucceeded PipelineRunStatus = "succeeded"
	PipelineRunFailed    PipelineRunStatus = "failed"
	PipelineRunErrored   PipelineRunStatus = "errored"
	PipelineRunAborted   PipelineRunStatus = "aborted"
)

//counterfeiter:generate . PipelineRunFactory
type PipelineRunFactory interface {
	// CreateRun validates params against the template's params schema,
	// allocates the next run number, materializes the instanced pipeline
	// (instance_vars: {"run": N} plus params), triggers entry jobs, and
	// returns the run row.
	CreateRun(templatePipelineID int, params map[string]any, createdBy string) (PipelineRun, error)
	GetRun(templatePipelineID, number int) (PipelineRun, bool, error)
	ListRuns(templatePipelineID int, limit int) ([]PipelineRun, error)
	RunningRuns() ([]PipelineRun, error) // for the lifecycle component
}

type PipelineRun interface {
	ID() int
	TemplatePipelineID() int
	InstancePipelineID() (int, bool)
	Number() int
	Params() map[string]any
	Status() PipelineRunStatus
	CreatedBy() string
	Finish(status PipelineRunStatus) error
	Archive() error
}
```

### 2.4 RunMetrics — owner: **agent-step** (`agent/schema/metrics.go`, shared module)

```go
package schema

// RunMetrics is one agent step's flight-recorder rollup — both the ingest
// payload for SubmitAgentRunMetrics and the row shape of agent_run_metrics.
type RunMetrics struct {
	TicketID        *int    `json:"ticket_id,omitempty"`
	PipelineRunID   *int    `json:"pipeline_run_id,omitempty"`
	BuildID         int     `json:"build_id"`
	PlanID          string  `json:"plan_id"`
	StepName        string  `json:"step_name"`
	WorkflowName    string  `json:"workflow_name,omitempty"`
	WorkflowVersion *int    `json:"workflow_version,omitempty"`
	WorkflowHash    string  `json:"workflow_hash,omitempty"`
	Status          string  `json:"status"` // ok | failed | error
	Summary         string  `json:"summary"`
	Model           string  `json:"model"`
	Usage           Usage   `json:"usage"`  // same shape as ci-agent/llm.Usage
	Turns           int     `json:"turns"`
	WallTimeSeconds int     `json:"wall_time_seconds"`
	CostUSD         float64 `json:"cost_usd"`
	Results         json.RawMessage `json:"results,omitempty"`        // full results.json
	EventsArtifact  string          `json:"events_artifact,omitempty"`
	EventCounts     map[string]int  `json:"event_counts,omitempty"`
}
```

### 2.5 Outcome — owner: **delivery-outcomes** (`agent/api/outcomes/types.go`)

```go
package outcomes

type MergeState string

const (
	MergeOpen           MergeState = "open"
	Merged              MergeState = "merged"
	MergedWithFixes     MergeState = "merged_with_fixes"
	ClosedUnmerged      MergeState = "closed_unmerged"
	// MergeConcluded added 2026-07-09 flow-decoupling (§11): terminal close for
	// a 'concluded' disposition — no merge intended (spike/research flows).
	// Never bucketed with ClosedUnmerged; excluded from merge-rate denominators.
	MergeConcluded      MergeState = "concluded"
)

type Outcome struct {
	TicketID          int        `json:"ticket_id"`
	Repo              string     `json:"repo"`
	Branch            string     `json:"branch"`
	PushedSha         string     `json:"pushed_sha"`
	MergeState        MergeState `json:"merge_state"`
	MergedSha         string     `json:"merged_sha,omitempty"`
	MergedAt          int64      `json:"merged_at,omitempty"`
	HumanCommitCount  int        `json:"human_commit_count"`
	HumanLinesAdded   int        `json:"human_lines_added"`
	HumanLinesDeleted int        `json:"human_lines_deleted"`
	Disposition       string     `json:"disposition,omitempty"`        // sent_back | abandoned | concluded
	DispositionReason string     `json:"disposition_reason,omitempty"` // §1.11 taxonomy
	DispositionNotes  string     `json:"disposition_notes,omitempty"`
	DisposedBy        string     `json:"disposed_by,omitempty"`
	LastCheckedAt     int64      `json:"last_checked_at,omitempty"`
}
```

### 2.6 UserCredential — owner: **credentials-and-budgets** (`agent/credentials/types.go`)

```go
package credentials

// Credential never carries the decrypted token in API responses;
// Token is populated only by Store.Resolve for dispatch/secret-attach.
type Credential struct {
	UserID         int    `json:"user_id"`
	UserName       string `json:"user_name"`
	Kind           string `json:"kind"` // anthropic_oauth | anthropic_api_key
	ExpiresAt      int64  `json:"expires_at,omitempty"`
	LastVerifiedAt int64  `json:"last_verified_at,omitempty"`
	JiraAccountID  string `json:"jira_account_id,omitempty"`

	Token string `json:"-"` // decrypted; in-memory only
}

//counterfeiter:generate . Store
type Store interface {
	Put(userID int, userName, kind, token string, expiresAt time.Time) error
	Status(userID int) ([]Credential, error)        // no tokens
	Resolve(userID int, kind string) (*Credential, bool, error) // decrypts
	ExpiringWithin(d time.Duration) ([]Credential, error)       // nag list
	Delete(userID int, kind string) error
}

// SecretAttacher is the ephemeral K8s secret helper (§8.2). Implemented once
// here; dispatch and the gateway use it, nobody re-implements secret lifecycle.
type SecretAttacher interface {
	// Attach CREATES-OR-UPDATES secret agent-run-<runID> in the worker
	// namespace with the §8.2 keys and returns its name. Idempotent per
	// runID. (2026-07-10 PARK-V2, §8.2/§3.2: on resume of an awaiting_human
	// run dispatch calls Attach again with the re-minted principal token and
	// a re-resolved user credential; continuation pods are new pods, so the
	// updated keys are picked up at container start.)
	Attach(ctx context.Context, runID int, cred *Credential, principalToken string) (secretName string, err error)
	// Cleanup deletes the secret. Called by the pipeline-run lifecycle
	// component on run completion (and best-effort by dispatch on error).
	Cleanup(ctx context.Context, runID int) error
}
```

### 2.7 Budget library — owner: **credentials-and-budgets** (`agent/budget/budget.go`) — the single source of budget truth

```go
package budget

// Checker is consulted by the dispatcher (admission), the agent step
// (slice env computation) and the gateway (mid-flight cutoff). All
// arithmetic — including "how much is left" — lives here and nowhere else.
//counterfeiter:generate . Checker
type Checker interface {
	// TicketRemaining = ticket budget − SUM(ledger cost for ticket_id),
	// where ticket budget = tickets.budget_usd ?? workflow default.
	TicketRemaining(ticketID int) (Remaining, error)
	// GlobalDailyRemaining = daily cap − SUM(ledger cost since local midnight).
	GlobalDailyRemaining() (Remaining, error)
	// StepSlice resolves an agent step's budget slice: min(step slice from
	// the workflow definition, TicketRemaining). Zero/negative = do not start.
	StepSlice(ticketID int, sliceUSD float64) (Remaining, error)
	// Record appends a ledger row (append-only).
	Record(entry LedgerEntry) error
}

type Remaining struct {
	LimitUSD     float64
	SpentUSD     float64
	RemainingUSD float64
	Exhausted    bool
}

type LedgerEntry struct { /* mirrors agent_cost_ledger columns, §1.4 */ }
```

Global daily cap is a web-node flag: `--agent-daily-budget-usd` (0 = unlimited). Per-ticket default comes from the workflow definition (§6 `budget.ticket_usd`).

### 2.8 Agent + Harvest step config (plan union) — owner: **agent-step** (agent), **harvest-step** (harvest); consumers: dispatch (renderer emits exactly these), workflow-store, gateway-mcp

In `atc/steps.go` (StepConfig union, registered in `StepPrecedence` before `run:`) and `atc/plan.go`:

```go
// atc/steps.go
type AgentStep struct {
	Name           string            `json:"agent"`                      // step name
	Prompt         string            `json:"prompt,omitempty"`           // inline prompt text
	PromptFile     string            `json:"prompt_file,omitempty"`      // or artifact-relative path (exactly one of the two)
	Model          string            `json:"model,omitempty"`
	MaxTurns       int               `json:"max_turns,omitempty"`
	BudgetSliceUSD float64           `json:"budget_slice_usd,omitempty"` // 0 = uncapped within ticket budget
	OutputSchema   string            `json:"output_schema,omitempty"`    // artifact-relative JSON-schema path
	Sidecars       []SidecarSource   `json:"sidecars,omitempty"`         // existing atc.SidecarSource union
	Inputs         []string          `json:"inputs,omitempty"`           // artifact names mounted into the workspace
	Outputs        []string          `json:"outputs,omitempty"`          // artifact names exported (workspace among them)
	Env            map[string]string `json:"env,omitempty"`              // static values only
	Timeout        string            `json:"timeout,omitempty"`
	Limits         *ContainerLimits  `json:"container_limits,omitempty"`
	Requests       *ContainerLimits  `json:"container_requests,omitempty"`
}

type HarvestStep struct {
	Name       string      `json:"harvest"`               // step name
	Workspace  string      `json:"workspace"`             // input artifact containing committed work
	Repo       string      `json:"repo"`
	TicketID   int         `json:"ticket_id,omitempty"`   // 0 = no ticket (pure-CI use)
	Branch     string      `json:"branch,omitempty"`      // e.g. agent/ticket-42; empty = no push
	Push       bool        `json:"push,omitempty"`
	GatePolicy GatePolicy  `json:"gate_policy"`           // §6.3 grammar, inline
	Judge      *JudgeConfig `json:"judge,omitempty"`      // §6.4; nil = no judge
	Timeout    string      `json:"timeout,omitempty"`
}
```

**Render-time-resolution rule (binding):** the renderer resolves *everything* from the workflow definition into literal values in these structs. The agent/harvest step implementations **never read `agent_workflow_definitions`** — a rendered pipeline is self-contained and reproducible from its config alone. Ticket/run identity reaches the step via env vars (§8.1), not by lookup.

### 2.8.1 Harvest push addendum (2026-07-09, F32) — co-signed harvest-step + ticket-core

The harvest step's push to the stable ticket branch is pinned as:

```
git push --force-with-lease=refs/heads/agent/ticket-<n> origin <sha>:refs/heads/agent/ticket-<n>
```

Rationale: attempt 2+ of a rework loop runs from a **fresh clone**, so a plain push to the existing `agent/ticket-<n>` head is deterministically rejected non-fast-forward — breaking the designed re-push-refresh contract on every second harvest. `--force-with-lease` is safe here because §8.3 makes harvest the branch's **only credentialed writer** (the PAT is scoped to `agent/*` branches), and push-by-sha guarantees are unaffected (the pushed sha is recorded in `agent_reviews.pushed_sha` / `push.done` regardless of what the branch previously pointed at). Per-attempt branch names are **FORBIDDEN** — they break §1.7's `agent/ticket-<id>` = branch identity and the outcome watcher's merge detection (§1.11). The harvest exec MUST include a divergent-remote-head fixture spec (remote branch pre-seeded at a different sha → push succeeds).

---

## 3. MCP tool schemas

All three sidecars are MCP servers speaking **streamable HTTP** (single `POST /mcp` endpoint) on fixed localhost ports (§8.1). Tool registration style follows `atc/api/mcpserver` (`AddTool(name, description, jsonSchema, handler)`). Every tool result is a single JSON object. **[DECIDED HERE: streamable HTTP (not stdio) so platform code — the harvest step's Go client — can call the same endpoint agents use, and so sidecars survive agent-process restarts.]**

**Transport (normative, 2026-07-09, F13):** all three sidecars — dev-mcp, platform-mcp, gateway — implement the **SSE progress path**: when a `tools/call` request carries `Accept: text/event-stream` and `params._meta.progressToken`, the server responds `Content-Type: text/event-stream` (immediate 200 + flush), emits `notifications/progress` frames on a coalescing heartbeat ticker, and delivers the final JSON-RPC tools/call response as the **last SSE frame** (frame format and notification shape per §3.1 and 04-dev-mcp's §3-preamble addendum, the wire spec of record). **Any MCP tool whose handler can block longer than 30s MUST be served over the SSE progress path, and the server MUST emit heartbeats even when the handler produces no output.** Rationale (empirical, claude CLI 2.1.77): the CLI abandons a progress-free buffered `tools/call` at **exactly 60s** — silently, with the model seeing "(completed with no output)" and no error flag; `MCP_TOOL_TIMEOUT` is NOT a mitigation (it drives only the outer ~27.8h watchdog, not the inner SDK 60s abort). Module-boundary rule: `ci-agent` and the root module never `require` each other, so this is an explicit **mirrored implementation** — main-module servers (platform-mcp `agent/platformmcp`, gateway `agent/gatewaymcp`) build on the in-place-upgraded `atc/api/mcpserver` (a byte-similar port of `ci-agent/devmcp`'s SSE path); dev-mcp keeps `ci-agent/devmcp` as the reference implementation; drift is guarded by mirrored server tests asserting the identical frame shape.

Common error taxonomy (all dev-mcp tools, mirrored by gateway): the tool **succeeds at the MCP layer** whenever it could run; the payload carries `"status": "ok" | "failed" | "error"` — `ok` = the checked thing passed, `failed` = it ran and found problems (tests failed, lint findings), `error` = tooling itself broke. MCP-level errors are reserved for malformed input.

### 3.1 dev-mcp — owner: **dev-mcp**; consumers: harvest-step, agent-step, process-intel-experiments

**`list_components`** — input:
```json
{ "type": "object", "properties": {}, "additionalProperties": false }
```
result:
```json
{
  "type": "object",
  "required": ["components"],
  "properties": {
    "components": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["id", "description", "paths", "kind"],
        "properties": {
          "id":          { "type": "string", "description": "opaque, repo-defined, stable" },
          "description": { "type": "string" },
          "paths":       { "type": "array", "items": { "type": "string" } },
          "kind":        { "type": "string", "enum": ["service", "library", "cli", "web", "docs", "other"] }
        }
      }
    }
  }
}
```

**`build`** — input:
```json
{
  "type": "object",
  "properties": {
    "component": { "type": "string", "description": "component id; omitted = whole repo" }
  },
  "additionalProperties": false
}
```
result (shared by `build`, `run_tests`, `lint`):
```json
{
  "type": "object",
  "required": ["status", "summary", "duration_seconds"],
  "properties": {
    "status":           { "type": "string", "enum": ["ok", "failed", "error"] },
    "summary":          { "type": "string", "description": "one-paragraph human/agent-readable outcome" },
    "duration_seconds": { "type": "number" },
    "output_tail":      { "type": "string", "description": "last <=200 lines of combined output" },
    "log_path":         { "type": "string", "description": "workspace-relative path to the full log file" },
    "failures": {
      "type": "array",
      "description": "structured failures when parseable (test names, lint rules)",
      "items": {
        "type": "object",
        "required": ["id", "message"],
        "properties": {
          "id":      { "type": "string" },
          "message": { "type": "string" },
          "path":    { "type": "string" },
          "line":    { "type": "integer" }
        }
      }
    }
  }
}
```

**`run_tests`** — input:
```json
{
  "type": "object",
  "properties": {
    "component": { "type": "string" },
    "focus":     { "type": "string", "description": "test-name filter, implementation-defined semantics (e.g. ginkgo --focus)" }
  },
  "additionalProperties": false
}
```
Streaming semantics: implementations MUST emit `notifications/progress` at least every 30s during long runs (message = current suite/package) so callers can distinguish "slow" from "hung"; the harvest client applies a per-gate timeout (§6.3), not a transport timeout. *(2026-07-09, F13)* This mandate applies to **ALL THREE sidecars** (dev-mcp, platform-mcp, gateway), not dev-mcp alone: default heartbeat **15s** (`DefaultHeartbeat` in both `ci-agent/devmcp` and the upgraded `atc/api/mcpserver` — half the 30s bound, 4× margin under the empirical 60s claude-CLI abandonment), overridable per role via `DEV_MCP_PROGRESS_INTERVAL` / `PLATFORM_MCP_PROGRESS_INTERVAL` / `GATEWAY_MCP_PROGRESS_INTERVAL` (Go duration syntax; a set-but-invalid value, a value ≤ 0, or a value > 30s is a **fatal startup error** — never clamped silently).

**`lint`** — input: same shape as `build`. Result: shared result schema above.

**`affected_components`** — input:
```json
{
  "type": "object",
  "required": ["changed_paths"],
  "properties": {
    "changed_paths": { "type": "array", "items": { "type": "string" } }
  },
  "additionalProperties": false
}
```
result:
```json
{
  "type": "object",
  "required": ["components"],
  "properties": {
    "components": { "type": "array", "items": { "type": "string" }, "description": "component ids; empty array = nothing mapped (caller policy decides full-suite fallback)" },
    "unmapped_paths": { "type": "array", "items": { "type": "string" } }
  }
}
```

The dev-mcp workstream also ships:
- **Go client** `agent/devmcp/client.go`: `type Client interface { ListComponents(ctx) ([]Component, error); Build(ctx, component string) (*ToolResult, error); RunTests(ctx, component, focus string) (*ToolResult, error); Lint(ctx, component string) (*ToolResult, error); AffectedComponents(ctx, paths []string) ([]string, error) }` — the harvest step's only way to run gates.
- **Contract-test kit** `agent/devmcp/contracttest`: a Go test helper (`contracttest.Run(t, endpointURL)`) any repo's implementation runs in its own CI; validates schemas, error taxonomy, progress emission.
- This repo's own implementation under `ci-agent/cmd/dev-mcp` (components: `atc`, `fly`, `web`, `ci-agent`, `topgun`, mapped from the Makefile targets in CLAUDE.md).

### 3.2 platform-mcp — owner: **platform-mcp-hitl**; consumers: dispatch, workflow-store, process-intel-experiments

All tools operate on the ticket identified by `AGENT_TICKET_ID` in the sidecar's env (§8.1) — agents cannot address other tickets. The sidecar calls the ATC API with its principal token (scopes `tickets:read`, `tickets:write`).

**Read model [DECIDED HERE — supersedes rendered-spec/plan env injection]:** agents reach the ticket's spec and plan **only** through these platform-mcp read tools (`read_ticket`, `list_tasks`, `get_task`); no spec/plan bytes are injected into any agent step by default. The tools back onto ticket-core `Store` methods (`Get` / `LatestSpec` / `ActivePlan`, §2.1). The optional file-mount delivery path is workflow-definition opt-in (`spec_delivery: files`, §6) and is read-only; `update_task_status` remains the write-back in both delivery modes.

**`read_ticket`** — input: `{ "type": "object", "properties": {}, "additionalProperties": false }`
result (envelope + spec **only**; tasks are reached via `list_tasks`/`get_task`):
```json
{
  "type": "object",
  "required": ["ticket"],
  "properties": {
    "ticket": {
      "type": "object",
      "required": ["id", "title", "repo", "state", "budget_usd", "workflow_name", "workflow_version"],
      "properties": {
        "id":               { "type": "integer" },
        "title":            { "type": "string" },
        "repo":             { "type": "string" },
        "state":            { "type": "string" },
        "budget_usd":       { "type": "number" },
        "workflow_name":    { "type": "string" },
        "workflow_version": { "type": "integer" }
      }
    },
    "spec": {
      "description": "latest spec, if any — null when the ticket has no spec, the normal state for spec-less workflows (§1.7)",
      "type": ["object", "null"],
      "required": ["title", "acceptance_criteria", "body_md"],
      "properties": {
        "title":               { "type": "string" },
        "acceptance_criteria": { "type": "array", "items": { "type": "string" } },
        "body_md":             { "type": "string" }
      }
    }
  }
}
```

**`list_tasks`** — input: `{ "type": "object", "properties": {}, "additionalProperties": false }`
result (cheap skeleton of the active plan — **no** detail bodies):
```json
{
  "type": "object",
  "required": ["tasks"],
  "properties": {
    "tasks": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["ordering", "title", "status"],
        "properties": {
          "ordering": { "type": "integer" },
          "title":    { "type": "string" },
          "status":   { "type": "string" }
        }
      }
    }
  }
}
```

A ticket with no active plan yields `{"tasks": []}` — a **successful** result, not an error; plan-lessness is how agents in plan-less workflows discover there is nothing to walk (§1.7 zero-rows rule).

**`get_task`** — input:
```json
{
  "type": "object",
  "required": ["ordering"],
  "properties": {
    "ordering": { "type": "integer" }
  },
  "additionalProperties": false
}
```
result (one active-plan task with its detail body):
```json
{
  "type": "object",
  "required": ["ordering", "title", "status", "detail_md"],
  "properties": {
    "ordering":  { "type": "integer" },
    "title":     { "type": "string" },
    "status":    { "type": "string" },
    "detail_md": { "type": "string" }
  }
}
```
An unknown `ordering` (not present in the active plan) is an MCP tool error (`isError=true`) — a `tools/call` result carrying the error text, matching how the shared `atc/api/mcpserver` surfaces every handler error — not an `ok`-with-empty result. (This is a tool-level error, NOT a JSON-RPC `-32602` error object: the shared mcpserver only emits `-32602` for a malformed `tools/call` envelope, never for a tool handler's returned error.) *(2026-07-09 amendment, §11 — FLOWS.md E5)* The same mechanism applies when the ticket has **no active plan at all**: `get_task` returns an MCP tool error (`isError=true`) whose text names the absent plan — `no active plan for ticket <id>` — because the caller addressed a specific task that cannot exist. This is deliberate asymmetry with `list_tasks` (empty list, success): agents discover plan-lessness via `list_tasks`, and only a mis-addressed `get_task` errors.

**`submit_spec`** — input:
```json
{
  "type": "object",
  "required": ["title", "body"],
  "properties": {
    "title":               { "type": "string" },
    "body":                { "type": "string", "description": "markdown; rationale and tradeoffs belong here" },
    "acceptance_criteria": { "type": "array", "items": { "type": "string" }, "minItems": 1 },
    "links":               { "type": "array", "items": { "type": "object", "required": ["title","url"], "properties": { "title": {"type":"string"}, "url": {"type":"string"} } } }
  },
  "additionalProperties": false
}
```
result: `{ "required": ["version"], "properties": { "version": { "type": "integer" } } }`

**`submit_plan`** — input:
```json
{
  "type": "object",
  "required": ["tasks"],
  "properties": {
    "tasks": {
      "type": "array", "minItems": 1,
      "items": {
        "type": "object",
        "required": ["title"],
        "properties": {
          "title":  { "type": "string" },
          "detail": { "type": "string", "description": "optional markdown" }
        }
      }
    }
  },
  "additionalProperties": false
}
```
result: `{ "required": ["plan_version"], "properties": { "plan_version": { "type": "integer" } } }` — replaces the active plan (new `plan_version`, orderings 1..N as given).

**`update_task_status`** — input:
```json
{
  "type": "object",
  "required": ["ordering", "status"],
  "properties": {
    "ordering": { "type": "integer", "minimum": 1, "description": "task position in the active plan" },
    "status":   { "type": "string", "enum": ["pending", "in_progress", "done", "skipped", "blocked"] },
    "note":     { "type": "string" }
  },
  "additionalProperties": false
}
```
result: `{ "properties": { "ok": { "type": "boolean" } } }`

**`ask_human`** — input:
```json
{
  "type": "object",
  "required": ["question"],
  "properties": {
    "question": { "type": "string", "description": "markdown; include enough context to answer without reading the transcript" },
    "options":  { "type": "array", "items": { "type": "string" }, "description": "optional multiple choice; empty = free text" },
    "default":  { "type": "string", "description": "answer used if the question times out under timeout_policy=default; stored in agent_run_questions.default_answer. Must be one of options when options are given. When the sidecar's PLATFORM_MCP_ASK_TIMEOUT_POLICY=default, omitting it is an MCP-level input error" }
  },
  "additionalProperties": false
}
```
result (returned only when the answer arrives — this is the parking tool):
```json
{
  "type": "object",
  "required": ["answer"],
  "properties": {
    "answer":      { "type": "string" },
    "answered_by": { "type": "string" },
    "timed_out":   { "type": "boolean", "description": "true when timeout_policy=default supplied the answer" }
  }
}
```

*(2026-07-10 amendment, §11 — PARK-V2, IDEMPOTENT-BY-QUESTION)* **Tool-description note (added to the `ask_human` description shipped to agents):** repeated byte-identical questions within one step return the first answer (§1.9 find-or-create on `question_hash`); agents needing a fresh answer must vary the question text.

**Park/resume protocol [DECIDED HERE]:** `ask_human` inserts an `agent_run_questions` row, fires the notification (§8.4), and then **blocks the MCP call** while the sidecar long-polls `GET /api/v1/agent/tickets/:id/questions/:qid` until `answered_at` is set. The agent process is idle-waiting on a tool call — the jetbridge supervisor's existing resume semantics keep the pod alive across web restarts, and the pipeline-run completion contract (§1.5) counts the build as running. Timeout behavior comes from the workflow definition's `hitl` block (§6), resolved at render time into sidecar env (`PLATFORM_MCP_ASK_TIMEOUT_SECONDS`, `PLATFORM_MCP_ASK_TIMEOUT_POLICY`). **Timeout resolution [DECIDED HERE]:** the sidecar itself enforces the timeout around its long-poll; on expiry it is the writer that resolves the question row, via `AnswerAgentQuestion` with its principal token (scope `questions:answer`, §4.1) — no human is involved. Policy `default`: it submits `answer` = the call's `default` field (persisted as `agent_run_questions.default_answer` at insert time) with `answered_by` = its principal name, then returns the ask_human result with `timed_out: true`. Policy `fail`: it resolves the row the same way with an empty answer (so the open-questions index and ticket UI release) and fails the `ask_human` call at the MCP layer, failing the step. Policy `park`: no timeout (`timeout_seconds` 0); the row is only ever resolved by a human. Either way a timed-out row never stays open.

**Checkpoints [REWRITTEN 2026-07-09, F14 — supersedes and RETRACTS the F1 wording; co-signed dispatch + platform-mcp-hitl + ticket-core + harvest-step]:** checkpoints reuse the same `agent_run_questions` park/resume table (`kind = 'checkpoint'`) but are **not** an agent/LLM step: the renderer emits a deterministic, bare `atc.TaskStep` (never wrapped in try/on_failure/ensure) that runs the checkpoint CLIENT, `platform-mcp checkpoint --name <n> [--description <d>]`. The client is an **unauthenticated deterministic CLI** that talks ONLY to the pod-local platform sidecar over loopback HTTP: it derives the endpoint by trimming the `/mcp` suffix from `PLATFORM_MCP_URL` and appending `/checkpoint`, then POSTs `{"name", "description?"}` with an `http.Client` that has **no timeout** (it blocks while parked); connection errors are retried up to 60 attempts × 5s (the sidecar may still be starting). CLIENT AUTH: **NONE** — the pod boundary is the auth boundary; the client reads only `PLATFORM_MCP_URL` (required; exit 2 if unset), and its other env rows (§8.1) are provenance/logging only. The **SIDECAR is the trust boundary**: it alone holds `AGENT_PRINCIPAL_TOKEN`; its `POST /checkpoint` handler (same mux as `/mcp` and `GET /healthz` on `MCP_LISTEN_ADDR`) files the `kind='checkpoint'` row via the ATC API (question `Approve checkpoint %q for ticket %d?` or the description when given; `options = ["approve","reject"]`, §1.9; `timeout_policy='park'`, `timeout_seconds=0`; `step_name` = its `AGENT_STEP_NAME`, i.e. `checkpoint-<name>`; `pipeline_run_id` = `AGENT_PIPELINE_RUN_ID`; `build_id=0` in v1 — pipeline_run_id + step_name are the join keys), fires the notification (§8.4), long-polls `GET /api/v1/agent/tickets/:id/questions/:qid` until `answered_at` is set, and responds `200 {"approved": <answer=="approve">, "answer", "answered_by"}`; ATC transport errors filing or awaiting → 502 (the reservation is kept so a client retry re-awaits the same open row); a second POST for the same name joins the open row (per-name dedup — exactly one row). The ATC answer route **rejects an answer not in the row's options when `kind='checkpoint'`**, so the stored answer is exactly `approve` or `reject`. The client translates the response into its **exit code**: 0 = approved, 1 = rejected OR non-200 OR bad response OR retries exhausted (a fatal-auth failure from the sidecar's AwaitAnswer surfaces as exit 1 with stderr prefix `principal rejected:`), 2 = usage error (missing `--name` or `PLATFORM_MCP_URL`). Exit-code chain: exit 1 fails the task ⇒ build ⇒ run completes failed; dispatch's **run-completion reconciler** then walks the ticket — latest checkpoint row answered `reject` with `on_reject: send_back` ⇒ `running→queued` (attempt_count++, capped by the dispatch MaxAttempts guard); `on_reject: fail` (or step not found in the frozen config) ⇒ `running→needs_review`; unanswered checkpoint rows on a completed run ⇒ `running→errored` and the orphaned rows are released. RETRACTED (from the F1 amendment of 2026-07-09 and the prior text here): the sentences stating that the client "inserts the row", "long-polls the ATC route", "reads reject-policy from argv", and is "NOT a call to a sidecar internal checkpoint endpoint" — the sidecar-POST model above is the one wire model. Unchanged: there are no `PLATFORM_MCP_CHECKPOINT` / `PLATFORM_MCP_CHECKPOINT_ON_REJECT` env vars, and the reject-policy (`on_reject`, §6.1) is consumed by the reconciler from the ticket's frozen workflow config — the client and sidecar never see it.

**PARK TRANSPORT [DECIDED HERE — 2026-07-09, F13]:** park REQUIRES the MCP transport to keep the blocked `tools/call` alive with `notifications/progress` heartbeats strictly less than 60s apart — the claude CLI abandons a progress-free call at exactly 60s, silently ("(completed with no output)", no error flag; empirical, claude 2.1.77; `MCP_TOOL_TIMEOUT` does not prevent it). `ask_human` is therefore served **only** over the SSE path (§3 preamble); its handler emits `parked: waiting for human answer to question <id>` at park start and the heartbeat ticker repeats it. The checkpoint `POST /checkpoint` internal endpoint is **exempt** (not an MCP tools/call; no claude CLI in the loop) but its serving `http.Server` must set `WriteTimeout: 0` and `IdleTimeout: 0` (`ReadHeaderTimeout: 5s` allowed) — any nonzero WriteTimeout severs long SSE streams and blocking checkpoint responses — and the checkpoint client's `http.Client` MUST have no global timeout. **PARK-DURATION BOUNDS:** a park is bounded by (1) **pod lifetime** — requires the pause-loop fix (F31 leg 1, jetbridge `container.go`: the pause command loops its bounded sleep so parked pods survive past 24h, §11), and (2) **principal lifetime** — park-policy runs are minted with `expires_at = now + --agent-park-timeout` (default 72h), never NULL; expiry remains the hard backstop, and a parked question outliving it fails **loudly** per the AwaitAnswer fatal-auth contract (consecutive-401/403 limit; transport/5xx errors still retried forever for web-restart survival — plan 08). *(2026-07-10, PARK-V2)* These bounds now govern the SHORT-PARK path only; past the short-park threshold the step exits (below) and neither bound applies — the wait is represented by the open question row and the `awaiting_human` run state (§1.5), backstopped by the same `--agent-park-timeout` wall clock.

**PARK-V2 — SHORT-PARK vs LONG-PARK (exit-and-respawn) [DECIDED 2026-07-10, §11 PARK-V2 seam delta; amends PARK TRANSPORT and PARK-DURATION BOUNDS; implements FLOWS.md P2.5 #1–#4]:** the SSE park above is retained as the **SHORT-PARK** mechanism and is unchanged below a threshold; beyond the threshold the agent EXITS and the answer RESPAWNS it (`claude -p --resume <session-id>`). A wait stops impersonating a running step. Nothing in F13 (SSE heartbeats), F31 legs 1–3, or the checkpoint seam is retracted — PARK-V2 sits above them and makes the >threshold branch of each moot.

- **Threshold:** new web flag `--agent-short-park-max` (`CONCOURSE_AGENT_SHORT_PARK_MAX`, `time.Duration`, DEFAULT `30m`, defined in `atc/atccmd/command.go` beside `--agent-park-timeout`). `0` disables exit-and-respawn entirely (pure PARK-V1 behavior — the rollback/escape hatch). Rendered by dispatch's renderer into the platform sidecar env as `PLATFORM_MCP_SHORT_PARK_MAX_SECONDS` (§8.1; literal; `0` = never exit). Per-workflow override (`hitl.short_park_max_seconds`) is explicitly DEFERRED — global flag only in v1. The threshold TIMER is owned by the platform SIDECAR (it starts the park, holds `asked_at`, and already runs the SSE heartbeat ticker); it applies to BOTH `ask_human` parks and `/checkpoint` parks. Rationale for 30m: strictly below every surviving F31 ceiling (~4h kubelet SPDY idle severance is the smallest); Anthropic prompt-cache TTL is 5 min, so any park past a few minutes pays a full cache-miss re-send whether the process lived or died — the only marginal cost of exiting is pod re-schedule, while work-hours checkpoint approvals keep the cheap SSE path.
- **Exit signal — sentinel file, not an HTTP poll:** when a park crosses the threshold, the platform sidecar atomically writes `flight/park.json` (write temp + `mv`; the alpine sidecar image has `mv` per F28) with payload `{"question_id": N, "kind": "question|checkpoint", "step_name": "...", "asked_at": RFC3339, "threshold_seconds": N, "crossed_at": RFC3339}`. This works because jetbridge's `buildSidecarContainers(sidecars, mainMounts, ...)` gives sidecars the main container's mounts — the flight volume is already shared. Chosen over a runner-polls-`GET /park-status` design because: (a) zero new HTTP surface; (b) the signal survives a sidecar crash/restart; (c) the file rides the ingested flight artifact, so the park-exit is post-mortem provenance for free; (d) the runner's watch is a trivial 5s stat loop with no liveness coupling to the sidecar.
- **Runner behavior** (`agent-runner`, plan 07 Task 24, amending the Task 15 runner): while claude runs, a watcher goroutine stats `flight/park.json` every 5s. On appearance: SIGTERM the claude process; wait `AGENT_PARK_EXIT_GRACE_SECONDS` (§8.1, default `30`); SIGKILL fallback. Transcript safety: the session JSONL is appended incrementally per message, so the assistant message carrying the pending `tool_use` is on disk BEFORE the MCP call resolves — even SIGKILL cannot lose the pending call (empirical pin P2, plan 07's `TestLiveClaudeParkExitResume`). The runner then copies the session JSONL to `flight/session.jsonl`, writes `flight/results.json` with wire status `"parked"`, `session_id`, and best-effort usage/cost accumulated from the teed stream-json events (claude may emit no final envelope on SIGTERM — the runner MUST NOT require one; pin P3), and exits with code **86** (frozen: `agent-runner` awaiting-human exit code).
- **Sidecar side:** when the runner kills claude, the blocked MCP `tools/call` connection drops; the sidecar cancels its `AwaitAnswer` long-poll on client-disconnect context cancellation. The question row STAYS OPEN (`answered_at IS NULL`) — it is the durable representation of the wait.
- **Checkpoint variant** (no claude in the loop): at threshold the sidecar's `/checkpoint` handler responds `202 {"parked": true}` to the blocked client POST; the checkpoint client gains frozen exit code **3** = parked-past-threshold (existing 0/1/2 unchanged). No TaskStep exec change: exit 3 fails the task, which is exactly the carrier we want (below).
- **The step's DISTINGUISHED END:** no fifth Concourse BUILD status is added (a build-status enum change ripples through fly/Elm/DB everywhere); the build finishes `failed` as a **carrier only**. The distinguished end is the triple: (1) exit code 86 (agent) / 3 (checkpoint client); (2) a new typed build event `awaiting_human` emitted by the AgentStep exec (additive `atc/event` type) plus the §5 flight event `step.park`; (3) a new `agent_run_step_state` row (§1.14). The authority the platform acts on is the OPEN park-policy question row — **never the build status**. A genuine failure has no open park row; a pod that DIES while parked leaves the open row and therefore still resumes — park now survives pod death, an improvement over PARK-V1, where a dead pod was a lost park.
- **RESUME — who re-arms:** dispatch's F17 run-completion reconciler is EXTENDED with a sibling pass `reconcileAwaitingRuns` (plan 11, Task 11c; same `Dispatcher.Run` pass, same polling+notify component — no new component constant). The ATC `AnswerAgentQuestion` route additionally fires the dispatcher's component notify so resume is prompt (polling remains the fallback; never notify-only). Per candidate (run `status = 'awaiting_human'` AND zero open park questions remain): (1) re-mint the per-run principal `agent-run-<run-id>` — revoke-and-recreate with `expires_at = now + RunPrincipalTimeout(cfg, workflow)`; (2) refresh the ephemeral secret via `credentials.SecretAttacher.Attach` (create-or-update, §2.6/§8.2; credential re-resolved through `credentials.Backend`, so a rotated user OAuth token is honored); (3) trigger a CONTINUATION BUILD — a manual build of the same entry job on the same instanced pipeline via the existing `db.Job.CreateBuild` seam, `created_by = "agent-dispatcher:resume"`. The lifecycler then flips the run to `running` (§1.5).
- **Continuation build semantics** (the exec consults `agent_run_step_state`, keyed `(pipeline_run_id, step_name)` — no per-build vars needed): a step with row `state = 'completed'` **SHORT-CIRCUIT REPLAYS** — restore the recorded output artifact handles from the fabric, register them, emit `step.start` (with `"replayed": true`) + `step.end`, return success; zero claude, zero cost. This is REQUIRED, not optional — standard-dev's plan-approval checkpoint parking overnight is the common case, and without short-circuit every resume would re-run `write-spec` cold. A step with row `state = 'awaiting_human'` **RESUMES** — restore the recorded artifacts as inputs (workspace + flight), set `AGENT_SESSION_ID` / `AGENT_SESSION_FILE` (§8.1); the runner installs the file at `~/.claude/projects/<cwd-slug>/<session-id>.jsonl` and invokes `claude -p --resume <AGENT_SESSION_ID> "<continuation prompt>" --output-format stream-json ...` with the frozen `agentrunner.ContinuationPrompt` = "Your wait for a human has ended and the question has been answered. Re-issue your pending platform-mcp tool call to receive the answer, then continue your task. If your step's goal is already complete, finish now." A step with no row runs cold (steps after the parked one). Checkpoint steps need no row: the client re-POSTs `/checkpoint`, the §1.9 DB-level dedup finds the answered row, and it exits 0/1 immediately.
- **Key protocol rule — IDEMPOTENT-BY-QUESTION (§1.9):** the resumed agent re-issues the pending MCP call and the sidecar returns the already-answered row IMMEDIATELY (no park, no SSE wait). The continuation prompt makes the model re-issue; the dedup makes re-issuing safe; neither depends on whether the CLI synthesized an interrupted tool_result (empirical pin P4 verifies the mechanism end-to-end).
- **Budget:** the continuation is the SAME step for budget-slice purposes and there is NO double admission — `Checker.StepSlice(ticketID, sliceUSD)` is a RESOLUTION (min of slice and ticket remaining), not a reservation. The continuation exec calls it again naturally at step start, and since the park-exit partial spend was already ledgered (§1.8/§1.14), the re-resolved slice is automatically TIGHTER. Re-resolution can only shrink, never double-count: the append-only ledger is the single spend authority and each execution writes its own `(build_id, plan_id)`-keyed row.

### 3.3 agent-gateway-mcp — owner: **gateway-mcp**; consumers: scorecards, process-intel-experiments

**`request_review`** — input:
```json
{
  "type": "object",
  "required": ["diff"],
  "properties": {
    "diff":     { "type": "string", "description": "unified diff to review" },
    "rubric":   { "type": "string", "description": "markdown rubric/instructions; default = general correctness review" },
    "context":  { "type": "string", "description": "optional extra context (spec excerpt, constraints)" },
    "provider": { "type": "string", "enum": ["claude", "codex", "cursor"], "default": "claude" },
    "model":    { "type": "string", "description": "provider-specific model id; empty = adapter default" }
  },
  "additionalProperties": false
}
```
result:
```json
{
  "type": "object",
  "required": ["status", "findings", "summary", "usage"],
  "properties": {
    "status":  { "type": "string", "enum": ["ok", "failed", "error"], "description": "error = provider/adapter broke; failed = review could not be produced (e.g. budget cutoff)" },
    "summary": { "type": "string" },
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["id", "severity", "category", "message"],
        "properties": {
          "id":       { "type": "string" },
          "severity": { "type": "string", "enum": ["critical", "major", "minor", "info"] },
          "category": { "type": "string" },
          "message":  { "type": "string" },
          "path":     { "type": "string" },
          "line":     { "type": "integer" }
        }
      }
    },
    "usage": { "$ref": "#/defs/GatewayUsage" }
  }
}
```

**`ask_agent`** — input:
```json
{
  "type": "object",
  "required": ["prompt"],
  "properties": {
    "prompt":        { "type": "string" },
    "provider":      { "type": "string", "enum": ["claude", "codex", "cursor"], "default": "claude" },
    "model":         { "type": "string" },
    "output_schema": { "type": "string", "description": "optional JSON schema the answer must satisfy" }
  },
  "additionalProperties": false
}
```
result:
```json
{
  "type": "object",
  "required": ["status", "answer", "usage"],
  "properties": {
    "status": { "type": "string", "enum": ["ok", "failed", "error"] },
    "answer": { "type": "string", "description": "text, or JSON string when output_schema was given" },
    "usage":  { "$ref": "#/defs/GatewayUsage" }
  }
}
```

`GatewayUsage` (embedded in every result AND emitted as a `subagent.result` + `cost.record` event, §5):
```json
{
  "type": "object",
  "required": ["provider", "model", "cost_usd", "duration_ms"],
  "properties": {
    "provider":      { "type": "string" },
    "model":         { "type": "string" },
    "input_tokens":  { "type": "integer" },
    "output_tokens": { "type": "integer" },
    "turns":         { "type": "integer" },
    "cost_usd":      { "type": "number" },
    "duration_ms":   { "type": "integer" }
  }
}
```

**Provider adapter interface** (`agent/gateway/adapter.go`):
```go
//counterfeiter:generate . Adapter
type Adapter interface {
	Name() string // "claude" | "codex" | "cursor"
	// Invoke runs one subagent call in-sidecar. maxCostUSD is the remaining
	// budget slice; the adapter passes provider-native limits where they
	// exist and the gateway hard-stops the subprocess when the metered
	// running cost would exceed it.
	Invoke(ctx context.Context, req Request, maxCostUSD float64) (*Response, error)
}
```
**Cutoff contract:** when the budget slice is exhausted mid-call, the gateway returns `status: "failed"` with `summary` prefixed `"budget cutoff:"` and full usage-so-far — never a silently truncated `ok`.

---

## 4. API route additions

### 4.1 Auth tiers and principal scopes — owner: **agent-identity**

Existing tiers in `atc/wrappa/api_auth_wrappa.go`: `authenticated`, `authenticated-if-provided`, `admin`, `authorized (team role via accessor/roles.go)`, and the `SubmitAgentReview` pass-through. agent-identity adds one tier:

- **`principal(<scope>)`** — a new wrappa case group wrapping handlers in `auth.CheckAgentPrincipalHandler(handler, rejector, scope)`: parses `Authorization: Bearer cap1.<id>.<secret>`, verifies against `agent_principals` (§1.2), requires `<scope>` ∈ row scopes. Routes in this tier also accept a normal admin user token (so `fly curl` debugging works). `SubmitAgentReview` moves from pass-through to `principal(reviews:write)` with the legacy static token accepted inside the handler until removal.

Scope vocabulary (closed set; adding one requires agent-identity sign-off): `reviews:write`, `tickets:read`, `tickets:write`, `metrics:write`, `costs:write`, `questions:answer` (held by the platform-mcp sidecar's per-run principal solely to resolve timed-out questions via `AnswerAgentQuestion`, §3.2; the long-poll *read* uses `tickets:read`). `runs:create` is **removed** from v1: dispatch and the experiments runner are in-process ATC components that call `PipelineRunFactory` (§2.3) directly rather than the HTTP API, so no principal-authenticated route grants it; re-adding it requires agent-identity sign-off like any other scope.

### 4.2 Route table

Names/paths follow `atc/routes.go` rata style; constants live in `atc/routes.go`, wrappa entries in the exhaustive switch, role entries in `accessor/roles.go` (every `authorized` route MUST get a `DefaultRoles` entry — the file's own comment warns missing entries silently become admin-only).

| Route name | Method | Path | Auth tier | Owner |
|---|---|---|---|---|
| `CreateAgentPrincipal` | POST | `/api/v1/agent/principals` | admin | agent-identity |
| `ListAgentPrincipals` | GET | `/api/v1/agent/principals` | admin | agent-identity |
| `RevokeAgentPrincipal` | DELETE | `/api/v1/agent/principals/:principal_id` | admin | agent-identity |
| `SetAgentUserCredential` | PUT | `/api/v1/agent/user-credentials` | authenticated (self only) | credentials-and-budgets |
| `GetAgentUserCredentialStatus` | GET | `/api/v1/agent/user-credentials` | authenticated (self only) | credentials-and-budgets |
| `DeleteAgentUserCredential` | DELETE | `/api/v1/agent/user-credentials/:kind` | authenticated (self only) | credentials-and-budgets |
| `GetAgentCostRollup` | GET | `/api/v1/agent/costs` (`?group_by=user\|ticket\|day\|workflow&since=&until=`) | authorized viewer | credentials-and-budgets |
| `SubmitAgentCostRecord` | POST | `/api/v1/agent/costs` | principal(costs:write) | credentials-and-budgets |
| `CreatePipelineRun` | POST | `/api/v1/teams/:team_name/pipelines/:pipeline_name/runs` | authorized member | pipeline-runs |
| `ListPipelineRuns` | GET | `/api/v1/teams/:team_name/pipelines/:pipeline_name/runs` | authorized viewer | pipeline-runs |
| `GetPipelineRun` | GET | `/api/v1/teams/:team_name/pipelines/:pipeline_name/runs/:run_number` | authorized viewer | pipeline-runs |
| `ListAgentWorkflows` | GET | `/api/v1/agent/workflows` | authorized viewer | workflow-store |
| `ListAgentWorkflowVersions` | GET | `/api/v1/agent/workflows/:workflow_name/versions` | authorized viewer | workflow-store |
| `GetAgentWorkflowVersion` | GET | `/api/v1/agent/workflows/:workflow_name/versions/:version` | authorized viewer | workflow-store |
| `CreateAgentWorkflowVersion` | POST | `/api/v1/agent/workflows/:workflow_name/versions` | authorized member | workflow-store |
| `PromoteAgentWorkflowVersion` | PUT | `/api/v1/agent/workflows/:workflow_name/versions/:version/live` | authorized member | workflow-store |
| `ListAgentTickets` | GET | `/api/v1/agent/tickets` (`?state=&repo=&origin=&limit=`) | authorized viewer | ticket-core |
| `CreateAgentTicket` | POST | `/api/v1/agent/tickets` | authorized member; also principal(tickets:write) for `origin: retrospective` | ticket-core |
| `GetAgentTicket` | GET | `/api/v1/agent/tickets/:ticket_id` | authorized viewer; also principal(tickets:read) | ticket-core |
| `UpdateAgentTicket` | PUT | `/api/v1/agent/tickets/:ticket_id` | authorized member | ticket-core |
| `TransitionAgentTicket` | PUT | `/api/v1/agent/tickets/:ticket_id/state` | authorized member; also principal(tickets:write) | ticket-core |
| `SubmitAgentTicketSpec` | POST | `/api/v1/agent/tickets/:ticket_id/spec` | principal(tickets:write); also authorized member | ticket-core |
| `SubmitAgentTicketPlan` | POST | `/api/v1/agent/tickets/:ticket_id/plan` | principal(tickets:write); also authorized member | ticket-core |
| `UpdateAgentTicketTask` | PUT | `/api/v1/agent/tickets/:ticket_id/tasks/:ordering` | principal(tickets:write) | ticket-core |
| `SubmitAgentRunMetrics` | POST | `/api/v1/agent/metrics` | principal(metrics:write) | agent-step |
| `ListAgentRunMetrics` | GET | `/api/v1/agent/tickets/:ticket_id/metrics` | authorized viewer | agent-step |
| `AskAgentQuestion` | POST | `/api/v1/agent/tickets/:ticket_id/questions` | principal(tickets:write) | platform-mcp-hitl |
| `GetAgentQuestion` | GET | `/api/v1/agent/tickets/:ticket_id/questions/:question_id` (long-poll `?wait=30s`) | principal(tickets:read); also authorized viewer | platform-mcp-hitl |
| `AnswerAgentQuestion` | PUT | `/api/v1/agent/tickets/:ticket_id/questions/:question_id/answer` | authorized member; also principal(questions:answer) — timeout resolution only, §3.2 (handler distinguishes it via `answered_by` = principal name + the sidecar-set timeout fields) | platform-mcp-hitl |
| `SetAgentTicketDisposition` | PUT | `/api/v1/agent/tickets/:ticket_id/disposition` | authorized member | delivery-outcomes |
| `GetAgentTicketOutcome` | GET | `/api/v1/agent/tickets/:ticket_id/outcome` | authorized viewer | delivery-outcomes |
| `GetAgentWorkflowScorecard` | GET | `/api/v1/agent/workflows/:workflow_name/scorecard` (`?versions=3,4`) | authorized viewer | scorecards |
| `ListAgentBenchmarkCases` / `CreateAgentBenchmarkCase` | GET/POST | `/api/v1/agent/benchmarks` | authorized viewer / member | process-intel-experiments |
| `CreateAgentExperiment` / `GetAgentExperiment` | POST/GET | `/api/v1/agent/experiments`, `/api/v1/agent/experiments/:experiment_id` | authorized member / viewer | process-intel-experiments |

"authorized (self only)" = `authenticated` tier in wrappa; handler restricts rows to the token's own user (there is no team-role concept for personal credentials).

**Team-less `/api/v1/agent/*` authorization [DECIDED HERE — corrects a false premise]:** the existing agent feedback routes are **not** main-team-authorized today. `auth.CheckAuthorizationHandler` reads the team from the `:team_name` URL param (`atc/api/auth/check_authorization_handler.go`); team-less paths like `/api/v1/agent/feedback` yield `IsAuthorized("")`, which reduces to `isAdmin` (`accessor.go`: `isAdmin || hasPermission(teamRoles[""])`, and no token carries roles for team `""`). Their `DefaultRoles` Viewer/Member entries in `accessor/roles.go` are therefore dead code — those routes are silently admin-only. Making the `authorized viewer/member` tiers in the table above actually work on team-less paths is **new work owned by agent-identity**: a `CheckAgentAuthorizationHandler` wrappa variant that hardcodes team `main` for team-less `/api/v1/agent/*` authorized routes (everything above except the `:team_name`-scoped pipeline-run routes), making the `DefaultRoles` entries effective. The five existing agent feedback routes (`SubmitAgentFeedback`, `GetAgentFeedback`, `GetAgentFeedbackSummary`, `ClassifyAgentVerdict`, `GetAgentReviewFindings`) move onto the same handler in the same change. Consumers: every workstream with an `authorized` team-less route in the table (credentials-and-budgets, workflow-store, ticket-core, agent-step, platform-mcp-hitl, delivery-outcomes, scorecards, process-intel-experiments).

---

## 5. Flight-recorder event schema — owner: **agent-step** (module `agent/schema`); consumers: gateway-mcp, harvest-step, scorecards, process-intel-experiments, ci-agent

Wire format unchanged: NDJSON lines `{"ts": RFC3339, "event": <type>, "data": {…}}` (`agent/schema.Event`, reader/writer already exist). Existing types are preserved verbatim:

`agent.start`, `agent.end`, `skill.start`, `skill.end`, `tool.call`, `tool.result`, `artifact.written`, `decision`, `error` (plus ci-agent's `plan.*` types, which remain valid ci-agent-namespaced extensions).

New event types (constants added to `agent/schema/event.go`; `data` payloads defined as Go structs in the same package so producers can't drift):

| Event type | Emitted by | `data` payload |
|---|---|---|
| `step.start` | agent-step exec / harvest exec | `{"step_name", "build_id", "plan_id", "ticket_id?", "workflow_name?", "workflow_version?", "workflow_hash?", "budget_slice_usd?", "resumed?": bool, "replayed?": bool}` — `resumed`/`replayed` added 2026-07-10 PARK-V2 (§3.2) |
| `step.end` | same | `{"step_name", "status": "ok\|failed\|error", "summary", "wall_time_seconds", "cost_usd", "turns", "session_id?"}` — `session_id` added 2026-07-10 PARK-V2 |
| `step.park` | agent-runner / agent-step exec at park-exit (2026-07-10 PARK-V2, §3.2) | `{"step_name", "question_id", "wait_seconds_at_exit", "session_id"}` |
| `step.resume` | agent-runner at continuation start (2026-07-10 PARK-V2, §3.2) | `{"step_name", "session_id", "question_id"}` |
| `gate.start` | harvest step | `{"gate": "build\|test\|lint", "component": "", "scope": "affected\|full"}` |
| `gate.result` | harvest step | `{"gate", "component", "scope", "status": "ok\|failed\|error", "duration_seconds", "summary", "log_artifact?"}` |
| `subagent.call` | gateway sidecar | `{"call_id", "tool": "request_review\|ask_agent", "provider", "model", "prompt_chars"}` |
| `subagent.result` | gateway sidecar | `{"call_id", "status", "provider", "model", "input_tokens", "output_tokens", "turns", "cost_usd", "duration_ms", "finding_count?"}` |
| `cost.record` | anything that spends money | mirrors `budget.LedgerEntry` (§2.7): `{"source", "provider", "model", "input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens", "turns", "cost_usd"}` |
| `budget.warn` | gateway / agent-step | `{"scope": "step\|ticket\|daily", "limit_usd", "spent_usd", "remaining_usd"}` (emitted at 80%) |
| `budget.stop` | gateway / agent-step | same payload; the call/step was cut off |
| `human.ask` | platform-mcp sidecar | `{"question_id", "kind": "question\|checkpoint", "question", "options"}` |
| `human.answer` | platform-mcp sidecar | `{"question_id", "answer", "answered_by", "wait_seconds", "timed_out"}` |
| `checkpoint.wait` | platform-mcp sidecar (`/checkpoint` handler, §3.2 — NOT the checkpoint task step) | `{"question_id", "checkpoint": "<name from workflow def>"}` |
| `checkpoint.release` | platform-mcp sidecar (`/checkpoint` handler, §3.2 — NOT the checkpoint task step) | `{"question_id", "approved": bool, "answered_by"}` — `approved` = (`answer == "approve"`); it drives the client's exit code via the sidecar's `/checkpoint` response (`true`⇒0, `false`⇒1); step failure is exit-code-driven, not an LLM verdict |
| `judge.score` | harvest step | `{"rubric_hash", "dimensions": [{"name", "score", "max", "rationale"}], "total", "max_total", "model", "cost_usd"}` |
| `push.done` | harvest step | `{"branch", "sha", "manifest_artifact"}` |

Rules: `data` keys are snake_case; producers may add keys, never repurpose them; consumers must ignore unknown keys and unknown event types (forward compat). Every event stream for a step MUST begin with `step.start` and end with `step.end` — ingestion (§1.8 `event_counts`) treats a stream missing `step.end` as `status: error`. *(2026-07-10 amendment, §11 — PARK-V2)* **One sanctioned exception:** a stream ending in `step.park` (no `step.end`) ingests as status `parked` (§1.8) — the park-exit is a defined end, not an error.

---

## 6. Workflow-definition YAML schema — owner: **workflow-store**; consumers: dispatch (renderer), agent-step, harvest-step, platform-mcp-hitl, scorecards

Evolved from `ci-agent/phaseconfig.Config` (name/env/mcp/steps/scoring); parsed by `agent/workflow.Parse` with `phaseconfig`-style eager validation. The raw bytes are the hashed provenance unit.

```yaml
# agent_workflow_definitions.definition — schema_version 1
schema_version: 1
name: standard-dev            # must match agent_workflow_definitions.name on import
description: spec -> plan -> implement -> review loop, single agent

spec_delivery: mcp           # mcp (default when omitted) | files
                             # mcp   = agents read spec/plan only via platform-mcp
                             #         read tools (read_ticket/list_tasks/get_task);
                             #         NO spec/plan bytes injected into any step.
                             # files = additionally materialize read-only spec.md /
                             #         plan.md, mounted as artifact "ticket" (§3.2, §11-dispatch).
                             # Normal hashed field (Go: workflow.Config.SpecDelivery
                             # string); import validation rejects any other value.

defaults:
  model: claude-sonnet-4-5    # any step may override
  max_turns: 80

budget:
  ticket_usd: 15.0            # default per-ticket budget (tickets.budget_usd overrides)
  judge_usd: 1.0              # harvest judge cap, funded by platform credential (§1.13)

sidecars:                     # named sidecar set; steps reference by name
  dev:
    image: ghcr.io/tdmtrader/mcp-dev-concourse   # ':<version>' pinned at import validation
    role: dev                 # dev | platform | gateway | custom
  platform:
    image: ghcr.io/tdmtrader/mcp-platform
    role: platform
  gateway:
    image: ghcr.io/tdmtrader/mcp-gateway
    role: gateway
    providers: [claude]       # which adapters this workflow may use

prompts:                      # prompt templates, inline — hashed with the definition.
  spec: |                     # Go text/template; render context: .Ticket .Spec .Tasks .Params (nil-safe, §6.2)
    Begin by calling platform-mcp read_ticket and list_tasks (get_task per
    task as you work). Explore the repo, then submit a spec with submit_spec. ...
  implement: |
    Implement the active plan task by task. Use dev-mcp run_tests with
    affected components after each task. ...

steps:                        # ordered; grammar below
- agent: write-spec
  prompt: spec                # key into prompts
  sidecars: [dev, platform]
  budget_slice_usd: 2.0
  outputs: [workspace]

- checkpoint: plan-approval   # human gate; renders to a checkpoint step (§3.2)
  on_reject: fail             # fail | send_back

- agent: implement
  prompt: implement
  sidecars: [dev, platform, gateway]
  budget_slice_usd: 10.0
  max_turns: 120              # overrides defaults
  inputs: [workspace]
  outputs: [workspace]

hitl:
  ask_timeout: park           # park | default | fail  (ask_human timeout policy);
                              # 'default' consumes the ask_human call's own 'default' field (§3.2)
  ask_timeout_seconds: 0      # 0 = indefinite

gate_policy:                  # consumed verbatim by the harvest step (§2.8 GatePolicy)
  gates:
  - gate: build
    scope: affected           # affected | full | affected_then_full
  - gate: test
    scope: affected_then_full # affected first (fast signal), then full suite
    timeout: 45m
  - gate: lint
    scope: affected
  on_gate_failure: needs_review   # only value in v1; named for future policies

judge:                        # rubric for the schema-constrained judge (§6.4)
  rubric:
  - name: correctness
    weight: 3
    guidance: "Does the change do what the spec's acceptance criteria require?"
  - name: tests
    weight: 2
    guidance: "Are new behaviors covered by meaningful tests?"
  - name: scope-discipline
    weight: 1
    guidance: "Small tractable diff; no drive-by refactors."
  pass_threshold: 6.5         # 0-10 weighted total
```

### 6.1 Step composition grammar [DECIDED HERE]

A `steps` entry is exactly one of:
- `agent: <name>` — fields: `prompt` (required, key into `prompts`), `sidecars` (list of names from `sidecars`), `budget_slice_usd`, `model`, `max_turns`, `inputs`/`outputs` (artifact names; `workspace` is the conventional threaded artifact), `output_schema` (key into an optional top-level `schemas` map).
- `checkpoint: <name>` — fields: `on_reject: fail | send_back`.

Linear sequence only — **no branching, no loops, no parallel fan-out in v1**. Iteration loops (test-fix cycles) live *inside* an agent step's prompt, not in the grammar; the harvest step is appended implicitly by the renderer as the terminal step and is not declared in `steps`. Rationale: every added grammar construct multiplies the renderer's golden-file surface and the scorecard's comparison ambiguity; the five existing ci-agent phases decompose cleanly into a linear sequence today.

### 6.2 Prompt packaging [DECIDED HERE]

Prompts are **inline in the definition YAML** (not repo files, not separate rows): the content hash then covers prompts, so "same workflow, different prompt" is a different version — which is precisely the unit of comparison scorecards need. Go `text/template` with the render context frozen as `.Ticket`, `.Spec`, `.Tasks`, `.Params` (renderer resolves; step receives final text per §2.8's resolution rule).

**Nil-safe render semantics [DECIDED HERE — 2026-07-09 amendment, §11; FLOWS.md E2]:** `.Spec` MAY be nil and `.Tasks` MAY be empty at render time — this is the **NORMAL** state, not an edge case: rendering happens at dispatch, before any agent step runs, so every dispatch (including the seeded spec-first workflow, whose spec is created mid-run by its first agent step) renders against a spec-less ticket. Template execution MUST NOT fail on absence. The mechanism is exactly one — **nil-safe method accessors on a pointer view type**:

```go
// agent/workflow/rendercontext.go — constructed by dispatch's renderer (plan 11)
// from tickets.Store.Get / LatestSpec / ActivePlan (§2.1 absence semantics).
type RenderContext struct {
	Ticket TicketView        // envelope; always present
	Spec   *SpecView         // nil when LatestSpec returned ok=false (no spec rows)
	Tasks  []TaskView        // empty when ActivePlan returned [] (no plan)
	Params map[string]string
}

// SpecView exposes NO exported fields; every template accessor is a nil-safe
// pointer-receiver method. Go text/template happily invokes methods on a nil
// pointer receiver, so bare leaf access can never hit the
// "nil pointer evaluating" execution error — there is no field path to it.
type SpecView struct{ spec *tickets.Spec }

func (s *SpecView) Title() string {
	if s == nil || s.spec == nil {
		return ""
	}
	return s.spec.Title
}

func (s *SpecView) BodyMD() string {
	if s == nil || s.spec == nil {
		return ""
	}
	return s.spec.Body
}

func (s *SpecView) AcceptanceCriteria() []string {
	if s == nil || s.spec == nil {
		return nil
	}
	return s.spec.AcceptanceCriteria
}
```

What this buys, normatively:

- `{{if .Spec}}` is the guard for spec-conditional prompt content — a nil `*SpecView` is falsy in text/template, so templates gate optional sections with it (and SHOULD, for any prose that only makes sense when a spec exists).
- Bare `{{.Spec.Title}}` / `{{.Spec.BodyMD}}` on a spec-less ticket renders the **empty string**, not a template execution error — the accessor is a method call on a nil receiver, which Go text/template executes normally. A template omitting the guard degrades to blanks; it never fails the dispatch.
- `{{if .Tasks}}` is falsy on the empty slice and `{{range .Tasks}}` iterates zero times; `TaskView` is a value type (exported fields are fine — slice elements are never nil).

**Import gate:** workflow-store's `Validate()` (plan 05) executes every `prompts` template against a validation-only mirror of the render context in its dispatch-time ground state — `.Spec` nil, `.Tasks` empty, and *(2026-07-09 verifier follow-up, aligning with plan 05's `nilRenderContext`)* tolerant `map[string]any` values for `.Ticket` and `.Params`, so a reference to an unknown envelope or param field renders `<no value>` instead of false-rejecting the definition (the envelope is always present at render; only a nil-deref is the import-blocking bug) — and rejects the definition on any execution error. Note the mirror's `.Spec` is a plain nil pointer *without* the nil-safe accessors above, so a bare `{{.Spec.Title}}` fails the import gate even though the dispatch renderer would degrade it to blanks: the gate deliberately forces the `{{if .Spec}}` guard. A template that cannot render a spec-less ticket is caught at import, never at dispatch. Renderer-side golden tests with `Spec=nil`/`Tasks=nil` in both `spec_delivery` modes land in plan 11 (FLOWS.md E4).

### 6.3 Gate-policy language (frozen for harvest-step and dispatch)

`GatePolicy` (Go: `agent/harvest/policy.go`, owned by harvest-step, YAML grammar owned here):

```go
type GatePolicy struct {
	Gates         []Gate `json:"gates"`
	OnGateFailure string `json:"on_gate_failure"` // "needs_review"
}
type Gate struct {
	Gate    string `json:"gate"`    // build | test | lint
	Scope   string `json:"scope"`   // affected | full | affected_then_full
	Focus   string `json:"focus,omitempty"`
	Timeout string `json:"timeout,omitempty"` // per-gate; default 30m
}
```

Semantics: gates run in order via the dev-mcp Go client; `affected` resolves components with `affected_components(git diff --name-only <target>...HEAD)`; empty affected-set falls back to `full` for that gate; first `failed`/`error` gate stops the sequence (`error` marks the ticket `errored`, not `failed`).

### 6.4 Judge rubric → six-verdict mapping — owner: **harvest-step**; sign-off consumers: delivery-outcomes, scorecards, process-intel-experiments

The judge scores the rubric dimensions (schema-constrained JSON output, `judge.score` event §5). The rubric is **not** findings-shaped; the mapping to the six-verdict feedback taxonomy (`accurate, false_positive, noisy, overly_strict, partially_correct, missed_context` — `ci-agent/schema/feedback.go`, moving to `agent/schema`) applies to the judge's optional per-dimension **cited issues**: each cited issue is written as a review finding into `agent_reviews.review` (existing findings shape) with `finding_type: "judge"`, making it feedback-eligible in the existing `agent_feedback` UI. Human verdicts on judge findings then calibrate the judge exactly like reviewer findings. Ticket-level dispositions stay in `agent_outcomes` (§1.11) — verdicts are per-finding, dispositions are per-ticket; the two are never conflated.

---

## 7. Pipeline-run params schema and template flag — owner: **pipeline-runs**; consumers: dispatch, process-intel-experiments

Template flag and params schema are **top-level pipeline config keys**, validated by `atc configvalidate`, and mirrored onto `pipelines.template` / `pipelines.params_schema` at `SaveConfig` time:

```yaml
template: true                # this pipeline never schedules; checks disabled
params:
- name: commit
  type: string                # string | number | bool | enum
  required: true
  description: git sha to test
- name: suite
  type: enum
  values: [unit, integration, behavioral]
  default: unit
- name: procs
  type: number
  default: 2
```

Go (`atc/config.go` additions):

```go
type Config struct {
	// ...existing fields...
	Template bool          `json:"template,omitempty"`
	Params   []ParamSchema `json:"params,omitempty"`
}

type ParamSchema struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`             // string | number | bool | enum
	Required    bool     `json:"required,omitempty"`
	Default     any      `json:"default,omitempty"`
	Values      []string `json:"values,omitempty"` // enum only
	Description string   `json:"description,omitempty"`
}
```

Validation rules (enforced by `CreatePipelineRun` and `fly run-pipeline`): unknown param names rejected; missing required params rejected; type coercion per JSON type; defaults filled server-side; the validated map is stored as `pipeline_runs.params` and injected into the instanced pipeline as vars alongside `instance_vars: {"run": <number>}`. `template: true` pipelines: scheduler skips them, resource checks are disabled, direct job triggering is rejected with a pointer to `run-pipeline`. Runs of a template render as instance groups in the existing UI (instance-vars machinery unchanged).

**Reserved vars [DECIDED HERE — 2026-07-09, F30; co-signed pipeline-runs + dispatch + harvest-step]:** two var names are reserved and rejected by the params validator as user-declared param names:

- `((run))` — the **per-template run NUMBER** (`pipelines.last_run_number` increment; resets per template). Display/instance-group identity only. It MUST NOT be used as a cross-table key.
- `((run_id))` — **`pipeline_runs.id`**, the global join key (§1.5). The id is allocated **before materialization** — `nextval` on the sequence inside the `CreateRun` transaction, row inserted with the explicit id — so `((run_id))` is resolvable when the instanced pipeline config is interpolated. Interpolation covers the **whole pipeline config**: params, sidecar env values, and secretKeyRef secret names (e.g. `agent-run-((run_id))`, §8.2) at run materialization.

Everything keyed across tables — `agent_run_metrics.pipeline_run_id`, `agent_run_questions.pipeline_run_id`, `agent_reviews.pipeline_run_id`, gateway ledger rows, the `agent-run-<id>` secret name, and the `AGENT_PIPELINE_RUN_ID` env var (§8.1) — uses `((run_id))`, never `((run))` (run numbers collide across templates while tickets/secrets/principals use the id). Renderer emissions that carry a `PipelineRunID` field may leave it 0 at render time; step execs fall back to `envInt("AGENT_PIPELINE_RUN_ID")` when the plan field is 0 (harvest exec contract, plan 09).

Fly: `fly -t t run-pipeline -p <template> -v key=val [-v ...]` (struct field + command file per the fly recipe); `fly runs -p <template>` lists runs.

### 7.1 Pipeline-runs implementation addendum (wave 1, owner: pipeline-runs)

Decisions made while implementing §1.5/§2.3/§7; consumers (dispatch, platform-mcp-hitl,
process-intel-experiments) should code against these:

1. **Template column on instances.** Run instances keep `template: true` in their
   materialized config, so `pipelines.template` is true for base templates AND their run
   instances. Base template = `template AND instance_vars IS NULL`. Scheduler skips base
   templates only; lidar's periodic check scan skips all `template = true` rows.
2. **"Versions pinned at creation" v1.** Implemented as a frozen check set: one
   manually-triggered check per resource enqueued at run creation **by
   `db.PipelineRunFactory.CreateRun` itself** (`CheckFactory.TryCreateCheck`, same seam
   as the `CheckResource` handler; the `POST .../runs` handler is a pass-through, so
   in-process consumers — dispatch, experiments — get the frozen check set too;
   amended 2026-07-09, F27), periodic checks
   disabled, `trigger: true` stripped at materialization from get steps WITHOUT
   `passed:` constraints. Gets WITH `passed:` keep their trigger flag so downstream
   jobs flow through chains as normal (spec §3) — external resource versions can never
   trigger a run-instance build, but passed-chain propagation can. Explicit `version:`
   pins pass through. Known limitation: a shared resource-config scope fed by other
   pipelines can surface newer versions to not-yet-scheduled jobs that lack `passed:`
   constraints.
3. **Completion + reopen.** Completion additionally requires no active unpaused job with
   `schedule_requested > last_scheduled` (closes the downstream-not-yet-created race).
   Completion is re-entrant: a completed run whose instance pipeline gains a
   pending/started job build — or a job build that COMPLETED after the run's
   `completed_at` (fast-finishing retriggers that never linger in pending/started;
   self-terminating because reopen→re-complete stamps a newer `completed_at`;
   amended 2026-07-09, F26) — is reopened (status `running`, `completed_at` cleared) and
   completes again. §2.3 gains:
   `PipelineRun.Reopen() error`, `PipelineRun.CheckComplete() (PipelineRunStatus, bool, error)`,
   `PipelineRun.InstancePipeline() (Pipeline, bool, error)`, plus getters
   `CreatedAt() time.Time`, `CompletedAt() (time.Time, bool)`, `Archived() bool`;
   `PipelineRunFactory` gains `CompletedRunsWithNewActivity() ([]PipelineRun, error)` and
   `RunsToArchive() ([]PipelineRun, error)`.
4. **Retention YAML carrier.** Top-level pipeline-config key
   `run_retention: {keep_last: K, ttl_days: N}` (Go: `atc.RunRetentionConfig`), mirrored to
   `pipelines.run_retention` at SaveConfig time. `keep_last` ranks completed, non-archived
   runs per template by number descending.
5. **Reserved param names.** A params-schema entry named `run` or `run_id` is a config
   validation error (`run_id` added 2026-07-09, F30).
6. **Template job triggering.** `CreateJobBuild` on a base template returns
   409 Conflict, body: `cannot trigger jobs on a template pipeline; use "fly run-pipeline" to create a run`.
7. **Wire shapes.** `POST .../runs` body: `{"params": {"name": <value>, ...}}`
   (`atc.CreatePipelineRunRequest`). Response/list element (`atc.PipelineRun`):
   `{"id": int, "number": int, "status": string, "params": object, "created_by": string,
   "created_at": epoch-seconds, "completed_at": epoch-seconds-omitempty, "archived": bool-omitempty}`.
8. **Entry-job trigger semantics.** Entry jobs (no `passed:` on any input) are triggered as
   manually-triggered builds by `CreateRun` inside the creation call, after the creation
   transaction commits.
9. **Run-id var (`((run_id))`).** `pipeline_runs.id` is allocated via `nextval` inside the
   creation transaction BEFORE materialization and injected as a second reserved static
   var alongside `((run))` (added 2026-07-09, F30). `((run))` = per-template run NUMBER
   (also the `instance_vars` identity; numbers reset per template). `((run_id))` =
   globally-unique `pipeline_runs.id`; it resolves at materialization only and is NOT
   part of `instance_vars`. Anything keying cross-template state (agent metrics,
   questions, reviews, gateway ledger rows — §8.1 `AGENT_PIPELINE_RUN_ID`) MUST
   interpolate `((run_id))`, never `((run))`. Co-signed with dispatch (renderer sites)
   and harvest.

---

## 8. Env-var / K8s-secret credential-injection contract — owners: **credentials-and-budgets** (secrets + helper), **agent-step** (step env), **dev-mcp** (ports/packaging); consumers: dispatch, gateway-mcp, harvest-step, platform-mcp-hitl

### 8.1 Env vars in the agent step's main container and sidecars

Set by the **owning exec implementation** (agent-step, harvest-step) or by **dispatch's renderer** (checkpoint task steps); values resolved at render/dispatch time; literal in the pod spec except secret refs. *(Mechanism, 2026-07-09, F15 — co-signed agent-step + dev-mcp + harvest-step + platform-mcp-hitl + dispatch:)* main-container rows travel via `runtime.ContainerSpec.Env`/`SecretEnv`; sidecar rows for **exec-owned steps** (agent, harvest) travel via the per-sidecar maps `runtime.ContainerSpec.SidecarEnv` (`map[sidecar-name][]"NAME=VALUE"`) and `runtime.ContainerSpec.SidecarSecretEnv` (`map[sidecar-name]map[env-name]vars.SecretRef`), populated programmatically by the owning exec — never from public pipeline YAML — and applied by jetbridge `buildSidecarContainers`; secret rows are always emitted as `ValueFrom.SecretKeyRef` (§8.2's secretKeyRef-only rule). **Checkpoint task steps** — whose sidecar env must survive serialization through the rendered pipeline config — carry it as `atc.SidecarConfig` env populated by dispatch's renderer: literal entries plus `SidecarEnvVar.ValueFrom`/`SecretKeyRef` entries, admitted at pod build time only when the secret name matches a `--kubernetes-sidecar-secret-prefixes` prefix (env `CONCOURSE_KUBERNETES_SIDECAR_SECRET_PREFIXES`; default empty = every sidecar secretKeyRef rejected; agentic deployments set `agent-run-`). Both paths converge in `buildSidecarContainers`. Sidecar secret refs are derived from the deterministic §8.2 secret name `agent-run-<pipeline_run_id>`.

**Checkpoint task step env row set (2026-07-09, F14/F16):** the rendered checkpoint step's MAIN container carries exactly `ATC_EXTERNAL_URL`, `AGENT_TICKET_ID`, `AGENT_PIPELINE_RUN_ID` (`((run_id))`, §7), `AGENT_STEP_NAME` (`checkpoint-<name>`), and `PLATFORM_MCP_URL` — **no `AGENT_PRINCIPAL_TOKEN` in the main container** (the client is unauthenticated, §3.2; only `PLATFORM_MCP_URL` is required — the rest are provenance/logging). Its single `platform` sidecar carries the four literal identity rows plus `AGENT_PRINCIPAL_TOKEN` via secretKeyRef (`agent-run-((run_id))`/`principal-token`); no `PLATFORM_MCP_ASK_TIMEOUT_*` (checkpoints always park) and no `BUILD_ID` (not knowable at render; checkpoint question rows carry `build_id=0` in v1).

| Env var | Container(s) | Source | Meaning |
|---|---|---|---|
| `CLAUDE_CODE_OAUTH_TOKEN` | main, gateway | secret `agent-run-<run-id>`, key `anthropic-token`; **pure-CI fallback:** secret named by web flag `--agent-platform-token-secret`, key `anthropic-token` | triggering user's vaulted token (spec-verified headless var). A pure-CI agent step (no verified `pipeline_run_id`, so no `agent-run-<id>` secret) has no per-run secret; when the operator sets `--agent-platform-token-secret` the exec wires this token from that platform-level secret as a secretKeyRef (same §8.2 secretKeyRef-only contract; the per-run secret always takes precedence). Unset ⇒ pure-CI agent steps have no token path and fail at claude auth (review decision, 2026-07-16) |
| `AGENT_PRINCIPAL_TOKEN` | platform, gateway, harvest | secret `agent-run-<run-id>`, key `principal-token` | per-run scoped principal token (§1.2), minted by dispatch with run-appropriate scopes; `expires_at` = now + `--agent-run-timeout` (6h default) — EXCEPT when the rendered workflow contains a park-policy `ask_human` or any checkpoint, in which case dispatch mints `expires_at` = now + `--agent-park-timeout` (web flag, `time.Duration`, default `72h`, defined in `atc/atccmd/command.go` beside `--agent-run-timeout`; expiry is NOT NULL — the backstop stays). Normative definition lives here; the implementing edit is dispatch's mint step (plan 11) and binds via this row |
| `ATC_EXTERNAL_URL` | all | literal | ATC base URL (name matches existing ci-agent publish contract) |
| `BUILD_ID` | all | literal (jetbridge already injects) | concourse build id |
| `AGENT_TICKET_ID` | all | literal | ticket id; empty for pure-CI agent steps |
| `AGENT_PIPELINE_RUN_ID` | all | literal | `pipeline_runs.id`, carried by the reserved var `((run_id))` (§7) — NEVER the per-template run number `((run))`; step execs fall back to this env when a rendered plan's `PipelineRunID` field is 0 |
| `AGENT_STEP_NAME` | main | literal | step name from the plan |
| `AGENT_WORKFLOW_NAME` / `AGENT_WORKFLOW_VERSION` / `AGENT_WORKFLOW_HASH` | main | literal | provenance tags for metrics/events |
| `AGENT_BUDGET_SLICE_USD` | main, gateway | literal | step's slice (§2.7); gateway enforces for sub-agent calls only; the main agent's own spend is admission-gated (StepSlice) + post-hoc reconciled at ingestion, and turn/timeout-capped within a step — not cut off mid-call |
| `DEV_MCP_URL` | main | literal `http://127.0.0.1:7780/mcp` | |
| `PLATFORM_MCP_URL` | main | literal `http://127.0.0.1:7781/mcp` | |
| `GATEWAY_MCP_URL` | main | literal `http://127.0.0.1:7782/mcp` | |
| `PLATFORM_MCP_ASK_TIMEOUT_POLICY` / `_SECONDS` | platform | literal from workflow `hitl` block | §3.2 |
| `PLATFORM_MCP_PROGRESS_INTERVAL` | platform | literal (optional) | SSE heartbeat override (§3.1), Go duration, must be ≤ 30s (else fatal startup error) |
| `GATEWAY_MCP_PROGRESS_INTERVAL` | gateway | literal (optional) | same as above, for the gateway |
| `AGENT_SESSION_ID` | main | literal — **continuation builds only** (2026-07-10 PARK-V2, §3.2) | claude session id to `--resume`, from `agent_run_step_state.session_id` (§1.14) |
| `AGENT_SESSION_FILE` | main | literal — **continuation builds only** (2026-07-10 PARK-V2, §3.2) | path to the restored `<flight>/session.jsonl`; the runner installs it at `~/.claude/projects/<cwd-slug>/<session-id>.jsonl` before invoking `--resume` |
| `AGENT_PARK_EXIT_GRACE_SECONDS` | main | literal, default `30` (2026-07-10 PARK-V2, §3.2) | SIGTERM→SIGKILL grace the runner grants claude at park-exit |
| `AGENT_STREAM_LOG_MAX_LINE_BYTES` | main | literal, default `16384` (2026-07-10 PARK-V2) | tee-side truncation bound for stream-json NDJSON lines (`…[truncated N bytes]` suffix; large tool_results are the offender) — truncation applies to the stdout tee ONLY, the runner's parser always sees the full line |
| `PLATFORM_MCP_SHORT_PARK_MAX_SECONDS` | platform | literal from `--agent-short-park-max` (2026-07-10 PARK-V2, §3.2) | short-park threshold in seconds; `0` = never exit (pure PARK-V1); the sidecar owns the timer for both `ask_human` and `/checkpoint` parks |
| `PLATFORM_MCP_PARK_PATH` | platform | literal, set by the **agent-step exec** via `ContainerSpec.SidecarEnv["platform"]` (F15; plan 07 Task 26), agent steps only (2026-07-10 PARK-V2 follow-up, §3.2) | absolute path of the §B1 park sentinel (`<flight mount>/park.json`); only the exec knows the flight mount path at container-spec time — NOT renderer-emitted; unset = the sidecar never writes a sentinel (the legal checkpoint-pod shape, where the `202` response is the exit signal) |

**[DECIDED HERE: fixed localhost ports 7780 (dev), 7781 (platform), 7782 (gateway).]** Pods are single-tenant so fixed ports are safe; every sidecar accepts `MCP_LISTEN_ADDR` to override; agents discover via the `*_MCP_URL` vars, never hardcode.

**Agent-step-owned additions (added by agent-step wave-2 plan; consumers: dispatch renderer, gateway):**

| Env var | Container(s) | Source | Meaning |
|---|---|---|---|
| `AGENT_PROMPT` | main | literal (from `AgentStep.Prompt`) | inline prompt text; mutually exclusive with `AGENT_PROMPT_FILE` |
| `AGENT_PROMPT_FILE` | main | literal (from `AgentStep.PromptFile`) | artifact-relative path (`input-name/path.md`) resolved inside the workdir |
| `AGENT_MODEL` / `AGENT_MAX_TURNS` | main | literal | claude CLI `--model` / `--max-turns` |
| `AGENT_OUTPUT_SCHEMA` | main | literal | artifact-relative JSON-schema path (optional) |
| `AGENT_FLIGHT_DIR` | main | literal `<workdir>/flight` | where agent-runner writes results.json + events.ndjson; backed by the implicit `flight` output volume |
| `AGENT_OUTPUT_<NAME>` | main | literal `<workdir>/<name>` per declared output (name uppercased, `-`→`_`) *(2026-07-16)* | absolute in-pod path of each user-declared output, so prompts target outputs deterministically — the first live dual-run's agent cd'd into an input and wrote its deliverable there, shipping an empty output |
| `AGENT_PLAN_ID` | main | literal (exec-set, from the step's `atc.PlanID` — never renderer/public YAML) *(2026-07-12)* | the step's plan id; with jetbridge-injected `BUILD_ID` it lets the runner populate `step.start`'s non-optional `build_id`/`plan_id` (§5) — the correlation key joining the event stream to its `agent_run_metrics` row |
| `AGENT_SYSTEM_PROMPT` | main | literal (from `AgentStep.SystemPrompt`) *(2026-07-18 source-format slice b)* | resolved system-prompt layer; runner passes via `--append-system-prompt` (workflow layer appended to the claude baseline; a step-level value replaced the workflow layer at render) |
| `AGENT_CONTEXT` | main | literal (from `AgentStep.Context`) *(2026-07-18)* | pre-concatenated session-start context block; runner prepends it to the prompt under a `# Workflow context` header |
| `AGENT_SKILLS` | main | literal, comma-joined (from `AgentStep.Skills`) *(2026-07-18)* | skill names the runner materializes from the `skills` input into `<workdir>/.claude/skills/` (claude project skills; the workspace git tree stays clean for harvest) |
| `AGENT_SKILLS_DIR` | main | literal `<workdir>/skills` (only when `AGENT_SKILLS` set) *(2026-07-18)* | mount path of the renderer-emitted `skills` input artifact (write-skills task output; `skills` joins `repo`/`ticket` as a renderer-reserved artifact name) |

- **Implicit `flight` output:** every agent step gets an output volume named `flight` (reserved; a user-declared output named `flight` is a validation error). The exec ingests `flight/results.json` and `flight/events.ndjson` synchronously before the step returns — this is the ingestion-before-artifact-GC guarantee.
- **Ticket/run identity via env:** the exec copies `AgentStep.Env` verbatim into the pod and *reads back* `AGENT_TICKET_ID`, `AGENT_PIPELINE_RUN_ID`, `AGENT_WORKFLOW_NAME`, `AGENT_WORKFLOW_VERSION`, `AGENT_WORKFLOW_HASH` from it for metrics tagging and budget-slice lookup. The renderer (dispatch) sets these keys; hand-written pipelines may set them too. Absent keys = pure-CI agent step (NULL tags).
- **MCP URL derivation:** for each sidecar whose `name` is `dev`, `platform`, or `gateway`, the exec sets `DEV_MCP_URL` / `PLATFORM_MCP_URL` / `GATEWAY_MCP_URL` to `http://127.0.0.1:7780|7781|7782/mcp` per the fixed-port decision. Other sidecar names get no URL env.
- **Main container image:** the `agent:` step config has no image field (per §2.8). The image comes from the web-node flag `--agent-step-image` (`CONCOURSE_AGENT_STEP_IMAGE`); the image must contain the claude CLI and the `agent-runner` entrypoint (image `ghcr.io/tdmtrader/agent-runner`, built per the §8.5 convention with a version tag; also pushed to `registry.home/agent-runner` for theborg). An unset flag makes any `agent:` step error at run time with a clear message.
- **Main process:** the exec runs `agent-runner` (argv0 only, no args) as process ID `agent` so `attachOrRun` reattaches across web restarts, exactly like the task step's `task` process ID.

### 8.2 Ephemeral run secret

- **Name:** `agent-run-<pipeline_run_id>` in the jetbridge worker namespace.
- **Keys:** `anthropic-token` (decrypted user credential), `principal-token` (per-run principal).
- **Labels:** `concourse/agent-run: "<run-id>"`, `concourse/ticket: "<ticket-id>"` — the `RunSecretReaper`'s sweep finds every secret, including strays, by label.
- **Lifecycle:** created by `credentials.SecretAttacher.Attach` (§2.6) during dispatch, AFTER the queued→running claim is won (which records `pipeline_run_id` per §2.1) and BEFORE the build tracker schedules the entry-job pods — there is no runtime race; the create→claim→attach order is deliberate, so the secret name (`agent-run-<pipeline_run_id>`) is known before any pod that references it is scheduled. **Deletion ownership [DECIDED 2026-07-09, F22]:** deleted by the **`RunSecretReaper`**, a polling component owned by **credentials-and-budgets** (plan 02), wired beside the `PlatformSecretSyncer` inside the K8s block (it already holds the clientset + namespace). Each pass it lists secrets by label `concourse/agent-run` and deletes (NotFound-tolerant) every one whose run is complete or absent, checked through a narrow `RunActive(runID)` seam against pipeline-runs; in the same pass it best-effort revokes the matching per-run principal. Polling, never notify-only (fork lesson). This covers BOTH the normal completion path and the crash window between `Attach` and pod scheduling. Plan 03's pipeline-run lifecycler stays deliberately pure — it gets **no** clientset/attacher; it is NOT the deleter. *(2026-07-10 amendment, §11 — PARK-V2)* `RunActive(runID)` counts `awaiting_human` (§1.5) **AS ACTIVE** — the `agent-run-<run-id>` secret and per-run principal row must survive the wait for the continuation to re-attach; and `SecretAttacher.Attach` is amended (additive, §2.6) to **CREATE-OR-UPDATE** the same `agent-run-<pipeline_run_id>` secret: on resume, dispatch's `reconcileAwaitingRuns` pass (§3.2) re-mints the per-run principal (revoke-and-recreate, `expires_at = now + RunPrincipalTimeout(cfg, workflow)` — the existing park-aware helper) and calls `Attach` again with the fresh `principal-token` and a credential re-resolved through `credentials.Backend` (so a rotated user OAuth token is honored); continuation pods are new pods, so updated keys are picked up at container start.
- **Consumption:** env `secretKeyRef` only — tokens never land in files, argv, or the DB in plaintext. *(2026-07-09, F20)* Keys may be `SecretEnv`-only (no matching literal env entry — e.g. the harvest main container's judge `CLAUDE_CODE_OAUTH_TOKEN`): jetbridge `applySecretRefs` **appends** a secretKeyRef-only EnvVar (sorted name order, deterministic pod specs) when no literal exists; the empty-placeholder workaround (a fake literal added just to be replaced) is forbidden.

**Long-lived platform credential secret [DECIDED HERE]** — owner: **credentials-and-budgets**; consumers: harvest-step, process-intel-experiments:

- **Name:** `agent-platform-credential` in the jetbridge worker namespace.
- **Keys:** `anthropic-token` — the decrypted credential of the `agent-platform` service user's `agent_user_credentials` row (§1.13).
- **Lifecycle:** created and kept in sync **bidirectionally** by the credentials-and-budgets workstream: re-written on `Store.Put` for the platform user's row and on encryption-key rotation; **and deleted when the platform credential is deleted from the vault** (`Store.Delete` for the platform user's row, e.g. via `fly agent auth --platform --delete`) — vault deletion removes this long-lived K8s secret. This propagates admin intent to the data plane (platform-funded pods can no longer authenticate) but does NOT revoke the upstream Anthropic token — the vault is only Concourse's store of it, so revocation at Anthropic is a separate, out-of-band admin action. Long-lived (not per-run), so harvest pods need no dispatch-time secret plumbing for it.
- **Consumption:** env `secretKeyRef` only (same rule as above), mounted exclusively into pods running platform-funded work — the harvest pod's judge (`CLAUDE_CODE_OAUTH_TOKEN`, §8.3) and retrospective/calibration jobs. Never mounted into agent-step pods, which carry only the per-run user credential.

### 8.3 Harvest-only git credentials

- **Name:** `agent-harvest-git-<repo-slug-sanitized>` (e.g. `agent-harvest-git-tdmtrader-concourse`), created **manually by an admin** per repo (v1; no API writes it).
- **Keys:** `username`, `token` (PAT with push-to-`agent/*`-branches permission only, where the host supports it).
- **Mount:** volume-mounted read-only at `/var/run/agent/git/` in the **harvest pod only** — the harvest step's pod spec is built by its own exec implementation and never includes agent sidecars or the agent's anthropic token alongside these credentials (judge calls use `CLAUDE_CODE_OAUTH_TOKEN` sourced via env `secretKeyRef` from the long-lived **platform** credential secret `agent-platform-credential`, §8.2; the agent's per-run user token is absent).
- **Git identity:** `concourse-agent[bot] <agent@concourse.local>` — the author string the outcome watcher's human-touch delta excludes (§1.11).

### 8.4 Notification channel — owner: **platform-mcp-hitl**

**[DECIDED HERE: v1 notification = generic webhook.]** Web flag `--agent-notify-webhook-url` (empty = UI-only). On `human.ask`, ticket state changes, and budget stops, the ATC POSTs `{"kind": "question|checkpoint|state|budget", "ticket_id": N, "title": "...", "url": "<ticket page>", "body": "..."}`. A webhook fans out to ntfy/Slack/etc. without the platform growing per-channel code; the ticket page remains the source of truth.

### 8.5 Sidecar image packaging convention — owner: **dev-mcp**; consumers: platform-mcp-hitl, gateway-mcp

- Images: `ghcr.io/tdmtrader/mcp-<name>` (`mcp-dev-concourse`, `mcp-platform`, `mcp-gateway`), version tag = the shipping repo's release tag; `latest` never referenced by workflow definitions (import validation rejects untagged/`latest` images).
- Each image: static Go binary (or provider CLIs for the gateway), `ENTRYPOINT` serves streamable HTTP MCP on `MCP_LISTEN_ADDR` (default `:778x` per role), exposes `GET /healthz` for the pod readiness probe, runs as non-root (uid 1000) — **MCP sidecar images only** *(scoped 2026-07-09, F25)*; main-step runner images (`agent-runner`, `harvest-runner`) run as **root** like every other step image, because jetbridge hostPath step volumes are kubelet-created `root:root 0755` and `fsGroup` is ignored for hostPath (a non-root user gets EACCES writing the flight recorder). Runner images additionally set `ENV IS_SANDBOX=1` so the claude CLI accepts `--dangerously-skip-permissions` as root (single-tenant, pod-isolated sandbox). The same scoping is mirrored in `deploy/MCP_IMAGES.md`'s convention table (User row).
- **Task-main-image constraint [2026-07-09, F28]:** any image used as a jetbridge task **MAIN** image needs POSIX `sh` plus `tail`, `mv`, `cat`, `sleep`, `mkdir`, `kill` (the pause command is `sh -c` and task steps run under the sh-based supervisor); **distroless bases are sidecar-only**. `mcp-platform` is shell-bearing (alpine base + nonroot user, with a build-time `command -v` smoke check for the six binaries) precisely because it doubles as the checkpoint task's MAIN image (§3.2) — sidecar use runs the ENTRYPOINT (MCP server mode); checkpoint task use runs `platform-mcp` from PATH (`/usr/local/bin/platform-mcp`) under the sh supervisor. `mcp-dev-concourse` and `mcp-gateway` remain sidecar-only and MAY stay distroless.
- **Workspace discovery [DECIDED HERE — 2026-07-09, F21; CWD convention, co-signed dev-mcp + agent-step + harvest-step]:** sidecar images never hardcode a workspace path (no `/workspace`). The `ENTRYPOINT` is the bare binary; every path-valued flag defaults relative to the process CWD (dev-mcp: `--config dev-mcp.yml`, `--workdir .`). The owning exec implementation sets each MCP sidecar's `WorkingDir` — when the sidecar config leaves it unset — to the workspace artifact's mount path inside the hashed build workdir (`<workdir>/<workspace-artifact-name>`); jetbridge's existing fallback (the main container's Dir) applies when no workspace artifact exists. Sidecars therefore take the workspace from CWD, and pod construction — not the image — decides where that is.
- CI job pattern: a job per image in the existing `cicd` pipeline on theborg — build with the repo's standard image-build task, run the contract-test kit against the built image (`docker run` + `contracttest.Run`, adding `-w /workspace` to emulate the exec-set WorkingDir), push on green. The dev-mcp workstream lands the first such job as the copyable template.

---

## 9. Contract-surface → section index

| Contract surface (from the workstream map) | Section(s) |
|---|---|
| agent-principal-auth | §1.2, §4.1 |
| user-credential-vault-and-secret-helper | §1.3, §2.6, §8.2 |
| budget-library-and-cost-ledger | §1.4, §2.7 |
| platform-credential-policy | §1.13, §8.2 (`agent-platform-credential`), §8.3 |
| pipeline-runs-api-and-lifecycle | §1.5, §2.3, §4.2, §7 |
| dev-mcp-contract-and-client | §3.1 |
| sidecar-image-packaging-convention | §8.5 |
| workflow-definition-schema-and-hash | §1.6, §2.2, §6 |
| ticket-tables-and-transition-function | §1.7, §2.1, §4.2 |
| agent-step-config-schema | §2.8, §8.1 |
| events-results-shared-schema-and-run-metrics | §1.8, §1.14, §2.4, §5 |
| harvest-terminal-step-schema-and-gate-policy | §1.10, §2.8, §2.8.1, §6.3, §8.3 |
| judge-rubric-and-verdict-mapping | §6.4 |
| platform-mcp-tool-contract | §1.9, §3.2 |
| park-v2-exit-and-respawn (2026-07-10) | §1.5, §1.8, §1.9, §1.14, §3.2, §5, §8.1, §8.2 |
| gateway-tool-contract-and-metering | §3.3, §5 |
| agent-outcomes-schema | §1.11, §2.5 |
| renderer-library-and-golden-templates | §2.8 (emission target), §6 (input), §7 (output validation) |
| scorecard-rollup-api | §4.2 (`GetAgentWorkflowScorecard`), over §1.8/§1.4/§1.11 rows |

## 10. Decisions made in this document (review checklist)

1. Cross-aggregate refs are plain columns, not FKs (agent_reviews.build_id precedent) — enables independent wave landing.
2. `agent/schema` is the canonical shared schema package, extracted into its own nested Go module (stdlib-only) consumed by both the main module and ci-agent via `require`+`replace`; `ci-agent/schema` is deleted after the import switch. (ci-agent has no dependency on the main module today — the cross-module dependency is new work.)
3. Migration numbers pre-allocated in blocks of 10 per workstream (1773106010–1773106109).
4. Principal tokens: GitHub-PAT style (`cap1.<id>.<secret>`, sha256-at-rest), not JWT.
5. Credential encryption reuses `atc/db/encryption` (existing AES-GCM strategy + nonce column convention).
6. Ticket number = `agent_tickets.id`; branch = `agent/ticket-<id>`.
7. Ticket terminal states split `failed` vs `errored` (three-way taxonomy at ticket level).
8. Platform LLM work funded by a dedicated `agent-platform` service user's credential; judge spend capped separately, outside the ticket budget.
9. Benchmark cases stored in DB, not in-repo (spec open item 6).
10. MCP sidecars speak streamable HTTP on fixed ports 7780/7781/7782; discovery via env.
11. dev-mcp result taxonomy `ok/failed/error` at payload level; MCP errors only for malformed input; progress notifications ≥ every 30s — **across all three sidecars** (default 15s heartbeat, `<ROLE>_MCP_PROGRESS_INTERVAL` overrides bounded ≤ 30s, §3.1).
12. *(rewritten 2026-07-09, F14 — sidecar-POST model)* `ask_human` parks by blocking the MCP call while the SIDECAR long-polls the ATC questions route; checkpoints reuse the same `agent_run_questions` table with `kind=checkpoint` but render as a deterministic `atc.TaskStep` running the **unauthenticated loopback client** `platform-mcp checkpoint --name <n>`, which POSTs the platform sidecar's internal `POST /checkpoint` endpoint; the SIDECAR (sole holder of `AGENT_PRINCIPAL_TOKEN`) files the row (`options=["approve","reject"]`, park/0, `build_id=0`), notifies, long-polls, and replies `{approved, answer, answered_by}`; client exit codes: 0 = approved, 1 = rejected-or-error, 2 = usage error — NOT an `atc.AgentStep`/LLM prompt; reject-policy (`on_reject`) is consumed by dispatch's run-completion reconciler from the frozen workflow config, never by the client; the `PLATFORM_MCP_CHECKPOINT`/`PLATFORM_MCP_CHECKPOINT_ON_REJECT` env vars are removed (read by nothing).
13. Workflow grammar v1 is a linear sequence of `agent`/`checkpoint` steps; harvest appended implicitly; no branching/loops.
14. Prompts inline in the workflow YAML (content hash covers them).
15. Judge cited issues become `finding_type: "judge"` findings feeding the existing six-verdict feedback loop; dispositions stay ticket-level in `agent_outcomes`.
16. Params schema is a top-level pipeline-config key mirrored to `pipelines` columns; run number via `pipelines.last_run_number` row-lock increment.
17. Parked (HITL) builds count as running: parking never completes a pipeline run.
18. Human-touch delta = numstat of non-`concourse-agent[bot]` commits between pushed sha and merge, first-parent walk.
19. Notifications v1 = single generic webhook flag.
20. Results.json keeps `pass/fail/error/abstain` wire values; DB/APIs use `ok/failed/error` with a defined mapping.
21. Team-less `/api/v1/agent/*` authorized routes require a new `CheckAgentAuthorizationHandler` hardcoding team `main` (agent-identity); today's team-less agent feedback routes are silently admin-only and their `DefaultRoles` entries are dead — there is no existing main-team precedent.
22. `ask_human` timeout default answer is carried per-question via the tool input's `default` field (persisted to `agent_run_questions.default_answer`); the platform-mcp sidecar resolves timed-out questions via `AnswerAgentQuestion` with `principal(questions:answer)`; the `runs:create` scope is removed from the closed set (run creation is in-process via `PipelineRunFactory`).
23. The platform Anthropic credential reaches harvest/retrospective pods via a long-lived `agent-platform-credential` K8s secret maintained by credentials-and-budgets (env `secretKeyRef` only), separate from the per-run `agent-run-<run-id>` secret.
24. Park transport requires <60s SSE `notifications/progress` heartbeats (`ask_human` is SSE-only; the checkpoint `POST /checkpoint` endpoint is exempt but must serve with zero write/idle timeouts); park-policy principals expire at now + `--agent-park-timeout` (72h default), never NULL.
25. `((run))` = per-template run number (display only); `((run_id))` = `pipeline_runs.id`, allocated pre-materialization in the `CreateRun` tx — the only cross-table key; both names reserved in the params validator (§7).
26. Per-run secret deletion is owned by credentials-and-budgets' `RunSecretReaper` polling component (plan 02), not the pipeline-run lifecycler (§8.2).
27. Harvest re-pushes to `agent/ticket-<n>` with `--force-with-lease` (harvest is the branch's only credentialed writer; per-attempt branch names forbidden, §2.8.1).
28. Ticket lifecycle gains the terminal state `concluded` ("run finished, human reviewed, no merge intended" — spike/research flows): entered only from `needs_review` via explicit human disposition; positive sibling of `abandoned`; the outcome watcher never waits on it and merge-rate metrics exclude it (frozen-enum now-or-migration decision, FLOWS.md §4).
29. Spec-lessness/plan-lessness is normative: a ticket MAY have zero spec and zero task rows for its entire lifecycle; store methods return zero values on absence (`LatestSpec → (nil,false,nil)`, `ActivePlan → ([]Task{},nil)`); the render context is nil-safe via `*SpecView` method accessors (bare `{{.Spec.Title}}` on a spec-less ticket renders empty, `{{if .Spec}}` guards conditional content); workflow-store import validation executes every prompt against the zero render context.
30. *(2026-07-10 PARK-V2; numbered 28 in the frozen delta text)* Long parks exit-and-respawn: past `--agent-short-park-max` (30m default; `0` disables) the platform sidecar writes `flight/park.json`, the runner SIGTERMs claude and exits 86 (the checkpoint client exits 3), the run enters non-terminal `awaiting_human` (zero pods, no live claude), and answer arrival makes dispatch's reconciler re-arm a continuation build that `--resume`s the session (§1.5, §3.2).
31. *(2026-07-10 PARK-V2; numbered 29 in the frozen delta text)* `ask_human`/checkpoint are idempotent-by-question: `agent_run_questions` UNIQUE `(pipeline_run_id, step_name, kind, question_hash)`; the ask route is find-or-create; answered rows return immediately (§1.9).
32. *(2026-07-10 PARK-V2; numbered 30 in the frozen delta text)* The continuation is the same logical step: `agent_run_step_state` keyed `(pipeline_run_id, step_name)` (§1.14); completed steps replay by artifact restore (zero cost); `StepSlice` re-resolution is allowed (resolution, not reservation — partial spend ledgered at park-exit makes it self-tightening); no new BUILD status — `awaiting_human` lives at the RUN level.

## 11. Amendment log

- 2026-07-07: initial freeze.
- 2026-07-08: review fixes applied before workstream planning kicked off (cross-workstream sign-off note):
  - Conventions bullet 2 / decision 2 (affects: agent-step, ci-agent): corrected the false claim that ci-agent already depends on the main module; `agent/schema` becomes its own nested stdlib-only module, consumed via `require`+`replace` from both sides.
  - §4.2 closing paragraph / new decision 21 (affects: agent-identity + every workstream with a team-less `authorized` route): corrected the false claim that existing agent feedback routes use main-team authorization — they are admin-only today; specified the new `CheckAgentAuthorizationHandler` mechanism.
  - §1.9 / §3.2 / §6 / new decision 22 (affects: platform-mcp-hitl, workflow-store, dispatch): added the missing default-answer carrier (`default` field on `ask_human` input) and the sidecar-driven timeout-resolution protocol.
  - §4.1 / §4.2 (affects: agent-identity, platform-mcp-hitl, pipeline-runs): `AnswerAgentQuestion` gains `principal(questions:answer)` for timeout resolution; `runs:create` removed from the scope vocabulary (no route granted it; run creation is in-process).
  - §8.2 / §8.3 / §1.13 / new decision 23 (affects: credentials-and-budgets, harvest-step, process-intel-experiments): defined the previously missing carrier for the platform Anthropic credential — long-lived `agent-platform-credential` secret.
- 2026-07-08: spec/plan delivery via granular platform-mcp read tools + optional file mount (affects: platform-mcp-hitl, dispatch, workflow-store, ticket-core-consumers). Supersedes the prior "rendered spec.md/plan.md as read-only workspace inputs via env vars AGENT_SPEC_MD/AGENT_PLAN_MD" design. §3.2: `read_ticket` returns envelope + spec **only** (tasks removed); added `list_tasks` (cheap skeleton) and `get_task` (one task with `detail_md`; unknown ordering → MCP tool error `isError=true`, matching how the shared `atc/api/mcpserver` surfaces handler errors — NOT a JSON-RPC `-32602` object); `update_task_status` unchanged (write-back, available in both delivery modes). §6: added optional top-level `spec_delivery: mcp|files` (default `mcp`; Go `workflow.Config.SpecDelivery`; normal hashed field; import validation rejects other values); seed-prompt convention now instructs the first agent step to begin with `read_ticket`/`list_tasks` (`get_task` per task). Default (`mcp`/empty) injects no spec/plan bytes into any step; `files` materializes read-only `spec.md`/`plan.md` (via `tickets.RenderSpecMarkdown`/`RenderPlanMarkdown`) mounted read-only as artifact `ticket`. §8.1 carries no `AGENT_SPEC_MD`/`AGENT_PLAN_MD` keys (never present here; dispatch deletes them where they existed). DB remains single source of truth; nothing flattened by default.
- 2026-07-08: `get_task` unknown-ordering error-mechanism correction (affects: platform-mcp-hitl). §3.2 previously promised an unknown `ordering` returns a JSON-RPC `-32602` error object, but the shared `atc/api/mcpserver` maps every tool handler's returned error to a `tools/call` result with `isError=true` (a successful call carrying error content) and only emits `-32602` for a malformed `tools/call` envelope — it has no path from a handler to a `-32602` error object (locked in by its committed tests). §3.2 now specifies an unknown ordering as an MCP tool error (`isError=true`), consistent with every other platform-mcp tool-validation error, and 08-platform-mcp-hitl's handler/tests assert the response carries NO top-level JSON-RPC error object plus `result.isError=true`.
- 2026-07-09: design-review fixes F2, F11, F12, F1 (four corrections; the last co-signed):
  - **F2** — §8.2 (ephemeral run secret, Lifecycle): reworded the misleading "during dispatch, before run creation" timing. The create→claim→attach order is deliberate and race-free: the secret is attached AFTER the queued→running claim is won (which records `pipeline_run_id` per §2.1) and BEFORE the build tracker schedules the entry-job pods, so `agent-run-<pipeline_run_id>` is known before any pod referencing it is scheduled. (Affects: credentials-and-budgets, dispatch, pipeline-runs. No contract-name changes.)
  - **F11** — §8.2 (long-lived platform credential secret, Lifecycle): the `agent-platform-credential` sync contract is now stated **bidirectional** — vault deletion of the platform credential (`Store.Delete` for the platform user's row, e.g. `fly agent auth --platform --delete`) removes the long-lived K8s secret. Noted that this propagates admin intent to the data plane but does NOT revoke the upstream Anthropic token (the vault is only Concourse's store of it; upstream revocation is a separate out-of-band admin action). (Affects: credentials-and-budgets, harvest-step, process-intel-experiments.)
  - **F12** — §8.1 (`AGENT_BUDGET_SLICE_USD` Meaning): corrected "gateway enforces" — the gateway enforces the slice **for sub-agent calls only**; the main agent's own spend is admission-gated (`Checker.StepSlice`, §2.7) + post-hoc reconciled at ingestion and turn/timeout-capped within a step, NOT cut off mid-call. (Affects: credentials-and-budgets, gateway-mcp, agent-step.)
  - **F1** (co-signed: dispatch + platform-mcp-hitl) — §3.2 (Park/resume protocol), §5 (`checkpoint.wait`/`checkpoint.release`), decision 12: workflow CHECKPOINTS render as a deterministic `atc.TaskStep` invoking the platform-mcp client command `platform-mcp checkpoint` (exit 0 = approve, exit 1 = reject; exit-1 fails the task ⇒ build ⇒ run ⇒ ticket `needs_review`), NOT an `atc.AgentStep`/LLM prompt and NOT a call to a sidecar "internal `checkpoint` endpoint". The client inserts the `kind=checkpoint` `agent_run_questions` row, notifies (§8.4), and long-polls the same `.../questions/:qid` route as `ask_human`; it reads ticket/run identity from `AGENT_TICKET_ID`/`AGENT_PIPELINE_RUN_ID` (§8.1) and checkpoint name / reject-policy from argv (`on_reject`, §6.1). The `PLATFORM_MCP_CHECKPOINT` / `PLATFORM_MCP_CHECKPOINT_ON_REJECT` env vars are removed — they were read by nothing and never appeared in the §8.1 env table (no §8.1 row change needed). §5 checkpoint rows now state the step is exit-code-driven, not an LLM verdict. **[PARTIALLY RETRACTED by the 2026-07-09 checkpoint-seam amendment below: the client→ATC sentences of this entry ("client inserts the row", "long-polls the route", "reject-policy from argv", "NOT a sidecar endpoint") no longer hold.]**
- 2026-07-09: **checkpoint-seam frozen delta** (F14/F16/F17/F28/F29/F36; supersedes the F1 entry above; co-signed: dispatch + platform-mcp-hitl + ticket-core + shared contracts; noted to harvest-step and agent-step):
  - **F14** — §3.2 / §5 / decision 12 rewritten to the **sidecar-POST model** (one mechanism, one wire model): the checkpoint CLIENT is an unauthenticated deterministic loopback CLI (`platform-mcp checkpoint --name <n>`) that POSTs the sidecar's `POST /checkpoint`; the SIDECAR is the trust boundary — it alone holds `AGENT_PRINCIPAL_TOKEN`, files the `kind='checkpoint'` row, fires §8.4, long-polls `GET /api/v1/agent/tickets/:id/questions/:qid`, and replies `{approved, answer, answered_by}`. The F1 entry's sentences that the client "inserts the row", "long-polls the ATC route", "reads reject-policy from argv", and is "NOT a call to a sidecar internal checkpoint endpoint" are **RETRACTED**. §5's `checkpoint.wait`/`checkpoint.release` emitter is the platform-mcp sidecar's `/checkpoint` handler (payloads unchanged; `approved` = `answer == "approve"`).
  - **Answer validation** — the ATC answer route rejects an answer not in the row's options when `kind='checkpoint'`, so the stored answer is exactly `approve` or `reject`.
  - **Checkpoint sidecar secret-env seam + gate** (checkpoint leg of F15) — renderer-emitted checkpoint sidecars carry `AGENT_PRINCIPAL_TOKEN` as a `SidecarEnvVar.ValueFrom`/`SecretKeyRef` entry in the rendered `SidecarConfig` (the env must survive the serialized pipeline config), guarded by the new web flag `--kubernetes-sidecar-secret-prefixes` (default EMPTY = every sidecar secretKeyRef rejected at pod build; agentic deployments set `agent-run-`). Accepted residual risk, recorded here: same-worker pipelines could reference another run's `agent-run-*` secret by name — accepted for v1 because per-run principal tokens are ticket-scoped and expire at run/park timeout (§8.1). Exec-owned (agent/harvest) sidecar env uses the runtime-seams maps instead — see the runtime-seams entry below; both paths are applied by jetbridge `buildSidecarContainers`.
  - **F16** — the checkpoint task's main container carries NO `AGENT_PRINCIPAL_TOKEN` and no `((principal-token))` param (§8.1 checkpoint row set; the client authenticates to nothing).
  - **F17** — dispatch's existing Dispatcher component gains a **run-completion reconciler** (no new component constant): tickets in `running` whose run completed are walked — it is the **second writer** of `running→needs_review` (harvest at plan 09 is primary; `ErrStaleTransition`/`ErrTicketNotFound` benign) and the **second legitimate caller** of `running→queued` (rejected `send_back` checkpoint re-dispatch, attempt_count++, capped by dispatch's MaxAttempts guard); unanswered checkpoint rows on a completed run ⇒ `running→errored` + orphaned rows released via `Answer(id, "", "dispatcher")`. §1.7 annotation broadened accordingly.
  - **F28/F29** — `mcp-platform` moves to a shell-bearing base (alpine + nonroot + build-time `command -v` smoke check) because it doubles as the checkpoint task MAIN image; §8.5 gains the task-main-image constraint (POSIX `sh` + `tail`/`mv`/`cat`/`sleep`/`mkdir`/`kill`; distroless = sidecar-only). The renderer splits image refs into `Source{"repository", "tag"}` via `splitImageRef` (digest refs pass through whole) — the tag never rides inside `repository`.
  - **F36** — render-time guard: a workflow with a checkpoint step but no `platform` sidecar (or an empty image) makes `Render` return `checkpoint %q requires a "platform" sidecar in the workflow definition` — it never emits a zero-value sidecar (plan 11 render test `TestRenderCheckpointWithoutPlatformSidecarErrors`).
- 2026-07-09: **runtime-seams package** (F15/F18/F20/F21/F25/F31-pause; affects: agent-step, dev-mcp, harvest-step, platform-mcp-hitl, dispatch; all jetbridge/runtime Go changes land as agent-step plan 07 Task 11B): `runtime.ContainerSpec` gains per-sidecar maps `SidecarEnv map[string][]string` and `SidecarSecretEnv map[string]map[string]vars.SecretRef`, applied by jetbridge `buildSidecarContainers(sidecars, mainMounts, defaultDir, sidecarEnv, sidecarSecretEnv)` — populated programmatically by the owning exec per §8.1, never from public pipeline YAML; `applySecretRefs(envList, secretEnv) []corev1.EnvVar` now **appends** secretKeyRef-only EnvVars (sorted name order) for `SecretEnv` keys with no literal counterpart (§8.2 Consumption; harvest judge token path; placeholder workaround forbidden); `supervised()` covers `ContainerTypeTask` AND the new `db.ContainerTypeAgent = "agent"` (agent steps REQUIRE supervision for web-restart resume and the park protocol; get/put/check stay unsupervised); sidecar images take the workspace from CWD set by the owning exec (bare-binary ENTRYPOINTs, no hardcoded `/workspace` — §8.5 CWD-convention bullet); §8.5's non-root rule is scoped to MCP sidecar images only — `agent-runner`/`harvest-runner` run as root with `ENV IS_SANDBOX=1` (kubelet hostPath step volumes are root:root 0755, fsGroup ignored for hostPath); pause pods loop their sleep — `trap 'exit 0' TERM; while :; do sleep 86400 & wait $!; done` — so parked pods survive past 24h.
- 2026-07-09: **SSE transport & park hardening** (F13 in full; F31 principal-expiry leg; affects: dev-mcp, platform-mcp-hitl, gateway-mcp, dispatch): §3 preamble's "SSE streaming for long calls" replaced by the normative transport paragraph — all three sidecars implement the SSE progress path; any tool that can block >30s MUST stream, with heartbeats even when the handler is silent (empirical 60s claude-CLI cliff; `MCP_TOOL_TIMEOUT` is not a fix); mirrored-implementation rule (main-module servers on the upgraded `atc/api/mcpserver`, dev-mcp on `ci-agent/devmcp`; no cross-module `require`; drift-guarded by mirrored tests). §3.1's 30s mandate extended to all three sidecars, default 15s heartbeat, `<ROLE>_MCP_PROGRESS_INTERVAL` overrides bounded ≤ 30s (out-of-range = fatal startup error). §3.2 gains the PARK TRANSPORT decision (`ask_human` SSE-only; `POST /checkpoint` exempt but zero write/idle timeouts; no-timeout checkpoint client) and PARK-DURATION BOUNDS (pod lifetime via the pause loop; principal lifetime via `--agent-park-timeout`). §8.1 `AGENT_PRINCIPAL_TOKEN` row: park-policy runs minted with `expires_at = now + --agent-park-timeout` (web flag, default 72h, defined beside `--agent-run-timeout` in `atc/atccmd/command.go`; NOT NULL — implementing edit is plan 11's mint step, bound via this row); two new §8.1 rows for the progress-interval envs. `AwaitAnswer` contract: transport/5xx retried forever; consecutive 401/403 fatal after `AuthFailureLimit` (12 × 5s ≥ 60s, outliving the §1.2 verification cache) → `ErrPrincipalRejected`, surfaced loudly (`principal rejected:` prefix; checkpoint client exits 1). Decisions 11/24 updated/added.
- 2026-07-09: **final-review fixes F30/F32/F22** (affects: pipeline-runs, dispatch, harvest-step, credentials-and-budgets, ticket-core):
  - **F30** — §7 reserved-vars contract + §8.1 row (co-signed pipeline-runs + dispatch + harvest-step): `((run_id))` is a new reserved var carrying `pipeline_runs.id`, allocated pre-materialization (`nextval` in the `CreateRun` tx, explicit-id insert); `((run))` remains the per-template run NUMBER (display/instance-group only, never a cross-table key); both names rejected as user param names by the params validator; interpolation covers params, sidecar env values, and secretKeyRef secret names; all cross-table keys (`AGENT_PIPELINE_RUN_ID`, `agent-run-<id>` secret, metrics/questions/reviews/gateway rows) use `((run_id))`; step execs fall back to `envInt("AGENT_PIPELINE_RUN_ID")` when a rendered plan's `PipelineRunID` is 0.
  - **F32** — new §2.8.1 (co-signed harvest-step + ticket-core): harvest re-push pinned as `git push --force-with-lease=refs/heads/agent/ticket-<n>` (attempt 2+ is a fresh clone ⇒ plain push is deterministically non-fast-forward); safe because §8.3 makes harvest the branch's only credentialed writer; per-attempt branch names forbidden; divergent-remote-head fixture spec required.
  - **F22** — §8.2 Lifecycle (deletion ownership): per-run `agent-run-<id>` secret deletion is owned by credentials-and-budgets' new `RunSecretReaper` polling component (plan 02, beside `PlatformSecretSyncer`; label-driven sweep + narrow `RunActive(runID)` seam; best-effort principal revoke in the same pass; covers the Attach→schedule crash window). Plan 03's lifecycler stays pure (no clientset) — the earlier "pipeline-run lifecycle component deletes on completion via Cleanup" attribution is retracted.
- 2026-07-09: **flow-decoupling edits + `concluded` terminal state** (per FLOWS.md edit list E2/E5 and its §4 now-or-migration call on the state enum; owner-approved; affects: workflow-store, dispatch, platform-mcp-hitl, ticket-core, delivery-outcomes, scorecards):
  - **E2 — §6.2 nil-safe render semantics made normative.** `.Spec` MAY be nil / `.Tasks` MAY be empty at render time — the NORMAL state, since rendering happens at dispatch before any agent step can submit a spec. One mechanism, specified exactly: the renderer builds `RenderContext{Ticket TicketView, Spec *SpecView, Tasks []TaskView, Params map[string]string}` where `SpecView` has no exported fields and only nil-safe pointer-receiver method accessors (`Title`/`BodyMD`/`AcceptanceCriteria`) — so `{{if .Spec}}` guards work (nil pointer is falsy) AND bare `{{.Spec.Title}}` on a spec-less ticket renders the empty string instead of failing template execution. `{{range .Tasks}}` iterates zero times on the empty slice. Workflow-store `Validate()` gains an import gate: every prompt template is executed against the zero render context and the definition is rejected on execution error (renderer golden tests with `Spec=nil`/`Tasks=nil` in both delivery modes land in plan 11, FLOWS.md E4). §6 YAML render-context comment annotated. New decision 29.
  - **E5 — zero spec/task rows normative** (§1.7, §2.1, §3.2): added "a ticket MAY have zero spec rows and zero task rows through its entire lifecycle; all consumers must handle absence" to §1.7; §2.1 documents `LatestSpec → (nil, false, nil)` and `ActivePlan → ([]Task{}, nil)` (empty slice, nil error — implementers must not add error-on-absence); §3.2 softens `read_ticket`'s spec description to "latest spec, if any", pins `list_tasks` with no plan as `{"tasks": []}` success, and specifies `get_task` with no active plan as an MCP tool error (`isError=true`) naming the absent plan (`no active plan for ticket <id>`), matching the unknown-ordering mechanism.
  - **`concluded`** (§1.7 CHECK + state machine, §2.1 `StateConcluded`, §1.11 disposition CHECK + prose, §2.5 comment, new decision 28): new TERMINAL ticket state — "run finished, human reviewed, no merge intended" (spike/research flows) — the positive sibling of `abandoned`, entered only from `needs_review` via explicit human disposition (`SetAgentTicketDisposition` → transition function; never written by harvest/dispatch/reconciler). The outcome watcher must not wait on `concluded` tickets (an existing outcome row is closed terminally like `abandoned` — watcher stops polling, no merge expected — but as its own `merge_state = 'concluded'`, never `closed_unmerged` (§1.11.1 / delivery-outcomes); push-less runs have no row), and merge-rate metrics exclude them. Landed now because §1.7's enum is frozen — adding it later means a CHECK-constraint migration plus backfill ambiguity for spikes already rotting in `needs_review`.
- 2026-07-09: **flow-decoupling verifier follow-ups** (affects: delivery-outcomes, scorecards, workflow-store, dispatch):
  - **Concluded ≠ closed_unmerged.** The three "closes the outcome exactly like `abandoned`" phrasings (§1.7, §1.11, and the `concluded` entry above) reworded to separate the shared terminal behavior (watcher stops polling; no merge expected) from the differing close state: a `concluded` disposition closes the row as its own `merge_state = 'concluded'`, never `closed_unmerged` (delivery-outcomes owns the distinction and must never bucket the two).
  - **Symmetric enum amendment.** The 2026-07-09 edit had amended only the `disposition` CHECK in the §1.11 DDL; the DDL is now amended in place symmetrically — `merge_state` CHECK gains `'concluded'` and `disposition_reason` CHECK gains `'research_complete'` — and §2.5 gains `MergeConcluded = "concluded"` (matching delivery-outcomes' `types.go`). 12-delivery-outcomes' §1.11.1 addendum bullet remains the authoritative carrier and now records the §2.5 constant too.
  - **§6.2 import gate aligned to plan 05.** The gate context is plan 05's enforcing `nilRenderContext` mirror — tolerant `map[string]any` `.Ticket`/`.Params` (unknown envelope-field references render `<no value>`, never false-rejected), `.Spec` a plain nil pointer so only a `.Spec` nil-deref import-blocks — replacing the earlier "zero `TicketView`" wording, which would have rejected templates plan 05 accepts.
- 2026-07-10: **PARK-V2 seam delta — exit-and-respawn for long human-waits** (FROZEN 2026-07-10; amends the 2026-07-09 "SSE transport & park hardening" entry; implements FLOWS.md P2.5 recommendations #1–#4; co-signed: agent-step (07) + platform-mcp-hitl (08) + pipeline-runs (03) + dispatch (11) + shared contracts; noted to ticket-core, credentials-and-budgets, workflow-store, scorecards). Principle: the SSE park (PARK-V1) is retained as the SHORT-PARK mechanism, unchanged below `--agent-short-park-max` (30m default; `0` = pure PARK-V1 rollback hatch); beyond it the agent EXITS and the answer RESPAWNS it (`claude -p --resume <session-id>`) — a wait stops impersonating a running step. Nothing in F13, F31 legs 1–3, or the checkpoint seam is retracted; PARK-V2 sits above them and makes the >threshold branch of each moot. Owner-approved: (1) exit-and-respawn hybrid with the `awaiting_human` run state decided NOW pre-freeze; (2) stream-json live watching; (3) session JSONL + `session_id` capture; (4) parked UI badge. The TICKET enum is NOT reopened — the ticket stays `running`; parked-ness derives from run state + open questions. Edits carried here:
  - **§1.5** — `pipeline_runs.status` CHECK gains non-terminal `'awaiting_human'` (migration `1773106032`; partial status index widened to `('running','awaiting_human')`); lifecycler-owned entry predicate (no pending/started builds + ≥1 entry build + open `timeout_policy='park'` question rows → `awaiting_human`, `completed_at` NULL, not complete); completion detection/retention/dispatch-reconciler treat it exactly as pending; exit via the F26 reopen query, whose `NotEq('running')` filter already admits `awaiting_human` (no query change — plan 03 Task 24 pins it) (resume) or the `--agent-park-timeout` wall clock (oldest open park question `asked_at` + 72h → run `errored`, rows released via `Answer(id, "", "platform")`, §8.4 `run.park_expired` notification, reconciler's unanswered-checkpoint branch errors the ticket). §2.3 gains `PipelineRunAwaitingHuman`.
  - **§1.8** — metrics `status` CHECK gains `'parked'` + `session_id` column (`TEXT NOT NULL DEFAULT ''`, same additive migration `1773106061`, agent-step block; `ThreeWayStatus` maps `parked → parked`; partial ingestion + partial-spend ledger append at park-exit); consumer note: executions of one logical step share `(pipeline_run_id, step_name)` — aggregate across rows with that key.
  - **§1.9** — `question_hash` column + `agent_run_questions_dedup` UNIQUE `(pipeline_run_id, step_name, kind, question_hash) WHERE pipeline_run_id IS NOT NULL` (migration `1773106072`); `AskAgentQuestion` becomes find-or-create (IDEMPOTENT-BY-QUESTION: answered rows return immediately — the resume fast path; open rows are joined); the checkpoint `ckOpen` map is demoted to a same-pod optimization.
  - **NEW §1.14** — `agent_run_step_state` (owner agent-step, migration `1773106062`): upserted at every agent-step end (`completed` normally, `awaiting_human` on exit-86 with `session_id`/`question_id`/artifact handles); outputs registered into the fabric even on exit-86 (F23 path); continuation-pin GC rule — handles referenced by rows of runs in (`running`,`awaiting_human`) are exempt from volume GC until the run is terminal (bounded by `--agent-park-timeout`).
  - **§3.2** — PARK TRANSPORT/PARK-DURATION BOUNDS scoped to SHORT-PARK; new PARK-V2 block: `--agent-short-park-max` threshold (sidecar-owned timer, both `ask_human` and `/checkpoint` parks; `PLATFORM_MCP_SHORT_PARK_MAX_SECONDS`; per-workflow override deferred), `flight/park.json` sentinel contract (atomic temp+`mv`; shared flight mount; survives sidecar crash; rides the ingested flight artifact), runner exit **86** / checkpoint-client exit **3** (202 `{"parked": true}`), the distinguished-end triple (exit code + additive `awaiting_human` build event + `step.park` + §1.14 row; build finishes `failed` as carrier only — the authority is the OPEN park-policy question row, never build status; park survives pod death), resume re-arming via dispatch's `reconcileAwaitingRuns` sibling pass (re-mint principal, `Attach` refresh, continuation build `created_by = "agent-dispatcher:resume"`; `AnswerAgentQuestion` fires the dispatcher notify), continuation semantics (completed → short-circuit replay REQUIRED; awaiting_human → `--resume` with the frozen `agentrunner.ContinuationPrompt`; no row → cold; checkpoints re-POST and dedup-resolve), the idempotent-by-question resume rule, and the StepSlice re-resolution budget rule (resolution, not reservation; self-tightening; no double-count). `ask_human` gains the idempotency tool-description note.
  - **§5** — additive events `step.park` `{"step_name","question_id","wait_seconds_at_exit","session_id"}` and `step.resume` `{"step_name","session_id","question_id"}`; `step.end` gains optional `session_id`; `step.start` gains optional `resumed`/`replayed`; ingestion exception: a stream ending in `step.park` (no `step.end`) ingests as `parked` — the ONE sanctioned exception to missing-`step.end`-is-error.
  - **§8.1** — new rows `AGENT_SESSION_ID` + `AGENT_SESSION_FILE` (continuation-only), `AGENT_PARK_EXIT_GRACE_SECONDS` (default 30), `AGENT_STREAM_LOG_MAX_LINE_BYTES` (default 16384; tee-only truncation), `PLATFORM_MCP_SHORT_PARK_MAX_SECONDS`, and (2026-07-10 follow-up — producer assigned per F15) `PLATFORM_MCP_PARK_PATH` (agent-step exec via `SidecarEnv["platform"]`, plan 07 Task 26; NOT renderer-emitted — only the exec knows the flight mount path; unset = never write, the legal checkpoint-pod shape).
  - **§8.2 / §2.6** — `SecretAttacher.Attach` amended (additive) to CREATE-OR-UPDATE the same `agent-run-<pipeline_run_id>` secret (resume refresh; credential re-resolved via `credentials.Backend`); `RunActive(runID)` counts `awaiting_human` as ACTIVE.
  - **Decisions 30–32** appended (numbered 28–30 in the frozen delta text). Gating: the PARK-V2 build is gated by the empirical pin `TestLiveClaudeParkExitResume` (plan 07, `//go:build live_claude`; pins P1–P6); red = fall back to `--agent-short-park-max=0` with zero schema waste (all schema changes are additive and inert at 0).
- 2026-07-08 (agent-identity planning addendum, cross-workstream sign-off note):
  - §1.2 / attribution convention (affects: harvest-step, ticket-core, delivery-outcomes): `agent_reviews` gains `submitted_by TEXT NOT NULL DEFAULT ''` via migration `1773106011` (inside agent-identity's 1773106010–19 block), recording the writing principal's name — the first demonstrator of the created_by/submitted_by audit-attribution convention. Orthogonal to harvest-step's planned ticket/run linkage columns (block 1773106080–89); no collision.
  - §4.1 Go surface names frozen by agent-identity: package `agent/api/principals` (types `Principal`, `CreateSpec`, `Store`, `Verifier`, `MemoryStore`, `Handler`; funcs `MintToken`, `ParseTokenID`, `HashToken`, `DisplayPrefix`, `NewContext`, `FromContext`; scope constants `ScopeReviewsWrite`, `ScopeTicketsRead`, `ScopeTicketsWrite`, `ScopeMetricsWrite`, `ScopeCostsWrite`, `ScopeQuestionsAnswer`; `LegacyPublishPrincipalName`; `TokenVersionPrefix = "cap1."`); `atc/db.AgentPrincipalsFactory` / `NewAgentPrincipalsFactory`; `atc/api/auth.CheckAgentPrincipalHandlerFactory` / `NewCheckAgentPrincipalHandlerFactory`; `atc/api/auth.CheckAgentAuthorizationHandler`. Verification failures on the principal tier are uniformly 401.
  - §4.2: the per-route scope audit (the document later workstreams add rows to) lives at `docs/superpowers/plans/agentic-platform/agent-route-scopes.md`.
- 2026-07-08 (credentials-and-budgets wave-1 planning addendum; affects: agent-identity, dispatch, gateway-mcp, workflow-store, agent-step):
  - **SubmitAgentCostRecord interim auth:** until agent-identity's `principal(costs:write)` tier lands, `POST /api/v1/agent/costs` ships with the same handler-validated static publish token as `SubmitAgentReview` (wrappa pass-through case, `Authorization: Bearer <--agent-review-publish-token>`). agent-identity flips both routes to principal auth in its cutover task; no OTHER route may adopt the static token.
  - **SecretAttacher labels:** `credentials.SecretAttacher.Attach` (§2.6) has no ticket parameter, so the per-run secret is created with only the `concourse/agent-run: "<run-id>"` label. Dispatch adds the `concourse/ticket` label itself when it has one — §8.2's label list is a target state satisfied jointly, and the reaper sweep keys off `concourse/agent-run` alone.
  - **`group_by=workflow` rollups (§4.2 `GetAgentCostRollup`):** `agent_cost_ledger` has no workflow column; workflow attribution rides `metadata->>'workflow'`. Writers that know their workflow (agent-step ingest, gateway metering) MUST set `{"workflow": "<name>@<version>"}` in `metadata`.
  - **`fly agent` command family:** `fly/commands/agent.go` `AgentCommand` struct is created by credentials-and-budgets with `Auth`/`Costs` subcommand fields; wave-mates (workflow-store) and later workstreams add their own fields (`Workflows`, `Tickets`, …) to the same struct — additive merges only.
  - **Migration deploy ordering:** the migrator is version-pointer based (`atc/db/migration/migration.go` `Migrate`: `currentVersion < m.Version`), so a deployed DB whose head is 1773106021+ will NEVER later apply a lower-numbered migration. Wave-1 branches MUST merge to `jetbridge` in migration-number order (identity 1773106010s → credentials 1773106020s → pipeline-runs 1773106030s → workflow-store 1773106040s) before any theborg deploy picks them up.
  - **Key rotation:** `agent_user_credentials.encrypted_token` is added to `atc/db/migration/encryption.go` `encryptedColumns` so `concourse web --encryption-key` rotation re-encrypts the vault (validates the §1.3 encryption decision — the rotation list is hardcoded and would otherwise silently skip the vault).
  - **Third credentials migration:** 1773106022 (within the allocated 1773106020–29 block) seeds the §1.13 `agent-platform` service user and creates the `agent_cost_daily_rollup` SQL view (the "dashboard view" deliverable).
  - **§1.13 ticket-budget arithmetic:** `budget.Ledger.SpentForTicket` (and therefore `Checker.TicketRemaining`/`StepSlice`) EXCLUDES `source = 'harvest_judge'` rows — per §1.13 the judge must never be starved by an agent that burned the ticket budget; judge spend is capped separately by the workflow's `judge_usd`. The global daily cap (`SpentSince`) includes ALL sources, platform spend included.
  - **Platform-credential provisioning (§1.13/§8.2):** the `agent-platform` service user never logs in, so `PUT /api/v1/agent/user-credentials` accepts an optional body field `"user": "platform"` (the ONLY non-self value), allowed for admin tokens only, which vaults the credential onto the service user's row; `GET`/`DELETE /api/v1/agent/user-credentials[/:kind]?user=platform` mirror it. Surfaced as `fly agent auth --platform [--delete]` (admin). All other access remains strictly self-scoped.
- 2026-07-09 (credentials-and-budgets final-review F22 addendum; affects: pipeline-runs, dispatch, agent-identity, gateway-mcp):
  - **Run-secret cleanup ownership (§8.2's "reaper safety-net GC"):** OWNED by credentials-and-budgets. `credentials.RunSecretReaper` (plan 02 Task 15a) IS the safety-net GC that §8.2, plan 03, and plan 11 reference: a polling `RunnableComponent` beside the platform syncer that lists worker-namespace secrets by the `concourse/agent-run` label, deletes any whose run is complete or absent (narrow `RunActive(runID)` seam; production impl `atc/db.NewAgentRunChecker` over `pipeline_runs` — absent row OR absent table = inactive), and best-effort revokes the per-run principal `agent-run-<run-id>` in the same pass. Attribution rewording: dispatch's in-process `SecretAttacher.Cleanup` on abort/error paths (plan 11) is the FIRST line of defense only; plan 03's lifecycler stays deliberately pure (no attacher/clientset — do NOT plumb one in). A 5-minute creation-grace window protects the dispatch `CreateRun`→`Attach` ordering from sweep races. The `PrincipalRevoker` binding ships nil until agent-identity's store lands (its cutover task binds it) — safe interim because per-run principals carry `expires_at`, unlike the secret.
- 2026-07-08: pipeline-runs wave-1 addendum (§7.1): template-column-true-on-instances rule,
  frozen-check-set pinning, completion reopen semantics + §2.3 interface extensions,
  run_retention YAML key, reserved param name `run`, 409 on template job trigger, wire shapes.
- 2026-07-09: pipeline-runs design-review fixes F26/F27/F30 in §7.1: reopen detection also
  matches job builds completed after the run's `completed_at` (fast-finish retriggers, F26);
  the frozen-check enqueue lives in `db.PipelineRunFactory.CreateRun`, the runs handler is a
  pass-through (F27); new reserved var `((run_id))` = `pipeline_runs.id` allocated via
  `nextval` before materialization, reserved param names now `run` AND `run_id` (F30,
  co-signed with dispatch + harvest for the §8.1 `AGENT_PIPELINE_RUN_ID` consumers).
- 2026-07-08 (workflow-store, owner amendments; affects consumers: dispatch, harvest-step, platform-mcp-hitl, scorecards, process-intel-experiments — additive only):
  - §1.6: added `promoted_by TEXT NOT NULL DEFAULT ''` so `Store.Promote`'s existing `promotedBy` argument is persisted (the interface already carried it; the DDL had no column).
  - §2.2: added `Definition.RawYAML` (`json:"raw_yaml,omitempty"`) carrying the exact stored YAML bytes on `Get`/`Live` responses; `List`/`Versions` leave it empty (and leave `config` as a zero object — metadata-only listings).
  - §4.2 workflow-route HTTP shapes pinned by the owner:
    - `GET /api/v1/agent/workflows` → 200 `[{"name","description","latest_version","content_hash","live_version","created_at"}]` (`live_version` 0 = none live).
    - `GET /api/v1/agent/workflows/:workflow_name/versions` → 200 `[Definition]` (metadata only), 404 unknown name.
    - `GET /api/v1/agent/workflows/:workflow_name/versions/:version` → 200 `Definition` incl. `config` + `raw_yaml`, 404 unknown, 400 non-integer version.
    - `POST /api/v1/agent/workflows/:workflow_name/versions` — body is the raw definition YAML (any Content-Type, ≤1 MiB) → 200 `Definition` (idempotent on content hash: re-importing identical bytes returns the existing version), 400 on parse/validation/name-mismatch, 413 oversize.
    - `PUT /api/v1/agent/workflows/:workflow_name/versions/:version/live` → 204, 404 unknown (name, version).
  - §6 grammar: added the optional top-level `spec_delivery` field (Go `Config.SpecDelivery string`, yaml/json `spec_delivery,omitempty`; values `""`/`mcp`/`files`, empty ⇒ `mcp`; a normal hashed field, write-time validated to reject any other value). This replaces the prior "rendered spec.md/plan.md as env vars `AGENT_SPEC_MD`/`AGENT_PLAN_MD`" design: the DB stays the single source of truth and nothing is flattened by default. Owned by workflow-store (§6), referenced by contracts §6, consumed by dispatch's renderer (11-dispatch) — `mcp` injects no spec/plan bytes (agents read via platform-mcp `read_ticket`/`list_tasks`/`get_task`, implemented by platform-mcp-hitl over ticket-core `Store` methods `Get`/`LatestSpec`/`ActivePlan`); `files` materializes read-only `spec.md`/`plan.md` mounted as the `ticket` artifact. Affects consumers: platform-mcp-hitl, dispatch, workflow-store, ticket-core-consumers.
  - Slot-shape freeze for wave-3 review: the `checkpoint:` step fields (`on_reject: fail|send_back`), `hitl` block (`ask_timeout: park|default|fail`, `ask_timeout_seconds`), `gate_policy` block (§6.3 YAML grammar — each `gates[]` entry carries `gate`, `scope`, `focus`, `timeout`, and the optional `retries: 0..2` flake-retry key harvest-step consumes; workflow-store validates the `0..2` bound at import and carries `Gate.Retries` through so dispatch's renderer can map it onto `harvest.Gate.Retries`), and `judge` block are stored and write-time validated by workflow-store but INERT until platform-mcp-hitl and harvest-step consume them; those workstreams review these shapes at wave-3 start and any change lands as a new `schema_version`, never a mutation of v1. §6.1's "optional top-level `schemas` map" is realized as `schemas: map[string]string` in `Config`. The optional top-level `spec_delivery` field (`SpecDelivery string`, values `""`/`mcp`/`files`, empty ⇒ `mcp`) is a normal hashed field owned by this grammar and validated at import; it is INERT here (workflow-store never renders) and is consumed by dispatch's renderer to pick the spec/plan read model — `mcp` (default: no spec/plan bytes injected; agents read via platform-mcp `read_ticket`/`list_tasks`/`get_task`) vs `files` (read-only `spec.md`/`plan.md` mounted as the `ticket` artifact).
  - Wrappa placement note: the five workflow routes land in the existing `auth.CheckAuthorizationHandler` case group (admin-only in effect, per decision 21) with `DefaultRoles` entries in place; agent-identity moves them onto `CheckAgentAuthorizationHandler` together with the existing agent feedback routes.
- 2026-07-08 (dev-mcp interface finalization — closes the implementation-level residue of spec open item 3; owner: dev-mcp; consumers notified: harvest-step, agent-step, process-intel-experiments, platform-mcp-hitl, gateway-mcp):
  - **Result encoding:** tool payloads are carried as a single `text` content block containing the JSON object (`{"content":[{"type":"text","text":"<json>"}]}`), matching the `atc/api/mcpserver` precedent. `isError` content results are never used for the ok/failed/error taxonomy.
  - **JSON-RPC error codes:** `-32602` covers all malformed input — unknown tool name, unknown component id, a component that does not define the requested command, `focus` on a component with no `focus_flag`, and missing/mistyped arguments. `-32603` covers server-internal marshaling faults. `-32700` parse errors, `-32601` unknown methods (unchanged from the precedent).
  - **Exit-code convention** (command-backed implementations): exit 0 → `ok`; exit codes listed in the command's `failed_exit_codes` (default `[1]`) → `failed`; any other exit code, spawn failure, or context cancellation → `error`.
  - **Progress/SSE:** the server responds with `text/event-stream` when the client sends `Accept: text/event-stream` AND `params._meta.progressToken`; otherwise buffered JSON (progress dropped). SSE frames are `event: message` + `data: <json-rpc message>`; progress notifications are `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":<echoed verbatim>,"message":"<latest output line>"}}` emitted on a heartbeat (default 15s — half the contract's 30s bound; env override `DEV_MCP_PROGRESS_INTERVAL`, Go duration syntax), with the final JSON-RPC response as the last frame.
  - **Logs:** `log_path` is workspace-relative under `.dev-mcp/logs/`; `output_tail` is the last ≤200 lines. The v1 reference implementation emits no structured `failures` entries (the field is optional in §3.1).
  - **Reference implementation config:** `dev-mcp.yml` at the repo root — `schema_version: 1`, a `components` list (`id`/`description`/`paths`/`kind` + optional `build`/`test`/`lint` command specs: `cmd` argv array, optional `dir`, `focus_flag`, `failed_exit_codes`), and an optional top-level `repo:` command group used when `component` is omitted. Whole-repo calls on an implementation without a `repo:` section are malformed input (`-32602`). This repo's `topgun` component defines only `test` (no Makefile build/lint target exists for it).
  - **Contract-test kit API:** `contracttest.Run(t, endpointURL)` runs the universal protocol/schema/taxonomy checks; `contracttest.RunWithOptions(t, endpointURL, Options{...})` adds opt-in execution checks (exercise-ok component, failing-lint taxonomy, slow-test progress emission, affected-path mapping).
- 2026-07-09 (SSE transport generalization — dev-mcp's §3-preamble SSE finalization becomes NORMATIVE for all three sidecars; owner: dev-mcp; consumers notified: platform-mcp-hitl, gateway-mcp, agent-step, harvest-step; resolves F13):
  - **Wire spec of record:** the 2026-07-08 Progress/SSE bullet above — SSE gating on `Accept: text/event-stream` AND `params._meta.progressToken`, frames `event: message` + `data: <json-rpc message>`, progress notifications `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":<echoed verbatim>,"message":"<latest>"}}` on a coalescing heartbeat ticker, final JSON-RPC response as the LAST SSE frame, buffered JSON when the client doesn't opt in — now binds dev-mcp, platform-mcp, AND gateway (delta D1/D3). Rationale is empirical: the claude CLI (v2.1.77) abandons a progress-free buffered tools/call at exactly 60s, silently ("(completed with no output)", no error flag); `MCP_TOOL_TIMEOUT` does NOT prevent it. Any MCP tool whose handler can block longer than 30s MUST be served over this SSE path.
  - **Mirrored implementation, not shared code:** `ci-agent` is a standalone Go module — the root module MUST NOT `require` ci-agent and ci-agent MUST NOT `require` the root. `ci-agent/devmcp` stays the reference server, unchanged; `atc/api/mcpserver` (currently buffered-only) is upgraded IN PLACE with a byte-similar port of `ci-agent/devmcp`'s SSE path (lands as 08 Task 9b, before 08 Task 10 and 10 Task 7), and platform-mcp/gateway build on it. No new shared module is extracted. Drift guard: `atc/api/mcpserver/server_test.go` gains SSE tests mirrored from `ci-agent/devmcp/server_test.go` (04 Task 4) asserting the identical frame shape.
  - **Heartbeat env pattern:** `DEV_MCP_PROGRESS_INTERVAL` generalizes to `<ROLE>_MCP_PROGRESS_INTERVAL` — `DEV_MCP_PROGRESS_INTERVAL` / `PLATFORM_MCP_PROGRESS_INTERVAL` / `GATEWAY_MCP_PROGRESS_INTERVAL` — Go duration syntax, default 15s (`DefaultHeartbeat`, half the §3.1 30s progress bound, 4x margin under the 60s CLI cliff). VALIDATION in all three binaries: a set-but-invalid value, a value <= 0, or a value > 30s is a FATAL startup error — never clamp silently.
  - dev-mcp's TRANSPORT is already compliant (its Task 4 server is the F13 proven-surviving implementation) and its BINARY enforces the same fatal bounds on `DEV_MCP_PROGRESS_INTERVAL` (04 Task 8: set-but-invalid, <= 0, or > 30s exits at startup, mirrored cmd-level test; `devmcp.NewServer`'s `<= 0 → DefaultHeartbeat` fallback is a library convenience for the unset case only — never reachable from a set env var); nothing else in plan 04 changes.
- 2026-07-08 (agent-step wave-2 plan): §8.1 gains agent-step-owned env vars (`AGENT_PROMPT`, `AGENT_PROMPT_FILE`, `AGENT_MODEL`, `AGENT_MAX_TURNS`, `AGENT_OUTPUT_SCHEMA`, `AGENT_FLIGHT_DIR`), the implicit `flight` output convention, MCP-URL-by-sidecar-name derivation, the `--agent-step-image` web flag, and the `agent-runner` image + `agent` process-ID conventions. Affects: dispatch (renderer emits `AgentStep.Env` incl. identity keys), gateway-mcp (reads `AGENT_BUDGET_SLICE_USD` as already specified). No existing rows changed.
- 2026-07-12 (agent-step review finding): §8.1 agent-step-owned additions gain `AGENT_PLAN_ID` (main container, exec-set literal from the step's plan id — never renderer/public YAML). §5 fixes `step.start`'s `build_id`/`plan_id` as non-optional, but no renderer-emitted env carries the plan id, so without this row every event stream opened with `"build_id":0,"plan_id":""` and could not be joined back to its `agent_run_metrics` row. Additive; no existing rows changed; no consumers need action (agent-runner reads it, everyone else ignores it).
- 2026-07-12 (agent-step review finding — migration-hole fix): the agent-step block's parked/`session_id` migration (§1.8, Task 21) and the `agent_run_step_state` migration (§1.14, Task 25) **swap numbers** — parked is now `1773106061`, `agent_run_step_state` is now `1773106062`. Rationale: Task 21 (parked) lands in wave 2; Task 25 (`agent_run_step_state`, PARK-V2 continuation) is deferred and gated on the `TestLiveClaudeParkExitResume` pin. The migrator is version-pointer based (`currentVersion < m.Version`), so shipping parked at the HIGHER number `1773106062` while `1773106061` stays reserved-but-absent leaves a hole the migrator **silently skips forever** once a DB reaches `1773106062` — `agent_run_step_state` could then never be created there. Giving the landing-first migration the lower number closes the hole; the deferred `agent_run_step_state` (`1773106062`) is now ABOVE this branch's head, and per §6/§7 ascending-merge-order discipline it must be renumbered to a value above the deploy head at land time if other blocks have advanced the pointer past it. Consumers that name `agent_run_step_state`'s migration number (dispatch plan 11, platform-mcp plan 08) updated to `1773106062`. The migration CONTENT is unchanged; the code files were renamed accordingly (`1773106061_agent_run_metrics_parked.{up,down}.sql`) and `jetbridgeHeadMigration` is now `1773106061`.
- 2026-07-16 (Task 19 dual-run finding): §8.1 agent-step-owned additions gain `AGENT_OUTPUT_<NAME>` (main container, exec-set literal `<workdir>/<name>` for every user-declared output; name uppercased with `-`→`_`). Dual-run #2's native review agent cd'd into the `repo` INPUT, wrote its deliverable to a cwd-relative path there, and the declared `review` output shipped empty — prompts had no deterministic way to name an output's location (only the reserved `AGENT_FLIGHT_DIR` was exported). Additive; no existing rows changed; agent-runner ignores it (prompt-consumed only). Same day, agent-runner hardening: claude now runs in its OWN process group (pty HUP from a severed exec killed it despite the supervisor shield — Node installs its own SIGHUP handling) and cancellation kills the whole detached group (the native reviewer itself flagged that leaked claude descendants escaped the terminal-end group kill once detached — the shared pgid had been the safety net).
- 2026-07-17 (Task 19 cutover, owner-approved): the `agent-review` shell job (build-from-source ci-agent) is RETIRED from the jetbridge pipeline; `agent-review-native` (the `agent:` step) is the review job of record. The plan's "5 dual-green runs, scores within ±2" criterion was replaced by MECHANISM parity, owner-approved: across 4 dual-runs the native path proved run/review/publish/metering end-to-end (agent-review-native #6: 139 turns, $1.47, review published `{"status":"saved"}` with correct repo/commit, agent_run_metrics row exact), while the shell reviewer hard-failed its own verdict 3 runs straight on diffs the native scored 6.5–7.5 — rubric divergence, not machinery. Campaign fixes recorded in the 2026-07-16 entries; final addition: agent-step output names are bounded to `[A-Za-z0-9_-]+` (an `=` in a name spliced a second env assignment past the collision guard — native review finding #6, closed same day).
- 2026-07-17 (ticket-core migration renumber — same failure mode as the 2026-07-12 migration-hole fix): the ticket-core block `1773106050–52` reserved in §1.1 was overtaken by the deploy head before ticket-core landed — agent-step shipped `1773106060`/`1773106061` and theborg's DB is already at `1773106061`. The migrator is version-pointer based (`currentVersion < m.Version`), so landing the ticket tables at their reserved lower numbers would leave them **silently skipped forever** on any DB at `1773106061`+. Per the §6/§7 ascending-merge-order discipline (and exactly as the 2026-07-12 entry prescribed for this case), the three ticket migrations are renumbered to the next slots above head: `agent_tickets` = `1773106062`, `agent_ticket_specs` = `1773106063`, `agent_ticket_tasks` = `1773106064`; `jetbridgeHeadMigration` / `JETBRIDGE_VERSION` become `1773106064`. The deferred PARK-V2 `agent_run_step_state` (agent-step Task 25), previously re-reserved at `1773106062`, moves to **`1773106065`** (still deferred; still must be re-renumbered above head at land time if the pointer advances past it again). DDL content is unchanged from §1.7 + the ticket-core addendum below. Consumers that name these numbers (§1.1 table, §1.14 stub header, agent-step plan 07, platform-mcp plan 08, dispatch plan 11) updated; the `1773106050–59` block is vacated, never to be reused.
- 2026-07-17 (ticket-core wave-start addendum, cross-workstream sign-off note — plan 06 Task 1, landed at execution time with the migration numbers above):
  - §1.7 (affects: platform-mcp-hitl, dispatch, harvest-step, delivery-outcomes, process-intel-experiments): `agent_tickets` gains two additive columns in migration `1773106062`: `created_by TEXT NOT NULL DEFAULT ''` (the agent-identity audit-attribution convention — principal name or human username that created the row) and `external_ref TEXT NOT NULL DEFAULT ''` (the Jira phase-2 seam from spec open item 10: holds the external issue key, e.g. `PROJ-123`; empty for native tickets; design note deferred with the rest of plan 06 Task 14). Field growth only; all other §1.7 columns, checks, and indexes are byte-identical.
  - §2.1 (affects: platform-mcp-hitl, dispatch, harvest-step, delivery-outcomes): Go surface names frozen by ticket-core, all in `agent/api/tickets`: `Ticket` gains `CreatedBy`/`ExternalRef` fields; supporting types `Spec`, `Link`, `Task`, `TaskStatus` (constants `TaskPending`, `TaskInProgress`, `TaskDone`, `TaskSkipped`, `TaskBlocked`), `ListFilter{State, Repo, Origin, Limit}`, `Update{Title, Body, BudgetUSD, WorkflowName, WorkflowVersion, TargetBranch}` (all pointers, nil = unchanged), `TransitionMeta{PipelineRunID *int, Branch string, ErrorDetail string}`, `TicketDetail{Ticket, Spec *Spec, Tasks []Task}`; HTTP request types `CreateRequest`, `UpdateRequest`, `TransitionRequest`, `SpecSubmission`, `PlanSubmission` (+`PlanTask`), `TaskStatusRequest`; errors `ErrInvalidTransition`, `ErrTicketNotFound`, `ErrStaleTransition`, `ErrNoActivePlan`, `ErrTaskNotFound`; funcs `ValidTransition(from, to State) bool`, `ValidState`, `ValidOrigin`, `ValidTaskStatus`; `MemoryStore`. The `Store` interface gains one additive method: `AppendTaskNote(ticketID, planVersion, ordering int, note string) error` — the persistence carrier for §3.2 `update_task_status`'s optional `note` field (appended to the task's `detail` as a markdown blockquote, `"> <note>"`, joined with blank lines). `atc/db.AgentTicketsFactory` / `NewAgentTicketsFactory` (dbfakes: `FakeAgentTicketsFactory`).
  - §2.1 transition side effects (affects: dispatch, harvest-step, platform-mcp-hitl, delivery-outcomes): `Transition` records: → `queued`: `queued_at=now()`, `completed_at=NULL`, `attempt_count+1` when from=`running` — the edge reads `running → queued (retryable platform error OR rejected send_back checkpoint re-dispatch; attempt_count++)`; its two legitimate callers are dispatch's retry path and dispatch's run-completion reconciler (checkpoint-seam delta §6, 2026-07-09); → `draft` (unqueue): `queued_at=NULL`; → `running`: `dispatched_at=now()`, `pipeline_run_id` from meta; → `needs_review`: `branch` from meta when non-empty — TWO writers: harvest (primary, per 09-harvest-step) and dispatch's run-completion reconciler (backup/safety net, empty meta); → `merged`/`merged_with_fixes`/`sent_back`/`abandoned`/`concluded`/`failed`/`errored`: `completed_at=now()`, plus `error_detail` from meta on `errored`. The `needs_review → concluded` edge (flow-decoupling delta, 2026-07-09, per FLOWS.md §3 spike-research / §4 state-enum decision) is TERMINAL — "run finished, human reviewed, no merge intended" — the positive sibling of `abandoned`, reachable ONLY via explicit human disposition from `needs_review`, no outgoing edges; it lands in the frozen enum NOW (pre-freeze) so no later migration is needed. The UPDATE is guarded by `WHERE id=$id AND state=$from`; zero rows updated resolves to `ErrTicketNotFound` (row gone) or `ErrStaleTransition` (state changed concurrently). `Store.Create` always inserts `state='draft'`; queueing is a separate Transition call (single-writer discipline).
  - §2.1/§3.2 (affects: platform-mcp-hitl): the `GetAgentTicket` response body is exactly `tickets.TicketDetail` JSON — `{"ticket": <§2.1 Ticket>, "spec": <latest Spec or null>, "tasks": [<active-plan Task>...]}` — the payload `read_ticket` returns verbatim in wave 3. `TransitionAgentTicket` returns 409 on `ErrInvalidTransition`/`ErrStaleTransition`, 404 on missing ticket. `CreateAgentTicket` origin rules: principal writes may only create `origin:"retrospective"`; human writes may create `web`/`fly`; `jira` is rejected (400) until the phase-2 sync component exists.
  - §4.1 (affects: platform-mcp-hitl, delivery-outcomes): combined route tiers ("authorized member (main); also principal(<scope>)") are implemented by a new composition helper owned by ticket-core: `atc/api/auth.AgentPrincipalOrMainTeamHandler(principalTier, mainTeamTier http.Handler) http.Handler` — dispatches on the `cap1.` bearer-token prefix: cap1 tokens go to the principal tier (`CheckAgentPrincipalHandlerFactory.HandlerFor`), everything else to `CheckAgentAuthorizationHandler`. platform-mcp-hitl reuses it for `GetAgentQuestion`/`AnswerAgentQuestion` in wave 3.
  - Render helper (affects: dispatch) — DECLARED, NOT YET LANDED: `tickets.RenderSpecMarkdown(t Ticket, spec *Spec) []byte` and `tickets.RenderPlanMarkdown(t Ticket, tasks []Task) []byte` produce the deterministic read-only `spec.md`/`plan.md` workspace inputs dispatch materializes at render time. Plan task glyphs: `[ ]` pending, `[~]` in_progress, `[x]` done, `[-]` skipped, `[!]` blocked. Deferred from the 2026-07-17 core slice (plan 06 Task 9) to land with dispatch's consumer; the signatures here stay frozen.
  - Coordination note (affects: agent-step, wave-mate) — RESOLVED at execution time: agent-step landed first (`1773106060`/`1773106061`); ticket-core landed second and took `1773106062–64` per the renumber entry above. The standing merge rule is unchanged for future blocks: `jetbridgeHeadMigration` is always the highest landed migration number; whoever merges second keeps the larger value.
- 2026-07-17 (manual-dispatch slice — plan 11 MILESTONE 1 + the manual-trigger core of MILESTONE 2, pulled forward; owner-approved same day): `agent/dispatch` lands with `RenderInput`, `RenderAgentStep`, `Render` (workflow.Config → `template: true` pipeline: one entry job `run`, optional `repo` git resource from `--agent-repo-base-url` + ticket slug, a `write-ticket` busybox task materializing `tickets.RenderSpecMarkdown/RenderPlanMarkdown` output as the read-only `ticket` artifact via base64, then the agent steps with §8.1 identity env — `AGENT_TICKET_ID` literal, `AGENT_PIPELINE_RUN_ID` = `((run_id))` per F30), `DispatchOne(Deps, ticketID, dispatchedBy)` (claim QUEUED → resolve + FREEZE workflow version onto the ticket → render → `SaveTemplate` as `agent-ticket-<id>` on main, update-in-place on re-dispatch → `CreateRun` → `Transition(queued→running, PipelineRunID)`; failures pre-transition leave the ticket queued), and `NewTeamTemplateSaver`. **v0 REFUSALS (render-time errors, not deferred silently):** step sidecars, checkpoints, `spec_delivery` `mcp`/empty (files only), harvest steps unsupported — all wave-3 surfaces. **DEFERRED AS A SET:** the `Dispatcher` RunnableComponent loop, budget admission, the run-completion reconciler (plan 11 Tasks 8/11/11b — they share wiring; manual close-out via `fly agent tickets transition` until then), and per-run principal minting/secret attach (rendered steps ride the same platform-level credential path as `agent-review-native`). New route `DispatchAgentTicket` `POST /api/v1/agent/tickets/:ticket_id/dispatch` — §4.2 tier "authorized member (main)", DELIBERATELY no principal tier: the human trigger IS the budget gate while admission is deferred (this supersedes plan 11's "no new HTTP routes" scope note FOR THE SLICE; the loop later calls `DispatchOne` in-process exactly as planned). Wire type `tickets.DispatchResponse{run_id, pipeline_name}`; client methods `TransitionAgentTicket`/`DispatchAgentTicket`; fly `agent tickets queue/transition/dispatch`. **§2.8 fix (affects agent-step, dispatch, process-intel-experiments):** `atc.AgentStep.Env` changes type `map[string]string` → `TaskEnv` (identical underlying map; custom unmarshal coerces scalars) — CreateRun materialization interpolates a standalone `((run_id))` as a JSON NUMBER, which the plain string map failed to unmarshal; found by the DB-backed dispatch spec (`atc/db/agent_dispatch_test.go`). `--agent-repo-base-url` web flag (default `https://github.com`; anonymous clones only until harvest's git-cred machinery). **§6 import-gate relaxation (affects workflow-store):** `workflow.Parse`'s produced-inputs check now seeds the reserved renderer-provided artifacts `repo` and `ticket` — steps may consume them without an earlier producer (the wave-1 gate predated the renderer and rejected every files-delivery workflow).
- 2026-07-17 (harvest v0 — the deliverable-unstranding core of plan 09, pulled forward; owner-approved): the `harvest:` step type EXISTS with the full §2.8.1 schema (`atc.HarvestStep`/`HarvestPlan`, StepPrecedence before `run`, all visitors) and a v0 execution: `harvest-runner` (built into the agent-step image alongside agent-runner) verifies the workspace is a COMMITTED clean git tree (F33: dirty ⇒ `fail`, nothing pushed, nothing auto-discarded) and pushes head-sha `--force-with-lease` to the stable `agent/ticket-<id>` branch with creds from the §8.3 `agent-harvest-git-<slug>` secret (keys: `token`, optional `username`; GIT_ASKPASS delivery — the token never reaches argv/logs). New runtime seam `runtime.ContainerSpec.SecretMounts` (whole-secret read-only volume, MAIN container only — spec-pinned that sidecars never receive it). `exec.HarvestStep` owns the §2.1 transition through the single writer (exit 0/1 → `needs_review`, branch recorded on 0+push; 2 → `errored`), GUARDED by the same `RunBelongsToPipeline`+`TicketBelongsToRun` linkage as the agent step (plan-carried ids are never trusted raw; unverifiable ⇒ transition skipped loudly). Dispatch's renderer emits the terminal harvest for every ticketed run (workspace convention: the agent commits its work INTO the workspace git checkout). **v0 REFUSALS (loud, both exec- and runner-side):** `gate_policy`/`judge`/`dev_mcp` configs error — the gate engine, judge, reviews/feedback ticket linkage (plan 09 Tasks 2–4/8/9), flight-recorder evidence (`manifest.json`/`review.json`/metrics upsert), and the Elm build-page rendering remain the full harvest-step workstream. When plan 09 executes, its tasks amend this landed core rather than green-fielding.
- 2026-07-17 (manual-dispatch dogfood fixes — findings from a 5-ticket live run, tickets #4–8): (a) `dispatch.Render` now REFUSES declared-but-unenforced workflow policy blocks (`gate_policy`/`hitl`/`judge`), matching the sidecar/checkpoint loud-fail pattern — these validate at import and get content-hashed as authoritative but v0 has no consumer (harvest-step/platform-mcp own them, wave 3), so silently dropping them misled workflow authors (affects: workflow-store — a definition that imports may still be refused at dispatch; harvest-step/platform-mcp-hitl will relax this when their consumers land). (b) `fly agent tickets` UX (CLI-only, no route/contract/state-machine change, single-writer discipline preserved): `transition --from` optional (server-read default; still sent as the from-guard, still 409s on a concurrent mover), `show`/`list` surface the deterministic `agent-ticket-<id>` run, new `watch` (streams the run's build events via the existing `GetBuild`+`BuildEvents` path), `create --queue`/`--dispatch` (each step independently validated, reports which failed), new `close` (running→needs_review→disposition, re-reading state between hops because running→needs_review has two writers). Deferred dogfood findings NOT fixed here (logged for their owners): `Ticket.UserID` never populated so all v0 spend attributes to the platform service user (dispatch wave-4 user resolution), and the per-run principal token is minted but has no consumer until wave 3 (both are known seams, not regressions).
- 2026-07-17 (ticket-core same-day fix — agent-review-native #7 proven finding): §2.1 `Store` gains ONE additive method, `UpdateActiveTask(ticketID, ordering int, status TaskStatus, note string) (planVersion int, err error)` — atomically resolves the ACTIVE plan version and applies the status update plus optional note append in one store operation (DB factory: single tx holding the ticket's FOR UPDATE row lock, the same lock SubmitPlan takes, so plan replacement serializes with task updates; MemoryStore: same under its mutex). The `UpdateAgentTicketTask` handler now calls it INSTEAD of the read-version-then-write pair `ActivePlan`+`UpdateTaskStatus`(+`AppendTaskNote`), which had a TOCTOU window: a concurrent `SubmitPlan` between the read and the write made the status update land silently on the superseded plan_version (200 OK, change invisible — a lost update, deterministically reproduced by `agent/api/tickets/task_race_test.go`). §3.2 `update_task_status` tool input/behavior unchanged (affects: platform-mcp-hitl — it simply rides the fixed route). The versioned `UpdateTaskStatus`/`AppendTaskNote` methods remain for explicit-version callers. Also: `CreateAgentTicket`/`UpdateAgentTicket` now 400 a negative `budget_usd` (reviewer observation; enforcement beyond validation still deferred to dispatch).
- 2026-07-18 (harvest v0.5 — gates pre-verify before push, ticket #14; relaxes the 2026-07-17 harvest v0 and manual-dispatch-dogfood entries above rather than superseding them): `agent/harvest.RunGates(policy, workspaceDir)` lands as an in-pod gate engine — v0.5 command map (fixed, documented as the interim executor until dev-mcp owns per-repo commands in wave 3): `build` → `go build ./...`, `test` → `go test ./...`, `lint` → `go vet ./...`, cwd=workspaceDir. Wired into `harvest-runner`'s `Run()` between the worktree-cleanliness check and the push: an unknown gate name or any scope OTHER THAN `full` errors that gate (tooling fault — `affected`/`affected_then_full` still name dev-mcp as the waiting wave-3 executor, loud, never silent); any FAILED gate ⇒ results `fail`, exit 1, NO push, no branch (§2.8.1: branch recorded on `ok` only); any ERRORED gate ⇒ `error`, exit 2. §6.3 flake stance implemented exactly: `retries` 0-2 failed-only re-runs (an `error` result is never retried), a pass-on-retry records `ok` with `flaky:true`/`attempt:N` — flakiness surfaced, never hidden. Gate outcomes ride `Results.Metadata.Gates []GateOutcome{Gate,Scope,Status,Attempt,Flaky,DurationSeconds,Detail}`. `exec.HarvestStep` now admits a plan whose every gate has scope `full` (any other scope still refused at admission, before a pod is even scheduled) and populates `HARVEST_CONFIG`'s `GatePolicy` from the plan; `judge`/`dev_mcp` remain fully refused (unchanged, still wave 3). `dispatch.Render` relaxes the same boundary: a workflow `gate_policy` renders when every gate is scope `full` and `on_gate_failure` is empty or `needs_review`, converting `workflow.GatePolicy`→`harvest.GatePolicy` onto the emitted harvest step; `affected`/`affected_then_full` gates, `hitl`, and `judge` still refuse with the existing wording (Fix-A boundary re-pinned, not weakened). `agent/workflow.parse`'s `validGateScopes` (affected|full|affected_then_full) is UNCHANGED — import-side validation still accepts all three; only the render/exec/runner enforcement boundary moved. dev-mcp remains the wave-3 executor for affected-scope gates; the judge and `agent_reviews`/`agent_feedback` linkage are untouched by this slice.
- 2026-07-18 (workflow source format slice b — skills/system-prompt/context materialization, per `docs/superpowers/specs/2026-07-17-workflow-source-format-and-skills-design.md` §4; relaxes the 2026-07-17 manual-dispatch-dogfood refusal entry's scope for these three surfaces): §2.8 `atc.AgentStep`/`AgentPlan` gain `SystemPrompt`, `Context`, `Skills []string` (renderer-resolved literals, like `Prompt`); §8.1 gains `AGENT_SYSTEM_PROMPT`, `AGENT_CONTEXT`, `AGENT_SKILLS`, `AGENT_SKILLS_DIR` (rows above). The renderer emits a `write-skills` busybox task (base64, same mechanism as `write-ticket`) producing the reserved `skills` artifact holding the union of referenced skill trees (≤512 KiB rendered; larger sets refuse and name the future fetch-by-version endpoint), auto-appends the `skills` input to skill-bearing steps, and resolves per-step effective layers (skills/context additive workflow∪step; step system_prompt replaces the workflow layer). `workflow.Parse`'s reserved renderer-provided artifacts grow to `repo`/`ticket`/`skills`. agent-runner materializes selected skills into `<workdir>/.claude/skills/` (claude project skills — CWD is the workdir, so the workspace git tree stays clean for harvest's F33 check), prepends `AGENT_CONTEXT` under a `# Workflow context` header, and passes `AGENT_SYSTEM_PROMPT` via `--append-system-prompt`. Trust boundary unchanged: harvest consumes none of these fields and never mounts the skills artifact. The slice-(a) render refusal for skills/system_prompt/context is REMOVED (gate_policy-beyond-full/hitl/judge refusals unchanged).
- 2026-07-18 (ticket #16 finding, build 567384 — resolve-once output paths): agent-runner now SURFACES the §8.1 `AGENT_OUTPUT_<NAME>` rows in the prompt itself — a "# Step outputs (platform-resolved absolute paths)" block (`$AGENT_OUTPUT_<NAME> = <abs path>` per declared output, `AGENT_OUTPUT_SCHEMA` excluded) inserted between the workflow-context block and the step prompt. This amends the 2026-07-16 entry's "agent-runner ignores it (prompt-consumed only)" note: the rows are still exec-set literals and prompts remain the consumer, but the runner now also inlines them so agents work from in-transcript literals instead of expanding the env var in every shell call. Rationale: ticket #16's develop-fable agent had `"$AGENT_OUTPUT_WORKSPACE"` expand EMPTY in a later shell call and `cp -a repo/. "$AGENT_OUTPUT_WORKSPACE/"` copied the checkout onto `/` (the agent noticed and recovered). Investigation cleared both propagation layers — the runner starts claude with its full inherited env (nil `cmd.Env`), and claude-code 2.0.1 spawns every Bash call with `{...process.env}` into a fresh shell (verified statically against the pinned bundle and dynamically: a 4-call probe with an interposed `unset` showed the var present in every call and shell state not persisting) — so the failure is agent/session-level env-expansion unreliability, and the fix removes the dependence on it. Companion prompt change: the live `develop` (v2) and `develop-fable` (v3) workspace protocols now pin the platform-resolved literal ("resolve ONCE, reuse the literal, NEVER re-expand"), seeds recorded in `agent/workflow/seeds/develop{,-fable}.yaml`.
