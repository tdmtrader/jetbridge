-- Restores the SHAPE the older binary needs: the wide state CHECK, the
-- error_detail column, and the two content tables as 1773106063/1773106064
-- defined them. The folded-away values are gone — a ticket closed under the
-- narrow vocabulary stays 'closed', and spec/task rows are not recoverable
-- (their prose now lives in agent_tickets.body).

ALTER TABLE agent_tickets DROP CONSTRAINT agent_tickets_state_check;

ALTER TABLE agent_tickets
    ADD CONSTRAINT agent_tickets_state_check
    CHECK (state IN ('draft', 'queued', 'running', 'needs_review',
                     'merged', 'merged_with_fixes', 'sent_back',
                     'abandoned', 'concluded', 'failed', 'errored', 'closed'));

ALTER TABLE agent_tickets
    ADD COLUMN error_detail TEXT NOT NULL DEFAULT '';

CREATE TABLE agent_ticket_specs (
    id                  SERIAL PRIMARY KEY,
    ticket_id           INTEGER NOT NULL REFERENCES agent_tickets (id) ON DELETE CASCADE,
    version             INTEGER NOT NULL DEFAULT 1,
    title               TEXT NOT NULL,
    body                TEXT NOT NULL DEFAULT '',
    acceptance_criteria JSONB NOT NULL DEFAULT '[]',
    links               JSONB NOT NULL DEFAULT '[]',
    submitted_by        TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (ticket_id, version)
);

CREATE TABLE agent_ticket_tasks (
    id           SERIAL PRIMARY KEY,
    ticket_id    INTEGER NOT NULL REFERENCES agent_tickets (id) ON DELETE CASCADE,
    plan_version INTEGER NOT NULL DEFAULT 1,
    ordering     INTEGER NOT NULL,
    title        TEXT NOT NULL,
    detail       TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending','in_progress','done','skipped','blocked')),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (ticket_id, plan_version, ordering)
);

CREATE INDEX agent_ticket_tasks_ticket ON agent_ticket_tasks (ticket_id, plan_version, ordering);
