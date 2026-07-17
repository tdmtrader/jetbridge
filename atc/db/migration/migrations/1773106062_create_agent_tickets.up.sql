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
    created_by             TEXT NOT NULL DEFAULT '',  -- audit attribution: principal name or username
    external_ref           TEXT NOT NULL DEFAULT '',  -- Jira phase-2 seam (issue key), '' = native
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
