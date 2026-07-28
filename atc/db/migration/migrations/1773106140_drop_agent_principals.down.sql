-- Restore only the historical table shape for a schema downgrade. Deliberately
-- do not restore any row, sentinel, token, or usable authority.
CREATE TABLE agent_principals (
    id           SERIAL PRIMARY KEY,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    token_prefix TEXT NOT NULL,
    token_hash   TEXT NOT NULL,
    scopes       TEXT[] NOT NULL DEFAULT '{}',
    team_name    TEXT NOT NULL DEFAULT 'main',
    created_by   TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX agent_principals_token_hash ON agent_principals (token_hash);
CREATE INDEX agent_principals_name ON agent_principals (name);
