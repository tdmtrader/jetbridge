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
| 1773106030–39 | pipeline-runs | `pipeline_runs`, pipelines template columns |
| 1773106040–49 | workflow-store | `agent_workflow_definitions` |
| 1773106050–59 | ticket-core | `agent_tickets`, `agent_ticket_specs`, `agent_ticket_tasks` |
| 1773106060–69 | agent-step | `agent_run_metrics` |
| 1773106070–79 | platform-mcp-hitl | `agent_run_questions` |
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

**Completion contract** (the part every consumer depends on): the lifecycle component (`atc.ComponentPipelineRunLifecycler`, polling+notify) marks a run complete when the instanced pipeline has **no builds in `pending` or `started`** and at least one entry job has run. Aggregate status is worst-of over the latest build of every job that ran (`errored > aborted > failed > succeeded`). **A parked step (`ask_human` / checkpoint) keeps its build `started`, therefore a parked run counts as `running`** — parking never completes a run. In-flight aborts: when every remaining build is aborted/finished, the run completes with `aborted` if any latest build was aborted and none errored.

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
    UNIQUE (name, version)
);

CREATE UNIQUE INDEX agent_workflow_definitions_live ON agent_workflow_definitions (name) WHERE live;
CREATE UNIQUE INDEX agent_workflow_definitions_hash ON agent_workflow_definitions (name, content_hash);
```

At most one live version per name (partial unique index). Importing byte-identical YAML is an idempotent no-op (hash unique index). Definitions are **immutable once created** — edits create a new version.

### 1.7 Ticket tables — owner: **ticket-core**; consumers: platform-mcp-hitl, dispatch, harvest-step, delivery-outcomes, process-intel-experiments

`1773106050_create_agent_tickets.up.sql`:

```sql
CREATE TABLE agent_tickets (
    id                     SERIAL PRIMARY KEY,    -- ticket number; branch is agent/ticket-<id>
    title                  TEXT NOT NULL,
    body                   TEXT NOT NULL DEFAULT '',  -- markdown problem statement
    state                  TEXT NOT NULL DEFAULT 'draft'
                           CHECK (state IN ('draft','queued','running','needs_review',
                                            'merged','merged_with_fixes','sent_back',
                                            'abandoned','failed','errored')),
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
draft → queued → running → needs_review → merged | merged_with_fixes | sent_back | abandoned
draft → abandoned
queued → draft (unqueue) | abandoned
running → queued (retryable platform error, attempt_count++) | failed | errored | needs_review
needs_review → queued (re-dispatch after send-back edits)
sent_back → queued
failed | errored → queued (manual retry)
```

`1773106051_create_agent_ticket_specs.up.sql`:

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

`1773106052_create_agent_ticket_tasks.up.sql`:

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
                        CHECK (merge_state IN ('open','merged','merged_with_fixes','closed_unmerged')),
    merged_sha          TEXT NOT NULL DEFAULT '',  -- default-branch commit from which pushed_sha became reachable
    merged_at           TIMESTAMPTZ,
    human_commit_count  INTEGER NOT NULL DEFAULT 0,   -- commits on the branch after pushed_sha, non-agent author
    human_lines_added   INTEGER NOT NULL DEFAULT 0,   -- human-touch delta: numstat of those commits
    human_lines_deleted INTEGER NOT NULL DEFAULT 0,
    disposition         TEXT NOT NULL DEFAULT ''
                        CHECK (disposition IN ('','sent_back','abandoned')),
    disposition_reason  TEXT NOT NULL DEFAULT ''
                        CHECK (disposition_reason IN ('','wrong_approach','incomplete','defective',
                                                      'superseded','not_needed','style','other')),
    disposition_notes   TEXT NOT NULL DEFAULT '',
    disposed_by         TEXT NOT NULL DEFAULT '',
    last_checked_at     TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (ticket_id)
);
```

**Human-touch delta definition [DECIDED HERE]:** the sum of `git diff --numstat <pushed_sha>..<branch-head-at-merge>` restricted to commits whose author is not the platform's git identity (`concourse-agent[bot]` — see §8.3). Merge commits themselves are excluded (first-parent walk). Computed once by the outcome watcher when `merge_state` transitions to merged; `merged_with_fixes` ⇔ `human_commit_count > 0`.

Explicit dispositions (`sent_back`/`abandoned`) live here (with the reason taxonomy); the ticket `state` mirrors them via the transition function.

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
	Disposition       string     `json:"disposition,omitempty"`        // sent_back | abandoned
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
	// Attach creates secret agent-run-<runID> in the worker namespace with
	// the §8.2 keys and returns its name. Idempotent per runID.
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

---

## 3. MCP tool schemas

All three sidecars are MCP servers speaking **streamable HTTP** (single `POST /mcp` endpoint, `application/json`; SSE streaming for long calls) on fixed localhost ports (§8.1). Tool registration style follows `atc/api/mcpserver` (`AddTool(name, description, jsonSchema, handler)`). Every tool result is a single JSON object; long-running tools emit MCP `notifications/progress` while running. **[DECIDED HERE: streamable HTTP (not stdio) so platform code — the harvest step's Go client — can call the same endpoint agents use, and so sidecars survive agent-process restarts.]**

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
Streaming semantics: implementations MUST emit `notifications/progress` at least every 30s during long runs (message = current suite/package) so callers can distinguish "slow" from "hung"; the harvest client applies a per-gate timeout (§6.3), not a transport timeout.

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

**`read_ticket`** — input: `{ "type": "object", "properties": {}, "additionalProperties": false }`
result:
```json
{
  "type": "object",
  "required": ["ticket"],
  "properties": {
    "ticket": { "$ref": "#/defs/Ticket (§2.1 JSON shape)" },
    "spec":   { "description": "latest spec (title, body, acceptance_criteria, links) or null" },
    "tasks":  { "type": "array", "description": "active plan tasks with status" }
  }
}
```

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

**Park/resume protocol [DECIDED HERE]:** `ask_human` inserts an `agent_run_questions` row, fires the notification (§8.4), and then **blocks the MCP call** while the sidecar long-polls `GET /api/v1/agent/tickets/:id/questions/:qid` until `answered_at` is set. The agent process is idle-waiting on a tool call — the jetbridge supervisor's existing resume semantics keep the pod alive across web restarts, and the pipeline-run completion contract (§1.5) counts the build as running. Timeout behavior comes from the workflow definition's `hitl` block (§6), resolved at render time into sidecar env (`PLATFORM_MCP_ASK_TIMEOUT_SECONDS`, `PLATFORM_MCP_ASK_TIMEOUT_POLICY`). **Timeout resolution [DECIDED HERE]:** the sidecar itself enforces the timeout around its long-poll; on expiry it is the writer that resolves the question row, via `AnswerAgentQuestion` with its principal token (scope `questions:answer`, §4.1) — no human is involved. Policy `default`: it submits `answer` = the call's `default` field (persisted as `agent_run_questions.default_answer` at insert time) with `answered_by` = its principal name, then returns the ask_human result with `timed_out: true`. Policy `fail`: it resolves the row the same way with an empty answer (so the open-questions index and ticket UI release) and fails the `ask_human` call at the MCP layer, failing the step. Policy `park`: no timeout (`timeout_seconds` 0); the row is only ever resolved by a human. Either way a timed-out row never stays open. Checkpoints use the same mechanism: the rendered pipeline inserts a dedicated checkpoint step that calls the sidecar's internal `checkpoint` endpoint with `kind=checkpoint` and blocks until approved (reject ⇒ step fails ⇒ run fails ⇒ ticket `needs_review`).

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
| `step.start` | agent-step exec / harvest exec | `{"step_name", "build_id", "plan_id", "ticket_id?", "workflow_name?", "workflow_version?", "workflow_hash?", "budget_slice_usd?"}` |
| `step.end` | same | `{"step_name", "status": "ok\|failed\|error", "summary", "wall_time_seconds", "cost_usd", "turns"}` |
| `gate.start` | harvest step | `{"gate": "build\|test\|lint", "component": "", "scope": "affected\|full"}` |
| `gate.result` | harvest step | `{"gate", "component", "scope", "status": "ok\|failed\|error", "duration_seconds", "summary", "log_artifact?"}` |
| `subagent.call` | gateway sidecar | `{"call_id", "tool": "request_review\|ask_agent", "provider", "model", "prompt_chars"}` |
| `subagent.result` | gateway sidecar | `{"call_id", "status", "provider", "model", "input_tokens", "output_tokens", "turns", "cost_usd", "duration_ms", "finding_count?"}` |
| `cost.record` | anything that spends money | mirrors `budget.LedgerEntry` (§2.7): `{"source", "provider", "model", "input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens", "turns", "cost_usd"}` |
| `budget.warn` | gateway / agent-step | `{"scope": "step\|ticket\|daily", "limit_usd", "spent_usd", "remaining_usd"}` (emitted at 80%) |
| `budget.stop` | gateway / agent-step | same payload; the call/step was cut off |
| `human.ask` | platform-mcp sidecar | `{"question_id", "kind": "question\|checkpoint", "question", "options"}` |
| `human.answer` | platform-mcp sidecar | `{"question_id", "answer", "answered_by", "wait_seconds", "timed_out"}` |
| `checkpoint.wait` | checkpoint step | `{"question_id", "checkpoint": "<name from workflow def>"}` |
| `checkpoint.release` | checkpoint step | `{"question_id", "approved": bool, "answered_by"}` |
| `judge.score` | harvest step | `{"rubric_hash", "dimensions": [{"name", "score", "max", "rationale"}], "total", "max_total", "model", "cost_usd"}` |
| `push.done` | harvest step | `{"branch", "sha", "manifest_artifact"}` |

Rules: `data` keys are snake_case; producers may add keys, never repurpose them; consumers must ignore unknown keys and unknown event types (forward compat). Every event stream for a step MUST begin with `step.start` and end with `step.end` — ingestion (§1.8 `event_counts`) treats a stream missing `step.end` as `status: error`.

---

## 6. Workflow-definition YAML schema — owner: **workflow-store**; consumers: dispatch (renderer), agent-step, harvest-step, platform-mcp-hitl, scorecards

Evolved from `ci-agent/phaseconfig.Config` (name/env/mcp/steps/scoring); parsed by `agent/workflow.Parse` with `phaseconfig`-style eager validation. The raw bytes are the hashed provenance unit.

```yaml
# agent_workflow_definitions.definition — schema_version 1
schema_version: 1
name: standard-dev            # must match agent_workflow_definitions.name on import
description: spec -> plan -> implement -> review loop, single agent

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
  spec: |                     # Go text/template; render context: .Ticket .Spec .Tasks .Params
    Read the ticket via platform-mcp read_ticket, explore the repo, then
    submit a spec with submit_spec. ...
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

Fly: `fly -t t run-pipeline -p <template> -v key=val [-v ...]` (struct field + command file per the fly recipe); `fly runs -p <template>` lists runs.

---

## 8. Env-var / K8s-secret credential-injection contract — owners: **credentials-and-budgets** (secrets + helper), **agent-step** (step env), **dev-mcp** (ports/packaging); consumers: dispatch, gateway-mcp, harvest-step, platform-mcp-hitl

### 8.1 Env vars in the agent step's main container and sidecars

Set by the agent-step exec implementation (values resolved at render/dispatch time; literal in the pod spec except secret refs):

| Env var | Container(s) | Source | Meaning |
|---|---|---|---|
| `CLAUDE_CODE_OAUTH_TOKEN` | main, gateway | secret `agent-run-<run-id>`, key `anthropic-token` | triggering user's vaulted token (spec-verified headless var) |
| `AGENT_PRINCIPAL_TOKEN` | platform, gateway, harvest | secret `agent-run-<run-id>`, key `principal-token` | per-run scoped principal token (§1.2), minted by dispatch with run-appropriate scopes, `expires_at` = now + run timeout |
| `ATC_EXTERNAL_URL` | all | literal | ATC base URL (name matches existing ci-agent publish contract) |
| `BUILD_ID` | all | literal (jetbridge already injects) | concourse build id |
| `AGENT_TICKET_ID` | all | literal | ticket id; empty for pure-CI agent steps |
| `AGENT_PIPELINE_RUN_ID` | all | literal | pipeline_runs.id |
| `AGENT_STEP_NAME` | main | literal | step name from the plan |
| `AGENT_WORKFLOW_NAME` / `AGENT_WORKFLOW_VERSION` / `AGENT_WORKFLOW_HASH` | main | literal | provenance tags for metrics/events |
| `AGENT_BUDGET_SLICE_USD` | main, gateway | literal | step's slice (§2.7); gateway enforces |
| `DEV_MCP_URL` | main | literal `http://127.0.0.1:7780/mcp` | |
| `PLATFORM_MCP_URL` | main | literal `http://127.0.0.1:7781/mcp` | |
| `GATEWAY_MCP_URL` | main | literal `http://127.0.0.1:7782/mcp` | |
| `PLATFORM_MCP_ASK_TIMEOUT_POLICY` / `_SECONDS` | platform | literal from workflow `hitl` block | §3.2 |

**[DECIDED HERE: fixed localhost ports 7780 (dev), 7781 (platform), 7782 (gateway).]** Pods are single-tenant so fixed ports are safe; every sidecar accepts `MCP_LISTEN_ADDR` to override; agents discover via the `*_MCP_URL` vars, never hardcode.

### 8.2 Ephemeral run secret

- **Name:** `agent-run-<pipeline_run_id>` in the jetbridge worker namespace.
- **Keys:** `anthropic-token` (decrypted user credential), `principal-token` (per-run principal).
- **Labels:** `concourse/agent-run: "<run-id>"`, `concourse/ticket: "<ticket-id>"` — the reaper's safety-net GC can find strays.
- **Lifecycle:** created by `credentials.SecretAttacher.Attach` (§2.6) during dispatch, before run creation; deleted by the pipeline-run lifecycle component on completion via `Cleanup` (plus a periodic sweep deleting labeled secrets whose run is complete — never notify-only, per fork lesson).
- **Consumption:** env `secretKeyRef` only — tokens never land in files, argv, or the DB in plaintext.

**Long-lived platform credential secret [DECIDED HERE]** — owner: **credentials-and-budgets**; consumers: harvest-step, process-intel-experiments:

- **Name:** `agent-platform-credential` in the jetbridge worker namespace.
- **Keys:** `anthropic-token` — the decrypted credential of the `agent-platform` service user's `agent_user_credentials` row (§1.13).
- **Lifecycle:** created and kept in sync by the credentials-and-budgets workstream: re-written on `Store.Put` for the platform user's row and on encryption-key rotation; long-lived (not per-run), so harvest pods need no dispatch-time secret plumbing for it.
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
- Each image: static Go binary (or provider CLIs for the gateway), `ENTRYPOINT` serves streamable HTTP MCP on `MCP_LISTEN_ADDR` (default `:778x` per role), exposes `GET /healthz` for the pod readiness probe, runs as non-root.
- CI job pattern: a job per image in the existing `cicd` pipeline on theborg — build with the repo's standard image-build task, run the contract-test kit against the built image (`docker run` + `contracttest.Run`), push on green. The dev-mcp workstream lands the first such job as the copyable template.

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
| events-results-shared-schema-and-run-metrics | §1.8, §2.4, §5 |
| harvest-terminal-step-schema-and-gate-policy | §1.10, §2.8, §6.3, §8.3 |
| judge-rubric-and-verdict-mapping | §6.4 |
| platform-mcp-tool-contract | §1.9, §3.2 |
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
11. dev-mcp result taxonomy `ok/failed/error` at payload level; MCP errors only for malformed input; progress notifications ≥ every 30s.
12. `ask_human` parks by blocking the MCP call over a long-poll route; checkpoints reuse the same table/mechanism with `kind=checkpoint`.
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

## 11. Amendment log

- 2026-07-07: initial freeze.
- 2026-07-08: review fixes applied before workstream planning kicked off (cross-workstream sign-off note):
  - Conventions bullet 2 / decision 2 (affects: agent-step, ci-agent): corrected the false claim that ci-agent already depends on the main module; `agent/schema` becomes its own nested stdlib-only module, consumed via `require`+`replace` from both sides.
  - §4.2 closing paragraph / new decision 21 (affects: agent-identity + every workstream with a team-less `authorized` route): corrected the false claim that existing agent feedback routes use main-team authorization — they are admin-only today; specified the new `CheckAgentAuthorizationHandler` mechanism.
  - §1.9 / §3.2 / §6 / new decision 22 (affects: platform-mcp-hitl, workflow-store, dispatch): added the missing default-answer carrier (`default` field on `ask_human` input) and the sidecar-driven timeout-resolution protocol.
  - §4.1 / §4.2 (affects: agent-identity, platform-mcp-hitl, pipeline-runs): `AnswerAgentQuestion` gains `principal(questions:answer)` for timeout resolution; `runs:create` removed from the scope vocabulary (no route granted it; run creation is in-process).
  - §8.2 / §8.3 / §1.13 / new decision 23 (affects: credentials-and-budgets, harvest-step, process-intel-experiments): defined the previously missing carrier for the platform Anthropic credential — long-lived `agent-platform-credential` secret.
