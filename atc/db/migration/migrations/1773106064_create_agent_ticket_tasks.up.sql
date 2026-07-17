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
