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
