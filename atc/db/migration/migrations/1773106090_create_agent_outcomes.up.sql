CREATE TABLE agent_outcomes (
    id                  SERIAL PRIMARY KEY,
    ticket_id           INTEGER NOT NULL,          -- join key agent_tickets.id
    repo                TEXT NOT NULL,
    branch              TEXT NOT NULL,             -- agent/ticket-<id>
    pushed_sha          TEXT NOT NULL DEFAULT '',  -- branch head at harvest push time
    base_sha            TEXT NOT NULL DEFAULT '',  -- gate-diff base recorded by harvest (§1.11.1)
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

CREATE INDEX agent_outcomes_open ON agent_outcomes (merge_state) WHERE merge_state = 'open';
