-- Recreates the table exactly as 1773106104 defined it. Rows are not
-- recoverable — the up migration drops them.
CREATE TABLE agent_ticket_comments (
    id          BIGSERIAL PRIMARY KEY,
    ticket_id   INTEGER NOT NULL REFERENCES agent_tickets (id) ON DELETE CASCADE,
    revision    BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    body        TEXT NOT NULL CHECK (btrim(body) <> ''),
    created_by  TEXT NOT NULL CHECK (btrim(created_by) <> ''),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    answer      TEXT NOT NULL DEFAULT '',
    answered_by TEXT NOT NULL DEFAULT '',
    answered_at TIMESTAMPTZ,
    CHECK (
        (answered_at IS NULL AND answer = '' AND answered_by = '')
        OR
        (answered_at IS NOT NULL AND btrim(answer) <> '' AND btrim(answered_by) <> '')
    )
);

CREATE INDEX agent_ticket_comments_ticket
    ON agent_ticket_comments (ticket_id, created_at, id);
